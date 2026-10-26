package extensions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

func TestRestartLegacyIdentityPluginQuarantinesBeforeRepublish(t *testing.T) {
	item := legacyPluginRuntimeServiceFixture(t, StatusEnabled)
	item.Manifest.Identity = &ManifestIdentity{
		ContractVersion: item.ID + ".identity@1",
		Providers: []ManifestIdentityProvider{{
			ID: item.ID + ".auth", ContractVersion: item.ID + ".auth@1",
			Kind: "auth", Handler: item.ID + ".auth",
		}},
	}
	item.Source = SourceBuiltin
	item.IsSystem = true
	item.IsDeletable = false
	refreshTrustPackageIdentity(t, &item)

	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		events:             &events,
	}
	store := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	runtime := &orderedQueryRuntime{events: &events}
	identityPublications := &recordingRuntimeIdentityPublicationBoundary{events: &events}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime).
		BindRuntimeIdentityPublications(identityPublications)
	WithAuditor(&recordingAuditWriter{events: &events})(service)

	restarted, err := service.Restart(t.Context(), extensionManager(), item.ID, RestartInput{
		IdempotencyKey: "restart-legacy-identity",
	})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Status != StatusEnabled || identityPublications.quarantineCalls != 1 ||
		identityPublications.publishCalls != 1 {
		t.Fatalf("restart result=%+v identity=%+v", restarted, identityPublications)
	}
	want := []string{
		"audit.extension.disable", "identity.quarantine",
		"desired.disable", "store.disable", "runtime.stop",
		"desired.enable", "store.enable", "runtime.start",
		"audit.extension.enable", "identity.publish",
		"audit.extension.restart",
	}
	if !slices.Equal(events, want) {
		t.Fatalf("restart publication order=%v", events)
	}
}

func TestRestartLegacyPluginPromotesExactLifecycleV2Target(t *testing.T) {
	current, candidate := restartLegacyToLifecycleV2Fixture(t)
	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{current.ID: current}),
		events:             &events,
	}
	legacyStore := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	store := &restartRecordingStore{recordingLegacyPluginRuntimeStore: legacyStore, events: &events}
	runner := &lifecycleV2RecordingRunner{beforeRun: func(input LifecycleCoordinatorRunInput) {
		events = append(events, "lifecycle."+input.Acquire.Operation)
		item := base.items[current.ID]
		item.Status = StatusEnabled
		base.items[current.ID] = item
	}}
	service := NewServiceWithOptions(store, t.TempDir(), "", &orderedQueryRuntime{events: &events},
		WithAuditor(&recordingAuditWriter{events: &events}),
		WithExecutableTrust(NewExecutableTrustService(store, &memoryExecutableTrustStore{}), true),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil },
			lifecycleV2AuthorityStore{},
		),
	)

	restarted, err := service.Restart(t.Context(), extensionManager(), current.ID, RestartInput{
		ConfirmCapabilities: true,
		IdempotencyKey:      "restart-legacy-to-v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Status != StatusEnabled || restarted.Version != candidate.Version ||
		restarted.PackageDigest != candidate.PackageDigest || restarted.StagedVersion != nil {
		t.Fatalf("restarted target=%+v candidate=%+v", restarted, candidate)
	}
	if runner.calls != 1 || runner.input.Acquire.Operation != string(LifecycleMachineEnable) ||
		runner.input.Extension.Version != candidate.Version {
		t.Fatalf("lifecycle input=%+v", runner.input)
	}
	disableAt := slices.Index(events, "desired.disable")
	promoteAt := slices.Index(events, "store.promote")
	enableAt := slices.Index(events, "lifecycle.enable")
	if disableAt < 0 || promoteAt <= disableAt || enableAt <= promoteAt {
		t.Fatalf("legacy bridge order=%v", events)
	}
}

func TestRestartFailureAfterPromotionRemainsDisabledAndSameKeyResumes(t *testing.T) {
	current, candidate := restartLegacyToLifecycleV2Fixture(t)
	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{current.ID: current}),
		events:             &events,
	}
	legacyStore := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	store := &restartRecordingStore{recordingLegacyPluginRuntimeStore: legacyStore, events: &events}
	preflightCalls := 0
	runner := &lifecycleV2RecordingRunner{beforeRun: func(LifecycleCoordinatorRunInput) {
		item := base.items[current.ID]
		item.Status = StatusEnabled
		base.items[current.ID] = item
	}}
	service := NewServiceWithOptions(store, t.TempDir(), "", &orderedQueryRuntime{events: &events},
		WithAuditor(&recordingAuditWriter{events: &events}),
		WithExecutableTrust(NewExecutableTrustService(store, &memoryExecutableTrustStore{}), true),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error {
				preflightCalls++
				if preflightCalls == 1 {
					return errors.New("temporary lifecycle preflight failure")
				}
				return nil
			},
			lifecycleV2AuthorityStore{},
		),
	)
	input := RestartInput{ConfirmCapabilities: true, IdempotencyKey: "restart-resumable"}

	_, err := service.Restart(t.Context(), extensionManager(), current.ID, input)
	if !errors.Is(err, ErrPreflightFailed) {
		t.Fatalf("first restart error=%v", err)
	}
	disabled := base.items[current.ID]
	if disabled.Status != StatusDisabled || disabled.Version != candidate.Version ||
		disabled.PackageDigest != candidate.PackageDigest || disabled.StagedVersion != nil {
		t.Fatalf("failed restart did not preserve exact disabled target=%+v", disabled)
	}

	restarted, err := service.Restart(t.Context(), extensionManager(), current.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Status != StatusEnabled || restarted.Version != candidate.Version ||
		runner.calls != 1 || preflightCalls != 2 {
		t.Fatalf("resumed restart=%+v runner=%d preflight=%d", restarted, runner.calls, preflightCalls)
	}
}

