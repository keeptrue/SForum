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
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

func TestLifecycleSEOMaterialFreezesExactAuthorityAndReachableDigestAliases(t *testing.T) {
	extension := lifecycleSEOTestExtension(t, "1.0.0", strings.Repeat("a", 64), 201)
	material, err := buildLifecycleRegistryMaterial(extension, lifecycleRegistryBinding(extension, "seo-digest-runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if material.seoPublication != nil {
		t.Fatal("SEO publication was built before exact lifecycle authority was frozen")
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{SEO: seoregistry.New()})
	impact := strings.Repeat("b", 64)
	if err := boundary.freezeSEOMaterial(&material, impact); err != nil {
		t.Fatal(err)
	}
	publication := material.seoPublication
	if publication == nil || publication.Artifact.ExtensionID != extension.ID ||
		publication.Artifact.ExtensionVersion != extension.Version ||
		publication.Artifact.PackageDigest != extension.PackageDigest || publication.Artifact.ImpactDigest != impact ||
		publication.Artifact.VersionID != extension.ActiveVersionID ||
		publication.Artifact.RuntimeInstanceID != "seo-digest-runtime" || len(publication.Contributions) != 1 {
		t.Fatalf("frozen SEO publication = %#v", publication)
	}
	v1Digest, err := encodeLifecycleRegistryMaterialDigest(&material, false, false)
	if err != nil {
		t.Fatal(err)
	}
	v5Digest, err := encodeLifecycleRegistryMaterialDigestV5(&material)
	if err != nil {
		t.Fatal(err)
	}
	if material.digest != v5Digest || material.legacyDigest != v1Digest ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest}) || v5Digest == v1Digest {
		t.Fatalf("SEO material digests primary=%s legacy=%s aliases=%v", material.digest, material.legacyDigest,
			registryMaterialCompatibleDigests(&material))
	}

	extension.Manifest.SEO[0].Handler = extension.ID + ".seo.mutated"
	if publication.Contributions[0].Handler != extension.ID+".seo.topic-title" {
		t.Fatalf("Manifest mutation changed frozen SEO publication = %#v", publication.Contributions[0])
	}
	before := material.digest
	material.seoPublication.Contributions[0].Priority++
	if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
		t.Fatal(err)
	}
	if material.digest == before ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest}) {
		t.Fatalf("SEO declaration drift digest=%s aliases=%v", material.digest, registryMaterialCompatibleDigests(&material))
	}
}

func TestLifecycleSEOMaterialsUseSourceRestoreAndTargetOperationAuthority(t *testing.T) {
	source := lifecycleSEOTestExtension(t, "1.0.0", strings.Repeat("c", 64), 202)
	target := lifecycleSEOTestExtension(t, "2.0.0", strings.Repeat("d", 64), 203)
	sourceBinding := lifecycleRegistryBinding(source, "seo-authority-source")
	targetBinding := lifecycleRegistryBinding(target, "seo-authority-target")
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
		SEO: seoregistry.New(), AssetAuthority: authority,
	})
	request := lifecycleRegistryRequest(source, target, sourceBinding, targetBinding, 1)
	if err := boundary.freezeSEOMaterials(context.Background(), request, &sourceMaterial, &targetMaterial); err != nil {
		t.Fatal(err)
	}
	if sourceMaterial.seoPublication.Artifact.ImpactDigest != sourceImpact ||
		targetMaterial.seoPublication.Artifact.ImpactDigest != targetImpact ||
		sourceMaterial.digest == targetMaterial.digest {
		t.Fatalf("source/target SEO authority = source %#v target %#v",
			sourceMaterial.seoPublication.Artifact, targetMaterial.seoPublication.Artifact)
	}
	if !reflect.DeepEqual(authority.calls, []string{"restore:" + source.ID, "operation:" + target.ID}) {
		t.Fatalf("SEO authority calls = %#v", authority.calls)
	}
}

