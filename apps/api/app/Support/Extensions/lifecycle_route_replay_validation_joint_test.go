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

func TestLifecycleValidationRejectsRequiredReplayCredentialMutationInEitherEnableOrder(t *testing.T) {
	for _, action := range []string{
		extensionmanifest.RouteActionBefore,
		extensionmanifest.RouteActionFilter,
		extensionmanifest.RouteActionWrap,
		extensionmanifest.RouteActionGlobalMiddleware,
	} {
		for _, requiredFirst := range []bool{true, false} {
			name := "mutation_then_required"
			if requiredFirst {
				name = "required_then_mutation"
			}
			t.Run(action+"/"+name, func(t *testing.T) {
				ctx := t.Context()
				manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
				routeRegistry := routes.NewRegistry()
				routeSchemas := jointReplayLifecycleRouteSchemas(t)
				jointReplayLifecyclePublishCore(t, routeRegistry)
				boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
					Repository: &memoryLifecycleRegistryRepository{phase: LifecycleRegistryPublicationSource},
					Manager:    manager, Pages: pages.NewRegistry(nil), Routes: routeRegistry,
					RouteSchemas: routeSchemas, Services: hostapi.NewServiceRegistry(),
				})

				required := jointReplayLifecycleExtension(t, "joint.required", 'a', true, "", "")
				mutation := jointReplayLifecycleExtension(t, "joint.mutation", 'b', false, action, jointReplayCoreRouteID)
				existing, target := mutation, required
				if requiredFirst {
					existing, target = required, mutation
				}
				if err := manager.Start(ctx, existing); err != nil {
					t.Fatal(err)
				}
				if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{existing}, false); err != nil {
					t.Fatalf("publish first extension: %v", err)
				}
				targetRuntime, err := manager.StageRuntimeInstance(ctx, target)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := manager.HealthRuntimeInstance(ctx, targetRuntime.Identity); err != nil {
					t.Fatal(err)
				}

				routeRevision := routeRegistry.PublicationSnapshot().Revision
				schemaRevision := routeSchemas.Revision()
				request := LifecycleBoundaryRequest{
					OperationID: 901, Operation: extensions.LifecycleMachineEnable, Position: 4,
					StepID: "lifecycle.enable.04.registry_prepared", Attempt: 1,
					TargetExtension: target,
					TargetBinding:   lifecycleRegistryBinding(target, targetRuntime.Identity.InstanceID),
				}
				err = boundary.ValidateLifecycleRegistries(ctx, request)
				if !errors.Is(err, routes.ErrRoutePolicyComposition) {
					t.Fatalf("joint lifecycle validation error = %v", err)
				}
				if got := routeRegistry.PublicationSnapshot().Revision; got != routeRevision {
					t.Fatalf("route revision advanced from %d to %d", routeRevision, got)
				}
				if got := routeSchemas.Revision(); got != schemaRevision {
					t.Fatalf("route schema revision advanced from %d to %d", schemaRevision, got)
				}
			})
		}
	}
}