func TestRestartStagedCapabilitiesAreConfirmedBeforeDowntime(t *testing.T) {
	current, _ := restartLegacyToLifecycleV2Fixture(t)
	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{current.ID: current}),
		events:             &events,
	}
	store := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	service := NewServiceWithRuntime(store, t.TempDir(), &orderedQueryRuntime{events: &events})

	_, err := service.Restart(t.Context(), extensionManager(), current.ID, RestartInput{
		IdempotencyKey: "restart-capabilities-preflight",
	})
	if !errors.Is(err, ErrCapabilityConfirmationRequired) {
		t.Fatalf("missing confirmation error=%v", err)
	}
	if len(events) != 0 || base.items[current.ID].Status != StatusEnabled {
		t.Fatalf("restart changed runtime before confirmation: events=%v item=%+v", events, base.items[current.ID])
	}
}

func TestRestartLifecycleV2StagedCapabilitiesAreConfirmedBeforeDowntime(t *testing.T) {
	current, _ := restartLegacyToLifecycleV2Fixture(t)
	current.Manifest.Lifecycle = &ManifestLifecycle{
		ContractVersion: current.ID + ".lifecycle@1",
		Enable:          extensionLifecycleOperationForAuthorityTest(current.ID),
	}
	current.Manifest.Capabilities = []string{"net.outbound"}
	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{current.ID: current}),
		events:             &events,
	}
	store := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	runner := &lifecycleV2RecordingRunner{}
	service := NewServiceWithOptions(store, t.TempDir(), "", &orderedQueryRuntime{events: &events},
		WithExecutableTrust(NewExecutableTrustService(store, &memoryExecutableTrustStore{}), true),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil },
			lifecycleV2AuthorityStore{},
		),
	)

	_, err := service.Restart(t.Context(), extensionManager(), current.ID, RestartInput{
		IdempotencyKey: "restart-v2-capabilities-preflight",
	})
	if !errors.Is(err, ErrCapabilityConfirmationRequired) {
		t.Fatalf("missing confirmation error=%v", err)
	}
	if runner.calls != 0 || len(events) != 0 || base.items[current.ID].Status != StatusEnabled {
		t.Fatalf("V2 restart reached downtime before confirmation: runner=%d events=%v item=%+v",
			runner.calls, events, base.items[current.ID])
	}
}

