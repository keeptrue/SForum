package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	"github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestLifecycleQueryPublicationUpgradeRollbackDisableAndStaleCAS(t *testing.T) {
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Queries: queries})
	source := lifecycleQueryTestExtension(t, "1.0.0", strings.Repeat("a", 64), 91)
	target := lifecycleQueryTestExtension(t, "2.0.0", strings.Repeat("b", 64), 92)
	sourceMaterial := lifecycleQueryTestMaterial(t, source, "query-source-runtime")
	targetMaterial := lifecycleQueryTestMaterial(t, target, "query-target-runtime")

	if _, err := queries.Publish(*sourceMaterial.queryPublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.validateQueryTransition(&sourceMaterial, &targetMaterial); err != nil {
		t.Fatalf("validate query transition: %v", err)
	}
	if err := boundary.reconcileQueries(source.ID, &sourceMaterial, &targetMaterial, &targetMaterial); err != nil {
		t.Fatalf("publish target query: %v", err)
	}
	assertLifecycleQueryArtifact(t, queries, targetMaterial.queryPublication.Artifact)

	if err := boundary.reconcileQueries(source.ID, &sourceMaterial, &targetMaterial, &sourceMaterial); err != nil {
		t.Fatalf("restore source query: %v", err)
	}
	assertLifecycleQueryArtifact(t, queries, sourceMaterial.queryPublication.Artifact)

	if err := boundary.reconcileQueries(source.ID, &sourceMaterial, nil, nil); err != nil {
		t.Fatalf("disable query publication: %v", err)
	}
	if _, found := queries.SnapshotPublication(source.ID); found {
		t.Fatal("disabled query publication remains inspectable as active")
	}

	// A newer exact runtime must win over a stale shutdown/rollback attempt.
	if _, err := queries.Publish(*targetMaterial.queryPublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileQueries(source.ID, &sourceMaterial, nil, nil); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale source removed replacement query publication: %v", err)
	}
	assertLifecycleQueryArtifact(t, queries, targetMaterial.queryPublication.Artifact)
}

func TestLifecycleQueryStartupRestoreSafeModeInspectionAndClosedPlanning(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	queries := queryregistry.New()
	core := lifecycleCoreQueryPublication(t)
	if _, err := queries.Publish(core); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routes.NewRegistry(), RouteSchemas: lifecycleRouteSchemaPublication(t),
		Queries: queries,
	})
	extension := lifecycleQueryTestExtension(t, "1.0.0", strings.Repeat("c", 64), 93)
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("restore query publication: %v", err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := queryregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	}
	assertLifecycleQueryArtifact(t, queries, expected)
	snapshot := queries.Snapshot()
	if snapshot.SafeMode || snapshot.SchemaVersion != queryregistry.SchemaVersion || snapshot.Digest == "" ||
		len(snapshot.Publications) != 2 || len(snapshot.Queries) != 2 {
		t.Fatalf("restored inspectable query snapshot = %#v", snapshot)
	}
	if _, err := queries.Plan(ctx, queryregistry.PlanRequest{
		QueryID: extension.ID + ".items", Fields: []string{"ID", "title"},
	}); !errors.Is(err, queryregistry.ErrContractInsufficient) {
		t.Fatalf("production planning without Host cost policy = %v", err)
	}

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, true); err != nil {
		t.Fatalf("restore query Safe Mode: %v", err)
	}
	safe := queries.Snapshot()
	if !safe.SafeMode || len(safe.Publications) != 1 || len(safe.Queries) != 1 ||
		safe.Publications[0].Artifact != core.Artifact {
		t.Fatalf("Safe Mode query snapshot = %#v", safe)
	}
}

