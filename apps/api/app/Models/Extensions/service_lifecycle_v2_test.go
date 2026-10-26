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

func TestServiceEnableSelectsExactLifecycleV2Operation(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		staged        bool
		wantOperation LifecycleMachineOperation
		wantVersion   string
		wantSource    bool
	}{
		{name: "first install", status: StatusInstalled, wantOperation: LifecycleMachineInstall, wantVersion: "1.0.0"},
		{name: "first install promotes staged candidate", status: StatusInstalled, staged: true, wantOperation: LifecycleMachineInstall, wantVersion: "2.0.0"},
		{name: "disabled restarts current artifact", status: StatusDisabled, staged: true, wantOperation: LifecycleMachineEnable, wantVersion: "1.0.0"},
		{name: "enabled activates staged upgrade", status: StatusEnabled, staged: true, wantOperation: LifecycleMachineUpgrade, wantVersion: "2.0.0", wantSource: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := lifecycleV2ServiceArtifact(t, "service.mapping", "1.0.0", SourceBuiltin)
			current.Status = test.status
			if test.staged {
				candidate := lifecycleV2ServiceArtifact(t, current.ID, "2.0.0", SourceBuiltin)
				current.StagedVersion = &ExtensionVersion{
					ID: 22, Version: candidate.Version, Manifest: candidate.Manifest,
					PackageDigest: candidate.PackageDigest, PackagePath: candidate.PackagePath,
					InstalledAt: candidate.InstalledAt,
				}
			}
			store := newFakeExtensionStore(map[string]Extension{current.ID: current})
			runner := &lifecycleV2RecordingRunner{}
			auditor := &lifecycleV2AuditWriter{nextID: 80}
			service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
				WithAuditor(auditor),
				WithExecutableTrust(NewExecutableTrustService(store, &memoryExecutableTrustStore{}), true),
				WithLifecycleCoordinator(runner, func(context.Context, LifecycleMachineOperation, *Extension, Extension) error {
					return nil
				}, lifecycleV2AuthorityStore{}),
			)

			_, err := service.Enable(t.Context(), extensionManager(), current.ID, EnableInput{IdempotencyKey: "mapping-request"})
			if err != nil {
				t.Fatal(err)
			}
			if runner.calls != 1 || LifecycleMachineOperation(runner.input.Acquire.Operation) != test.wantOperation ||
				runner.input.Extension.Version != test.wantVersion || (runner.input.SourceExtension != nil) != test.wantSource {
				t.Fatalf("coordinator input = %#v", runner.input)
			}
			if test.wantSource && runner.input.SourceExtension.Version != "1.0.0" {
				t.Fatalf("source version = %q", runner.input.SourceExtension.Version)
			}
		})
	}
}

func TestServiceLifecycleV2RequiresStableIdempotencyBeforeAuditOrPreflight(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.idempotency", "1.0.0", SourceBuiltin)
	item.Status = StatusInstalled
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	runner := &lifecycleV2RecordingRunner{}
	auditor := &lifecycleV2AuditWriter{nextID: 10}
	preflightCalls := 0
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(auditor),
		WithExecutableTrust(NewExecutableTrustService(store, &memoryExecutableTrustStore{}), true),
		WithLifecycleCoordinator(runner, func(context.Context, LifecycleMachineOperation, *Extension, Extension) error {
			preflightCalls++
			return nil
		}, lifecycleV2AuthorityStore{}),
	)

	_, err := service.Enable(t.Context(), extensionManager(), item.ID, EnableInput{})
	if !errors.Is(err, ErrLifecycleCoordinatorInvalid) {
		t.Fatalf("missing key error = %v", err)
	}
	if runner.calls != 0 || auditor.calls != 0 || preflightCalls != 0 {
		t.Fatalf("side effects before key validation: runner=%d audit=%d preflight=%d", runner.calls, auditor.calls, preflightCalls)
	}
}

