package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

type lifecycleRecoveryInspectionRepository struct {
	operation  LifecycleOperation
	steps      []LifecycleStepAttempt
	recoveries []LifecycleRecoveryDecision
}

func (r *lifecycleRecoveryInspectionRepository) Operation(_ context.Context, extensionID string, operationID int64) (LifecycleOperation, error) {
	if r.operation.ExtensionID != extensionID || r.operation.ID != operationID {
		return LifecycleOperation{}, ErrLifecycleOperationNotFound
	}
	return r.operation, nil
}

func (r *lifecycleRecoveryInspectionRepository) OpenOperation(context.Context, string) (LifecycleOperation, error) {
	return LifecycleOperation{}, ErrLifecycleOperationNotFound
}

func (r *lifecycleRecoveryInspectionRepository) ListOperations(_ context.Context, extensionID string, _ int) ([]LifecycleOperation, error) {
	if r.operation.ExtensionID != extensionID {
		return []LifecycleOperation{}, nil
	}
	return []LifecycleOperation{r.operation}, nil
}

func (r *lifecycleRecoveryInspectionRepository) ListStepAttempts(_ context.Context, operationID int64) ([]LifecycleStepAttempt, error) {
	result := make([]LifecycleStepAttempt, 0, len(r.steps))
	for _, step := range r.steps {
		if step.OperationID == operationID {
			result = append(result, step)
		}
	}
	return result, nil
}

func (r *lifecycleRecoveryInspectionRepository) ListRecoveryDecisions(_ context.Context, operationID int64) ([]LifecycleRecoveryDecision, error) {
	result := make([]LifecycleRecoveryDecision, 0, len(r.recoveries))
	for _, decision := range r.recoveries {
		if decision.OperationID == operationID {
			result = append(result, decision)
		}
	}
	return result, nil
}

