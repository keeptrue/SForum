package extensionsruntime

import (
	"errors"
	"testing"

	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

type conflictingLifecycleRoutePublication struct {
	registry *routes.Registry
	calls    int
}

func (p *conflictingLifecycleRoutePublication) PublicationSnapshot() routes.PublicationSnapshot {
	return p.registry.PublicationSnapshot()
}

func (p *conflictingLifecycleRoutePublication) PublishIfRevision(
	routes.Publication,
	uint64,
) (routes.Snapshot, error) {
	p.calls++
	return p.registry.Snapshot(), routes.ErrRevisionConflict
}

func TestLifecycleStartupRestoresSchemasWhenRouteCASNeverPublishes(t *testing.T) {
	schemas := lifecycleRouteSchemaPublication(t)
	registry := routes.NewRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Routes: registry, RouteSchemas: schemas,
	})
	source := lifecycleRoutePolicyMaterial(
		t, "1.0.0", "d", 1301, "/policy-rollback-source", "runtime-policy-rollback-source",
	)
	target := lifecycleRoutePolicyMaterial(
		t, "2.0.0", "e", 1302, "/policy-rollback-target", "runtime-policy-rollback-target",
	)
	publishLifecycleRoutePolicyMaterial(t, schemas, registry, source)
	beforeRoutes := registry.PublicationSnapshot()
	beforeSchemas := schemas.PublicationSnapshot()
	conflicts := &conflictingLifecycleRoutePublication{registry: registry}
	boundary.routePublisher = conflicts

	err := boundary.restoreRoutePolicyPublications(
		t.Context(), []routes.PluginRouteSet{target.routes}, []extensionopenapi.Artifact{target.routeSchema}, false,
	)
	if !errors.Is(err, routes.ErrRevisionConflict) {
		t.Fatalf("startup route publication error = %v", err)
	}
	if conflicts.calls != 16 {
		t.Fatalf("route CAS attempts = %d, want 16", conflicts.calls)
	}
	afterRoutes := registry.PublicationSnapshot()
	if afterRoutes.Revision != beforeRoutes.Revision {
		t.Fatalf("route revision advanced from %d to %d", beforeRoutes.Revision, afterRoutes.Revision)
	}
	assertLifecycleRoutePolicyBinding(
		t, registry, source.extension, source.binding.RuntimeInstanceID, "GET", "/policy-rollback-source/api",
	)
	afterSchemas := schemas.PublicationSnapshot()
	if afterSchemas.Revision != beforeSchemas.Revision+2 {
		t.Fatalf("compensated schema revision = %d, want %d", afterSchemas.Revision, beforeSchemas.Revision+2)
	}
	if len(afterSchemas.Artifacts) != 1 ||
		afterSchemas.Artifacts[0].ExtensionID != source.extension.ID ||
		afterSchemas.Artifacts[0].ExtensionVersion != source.extension.Version ||
		afterSchemas.Artifacts[0].PackageDigest != source.extension.PackageDigest {
		t.Fatalf("compensated schema artifacts = %#v", afterSchemas.Artifacts)
	}
}

func TestLifecycleReconcileRestoresSchemasWhenRouteCASNeverPublishes(t *testing.T) {
	schemas := lifecycleRouteSchemaPublication(t)
	registry := routes.NewRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Routes: registry, RouteSchemas: schemas,
	})
	source := lifecycleRoutePolicyMaterial(
		t, "1.0.0", "f", 1351, "/policy-reconcile-source", "runtime-policy-reconcile-source",
	)
	target := lifecycleRoutePolicyMaterial(
		t, "2.0.0", "a", 1352, "/policy-reconcile-target", "runtime-policy-reconcile-target",
	)
	publishLifecycleRoutePolicyMaterial(t, schemas, registry, source)
	beforeRoutes := registry.PublicationSnapshot()
	beforeSchemas := schemas.PublicationSnapshot()
	conflicts := &conflictingLifecycleRoutePublication{registry: registry}
	boundary.routePublisher = conflicts

	err := boundary.reconcileRoutePolicyPublications(
		t.Context(), target.extension.ID, &source, &target, &target,
	)
	if !errors.Is(err, routes.ErrRevisionConflict) {
		t.Fatalf("reconcile route publication error = %v", err)
	}
	if conflicts.calls != 16 {
		t.Fatalf("route CAS attempts = %d, want 16", conflicts.calls)
	}
	afterRoutes := registry.PublicationSnapshot()
	if afterRoutes.Revision != beforeRoutes.Revision {
		t.Fatalf("route revision advanced from %d to %d", beforeRoutes.Revision, afterRoutes.Revision)
	}
	assertLifecycleRoutePolicyBinding(
		t, registry, source.extension, source.binding.RuntimeInstanceID, "GET", "/policy-reconcile-source/api",
	)
	afterSchemas := schemas.PublicationSnapshot()
	if afterSchemas.Revision != beforeSchemas.Revision+2 {
		t.Fatalf("compensated schema revision = %d, want %d", afterSchemas.Revision, beforeSchemas.Revision+2)
	}
	if len(afterSchemas.Artifacts) != 1 ||
		afterSchemas.Artifacts[0].ExtensionID != source.extension.ID ||
		afterSchemas.Artifacts[0].ExtensionVersion != source.extension.Version ||
		afterSchemas.Artifacts[0].PackageDigest != source.extension.PackageDigest {
		t.Fatalf("compensated schema artifacts = %#v", afterSchemas.Artifacts)
	}
}

func TestLifecycleRouteSchemaCompensationDoesNotOverwriteNewerWriter(t *testing.T) {
	schemas := lifecycleRouteSchemaPublication(t)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Routes: routes.NewRegistry(), RouteSchemas: schemas,
	})
	source := lifecycleRoutePolicyMaterial(
		t, "1.0.0", "1", 1401, "/policy-fence-source", "runtime-policy-fence-source",
	)
	target := lifecycleRoutePolicyMaterial(
		t, "2.0.0", "2", 1402, "/policy-fence-target", "runtime-policy-fence-target",
	)
	newer := lifecycleRoutePolicyMaterial(
		t, "3.0.0", "3", 1403, "/policy-fence-newer", "runtime-policy-fence-newer",
	)
	before, err := schemas.Publish([]extensionopenapi.Artifact{source.routeSchema})
	if err != nil {
		t.Fatal(err)
	}
	published, err := schemas.Publish([]extensionopenapi.Artifact{target.routeSchema})
	if err != nil {
		t.Fatal(err)
	}
	newest, err := schemas.Publish([]extensionopenapi.Artifact{newer.routeSchema})
	if err != nil {
		t.Fatal(err)
	}

	want := errors.New("route publication failed")
	got := boundary.restoreRouteSchemasAfterRouteFailure(lifecycleRouteSchemaChange{
		before: before, published: published, changed: true,
	}, want)
	if !errors.Is(got, want) || !errors.Is(got, extensionopenapi.ErrRouteSchemaRevisionConflict) {
		t.Fatalf("fenced compensation error = %v", got)
	}
	after := schemas.PublicationSnapshot()
	if after.Revision != newest.Revision || len(after.Artifacts) != 1 ||
		after.Artifacts[0].ExtensionVersion != newer.extension.Version ||
		after.Artifacts[0].PackageDigest != newer.extension.PackageDigest {
		t.Fatalf("newer schema writer was overwritten: %#v", after)
	}
}