func TestLifecycleStartupRestoreRejectsRequiredReplayCredentialMutationAtomically(t *testing.T) {
	ctx := t.Context()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	routeRegistry := routes.NewRegistry()
	routeSchemas := jointReplayLifecycleRouteSchemas(t)
	jointReplayLifecyclePublishCore(t, routeRegistry)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routeRegistry, RouteSchemas: routeSchemas,
	})
	required := jointReplayLifecycleExtension(t, "startup.required", 'c', true, "", "")
	mutation := jointReplayLifecycleExtension(t, "startup.mutation", 'd', false, extensionmanifest.RouteActionFilter, jointReplayCoreRouteID)
	for _, extension := range []extensions.Extension{required, mutation} {
		if err := manager.Start(ctx, extension); err != nil {
			t.Fatal(err)
		}
	}
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{required}, false); err != nil {
		t.Fatalf("publish baseline startup snapshot: %v", err)
	}
	routeRevision := routeRegistry.PublicationSnapshot().Revision
	schemaRevision := routeSchemas.Revision()

	err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{required, mutation}, false)
	if !errors.Is(err, routes.ErrRoutePolicyComposition) {
		t.Fatalf("startup joint validation error = %v", err)
	}
	if got := routeRegistry.PublicationSnapshot().Revision; got != routeRevision {
		t.Fatalf("route revision advanced from %d to %d", routeRevision, got)
	}
	if got := routeSchemas.Revision(); got != schemaRevision {
		t.Fatalf("route schema revision advanced from %d to %d", schemaRevision, got)
	}
	publication := routeRegistry.PublicationSnapshot().Publication
	if len(publication.Plugins) != 1 || publication.Plugins[0].Artifact.ExtensionID != required.ID {
		t.Fatalf("baseline route publication changed after failed startup restore: %#v", publication.Plugins)
	}
	artifacts := routeSchemas.PublicationSnapshot().Artifacts
	if len(artifacts) != 1 || artifacts[0].ExtensionID != required.ID {
		t.Fatalf("baseline schema publication changed after failed startup restore: %#v", artifacts)
	}
}

