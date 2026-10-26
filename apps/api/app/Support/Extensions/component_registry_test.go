package extensionsruntime

import (
	"errors"
	"slices"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	componentcatalog "github.com/keeptrue/sforum/apps/api/app/Support/ComponentCatalog"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestComponentRegistryPublishesCoreAndEveryAction(t *testing.T) {
	registry := NewComponentRegistry()
	initial := registry.Snapshot()
	if initial.Revision != 0 || len(initial.Targets) != len(componentcatalog.CoreComponentCatalog()) {
		t.Fatalf("initial Core snapshot = revision:%d targets:%d", initial.Revision, len(initial.Targets))
	}
	core := componentTestFindTarget(t, initial.Targets, componentTestCoreTarget)
	if !core.Core || core.ContractVersion != componentTestCoreContract ||
		!slices.Contains(core.Owners, componentcatalog.OwnerPublic) {
		t.Fatalf("Core target = %#v", core)
	}

	id := "component.actions"
	actions := []string{
		extensionmanifest.ComponentActionAdd,
		extensionmanifest.ComponentActionBefore,
		extensionmanifest.ComponentActionAfter,
		extensionmanifest.ComponentActionWrap,
		extensionmanifest.ComponentActionReplace,
		extensionmanifest.ComponentActionHide,
		extensionmanifest.ComponentActionFilterProps,
		extensionmanifest.ComponentActionFilterResult,
	}
	declarations := make([]extensions.ManifestComponent, 0, len(actions))
	for index, action := range actions {
		declarations = append(declarations, componentTestContribution(
			id, strings.ReplaceAll(action, "_", "-"), action, len(actions)-index,
			componentTestCoreTarget, componentTestCoreContract,
		))
	}
	extension := componentTestExtension(t, id, extensions.TypePlugin, declarations...)
	if err := registry.ReplaceRuntime(extension, "runtime-actions"); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Revision != 1 || len(plan.Contributions) != len(actions) ||
		len(plan.ReplaceCandidates) != 1 || plan.ReplaceWinner == nil {
		t.Fatalf("action plan = %#v", plan)
	}
	seen := make(map[string]bool, len(actions))
	for _, contribution := range plan.Contributions {
		seen[contribution.Action] = true
		if contribution.Artifact.ExtensionID != id || contribution.Artifact.ExtensionVersion != "1.0.0" ||
			contribution.Artifact.PackageDigest != extension.PackageDigest ||
			contribution.Artifact.RuntimeInstanceID != "runtime-actions" {
			t.Fatalf("non-exact contribution = %#v", contribution)
		}
	}
	for _, action := range actions {
		if !seen[action] {
			t.Fatalf("action %q was not published", action)
		}
	}
	addID := id + ".component.add"
	added := componentTestFindTarget(t, registry.Snapshot().Targets, addID)
	if added.Core || added.Provider == nil || added.Provider.ID != addID {
		t.Fatalf("extension add target = %#v", added)
	}
	if _, err := registry.ResolvePlan(componentTestCoreTarget, "sforum.component.page.forum.home@2"); !errors.Is(err, ErrComponentRegistryTargetNotFound) {
		t.Fatalf("mismatched Core target contract = %v", err)
	}
	staleTarget := componentTestExtension(t, "component.stale-target", extensions.TypePlugin,
		componentTestContribution(
			"component.stale-target", "before-home", extensionmanifest.ComponentActionBefore, 0,
			componentTestCoreTarget, "sforum.component.page.forum.home@2",
		),
	)
	if err := registry.ReplaceRuntime(staleTarget, "runtime-stale-target"); !errors.Is(err, ErrComponentRegistryConflict) {
		t.Fatalf("stale target contract publication = %v", err)
	}
	if current := registry.Snapshot(); current.Revision != 1 {
		t.Fatalf("failed target publication changed revision to %d", current.Revision)
	}

	theme := componentTestExtension(t, "component.theme", extensions.TypeTheme,
		componentTestContribution(
			"component.theme", "public-hide", extensionmanifest.ComponentActionHide, 0,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	if err := registry.ReplaceRuntime(theme, "runtime-theme"); err != nil {
		t.Fatalf("public theme contribution = %v", err)
	}
	adminTheme := componentTestExtension(t, "component.admin-theme", extensions.TypeTheme,
		componentTestContribution(
			"component.admin-theme", "admin-hide", extensionmanifest.ComponentActionHide, 0,
			"core.component.page.admin", "sforum.component.page.admin@1",
		),
	)
	if err := registry.ReplaceRuntime(adminTheme, "runtime-admin-theme"); !errors.Is(err, ErrComponentRegistryInvalid) {
		t.Fatalf("theme targeting admin Core UI = %v", err)
	}
}

func TestComponentRegistryOrdersReplaceProvidersAndSupportsExactSelection(t *testing.T) {
	registry := NewComponentRegistry()
	alphaID := "component.alpha"
	alpha := componentTestExtension(t, alphaID, extensions.TypePlugin,
		componentTestContribution(alphaID, "replace-a", extensionmanifest.ComponentActionReplace, 20, componentTestCoreTarget, componentTestCoreContract),
		componentTestContribution(alphaID, "replace-z", extensionmanifest.ComponentActionReplace, 20, componentTestCoreTarget, componentTestCoreContract),
	)
	betaID := "component.beta"
	beta := componentTestExtension(t, betaID, extensions.TypePlugin,
		componentTestContribution(betaID, "replace-b", extensionmanifest.ComponentActionReplace, 20, componentTestCoreTarget, componentTestCoreContract),
	)
	gammaID := "component.gamma"
	gamma := componentTestExtension(t, gammaID, extensions.TypePlugin,
		componentTestContribution(gammaID, "replace-g", extensionmanifest.ComponentActionReplace, 30, componentTestCoreTarget, componentTestCoreContract),
	)
	for _, item := range []struct {
		extension extensions.Extension
		instance  string
	}{{alpha, "runtime-alpha"}, {beta, "runtime-beta"}, {gamma, "runtime-gamma"}} {
		if err := registry.ReplaceRuntime(item.extension, item.instance); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		gammaID + ".component.replace-g",
		alphaID + ".component.replace-a",
		alphaID + ".component.replace-z",
		betaID + ".component.replace-b",
	}
	got := make([]string, len(plan.ReplaceCandidates))
	for index := range plan.ReplaceCandidates {
		got[index] = plan.ReplaceCandidates[index].ID
	}
	if !slices.Equal(got, want) || plan.ReplaceWinner == nil || plan.ReplaceWinner.ID != want[0] ||
		plan.Conflict == nil || plan.Conflict.ExplicitSelection ||
		len(plan.Contributions) != 1 || plan.Contributions[0].ID != want[0] {
		t.Fatalf("default replace resolution = got:%#v plan:%#v", got, plan)
	}

	selection, err := registry.SelectReplaceProvider(SelectComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ContributionID: betaID + ".component.replace-b", ExpectedRevision: plan.Revision,
	})
	if err != nil || selection.Artifact.RuntimeInstanceID != "runtime-beta" {
		t.Fatalf("selection = %#v, %v", selection, err)
	}
	if _, err := registry.SelectReplaceProvider(SelectComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ContributionID: "component.missing.replace", ExpectedRevision: selection.SelectedAtRevision,
	}); !errors.Is(err, ErrComponentRegistryProviderNotFound) {
		t.Fatalf("missing replace provider = %v", err)
	}
	selected, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil || selected.ReplaceWinner == nil || selected.ReplaceWinner.ID != selection.ContributionID ||
		selected.Selection == nil || selected.Conflict == nil || !selected.Conflict.ExplicitSelection ||
		len(selected.Contributions) != 1 || selected.Contributions[0].ID != selection.ContributionID {
		t.Fatalf("selected replace plan = %#v, %v", selected, err)
	}
	if _, err := registry.SelectReplaceProvider(SelectComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ContributionID: want[0], ExpectedRevision: plan.Revision,
	}); !errors.Is(err, ErrComponentRegistryRevisionConflict) {
		t.Fatalf("stale selection revision = %v", err)
	}
	reset, err := registry.ResetReplaceProvider(ResetComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ExpectedRevision: selected.Revision,
	})
	if err != nil || !reset {
		t.Fatalf("reset selection = %t, %v", reset, err)
	}
	defaulted, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil || defaulted.ReplaceWinner == nil || defaulted.ReplaceWinner.ID != want[0] || defaulted.Selection != nil {
		t.Fatalf("reset replace plan = %#v, %v", defaulted, err)
	}

	snapshot := registry.Snapshot()
	componentTestFindTarget(t, snapshot.Targets, componentTestCoreTarget).Owners[0] = "mutated"
	snapshot.Conflicts[0].Candidates[0].ID = "mutated"
	defaulted.Contributions[0].ID = "mutated"
	fresh := registry.Snapshot()
	if componentTestFindTarget(t, fresh.Targets, componentTestCoreTarget).Owners[0] == "mutated" ||
		fresh.Conflicts[0].Candidates[0].ID == "mutated" {
		t.Fatal("snapshot data mutated the immutable registry state")
	}
	freshPlan, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil || freshPlan.Contributions[0].ID == "mutated" ||
		freshPlan.Contributions[0].propsValidator != nil || freshPlan.Target.Provider != nil {
		t.Fatalf("resolve plan was not detached = %#v, %v", freshPlan, err)
	}
}

