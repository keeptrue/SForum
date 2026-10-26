package extensionsruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestLifecycleRoutePolicyStartupBindsExactRuntimeAndSafeModeClearsAuthority(t *testing.T) {
	ctx := t.Context()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	routeRegistry := routes.NewRegistry()
	routeSchemas := lifecycleRouteSchemaPublication(t)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routeRegistry, RouteSchemas: routeSchemas,
	})
	extension := lifecycleRegistryRequiredRoutePolicy(t, lifecycleRegistryTestExtension(
		t, "1.0.0", strings.Repeat("7", 64), 701, "/policy-startup",
	))
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyBindingWithPolicy(
		t, routeRegistry, extension, runtime.Identity.InstanceID, "POST", "/policy-startup/api",
		lifecycleRequiredRoutePolicy,
	)

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, true); err != nil {
		t.Fatal(err)
	}
	safe := routeRegistry.PublicationSnapshot().Publication
	if !safe.SafeMode || len(safe.Plugins) != 0 || safe.Policies != nil {
		t.Fatalf("safe-mode route publication = %#v", safe)
	}
	if _, err := routeRegistry.BuildExecutionPlan("POST", "/policy-startup/api"); !errors.Is(err, routes.ErrRouteNotFound) {
		t.Fatalf("safe-mode execution plan error = %v", err)
	}

	// Leaving Safe Mode is a process restart. A fresh boundary must rebuild an
	// authoritative policy set over the retained immutable owners.
	boundary = NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routeRegistry, RouteSchemas: routeSchemas,
	})
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyBindingWithPolicy(
		t, routeRegistry, extension, runtime.Identity.InstanceID, "POST", "/policy-startup/api",
		lifecycleRequiredRoutePolicy,
	)
}

func TestLifecycleRoutePolicyReconcileBindsEnableAndEnableRollback(t *testing.T) {
	ctx := t.Context()
	routeRegistry := routes.NewRegistry()
	routeSchemas := lifecycleRouteSchemaPublication(t)
	empty, err := routes.BindRouteExecutionPolicies(routes.Publication{}, routeSchemas)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routeRegistry.Publish(empty); err != nil {
		t.Fatal(err)
	}
	target := lifecycleRoutePolicyMaterial(
		t, "1.0.0", "8", 801, "/policy-enable", "runtime-policy-enable",
	)
	if _, err := routeSchemas.Publish([]extensionopenapi.Artifact{target.routeSchema}); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Routes: routeRegistry, RouteSchemas: routeSchemas,
	})
	if err := boundary.reconcileRoutes(ctx, target.extension.ID, nil, &target, &target); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyPublication(
		t, routeRegistry, target.extension, target.binding.RuntimeInstanceID, "GET", "/policy-enable/api",
		lifecycleDisabledRoutePolicy,
	)

	if _, err := routeSchemas.Publish(nil); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileRoutes(ctx, target.extension.ID, nil, &target, nil); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyEmpty(t, routeRegistry, false)
}

