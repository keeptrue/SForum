package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type lifecycleProviderSelectionStore struct {
	*fakeExtensionStore
	selectedMail string
	restoreCalls int
	selectErr    error
	restoreErr   error
}

func (s *lifecycleProviderSelectionStore) SelectedMailProvider(context.Context) (string, error) {
	return s.selectedMail, s.selectErr
}

func (s *lifecycleProviderSelectionStore) RestoreMailProvider(context.Context) error {
	s.restoreCalls++
	if s.restoreErr != nil {
		return s.restoreErr
	}
	s.selectedMail = ""
	return nil
}

type lifecycleStorageSelectionClearer struct {
	calls     []string
	failCalls int
	err       error
}

type lifecycleRouteSelectionInvalidator struct {
	calls  []string
	err    error
	onCall func()
}

type lifecycleProviderSlotSelectionInvalidator struct {
	calls  []string
	err    error
	onCall func()
}

func (i *lifecycleProviderSlotSelectionInvalidator) InvalidateProviderSlotSelections(
	_ context.Context, extensionID string, actorUserID, auditEventID int64, reasonCode string,
) error {
	i.calls = append(i.calls, fmt.Sprintf("%s:%d:%d:%s", extensionID, actorUserID, auditEventID, reasonCode))
	if i.onCall != nil {
		i.onCall()
	}
	return i.err
}

func (i *lifecycleRouteSelectionInvalidator) InvalidateRouteProviderSelections(
	_ context.Context, extensionID string, actorUserID, auditEventID int64, reasonCode string,
) error {
	i.calls = append(i.calls, fmt.Sprintf("%s:%d:%d:%s", extensionID, actorUserID, auditEventID, reasonCode))
	if i.onCall != nil {
		i.onCall()
	}
	return i.err
}

func (c *lifecycleStorageSelectionClearer) ClearStorageProviderSelectionIfMatch(_ context.Context, extensionID string) error {
	c.calls = append(c.calls, extensionID)
	if len(c.calls) <= c.failCalls {
		return c.err
	}
	return nil
}

func TestRemovedBuiltinPruneClearsHostProviderSelections(t *testing.T) {
	removed := lifecycleV2ServiceArtifact(t, "builtin.removed-provider", "1.0.0", SourceBuiltin)
	kept := lifecycleV2ServiceArtifact(t, "builtin.kept-provider", "1.0.0", SourceBuiltin)
	store := &lifecycleProviderSelectionStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{
			removed.ID: removed,
			kept.ID:    kept,
		}),
		selectedMail: removed.ID,
	}
	storage := &lifecycleStorageSelectionClearer{}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", nil,
		WithStorageSelectionClearer(storage),
	)

	if err := service.catalog.pruneMissingBuiltins(t.Context(), []string{kept.ID}); err != nil {
		t.Fatal(err)
	}
	if store.selectedMail != "" || store.restoreCalls != 1 {
		t.Fatalf("mail selection=%q restoreCalls=%d", store.selectedMail, store.restoreCalls)
	}
	if len(storage.calls) != 1 || storage.calls[0] != removed.ID {
		t.Fatalf("storage selection calls=%v", storage.calls)
	}
}

func TestLegacyProviderCleanupDoesNotRequireV2RouteAuditEvidence(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "legacy-provider-cleanup", "1.0.0", SourceBuiltin)
	item.Status = StatusEnabled
	base := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store := &lifecycleProviderSelectionStore{fakeExtensionStore: base, selectedMail: item.ID}
	routes := &lifecycleRouteSelectionInvalidator{}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", &fakeRuntimeManager{},
		WithRouteProviderSelectionInvalidator(routes),
	)
	if err := service.clearPluginProviderSelections(t.Context(), item.ID, clearMailProviderSelection); err != nil {
		t.Fatal(err)
	}
	if len(routes.calls) != 0 || store.selectedMail != "" {
		t.Fatalf("legacy cleanup route calls=%#v mail=%q", routes.calls, store.selectedMail)
	}
}

