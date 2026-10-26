package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	testCommandID            = "sforum.test.atomic-write"
	testCommandVersion       = "1"
	testCommandInputSchema   = "sforum.test.atomic-write.input"
	testCommandOutputSchema  = "sforum.test.atomic-write.result"
	testCommandSchemaVersion = "1"
)

func TestProtocolV2CommandPlanIsPolicyCheckedAndWriteFree(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	var executeCalls atomic.Int32
	engine := newTestProtocolV2CommandEngine(t, backend, func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		executeCalls.Add(1)
		return testProtocolV2CommandExecution(t), nil
	})

	plan, err := engine.plan(context.Background(), testProtocolV2CommandRequest(t, "plan-key", "next"))
	if err != nil || plan.GetError() != nil {
		t.Fatalf("plan = %#v, err = %v", plan, err)
	}
	if plan.GetPlanId() == "" || plan.GetCommandId() != testCommandID || plan.GetCommandVersion() != testCommandVersion {
		t.Fatalf("unexpected plan identity: %#v", plan)
	}
	if len(plan.GetPolicy()) != 1 || !plan.GetPolicy()[0].GetAllowed() || len(plan.GetImpact()) != 1 {
		t.Fatalf("policy/impact = %#v / %#v", plan.GetPolicy(), plan.GetImpact())
	}
	if plan.GetProjectedResult().GetSchemaId() != testCommandOutputSchema {
		t.Fatalf("projected result = %#v", plan.GetProjectedResult())
	}
	if backend.beginCount() != 0 || executeCalls.Load() != 0 {
		t.Fatalf("Plan opened a transaction or executed writes: begins=%d execute=%d", backend.beginCount(), executeCalls.Load())
	}

	again, err := engine.plan(context.Background(), testProtocolV2CommandRequest(t, "different-key", "next"))
	if err != nil || again.GetPlanId() != plan.GetPlanId() {
		t.Fatalf("plan id must ignore transport/idempotency correlation: first=%q again=%q err=%v", plan.GetPlanId(), again.GetPlanId(), err)
	}
}

func TestProtocolV2CommandExecuteCommitsAuditsAndReplays(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	var executeCalls atomic.Int32
	engine := newTestProtocolV2CommandEngine(t, backend, func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		executeCalls.Add(1)
		if _, err := tx.Exec(ctx, "test.write", "topic:42", request.GetInput().GetValue().AsMap()["value"]); err != nil {
			return nil, err
		}
		return testProtocolV2CommandExecution(t), nil
	})

	request := testProtocolV2CommandRequest(t, "commit-key", "next")
	committed, err := engine.execute(context.Background(), request)
	if err != nil || committed.GetError() != nil || committed.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED {
		t.Fatalf("committed = %#v, err = %v", committed, err)
	}
	if committed.GetTransactionId() == "" || committed.GetAuditEventId() == "" || committed.GetCommittedRevision() != "rev-2" {
		t.Fatalf("missing committed metadata: %#v", committed)
	}
	if got := backend.value("topic:42"); got != "next" {
		t.Fatalf("committed value = %q", got)
	}
	if audits := backend.auditSnapshot(); len(audits) != 1 || audits[0].ActorUserID != 0 || audits[0].ExtensionID != "demo.plugin" || len(audits[0].Impact) != 1 {
		t.Fatalf("audits = %#v", audits)
	}

	retry := proto.Clone(request).(*hostv2.CommandRequest)
	retry.Context.RequestId = "command-retry"
	replayed, err := engine.execute(context.Background(), retry)
	if err != nil || replayed.GetError() != nil || replayed.GetState() != hostv2.CommandState_COMMAND_STATE_REPLAYED {
		t.Fatalf("replayed = %#v, err = %v", replayed, err)
	}
	if replayed.GetTransactionId() != committed.GetTransactionId() || replayed.GetAuditEventId() != committed.GetAuditEventId() || executeCalls.Load() != 1 {
		t.Fatalf("replay changed receipt or executed twice: committed=%#v replay=%#v calls=%d", committed, replayed, executeCalls.Load())
	}
	if replayed.GetContext().GetRequestId() != "command-retry" {
		t.Fatalf("replay retained stale response context: %#v", replayed.GetContext())
	}
	restarted := proto.Clone(request).(*hostv2.CommandRequest)
	restarted.Context.RequestId = "command-after-restart"
	restarted.Context.Extension.RuntimeEpoch++
	restarted.Context.Extension.InstanceId = "replacement-instance"
	replayed, err = engine.execute(context.Background(), restarted)
	if err != nil || replayed.GetState() != hostv2.CommandState_COMMAND_STATE_REPLAYED || replayed.GetError() != nil || executeCalls.Load() != 1 {
		t.Fatalf("restart replay = %#v, calls=%d, err=%v", replayed, executeCalls.Load(), err)
	}
	if backend.commitCount() != 3 || backend.rollbackCount() != 0 {
		t.Fatalf("transaction counts: commits=%d rollbacks=%d", backend.commitCount(), backend.rollbackCount())
	}
}