func TestLifecycleRoutePolicyReconcileRetriesStaleCASAndReplacesExactRuntime(t *testing.T) {
	ctx := t.Context()
	routeRegistry := routes.NewRegistry()
	routeSchemas := lifecycleRouteSchemaPublication(t)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Routes: routeRegistry, RouteSchemas: routeSchemas,
	})
	source := lifecycleRoutePolicyMaterial(
		t, "1.0.0", "9", 901, "/policy-source", "runtime-policy-source",
	)
	target := lifecycleRoutePolicyMaterial(
		t, "2.0.0", "a", 902, "/policy-target", "runtime-policy-target",
	)
	publishLifecycleRoutePolicyMaterial(t, routeSchemas, routeRegistry, source)
	assertLifecycleRoutePolicyBinding(
		t, routeRegistry, source.extension, source.binding.RuntimeInstanceID, "GET", "/policy-source/api",
	)

	if _, err := routeSchemas.Publish([]extensionopenapi.Artifact{target.routeSchema}); err != nil {
		t.Fatal(err)
	}
	stale := routeRegistry.PublicationSnapshot()
	staleCandidate, err := replaceLifecycleRouteSet(
		stale.Publication, target.extension.ID, &target.routes, &source, &target,
	)
	if err != nil {
		t.Fatal(err)
	}
	staleCandidate, err = routes.BindRouteExecutionPolicies(staleCandidate, routeSchemas)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routeRegistry.Publish(stale.Publication); err != nil {
		t.Fatal(err)
	}
	if _, err := routeRegistry.PublishIfRevision(staleCandidate, stale.Revision); !errors.Is(err, routes.ErrRevisionConflict) {
		t.Fatalf("stale route policy CAS error = %v", err)
	}
	assertLifecycleRoutePolicyBinding(
		t, routeRegistry, source.extension, source.binding.RuntimeInstanceID, "GET", "/policy-source/api",
	)

	if err := boundary.reconcileRoutes(ctx, target.extension.ID, &source, &target, &target); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyPublication(
		t, routeRegistry, target.extension, target.binding.RuntimeInstanceID, "GET", "/policy-target/api",
		lifecycleDisabledRoutePolicy,
	)

	if _, err := routeSchemas.Publish([]extensionopenapi.Artifact{source.routeSchema}); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileRoutes(ctx, source.extension.ID, &source, &target, &source); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyPublication(
		t, routeRegistry, source.extension, source.binding.RuntimeInstanceID, "GET", "/policy-source/api",
		lifecycleDisabledRoutePolicy,
	)

	for _, operation := range []string{"disable", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			if _, err := routeSchemas.Publish(nil); err != nil {
				t.Fatal(err)
			}
			if err := boundary.reconcileRoutes(ctx, source.extension.ID, &source, &target, nil); err != nil {
				t.Fatal(err)
			}
			assertLifecycleRoutePolicyEmpty(t, routeRegistry, false)
			if operation == "disable" {
				if _, err := routeSchemas.Publish([]extensionopenapi.Artifact{source.routeSchema}); err != nil {
					t.Fatal(err)
				}
				if err := boundary.reconcileRoutes(ctx, source.extension.ID, &source, &target, &source); err != nil {
					t.Fatal(err)
				}
				assertLifecycleRoutePolicyPublication(
					t, routeRegistry, source.extension, source.binding.RuntimeInstanceID, "GET", "/policy-source/api",
					lifecycleDisabledRoutePolicy,
				)
			}
		})
	}
}