func TestServiceLifecycleRecoveryFreezesOriginalRequestAndUsesNewAuditIdentity(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.recovery", "1.0.0", SourceBuiltin)
	item.Status = StatusDisabled
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	authority := LifecycleAuthoritySnapshot{
		SchemaVersion: LifecycleAuthoritySnapshotSchemaV1,
		AuthorityType: LifecycleAuthorityBuiltin,
		ActorUserID:   11,
	}
	authorityJSON, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	failedAt := time.Now().UTC().Add(-time.Minute)
	operation := LifecycleOperation{
		ID: 601, ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
		ArtifactDigests: json.RawMessage(`{"package":"frozen"}`), Operation: string(LifecycleMachineEnable),
		State: LifecycleStateFailed, PlanVersion: item.Manifest.Lifecycle.ContractVersion,
		IdempotencyKey: "original-enable-key", RequestFingerprint: strings.Repeat("f", 64),
		AuthorityType: LifecycleAuthorityBuiltin, AuthoritySnapshot: authorityJSON,
		RequestedByUserID: 11, AuditEventID: 12, AttemptCount: 1, Revision: 4,
		TerminalResult: LifecycleTerminalFailed, CompletedAt: &failedAt,
	}
	repository := &lifecycleRecoveryInspectionRepository{operation: operation}
	runner := &lifecycleV2RecordingRunner{operationID: operation.ID}
	runner.beforeRun = func(input LifecycleCoordinatorRunInput) {
		now := time.Now().UTC()
		repository.operation.State = LifecycleStateEnabled
		repository.operation.TerminalResult = LifecycleTerminalSucceeded
		repository.operation.CompletedAt = &now
		repository.operation.AttemptCount = 2
		repository.operation.RecoveryActorUserID = input.RecoveryActorUserID
		repository.operation.RecoveryAuditEventID = input.RecoveryAuditEventID
		repository.recoveries = append(repository.recoveries, LifecycleRecoveryDecision{
			ID: 701, OperationID: operation.ID, OperationAttempt: 2,
			Decision: LifecycleRecoveryRetry, Reason: input.RecoveryReason,
			ActorUserID: input.RecoveryActorUserID, AuditEventID: input.RecoveryAuditEventID, CreatedAt: now,
		})
	}
	auditor := &lifecycleV2AuditWriter{nextID: 800}
	preflightCalls := 0
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(auditor),
		WithLifecycleInspectionRepository(repository),
		WithLifecycleCoordinator(runner, func(_ context.Context, kind LifecycleMachineOperation, source *Extension, target Extension) error {
			preflightCalls++
			if kind != LifecycleMachineEnable || source != nil || !sameLifecycleExactArtifact(target, item) {
				t.Fatalf("recovery preflight = %q %#v %#v", kind, source, target)
			}
			return nil
		}, lifecycleV2AuthorityStore{operation: &operation}),
	)
	actor := techAdminPluginManager()
	actor.ID = 99

	detail, err := service.RecoverLifecycleOperation(t.Context(), actor, item.ID, operation.ID, LifecycleRecoveryInput{
		Decision: LifecycleRecoveryRetry, Reason: "retry after dependency recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != operation.ID || detail.AttemptCount != 2 || len(detail.Recoveries) != 1 ||
		detail.Recoveries[0].ActorUserID != actor.ID || detail.Recoveries[0].AuditEventID != 801 {
		t.Fatalf("recovery detail = %#v", detail)
	}
	if runner.calls != 1 || preflightCalls != 1 || !runner.input.Retry || !runner.input.Acquire.ExistingOnly ||
		runner.input.SkipFailedStep || runner.input.RecoveryActorUserID != actor.ID || runner.input.RecoveryAuditEventID != 801 {
		t.Fatalf("recovery input = %#v", runner.input)
	}
	if runner.input.Acquire.IdempotencyKey != operation.IdempotencyKey ||
		runner.input.Acquire.RequestFingerprint != operation.RequestFingerprint ||
		runner.input.Acquire.RequestedByUserID != operation.RequestedByUserID ||
		runner.input.Acquire.AuditEventID != operation.AuditEventID ||
		string(runner.input.Acquire.AuthoritySnapshot) != string(operation.AuthoritySnapshot) ||
		string(runner.input.Acquire.ArtifactDigests) != string(operation.ArtifactDigests) {
		t.Fatalf("original request was not frozen: %#v", runner.input.Acquire)
	}
	if len(auditor.events) != 1 || auditor.events[0].Action != audit.ActionExtensionLifecycleRecovery ||
		auditor.events[0].ActorUserID != actor.ID {
		t.Fatalf("recovery audit = %#v", auditor.events)
	}
}

func TestServiceLifecycleRecoveryForcedUninstallRequiresSuperAdminAndFinalizes(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.forced-recovery", "1.0.0", SourceUploaded)
	item.Status = StatusDisabled
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	failedAt := time.Now().UTC().Add(-time.Minute)
	operation := LifecycleOperation{
		ID: 602, ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
		ArtifactDigests: json.RawMessage(`{}`), Operation: string(LifecycleMachineUninstall),
		State: LifecycleStateFailed, PlanVersion: item.Manifest.Lifecycle.ContractVersion,
		IdempotencyKey: "original-uninstall-key", RequestFingerprint: strings.Repeat("e", 64),
		AuthorityType: LifecycleAuthorityTrustGrant, AuthoritySnapshot: json.RawMessage(`{"schemaVersion":"sforum.lifecycle.authority@1"}`),
		RequestedByUserID: 11, AuditEventID: 12, RemovalMode: LifecycleRemovalPreserve,
		AttemptCount: 1, Revision: 4, TerminalResult: LifecycleTerminalFailed, CompletedAt: &failedAt,
	}
	repository := &lifecycleRecoveryInspectionRepository{operation: operation}
	runner := &lifecycleV2RecordingRunner{operationID: operation.ID}
	runner.beforeRun = func(input LifecycleCoordinatorRunInput) {
		now := time.Now().UTC()
		repository.operation.State = LifecycleStateUninstalling
		repository.operation.TerminalResult = LifecycleTerminalSucceeded
		repository.operation.CompletedAt = &now
		repository.operation.AttemptCount = 2
		repository.operation.Forced = true
		repository.operation.RecoveryActorUserID = input.RecoveryActorUserID
		repository.operation.RecoveryAuditEventID = input.RecoveryAuditEventID
		repository.recoveries = []LifecycleRecoveryDecision{{
			ID: 702, OperationID: operation.ID, OperationAttempt: 2,
			Decision: LifecycleRecoverySkipStep, EscalateForced: true, Reason: input.SkipReason,
			ActorUserID: input.RecoveryActorUserID, AuditEventID: input.RecoveryAuditEventID, CreatedAt: now,
		}}
	}
	auditor := &lifecycleV2AuditWriter{nextID: 900}
	finalizerCalls := 0
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(auditor), WithLifecycleInspectionRepository(repository),
		WithLifecycleCoordinator(runner, func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil }, lifecycleV2AuthorityStore{operation: &operation}),
		WithLifecycleCleanupFinalizer(func(_ context.Context, operationID int64) (LifecycleCleanupFinalization, error) {
			finalizerCalls++
			delete(store.items, item.ID)
			return LifecycleCleanupFinalization{OperationID: operationID, Status: "finalized", PhysicalPurgeComplete: true}, nil
		}),
	)
	reason := "external subscription may remain"
	manager := techAdminPluginManager()
	manager.ID = 98
	_, err := service.RecoverLifecycleOperation(t.Context(), manager, item.ID, operation.ID, LifecycleRecoveryInput{
		Decision: LifecycleRecoverySkipStep, Reason: reason, EscalateForced: true,
	})
	if !errors.Is(err, identity.ErrPermissionDenied) || runner.calls != 0 || auditor.calls != 0 {
		t.Fatalf("delegated forced recovery = %v runner=%d audit=%d", err, runner.calls, auditor.calls)
	}

	admin := extensionManager()
	admin.ID = 97
	detail, err := service.RecoverLifecycleOperation(t.Context(), admin, item.ID, operation.ID, LifecycleRecoveryInput{
		Decision: LifecycleRecoverySkipStep, Reason: reason, EscalateForced: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runner.input.Retry || !runner.input.SkipFailedStep || runner.input.SkipReason != reason ||
		!runner.input.EscalateForced || finalizerCalls != 1 || !detail.Forced || len(detail.Recoveries) != 1 ||
		!detail.Recoveries[0].EscalateForced {
		t.Fatalf("forced recovery detail=%#v input=%#v finalizer=%d", detail, runner.input, finalizerCalls)
	}
}

func TestServiceLifecycleRecoveryRejectsClosedSuccessBeforeAudit(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.closed-recovery", "1.0.0", SourceBuiltin)
	completedAt := time.Now().UTC()
	operation := LifecycleOperation{
		ID: 603, ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
		Operation: string(LifecycleMachineEnable), TerminalResult: LifecycleTerminalSucceeded, CompletedAt: &completedAt,
	}
	repository := &lifecycleRecoveryInspectionRepository{operation: operation}
	runner := &lifecycleV2RecordingRunner{}
	auditor := &lifecycleV2AuditWriter{nextID: 1000}
	service := NewServiceWithOptions(newFakeExtensionStore(map[string]Extension{item.ID: item}), t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(auditor), WithLifecycleInspectionRepository(repository),
		WithLifecycleCoordinator(runner, func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil }, lifecycleV2AuthorityStore{}),
	)
	_, err := service.RecoverLifecycleOperation(t.Context(), techAdminPluginManager(), item.ID, operation.ID, LifecycleRecoveryInput{Decision: LifecycleRecoveryRetry})
	if !errors.Is(err, ErrLifecycleNotRecoverable) || runner.calls != 0 || auditor.calls != 0 {
		t.Fatalf("closed success recovery = %v runner=%d audit=%d", err, runner.calls, auditor.calls)
	}
}