func jointReplayLifecycleExtension(
	t *testing.T,
	extensionID string,
	digestByte byte,
	required bool,
	action string,
	targetRouteID string,
) extensions.Extension {
	t.Helper()
	version := "1.0.0"
	extension := exactCoordinatorTestExtension(
		extensionID, version, strings.Repeat(string(digestByte), 64), extensionID+".lifecycle@1", 1,
	)
	extension.Name = extensionID
	extension.Status = extensions.StatusEnabled
	extension.PackagePath = t.TempDir()

	backendBody := []byte("joint-replay-" + extensionID)
	backendDigest := jointReplayLifecycleDigest(backendBody)
	schemaBody := []byte(`{"type":"object","additionalProperties":true}`)
	schemaDigest := jointReplayLifecycleDigest(schemaBody)
	extension.Manifest.Backend = extensions.ManifestBackend{
		Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
		Digest: backendDigest, HostAPIVersion: "sforum.host@2",
	}
	extension.Manifest.PackageFiles = []extensions.ManifestPackageFile{
		{ID: extensionID + ".file.backend", Kind: "executable", Path: "backend/plugin", Digest: backendDigest},
		{ID: extensionID + ".file.schema", Kind: "schema", Path: "openapi/schemas/common.json", Digest: schemaDigest, Version: "1"},
	}
	if required {
		routeID := extensionID + ".execute"
		extension.Manifest.Routes = []extensions.ManifestRoute{{
			ID: routeID, ContractVersion: routeID + "@1", Action: extensionmanifest.RouteActionReplace,
			TargetID: jointReplayCoreRouteID,
			Path:     "/joint-replay", Methods: []string{"POST"}, Guard: extensionmanifest.GuardCorePublic,
			Priority: 10,
			Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.execute",
			RequestSchema: "openapi/schemas/common.json", ResponseSchema: "openapi/schemas/common.json",
		}}
		openAPIBody := []byte(fmt.Sprintf(`openapi: 3.1.0
info:
  title: Joint required replay
  version: %s
paths:
  /joint-replay:
    post:
      operationId: %s.api.execute
      x-sforum-route-id: %s
      x-sforum-contract-version: %s@1
      x-sforum-guard: core.guard.public
      x-sforum-request-schema: openapi/schemas/common.json
      x-sforum-response-schema: openapi/schemas/common.json
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
              $ref: 'schemas/common.json'
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: 'schemas/common.json'
`, version, extensionID, routeID, routeID))
		openAPIDigest := jointReplayLifecycleDigest(openAPIBody)
		extension.Manifest.OpenAPI = []extensions.ManifestOpenAPIFragment{{
			ID: extensionID + ".openapi", ContractVersion: extensionID + ".openapi@1",
			Path: "openapi/routes.yaml", Digest: openAPIDigest, Namespace: extensionID + ".api",
		}}
		extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
			ID: extensionID + ".file.openapi", Kind: "openapi", Path: "openapi/routes.yaml", Digest: openAPIDigest,
		})
		jointReplayLifecycleWriteFile(t, extension.PackagePath, "openapi/routes.yaml", openAPIBody)
	} else {
		routeID := extensionID + "." + action
		// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 不再是路由守卫，
		// 凭证头变更声明只能由包内显式声明的 kind=raw_request 守卫铸造。本用例需要
		// 一份**合法**的凭证头变更声明来触发重放/凭证变更冲突，所以声明自己的守卫。
		guardID := extensionID + ".guard.raw"
		guardBody := []byte("joint-replay-guard-" + extensionID)
		guardDigest := jointReplayLifecycleDigest(guardBody)
		extension.Manifest.Guards = []extensions.ManifestGuard{{
			ID: guardID, ContractVersion: guardID + "@1", Kind: extensionmanifest.GuardKindRawRequest,
			Entry: "backend/guard", Digest: guardDigest,
		}}
		extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
			ID: extensionID + ".file.guard", Kind: "executable", Path: "backend/guard", Digest: guardDigest,
		})
		jointReplayLifecycleWriteFile(t, extension.PackagePath, "backend/guard", guardBody)
		route := extensions.ManifestRoute{
			ID: routeID, ContractVersion: routeID + "@1", Action: action,
			TargetID: targetRouteID, Path: "/joint-replay", Methods: []string{"POST"},
			Guard: guardID, Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
			Handler: "route." + action, RequestSchema: "openapi/schemas/common.json",
			ResponseSchema:       "openapi/schemas/common.json",
			MutableRequestFields: []string{"/headers/authorization"},
		}
		if action == extensionmanifest.RouteActionGlobalMiddleware {
			route.TargetID, route.Path, route.Methods = "", "", nil
		}
		extension.Manifest.Routes = []extensions.ManifestRoute{route}
	}

	jointReplayLifecycleWriteFile(t, extension.PackagePath, "backend/plugin", backendBody)
	jointReplayLifecycleWriteFile(t, extension.PackagePath, "openapi/schemas/common.json", schemaBody)
	jointReplayLifecycleWriteFile(t, extension.PackagePath, "theme.json", []byte(`{"pages":[]}`))
	if err := extensionmanifest.Validate(extension.Manifest); err != nil {
		t.Fatalf("joint replay manifest %s: %v", extensionID, err)
	}
	manifestBody, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	jointReplayLifecycleWriteFile(t, extension.PackagePath, extensionmanifest.ManifestFileName, manifestBody)
	extension.PackageDigest, err = extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	return extension
}

func jointReplayLifecycleWriteFile(t *testing.T, root, name string, body []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func jointReplayLifecycleDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

const jointReplayCoreRouteID = "core.route.joint_replay"

func jointReplayLifecyclePublishCore(t *testing.T, registry *routes.Registry) {
	t.Helper()
	if _, err := registry.Publish(routes.Publication{Core: []routes.CoreRoute{{
		ID: jointReplayCoreRouteID, ContractVersion: "sforum.route.joint_replay@1",
		Method: "POST", Path: "/joint-replay",
		Guard: routes.CoreGuardDescriptor{
			RouteID: jointReplayCoreRouteID, ContractVersion: "sforum.route.joint_replay@1",
			Method: "POST", Kind: routes.CoreGuardPublic,
		},
	}}}); err != nil {
		t.Fatal(err)
	}
}

func jointReplayLifecycleRouteSchemas(t *testing.T) *extensionopenapi.RouteSchemaPublication {
	t.Helper()
	publication, err := extensionopenapi.NewRouteSchemaContractPublication([]extensionopenapi.CoreOperation{{
		RouteID: jointReplayCoreRouteID, Path: "/joint-replay", Method: "POST",
		OperationID: "core.route.joint_replay.post",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}