func TestProtocolV2CommandDelegatedActorIsVerifiedAuthorizedAndAudited(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	definition := testProtocolV2CommandDefinition(t, func(ctx context.Context, _ pgx.Tx, _ *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		if actorUserID, ok := ProtocolV2CommandActorUserID(ctx); !ok || actorUserID != 42 {
			t.Fatalf("execute actor = %d, %v", actorUserID, ok)
		}
		return testProtocolV2CommandExecution(t), nil
	})
	definition.ActorMode = protocolV2CommandActorDelegated
	definition.RequiredPermissions = []string{"topic.edit", "topic.read"}
	originalPreview := definition.Preview
	definition.Preview = func(ctx context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		if actorUserID, ok := ProtocolV2CommandActorUserID(ctx); ok || actorUserID != 0 {
			t.Fatalf("plan exposed un-rechecked actor = %d, %v", actorUserID, ok)
		}
		return originalPreview(ctx, request)
	}
	originalPrepare := definition.Prepare
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		if actorUserID, ok := ProtocolV2CommandActorUserID(ctx); !ok || actorUserID != 42 {
			t.Fatalf("prepare actor = %d, %v", actorUserID, ok)
		}
		return originalPrepare(ctx, tx, request)
	}
	engine, err := newProtocolV2CommandEngineWithActorDelegation(backend, authority, definition)
	if err != nil {
		t.Fatal(err)
	}
	request := testProtocolV2CommandRequest(t, "delegated-key", "next")
	request.Context.Extension.ArtifactDigest = strings.Repeat("a", 64)
	request.Context.Extension.TrustGrantId = "41"
	runtime := cloneProtocolV2ExtensionIdentity(request.Context.Extension)
	request.ActorDelegation, err = authority.IssueActorDelegation(context.Background(), ProtocolV2ActorDelegationRequest{
		ActorUserID: 42, Runtime: runtime, CommandID: definition.ID,
		CommandVersion: definition.Version, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), runtime)
	plan, err := engine.plan(ctx, request)
	if err != nil || plan.GetError() != nil || backend.beginCount() != 0 {
		t.Fatalf("delegated plan = %#v, %v, begins=%d", plan, err, backend.beginCount())
	}
	committed, err := engine.execute(ctx, request)
	if err != nil || committed.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED || committed.GetError() != nil {
		t.Fatalf("delegated command = %#v, %v", committed, err)
	}
	backend.mu.Lock()
	if backend.actorCalls != 1 || backend.actorReceipt || !protocolV2SHA256Hex(backend.actorFingerprint) ||
		len(backend.actorPermissions) != 2 || backend.actorPermissions[0] != "topic.edit" || backend.actorPermissions[1] != "topic.read" {
		t.Fatalf("actor authorization = calls:%d receipt:%v permissions:%#v fingerprint:%q", backend.actorCalls, backend.actorReceipt, backend.actorPermissions, backend.actorFingerprint)
	}
	backend.mu.Unlock()
	if audits := backend.auditSnapshot(); len(audits) != 1 || audits[0].ActorUserID != 42 {
		t.Fatalf("delegated audits = %#v", audits)
	}

	retry := proto.Clone(request).(*hostv2.CommandRequest)
	retry.Context.RequestId = "delegated-retry"
	replayed, err := engine.execute(ctx, retry)
	if err != nil || replayed.GetState() != hostv2.CommandState_COMMAND_STATE_REPLAYED || replayed.GetError() != nil {
		t.Fatalf("delegated replay = %#v, %v", replayed, err)
	}
	backend.mu.Lock()
	if backend.actorCalls != 2 || !backend.actorReceipt {
		t.Fatalf("replay authorization = calls:%d receipt:%v", backend.actorCalls, backend.actorReceipt)
	}
	backend.mu.Unlock()
}

