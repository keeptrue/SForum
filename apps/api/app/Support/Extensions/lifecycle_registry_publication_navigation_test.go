package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

func TestLifecycleNavigationMaterialFreezesExactAuthorityAndReachableDigestAliases(t *testing.T) {
	extension := lifecycleNavigationTestExtension(t, "1.0.0", strings.Repeat("a", 64), 301)
	material, err := buildLifecycleRegistryMaterial(extension, lifecycleRegistryBinding(extension, "nav-digest-runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if material.navigationPublication != nil {
		t.Fatal("navigation publication was built before exact lifecycle authority was frozen")
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Navigation: navigationregistry.New(),
	})
	impact := strings.Repeat("b", 64)
	if err := boundary.freezeNavigationMaterial(&material, impact); err != nil {
		t.Fatal(err)
	}
	publication := material.navigationPublication
	if publication == nil || publication.Artifact.ExtensionID != extension.ID ||
		publication.Artifact.ExtensionVersion != extension.Version ||
		publication.Artifact.PackageDigest != extension.PackageDigest || publication.Artifact.ImpactDigest != impact ||
		publication.Artifact.VersionID != extension.ActiveVersionID ||
		publication.Artifact.RuntimeInstanceID != "nav-digest-runtime" ||
		len(publication.Navigation) != 1 || len(publication.Regions) != 1 {
		t.Fatalf("frozen navigation publication = %#v", publication)
	}
	if publication.Navigation[0].Visibility != navigationregistry.VisibilityPublic ||
		publication.Navigation[0].Labels["zh-CN"] != publication.Navigation[0].Label {
		t.Fatalf("navigation defaults = %#v", publication.Navigation[0])
	}
	v1Digest, err := encodeLifecycleRegistryMaterialDigest(&material, false, false)
	if err != nil {
		t.Fatal(err)
	}
	v7Digest, err := encodeLifecycleRegistryMaterialDigestV7(&material)
	if err != nil {
		t.Fatal(err)
	}
	if material.digest != v7Digest || material.legacyDigest != v1Digest ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest}) || v7Digest == v1Digest {
		t.Fatalf("navigation material digests primary=%s legacy=%s aliases=%v", material.digest, material.legacyDigest,
			registryMaterialCompatibleDigests(&material))
	}

	extension.Manifest.Navigation[0].Label = "mutated"
	if publication.Navigation[0].Label != extension.ID+".nav.item" {
		t.Fatalf("Manifest mutation changed frozen navigation publication = %#v", publication.Navigation[0])
	}
	before := material.digest
	material.navigationPublication.Navigation[0].Order++
	if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
		t.Fatal(err)
	}
	if material.digest == before ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest}) {
		t.Fatalf("navigation declaration drift digest=%s aliases=%v", material.digest, registryMaterialCompatibleDigests(&material))
	}
}

func TestLifecycleNavigationMaterialsUseSourceRestoreAndTargetOperationAuthority(t *testing.T) {
	source := lifecycleNavigationTestExtension(t, "1.0.0", strings.Repeat("c", 64), 302)
	target := lifecycleNavigationTestExtension(t, "2.0.0", strings.Repeat("d", 64), 303)
	sourceBinding := lifecycleRegistryBinding(source, "nav-authority-source")
	targetBinding := lifecycleRegistryBinding(target, "nav-authority-target")
	sourceMaterial, err := buildLifecycleRegistryMaterial(source, sourceBinding)
	if err != nil {
		t.Fatal(err)
	}
	targetMaterial, err := buildLifecycleRegistryMaterial(target, targetBinding)
	if err != nil {
		t.Fatal(err)
	}
	sourceImpact, targetImpact := strings.Repeat("e", 64), strings.Repeat("f", 64)
	authority := &staticAssetAuthority{
		restore:   map[string]string{source.ID: sourceImpact},
		operation: map[string]string{target.ID: targetImpact},
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Navigation: navigationregistry.New(), AssetAuthority: authority,
	})
	request := lifecycleRegistryRequest(source, target, sourceBinding, targetBinding, 1)
	if err := boundary.freezeNavigationMaterials(context.Background(), request, &sourceMaterial, &targetMaterial); err != nil {
		t.Fatal(err)
	}
	if sourceMaterial.navigationPublication.Artifact.ImpactDigest != sourceImpact ||
		targetMaterial.navigationPublication.Artifact.ImpactDigest != targetImpact ||
		sourceMaterial.digest == targetMaterial.digest {
		t.Fatalf("source/target navigation authority = source %#v target %#v",
			sourceMaterial.navigationPublication.Artifact, targetMaterial.navigationPublication.Artifact)
	}
	if !reflect.DeepEqual(authority.calls, []string{"restore:" + source.ID, "operation:" + target.ID}) {
		t.Fatalf("navigation authority calls = %#v", authority.calls)
	}
}

