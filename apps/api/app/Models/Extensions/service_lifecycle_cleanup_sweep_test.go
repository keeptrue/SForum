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

type sweepSourceStub struct {
	items []PendingUninstallCleanup
	err   error
}

func (s *sweepSourceStub) PendingUninstallCleanups(
	_ context.Context, extensionID string, _ int,
) ([]PendingUninstallCleanup, error) {
	if s.err != nil {
		return nil, s.err
	}
	items := []PendingUninstallCleanup{}
	for _, item := range s.items {
		if extensionID == "" || item.ExtensionID == extensionID {
			items = append(items, item)
		}
	}
	return items, nil
}

func sweepUninstallOperation(id int64, extensionID string) LifecycleOperation {
	completedAt := time.Now().UTC().Add(-time.Minute)
	return LifecycleOperation{
		ID: id, ExtensionID: extensionID, ExtensionVersion: "1.0.0",
		PackageDigest:   strings.Repeat("a", 64),
		ArtifactDigests: json.RawMessage(`{"package":"` + strings.Repeat("a", 64) + `"}`),
		Operation:       string(LifecycleMachineUninstall), State: LifecycleStateUninstalling,
		PlanVersion: "test.cleanup@1", IdempotencyKey: "sweep-key", RequestFingerprint: strings.Repeat("f", 64),
		AuthorityType: LifecycleAuthorityBuiltin, AuthoritySnapshot: json.RawMessage(`{}`),
		RemovalMode:    LifecycleRemovalPreserve,
		TerminalResult: LifecycleTerminalSucceeded, CompletedAt: &completedAt,
	}
}

func TestFinalizeUninstallCleanupForExtensionRequiresSuperAdmin(t *testing.T) {
	service := NewServiceWithOptions(
		newFakeExtensionStore(map[string]Extension{}), t.TempDir(), "", LocalRuntimeManager{},
		WithLifecycleCleanupSweepSource(&sweepSourceStub{}),
	)
	if _, err := NewCleanupConvergenceService(service).FinalizeUninstallCleanupForExtension(
		t.Context(), techAdminPluginManager(), "demo.plugin",
	); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("non-super-admin finalize = %v", err)
	}
}

func TestFinalizeUninstallCleanupWithoutSweepSourceFailsClosed(t *testing.T) {
	service := NewServiceWithOptions(
		newFakeExtensionStore(map[string]Extension{}), t.TempDir(), "", LocalRuntimeManager{},
	)
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}}
	if _, err := NewCleanupConvergenceService(service).FinalizeUninstallCleanupForExtension(
		t.Context(), actor, "demo.plugin",
	); !errors.Is(err, ErrLifecycleCoordinatorUnavailable) {
		t.Fatalf("missing sweep source = %v", err)
	}
}

func TestFinalizeUninstallCleanupForExtensionIdempotentWhenNothingPending(t *testing.T) {
	finalizerCalls := 0
	service := NewServiceWithOptions(
		newFakeExtensionStore(map[string]Extension{}), t.TempDir(), "", LocalRuntimeManager{},
		WithLifecycleCleanupSweepSource(&sweepSourceStub{}),
		WithLifecycleInspectionRepository(&lifecycleRecoveryInspectionRepository{}),
		WithLifecycleCleanupFinalizer(func(_ context.Context, _ int64) (LifecycleCleanupFinalization, error) {
			finalizerCalls++
			return LifecycleCleanupFinalization{}, nil
		}),
	)
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}}
	result, err := NewCleanupConvergenceService(service).FinalizeUninstallCleanupForExtension(t.Context(), actor, "demo.plugin")
	if err != nil {
		t.Fatal(err)
	}
	if result.Uninstalled || finalizerCalls != 0 {
		t.Fatalf("idempotent finalize result=%#v finalizer=%d", result, finalizerCalls)
	}
}