func TestLifecycleFilterOnlyQueryPublicationBuildRestoreAndSafeMode(t *testing.T) {
	ctx := context.Background()
	extension := legacyRuntimeFilterOnlyExtension("1.0.0", 'c', 43)
	binding := lifecycleRegistryBinding(extension, "filter-build-runtime")
	publication, err := buildLifecycleQueryPublication(extension, binding)
	if err != nil || publication == nil {
		t.Fatalf("build filter-only publication = %#v, %v", publication, err)
	}
	expected := queryregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: binding.RuntimeInstanceID,
	}
	if publication.Artifact != expected || len(publication.Queries) != 0 ||
		len(publication.ResultFilters) != 1 || publication.ResultFilters[0].ID != extension.ID+".items.mask" {
		t.Fatalf("filter-only publication = %#v, expected artifact=%#v", publication, expected)
	}
	empty := extension
	empty.Manifest.QueryResultFilters = nil
	if publication, err := buildLifecycleQueryPublication(empty, binding); err != nil || publication != nil {
		t.Fatalf("empty Query surface publication = %#v, %v", publication, err)
	}

	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routes.NewRegistry(), RouteSchemas: lifecycleRouteSchemaPublication(t),
		Queries: queries,
	})
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("restore filter-only publication: %v", err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	restored, found := queries.SnapshotPublication(extension.ID)
	if !found || restored.Artifact.RuntimeInstanceID != runtime.Identity.InstanceID ||
		len(restored.Queries) != 0 || len(restored.ResultFilters) != 1 {
		t.Fatalf("restored filter-only publication = %#v, found=%t", restored, found)
	}
	snapshot := queries.Snapshot()
	if snapshot.SafeMode || len(snapshot.Publications) != 1 || len(snapshot.Queries) != 0 {
		t.Fatalf("restored filter-only graph = %#v", snapshot)
	}

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, true); err != nil {
		t.Fatalf("restore filter-only Safe Mode: %v", err)
	}
	safe := queries.Snapshot()
	if !safe.SafeMode || len(safe.Publications) != 0 || len(safe.Queries) != 0 {
		t.Fatalf("filter-only Safe Mode graph = %#v", safe)
	}
}

func TestLifecycleQueryMaterialDigestFreezesFamilyAndLegacyAliases(t *testing.T) {
	extension := lifecycleQueryTestExtension(t, "1.0.0", strings.Repeat("e", 64), 95)
	material := lifecycleQueryTestMaterial(t, extension, "query-digest-runtime")
	v1Digest, err := encodeLifecycleRegistryMaterialDigest(&material, false, false)
	if err != nil {
		t.Fatal(err)
	}
	v3Digest, err := encodeLifecycleRegistryMaterialDigest(&material, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if material.digest != v3Digest || material.digest == v1Digest || material.legacyDigest != v1Digest ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest}) {
		t.Fatalf("query digest material = %#v", material)
	}

	before := material.digest
	material.queryPublication.Queries[0].CacheTags = []string{"changed.query.tag"}
	if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
		t.Fatal(err)
	}
	if material.digest == before {
		t.Fatal("query declaration drift did not change durable registry-plan digest")
	}

	material.assetPublication = &assetregistry.Publication{Artifact: assetregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
		ImpactDigest: strings.Repeat("f", 64), OwnerKind: assetregistry.OwnerKindPlugin,
	}}
	material.assetAdmitted = true
	v2Digest, err := encodeLifecycleRegistryMaterialDigest(&material, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest, v2Digest}) {
		t.Fatalf("asset+query compatibility aliases = %v", registryMaterialCompatibleDigests(&material))
	}
	record := lifecycleRegistryPublicationRecord{
		Fence: lifecyclePublicationFence{
			OperationID: 95, Operation: extensions.LifecycleMachineUpgrade, StepID: "registry",
			Position: 1, Mode: LifecycleBoundaryActivate, Attempt: 1,
		},
		SourceDigest: v1Digest, TargetDigest: v2Digest,
	}
	resume := PrepareLifecycleRegistryPublicationInput{
		Fence: record.Fence, SourceDigest: material.digest, TargetDigest: material.digest,
		CompatibleSourceDigests: registryMaterialCompatibleDigests(&material),
		CompatibleTargetDigests: registryMaterialCompatibleDigests(&material),
	}
	if !record.matchesInput(resume) || !validLifecycleRegistryPrepareInput(resume) {
		t.Fatal("query-bearing @3 material did not explicitly resume exact @1/@2 in-flight digests")
	}
}

