package extensionsruntime

import (
	"errors"
	"strings"
	"sync"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

func TestLegacyRuntimeQueriesPublishWithoutLifecycleAndRollbackQuarantine(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	if extension.Manifest.Lifecycle != nil {
		t.Fatal("fixture unexpectedly uses Lifecycle V2")
	}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})

	published, err := boundary.PublishRuntimeQueries(t.Context(), extension)
	if err != nil || published == nil {
		t.Fatalf("publish legacy runtime queries = %#v, %v", published, err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	publication, found := queries.SnapshotPublication(extension.ID)
	if !found || publication.Artifact.VersionID != extension.ActiveVersionID ||
		publication.Artifact.RuntimeInstanceID != runtime.Identity.InstanceID || len(publication.Queries) != 1 {
		t.Fatalf("published exact runtime queries = %#v", publication)
	}

	quarantined, err := boundary.QuarantineRuntimeQueries(t.Context(), extension)
	if err != nil || quarantined == nil {
		t.Fatalf("quarantine legacy runtime queries = %#v, %v", quarantined, err)
	}
	if _, found := queries.SnapshotPublication(extension.ID); found || manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatal("quarantine left query publication or runtime admission open")
	}
	if err := quarantined.Rollback(); err != nil {
		t.Fatalf("rollback query quarantine: %v", err)
	}
	publication, found = queries.SnapshotPublication(extension.ID)
	if !found || publication.Artifact.RuntimeInstanceID != runtime.Identity.InstanceID ||
		!manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatalf("rollback did not restore publication-before-admission: %#v", publication)
	}
}

func TestRuntimeQueryAdmissionRejectsVersionIDDrift(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	publication := runtimeQueryTestPublication(t, extension, runtime.Identity.InstanceID)
	publication.Artifact.VersionID++
	if _, err := queries.Publish(publication); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.Plan(t.Context(), queryregistry.PlanRequest{QueryID: publication.Queries[0].ID}); !errors.Is(err, queryregistry.ErrArtifactUnavailable) {
		t.Fatalf("VersionID-drifted plan error=%v", err)
	}
	if boundary.runtimeQueryArtifactAvailable(publication.Artifact) {
		t.Fatal("VersionID-drifted runtime artifact was admitted")
	}
	drifted := extension
	drifted.ActiveVersionID++
	if _, err := boundary.PublishRuntimeQueries(t.Context(), drifted); !errors.Is(err, extensions.ErrRuntimeQueryPublicationUnavailable) {
		t.Fatalf("VersionID-drifted runtime publication error=%v", err)
	}
}

func TestLegacyRuntimeFilterOnlyPublishesQuarantinesAndRollsBack(t *testing.T) {
	extension := legacyRuntimeFilterOnlyExtension("1.0.0", 'c', 43)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	published, err := boundary.PublishRuntimeQueries(t.Context(), extension)
	if err != nil || published == nil {
		t.Fatalf("publish filter-only runtime = %#v, %v", published, err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	publication, found := queries.SnapshotPublication(extension.ID)
	if !found || publication.Artifact.VersionID != extension.ActiveVersionID ||
		publication.Artifact.RuntimeInstanceID != runtime.Identity.InstanceID ||
		len(publication.Queries) != 0 || len(publication.ResultFilters) != 1 {
		t.Fatalf("published exact filter-only runtime = %#v", publication)
	}

	quarantined, err := boundary.QuarantineRuntimeQueries(t.Context(), extension)
	if err != nil || quarantined == nil {
		t.Fatalf("quarantine filter-only runtime = %#v, %v", quarantined, err)
	}
	if _, found := queries.SnapshotPublication(extension.ID); found || manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatal("quarantine left filter-only publication or runtime admission open")
	}
	if err := quarantined.Rollback(); err != nil {
		t.Fatalf("rollback filter-only quarantine: %v", err)
	}
	publication, found = queries.SnapshotPublication(extension.ID)
	if !found || publication.Artifact.RuntimeInstanceID != runtime.Identity.InstanceID ||
		len(publication.ResultFilters) != 1 || !manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatalf("filter-only rollback did not restore publication-before-admission: %#v", publication)
	}
}

func TestRuntimeQuerySurfacesRequireExactProtocolInstance(t *testing.T) {
	for _, test := range []struct {
		name      string
		extension extensions.Extension
	}{
		{name: "query owner", extension: legacyRuntimeQueryExtension("1.0.0", 'a', 41)},
		{name: "filter only", extension: legacyRuntimeFilterOnlyExtension("1.0.0", 'c', 43)},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(ManagerConfig{Starter: fakeStarter{}})
			if err := manager.Start(t.Context(), test.extension); !errors.Is(err, ErrVersionedRuntimeContractInvalid) {
				t.Fatalf("Query Registry runtime accepted empty instance identity = %v", err)
			}
		})
	}
}

func TestLegacyRuntimeQueryStaleArtifactCannotReplaceOrRemoveNewer(t *testing.T) {
	stale := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	newer := legacyRuntimeQueryExtension("2.0.0", 'b', 42)
	newerPublication := runtimeQueryTestPublication(t, newer, "runtime-newer")
	queries := queryregistry.New()
	if _, err := queries.Publish(newerPublication); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	runtime, _ := manager.ActiveRuntimeInstance(stale.ID)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})

	if _, err := boundary.PublishRuntimeQueries(t.Context(), stale); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale publish replaced newer query artifact = %v", err)
	}
	if _, err := boundary.QuarantineRuntimeQueries(t.Context(), stale); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale disable removed newer query artifact = %v", err)
	}
	active, found := queries.SnapshotPublication(stale.ID)
	if !found || active.Artifact != newerPublication.Artifact || !manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatalf("stale operation changed newer artifact/admission: %#v", active)
	}
}