func TestLifecycleSEOMaterialAliasesOnlyReachablePriorEncoders(t *testing.T) {
	base := lifecycleSEOTestMaterial(
		t, lifecycleSEOTestExtension(t, "1.0.0", strings.Repeat("0", 64), 210),
		"seo-alias-runtime", strings.Repeat("1", 64),
	)
	tests := []struct {
		name                            string
		withAsset, withQuery, withCache bool
		wantAliases                     int
	}{
		{name: "seo only", wantAliases: 1},
		{name: "asset and seo", withAsset: true, wantAliases: 2},
		{name: "query and seo", withQuery: true, wantAliases: 2},
		{name: "cache and seo", withCache: true, wantAliases: 2},
		{name: "all prior families and seo", withAsset: true, withQuery: true, withCache: true, wantAliases: 4},
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

func TestLifecycleSEOPublicationUpgradeRollbackDisableAndStaleCAS(t *testing.T) {
	ctx := context.Background()
	registry := seoregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{SEO: registry})
	if boundary.SEORegistry() != registry {
		t.Fatal("lifecycle boundary created a second SEO Registry")
	}
	source := lifecycleSEOTestExtension(t, "1.0.0", strings.Repeat("1", 64), 204)
	target := lifecycleSEOTestExtension(t, "2.0.0", strings.Repeat("2", 64), 205)
	sourceMaterial := lifecycleSEOTestMaterial(t, source, "seo-source-runtime", strings.Repeat("3", 64))
	targetMaterial := lifecycleSEOTestMaterial(t, target, "seo-target-runtime", strings.Repeat("4", 64))
	if _, err := registry.Publish(*sourceMaterial.seoPublication); err != nil {
		t.Fatal(err)
	}

	drift := sourceMaterial
	drifted := *sourceMaterial.seoPublication
	drifted.Contributions = append([]seoregistry.Declaration(nil), drifted.Contributions...)
	drifted.Contributions[0].Priority++
	drift.seoPublication = &drifted
	if err := boundary.validateSEOTransition(&drift, &targetMaterial); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact SEO drift passed validation: %v", err)
	}
	if err := boundary.reconcileSEO(ctx, source.ID, &drift, &targetMaterial, &drift); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact SEO drift passed reconciliation: %v", err)
	}
	if err := boundary.validateSEOTransition(&sourceMaterial, &targetMaterial); err != nil {
		t.Fatalf("validate SEO transition: %v", err)
	}
	if err := boundary.reconcileSEO(ctx, source.ID, &sourceMaterial, &targetMaterial, &targetMaterial); err != nil {
		t.Fatalf("publish target SEO: %v", err)
	}
	assertLifecycleSEOArtifact(t, registry, targetMaterial.seoPublication.Artifact)
	if err := boundary.reconcileSEO(ctx, source.ID, &sourceMaterial, &targetMaterial, &sourceMaterial); err != nil {
		t.Fatalf("restore source SEO: %v", err)
	}
	assertLifecycleSEOArtifact(t, registry, sourceMaterial.seoPublication.Artifact)
	if err := boundary.reconcileSEO(ctx, source.ID, &sourceMaterial, nil, nil); err != nil {
		t.Fatalf("disable SEO publication: %v", err)
	}
	if _, found := registry.SnapshotPublication(source.ID); found {
		t.Fatal("disabled SEO publication remains active")
	}

	if _, err := registry.Publish(*targetMaterial.seoPublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileSEO(ctx, source.ID, &sourceMaterial, nil, nil); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale source removed replacement SEO publication: %v", err)
	}
	assertLifecycleSEOArtifact(t, registry, targetMaterial.seoPublication.Artifact)
}

func TestLifecycleSEOStartupRestoreSafeModeCoreOnlyAndRevisionFence(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	registry := seoregistry.New()
	core := lifecycleCoreSEOPublication(t, "core.seo.bootstrap", '5')
	if _, err := registry.Publish(core); err != nil {
		t.Fatal(err)
	}
	extension := lifecycleSEOTestExtension(t, "1.0.0", strings.Repeat("6", 64), 206)
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	impact := strings.Repeat("7", 64)
	authority := &staticAssetAuthority{restore: map[string]string{extension.ID: impact}}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Pages: pages.NewRegistry(nil), Routes: routes.NewRegistry(),
		RouteSchemas: lifecycleRouteSchemaPublication(t), SEO: registry, AssetAuthority: authority,
	})
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("restore SEO publication: %v", err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := seoregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: impact,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: runtime.Identity.InstanceID,
	}
	assertLifecycleSEOArtifact(t, registry, expected)

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, true); err != nil {
		t.Fatalf("restore SEO Safe Mode: %v", err)
	}
	safe := registry.Snapshot()
	if !safe.SafeMode || len(safe.Publications) != 1 || safe.Publications[0].Artifact != core.Artifact ||
		len(safe.Contributions) != 1 {
		t.Fatalf("Safe Mode SEO snapshot = %#v", safe)
	}
	if err := boundary.RestoreSEOPublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("leave SEO Safe Mode: %v", err)
	}
	assertLifecycleSEOArtifact(t, registry, expected)

	concurrentCore := lifecycleCoreSEOPublication(t, "core.seo.concurrent", '8')
	authority.onRestore = func() {
		_, _ = registry.Publish(concurrentCore)
	}
	if err := boundary.RestoreSEOPublications(ctx, []extensions.Extension{extension}, false); !errors.Is(err, seoregistry.ErrRevisionConflict) {
		t.Fatalf("concurrent SEO writer bypassed startup revision fence: %v", err)
	}
	if err := boundary.RestoreSEOPublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("retry SEO restore after concurrent writer: %v", err)
	}
	if snapshot := registry.Snapshot(); len(snapshot.Publications) != 3 {
		t.Fatalf("retry lost Core SEO publication: %#v", snapshot)
	}
}

