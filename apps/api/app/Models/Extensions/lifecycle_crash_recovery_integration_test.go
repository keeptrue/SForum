package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresLifecycleCrashRecoveryAtEveryRecommendedGate(t *testing.T) {
	ctx, pool := openLifecycleCrashTestPool(t)
	operations := []LifecycleMachineOperation{
		LifecycleMachineInstall, LifecycleMachineEnable, LifecycleMachineDisable,
		LifecycleMachineUpgrade, LifecycleMachineRollback, LifecycleMachineUninstall,
	}
	for operationIndex, operationKind := range operations {
		path, err := RecommendedLifecyclePath(operationKind)
		if err != nil {
			t.Fatal(err)
		}
		// Index 0 is an already-complete, side-effect-free acquisition anchor;
		// the pure machine correctly forbids fabricating a crash at that position.
		for target := 1; target < len(path); target++ {
			terminal := LifecycleMachineFailedRun
			if (operationIndex+target)%2 == 1 {
				terminal = LifecycleMachineCancelled
			}
			step := path[target]
			name := fmt.Sprintf("%s/gate-%02d-%s-%s/%s", operationKind, target, step.State, step.Action, terminal)
			t.Run(name, func(t *testing.T) {
				extensionID := createLifecycleCrashTestExtension(t, ctx, pool, operationKind, target)
				repository := NewPostgresLifecycleRepository(pool)
				acquireInput := lifecycleCrashAcquireInput(extensionID, string(operationKind))
				if operationKind == LifecycleMachineUninstall {
					acquireInput.RemovalMode = LifecycleRemovalPreserve
				}
				acquired, err := repository.AcquireOperation(ctx, acquireInput)
				if err != nil {
					t.Fatal(err)
				}
				operation := acquired.Operation
				machine, err := NewLifecycleStateMachine(operationKind, false)
				if err != nil {
					t.Fatal(err)
				}
				machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
					State: machine.State, Action: machine.Action, CompleteStep: true,
					Progress: LifecycleProgressCursor{TotalUnits: uint64(len(path) - 1)},
				})
				completedStepIDs := make([]string, 0)

				for index := 1; index < target; index++ {
					gate := path[index]
					beginProgress := lifecycleCrashProgress(index-1, len(path)-1, index*2-1, "begin")
					machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
						State: gate.State, Action: gate.Action, Progress: beginProgress,
					})
					checkpoint := lifecycleCrashCheckpoint(index, "begin")
					operation = persistLifecycleCrashMachine(t, ctx, repository, operation, machine, checkpoint)
					if gate.Action != "" {
						stepID := lifecycleCrashStepID(operationKind, index)
						attempt := beginLifecycleCrashStep(t, ctx, repository, operation.ID, stepID, gate.Action, index)
						if _, err := repository.CompleteStepAttempt(ctx, CompleteLifecycleStepAttemptInput{
							AttemptID: attempt.ID, Status: LifecycleStepSucceeded,
							Checkpoint:     beginProgress.Checkpoint,
							CompletedUnits: int64(index), TotalUnits: int64(len(path) - 1),
							ResultDocument: json.RawMessage(`{"completed":true}`),
						}); err != nil {
							t.Fatal(err)
						}
						completedStepIDs = append(completedStepIDs, stepID)
					}
					completeProgress := lifecycleCrashProgress(index, len(path)-1, index*2, "complete")
					machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
						State: gate.State, Action: gate.Action, CompleteStep: true, Progress: completeProgress,
					})
					operation = persistLifecycleCrashMachine(
						t, ctx, repository, operation, machine, lifecycleCrashCheckpoint(index, "complete"),
					)
				}

				// Enter the target gate but crash before it completes.
				targetProgress := lifecycleCrashProgress(target-1, len(path)-1, target*2-1, "begin")
				machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
					State: step.State, Action: step.Action, Progress: targetProgress,
				})
				targetCheckpoint := lifecycleCrashCheckpoint(target, "crash")
				operation = persistLifecycleCrashMachine(t, ctx, repository, operation, machine, targetCheckpoint)
				var targetAttempt LifecycleStepAttempt
				if step.Action != "" {
					stepID := lifecycleCrashStepID(operationKind, target)
					targetAttempt = beginLifecycleCrashStep(t, ctx, repository, operation.ID, stepID, step.Action, target)
					targetAttempt, err = repository.UpdateStepProgress(ctx, UpdateLifecycleStepProgressInput{
						AttemptID: targetAttempt.ID, Status: LifecycleStepRunning,
						Checkpoint:     targetProgress.Checkpoint,
						CompletedUnits: int64(target - 1), TotalUnits: int64(len(path) - 1),
						Message: "crash injection",
					})
					if err != nil {
						t.Fatal(err)
					}
				}

				// A new process must acquire the same logical operation and exact checkpoint.
				restarted := NewPostgresLifecycleRepository(pool)
				reacquired, err := restarted.AcquireOperation(ctx, acquireInput)
				if err != nil || reacquired.Created || reacquired.Operation.ID != operation.ID {
					t.Fatalf("reacquire = %#v, err=%v", reacquired, err)
				}
				if !lifecycleJSONEqual(reacquired.Operation.Checkpoint, targetCheckpoint) {
					t.Fatalf("checkpoint changed across crash: got %s want %s", reacquired.Operation.Checkpoint, targetCheckpoint)
				}
				assertLifecycleCrashMachineDocument(t, reacquired.Operation.Progress, machine)

				stepStatus := string(terminal)
				if targetAttempt.ID != 0 {
					if _, err := restarted.CompleteStepAttempt(ctx, CompleteLifecycleStepAttemptInput{
						AttemptID: targetAttempt.ID, Status: stepStatus,
						Checkpoint:     targetAttempt.Checkpoint,
						CompletedUnits: targetAttempt.CompletedUnits, TotalUnits: targetAttempt.TotalUnits,
						Error: lifecycleCrashTypedError(terminal),
					}); err != nil {
						t.Fatal(err)
					}
				}
				machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
					State: LifecycleMachineFailed, Action: machine.Action,
					TerminalResult: terminal, Progress: machine.Progress,
				})
				operation = persistLifecycleCrashSnapshot(t, ctx, restarted, operation, machine)
				failed, err := restarted.CompleteOperation(ctx, CompleteLifecycleOperationInput{
					OperationID: operation.ID, ExpectedRevision: operation.Revision,
					ExpectedState: operation.State, State: LifecycleStateFailed,
					TerminalResult: string(terminal), Error: lifecycleCrashTypedError(terminal),
				})
				if err != nil {
					t.Fatal(err)
				}
				assertLifecycleCrashMachineDocument(t, failed.Progress, machine)

				// Resume reuses the same operation row and re-enters only its exact failed gate.
				resumed, err := restarted.ResumeOperation(ctx, ResumeLifecycleOperationInput{
					OperationID: failed.ID, ExpectedRevision: failed.Revision, ExpectedState: LifecycleStateFailed,
					Decision: LifecycleRecoveryRetry, ActorUserID: int64(1000 + target), AuditEventID: int64(2000 + target),
				})
				if err != nil {
					t.Fatal(err)
				}
				if resumed.ID != failed.ID || resumed.AttemptCount != failed.AttemptCount+1 ||
					resumed.State != LifecycleStateRecovery || !lifecycleJSONEqual(resumed.Checkpoint, targetCheckpoint) {
					t.Fatalf("resume lost operation identity/checkpoint: before=%#v after=%#v", failed, resumed)
				}
				machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
					State: LifecycleMachineRecovery, Retry: true, Progress: machine.Progress,
				})
				resumed = persistLifecycleCrashMachine(t, ctx, restarted, resumed, machine, nil)
				machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
					State: step.State, Action: step.Action, Progress: machine.Progress,
				})
				resumed = persistLifecycleCrashMachine(t, ctx, restarted, resumed, machine, nil)
				if resumed.State != string(step.State) || resumed.CurrentStepID != lifecycleCrashStepID(operationKind, target) {
					t.Fatalf("recovery jumped gate: operation=%#v target=%#v", resumed, step)
				}
				assertLifecycleCrashMachineDocument(t, resumed.Progress, machine)

				if step.Action != "" {
					retry := beginLifecycleCrashStep(
						t, ctx, restarted, resumed.ID, lifecycleCrashStepID(operationKind, target), step.Action, target,
					)
					if retry.Attempt != targetAttempt.Attempt+1 {
						t.Fatalf("retry attempt = %d, want %d", retry.Attempt, targetAttempt.Attempt+1)
					}
					if _, err := restarted.UpdateStepProgress(ctx, UpdateLifecycleStepProgressInput{
						AttemptID: retry.ID, Status: LifecycleStepRunning,
						Checkpoint:     targetProgress.Checkpoint,
						CompletedUnits: int64(target - 1), TotalUnits: int64(len(path) - 1),
					}); err != nil {
						t.Fatal(err)
					}
				}
				assertLifecycleCrashAttempts(t, ctx, restarted, resumed.ID, completedStepIDs, operationKind, target, step.Action != "")
			})
		}
	}
}

