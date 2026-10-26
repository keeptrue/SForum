package extensionsruntime

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func TestLegacyRuntimeCachesPublishCompleteDeclarationAndRollbackQuarantine(t *testing.T) {
	extension := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	if extension.Manifest.Lifecycle != nil {
		t.Fatal("fixture unexpectedly uses Lifecycle V2")
	}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	caches := cacheregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})

	published, err := boundary.PublishRuntimeCaches(t.Context(), extension)
	if err != nil || published == nil {
		t.Fatalf("publish legacy runtime caches = %#v, %v", published, err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	publication, found := caches.SnapshotPublication(extension.ID)
	want := extension.Manifest.Cache[0]
	if !found || publication.Artifact.VersionID != extension.ActiveVersionID ||
		publication.Artifact.RuntimeInstanceID != runtime.Identity.InstanceID || len(publication.Caches) != 2 {
		t.Fatalf("published exact runtime caches = %#v", publication)
	}
	declaration := cacheregistry.Declaration{}
	for _, candidate := range publication.Caches {
		if candidate.ID == want.ID {
			declaration = candidate
		}
	}
	if declaration.ID != want.ID || declaration.ContractVersion != want.ContractVersion ||
		declaration.Namespace != want.Namespace || declaration.Policy != want.Policy ||
		declaration.Provider != want.Provider || !reflect.DeepEqual(declaration.Tags, want.Tags) ||
		!reflect.DeepEqual(declaration.Invalidators, want.Invalidators) {
		t.Fatalf("incomplete cache declaration = %#v, want=%#v", declaration, want)
	}

	quarantined, err := boundary.QuarantineRuntimeCaches(t.Context(), extension)
	if err != nil || quarantined == nil {
		t.Fatalf("quarantine legacy runtime caches = %#v, %v", quarantined, err)
	}
	if _, found := caches.SnapshotPublication(extension.ID); found || manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatal("quarantine left cache publication or runtime admission open")
	}
	if err := quarantined.Rollback(); err != nil {
		t.Fatalf("rollback cache quarantine: %v", err)
	}
	publication, found = caches.SnapshotPublication(extension.ID)
	if !found || publication.Artifact.RuntimeInstanceID != runtime.Identity.InstanceID ||
		!manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatalf("rollback did not restore publication-before-admission: %#v", publication)
	}
}

func TestLegacyRuntimeCacheStaleArtifactCannotReplaceOrRemoveNewer(t *testing.T) {
	stale := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	newer := legacyRuntimeCacheExtension("2.0.0", 'b', 42)
	newerPublication := runtimeCacheTestPublication(t, newer, "runtime-newer")
	caches := cacheregistry.New()
	if _, err := caches.Publish(newerPublication); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	runtime, _ := manager.ActiveRuntimeInstance(stale.ID)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})

	if _, err := boundary.PublishRuntimeCaches(t.Context(), stale); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale publish replaced newer cache artifact = %v", err)
	}
	if _, err := boundary.QuarantineRuntimeCaches(t.Context(), stale); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale disable removed newer cache artifact = %v", err)
	}
	active, found := caches.SnapshotPublication(stale.ID)
	if !found || active.Artifact != newerPublication.Artifact || !manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatalf("stale operation changed newer artifact/admission: %#v", active)
	}
}

func TestLegacyRuntimeCacheDisableRejectsStaleRuntimeInstanceBeforeDrain(t *testing.T) {
	extension := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	caches := cacheregistry.New()
	stale := runtimeCacheTestPublication(t, extension, "runtime-stale")
	if _, err := caches.Publish(stale); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})

	if _, err := boundary.QuarantineRuntimeCaches(t.Context(), extension); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale runtime instance quarantine = %v", err)
	}
	active, found := caches.SnapshotPublication(extension.ID)
	if !found || active.Artifact != stale.Artifact || !manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatalf("stale runtime instance changed publication/admission: %#v", active)
	}
}

func TestLegacyRuntimeCacheDeclarationDriftFailsClosed(t *testing.T) {
	extension := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	caches := cacheregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})
	if _, err := boundary.PublishRuntimeCaches(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	before, _ := caches.SnapshotPublication(extension.ID)

	drift := extension
	drift.Manifest.Cache = append([]extensions.ManifestCache(nil), extension.Manifest.Cache...)
	drift.Manifest.Cache[0].Tags = []string{extension.ID + ".cache.changed"}
	mutation, err := boundary.PublishRuntimeCaches(t.Context(), drift)
	if !errors.Is(err, cacheregistry.ErrArtifactConflict) || mutation == nil {
		t.Fatalf("same-artifact cache declaration drift = %#v, %v", mutation, err)
	}
	active, found := caches.SnapshotPublication(extension.ID)
	if !found || !reflect.DeepEqual(active, before) {
		t.Fatalf("declaration drift changed active publication = %#v", active)
	}
	if err := mutation.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, found := caches.SnapshotPublication(extension.ID); found {
		t.Fatal("failed enable compensation retained drifted exact cache publication")
	}
}