func TestLifecycleCleanupInvalidatesProviderSlotsBeforeRoutesAndLegacySelections(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.provider-slot-cleanup", "1.0.0", SourceBuiltin)
	item.Status = StatusEnabled
	store := &lifecycleProviderSelectionStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		selectedMail:       item.ID,
	}
	routeSelections := &lifecycleRouteSelectionInvalidator{}
	providerSlots := &lifecycleProviderSlotSelectionInvalidator{onCall: func() {
		if len(routeSelections.calls) != 0 || store.selectedMail == "" {
			t.Fatal("provider slot invalidation did not run before route and legacy cleanup")
		}
	}}
	routeSelections.onCall = func() {
		if len(providerSlots.calls) != 1 || store.selectedMail == "" {
			t.Fatal("route invalidation ran outside provider selection ordering")
		}
	}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", &fakeRuntimeManager{},
		WithProviderSlotSelectionInvalidator(providerSlots),
		WithRouteProviderSelectionInvalidator(routeSelections),
	)
	if err := service.clearPluginProviderSelectionsWithAudit(
		t.Context(), item.ID, 17, 23, "extension_disabled", clearMailProviderSelection,
	); err != nil {
		t.Fatal(err)
	}
	if len(providerSlots.calls) != 1 || len(routeSelections.calls) != 1 || store.selectedMail != "" || store.restoreCalls != 1 {
		t.Fatalf("slot=%#v route=%#v mail=%q restores=%d", providerSlots.calls, routeSelections.calls, store.selectedMail, store.restoreCalls)
	}
}

func TestServiceLifecycleV2DisableKeepsMailProviderSelectionWithoutLegacyRuntimeStop(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.provider-disable", "1.0.0", SourceBuiltin)
	item.Status = StatusEnabled
	base := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store := &lifecycleProviderSelectionStore{fakeExtensionStore: base, selectedMail: item.ID}
	storage := &lifecycleStorageSelectionClearer{}
	routeSelections := &lifecycleRouteSelectionInvalidator{onCall: func() {
		if store.selectedMail != item.ID || len(storage.calls) != 0 {
			t.Fatal("route selection was not invalidated before other provider cleanup")
		}
	}}
	runtime := &fakeRuntimeManager{}
	runner := &lifecycleV2RecordingRunner{operationID: 801, beforeRun: func(LifecycleCoordinatorRunInput) {
		published := store.items[item.ID]
		published.Status = StatusDisabled
		store.items[item.ID] = published
	}}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", runtime,
		WithAuditor(&lifecycleV2AuditWriter{nextID: 900}),
		WithStorageSelectionClearer(storage),
		WithRouteProviderSelectionInvalidator(routeSelections),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil },
			lifecycleV2AuthorityStore{authority: lifecycleAuthorityTestGrant(t, item)},
		),
	)

	disabled, err := service.DisableWithInput(
		t.Context(), extensionManager(), item.ID,
		LifecycleRequestInput{IdempotencyKey: "provider-disable"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != StatusDisabled || store.selectedMail != item.ID || store.restoreCalls != 0 ||
		len(storage.calls) != 1 || storage.calls[0] != item.ID || len(routeSelections.calls) != 1 ||
		!strings.HasSuffix(routeSelections.calls[0], ":extension_disabled") {
		t.Fatalf("disabled=%#v mail=%q restore=%d storage=%#v", disabled, store.selectedMail, store.restoreCalls, storage.calls)
	}
	if len(runtime.stopped) != 0 {
		t.Fatalf("V2 service used legacy runtime stop: %#v", runtime.stopped)
	}
}

func TestServiceLifecycleV2DisableReplayRetriesProviderCleanup(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.provider-disable-replay", "1.0.0", SourceBuiltin)
	item.Status = StatusEnabled
	base := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store := &lifecycleProviderSelectionStore{fakeExtensionStore: base, selectedMail: item.ID}
	cleanupFailure := errors.New("attachment option unavailable")
	storage := &lifecycleStorageSelectionClearer{failCalls: 1, err: cleanupFailure}
	routeSelections := &lifecycleRouteSelectionInvalidator{}
	runner := &lifecycleV2RecordingRunner{operationID: 802, beforeRun: func(LifecycleCoordinatorRunInput) {
		published := store.items[item.ID]
		published.Status = StatusDisabled
		store.items[item.ID] = published
	}}
	actor := extensionManager()
	service := NewServiceWithOptions(
		store, t.TempDir(), "", &fakeRuntimeManager{},
		WithAuditor(&lifecycleV2AuditWriter{nextID: 910}), WithStorageSelectionClearer(storage),
		WithRouteProviderSelectionInvalidator(routeSelections),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil },
			lifecycleV2AuthorityStore{authority: lifecycleAuthorityTestGrant(t, item)},
		),
	)
	if _, err := service.DisableWithInput(
		t.Context(), actor, item.ID, LifecycleRequestInput{IdempotencyKey: "provider-disable-replay"},
	); !errors.Is(err, cleanupFailure) {
		t.Fatalf("first cleanup error = %v", err)
	}

	completedAt := time.Now().UTC()
	operation := LifecycleOperation{
		ID: 802, ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
		Operation: string(LifecycleMachineDisable), IdempotencyKey: "provider-disable-replay",
		RequestedByUserID: actor.ID, TerminalResult: LifecycleTerminalSucceeded, CompletedAt: &completedAt,
		AuditEventID: 911,
	}
	replayRunner := &lifecycleV2RecordingRunner{}
	replay := NewServiceWithOptions(
		store, t.TempDir(), "", &fakeRuntimeManager{},
		WithAuditor(&lifecycleV2AuditWriter{nextID: 920}), WithStorageSelectionClearer(storage),
		WithRouteProviderSelectionInvalidator(routeSelections),
		WithLifecycleCoordinator(
			replayRunner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error {
				t.Fatal("completed replay ran preflight")
				return nil
			},
			lifecycleV2AuthorityStore{authority: lifecycleAuthorityTestGrant(t, item), operation: &operation},
		),
	)
	disabled, err := replay.DisableWithInput(
		t.Context(), actor, item.ID, LifecycleRequestInput{IdempotencyKey: operation.IdempotencyKey},
	)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != StatusDisabled || len(storage.calls) != 2 || replayRunner.calls != 0 ||
		store.restoreCalls != 0 || store.selectedMail != item.ID || len(routeSelections.calls) != 2 {
		t.Fatalf("replay disabled=%#v storage=%#v runner=%d restore=%d mail=%q",
			disabled, storage.calls, replayRunner.calls, store.restoreCalls, store.selectedMail)
	}
}

