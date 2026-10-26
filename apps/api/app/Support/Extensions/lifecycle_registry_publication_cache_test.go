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
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

func TestLifecycleCacheMaterialFreezesManifestAndReachableDigestAliases(t *testing.T) {
	extension := lifecycleCacheTestExtension(t, "1.0.0", strings.Repeat("a", 64), 101)
	material := lifecycleCacheTestMaterial(t, extension, "cache-digest-runtime")
	publication := material.cachePublication
	if publication.Artifact.ExtensionID != extension.ID ||
		publication.Artifact.ExtensionVersion != extension.Version ||
		publication.Artifact.PackageDigest != extension.PackageDigest ||
		publication.Artifact.VersionID != extension.ActiveVersionID ||
		publication.Artifact.RuntimeInstanceID != "cache-digest-runtime" || len(publication.Caches) != 1 {
		t.Fatalf("frozen cache publication = %#v", publication)
	}

	v1Digest, err := encodeLifecycleRegistryMaterialDigest(&material, false, false)
	if err != nil {
		t.Fatal(err)
	}
	v2Digest, err := encodeLifecycleRegistryMaterialDigest(&material, true, false)
	if err != nil {
		t.Fatal(err)
	}
	v3Digest, err := encodeLifecycleRegistryMaterialDigest(&material, true, true)
	if err != nil {
		t.Fatal(err)
	}
	v4Digest, err := encodeLifecycleRegistryMaterialDigestV4(&material)
	if err != nil {
		t.Fatal(err)
	}
	aliases := []string{v1Digest}
	if material.digest != v4Digest || material.legacyDigest != v1Digest ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), aliases) {
		t.Fatalf("cache material digests primary=%s legacy=%s aliases=%v", material.digest, material.legacyDigest,
			registryMaterialCompatibleDigests(&material))
	}
	if v4Digest == v1Digest || v4Digest == v2Digest || v4Digest == v3Digest ||
		!validLifecycleRegistryCompatibleDigests(aliases, v4Digest) {
		t.Fatal("@4 cache-only material was not separated from its reachable @1 alias")
	}
	input := PrepareLifecycleRegistryPublicationInput{
		Fence: lifecyclePublicationFence{
			OperationID: 101, Operation: extensions.LifecycleMachineUpgrade, StepID: "registry",
			Position: 1, Mode: LifecycleBoundaryActivate, Attempt: 1,
		},
		SourceDigest: v4Digest, TargetDigest: v4Digest,
		CompatibleSourceDigests: aliases, CompatibleTargetDigests: aliases,
	}
	if !validLifecycleRegistryPrepareInput(input) {
		t.Fatal("@4 publication input rejected its exact reachable recovery alias")
	}
	for _, stored := range aliases {
		record := lifecycleRegistryPublicationRecord{Fence: input.Fence, SourceDigest: stored, TargetDigest: stored}
		if !record.matchesInput(input) {
			t.Fatalf("@4 material cannot resume exact historical digest %s", stored)
		}
	}

	// Caller-owned Manifest slices cannot rewrite already frozen publication.
	extension.Manifest.Cache[0].Tags[0] = extension.ID + ".mutated"
	extension.Manifest.Cache[0].Invalidators[0] = extension.ID + ".mutated"
	if publication.Caches[0].Tags[0] != "registry.demo.cache.tag" ||
		publication.Caches[0].Invalidators[0] != "registry.demo.cache.invalidate" {
		t.Fatalf("Manifest mutation changed frozen publication = %#v", publication.Caches[0])
	}

	before := material.digest
	material.cachePublication.Caches[0].Tags = []string{extension.ID + ".cache.changed"}
	if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
		t.Fatal(err)
	}
	if material.digest == before || !reflect.DeepEqual(registryMaterialCompatibleDigests(&material), aliases) {
		t.Fatalf("cache drift digest=%s aliases=%v", material.digest, registryMaterialCompatibleDigests(&material))
	}
}

func TestLifecycleCacheMaterialAliasesOnlyReachablePriorEncoders(t *testing.T) {
	base := lifecycleCacheTestMaterial(
		t, lifecycleCacheTestExtension(t, "1.0.0", strings.Repeat("4", 64), 109), "cache-alias-runtime",
	)
	tests := []struct {
		name      string
		withAsset bool
		withQuery bool
		wantAlias int
	}{
		{name: "cache only", wantAlias: 1},
		{name: "asset and cache", withAsset: true, wantAlias: 2},
		{name: "query and cache", withQuery: true, wantAlias: 2},
		{name: "asset query and cache", withAsset: true, withQuery: true, wantAlias: 3},
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
			if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
				t.Fatal(err)
			}
			aliases := registryMaterialCompatibleDigests(&material)
			if len(aliases) != test.wantAlias ||
				!validLifecycleRegistryCompatibleDigests(aliases, material.digest) {
				t.Fatalf("aliases=%v, want count=%d", aliases, test.wantAlias)
			}
		})
	}
}