func TestServiceDisableUsesFrozenAuthorityWithoutLiveGrantAndCurrentRequestActor(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.disable", "1.0.0", SourceUploaded)
	item.Status = StatusEnabled
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	authority := lifecycleAuthorityTestGrant(t, item)
	authorities := lifecycleV2AuthorityStore{authority: authority}
	order := []string{}
	runner := &lifecycleV2RecordingRunner{beforeRun: func(input LifecycleCoordinatorRunInput) {
		order = append(order, "run")
		stored := store.items[item.ID]
		stored.Status = StatusDisabled
		store.items[item.ID] = stored
	}}
	auditor := &lifecycleV2AuditWriter{nextID: 90, onAppend: func() { order = append(order, "audit") }}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(auditor),
		WithLifecycleCoordinator(runner, func(_ context.Context, operation LifecycleMachineOperation, source *Extension, target Extension) error {
			order = append(order, "preflight")
			if operation != LifecycleMachineDisable || source == nil || !sameLifecycleExactArtifact(*source, target) {
				t.Fatalf("disable preflight artifacts: operation=%q source=%#v target=%#v", operation, source, target)
			}
			return nil
		}, authorities),
	)
	actor := identity.Actor{
		ID: 99, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionExtensionPluginManage: true},
	}

	disabled, err := service.DisableWithInput(t.Context(), actor, item.ID, LifecycleRequestInput{IdempotencyKey: "disable-request"})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != StatusDisabled || store.disabledID != "" {
		t.Fatalf("V2 disable used legacy mutation: status=%q legacyID=%q", disabled.Status, store.disabledID)
	}
	if got := strings.Join(order, ","); got != "audit,preflight,run" {
		t.Fatalf("execution order = %q", got)
	}
	if runner.input.Acquire.RequestedByUserID != actor.ID || runner.input.Acquire.AuditEventID != 91 {
		t.Fatalf("request authority split = %#v", runner.input.Acquire)
	}
	if lifecycleOperationAuthorityActorUserID(LifecycleOperation{
		AuthoritySnapshot: runner.input.Acquire.AuthoritySnapshot,
		RequestedByUserID: runner.input.Acquire.RequestedByUserID,
	}) != authority.ActorUserID {
		t.Fatal("frozen authority actor was replaced by current request actor")
	}
	if len(store.events) != 1 || store.events[0].Action != EventDisabled {
		t.Fatalf("compatibility events = %#v", store.events)
	}
}

func TestServiceRollbackBindsExactHistoricalTargetAndFrozenAuthority(t *testing.T) {
	source := lifecycleV2ServiceArtifact(t, "service.rollback", "2.0.0", SourceUploaded)
	source.Status = StatusEnabled
	target := lifecycleV2ServiceArtifact(t, source.ID, "1.0.0", SourceUploaded)
	target.Status = StatusEnabled
	target.ActiveVersionID = 10
	version := ExtensionVersion{
		ID: target.ActiveVersionID, Version: target.Version, Manifest: target.Manifest,
		PackageDigest: target.PackageDigest, AdminFrontendDigest: target.AdminFrontendDigest,
		PackagePath: target.PackagePath, InstalledAt: target.InstalledAt,
	}
	baseStore := newFakeExtensionStore(map[string]Extension{source.ID: source})
	store := &lifecycleV2VersionStore{fakeExtensionStore: baseStore, versions: []ExtensionVersion{version}}
	authority := lifecycleAuthorityTestGrant(t, target)
	runner := &lifecycleV2RecordingRunner{beforeRun: func(LifecycleCoordinatorRunInput) {
		baseStore.items[source.ID] = target
	}}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(&lifecycleV2AuditWriter{nextID: 120}),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil },
			lifecycleV2AuthorityStore{authority: authority},
		),
	)

	rolledBack, err := service.Rollback(t.Context(), extensionManager(), source.ID, RollbackInput{
		TargetVersion: target.Version, TargetPackageDigest: target.PackageDigest, IdempotencyKey: "rollback-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Version != target.Version || runner.input.Extension.Version != target.Version ||
		runner.input.SourceExtension == nil || runner.input.SourceExtension.Version != source.Version ||
		runner.input.Acquire.Operation != string(LifecycleMachineRollback) {
		t.Fatalf("rollback result=%#v input=%#v", rolledBack, runner.input)
	}
}

func TestServiceLifecycleV2ReplayDoesNotRepeatCompatibilityEvent(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.replay", "1.0.0", SourceBuiltin)
	item.Status = StatusInstalled
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	runner := &lifecycleV2RecordingRunner{replayed: true}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(&lifecycleV2AuditWriter{nextID: 20}),
		WithExecutableTrust(NewExecutableTrustService(store, &memoryExecutableTrustStore{}), true),
		WithLifecycleCoordinator(runner, func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil }, lifecycleV2AuthorityStore{}),
	)
	if _, err := service.Enable(t.Context(), extensionManager(), item.ID, EnableInput{IdempotencyKey: "replay-request"}); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 0 {
		t.Fatalf("replay repeated compatibility event: %#v", store.events)
	}
}

