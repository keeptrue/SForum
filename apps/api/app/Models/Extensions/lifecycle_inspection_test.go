package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type lifecycleInspectionTestRepository struct {
	operation  LifecycleOperation
	operations []LifecycleOperation
	steps      []LifecycleStepAttempt
	recoveries []LifecycleRecoveryDecision
}

func (r *lifecycleInspectionTestRepository) Operation(_ context.Context, extensionID string, operationID int64) (LifecycleOperation, error) {
	if r.operation.ExtensionID != extensionID || r.operation.ID != operationID {
		return LifecycleOperation{}, ErrLifecycleOperationNotFound
	}
	return r.operation, nil
}

func (r *lifecycleInspectionTestRepository) OpenOperation(context.Context, string) (LifecycleOperation, error) {
	return LifecycleOperation{}, ErrLifecycleOperationNotFound
}

func (r *lifecycleInspectionTestRepository) ListOperations(_ context.Context, extensionID string, _ int) ([]LifecycleOperation, error) {
	result := make([]LifecycleOperation, 0, len(r.operations))
	for _, operation := range r.operations {
		if operation.ExtensionID == extensionID {
			result = append(result, operation)
		}
	}
	return result, nil
}

func (r *lifecycleInspectionTestRepository) ListStepAttempts(_ context.Context, operationID int64) ([]LifecycleStepAttempt, error) {
	result := make([]LifecycleStepAttempt, 0, len(r.steps))
	for _, step := range r.steps {
		if step.OperationID == operationID {
			result = append(result, step)
		}
	}
	return result, nil
}

func (r *lifecycleInspectionTestRepository) ListRecoveryDecisions(_ context.Context, operationID int64) ([]LifecycleRecoveryDecision, error) {
	result := make([]LifecycleRecoveryDecision, 0, len(r.recoveries))
	for _, decision := range r.recoveries {
		if decision.OperationID == operationID {
			result = append(result, decision)
		}
	}
	return result, nil
}

func TestLifecycleInspectionReturnsOnlyAllowlistedFields(t *testing.T) {
	now := time.Now().UTC()
	operation := LifecycleOperation{
		ID: 41, ExtensionID: "demo.lifecycle", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), ArtifactDigests: json.RawMessage(`{"secret":"artifact"}`),
		Operation: LifecycleOperationUninstall, State: LifecycleStateFailed,
		PlanVersion: "demo.lifecycle@1", IdempotencyKey: "private-idempotency",
		RequestFingerprint: "private-fingerprint", AuthorityType: LifecycleAuthorityTrustGrant,
		TrustGrantID: 9, AuthoritySnapshot: json.RawMessage(`{"secret":"authority"}`),
		RequestedByUserID: 7, AuditEventID: 8, RemovalMode: LifecycleRemovalComplete,
		Forced: true, AttemptCount: 2, Revision: 5, CurrentStepID: "lifecycle.uninstall.04.uninstall",
		Checkpoint: json.RawMessage(`{"secret":"checkpoint"}`),
		Progress:   json.RawMessage(`{"secret":"progress"}`), TerminalResult: LifecycleTerminalFailed,
		ResultDocument: json.RawMessage(`{"secret":"operation-result"}`),
		Error:          LifecycleExecutionError{Code: "external.cleanup", Message: "cleanup failed", Retryable: true, Metadata: json.RawMessage(`{"secret":"error-metadata"}`)},
		CreatedAt:      now, UpdatedAt: now,
	}
	step := LifecycleStepAttempt{
		ID: 52, OperationID: operation.ID, StepID: operation.CurrentStepID,
		LifecycleAction: LifecycleOperationUninstall, PlanVersion: operation.PlanVersion,
		Attempt: 2, Status: LifecycleStepFailed, Checkpoint: "private-step-checkpoint",
		CompletedUnits: 1, TotalUnits: 3, ProgressMessage: "Cleaning external resources",
		InputDocument: json.RawMessage(`{"secret":"step-input"}`), ResultDocument: json.RawMessage(`{"secret":"step-result"}`),
		Error:      LifecycleExecutionError{Code: "external.cleanup", Message: "cleanup failed", Metadata: json.RawMessage(`{"secret":"step-error-metadata"}`)},
		SkipReason: "operator accepted residual resources", Forced: true,
		ActorUserID: 7, AuditEventID: 8, LeaseOwnerToken: "private-lease-token",
		LeaseRevision: 4, CreatedAt: now, UpdatedAt: now,
	}
	recovery := LifecycleRecoveryDecision{
		ID: 53, OperationID: operation.ID, OperationAttempt: 2,
		Decision: LifecycleRecoverySkipStep, EscalateForced: true,
		Reason: "operator accepted residual resources", ActorUserID: 7, AuditEventID: 9, CreatedAt: now,
	}
	repository := &lifecycleInspectionTestRepository{
		operation: operation, operations: []LifecycleOperation{operation},
		steps: []LifecycleStepAttempt{step}, recoveries: []LifecycleRecoveryDecision{recovery},
	}
	service := NewServiceWithOptions(newFakeExtensionStore(map[string]Extension{}), "", "", nil, WithLifecycleInspectionRepository(repository))
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}}

	history, err := service.LifecycleOperations(context.Background(), actor, operation.ExtensionID, 10)
	if err != nil || len(history) != 1 || history[0].ID != operation.ID {
		t.Fatalf("history=%#v err=%v", history, err)
	}
	if history[0].Closed {
		t.Fatal("an operation without completed_at must not report closed")
	}
	detail, err := service.LifecycleOperation(context.Background(), actor, operation.ExtensionID, operation.ID)
	if err != nil || len(detail.Steps) != 1 || detail.Steps[0].ID != step.ID ||
		len(detail.Recoveries) != 1 || detail.Recoveries[0].ID != recovery.ID {
		t.Fatalf("detail=%#v err=%v", detail, err)
	}
	document, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"artifactDigests", "authoritySnapshot", "trustGrantId", "idempotencyKey", "requestFingerprint",
		"checkpoint", "progress\"", "inputDocument", "resultDocument", "metadata", "leaseOwnerToken", "leaseRevision",
		"private-idempotency", "private-fingerprint", "secret", "private-lease-token",
		"external.cleanup", "cleanup failed", "Cleaning external resources",
	} {
		if strings.Contains(string(document), forbidden) {
			t.Fatalf("public lifecycle document leaked %q: %s", forbidden, document)
		}
	}
	if detail.Error.Code != "lifecycle.action_failed" || detail.Error.Reason != "lifecycle.action_failed" ||
		detail.Steps[0].Error.Code != "lifecycle.action_failed" || detail.Steps[0].ProgressMessage != "" {
		t.Fatalf("public lifecycle errors were not canonicalized: %#v", detail)
	}
}