func TestLifecycleRoutePolicyUpgradeAndRollbackPublishExactRuntimePolicies(t *testing.T) {
	ctx := t.Context()
	repository := &memoryLifecycleRegistryRepository{phase: LifecycleRegistryPublicationSource}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	pageRegistry := pages.NewRegistry(nil)
	routeRegistry := routes.NewRegistry()
	routeSchemas := lifecycleRouteSchemaPublication(t)
	serviceRegistry := hostapi.NewServiceRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Repository: repository, Manager: manager, Pages: pageRegistry,
		Routes: routeRegistry, RouteSchemas: routeSchemas, Services: serviceRegistry,
	})

	source := lifecycleRegistryTestExtension(
		t, "1.0.0", strings.Repeat("b", 64), 1001, "/policy-upgrade-source",
	)
	if err := manager.Start(ctx, source); err != nil {
		t.Fatal(err)
	}
	sourceRuntime, err := manager.ActiveRuntimeInstance(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceBinding := lifecycleRegistryBinding(source, sourceRuntime.Identity.InstanceID)
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{source}, false); err != nil {
		t.Fatal(err)
	}
	if err := serviceRegistry.ReplaceExtension(source.ID, []hostapi.ServiceRegistration{
		lifecycleServiceRegistration(source, sourceBinding.RuntimeInstanceID),
	}); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyBinding(
		t, routeRegistry, source, sourceBinding.RuntimeInstanceID, "GET", "/policy-upgrade-source/api",
	)

	target := lifecycleRegistryTestExtension(
		t, "2.0.0", strings.Repeat("c", 64), 1002, "/policy-upgrade-target",
	)
	target = lifecycleRegistryRequiredRoutePolicy(t, target)
	targetRuntime, err := manager.StageRuntimeInstance(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(ctx, targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	targetBinding := lifecycleRegistryBinding(target, targetRuntime.Identity.InstanceID)
	transaction, err := boundary.PrepareLifecycleRegistryPublication(
		ctx,
		lifecycleRegistryRequest(source, target, sourceBinding, targetBinding, 1),
		LifecycleBoundaryActivate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(sourceRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.WaitDrain(ctx, sourceRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishDrainedRuntimeInstance(ctx, targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	if err := serviceRegistry.ReplaceExtension(target.ID, []hostapi.ServiceRegistration{
		lifecycleServiceRegistration(target, targetBinding.RuntimeInstanceID),
	}); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyPublication(
		t, routeRegistry, target, targetBinding.RuntimeInstanceID, "POST", "/policy-upgrade-target/api",
		lifecycleRequiredRoutePolicy,
	)

	if err := transaction.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishDrainedRuntimeInstance(ctx, sourceRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResumeRuntimeInstance(sourceRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyBinding(
		t, routeRegistry, source, sourceBinding.RuntimeInstanceID, "GET", "/policy-upgrade-source/api",
	)
}

func TestLifecycleRoutePolicyEnableAndRollbackPublishAuthoritativeStates(t *testing.T) {
	ctx := t.Context()
	repository := &memoryLifecycleRegistryRepository{phase: LifecycleRegistryPublicationSource}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	routeRegistry := routes.NewRegistry()
	routeSchemas := lifecycleRouteSchemaPublication(t)
	serviceRegistry := hostapi.NewServiceRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Repository: repository, Manager: manager, Pages: pages.NewRegistry(nil),
		Routes: routeRegistry, RouteSchemas: routeSchemas, Services: serviceRegistry,
	})
	target := lifecycleRegistryTestExtension(
		t, "1.0.0", strings.Repeat("d", 64), 1101, "/policy-enable-transaction",
	)
	targetRuntime, err := manager.StageRuntimeInstance(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(ctx, targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	targetBinding := lifecycleRegistryBinding(target, targetRuntime.Identity.InstanceID)
	request := LifecycleBoundaryRequest{
		OperationID: 1101, Operation: extensions.LifecycleMachineEnable, Position: 4,
		StepID: "lifecycle.enable.04.policy.bound", Attempt: 1,
		TargetExtension: target, TargetBinding: targetBinding, ActorUserID: 42, AuditEventID: 1102,
	}
	transaction, err := boundary.PrepareLifecycleRegistryPublication(ctx, request, LifecycleBoundaryActivate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishDrainedRuntimeInstance(ctx, targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	if err := serviceRegistry.ReplaceExtension(target.ID, []hostapi.ServiceRegistration{
		lifecycleServiceRegistration(target, targetBinding.RuntimeInstanceID),
	}); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyPublication(
		t, routeRegistry, target, targetBinding.RuntimeInstanceID, "GET", "/policy-enable-transaction/api",
		lifecycleDisabledRoutePolicy,
	)
	if err := transaction.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRoutePolicyEmpty(t, routeRegistry, false)
}

func TestLifecycleRoutePolicyDisableAndUninstallPublishAuthoritativeEmpty(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		stepID    string
	}{
		{name: "disable", operation: extensions.LifecycleMachineDisable, stepID: "lifecycle.disable.03.policy.empty"},
		{name: "uninstall", operation: extensions.LifecycleMachineUninstall, stepID: "lifecycle.uninstall.03.policy.empty"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			repository := &memoryLifecycleRegistryRepository{phase: LifecycleRegistryPublicationSource}
			manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
			routeRegistry := routes.NewRegistry()
			routeSchemas := lifecycleRouteSchemaPublication(t)
			serviceRegistry := hostapi.NewServiceRegistry()
			boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
				Repository: repository, Manager: manager, Pages: pages.NewRegistry(nil),
				Routes: routeRegistry, RouteSchemas: routeSchemas, Services: serviceRegistry,
			})
			source := lifecycleRegistryTestExtension(
				t, "1.0.0", strings.Repeat(string(rune('e'+index)), 64), int64(1201+index),
				"/policy-"+test.name,
			)
			if err := manager.Start(ctx, source); err != nil {
				t.Fatal(err)
			}
			runtime, err := manager.ActiveRuntimeInstance(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			binding := lifecycleRegistryBinding(source, runtime.Identity.InstanceID)
			if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{source}, false); err != nil {
				t.Fatal(err)
			}
			if err := serviceRegistry.ReplaceExtension(source.ID, []hostapi.ServiceRegistration{
				lifecycleServiceRegistration(source, binding.RuntimeInstanceID),
			}); err != nil {
				t.Fatal(err)
			}
			assertLifecycleRoutePolicyBinding(
				t, routeRegistry, source, binding.RuntimeInstanceID, "GET", "/policy-"+test.name+"/api",
			)
			request := LifecycleBoundaryRequest{
				OperationID: int64(1201 + index), Operation: test.operation, Position: 3,
				StepID: test.stepID, Attempt: 1,
				SourceExtension: &source, TargetExtension: source,
				SourceBinding: binding, TargetBinding: binding, ActorUserID: 42, AuditEventID: int64(1211 + index),
			}
			transaction, err := boundary.PrepareLifecycleRegistryPublication(ctx, request, LifecycleBoundaryDeactivate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.BeginDrain(runtime.Identity); err != nil {
				t.Fatal(err)
			}
			if err := manager.WaitDrain(ctx, runtime.Identity); err != nil {
				t.Fatal(err)
			}
			if err := transaction.Publish(ctx); err != nil {
				t.Fatal(err)
			}
			assertLifecycleRoutePolicyEmpty(t, routeRegistry, false)
			if err := transaction.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			assertLifecycleRoutePolicyPublication(
				t, routeRegistry, source, binding.RuntimeInstanceID, "GET", "/policy-"+test.name+"/api",
				lifecycleDisabledRoutePolicy,
			)
		})
	}
}

func assertLifecycleRoutePolicyBinding(
	t *testing.T,
	registry *routes.Registry,
	extension extensions.Extension,
	runtimeInstanceID string,
	method string,
	path string,
) {
	t.Helper()
	assertLifecycleRoutePolicyBindingWithPolicy(
		t, registry, extension, runtimeInstanceID, method, path, lifecycleDisabledRoutePolicy,
	)
}

func assertLifecycleRoutePolicyBindingWithPolicy(
	t *testing.T,
	registry *routes.Registry,
	extension extensions.Extension,
	runtimeInstanceID string,
	method string,
	path string,
	expectedPolicy routes.RouteExecutionPolicy,
) {
	t.Helper()
	binding := assertLifecycleRoutePolicyPublication(
		t, registry, extension, runtimeInstanceID, method, path, expectedPolicy,
	)
	snapshot := registry.PublicationSnapshot()
	plan, err := registry.BuildExecutionPlan(method, path)
	if err != nil {
		t.Fatal(err)
	}
	policy, bound := plan.ExecutionPolicy()
	if plan.Revision() != snapshot.Revision || !bound || policy != binding.Policy ||
		plan.Terminal().Provider.Artifact != binding.Artifact {
		t.Fatalf(
			"execution plan revision=%d policy=%#v bound=%t terminal=%#v snapshot=%d binding=%#v",
			plan.Revision(), policy, bound, plan.Terminal(), snapshot.Revision, binding,
		)
	}
}

func assertLifecycleRoutePolicyPublication(
	t *testing.T,
	registry *routes.Registry,
	extension extensions.Extension,
	runtimeInstanceID string,
	method string,
	path string,
	expectedPolicy routes.RouteExecutionPolicy,
) routes.RoutePolicyBinding {
	t.Helper()
	publication := registry.PublicationSnapshot().Publication
	if publication.SafeMode || publication.Policies == nil || len(publication.Policies) != 1 {
		t.Fatalf("route policy publication = %#v", publication)
	}
	binding := publication.Policies[0]
	route := extension.Manifest.Routes[0]
	if binding.Artifact.ExtensionID != extension.ID ||
		binding.Artifact.ExtensionVersion != extension.Version ||
		binding.Artifact.PackageDigest != extension.PackageDigest ||
		binding.Artifact.RuntimeInstanceID != runtimeInstanceID ||
		binding.RouteID != route.ID || binding.ContractVersion != route.ContractVersion ||
		binding.Method != method || binding.Policy != expectedPolicy {
		t.Fatalf("route policy binding = %#v", binding)
	}

	detached := routes.NewRegistry()
	if _, err := detached.Publish(publication); err != nil {
		t.Fatalf("publish detached route policy snapshot: %v", err)
	}
	plan, err := detached.BuildExecutionPlan(method, path)
	if err != nil {
		t.Fatal(err)
	}
	policy, bound := plan.ExecutionPolicy()
	if !bound || policy != binding.Policy || plan.Terminal().Provider.Artifact != binding.Artifact {
		t.Fatalf("detached execution plan policy=%#v bound=%t terminal=%#v binding=%#v", policy, bound, plan.Terminal(), binding)
	}
	return binding
}

var (
	// Host OpenAPI route policies always attach default request-size + CORS.
	lifecycleDisabledRoutePolicy = routes.RouteExecutionPolicy{
		RateLimit: "disabled", Idempotency: "disabled",
		RequestSizeBytes: 1 << 20, CORSPolicy: "host.cors.same_origin@1",
	}
	lifecycleRequiredRoutePolicy = routes.RouteExecutionPolicy{
		RateLimit: "host.ip_write@1", Idempotency: "required.24h@1", IdempotencyRequired: true,
		RequestSizeBytes: 1 << 20, CORSPolicy: "host.cors.same_origin@1",
	}
)

func lifecycleRegistryRequiredRoutePolicy(
	t *testing.T,
	extension extensions.Extension,
) extensions.Extension {
	t.Helper()
	if len(extension.Manifest.Routes) != 1 || len(extension.Manifest.OpenAPI) != 1 {
		t.Fatalf("required route policy fixture shape = %#v", extension.Manifest)
	}
	route := &extension.Manifest.Routes[0]
	route.Methods = []string{"POST"}
	route.RequestSchema = "registry.demo.read.request@1"
	openAPIBody := []byte(fmt.Sprintf(`openapi: 3.1.0
info:
  title: Registry demo required replay
  version: %s
paths:
  %s:
    post:
      operationId: registry.demo.api.read
      x-sforum-route-id: %s
      x-sforum-contract-version: %s
      x-sforum-guard: core.guard.public
      x-sforum-request-schema: registry.demo.read.request@1
      x-sforum-response-schema: registry.demo.read.response@1
      x-sforum-rate-limit: host.ip_write@1
      x-sforum-idempotency: required.24h@1
      parameters:
        - name: Idempotency-Key
          in: header
          required: true
          schema:
            type: string
            maxLength: 128
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
`, extension.Version, route.Path, route.ID, route.ContractVersion))
	digest := sha256.Sum256(openAPIBody)
	digestValue := hex.EncodeToString(digest[:])
	extension.Manifest.OpenAPI[0].Digest = digestValue
	for index := range extension.Manifest.PackageFiles {
		file := &extension.Manifest.PackageFiles[index]
		if file.Path == extension.Manifest.OpenAPI[0].Path {
			file.Digest = digestValue
		}
	}
	if err := os.WriteFile(
		filepath.Join(extension.PackagePath, filepath.FromSlash(extension.Manifest.OpenAPI[0].Path)),
		openAPIBody,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	manifestBody, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(extension.PackagePath, extensionmanifest.ManifestFileName), manifestBody, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest, err = extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	return extension
}

func assertLifecycleRoutePolicyEmpty(t *testing.T, registry *routes.Registry, safeMode bool) {
	t.Helper()
	publication := registry.PublicationSnapshot().Publication
	if publication.SafeMode != safeMode || len(publication.Plugins) != 0 || len(publication.Policies) != 0 {
		t.Fatalf("empty route policy publication = %#v", publication)
	}
	if safeMode && publication.Policies != nil || !safeMode && publication.Policies == nil {
		t.Fatalf("empty route policy authority semantics = %#v", publication.Policies)
	}
}

func lifecycleRoutePolicyMaterial(
	t *testing.T,
	version string,
	digestSeed string,
	versionID int64,
	path string,
	runtimeInstanceID string,
) lifecycleRegistryMaterial {
	t.Helper()
	extension := lifecycleRegistryTestExtension(
		t, version, strings.Repeat(digestSeed, 64), versionID, path,
	)
	material, err := buildLifecycleRegistryMaterial(
		extension, lifecycleRegistryBinding(extension, runtimeInstanceID),
	)
	if err != nil {
		t.Fatal(err)
	}
	return material
}

func publishLifecycleRoutePolicyMaterial(
	t *testing.T,
	schemas *extensionopenapi.RouteSchemaPublication,
	registry *routes.Registry,
	material lifecycleRegistryMaterial,
) {
	t.Helper()
	if _, err := schemas.Publish([]extensionopenapi.Artifact{material.routeSchema}); err != nil {
		t.Fatal(err)
	}
	publication := registry.PublicationSnapshot().Publication
	publication.SafeMode = false
	publication.Plugins = []routes.PluginRouteSet{material.routes}
	bound, err := routes.BindRouteExecutionPolicies(publication, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Publish(bound); err != nil {
		t.Fatal(err)
	}
}