func TestLifecycleNavigationMaterialAliasesOnlyReachablePriorEncoders(t *testing.T) {
	base := lifecycleNavigationTestMaterial(
		t, lifecycleNavigationTestExtension(t, "1.0.0", strings.Repeat("0", 64), 310),
		"nav-alias-runtime", strings.Repeat("1", 64),
	)
	tests := []struct {
		name                                             string
		withAsset, withQuery, withCache, withSEO, withID bool
		wantAliases                                      int
	}{
		{name: "navigation only", wantAliases: 1},
		{name: "asset and navigation", withAsset: true, wantAliases: 2},
		{name: "query and navigation", withQuery: true, wantAliases: 2},
		{name: "cache and navigation", withCache: true, wantAliases: 2},
		{name: "seo and navigation", withSEO: true, wantAliases: 2},
		{name: "identity and navigation", withID: true, wantAliases: 2},
		{name: "all prior families and navigation", withAsset: true, withQuery: true, withCache: true, withSEO: true, withID: true, wantAliases: 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			material := base
			if test.withAsset {
				material.assetPublication = &assetregistry.Publication{}
			}
			if test.withQuery {
				material.queryPublication = &queryregistry.Publication{}
			}
			if test.withCache {
				material.cachePublication = &cacheregistry.Publication{}
			}
			if test.withSEO {
				material.seoPublication = &seoregistry.Publication{}
			}
			if test.withID {
				// 仅占位以触发 @6 编码器；不需要完整 identity 声明。
				material.identityPublication = &identityregistry.Publication{}
			}
			if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
				t.Fatal(err)
			}
			aliases := registryMaterialCompatibleDigests(&material)
			if len(aliases) != test.wantAliases ||
				!validLifecycleRegistryCompatibleDigests(aliases, material.digest) {
				t.Fatalf("aliases=%v, want count=%d", aliases, test.wantAliases)
			}
		})
	}
}

func TestLifecycleNavigationPublicationUpgradeRollbackDisableAndStaleCAS(t *testing.T) {
	ctx := context.Background()
	registry := navigationregistry.New()
	if _, err := registry.Publish(navigationregistry.CorePublication()); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Navigation: registry})
	if boundary.NavigationRegistry() != registry {
		t.Fatal("lifecycle boundary created a second Navigation Registry")
	}
	source := lifecycleNavigationTestExtension(t, "1.0.0", strings.Repeat("1", 64), 304)
	target := lifecycleNavigationTestExtension(t, "2.0.0", strings.Repeat("2", 64), 305)
	sourceMaterial := lifecycleNavigationTestMaterial(t, source, "nav-source-runtime", strings.Repeat("3", 64))
	targetMaterial := lifecycleNavigationTestMaterial(t, target, "nav-target-runtime", strings.Repeat("4", 64))
	if _, err := registry.Publish(*sourceMaterial.navigationPublication); err != nil {
		t.Fatal(err)
	}

	drift := sourceMaterial
	drifted := *sourceMaterial.navigationPublication
	drifted.Navigation = append([]navigationregistry.NavigationDeclaration(nil), drifted.Navigation...)
	drifted.Navigation[0].Order++
	drift.navigationPublication = &drifted
	if err := boundary.validateNavigationTransition(&drift, &targetMaterial); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact navigation drift passed validation: %v", err)
	}
	if err := boundary.reconcileNavigation(ctx, source.ID, &drift, &targetMaterial, &drift); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact navigation drift passed reconciliation: %v", err)
	}
	if err := boundary.validateNavigationTransition(&sourceMaterial, &targetMaterial); err != nil {
		t.Fatalf("validate navigation transition: %v", err)
	}
	if err := boundary.reconcileNavigation(ctx, source.ID, &sourceMaterial, &targetMaterial, &targetMaterial); err != nil {
		t.Fatalf("publish target navigation: %v", err)
	}
	assertLifecycleNavigationArtifact(t, registry, targetMaterial.navigationPublication.Artifact)
	if err := boundary.reconcileNavigation(ctx, source.ID, &sourceMaterial, &targetMaterial, &sourceMaterial); err != nil {
		t.Fatalf("restore source navigation: %v", err)
	}
	assertLifecycleNavigationArtifact(t, registry, sourceMaterial.navigationPublication.Artifact)
	if err := boundary.reconcileNavigation(ctx, source.ID, &sourceMaterial, nil, nil); err != nil {
		t.Fatalf("disable navigation publication: %v", err)
	}
	if _, found := registry.SnapshotPublication(source.ID); found {
		t.Fatal("disabled navigation publication remains active")
	}

	if _, err := registry.Publish(*targetMaterial.navigationPublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileNavigation(ctx, source.ID, &sourceMaterial, nil, nil); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale source removed replacement navigation publication: %v", err)
	}
	assertLifecycleNavigationArtifact(t, registry, targetMaterial.navigationPublication.Artifact)
}