func TestServiceLifecycleV2UninstallClearsProvidersBeforePhysicalFinalizer(t *testing.T) {
	item := lifecycleV2ServiceArtifact(t, "service.provider-uninstall", "1.0.0", SourceUploaded)
	item.Status = StatusDisabled
	base := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store := &lifecycleProviderSelectionStore{fakeExtensionStore: base, selectedMail: item.ID}
	storage := &lifecycleStorageSelectionClearer{}
	routeSelections := &lifecycleRouteSelectionInvalidator{}
	runner := &lifecycleV2RecordingRunner{operationID: 803}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", &fakeRuntimeManager{},
		WithAuditor(&lifecycleV2AuditWriter{nextID: 930}), WithStorageSelectionClearer(storage),
		WithRouteProviderSelectionInvalidator(routeSelections),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil },
			lifecycleV2AuthorityStore{authority: lifecycleAuthorityTestGrant(t, item)},
		),
		WithLifecycleCleanupFinalizer(func(_ context.Context, operationID int64) (LifecycleCleanupFinalization, error) {
			if store.selectedMail != "" || len(storage.calls) != 1 || len(routeSelections.calls) != 1 {
				t.Fatalf("physical finalizer ran before provider cleanup: mail=%q storage=%#v", store.selectedMail, storage.calls)
			}
			delete(store.items, item.ID)
			return LifecycleCleanupFinalization{OperationID: operationID, Status: "finalized", PhysicalPurgeComplete: true}, nil
		}),
	)

	result, err := service.UninstallWithResult(t.Context(), extensionManager(), item.ID, UninstallInput{
		IdempotencyKey: "provider-uninstall", RemovalMode: LifecycleRemovalPreserve,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Uninstalled || store.restoreCalls != 1 || len(storage.calls) != 1 ||
		len(routeSelections.calls) != 1 || !strings.HasSuffix(routeSelections.calls[0], ":extension_uninstalled") {
		t.Fatalf("uninstall=%#v restore=%d storage=%#v", result, store.restoreCalls, storage.calls)
	}
}