func TestLifecycleQueryStartupSkipsUnavailableRuntime(t *testing.T) {
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: NewManager(ManagerConfig{Starter: newManagerStagedStarter()}),
		Routes:  routes.NewRegistry(), RouteSchemas: lifecycleRouteSchemaPublication(t), Queries: queries,
	})
	extension := lifecycleQueryTestExtension(t, "1.0.0", strings.Repeat("d", 64), 94)
	if err := boundary.RestoreRoutePublications(context.Background(), []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	if snapshot := queries.Snapshot(); len(snapshot.Publications) != 0 || len(snapshot.Queries) != 0 {
		t.Fatalf("unavailable runtime query publication = %#v", snapshot)
	}
}

func TestLifecycleQueryStartupRejectsMismatchedRuntime(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routes.NewRegistry(), RouteSchemas: lifecycleRouteSchemaPublication(t),
		Queries: queryregistry.New(),
	})
	running := lifecycleQueryTestExtension(t, "1.0.0", strings.Repeat("6", 64), 96)
	desired := lifecycleQueryTestExtension(t, "2.0.0", strings.Repeat("7", 64), 97)
	if err := manager.Start(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{desired}, false); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("mismatched startup query runtime = %v", err)
	}
}

func lifecycleQueryTestExtension(t *testing.T, version, seed string, versionID int64) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, seed, versionID, "/query-"+version)
	extension.Manifest.Queries = []extensions.ManifestQuery{{
		ID: extension.ID + ".items", ContractVersion: extension.ID + ".items@1",
		Entity: extension.ID + ".item", PlanVersion: extension.ID + ".items.plan@1",
		Fields: []string{"ID", "title"}, Relations: []string{"owner"},
		Filters: []string{"status"}, Sort: []string{"created_at"}, Pagination: "offset",
		ResultSchema: extension.ID + ".items.result@1", PermissionPolicy: "public",
		CacheTags: []string{extension.ID + ".items"},
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

func lifecycleQueryTestMaterial(
	t *testing.T,
	extension extensions.Extension,
	runtimeInstanceID string,
) lifecycleRegistryMaterial {
	t.Helper()
	material, err := buildLifecycleRegistryMaterial(
		extension,
		lifecycleRegistryBinding(extension, runtimeInstanceID),
	)
	if err != nil {
		t.Fatal(err)
	}
	if material.queryPublication == nil {
		t.Fatal("query declaration did not produce a lifecycle publication")
	}
	return material
}

func assertLifecycleQueryArtifact(
	t *testing.T,
	registry *queryregistry.Registry,
	expected queryregistry.Artifact,
) {
	t.Helper()
	publication, found := registry.SnapshotPublication(expected.ExtensionID)
	if !found || publication.Artifact != expected || len(publication.Queries) != 1 {
		t.Fatalf("query publication = %#v, found=%t, expected=%#v", publication, found, expected)
	}
	resolved, err := registry.Resolve(expected.ExtensionID + ".items")
	if err != nil || resolved.Artifact != expected {
		t.Fatalf("resolved query = %#v, %v", resolved, err)
	}
}

func lifecycleCoreQueryPublication(t *testing.T) queryregistry.Publication {
	t.Helper()
	artifact, err := queryregistry.NewCoreArtifact("core.query", "1.0.0", strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	return queryregistry.Publication{
		Artifact: artifact,
		Queries: []queryregistry.QueryDeclaration{{
			ID: "core.query.health", ContractVersion: "core.query.health@1", Entity: "core.query.health",
			PlanVersion: "core.query.health.plan@1", Fields: []string{"status"}, Pagination: "none",
			ResultSchema: "core.query.health.result@1", PermissionPolicy: "public",
		}},
	}
}