func TestLifecycleNavigationStartupRestoreSafeModeCoreOnlyAndRevisionFence(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	registry := navigationregistry.New()
	core := navigationregistry.CorePublication()
	if _, err := registry.Publish(core); err != nil {
		t.Fatal(err)
	}
	extension := lifecycleNavigationTestExtension(t, "1.0.0", strings.Repeat("6", 64), 306)
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	impact := strings.Repeat("7", 64)
	authority := &staticAssetAuthority{restore: map[string]string{extension.ID: impact}}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Pages: pages.NewRegistry(nil), Routes: routes.NewRegistry(),
		RouteSchemas: lifecycleRouteSchemaPublication(t), Navigation: registry, AssetAuthority: authority,
	})
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("restore navigation publication: %v", err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := navigationregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: impact,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: runtime.Identity.InstanceID,
	}
	assertLifecycleNavigationArtifact(t, registry, expected)

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, true); err != nil {
		t.Fatalf("restore navigation Safe Mode: %v", err)
	}
	safe := registry.Snapshot()
	if !safe.SafeMode || len(safe.Publications) != 1 || safe.Publications[0].Artifact != core.Artifact {
		t.Fatalf("Safe Mode navigation snapshot = %#v", safe)
	}
	if err := boundary.RestoreNavigationPublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("leave navigation Safe Mode: %v", err)
	}
	assertLifecycleNavigationArtifact(t, registry, expected)

	concurrentCore := lifecycleCoreNavigationPublication(t, "core.nav.concurrent", '8')
	authority.onRestore = func() {
		_, _ = registry.Publish(concurrentCore)
	}
	if err := boundary.RestoreNavigationPublications(ctx, []extensions.Extension{extension}, false); !errors.Is(err, navigationregistry.ErrRevisionConflict) {
		t.Fatalf("concurrent navigation writer bypassed startup revision fence: %v", err)
	}
	if err := boundary.RestoreNavigationPublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("retry navigation restore after concurrent writer: %v", err)
	}
	if snapshot := registry.Snapshot(); len(snapshot.Publications) != 3 {
		t.Fatalf("retry lost Core navigation publication: %#v", snapshot)
	}
}

func TestLifecycleNavigationRejectsPluginClaimingCoreNamespace(t *testing.T) {
	extension := lifecycleNavigationTestExtension(t, "1.0.0", strings.Repeat("9", 64), 307)
	extension.ID = "core.forged-nav"
	extension.Manifest.ID = extension.ID
	extension.Manifest.Navigation[0].ID = extension.ID + ".item"
	extension.Manifest.Navigation[0].ContractVersion = extension.Manifest.Navigation[0].ID + "@1"
	extension.Manifest.Regions[0].ID = extension.ID + ".region"
	extension.Manifest.Regions[0].ContractVersion = extension.Manifest.Regions[0].ID + "@1"
	binding := lifecycleRegistryBinding(extension, "forged-core-runtime")
	if _, err := buildLifecycleNavigationPublication(extension, binding, strings.Repeat("a", 64)); !errors.Is(err, navigationregistry.ErrInvalid) {
		t.Fatalf("plugin core namespace claim = %v", err)
	}
}