func TestServiceDisableReplayUsesPersistedOperationAfterStateChanged(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.disable-replay", "1.0.0", SourceBuiltin)
	item.Status = StatusDisabled // The first request already published deactivation.
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	authority := LifecycleAuthoritySnapshot{
		SchemaVersion: LifecycleAuthoritySnapshotSchemaV1,
		AuthorityType: LifecycleAuthorityBuiltin,
		ActorUserID:   extensionManager().ID,
	}
	authorityJSON, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	operation := LifecycleOperation{
		ID: 70, ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
		Operation: string(LifecycleMachineDisable), PlanVersion: item.Manifest.Lifecycle.ContractVersion,
		IdempotencyKey: "disable-replay", RequestFingerprint: strings.Repeat("f", 64),
		AuthorityType: LifecycleAuthorityBuiltin, AuthoritySnapshot: authorityJSON,
		RequestedByUserID: extensionManager().ID, AuditEventID: 71,
		TerminalResult: LifecycleTerminalSucceeded,
	}
	completedAt := time.Now().UTC()
	operation.CompletedAt = &completedAt
	runner := &lifecycleV2RecordingRunner{replayed: true}
	auditor := &lifecycleV2AuditWriter{nextID: 100}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(auditor),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil },
			lifecycleV2AuthorityStore{operation: &operation},
		),
	)

	got, err := service.DisableWithInput(t.Context(), extensionManager(), item.ID, LifecycleRequestInput{IdempotencyKey: operation.IdempotencyKey})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusDisabled || runner.calls != 0 || auditor.calls != 0 || len(store.events) != 0 {
		t.Fatalf("disable replay: item=%#v input=%#v audit=%d events=%#v", got, runner.input, auditor.calls, store.events)
	}
}

func TestServiceUninstallLifecycleV2UsesFrozenAuthorityAndExactFinalizer(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
		want string
	}{
		{name: "safe default", want: LifecycleRemovalPreserve},
		{name: "preserve", mode: LifecycleRemovalPreserve, want: LifecycleRemovalPreserve},
		{name: "export then remove", mode: LifecycleRemovalExportThenRemove, want: LifecycleRemovalExportThenRemove},
		{name: "complete removal", mode: LifecycleRemovalComplete, want: LifecycleRemovalComplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := lifecycleV2ServiceArtifact(t, "service.uninstall."+strings.ReplaceAll(test.name, " ", "-"), "1.0.0", SourceUploaded)
			item.Status = StatusEnabled
			store := newFakeExtensionStore(map[string]Extension{item.ID: item})
			authority := lifecycleAuthorityTestGrant(t, item)
			order := []string{}
			runner := &lifecycleV2RecordingRunner{operationID: 401, beforeRun: func(input LifecycleCoordinatorRunInput) {
				order = append(order, "coordinator")
				stored := store.items[item.ID]
				stored.Status = StatusDisabled
				store.items[item.ID] = stored
			}}
			auditor := &lifecycleV2AuditWriter{nextID: 500, onAppend: func() { order = append(order, "audit") }}
			finalizerCalls := 0
			service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
				WithAuditor(auditor),
				WithLifecycleCoordinator(
					runner,
					func(_ context.Context, operation LifecycleMachineOperation, source *Extension, target Extension) error {
						order = append(order, "preflight")
						if operation != LifecycleMachineUninstall || source == nil || !sameLifecycleExactArtifact(*source, target) {
							t.Fatalf("uninstall preflight = %q %#v %#v", operation, source, target)
						}
						return nil
					},
					lifecycleV2AuthorityStore{authority: authority},
				),
				WithLifecycleCleanupFinalizer(func(_ context.Context, operationID int64) (LifecycleCleanupFinalization, error) {
					order = append(order, "finalizer")
					finalizerCalls++
					if operationID != 401 {
						t.Fatalf("finalizer operation = %d", operationID)
					}
					if _, ok := store.items[item.ID]; !ok {
						t.Fatal("extension identity was deleted before terminal finalizer")
					}
					delete(store.items, item.ID)
					return LifecycleCleanupFinalization{OperationID: operationID, Status: "finalized", PhysicalPurgeComplete: true}, nil
				}),
			)
			actor := techAdminPluginManager()
			actor.ID = 77

			result, err := service.UninstallWithResult(t.Context(), actor, item.ID, UninstallInput{
				RemovalMode: test.mode, IdempotencyKey: "uninstall-request-" + strings.ReplaceAll(test.name, " ", "-"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Uninstalled || result.OperationID != 401 || result.RemovalMode != test.want ||
				result.Cleanup == nil || !result.Cleanup.PhysicalPurgeComplete || finalizerCalls != 1 {
				t.Fatalf("uninstall result = %#v", result)
			}
			if _, ok := store.items[item.ID]; ok || store.disabledID != "" {
				t.Fatalf("legacy mutation ran before exact finalizer: disabled=%q itemPresent=%v", store.disabledID, ok)
			}
			if runner.input.Acquire.Operation != string(LifecycleMachineUninstall) ||
				runner.input.Acquire.RemovalMode != test.want || runner.input.Acquire.RequestedByUserID != actor.ID ||
				runner.input.SourceExtension == nil || !sameLifecycleExactArtifact(*runner.input.SourceExtension, item) ||
				lifecycleOperationAuthorityActorUserID(LifecycleOperation{
					AuthoritySnapshot: runner.input.Acquire.AuthoritySnapshot,
					RequestedByUserID: runner.input.Acquire.RequestedByUserID,
				}) != authority.ActorUserID {
				t.Fatalf("uninstall coordinator input = %#v", runner.input)
			}
			if got := strings.Join(order, ","); got != "audit,preflight,coordinator,finalizer" {
				t.Fatalf("uninstall order = %q", got)
			}
			if len(auditor.events) != 1 || auditor.events[0].Action != audit.ActionExtensionUninstalled {
				t.Fatalf("uninstall audit = %#v", auditor.events)
			}
		})
	}
}