func TestProtocolV2CommandDelegationEnvelopeFailsClosed(t *testing.T) {
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	actorDefinition := testProtocolV2CommandDefinition(t, func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		t.Fatal("invalid delegated command executed")
		return nil, nil
	})
	actorDefinition.ActorMode = protocolV2CommandActorDelegated
	actorDefinition.RequiredPermissions = []string{"topic.read"}
	actorEngine, err := newProtocolV2CommandEngineWithActorDelegation(newFakeProtocolV2CommandBackend(), authority, actorDefinition)
	if err != nil {
		t.Fatal(err)
	}
	request := testProtocolV2CommandRequest(t, "delegation-required", "next")
	result, err := actorEngine.execute(context.Background(), request)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_REJECTED || result.GetError().GetReason() != "host.command_actor_delegation_required" {
		t.Fatalf("missing delegation = %#v, %v", result, err)
	}
	request.ActorDelegation = "forged"
	result, err = actorEngine.execute(context.Background(), request)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_REJECTED || result.GetError().GetReason() != "host.command_actor_delegation_invalid" {
		t.Fatalf("forged delegation = %#v, %v", result, err)
	}

	serviceBackend := newFakeProtocolV2CommandBackend()
	serviceEngine := newTestProtocolV2CommandEngine(t, serviceBackend, func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		t.Fatal("actor token reached actorless service command")
		return nil, nil
	})
	serviceRequest := testProtocolV2CommandRequest(t, "service-key", "next")
	serviceRequest.ActorDelegation = "forged"
	result, err = serviceEngine.execute(context.Background(), serviceRequest)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_REJECTED || result.GetError().GetReason() != "host.command_actor_delegation_unexpected" || serviceBackend.beginCount() != 0 {
		t.Fatalf("unexpected service delegation = %#v, %v", result, err)
	}
}

func TestProtocolV2CommandIdempotencyConflictRollsBack(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	var executeCalls atomic.Int32
	engine := newTestProtocolV2CommandEngine(t, backend, func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		executeCalls.Add(1)
		_, err := tx.Exec(ctx, "test.write", "topic:42", request.GetInput().GetValue().AsMap()["value"])
		return testProtocolV2CommandExecution(t), err
	})

	first, err := engine.execute(context.Background(), testProtocolV2CommandRequest(t, "same-key", "first"))
	if err != nil || first.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED {
		t.Fatalf("first = %#v, err = %v", first, err)
	}
	conflict, err := engine.execute(context.Background(), testProtocolV2CommandRequest(t, "same-key", "different"))
	if err != nil || conflict.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK || conflict.GetError().GetReason() != "host.command_idempotency_conflict" {
		t.Fatalf("conflict = %#v, err = %v", conflict, err)
	}
	if got := backend.value("topic:42"); got != "first" || executeCalls.Load() != 1 {
		t.Fatalf("conflicting retry escaped idempotency: value=%q calls=%d", got, executeCalls.Load())
	}
	if backend.rollbackCount() != 1 || len(backend.auditSnapshot()) != 1 {
		t.Fatalf("rollback/audit counts = %d/%d", backend.rollbackCount(), len(backend.auditSnapshot()))
	}
}

