package extensionsruntime

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestProviderSlotRegistryPublishesExactDeterministicCandidates(t *testing.T) {
	registry := NewVersionedProviderSlotRegistry()
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	consumer := versionedProviderExtension("providers.consumer", strings.Repeat("b", 64), providerSlotConsumer("providers.consumer.delivery", 50))
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^1.0.0", Kind: "required"}}
	if err := registry.ReplaceRuntime(owner, "owner-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(consumer, "consumer-runtime"); err != nil {
		t.Fatal(err)
	}

	caller := ProviderSlotCaller{
		ExtensionID: consumer.ID, ExtensionVersion: consumer.Version, ArtifactDigest: consumer.PackageDigest,
		RuntimeInstanceID: "consumer-runtime", Attested: true,
	}
	resolution, err := registry.Discover(caller, owner.Manifest.Providers[0].ID, owner.Manifest.Providers[0].ContractVersion)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{resolution.Candidates[0].ID, resolution.Candidates[1].ID}; !reflect.DeepEqual(got, []string{"providers.consumer.delivery", "providers.owner.delivery"}) {
		t.Fatalf("candidate order = %#v", got)
	}
	if resolution.Contract.Artifact.RuntimeInstanceID != "owner-runtime" || resolution.Candidates[0].Artifact.RuntimeInstanceID != "consumer-runtime" {
		t.Fatalf("exact identities = %#v", resolution)
	}

	caller.ArtifactDigest = strings.Repeat("f", 64)
	if _, err := registry.Discover(caller, resolution.Contract.ID, resolution.Contract.ContractVersion); !errors.Is(err, ErrProviderSlotDenied) {
		t.Fatalf("forged caller digest = %v", err)
	}
	snapshot := registry.Snapshot()
	snapshot.Contracts[0].RequestSchema = "forged@1"
	snapshot.Candidates[0].Artifact.RuntimeInstanceID = "forged-runtime"
	again, err := registry.Discover(ProviderSlotCaller{}, resolution.Contract.ID, resolution.Contract.ContractVersion)
	if err != nil || again.Contract.RequestSchema != providerRequestSchema || again.Candidates[0].Artifact.RuntimeInstanceID != "consumer-runtime" {
		t.Fatalf("snapshot mutation escaped = %#v, %v", again, err)
	}
}

func TestProviderSlotRegistryExactReplacementAndUpgradeToEmpty(t *testing.T) {
	registry := NewVersionedProviderSlotRegistry()
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	if err := registry.ReplaceRuntime(owner, "owner-runtime-1"); err != nil {
		t.Fatal(err)
	}
	upgraded := owner
	upgraded.Version, upgraded.Manifest.Version = "1.1.0", "1.1.0"
	upgraded.PackageDigest = strings.Repeat("b", 64)
	if err := registry.ReplaceRuntime(upgraded, "owner-runtime-2"); err != nil {
		t.Fatal(err)
	}
	if removed, err := registry.RemoveRuntime(owner.ID, "owner-runtime-1"); err != nil || removed {
		t.Fatalf("stale removal = %t, %v", removed, err)
	}
	resolution, err := registry.Discover(ProviderSlotCaller{}, providerSlotID, providerContractVersion)
	if err != nil || resolution.Contract.Artifact.ExtensionVersion != "1.1.0" || resolution.Contract.Artifact.RuntimeInstanceID != "owner-runtime-2" {
		t.Fatalf("replacement = %#v, %v", resolution, err)
	}

	empty := upgraded
	empty.Manifest.Providers = nil
	if err := registry.ReplaceRuntime(empty, "owner-runtime-3"); err != nil {
		t.Fatal(err)
	}
	if snapshot := registry.Snapshot(); len(snapshot.Contracts) != 0 || len(snapshot.Candidates) != 0 {
		t.Fatalf("upgrade to empty retained contracts = %#v", snapshot)
	}
}