func TestPostgresLifecycleConcurrentRecoveryCASHasOneWinner(t *testing.T) {
	ctx, pool := openLifecycleCrashTestPool(t)
	extensionID := createLifecycleCrashTestExtension(t, ctx, pool, LifecycleMachineEnable, 1)
	repository := NewPostgresLifecycleRepository(pool)
	input := lifecycleCrashAcquireInput(extensionID, LifecycleOperationEnable)
	acquired, err := repository.AcquireOperation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	machine, _ := NewLifecycleStateMachine(LifecycleMachineEnable, false)
	path, _ := RecommendedLifecyclePath(machine.Operation)
	machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
		State: machine.State, Action: machine.Action, CompleteStep: true,
		Progress: LifecycleProgressCursor{TotalUnits: uint64(len(path) - 1)},
	})
	machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
		State: path[1].State, Action: path[1].Action,
		Progress: lifecycleCrashProgress(0, len(path)-1, 1, "begin"),
	})
	operation := persistLifecycleCrashMachine(t, ctx, repository, acquired.Operation, machine, lifecycleCrashCheckpoint(1, "crash"))
	machine = applyLifecycleCrashTransition(t, machine, LifecycleStateTransition{
		State: LifecycleMachineFailed, Action: machine.Action,
		TerminalResult: LifecycleMachineFailedRun, Progress: machine.Progress,
	})
	operation = persistLifecycleCrashSnapshot(t, ctx, repository, operation, machine)
	failed, err := repository.CompleteOperation(ctx, CompleteLifecycleOperationInput{
		OperationID: operation.ID, ExpectedRevision: operation.Revision, ExpectedState: operation.State,
		State: LifecycleStateFailed, TerminalResult: LifecycleTerminalFailed,
		Error: lifecycleCrashTypedError(LifecycleMachineFailedRun),
	})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	start := make(chan struct{})
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := repository.ResumeOperation(ctx, ResumeLifecycleOperationInput{
				OperationID: failed.ID, ExpectedRevision: failed.Revision, ExpectedState: LifecycleStateFailed,
				Decision: LifecycleRecoveryRetry, ActorUserID: 1001, AuditEventID: 2001,
			})
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	winners := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrLifecycleRevisionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent recovery error = %v", err)
		}
	}
	if winners != 1 || conflicts != callers-1 {
		t.Fatalf("recovery winners=%d conflicts=%d", winners, conflicts)
	}
	open, err := repository.OpenOperation(ctx, extensionID)
	if err != nil || open.State != LifecycleStateRecovery || open.AttemptCount != 2 || open.Revision != failed.Revision+1 {
		t.Fatalf("recovered operation = %#v, err=%v", open, err)
	}
	decisions, err := repository.ListRecoveryDecisions(ctx, failed.ID)
	if err != nil || len(decisions) != 1 || decisions[0].OperationAttempt != 2 ||
		decisions[0].ActorUserID != 1001 || decisions[0].AuditEventID != 2001 {
		t.Fatalf("concurrent recovery decisions = %#v, err=%v", decisions, err)
	}
}

func openLifecycleCrashTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func createLifecycleCrashTestExtension(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	operation LifecycleMachineOperation,
	gate int,
) string {
	t.Helper()
	extensionID := fmt.Sprintf("crash.integration.%s.%d.%d", operation, gate, time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system, is_deletable)
		VALUES ($1, 'plugin', 'Crash Recovery Integration', 'installed', 'builtin', true, false)
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_operations WHERE extension_id = $1`, extensionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, extensionID)
	})
	return extensionID
}

func lifecycleCrashAcquireInput(extensionID, operation string) AcquireLifecycleOperationInput {
	return AcquireLifecycleOperationInput{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("a", 64),
		ArtifactDigests: json.RawMessage(`{"backend":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		Operation:       operation, PlanVersion: "lifecycle.integration@1", IdempotencyKey: operation + ":crash",
		RequestFingerprint: strings.Repeat("b", 64), AuthorityType: LifecycleAuthorityBuiltin,
		AuthoritySnapshot: json.RawMessage(`{"source":"builtin"}`),
	}
}

func persistLifecycleCrashMachine(
	t *testing.T,
	ctx context.Context,
	repository *PostgresLifecycleRepository,
	operation LifecycleOperation,
	machine LifecycleStateMachine,
	checkpoint json.RawMessage,
) LifecycleOperation {
	t.Helper()
	document, err := json.Marshal(machine)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repository.TransitionOperation(ctx, TransitionLifecycleOperationInput{
		OperationID: operation.ID, ExpectedRevision: operation.Revision, ExpectedState: operation.State,
		State: string(machine.State), CurrentStepID: lifecycleCrashStepID(machine.Operation, machine.Position),
		Checkpoint: checkpoint, Progress: document,
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

// failed state and its typed error are committed atomically by CompleteOperation;
// this update only checkpoints the pure machine while retaining the current DB state.
func persistLifecycleCrashSnapshot(
	t *testing.T,
	ctx context.Context,
	repository *PostgresLifecycleRepository,
	operation LifecycleOperation,
	machine LifecycleStateMachine,
) LifecycleOperation {
	t.Helper()
	document, err := json.Marshal(machine)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repository.TransitionOperation(ctx, TransitionLifecycleOperationInput{
		OperationID: operation.ID, ExpectedRevision: operation.Revision, ExpectedState: operation.State,
		State: operation.State, CurrentStepID: lifecycleCrashStepID(machine.Operation, machine.Position), Progress: document,
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func beginLifecycleCrashStep(
	t *testing.T,
	ctx context.Context,
	repository *PostgresLifecycleRepository,
	operationID int64,
	stepID string,
	action LifecycleMachineAction,
	gate int,
) LifecycleStepAttempt {
	t.Helper()
	result, err := repository.BeginStepAttempt(ctx, BeginLifecycleStepAttemptInput{
		OperationID: operationID, StepID: stepID, LifecycleAction: string(action),
		PlanVersion:   "lifecycle.integration@1",
		InputDocument: json.RawMessage(fmt.Sprintf(`{"gate":%d}`, gate)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Attempt
}

func applyLifecycleCrashTransition(
	t *testing.T,
	machine LifecycleStateMachine,
	transition LifecycleStateTransition,
) LifecycleStateMachine {
	t.Helper()
	next, err := ApplyLifecycleTransition(machine, transition)
	if err != nil {
		t.Fatalf("apply crash transition %#v to %#v: %v", transition, machine, err)
	}
	return next
}

func lifecycleCrashProgress(completed, total, sequence int, phase string) LifecycleProgressCursor {
	return LifecycleProgressCursor{
		CompletedUnits: uint64(completed), TotalUnits: uint64(total),
		Checkpoint: fmt.Sprintf("checkpoint-%02d-%s", sequence, phase), CheckpointSequence: uint64(sequence),
	}
}

func lifecycleCrashCheckpoint(gate int, phase string) json.RawMessage {
	body, _ := json.Marshal(map[string]any{"gate": gate, "token": fmt.Sprintf("gate-%02d-%s", gate, phase)})
	return body
}

func lifecycleCrashStepID(operation LifecycleMachineOperation, gate int) string {
	return fmt.Sprintf("%s.gate.%02d", operation, gate)
}

func lifecycleCrashTypedError(terminal LifecycleMachineTerminal) LifecycleExecutionError {
	return LifecycleExecutionError{
		Code: string(terminal), Reason: "injected_crash", Message: "injected lifecycle crash", Retryable: true,
		Metadata: json.RawMessage(`{"source":"integration_test"}`),
	}
}

func assertLifecycleCrashMachineDocument(t *testing.T, document json.RawMessage, want LifecycleStateMachine) {
	t.Helper()
	var got LifecycleStateMachine
	if err := json.Unmarshal(document, &got); err != nil {
		t.Fatalf("decode persisted state machine: %v", err)
	}
	if got != want {
		t.Fatalf("persisted state machine = %#v, want %#v", got, want)
	}
}

func assertLifecycleCrashAttempts(
	t *testing.T,
	ctx context.Context,
	repository *PostgresLifecycleRepository,
	operationID int64,
	completedStepIDs []string,
	operation LifecycleMachineOperation,
	target int,
	targetHasAction bool,
) {
	t.Helper()
	attempts, err := repository.ListStepAttempts(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	byStep := make(map[string][]LifecycleStepAttempt)
	for _, attempt := range attempts {
		byStep[attempt.StepID] = append(byStep[attempt.StepID], attempt)
	}
	for _, stepID := range completedStepIDs {
		items := byStep[stepID]
		if len(items) != 1 || items[0].Attempt != 1 || items[0].Status != LifecycleStepSucceeded {
			t.Fatalf("completed step %s was repeated: %#v", stepID, items)
		}
	}
	if targetHasAction {
		stepID := lifecycleCrashStepID(operation, target)
		items := byStep[stepID]
		if len(items) != 2 || items[0].Attempt != 1 || items[1].Attempt != 2 {
			t.Fatalf("target step attempts %s = %#v", stepID, items)
		}
	}
}