func TestProtocolV2CommandFailuresRollbackEveryAtomicWrite(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeProtocolV2CommandBackend)
		execute   func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error)
	}{
		{
			name: "handler",
			execute: func(ctx context.Context, tx pgx.Tx, _ *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
				_, _ = tx.Exec(ctx, "test.write", "topic:42", "leaked")
				return nil, newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.command_test_failed", "The test write failed.", false)
			},
		},
		{
			name:      "audit storage",
			configure: func(backend *fakeProtocolV2CommandBackend) { backend.auditErr = errors.New("audit unavailable") },
			execute: func(ctx context.Context, tx pgx.Tx, _ *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
				_, _ = tx.Exec(ctx, "test.write", "topic:42", "leaked")
				return testProtocolV2CommandExecution(t), nil
			},
		},
		{
			name:      "idempotency storage",
			configure: func(backend *fakeProtocolV2CommandBackend) { backend.saveErr = errors.New("ledger unavailable") },
			execute: func(ctx context.Context, tx pgx.Tx, _ *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
				_, _ = tx.Exec(ctx, "test.write", "topic:42", "leaked")
				return testProtocolV2CommandExecution(t), nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2CommandBackend()
			if test.configure != nil {
				test.configure(backend)
			}
			engine := newTestProtocolV2CommandEngine(t, backend, test.execute)
			result, err := engine.execute(context.Background(), testProtocolV2CommandRequest(t, "rollback-key", "next"))
			if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK || result.GetError() == nil {
				t.Fatalf("result = %#v, err = %v", result, err)
			}
			if got := backend.value("topic:42"); got != "" {
				t.Fatalf("business write escaped rollback: %q", got)
			}
			if len(backend.auditSnapshot()) != 0 || backend.receiptCount() != 0 || backend.commitCount() != 0 || backend.rollbackCount() != 1 {
				t.Fatalf("atomic state leaked: audits=%d receipts=%d commits=%d rollbacks=%d", len(backend.auditSnapshot()), backend.receiptCount(), backend.commitCount(), backend.rollbackCount())
			}
		})
	}
}

func TestProtocolV2CommandAuthoritativePolicyAndDryRun(t *testing.T) {
	t.Run("transaction policy denied", func(t *testing.T) {
		backend := newFakeProtocolV2CommandBackend()
		definition := testProtocolV2CommandDefinition(t, func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
			t.Fatal("denied command executed")
			return nil, nil
		})
		definition.Prepare = func(_ context.Context, tx pgx.Tx, _ *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
			if tx == nil {
				t.Fatal("authoritative prepare did not receive transaction")
			}
			return &protocolV2CommandPreparation{
				Policy:          []*hostv2.PolicyDecision{{PolicyId: "sforum.test.policy@1", ResourceId: "topic:42", Allowed: false, Reason: "denied"}},
				Impact:          []*hostv2.ImpactItem{{Module: "forum", Action: "update", ResourceType: "topic", ResourceId: "42"}},
				ProjectedResult: testProtocolV2CommandDocument(t, testCommandOutputSchema, map[string]any{"status": "planned"}),
			}, nil
		}
		engine, err := newProtocolV2CommandEngine(backend, definition)
		if err != nil {
			t.Fatal(err)
		}
		result, err := engine.execute(context.Background(), testProtocolV2CommandRequest(t, "denied-key", "next"))
		if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK || result.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED || backend.beginCount() != 1 || backend.rollbackCount() != 1 {
			t.Fatalf("result=%#v err=%v begins=%d rollbacks=%d", result, err, backend.beginCount(), backend.rollbackCount())
		}
	})

	t.Run("dry run", func(t *testing.T) {
		backend := newFakeProtocolV2CommandBackend()
		engine := newTestProtocolV2CommandEngine(t, backend, func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
			t.Fatal("dry-run command executed")
			return nil, nil
		})
		request := testProtocolV2CommandRequest(t, "dry-run-key", "next")
		request.DryRun = true
		result, err := engine.execute(context.Background(), request)
		if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_REJECTED || result.GetError().GetReason() != "host.command_dry_run_requires_plan" || backend.beginCount() != 0 {
			t.Fatalf("result=%#v err=%v begins=%d", result, err, backend.beginCount())
		}
	})
}