func TestProviderSlotRegistryDependenciesDisableAndContractDrift(t *testing.T) {
	registry := NewVersionedProviderSlotRegistry()
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	optional := versionedProviderExtension("providers.optional", strings.Repeat("b", 64), providerSlotConsumer("providers.optional.delivery", 30))
	optional.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^1.0.0", Kind: "optional"}}
	if err := registry.ReplaceRuntime(optional, "optional-runtime"); err != nil {
		t.Fatalf("optional missing owner: %v", err)
	}
	if err := registry.ReplaceRuntime(owner, "owner-runtime"); err != nil {
		t.Fatal(err)
	}
	resolution, err := registry.Discover(ProviderSlotCaller{}, providerSlotID, providerContractVersion)
	if err != nil || len(resolution.Candidates) != 2 || resolution.Candidates[0].ID != "providers.optional.delivery" {
		t.Fatalf("optional convergence = %#v, %v", resolution, err)
	}
	if removed, err := registry.RemoveRuntime(owner.ID, "owner-runtime"); err != nil || !removed {
		t.Fatalf("optional owner disable = %t, %v", removed, err)
	}
	if snapshot := registry.Snapshot(); len(snapshot.Contracts) != 0 || len(snapshot.Candidates) != 0 {
		t.Fatalf("optional disable snapshot = %#v", snapshot)
	}
	if err := registry.ReplaceRuntime(owner, "owner-runtime-2"); err != nil {
		t.Fatal(err)
	}

	required := versionedProviderExtension("providers.required", strings.Repeat("c", 64), providerSlotConsumer("providers.required.delivery", 40))
	required.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^1.0.0", Kind: "required"}}
	if err := registry.ReplaceRuntime(required, "required-runtime"); err != nil {
		t.Fatal(err)
	}
	if removed, err := registry.RemoveRuntime(owner.ID, "owner-runtime-2"); removed || !errors.Is(err, ErrProviderSlotConflict) {
		t.Fatalf("required owner disable = %t, %v", removed, err)
	}

	mismatch := required
	mismatch.ID, mismatch.Manifest.ID = "providers.mismatch", "providers.mismatch"
	mismatch.Manifest.Providers[0].ID = "providers.mismatch.delivery"
	mismatch.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^2.0.0", Kind: "required"}}
	if err := registry.ReplaceRuntime(mismatch, "mismatch-runtime"); !errors.Is(err, ErrProviderSlotConflict) {
		t.Fatalf("required version mismatch = %v", err)
	}
	drift := required
	drift.ID, drift.Manifest.ID = "providers.drift", "providers.drift"
	drift.Manifest.Providers[0].ID = "providers.drift.delivery"
	drift.Manifest.Providers[0].ResponseSchema = "providers.owner.delivery.response@2"
	if err := registry.ReplaceRuntime(drift, "drift-runtime"); !errors.Is(err, ErrProviderSlotConflict) {
		t.Fatalf("contract drift = %v", err)
	}
}

func TestProviderSlotRegistryRejectsDuplicateDefinitionAndRollsBackHookBus(t *testing.T) {
	registry := NewVersionedProviderSlotRegistry()
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	duplicate := versionedProviderExtension("providers.duplicate", strings.Repeat("b", 64), providerSlotDefinition(20))
	duplicate.Manifest.Providers[0].ID = "providers.duplicate.delivery"
	if err := registry.ReplaceRuntime(owner, "owner-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(duplicate, "duplicate-runtime"); !errors.Is(err, ErrProviderSlotConflict) {
		t.Fatalf("duplicate slot = %v", err)
	}

	bus := NewHookBus(HookBusConfig{})
	invalid := owner
	hook := versionedHookDefinition()
	hook.TimeoutMS = extensionmanifest.HookMaximumTimeoutMS + 1
	invalid.Manifest.Hooks = []extensions.ManifestHook{hook}
	if err := bus.RegisterRuntime(invalid, "invalid-runtime"); !errors.Is(err, ErrHookRegistryInvalid) {
		t.Fatalf("hook publication failure = %v", err)
	}
	if snapshot := bus.ProviderSlots().Snapshot(); len(snapshot.Contracts) != 0 || len(snapshot.Candidates) != 0 {
		t.Fatalf("provider publication survived hook rollback = %#v", snapshot)
	}
}

const (
	providerSlotID          = "providers.owner.delivery"
	providerContractVersion = "providers.owner.delivery@1"
	providerRequestSchema   = "providers.owner.delivery.request@1"
	providerResponseSchema  = "providers.owner.delivery.response@1"
)

func providerSlotDefinition(priority int) extensions.ManifestProvider {
	return extensions.ManifestProvider{
		ID: providerSlotID, ContractVersion: providerContractVersion, Slot: "providers.owner.delivery.slot",
		Label: "Delivery", Handler: "provider.delivery", RequestSchema: providerRequestSchema,
		ResponseSchema: providerResponseSchema, Fallback: "next", TimeoutMS: 100, Priority: priority,
	}
}

func providerSlotConsumer(id string, priority int) extensions.ManifestProvider {
	provider := providerSlotDefinition(priority)
	provider.ID = id
	provider.TargetID = providerSlotID
	provider.Handler = "provider.consume"
	return provider
}

func versionedProviderExtension(id, digest string, providers ...extensions.ManifestProvider) extensions.Extension {
	return extensions.Extension{
		ID: id, Version: "1.0.0", Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackageDigest: digest,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Version: "1.0.0", Type: extensions.TypePlugin,
			Backend:   extensions.ManifestBackend{Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2},
			Providers: append([]extensions.ManifestProvider(nil), providers...),
		},
	}
}