func TestLifecycleCachePublicationUpgradeRollbackDisableAndStaleCAS(t *testing.T) {
	ctx := context.Background()
	caches := cacheregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Caches: caches})
	if boundary.CacheRegistry() != caches {
		t.Fatal("lifecycle boundary created a second Cache Registry")
	}
	source := lifecycleCacheTestExtension(t, "1.0.0", strings.Repeat("b", 64), 102)
	target := lifecycleCacheTestExtension(t, "2.0.0", strings.Repeat("c", 64), 103)
	sourceMaterial := lifecycleCacheTestMaterial(t, source, "cache-source-runtime")
	targetMaterial := lifecycleCacheTestMaterial(t, target, "cache-target-runtime")

	if _, err := caches.Publish(*sourceMaterial.cachePublication); err != nil {
		t.Fatal(err)
	}
	drift := sourceMaterial
	driftPublication := *sourceMaterial.cachePublication
	driftPublication.Caches = append([]cacheregistry.Declaration(nil), driftPublication.Caches...)
	driftPublication.Caches[0].Policy = cacheregistry.PolicyPublic
	drift.cachePublication = &driftPublication
	if err := boundary.validateCacheTransition(&drift, &targetMaterial); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact cache declaration drift passed validation: %v", err)
	}
	if err := boundary.reconcileCaches(ctx, source.ID, &drift, &targetMaterial, &drift); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact cache declaration drift passed reconciliation: %v", err)
	}
	if err := boundary.validateCacheTransition(&sourceMaterial, &targetMaterial); err != nil {
		t.Fatalf("validate cache transition: %v", err)
	}
	if err := boundary.reconcileCaches(ctx, source.ID, &sourceMaterial, &targetMaterial, &targetMaterial); err != nil {
		t.Fatalf("publish target cache: %v", err)
	}
	assertLifecycleCacheArtifact(t, caches, targetMaterial.cachePublication.Artifact)

	// Snapshot and nested declaration slices are immutable caller copies.
	snapshot := caches.Snapshot()
	snapshot.Publications[0].Caches[0].Tags[0] = "registry.demo.cache.corrupt"
	snapshot.Caches[0].Invalidators[0] = "registry.demo.cache.corrupt"
	assertLifecycleCacheArtifact(t, caches, targetMaterial.cachePublication.Artifact)

	if err := boundary.reconcileCaches(ctx, source.ID, &sourceMaterial, &targetMaterial, &sourceMaterial); err != nil {
		t.Fatalf("restore source cache: %v", err)
	}
	assertLifecycleCacheArtifact(t, caches, sourceMaterial.cachePublication.Artifact)
	if err := boundary.reconcileCaches(ctx, source.ID, &sourceMaterial, nil, nil); err != nil {
		t.Fatalf("disable cache publication: %v", err)
	}
	if _, found := caches.SnapshotPublication(source.ID); found {
		t.Fatal("disabled cache publication remains active")
	}

	if _, err := caches.Publish(*targetMaterial.cachePublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileCaches(ctx, source.ID, &sourceMaterial, nil, nil); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale source removed replacement cache publication: %v", err)
	}
	assertLifecycleCacheArtifact(t, caches, targetMaterial.cachePublication.Artifact)
}

func TestLifecycleCacheStartupRestoreAdmissionSafeModeAndCoreOnly(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	caches := cacheregistry.New()
	core := lifecycleCoreCachePublication(t)
	if _, err := caches.Publish(core); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})
	extension := lifecycleCacheTestExtension(t, "1.0.0", strings.Repeat("d", 64), 104)
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	if err := boundary.restoreCachePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("restore cache publication: %v", err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := cacheregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	}
	assertLifecycleCacheArtifact(t, caches, expected)
	resolved, err := caches.Resolve(extension.ID + ".cache.primary")
	if err != nil || resolved.Artifact != expected {
		t.Fatalf("admitted cache resolve = %#v, %v", resolved, err)
	}
	resolved.Tags[0] = extension.ID + ".cache.corrupt"
	again, err := caches.Resolve(extension.ID + ".cache.primary")
	if err != nil || again.Tags[0] != extension.ID+".cache.tag" {
		t.Fatalf("resolved mutation escaped into Registry = %#v, %v", again, err)
	}

	if err := boundary.restoreCachePublications(ctx, []extensions.Extension{extension}, true); err != nil {
		t.Fatalf("restore cache Safe Mode: %v", err)
	}
	safe := caches.Snapshot()
	if !safe.SafeMode || len(safe.Publications) != 1 || len(safe.Caches) != 1 ||
		safe.Publications[0].Artifact != core.Artifact {
		t.Fatalf("Safe Mode cache snapshot = %#v", safe)
	}
	if _, err := caches.Resolve(extension.ID + ".cache.primary"); !errors.Is(err, cacheregistry.ErrNotFound) {
		t.Fatalf("Safe Mode plugin cache resolve = %v", err)
	}
	if _, err := caches.Resolve(core.Caches[0].ID); err != nil {
		t.Fatalf("Safe Mode Core cache resolve = %v", err)
	}

	if err := boundary.restoreCachePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("leave cache Safe Mode: %v", err)
	}
	if _, err := manager.BeginDrain(runtime.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := caches.Resolve(extension.ID + ".cache.primary"); !errors.Is(err, cacheregistry.ErrArtifactUnavailable) {
		t.Fatalf("draining runtime cache resolve = %v", err)
	}
}