func TestServiceUninstallLifecycleV2TerminalReplayRetriesOnlyFinalizer(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.uninstall-replay", "1.0.0", SourceUploaded)
	item.Status = StatusDisabled
	authority := lifecycleAuthorityTestGrant(t, item)
	authorityJSON, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	operation := LifecycleOperation{
		ID: 402, ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
		Operation: string(LifecycleMachineUninstall), PlanVersion: item.Manifest.Lifecycle.ContractVersion,
		IdempotencyKey: "uninstall-replay", RequestFingerprint: strings.Repeat("a", 64),
		AuthorityType: authority.AuthorityType, TrustGrantID: authority.Grant.ID, AuthoritySnapshot: authorityJSON,
		RequestedByUserID: 88, AuditEventID: 91, RemovalMode: LifecycleRemovalPreserve,
		TerminalResult: LifecycleTerminalSucceeded, CompletedAt: &completedAt,
	}
	store := newFakeExtensionStore(map[string]Extension{}) // Physical identity may already be gone.
	runner := &lifecycleV2RecordingRunner{}
	auditor := &lifecycleV2AuditWriter{nextID: 100}
	finalizerCalls := 0
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(auditor),
		WithLifecycleCoordinator(runner, func(context.Context, LifecycleMachineOperation, *Extension, Extension) error {
			t.Fatal("terminal uninstall replay ran preflight")
			return nil
		}, lifecycleV2AuthorityStore{operation: &operation}),
		WithLifecycleCleanupFinalizer(func(_ context.Context, operationID int64) (LifecycleCleanupFinalization, error) {
			finalizerCalls++
			return LifecycleCleanupFinalization{OperationID: operationID, Status: "finalized", PhysicalPurgeComplete: true}, nil
		}),
	)
	actor := techAdminPluginManager()
	actor.ID = operation.RequestedByUserID

	result, err := service.UninstallWithResult(t.Context(), actor, item.ID, UninstallInput{IdempotencyKey: operation.IdempotencyKey})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.OperationID != operation.ID || finalizerCalls != 1 || runner.calls != 0 || auditor.calls != 0 {
		t.Fatalf("terminal replay result=%#v finalizer=%d runner=%d audit=%d", result, finalizerCalls, runner.calls, auditor.calls)
	}
	if _, err := service.UninstallWithResult(t.Context(), actor, item.ID, UninstallInput{
		IdempotencyKey: operation.IdempotencyKey, RemovalMode: LifecycleRemovalComplete,
	}); !errors.Is(err, ErrLifecycleFingerprintConflict) {
		t.Fatalf("changed replay removal mode = %v", err)
	}
}

func TestServiceUninstallLifecycleV2FailureRetainsArtifactForRecovery(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.uninstall-failure", "1.0.0", SourceUploaded)
	item.Status = StatusEnabled
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	finalizerCalls := 0
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(&lifecycleV2AuditWriter{nextID: 200}),
		WithLifecycleCoordinator(
			&lifecycleV2RecordingRunner{err: ErrLifecycleCoordinatorActionFailed},
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil },
			lifecycleV2AuthorityStore{authority: lifecycleAuthorityTestGrant(t, item)},
		),
		WithLifecycleCleanupFinalizer(func(context.Context, int64) (LifecycleCleanupFinalization, error) {
			finalizerCalls++
			return LifecycleCleanupFinalization{}, nil
		}),
	)
	actor := techAdminPluginManager()

	_, err := service.UninstallWithResult(t.Context(), actor, item.ID, UninstallInput{IdempotencyKey: "uninstall-failure"})
	if !errors.Is(err, ErrLifecycleCoordinatorActionFailed) {
		t.Fatalf("uninstall failure = %v", err)
	}
	if _, ok := store.items[item.ID]; !ok || finalizerCalls != 0 || store.disabledID != "" {
		t.Fatalf("failed uninstall lost recovery state: present=%v finalizer=%d disabled=%q", ok, finalizerCalls, store.disabledID)
	}
}