func TestFinalizeUninstallCleanupForExtensionFinalizesPendingTombstone(t *testing.T) {
	const extensionID = "demo.sweep-finalize"
	operation := sweepUninstallOperation(801, extensionID)
	inspector := &lifecycleRecoveryInspectionRepository{operation: operation}
	finalizerCalls := 0
	auditor := &lifecycleV2AuditWriter{nextID: 900}
	service := NewServiceWithOptions(
		newFakeExtensionStore(map[string]Extension{}), t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(auditor),
		WithLifecycleCleanupSweepSource(&sweepSourceStub{items: []PendingUninstallCleanup{
			{OperationID: operation.ID, ExtensionID: extensionID, RemovalMode: LifecycleRemovalPreserve},
		}}),
		WithLifecycleInspectionRepository(inspector),
		WithLifecycleCleanupFinalizer(func(_ context.Context, operationID int64) (LifecycleCleanupFinalization, error) {
			finalizerCalls++
			if operationID != operation.ID {
				t.Fatalf("finalizer operation = %d", operationID)
			}
			return LifecycleCleanupFinalization{
				OperationID: operationID, Status: "finalized", PhysicalPurgeComplete: true,
			}, nil
		}),
	)
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}}
	result, err := NewCleanupConvergenceService(service).FinalizeUninstallCleanupForExtension(t.Context(), actor, extensionID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Uninstalled || result.OperationID != operation.ID || result.Replayed != true || finalizerCalls != 1 {
		t.Fatalf("finalize result=%#v finalizerCalls=%d", result, finalizerCalls)
	}
	if len(auditor.events) != 1 || auditor.events[0].Action != audit.ActionExtensionUninstalled {
		t.Fatalf("finalize audit = %#v", auditor.events)
	}
}

func TestRunPendingUninstallCleanupSweepIsolatesItemFailures(t *testing.T) {
	operations := []LifecycleOperation{
		sweepUninstallOperation(901, "demo.sweep-ok"),
		sweepUninstallOperation(902, "demo.sweep-fail"),
	}
	inspector := &lifecycleRecoverySweepInspector{operations: operations}
	service := NewServiceWithOptions(
		newFakeExtensionStore(map[string]Extension{}), t.TempDir(), "", LocalRuntimeManager{},
		WithLifecycleCleanupSweepSource(&sweepSourceStub{items: []PendingUninstallCleanup{
			{OperationID: 901, ExtensionID: "demo.sweep-ok", RemovalMode: LifecycleRemovalPreserve},
			{OperationID: 902, ExtensionID: "demo.sweep-fail", RemovalMode: LifecycleRemovalPreserve},
		}}),
		WithLifecycleInspectionRepository(inspector),
		WithLifecycleCleanupFinalizer(func(_ context.Context, operationID int64) (LifecycleCleanupFinalization, error) {
			if operationID == 902 {
				return LifecycleCleanupFinalization{}, errors.New("purge backend unavailable")
			}
			return LifecycleCleanupFinalization{
				OperationID: operationID, Status: "finalized", PhysicalPurgeComplete: true,
			}, nil
		}),
	)
	result, err := NewCleanupConvergenceService(service).RunPendingUninstallCleanupSweep(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Finalized) != 1 || result.Finalized[0] != 901 {
		t.Fatalf("sweep finalized = %#v", result.Finalized)
	}
	if len(result.Failed) != 1 || result.Failed[0].OperationID != 902 {
		t.Fatalf("sweep failures = %#v", result.Failed)
	}
}

func TestRunPendingUninstallCleanupSweepFailsClosedWithoutSource(t *testing.T) {
	service := NewServiceWithOptions(
		newFakeExtensionStore(map[string]Extension{}), t.TempDir(), "", LocalRuntimeManager{},
	)
	if _, err := NewCleanupConvergenceService(service).RunPendingUninstallCleanupSweep(t.Context(), 10); !errors.Is(err, ErrLifecycleCoordinatorUnavailable) {
		t.Fatalf("missing sweep source sweep = %v", err)
	}
}

// lifecycleRecoverySweepInspector 按扩展加载多个 operation，供 sweep 隔离失败测试。
type lifecycleRecoverySweepInspector struct {
	operations []LifecycleOperation
}

func (r *lifecycleRecoverySweepInspector) Operation(_ context.Context, extensionID string, operationID int64) (LifecycleOperation, error) {
	for _, operation := range r.operations {
		if operation.ExtensionID == extensionID && operation.ID == operationID {
			return operation, nil
		}
	}
	return LifecycleOperation{}, ErrLifecycleOperationNotFound
}

func (r *lifecycleRecoverySweepInspector) OpenOperation(context.Context, string) (LifecycleOperation, error) {
	return LifecycleOperation{}, ErrLifecycleOperationNotFound
}

func (r *lifecycleRecoverySweepInspector) ListOperations(_ context.Context, extensionID string, _ int) ([]LifecycleOperation, error) {
	result := []LifecycleOperation{}
	for _, operation := range r.operations {
		if operation.ExtensionID == extensionID {
			result = append(result, operation)
		}
	}
	return result, nil
}

func (r *lifecycleRecoverySweepInspector) ListStepAttempts(context.Context, int64) ([]LifecycleStepAttempt, error) {
	return []LifecycleStepAttempt{}, nil
}

func (r *lifecycleRecoverySweepInspector) ListRecoveryDecisions(context.Context, int64) ([]LifecycleRecoveryDecision, error) {
	return []LifecycleRecoveryDecision{}, nil
}