func TestLifecycleCacheStartupSkipsMissingRuntimeAndRejectsMismatchedRuntime(t *testing.T) {
	ctx := context.Background()
	desired := lifecycleCacheTestExtension(t, "2.0.0", strings.Repeat("e", 64), 106)
	missingCaches := cacheregistry.New()
	missing := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: NewManager(ManagerConfig{Starter: newManagerStagedStarter()}), Caches: missingCaches,
	})
	if err := missing.restoreCachePublications(ctx, []extensions.Extension{desired}, false); err != nil {
		t.Fatal(err)
	}
	if snapshot := missingCaches.Snapshot(); len(snapshot.Publications) != 0 || len(snapshot.Caches) != 0 {
		t.Fatalf("missing runtime cache publication = %#v", snapshot)
	}

	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	running := lifecycleCacheTestExtension(t, "1.0.0", strings.Repeat("f", 64), 105)
	if err := manager.Start(ctx, running); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: cacheregistry.New(),
	})
	if err := boundary.restoreCachePublications(ctx, []extensions.Extension{desired}, false); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("mismatched startup cache runtime = %v", err)
	}
}

func TestLifecycleCacheReconcileConcurrentExactCAS(t *testing.T) {
	ctx := context.Background()
	caches := cacheregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Caches: caches})
	source := lifecycleCacheTestExtension(t, "1.0.0", strings.Repeat("1", 64), 107)
	target := lifecycleCacheTestExtension(t, "2.0.0", strings.Repeat("2", 64), 108)
	sourceMaterial := lifecycleCacheTestMaterial(t, source, "cache-race-source")
	targetMaterial := lifecycleCacheTestMaterial(t, target, "cache-race-target")
	if _, err := caches.Publish(*sourceMaterial.cachePublication); err != nil {
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
			if err := boundary.reconcileCaches(ctx, source.ID, &sourceMaterial, &targetMaterial, desired); err != nil {
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
				snapshot := caches.Snapshot()
				if len(snapshot.Publications) != 1 || len(snapshot.Caches) != 1 || snapshot.Digest == "" {
					errorsSeen <- errors.New("reader observed a partial cache snapshot")
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
	publication, found := caches.SnapshotPublication(source.ID)
	if !found || (publication.Artifact != sourceMaterial.cachePublication.Artifact &&
		publication.Artifact != targetMaterial.cachePublication.Artifact) {
		t.Fatalf("concurrent final cache publication = %#v, found=%t", publication, found)
	}
}

func lifecycleCacheTestExtension(t *testing.T, version, seed string, versionID int64) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, seed, versionID, "/cache-"+strings.ReplaceAll(version, ".", "-"))
	extension.Manifest.Cache = []extensions.ManifestCache{{
		ID: extension.ID + ".cache.primary", ContractVersion: extension.ID + ".cache.primary@1",
		Namespace: extension.ID + ".cache", Policy: cacheregistry.PolicyActor,
		Tags: []string{extension.ID + ".cache.tag"}, Provider: "core.cache.redis",
		Invalidators: []string{extension.ID + ".cache.invalidate"},
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

func lifecycleCacheTestMaterial(
	t *testing.T,
	extension extensions.Extension,
	runtimeInstanceID string,
) lifecycleRegistryMaterial {
	t.Helper()
	material, err := buildLifecycleRegistryMaterial(extension, lifecycleRegistryBinding(extension, runtimeInstanceID))
	if err != nil {
		t.Fatal(err)
	}
	if material.cachePublication == nil {
		t.Fatal("cache declaration did not produce a lifecycle publication")
	}
	return material
}

func assertLifecycleCacheArtifact(t *testing.T, registry *cacheregistry.Registry, expected cacheregistry.Artifact) {
	t.Helper()
	publication, found := registry.SnapshotPublication(expected.ExtensionID)
	if !found || publication.Artifact != expected || len(publication.Caches) != 1 ||
		publication.Caches[0].Tags[0] != expected.ExtensionID+".cache.tag" ||
		publication.Caches[0].Invalidators[0] != expected.ExtensionID+".cache.invalidate" {
		t.Fatalf("cache publication = %#v, found=%t, expected=%#v", publication, found, expected)
	}
}

func lifecycleCoreCachePublication(t *testing.T) cacheregistry.Publication {
	t.Helper()
	artifact, err := cacheregistry.NewCoreArtifact("core.cache", "1.0.0", strings.Repeat("3", 64))
	if err != nil {
		t.Fatal(err)
	}
	return cacheregistry.Publication{Artifact: artifact, Caches: []cacheregistry.Declaration{{
		ID: "core.cache.health", ContractVersion: "core.cache.health@1",
		Namespace: "core.cache.health", Policy: cacheregistry.PolicyPublic,
	}}}
}