func TestLifecycleNavigationReconcilePublishesOnlyCompleteSnapshotsUnderRace(t *testing.T) {
	ctx := context.Background()
	registry := navigationregistry.New()
	if _, err := registry.Publish(navigationregistry.CorePublication()); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Navigation: registry})
	source := lifecycleNavigationTestExtension(t, "1.0.0", strings.Repeat("b", 64), 308)
	target := lifecycleNavigationTestExtension(t, "2.0.0", strings.Repeat("c", 64), 309)
	sourceMaterial := lifecycleNavigationTestMaterial(t, source, "nav-race-source", strings.Repeat("d", 64))
	targetMaterial := lifecycleNavigationTestMaterial(t, target, "nav-race-target", strings.Repeat("e", 64))
	if _, err := registry.Publish(*sourceMaterial.navigationPublication); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errorsSeen := make(chan error, 5)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for iteration := 0; iteration < 200; iteration++ {
			desired := &sourceMaterial
			if iteration%2 == 0 {
				desired = &targetMaterial
			}
			if err := boundary.reconcileNavigation(ctx, source.ID, &sourceMaterial, &targetMaterial, desired); err != nil {
				errorsSeen <- err
				return
			}
		}
	}()
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 400; iteration++ {
				snapshot := registry.Snapshot()
				// Core + exactly one plugin owner must always be visible.
				if len(snapshot.Publications) != 2 || snapshot.Digest == "" {
					errorsSeen <- errors.New("reader observed a partial navigation snapshot")
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}
}

func lifecycleNavigationTestExtension(t *testing.T, version, seed string, versionID int64) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, seed, versionID, "/nav-"+strings.ReplaceAll(version, ".", "-"))
	extension.Manifest.Navigation = []extensions.ManifestNavigation{{
		ID: extension.ID + ".nav.item", ContractVersion: extension.ID + ".nav.item@1",
		Kind: navigationregistry.NavigationKindItem, Action: navigationregistry.ActionAdd,
		Label: extension.ID + ".nav.item", Href: "/nav/" + version, Order: 10,
	}}
	extension.Manifest.Regions = []extensions.ManifestRegion{{
		ID: extension.ID + ".region.widget", ContractVersion: extension.ID + ".region.widget@1",
		Kind: navigationregistry.RegionKindWidget, Action: navigationregistry.ActionAdd,
		Label: extension.ID + ".region.widget", Multiple: true,
	}}
	manifestDocument, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extension.PackagePath, extensionmanifest.ManifestFileName), manifestDocument, 0o600); err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest, err = extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	return extension
}

func lifecycleNavigationTestMaterial(
	t *testing.T,
	extension extensions.Extension,
	runtimeInstanceID string,
	impactDigest string,
) lifecycleRegistryMaterial {
	t.Helper()
	material, err := buildLifecycleRegistryMaterial(extension, lifecycleRegistryBinding(extension, runtimeInstanceID))
	if err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Navigation: navigationregistry.New(),
	})
	if err := boundary.freezeNavigationMaterial(&material, impactDigest); err != nil {
		t.Fatal(err)
	}
	return material
}

func assertLifecycleNavigationArtifact(t *testing.T, registry *navigationregistry.Registry, expected navigationregistry.Artifact) {
	t.Helper()
	publication, found := registry.SnapshotPublication(expected.ExtensionID)
	if !found || publication.Artifact != expected || len(publication.Navigation) != 1 ||
		publication.Navigation[0].ID != expected.ExtensionID+".nav.item" ||
		len(publication.Regions) != 1 {
		t.Fatalf("navigation publication = %#v, found=%t, expected=%#v", publication, found, expected)
	}
}

func lifecycleCoreNavigationPublication(t *testing.T, id string, marker byte) navigationregistry.Publication {
	t.Helper()
	artifact, err := navigationregistry.NewCoreArtifact(
		id, "1.0.0", strings.Repeat(string(marker), 64), strings.Repeat(string(marker+1), 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	return navigationregistry.Publication{Artifact: artifact, Navigation: []navigationregistry.NavigationDeclaration{{
		ID: id + ".menu", ContractVersion: id + ".menu@1", Kind: navigationregistry.NavigationKindMenu,
		Action: navigationregistry.ActionAdd, Label: id, Visibility: navigationregistry.VisibilityPublic,
	}}}
}