func TestProtocolV2CommandCancellationStillRollsBack(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	engine := newTestProtocolV2CommandEngine(t, backend, func(ctx context.Context, tx pgx.Tx, _ *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		_, _ = tx.Exec(context.WithoutCancel(ctx), "test.write", "topic:42", "leaked")
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := engine.execute(ctx, testProtocolV2CommandRequest(t, "cancelled-key", "next"))
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK || result.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_CANCELLED {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if got := backend.value("topic:42"); got != "" || backend.rollbackCount() != 1 {
		t.Fatalf("cancelled write escaped rollback: value=%q rollbacks=%d", got, backend.rollbackCount())
	}
}

func TestProtocolV2CommandRejectsInvalidEnvelopeWithoutTransaction(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*hostv2.CommandRequest)
		reason string
	}{
		{
			name: "missing idempotency",
			mutate: func(request *hostv2.CommandRequest) {
				request.IdempotencyKey = ""
				request.Context.IdempotencyKey = ""
			},
			reason: "host.command_idempotency_required",
		},
		{
			name: "mismatched idempotency",
			mutate: func(request *hostv2.CommandRequest) {
				request.Context.IdempotencyKey = "other-key"
			},
			reason: "host.command_idempotency_mismatch",
		},
		{
			name: "non-visible idempotency",
			mutate: func(request *hostv2.CommandRequest) {
				request.IdempotencyKey = " key"
				request.Context.IdempotencyKey = " key"
			},
			reason: "host.command_idempotency_invalid",
		},
		{
			name: "wrong input schema",
			mutate: func(request *hostv2.CommandRequest) {
				request.Input.SchemaVersion = "2"
			},
			reason: "host.command_schema_mismatch",
		},
		{
			name: "missing extension",
			mutate: func(request *hostv2.CommandRequest) {
				request.Context.Extension.ExtensionId = ""
			},
			reason: "host.command_extension_required",
		},
		{
			name: "unattested actor",
			mutate: func(request *hostv2.CommandRequest) {
				request.Context.Actor = &protocolv2.Actor{UserId: 42}
			},
			reason: "host.command_actor_unattested",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2CommandBackend()
			engine := newTestProtocolV2CommandEngine(t, backend, func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
				t.Fatal("invalid command executed")
				return nil, nil
			})
			request := testProtocolV2CommandRequest(t, "valid-key", "next")
			test.mutate(request)
			result, err := engine.execute(context.Background(), request)
			if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_REJECTED || result.GetError().GetReason() != test.reason || backend.beginCount() != 0 {
				t.Fatalf("result=%#v err=%v begins=%d", result, err, backend.beginCount())
			}
		})
	}
}

func TestProtocolV2CommandPlanIsRaceSafe(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	engine := newTestProtocolV2CommandEngine(t, backend, func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		return testProtocolV2CommandExecution(t), nil
	})
	const workers = 64
	ids := make(chan string, workers)
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			plan, err := engine.plan(context.Background(), testProtocolV2CommandRequest(t, "race-key", "next"))
			if err != nil || plan.GetError() != nil {
				errorsSeen <- fmt.Errorf("plan=%v err=%w", plan.GetError(), err)
				return
			}
			ids <- plan.GetPlanId()
		}()
	}
	wait.Wait()
	close(ids)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	var expected string
	for id := range ids {
		if expected == "" {
			expected = id
		}
		if id == "" || id != expected {
			t.Fatalf("non-deterministic plan id: got=%q expected=%q", id, expected)
		}
	}
}

func TestGatewayFreezesProtocolV2CommandRuntimeAtBrokerRegistration(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	engine := newTestProtocolV2CommandEngine(t, backend, func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		return testProtocolV2CommandExecution(t), nil
	})
	gateway := NewGateway(New(Config{}))
	if err := gateway.BindProtocolV2CommandRuntime(newProtocolV2CommandRuntime(engine)); err != nil {
		t.Fatalf("bind command runtime: %v", err)
	}
	server := grpc.NewServer()
	gateway.RegisterProtocolV2(server)
	if _, ok := server.GetServiceInfo()["sforum.host.v2.HostCommandService"]; !ok {
		t.Fatal("bound command service was not registered")
	}
	if gateway.commands != engine || !gateway.protocolV2CommandsFrozen {
		t.Fatalf("command snapshot was not frozen: engine=%p frozen=%v", gateway.commands, gateway.protocolV2CommandsFrozen)
	}
	replacement := newTestProtocolV2CommandEngine(t, newFakeProtocolV2CommandBackend(), func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		return testProtocolV2CommandExecution(t), nil
	})
	if err := gateway.BindProtocolV2CommandRuntime(newProtocolV2CommandRuntime(replacement)); err == nil {
		t.Fatal("runtime replacement after broker registration must be rejected")
	}
	if gateway.commands != engine {
		t.Fatal("rejected replacement changed the frozen command snapshot")
	}
}