func TestLegacyRuntimeQueryDisableRejectsStaleRuntimeInstanceBeforeDrain(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	stale := runtimeQueryTestPublication(t, extension, "runtime-stale")
	if _, err := queries.Publish(stale); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})

	if _, err := boundary.QuarantineRuntimeQueries(t.Context(), extension); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale runtime instance quarantine = %v", err)
	}
	active, found := queries.SnapshotPublication(extension.ID)
	if !found || active.Artifact != stale.Artifact || !manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatalf("stale runtime instance changed publication/admission: %#v", active)
	}
}

func TestLegacyRuntimeQueryPublishRollbackCannotDeleteConcurrentReplacement(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	mutation, err := boundary.PublishRuntimeQueries(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	active, _ := queries.SnapshotPublication(extension.ID)
	newer := legacyRuntimeQueryExtension("2.0.0", 'b', 42)
	replacement := runtimeQueryTestPublication(t, newer, "runtime-newer")
	if _, err := queries.PublishIfArtifact(active.Artifact, replacement); err != nil {
		t.Fatal(err)
	}

	if err := mutation.Rollback(); !errors.Is(err, queryregistry.ErrArtifactConflict) {
		t.Fatalf("stale rollback removed concurrent replacement = %v", err)
	}
	current, found := queries.SnapshotPublication(extension.ID)
	if !found || current.Artifact != replacement.Artifact {
		t.Fatalf("concurrent replacement changed by stale rollback = %#v", current)
	}
}

func TestLegacyRuntimeQueryIdempotentPublishRollbackClosesPublication(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	replayed, err := boundary.PublishRuntimeQueries(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayed.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, found := queries.SnapshotPublication(extension.ID); found {
		t.Fatal("failed enable compensation retained idempotently replayed query publication")
	}
}

func TestLegacyRuntimeQueryMutationRollbackIsRaceSafeAndIdempotent(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	mutation, err := boundary.PublishRuntimeQueries(t.Context(), extension)
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
	if _, found := queries.SnapshotPublication(extension.ID); found {
		t.Fatal("concurrent rollback left publication active")
	}
}

func TestLegacyRuntimeQueryPublishFailsClosedInSafeMode(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	if _, err := queries.ReplaceAll(nil, true); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); !errors.Is(err, queryregistry.ErrSafeMode) {
		t.Fatalf("Safe Mode query publication = %v", err)
	}
	if snapshot := queries.Snapshot(); !snapshot.SafeMode || len(snapshot.Publications) != 0 {
		t.Fatalf("Safe Mode query snapshot = %#v", snapshot)
	}
}

func TestQueryStartupRestoreRemovesDisabledLegacyPublication(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	publication := runtimeQueryTestPublication(t, extension, "runtime-stale")
	queries := queryregistry.New()
	if _, err := queries.Publish(publication); err != nil {
		t.Fatal(err)
	}
	extension.Status = extensions.StatusDisabled
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: NewManager(ManagerConfig{Starter: newManagerStagedStarter()}), Queries: queries,
	})
	if err := boundary.restoreQueryPublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	if _, found := queries.SnapshotPublication(extension.ID); found {
		t.Fatal("startup restore retained a disabled legacy Query publication")
	}
}

func legacyRuntimeQueryExtension(version string, digest byte, versionID int64) extensions.Extension {
	id := "legacy.query"
	return extensions.Extension{
		ID: id, Name: "Legacy Query", Version: version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: strings.Repeat(string(digest), 64), ActiveVersionID: versionID,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Name: "Legacy Query", Version: version, Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
				Digest: strings.Repeat(string(digest), 64), HostAPIVersion: "sforum.host-api@2",
			},
			Queries: []extensions.ManifestQuery{{
				ID: id + ".items", ContractVersion: id + ".items@1", Entity: id + ".item",
				PlanVersion: id + ".items.plan@1", Fields: []string{"id"}, Pagination: "none",
				ResultSchema: id + ".items.result@1", PermissionPolicy: "public",
			}},
		},
	}
}

func legacyRuntimeFilterOnlyExtension(version string, digest byte, versionID int64) extensions.Extension {
	extension := legacyRuntimeQueryExtension(version, digest, versionID)
	extension.Manifest.Queries = nil
	extension.Manifest.QueryResultFilters = []extensions.ManifestQueryResultFilter{{
		ID: extension.ID + ".items.mask", ContractVersion: extension.ID + ".items.mask@1",
		QueryID: "owner.query.items", QueryContractVersion: "owner.query.items@1",
		QueryPlanVersion: "owner.query.items.plan@1", Handler: extension.ID + ".items.mask",
		FailurePolicy: "fail_open", TimeoutMS: 500,
		Dependency: &extensions.ManifestQueryResultFilterDependency{
			ExtensionID: "owner.query", VersionConstraint: "^1.0.0",
		},
	}}
	extension.Manifest.Dependencies = []extensions.ManifestDependency{{
		ID: "owner.query", Version: "^1.0.0", Kind: "optional",
	}}
	return extension
}

func runtimeQueryTestPublication(
	t *testing.T,
	extension extensions.Extension,
	runtimeInstanceID string,
) queryregistry.Publication {
	t.Helper()
	publication, err := buildLifecycleQueryPublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtimeInstanceID,
	})
	if err != nil || publication == nil {
		t.Fatalf("build query publication = %#v, %v", publication, err)
	}
	return *publication
}