type lifecycleV2RecordingRunner struct {
	input       LifecycleCoordinatorRunInput
	calls       int
	replayed    bool
	operationID int64
	err         error
	beforeRun   func(LifecycleCoordinatorRunInput)
}

func (r *lifecycleV2RecordingRunner) Run(_ context.Context, input LifecycleCoordinatorRunInput) (LifecycleCoordinatorRunResult, error) {
	r.calls++
	r.input = input
	if r.beforeRun != nil {
		r.beforeRun(input)
	}
	if r.err != nil {
		return LifecycleCoordinatorRunResult{}, r.err
	}
	completedAt := time.Now().UTC()
	return LifecycleCoordinatorRunResult{
		Operation: LifecycleOperation{
			ID: r.operationID, ExtensionID: input.Acquire.ExtensionID,
			ExtensionVersion: input.Acquire.ExtensionVersion, PackageDigest: input.Acquire.PackageDigest,
			Operation: input.Acquire.Operation, RemovalMode: input.Acquire.RemovalMode,
			RequestedByUserID: input.Acquire.RequestedByUserID, AuditEventID: input.Acquire.AuditEventID,
			TerminalResult: LifecycleTerminalSucceeded, CompletedAt: &completedAt,
		},
		Replayed: r.replayed,
	}, nil
}

type lifecycleV2AuthorityStore struct {
	authority LifecycleAuthoritySnapshot
	err       error
	operation *LifecycleOperation
}

func (s lifecycleV2AuthorityStore) OperationByIdempotencyKey(context.Context, string, string) (LifecycleOperation, error) {
	if s.operation == nil {
		return LifecycleOperation{}, ErrLifecycleOperationNotFound
	}
	return *s.operation, s.err
}

func (s lifecycleV2AuthorityStore) LastSuccessfulLifecycleAuthority(context.Context, ExactExtensionVersionInput) (LifecycleAuthoritySnapshot, error) {
	return s.authority, s.err
}

type lifecycleV2AuditWriter struct {
	nextID   int64
	calls    int
	onAppend func()
	events   []audit.Event
}

type lifecycleV2VersionStore struct {
	*fakeExtensionStore
	versions []ExtensionVersion
}

func (s *lifecycleV2VersionStore) RollbackExtensionVersion(context.Context, RollbackExtensionVersionInput) (Extension, error) {
	return Extension{}, errors.New("unexpected legacy rollback mutation")
}

func (s *lifecycleV2VersionStore) GetExtensionVersion(_ context.Context, input ExactExtensionVersionInput) (ExtensionVersion, error) {
	for _, version := range s.versions {
		if input.ExtensionID == s.fakeExtensionStore.items[input.ExtensionID].ID &&
			version.Version == input.Version && version.PackageDigest == input.PackageDigest {
			return version, nil
		}
	}
	return ExtensionVersion{}, ErrExtensionVersionNotFound
}

func (s *lifecycleV2VersionStore) ListExtensionVersions(context.Context, string) ([]ExtensionVersion, error) {
	return append([]ExtensionVersion(nil), s.versions...), nil
}

func (w *lifecycleV2AuditWriter) Append(ctx context.Context, event audit.Event) error {
	_, err := w.AppendReturningID(ctx, event)
	return err
}

func (w *lifecycleV2AuditWriter) AppendReturningID(_ context.Context, event audit.Event) (int64, error) {
	w.calls++
	w.events = append(w.events, event)
	if w.onAppend != nil {
		w.onAppend()
	}
	w.nextID++
	return w.nextID, nil
}

func lifecycleV2ServiceArtifact(t *testing.T, id, version, source string) Extension {
	t.Helper()
	item := lifecycleAuthorityTestExtension(t, id, source)
	item.Version = version
	item.Manifest.Version = version
	item.Manifest.Backend.ProtocolVersion = 2
	item.ActiveVersionID = 11
	refreshTrustPackageIdentity(t, &item)
	return item
}