// operation 703 的形态：uninstall 收尾在 uninstalling 阶段，终态只由 completed_at
// + terminal_result 表达。closed 必须是唯一判据，否则 API 表面读起来像「还没关」，
// 而 CLI 依据 completed_at 报「已关闭」——两边对同一行得出相反结论。
func TestLifecycleInspectionMarksCompletedOperationClosedRegardlessOfPhase(t *testing.T) {
	now := time.Now().UTC()
	completedAt := now.Add(-time.Minute)
	operation := LifecycleOperation{
		ID: 703, ExtensionID: "sforum.web-search", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64),
		Operation:     LifecycleOperationUninstall, State: LifecycleStateUninstalling,
		PlanVersion: "sforum.web-search@1", AttemptCount: 1, Revision: 26,
		TerminalResult: LifecycleTerminalSucceeded, CompletedAt: &completedAt,
		CreatedAt: now, UpdatedAt: completedAt,
	}
	repository := &lifecycleInspectionTestRepository{
		operation: operation, operations: []LifecycleOperation{operation},
	}
	service := NewServiceWithOptions(newFakeExtensionStore(map[string]Extension{}), "", "", nil, WithLifecycleInspectionRepository(repository))
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}}

	detail, err := service.LifecycleOperation(context.Background(), actor, operation.ExtensionID, operation.ID)
	if err != nil {
		t.Fatalf("load completed operation: %v", err)
	}
	if !detail.Closed {
		t.Fatalf("completed operation must report closed: %#v", detail.LifecycleOperationSummary)
	}
	if detail.State != LifecycleStateUninstalling || detail.TerminalResult != LifecycleTerminalSucceeded {
		t.Fatalf("last phase and terminal result must stay visible: %#v", detail.LifecycleOperationSummary)
	}
	if detail.CompletedAt == nil {
		t.Fatal("completedAt must be exposed next to closed")
	}
}

func TestLifecycleInspectionPermissionAndDependencyBoundaries(t *testing.T) {
	service := NewService(newFakeExtensionStore(map[string]Extension{}), "")
	if _, err := service.LifecycleOperations(context.Background(), identity.Actor{}, "demo.lifecycle", 10); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("unprivileged history error=%v", err)
	}
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}}
	if _, err := service.LifecycleOperations(context.Background(), actor, "demo.lifecycle", 10); !errors.Is(err, ErrLifecycleCoordinatorUnavailable) {
		t.Fatalf("missing inspector history error=%v", err)
	}
	if _, err := service.LifecycleOperation(context.Background(), actor, "", 1); !errors.Is(err, ErrLifecycleCoordinatorUnavailable) {
		t.Fatalf("missing inspector detail error=%v", err)
	}
}
