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

func TestPostgresLifecycleRepositoryConcurrentAcquireAndRecovery(t *testing.T) {
	ctx, pool, repository, extensionID := newLifecycleRepositoryIntegration(t)
	input := lifecycleAcquireTestInput(extensionID, LifecycleOperationEnable)

	const callers = 8
	start := make(chan struct{})
	results := make(chan AcquireLifecycleOperationResult, callers)
	errorsCh := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := repository.AcquireOperation(ctx, input)
			results <- result
			errorsCh <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent acquire: %v", err)
		}
	}
	var operation LifecycleOperation
	created := 0
	for result := range results {
		if result.Created {
			created++
		}
		if operation.ID == 0 {
			operation = result.Operation
		}
		if result.Operation.ID != operation.ID {
			t.Fatalf("same idempotency key returned operations %d and %d", operation.ID, result.Operation.ID)
		}
	}
	if created != 1 {
		t.Fatalf("created operations = %d, want 1", created)
	}

	changed := input
	changed.RequestFingerprint = strings.Repeat("f", 64)
	if _, err := repository.AcquireOperation(ctx, changed); !errors.Is(err, ErrLifecycleFingerprintConflict) {
		t.Fatalf("changed fingerprint error = %v", err)
	}
	other := input
	other.IdempotencyKey = "enable:other"
	other.RequestFingerprint = strings.Repeat("e", 64)
	if _, err := repository.AcquireOperation(ctx, other); !errors.Is(err, ErrLifecycleOperationInProgress) {
		t.Fatalf("second open operation error = %v", err)
	}

	casStart := make(chan struct{})
	casResults := make(chan error, 2)
	for index := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-casStart
			_, err := repository.TransitionOperation(ctx, TransitionLifecycleOperationInput{
				OperationID: operation.ID, ExpectedRevision: operation.Revision,
				ExpectedState: LifecycleStatePlanned, State: LifecycleStateStarting,
				CurrentStepID: "enable.execute",
				Checkpoint:    json.RawMessage(fmt.Sprintf(`{"worker":%d}`, index)),
				Progress:      json.RawMessage(`{"completed":0,"total":2}`),
			})
			casResults <- err
		}()
	}
	close(casStart)
	wait.Wait()
	close(casResults)
	casSucceeded := 0
	casConflicted := 0
	for err := range casResults {
		switch {
		case err == nil:
			casSucceeded++
		case errors.Is(err, ErrLifecycleRevisionConflict):
			casConflicted++
		default:
			t.Fatalf("unexpected CAS result: %v", err)
		}
	}
	if casSucceeded != 1 || casConflicted != 1 {
		t.Fatalf("CAS succeeded=%d conflicted=%d", casSucceeded, casConflicted)
	}
	operation, err := repository.OpenOperation(ctx, extensionID)
	if err != nil || operation.State != LifecycleStateStarting || operation.Revision != 2 {
		t.Fatalf("open operation = %#v, err=%v", operation, err)
	}

	stepInput := BeginLifecycleStepAttemptInput{
		OperationID: operation.ID, StepID: "enable.execute", LifecycleAction: "enable",
		PlanVersion: input.PlanVersion, InputDocument: json.RawMessage(`{"schema":"enable.input@1"}`),
		Checkpoint: "resume-1",
	}
	stepStart := make(chan struct{})
	stepResults := make(chan BeginLifecycleStepAttemptResult, 2)
	stepErrors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-stepStart
			result, err := repository.BeginStepAttempt(ctx, stepInput)
			stepResults <- result
			stepErrors <- err
		}()
	}
	close(stepStart)
	wait.Wait()
	close(stepResults)
	close(stepErrors)
	for err := range stepErrors {
		if err != nil {
			t.Fatalf("concurrent step begin: %v", err)
		}
	}
	var step BeginLifecycleStepAttemptResult
	stepCreated := 0
	for result := range stepResults {
		if result.Created {
			stepCreated++
		}
		if step.Attempt.ID == 0 {
			step = result
		}
		if result.Attempt.ID != step.Attempt.ID {
			t.Fatalf("same stable step returned attempts %d and %d", step.Attempt.ID, result.Attempt.ID)
		}
	}
	if stepCreated != 1 || step.Attempt.Attempt != 1 || step.Attempt.Checkpoint != stepInput.Checkpoint {
		t.Fatalf("step created=%d attempt=%#v", stepCreated, step.Attempt)
	}
	createdAt := step.Attempt.UpdatedAt
	progress, err := repository.UpdateStepProgress(ctx, UpdateLifecycleStepProgressInput{
		AttemptID: step.Attempt.ID, Status: LifecycleStepRunning, Checkpoint: "checkpoint-1",
		CompletedUnits: 1, TotalUnits: 2, Message: "halfway",
	})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Checkpoint != "checkpoint-1" || progress.CompletedUnits != 1 || !progress.UpdatedAt.After(createdAt) {
		t.Fatalf("progress did not persist or advance updated_at: %#v", progress)
	}

	// 模拟进程重启：新 repository 必须复用同一 operation 和未完成 step/checkpoint。
	restarted := NewPostgresLifecycleRepository(pool)
	reacquired, err := restarted.AcquireOperation(ctx, input)
	if err != nil || reacquired.Created || reacquired.Operation.ID != operation.ID {
		t.Fatalf("restart acquire = %#v, err=%v", reacquired, err)
	}
	resumedStep, err := restarted.LatestStepAttempt(ctx, operation.ID, stepInput.StepID)
	if err != nil || resumedStep.Checkpoint != "checkpoint-1" || resumedStep.Status != LifecycleStepRunning {
		t.Fatalf("restart step = %#v, err=%v", resumedStep, err)
	}

	if _, err := restarted.CompleteStepAttempt(ctx, CompleteLifecycleStepAttemptInput{
		AttemptID: step.Attempt.ID, Status: LifecycleStepFailed,
		CompletedUnits: 1, TotalUnits: 2,
	}); !errors.Is(err, ErrLifecycleInvalidInput) {
		t.Fatalf("failed step without typed error = %v", err)
	}
	failedStep, err := restarted.CompleteStepAttempt(ctx, CompleteLifecycleStepAttemptInput{
		AttemptID: step.Attempt.ID, Status: LifecycleStepFailed, Checkpoint: "checkpoint-failed",
		CompletedUnits: 1, TotalUnits: 2, Message: "provider unavailable",
		Error: LifecycleExecutionError{
			Code: "unavailable", Reason: "provider_unavailable", Message: "provider unavailable",
			Retryable: true, Metadata: json.RawMessage(`{"provider":"demo"}`),
		},
	})
	if err != nil || failedStep.Status != LifecycleStepFailed || !failedStep.Error.Retryable {
		t.Fatalf("complete failed step = %#v, err=%v", failedStep, err)
	}

	if _, err := restarted.CompleteOperation(ctx, CompleteLifecycleOperationInput{
		OperationID: operation.ID, ExpectedRevision: operation.Revision,
		ExpectedState: LifecycleStateStarting, State: LifecycleStateFailed,
		TerminalResult: LifecycleTerminalFailed,
	}); !errors.Is(err, ErrLifecycleInvalidInput) {
		t.Fatalf("failed operation without typed error = %v", err)
	}
	failedOperation, err := restarted.CompleteOperation(ctx, CompleteLifecycleOperationInput{
		OperationID: operation.ID, ExpectedRevision: operation.Revision,
		ExpectedState: LifecycleStateStarting, State: LifecycleStateFailed,
		TerminalResult: LifecycleTerminalFailed,
		Error: LifecycleExecutionError{
			Code: "unavailable", Reason: "provider_unavailable", Message: "provider unavailable", Retryable: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.ResumeOperation(ctx, ResumeLifecycleOperationInput{
		OperationID: operation.ID, ExpectedRevision: failedOperation.Revision, ExpectedState: LifecycleStateFailed,
		Decision: LifecycleRecoveryRetry, ActorUserID: 81, AuditEventID: 91,
	})
	if err != nil || recovered.State != LifecycleStateRecovery || recovered.AttemptCount != 2 || recovered.CompletedAt != nil {
		t.Fatalf("recovered operation = %#v, err=%v", recovered, err)
	}
	decision, err := restarted.RecoveryDecision(ctx, operation.ID, recovered.AttemptCount)
	if err != nil || decision.Decision != LifecycleRecoveryRetry || decision.ActorUserID != 81 ||
		decision.AuditEventID != 91 || recovered.RecoveryActorUserID != 81 || recovered.RecoveryAuditEventID != 91 {
		t.Fatalf("recovery decision = %#v operation=%#v err=%v", decision, recovered, err)
	}
	open, err := restarted.ListOpenOperations(ctx, 10)
	if err != nil || !lifecycleOperationsContain(open, operation.ID) {
		t.Fatalf("open recovery operations = %#v, err=%v", open, err)
	}

	retry, err := restarted.BeginStepAttempt(ctx, stepInput)
	if err != nil || !retry.Created || retry.Attempt.Attempt != 2 ||
		retry.Attempt.Checkpoint != stepInput.Checkpoint || retry.Attempt.CompletedUnits != 0 || retry.Attempt.TotalUnits != 0 {
		t.Fatalf("begin retry = %#v, err=%v", retry, err)
	}
	if _, err := restarted.UpdateStepProgress(ctx, UpdateLifecycleStepProgressInput{
		AttemptID: retry.Attempt.ID, Status: LifecycleStepRunning, Checkpoint: "checkpoint-2",
		CompletedUnits: 2, TotalUnits: 2, Message: "done",
	}); err != nil {
		t.Fatal(err)
	}
	succeededStep, err := restarted.CompleteStepAttempt(ctx, CompleteLifecycleStepAttemptInput{
		AttemptID: retry.Attempt.ID, Status: LifecycleStepSucceeded, Checkpoint: "checkpoint-2",
		CompletedUnits: 2, TotalUnits: 2, Message: "done",
		ResultDocument: json.RawMessage(`{"installed":true}`),
	})
	if err != nil || succeededStep.Status != LifecycleStepSucceeded || !lifecycleJSONHasBool(succeededStep.ResultDocument, "installed") {
		t.Fatalf("successful retry = %#v, err=%v", succeededStep, err)
	}
	transitioned, err := restarted.TransitionOperation(ctx, TransitionLifecycleOperationInput{
		OperationID: operation.ID, ExpectedRevision: recovered.Revision,
		ExpectedState: LifecycleStateRecovery, State: LifecycleStateStarting,
		CurrentStepID: stepInput.StepID, Progress: json.RawMessage(`{"completed":2,"total":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.CompleteOperation(ctx, CompleteLifecycleOperationInput{
		OperationID: operation.ID, ExpectedRevision: transitioned.Revision,
		ExpectedState: LifecycleStateStarting, State: LifecycleStateEnabled,
		TerminalResult: LifecycleTerminalSucceeded, ResultDocument: json.RawMessage(`{"enabled":true}`),
	})
	if err != nil || completed.TerminalResult != LifecycleTerminalSucceeded || completed.CompletedAt == nil {
		t.Fatalf("completed operation = %#v, err=%v", completed, err)
	}
	if _, err := restarted.OpenOperation(ctx, extensionID); !errors.Is(err, ErrLifecycleOperationNotFound) {
		t.Fatalf("completed operation remained open: %v", err)
	}
	attempts, err := restarted.ListStepAttempts(ctx, operation.ID)
	if err != nil || len(attempts) != 2 || attempts[0].Attempt != 1 || attempts[1].Attempt != 2 {
		t.Fatalf("step attempts = %#v, err=%v", attempts, err)
	}
}

func TestPostgresLifecycleRepositoryEnforcesAuthorityAndRetentionBoundaries(t *testing.T) {
	ctx, pool, repository, extensionID := newLifecycleRepositoryIntegration(t)

	forcedEnable := lifecycleAcquireTestInput(extensionID, LifecycleOperationEnable)
	forcedEnable.Forced = true
	if _, err := repository.AcquireOperation(ctx, forcedEnable); !errors.Is(err, ErrLifecycleInvalidInput) {
		t.Fatalf("forced non-uninstall error = %v", err)
	}

	var auditEventID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO audit_events (action, metadata)
		VALUES ('extension.lifecycle.integration', '{}'::jsonb)
		RETURNING id
	`).Scan(&auditEventID); err != nil {
		t.Fatal(err)
	}
	input := lifecycleAcquireTestInput(extensionID, LifecycleOperationUninstall)
	input.IdempotencyKey = "uninstall:first"
	input.RequestFingerprint = strings.Repeat("c", 64)
	input.RemovalMode = LifecycleRemovalPreserve
	input.Forced = true
	input.AuditEventID = auditEventID
	acquired, err := repository.AcquireOperation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	step, err := repository.BeginStepAttempt(ctx, BeginLifecycleStepAttemptInput{
		OperationID: acquired.Operation.ID, StepID: "uninstall.execute",
		LifecycleAction: "uninstall", PlanVersion: input.PlanVersion, AuditEventID: auditEventID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CompleteStepAttempt(ctx, CompleteLifecycleStepAttemptInput{
		AttemptID: step.Attempt.ID, Status: LifecycleStepCancelled,
		CompletedUnits: 0, TotalUnits: 1,
	}); !errors.Is(err, ErrLifecycleInvalidInput) {
		t.Fatalf("cancelled step without typed error = %v", err)
	}
	cancelledStep, err := repository.CompleteStepAttempt(ctx, CompleteLifecycleStepAttemptInput{
		AttemptID: step.Attempt.ID, Status: LifecycleStepCancelled,
		CompletedUnits: 0, TotalUnits: 1, AuditEventID: auditEventID,
		Error: LifecycleExecutionError{Code: "cancelled", Reason: "operator_cancelled", Message: "operator cancelled"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CompleteOperation(ctx, CompleteLifecycleOperationInput{
		OperationID: acquired.Operation.ID, ExpectedRevision: acquired.Operation.Revision,
		ExpectedState: LifecycleStatePlanned, State: LifecycleStateUninstalling,
		TerminalResult: LifecycleTerminalCancelled,
	}); !errors.Is(err, ErrLifecycleInvalidInput) {
		t.Fatalf("cancelled operation without typed error = %v", err)
	}
	completed, err := repository.CompleteOperation(ctx, CompleteLifecycleOperationInput{
		OperationID: acquired.Operation.ID, ExpectedRevision: acquired.Operation.Revision,
		ExpectedState: LifecycleStatePlanned, State: LifecycleStateUninstalling,
		TerminalResult: LifecycleTerminalCancelled, AuditEventID: auditEventID,
		Error: LifecycleExecutionError{Code: "cancelled", Reason: "operator_cancelled", Message: "operator cancelled"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM audit_events WHERE id = $1`, auditEventID); err != nil {
		t.Fatalf("audit retention delete was blocked: %v", err)
	}
	retained, err := repository.OperationByIdempotencyKey(ctx, extensionID, input.IdempotencyKey)
	if err != nil || retained.AuditEventID != auditEventID {
		t.Fatalf("operation audit snapshot = %#v, err=%v", retained, err)
	}
	retainedStep, err := repository.LatestStepAttempt(ctx, acquired.Operation.ID, "uninstall.execute")
	if err != nil || retainedStep.AuditEventID != auditEventID || retainedStep.ID != cancelledStep.ID {
		t.Fatalf("step audit snapshot = %#v, err=%v", retainedStep, err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, extensionID); err != nil {
		t.Fatal(err)
	}
	retained, err = repository.OperationByIdempotencyKey(ctx, extensionID, input.IdempotencyKey)
	if err != nil || retained.ID != completed.ID || retained.RemovalMode != LifecycleRemovalPreserve || !retained.Forced {
		t.Fatalf("uninstall history after extension deletion = %#v, err=%v", retained, err)
	}
	byID, err := repository.Operation(ctx, extensionID, completed.ID)
	if err != nil || byID.ID != completed.ID {
		t.Fatalf("operation by exact extension/id = %#v, err=%v", byID, err)
	}
	history, err := repository.ListOperations(ctx, extensionID, 1)
	if err != nil || len(history) != 1 || history[0].ID != completed.ID {
		t.Fatalf("bounded operation history = %#v, err=%v", history, err)
	}
	if _, err := repository.Operation(ctx, "another.extension", completed.ID); !errors.Is(err, ErrLifecycleOperationNotFound) {
		t.Fatalf("cross-extension operation lookup error = %v", err)
	}
}

func TestPostgresLifecycleRepositoryPersistsEveryRecoveryDecisionWithoutReplacingAuthority(t *testing.T) {
	ctx, _, repository, extensionID := newLifecycleRepositoryIntegration(t)
	input := lifecycleAcquireTestInput(extensionID, LifecycleOperationUninstall)
	input.RemovalMode = LifecycleRemovalComplete
	input.AuditEventID = 71
	input.AuthoritySnapshot = json.RawMessage(`{"schemaVersion":"authority@1","actorUserId":41}`)
	acquired, err := repository.AcquireOperation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	failure := LifecycleExecutionError{Code: "cleanup.failed", Reason: "cleanup.failed", Message: "external resource remains"}
	failed, err := repository.CompleteOperation(ctx, CompleteLifecycleOperationInput{
		OperationID: acquired.Operation.ID, ExpectedRevision: acquired.Operation.Revision,
		ExpectedState: acquired.Operation.State, State: LifecycleStateFailed,
		TerminalResult: LifecycleTerminalFailed, Error: failure,
	})
	if err != nil {
		t.Fatal(err)
	}
	forced, err := repository.ResumeOperation(ctx, ResumeLifecycleOperationInput{
		OperationID: failed.ID, ExpectedRevision: failed.Revision, ExpectedState: failed.State,
		Decision: LifecycleRecoverySkipStep, EscalateForced: true,
		Reason: "operator accepted residual external resources", ActorUserID: 81, AuditEventID: 91,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !forced.Forced || forced.RecoveryActorUserID != 81 || forced.RecoveryAuditEventID != 91 ||
		forced.AuditEventID != 71 || !lifecycleJSONEqual(forced.AuthoritySnapshot, input.AuthoritySnapshot) {
		t.Fatalf("forced recovery replaced authority: %#v", forced)
	}

	failedAgain, err := repository.CompleteOperation(ctx, CompleteLifecycleOperationInput{
		OperationID: forced.ID, ExpectedRevision: forced.Revision, ExpectedState: forced.State,
		State: LifecycleStateFailed, TerminalResult: LifecycleTerminalFailed, Error: failure,
		AuditEventID: 91,
	})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := repository.ResumeOperation(ctx, ResumeLifecycleOperationInput{
		OperationID: failedAgain.ID, ExpectedRevision: failedAgain.Revision, ExpectedState: failedAgain.State,
		Decision: LifecycleRecoveryRetry, ActorUserID: 82, AuditEventID: 92,
	})
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := repository.ListRecoveryDecisions(ctx, retried.ID)
	if err != nil || len(decisions) != 2 || decisions[0].OperationAttempt != 2 ||
		decisions[0].Decision != LifecycleRecoverySkipStep || !decisions[0].EscalateForced ||
		decisions[0].ActorUserID != 81 || decisions[0].AuditEventID != 91 ||
		decisions[1].OperationAttempt != 3 || decisions[1].Decision != LifecycleRecoveryRetry ||
		decisions[1].EscalateForced || decisions[1].ActorUserID != 82 || decisions[1].AuditEventID != 92 {
		t.Fatalf("recovery decisions = %#v, err=%v", decisions, err)
	}
	if !retried.Forced || retried.RecoveryActorUserID != 82 || retried.RecoveryAuditEventID != 92 ||
		retried.AuditEventID != 71 || !lifecycleJSONEqual(retried.AuthoritySnapshot, input.AuthoritySnapshot) {
		t.Fatalf("latest recovery replaced original authority: %#v", retried)
	}
}

func TestPostgresLifecycleInspectionRejectsInvalidScopeBeforeQuery(t *testing.T) {
	repository := &PostgresLifecycleRepository{}
	if _, err := repository.Operation(context.Background(), "", 1); !errors.Is(err, ErrLifecycleInvalidInput) {
		t.Fatalf("empty extension operation error = %v", err)
	}
	if _, err := repository.Operation(context.Background(), "valid.extension", 0); !errors.Is(err, ErrLifecycleInvalidInput) {
		t.Fatalf("zero operation id error = %v", err)
	}
	if _, err := repository.ListOperations(context.Background(), "INVALID", 10); !errors.Is(err, ErrLifecycleInvalidInput) {
		t.Fatalf("invalid extension history error = %v", err)
	}
}

func newLifecycleRepositoryIntegration(t *testing.T) (context.Context, *pgxpool.Pool, *PostgresLifecycleRepository, string) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	extensionID := "lifecycle.integration." + fmt.Sprintf("%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system, is_deletable)
		VALUES ($1, 'plugin', 'Lifecycle Integration', 'installed', 'builtin', true, false)
	`, extensionID); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_operations WHERE extension_id = $1`, extensionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, extensionID)
		pool.Close()
	})
	return ctx, pool, NewPostgresLifecycleRepository(pool), extensionID
}

func lifecycleAcquireTestInput(extensionID, operation string) AcquireLifecycleOperationInput {
	return AcquireLifecycleOperationInput{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("a", 64),
		ArtifactDigests: json.RawMessage(`{"backend":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		Operation:       operation, PlanVersion: "lifecycle.integration@1", IdempotencyKey: operation + ":first",
		RequestFingerprint: strings.Repeat("b", 64), AuthorityType: LifecycleAuthorityBuiltin,
		AuthoritySnapshot: json.RawMessage(`{"source":"builtin"}`),
	}
}

func lifecycleOperationsContain(items []LifecycleOperation, operationID int64) bool {
	for _, item := range items {
		if item.ID == operationID {
			return true
		}
	}
	return false
}

func lifecycleJSONHasBool(document json.RawMessage, key string) bool {
	var value map[string]any
	return json.Unmarshal(document, &value) == nil && value[key] == true
}