func newTestProtocolV2CommandEngine(
	t *testing.T,
	backend protocolV2CommandBackend,
	execute func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error),
) *protocolV2CommandEngine {
	t.Helper()
	engine, err := newProtocolV2CommandEngine(backend, testProtocolV2CommandDefinition(t, execute))
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func testProtocolV2CommandDefinition(
	t *testing.T,
	execute func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error),
) protocolV2CommandDefinition {
	t.Helper()
	preview := func(context.Context, *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		return &protocolV2CommandPreparation{
			Policy:          []*hostv2.PolicyDecision{{PolicyId: "sforum.test.policy@1", ResourceId: "topic:42", Allowed: true, Reason: "allowed"}},
			Impact:          []*hostv2.ImpactItem{{Module: "forum", Action: "update", ResourceType: "topic", ResourceId: "42", Summary: "Update test topic", Reversible: true}},
			ProjectedResult: testProtocolV2CommandDocument(t, testCommandOutputSchema, map[string]any{"status": "planned"}),
		}, nil
	}
	return protocolV2CommandDefinition{
		ID: testCommandID, Version: testCommandVersion,
		InputSchemaID: testCommandInputSchema, InputSchemaVersion: testCommandSchemaVersion,
		OutputSchemaID: testCommandOutputSchema, OutputSchemaVersion: testCommandSchemaVersion,
		Preview: preview,
		Prepare: func(context.Context, pgx.Tx, *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
			return &protocolV2CommandPreparation{
				Policy:          []*hostv2.PolicyDecision{{PolicyId: "sforum.test.policy@1", ResourceId: "topic:42", Allowed: true, Reason: "allowed"}},
				Impact:          []*hostv2.ImpactItem{{Module: "forum", Action: "update", ResourceType: "topic", ResourceId: "42", Summary: "Update test topic", Reversible: true}},
				ProjectedResult: testProtocolV2CommandDocument(t, testCommandOutputSchema, map[string]any{"status": "planned"}),
			}, nil
		},
		Execute: execute,
	}
}

func testProtocolV2CommandRequest(t *testing.T, idempotencyKey, value string) *hostv2.CommandRequest {
	t.Helper()
	requestContext := testProtocolV2RequestContext()
	requestContext.RequestId = "command-request"
	requestContext.IdempotencyKey = idempotencyKey
	requestContext.Actor = nil
	return &hostv2.CommandRequest{
		Context: requestContext, CommandId: testCommandID, CommandVersion: testCommandVersion,
		IdempotencyKey: idempotencyKey, ExpectedRevision: "rev-1",
		Input: testProtocolV2CommandDocument(t, testCommandInputSchema, map[string]any{"value": value}),
	}
}

func testProtocolV2CommandExecution(t *testing.T) *protocolV2CommandExecution {
	t.Helper()
	return &protocolV2CommandExecution{
		Output:            testProtocolV2CommandDocument(t, testCommandOutputSchema, map[string]any{"status": "committed"}),
		CommittedRevision: "rev-2",
	}
}

func testProtocolV2CommandDocument(t *testing.T, schemaID string, value map[string]any) *protocolv2.TypedDocument {
	t.Helper()
	document, err := structpb.NewStruct(value)
	if err != nil {
		t.Fatal(err)
	}
	return &protocolv2.TypedDocument{SchemaId: schemaID, SchemaVersion: testCommandSchemaVersion, Value: document}
}
