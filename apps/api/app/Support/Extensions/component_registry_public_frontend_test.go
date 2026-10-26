package extensionsruntime

import (
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestComponentRegistryPublicL2AdmissionTracksExactActivePlan(t *testing.T) {
	owner := componentPublicL2Extension(t, "component.public-owner", extensionmanifest.ComponentActionAdd, 0, "", "")
	registry := NewComponentRegistry()
	ownerInstance := componentPackageRuntimeInstanceID(owner)
	if err := registry.ReplaceRuntime(owner, ownerInstance); err != nil {
		t.Fatal(err)
	}
	if !registry.AdmitPublicComponent(owner, owner.Manifest.Components[0]) {
		t.Fatal("exact active add provider was not admitted")
	}

	stale := owner
	stale.PackageDigest = strings.Repeat("f", 64)
	if registry.AdmitPublicComponent(stale, stale.Manifest.Components[0]) {
		t.Fatal("stale package artifact was admitted")
	}
	disabled := owner
	disabled.Status = extensions.StatusInstalled
	if registry.AdmitPublicComponent(disabled, disabled.Manifest.Components[0]) {
		t.Fatal("disabled package artifact was admitted")
	}

	hiderID := "component.public-hider"
	hider := componentTestExtension(t, hiderID, extensions.TypePlugin, componentTestContribution(
		hiderID, "hide-owner", extensionmanifest.ComponentActionHide, 100,
		owner.Manifest.Components[0].ID, owner.Manifest.Components[0].ContractVersion,
	))
	hider.Manifest.Dependencies = []extensions.ManifestDependency{{
		ID: owner.ID, Version: "^1.0.0", Kind: "required",
	}}
	if err := registry.ReplaceRuntime(hider, componentPackageRuntimeInstanceID(hider)); err != nil {
		t.Fatal(err)
	}
	if registry.AdmitPublicComponent(owner, owner.Manifest.Components[0]) {
		t.Fatal("hidden target exposed its public L2 descriptor")
	}
	if removed, err := registry.RemoveRuntime(hider.ID, componentPackageRuntimeInstanceID(hider)); err != nil || !removed {
		t.Fatalf("remove hider: removed=%t err=%v", removed, err)
	}
	if !registry.AdmitPublicComponent(owner, owner.Manifest.Components[0]) {
		t.Fatal("provider did not recover after exact hider removal")
	}
	if removed, err := registry.RemoveRuntime(owner.ID, ownerInstance); err != nil || !removed {
		t.Fatalf("remove owner: removed=%t err=%v", removed, err)
	}
	if registry.AdmitPublicComponent(owner, owner.Manifest.Components[0]) {
		t.Fatal("removed package retained public L2 admission")
	}
}

func TestComponentRegistryPublicL2AdmissionRejectsLosingAndDependencyInvalidContributions(t *testing.T) {
	winner := componentPublicL2Extension(
		t, "component.public-winner", extensionmanifest.ComponentActionReplace, 50,
		componentTestCoreTarget, componentTestCoreContract,
	)
	loser := componentPublicL2Extension(
		t, "component.public-loser", extensionmanifest.ComponentActionReplace, 10,
		componentTestCoreTarget, componentTestCoreContract,
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceAll([]ComponentRuntimeSnapshot{
		{Extension: loser, InstanceID: componentPackageRuntimeInstanceID(loser)},
		{Extension: winner, InstanceID: componentPackageRuntimeInstanceID(winner)},
	}); err != nil {
		t.Fatal(err)
	}
	if !registry.AdmitPublicComponent(winner, winner.Manifest.Components[0]) {
		t.Fatal("deterministic replace winner was not admitted")
	}
	if registry.AdmitPublicComponent(loser, loser.Manifest.Components[0]) {
		t.Fatal("losing replace provider exposed its public L2 descriptor")
	}

	optional := componentPublicL2Extension(
		t, "component.public-optional", extensionmanifest.ComponentActionBefore, 10,
		"component.missing-owner.component.card", "component.missing-owner.component.card@1",
	)
	optional.Manifest.Dependencies = []extensions.ManifestDependency{{
		ID: "component.missing-owner", Version: "^1.0.0", Kind: "optional",
	}}
	optionalRegistry := NewComponentRegistry()
	if err := optionalRegistry.ReplaceRuntime(optional, componentPackageRuntimeInstanceID(optional)); err != nil {
		t.Fatal(err)
	}
	if optionalRegistry.AdmitPublicComponent(optional, optional.Manifest.Components[0]) {
		t.Fatal("optional contribution with an unavailable target was admitted")
	}
}

func componentPublicL2Extension(
	t *testing.T,
	id string,
	action string,
	priority int,
	targetID string,
	targetContract string,
) extensions.Extension {
	t.Helper()
	declaration := componentTestContribution(id, "browser", action, priority, targetID, targetContract)
	extension := componentTestExtension(t, id, extensions.TypePlugin, declaration)
	entryID := id + ".file.browser"
	extension.Manifest.Components[0].L2Component = entryID
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: entryID, Kind: "frontend", Path: "frontend/public/browser.mjs", Digest: strings.Repeat("9", 64),
	})
	return extension
}