func TestLegacyRuntimeCacheDisableThenReenablePublishesNewInstance(t *testing.T) {
	extension := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	caches := cacheregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.PublishRuntimeCaches(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	first, _ := caches.SnapshotPublication(extension.ID)
	if _, err := boundary.QuarantineRuntimeCaches(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	if _, found := caches.SnapshotPublication(extension.ID); found {
		t.Fatal("disabled runtime left cache publication active")
	}

	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.PublishRuntimeCaches(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	second, found := caches.SnapshotPublication(extension.ID)
	if !found || second.Artifact.RuntimeInstanceID == first.Artifact.RuntimeInstanceID {
		t.Fatalf("re-enable retained stale runtime identity: first=%#v second=%#v", first.Artifact, second.Artifact)
	}
	resolved, err := caches.Resolve(extension.Manifest.Cache[0].ID)
	if err != nil || resolved.Artifact != second.Artifact {
		t.Fatalf("re-enabled cache admission = %#v, %v", resolved, err)
	}
}

func TestLegacyRuntimeCachePublishRollbackCannotDeleteConcurrentReplacement(t *testing.T) {
	extension := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	caches := cacheregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})
	mutation, err := boundary.PublishRuntimeCaches(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	active, _ := caches.SnapshotPublication(extension.ID)
	newer := legacyRuntimeCacheExtension("2.0.0", 'b', 42)
	replacement := runtimeCacheTestPublication(t, newer, "runtime-newer")
	if _, err := caches.PublishIfArtifact(active.Artifact, replacement); err != nil {
		t.Fatal(err)
	}

	if err := mutation.Rollback(); !errors.Is(err, cacheregistry.ErrArtifactConflict) {
		t.Fatalf("stale rollback removed concurrent replacement = %v", err)
	}
	current, found := caches.SnapshotPublication(extension.ID)
	if !found || current.Artifact != replacement.Artifact {
		t.Fatalf("concurrent replacement changed by stale rollback = %#v", current)
	}
}

func TestLegacyRuntimeCacheIdempotentPublishRollbackClosesPublication(t *testing.T) {
	extension := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	caches := cacheregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})
	if _, err := boundary.PublishRuntimeCaches(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	replayed, err := boundary.PublishRuntimeCaches(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayed.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, found := caches.SnapshotPublication(extension.ID); found {
		t.Fatal("failed enable compensation retained idempotently replayed cache publication")
	}
}

func TestLegacyRuntimeCacheMutationRollbackIsRaceSafeAndIdempotent(t *testing.T) {
	extension := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	caches := cacheregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})
	mutation, err := boundary.PublishRuntimeCaches(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 16)
	var group sync.WaitGroup
	for index := 0; index < cap(errs); index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errs <- mutation.Rollback()
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, found := caches.SnapshotPublication(extension.ID); found {
		t.Fatal("concurrent rollback left cache publication active")
	}
}

func TestLegacyRuntimeCachePublishFailsClosedInSafeMode(t *testing.T) {
	extension := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	caches := cacheregistry.New()
	if _, err := caches.ReplaceAll(nil, true); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Caches: caches,
	})
	if _, err := boundary.PublishRuntimeCaches(t.Context(), extension); !errors.Is(err, cacheregistry.ErrSafeMode) {
		t.Fatalf("Safe Mode cache publication = %v", err)
	}
	if snapshot := caches.Snapshot(); !snapshot.SafeMode || len(snapshot.Publications) != 0 {
		t.Fatalf("Safe Mode cache snapshot = %#v", snapshot)
	}
}

func TestCacheStartupRestoreRemovesDisabledLegacyPublication(t *testing.T) {
	extension := legacyRuntimeCacheExtension("1.0.0", 'a', 41)
	publication := runtimeCacheTestPublication(t, extension, "runtime-stale")
	caches := cacheregistry.New()
	if _, err := caches.Publish(publication); err != nil {
		t.Fatal(err)
	}
	extension.Status = extensions.StatusDisabled
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: NewManager(ManagerConfig{Starter: newManagerStagedStarter()}), Caches: caches,
	})
	if err := boundary.restoreCachePublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	if _, found := caches.SnapshotPublication(extension.ID); found {
		t.Fatal("startup restore retained a disabled legacy Cache publication")
	}
}

func legacyRuntimeCacheExtension(version string, digest byte, versionID int64) extensions.Extension {
	id := "legacy.cache"
	return extensions.Extension{
		ID: id, Name: "Legacy Cache", Version: version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: strings.Repeat(string(digest), 64), ActiveVersionID: versionID,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Name: "Legacy Cache", Version: version, Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
				Digest: strings.Repeat(string(digest), 64), HostAPIVersion: "sforum.host-api@2",
			},
			Cache: []extensions.ManifestCache{
				{
					ID: id + ".results", ContractVersion: id + ".results@1", Namespace: id + ".results",
					Policy: cacheregistry.PolicyActor, Tags: []string{id + ".tag"}, Provider: "core.cache.redis",
					Invalidators: []string{id + ".invalidate"},
				},
				{
					ID: id + ".metadata", ContractVersion: id + ".metadata@1", Namespace: id + ".metadata",
					Policy: cacheregistry.PolicyPublic, Tags: []string{id + ".metadata.tag"},
					Invalidators: []string{id + ".metadata.invalidate"},
				},
			},
		},
	}
}

func runtimeCacheTestPublication(
	t *testing.T,
	extension extensions.Extension,
	runtimeInstanceID string,
) cacheregistry.Publication {
	t.Helper()
	publication, err := buildLifecycleCachePublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtimeInstanceID,
	})
	if err != nil || publication == nil {
		t.Fatalf("build cache publication = %#v, %v", publication, err)
	}
	return *publication
}