func TestComponentRegistryRequiresCompatibleDependenciesAndAvoidsDanglingTargets(t *testing.T) {
	ownerID := "component.owner"
	ownerTarget := ownerID + ".component.card"
	ownerContract := ownerTarget + "@1"
	owner := componentTestExtension(t, ownerID, extensions.TypePlugin,
		componentTestContribution(ownerID, "card", extensionmanifest.ComponentActionAdd, 0, "", ""),
	)
	optionalID := "component.optional"
	optional := componentTestExtension(t, optionalID, extensions.TypePlugin,
		componentTestContribution(optionalID, "before-card", extensionmanifest.ComponentActionBefore, 10, ownerTarget, ownerContract),
	)
	optional.Manifest.Dependencies = []extensions.ManifestDependency{{
		ID: ownerID, Version: "^1.0.0", Kind: "optional",
	}}
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(optional, "runtime-optional"); err != nil {
		t.Fatalf("optional missing contribution = %v", err)
	}
	if _, err := registry.ResolvePlan(ownerTarget, ownerContract); !errors.Is(err, ErrComponentRegistryTargetNotFound) {
		t.Fatalf("optional missing target = %v", err)
	}
	if err := registry.ReplaceRuntime(owner, "runtime-owner"); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.ResolvePlan(ownerTarget, ownerContract)
	if err != nil || len(plan.Contributions) != 1 || plan.Contributions[0].Artifact.ExtensionID != optionalID {
		t.Fatalf("resolved optional contribution = %#v, %v", plan, err)
	}
	if removed, err := registry.RemoveRuntime(ownerID, "runtime-owner"); err != nil || !removed {
		t.Fatalf("remove optional owner = %t, %v", removed, err)
	}
	if _, err := registry.ResolvePlan(ownerTarget, ownerContract); !errors.Is(err, ErrComponentRegistryTargetNotFound) {
		t.Fatalf("removed optional target remained = %v", err)
	}

	if err := registry.ReplaceRuntime(owner, "runtime-owner-2"); err != nil {
		t.Fatal(err)
	}
	incompatibleID := "component.incompatible"
	incompatible := componentTestExtension(t, incompatibleID, extensions.TypePlugin,
		componentTestContribution(incompatibleID, "after-card", extensionmanifest.ComponentActionAfter, 5, ownerTarget, ownerContract),
	)
	incompatible.Manifest.Dependencies = []extensions.ManifestDependency{{
		ID: ownerID, Version: "<1.0.0", Kind: "optional",
	}}
	if err := registry.ReplaceRuntime(incompatible, "runtime-incompatible"); err != nil {
		t.Fatalf("optional incompatible contribution = %v", err)
	}
	plan, err = registry.ResolvePlan(ownerTarget, ownerContract)
	if err != nil {
		t.Fatal(err)
	}
	for _, contribution := range plan.Contributions {
		if contribution.Artifact.ExtensionID == incompatibleID {
			t.Fatalf("optional incompatible contribution was published: %#v", contribution)
		}
	}

	requiredID := "component.required"
	required := componentTestExtension(t, requiredID, extensions.TypePlugin,
		componentTestContribution(requiredID, "wrap-card", extensionmanifest.ComponentActionWrap, 20, ownerTarget, ownerContract),
	)
	required.Manifest.Dependencies = []extensions.ManifestDependency{{
		ID: ownerID, Version: "^1.0.0", Kind: "required",
	}}
	if err := registry.ReplaceRuntime(required, "runtime-required"); err != nil {
		t.Fatal(err)
	}
	before := registry.Snapshot()
	if removed, err := registry.RemoveRuntime(ownerID, "runtime-owner-2"); removed || !errors.Is(err, ErrComponentRegistryConflict) {
		t.Fatalf("required owner removal = %t, %v", removed, err)
	}
	after := registry.Snapshot()
	if after.Revision != before.Revision || len(after.Targets) != len(before.Targets) || len(after.Contributions) != len(before.Contributions) {
		t.Fatalf("failed removal changed snapshot: before=%#v after=%#v", before, after)
	}
	if removed, err := registry.RemoveRuntime(requiredID, "runtime-required"); err != nil || !removed {
		t.Fatalf("remove required consumer = %t, %v", removed, err)
	}
	if removed, err := registry.RemoveRuntime(ownerID, "runtime-owner-2"); err != nil || !removed {
		t.Fatalf("remove unreferenced owner = %t, %v", removed, err)
	}
}