func TestLifecycleSEORejectsPluginClaimingCoreNamespace(t *testing.T) {
	extension := lifecycleSEOTestExtension(t, "1.0.0", strings.Repeat("9", 64), 207)
	extension.ID = "core.forged-seo"
	extension.Manifest.ID = extension.ID
	extension.Manifest.SEO[0].ID = extension.ID + ".title"
	extension.Manifest.SEO[0].ContractVersion = extension.Manifest.SEO[0].ID + "@1"
	extension.Manifest.SEO[0].Handler = extension.ID + ".title"
	binding := lifecycleRegistryBinding(extension, "forged-core-runtime")
	if _, err := buildLifecycleSEOPublication(extension, binding, strings.Repeat("a", 64)); !errors.Is(err, seoregistry.ErrInvalid) {
		t.Fatalf("plugin core namespace claim = %v", err)
	}
}

func TestLifecycleSEOReconcilePublishesOnlyCompleteSnapshotsUnderRace(t *testing.T) {
	ctx := context.Background()
	registry := seoregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{SEO: registry})
	source := lifecycleSEOTestExtension(t, "1.0.0", strings.Repeat("b", 64), 208)
	target := lifecycleSEOTestExtension(t, "2.0.0", strings.Repeat("c", 64), 209)
	sourceMaterial := lifecycleSEOTestMaterial(t, source, "seo-race-source", strings.Repeat("d", 64))
	targetMaterial := lifecycleSEOTestMaterial(t, target, "seo-race-target", strings.Repeat("e", 64))
	if _, err := registry.Publish(*sourceMaterial.seoPublication); err != nil {
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
			if err := boundary.reconcileSEO(ctx, source.ID, &sourceMaterial, &targetMaterial, desired); err != nil {
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
				if len(snapshot.Publications) != 1 || len(snapshot.Contributions) != 1 || snapshot.Digest == "" {
					errorsSeen <- errors.New("reader observed a partial SEO snapshot")
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

func lifecycleSEOTestExtension(t *testing.T, version, seed string, versionID int64) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, seed, versionID, "/seo-"+strings.ReplaceAll(version, ".", "-"))
	extension.Manifest.SEO = []extensions.ManifestSEO{{
		ID: extension.ID + ".seo.topic-title", ContractVersion: extension.ID + ".seo.topic-title@1",
		Scope: "core.page.topic", Kind: seoregistry.KindTitle, Action: seoregistry.ActionFilter,
		Handler: extension.ID + ".seo.topic-title", Priority: 100,
		FailurePolicy: seoregistry.FailurePolicyFallback, TimeoutMS: 500,
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

func lifecycleSEOTestMaterial(
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
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{SEO: seoregistry.New()})
	if err := boundary.freezeSEOMaterial(&material, impactDigest); err != nil {
		t.Fatal(err)
	}
	return material
}

func assertLifecycleSEOArtifact(t *testing.T, registry *seoregistry.Registry, expected seoregistry.Artifact) {
	t.Helper()
	publication, found := registry.SnapshotPublication(expected.ExtensionID)
	if !found || publication.Artifact != expected || len(publication.Contributions) != 1 ||
		publication.Contributions[0].Handler != expected.ExtensionID+".seo.topic-title" {
		t.Fatalf("SEO publication = %#v, found=%t, expected=%#v", publication, found, expected)
	}
}

func lifecycleCoreSEOPublication(t *testing.T, id string, marker byte) seoregistry.Publication {
	t.Helper()
	artifact, err := seoregistry.NewCoreArtifact(
		id, "1.0.0", strings.Repeat(string(marker), 64), strings.Repeat(string(marker+1), 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	return seoregistry.Publication{Artifact: artifact, Contributions: []seoregistry.Declaration{{
		ID: id + ".title", ContractVersion: id + ".title@1", Scope: "core.page.home",
		Kind: seoregistry.KindTitle, Action: seoregistry.ActionFilter, Handler: id + ".title",
		FailurePolicy: seoregistry.FailurePolicyFallback,
	}}}
}