func TestExecutableTrustStatusReturnsBuiltinNotRequiredWithGateDisabled(t *testing.T) {
	item := exactTrustExtension(t, "builtin.trust-status")
	item.Source = SourceBuiltin
	item.IsSystem = true
	item.IsDeletable = false
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	service := NewServiceWithOptions(store, t.TempDir(), "", &fakeRuntimeManager{},
		WithExecutableTrust(trust, false),
	)

	status, err := service.ExecutableTrustStatus(t.Context(), extensionManager(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TrustRequired || !status.Trusted || status.Impact.ExtensionID != item.ID {
		t.Fatalf("builtin trust status=%+v", status)
	}
}

func TestExecutableTrustStatusForStagedBuiltinReturnsNotRequiredWithGateDisabled(t *testing.T) {
	current := exactTrustExtension(t, "builtin.staged-trust-status")
	current.Source = SourceBuiltin
	current.IsSystem = true
	current.IsDeletable = false
	current.Status = StatusEnabled
	current.ActiveVersionID = 1

	candidate := exactTrustExtension(t, current.ID)
	candidate.Version = "2.0.0"
	candidate.Manifest.Version = candidate.Version
	candidate.ActiveVersionID = 2
	if err := writeManifest(candidate.PackagePath, candidate.Manifest); err != nil {
		t.Fatal(err)
	}
	var err error
	candidate.PackageDigest, err = extensionpackage.DigestTree(candidate.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	current.StagedVersion = &ExtensionVersion{
		ID: candidate.ActiveVersionID, Version: candidate.Version, Manifest: candidate.Manifest,
		PackageDigest: candidate.PackageDigest, AdminFrontendDigest: candidate.AdminFrontendDigest,
		PackagePath: candidate.PackagePath, InstalledAt: candidate.InstalledAt,
	}

	store := newFakeExtensionStore(map[string]Extension{current.ID: current})
	service := NewServiceWithOptions(store, t.TempDir(), "", &fakeRuntimeManager{},
		WithExecutableTrust(NewExecutableTrustService(store, &memoryExecutableTrustStore{}), false),
	)

	status, err := service.ExecutableTrustStatusForStaged(t.Context(), extensionManager(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TrustRequired || !status.Trusted ||
		status.Impact.ExtensionVersion != candidate.Version ||
		status.Impact.PackageDigest != candidate.PackageDigest {
		t.Fatalf("staged builtin trust status=%+v", status)
	}
}

func restartLegacyToLifecycleV2Fixture(t *testing.T) (Extension, Extension) {
	t.Helper()
	current := legacyPluginRuntimeServiceFixture(t, StatusEnabled)
	current.Source = SourceBuiltin
	current.IsSystem = true
	current.IsDeletable = false

	candidate := current
	candidate.Version = "2.0.0"
	candidate.Manifest.Version = candidate.Version
	candidate.Manifest.Lifecycle = &ManifestLifecycle{
		ContractVersion: current.ID + ".lifecycle@1",
		Enable:          extensionLifecycleOperationForAuthorityTest(current.ID),
	}
	candidate.Status = StatusDisabled
	candidate.ActiveVersionID = 52
	candidate.PackagePath = copyRestartFixturePackage(t, current.PackagePath)
	refreshTrustPackageIdentity(t, &candidate)
	current.StagedVersion = &ExtensionVersion{
		ID: candidate.ActiveVersionID, Version: candidate.Version, Manifest: candidate.Manifest,
		PackageDigest: candidate.PackageDigest, AdminFrontendDigest: candidate.AdminFrontendDigest,
		PackagePath: candidate.PackagePath, InstalledAt: candidate.InstalledAt,
	}
	return current, candidate
}

func copyRestartFixturePackage(t *testing.T, source string) string {
	t.Helper()
	target := t.TempDir()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(destination, body, info.Mode())
	})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

type restartRecordingStore struct {
	*recordingLegacyPluginRuntimeStore
	events *[]string
}

func (s *restartRecordingStore) PromoteStagedVersion(
	ctx context.Context,
	input StagedVersionCASInput,
) (Extension, error) {
	*s.events = append(*s.events, "store.promote")
	return s.recordingLegacyPluginRuntimeStore.orderedQueryStore.fakeExtensionStore.
		PromoteStagedVersion(ctx, input)
}
