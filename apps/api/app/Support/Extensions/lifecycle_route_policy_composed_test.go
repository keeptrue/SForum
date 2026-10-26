package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestComposedLifecycleRoutePolicyPublishesProductionRegistry(t *testing.T) {
	tests := []struct {
		name            string
		operation       extensions.LifecycleMachineOperation
		position        int
		sourceVersion   string
		sourceVersionID int64
		targetVersion   string
		targetVersionID int64
		activate        bool
	}{
		{
			name: "enable", operation: extensions.LifecycleMachineEnable, position: 5,
			targetVersion: "1.0.0", targetVersionID: 1301, activate: true,
		},
		{
			name: "upgrade", operation: extensions.LifecycleMachineUpgrade, position: 8,
			sourceVersion: "1.0.0", sourceVersionID: 1311,
			targetVersion: "2.0.0", targetVersionID: 1312, activate: true,
		},
		{
			name: "rollback", operation: extensions.LifecycleMachineRollback, position: 6,
			sourceVersion: "2.0.0", sourceVersionID: 1322,
			targetVersion: "1.0.0", targetVersionID: 1321, activate: true,
		},
		{
			name: "disable", operation: extensions.LifecycleMachineDisable, position: 3,
			sourceVersion: "1.0.0", sourceVersionID: 1331,
			targetVersion: "1.0.0", targetVersionID: 1331,
		},
		{
			name: "uninstall", operation: extensions.LifecycleMachineUninstall, position: 3,
			sourceVersion: "1.0.0", sourceVersionID: 1341,
			targetVersion: "1.0.0", targetVersionID: 1341,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			extensionID := "policy.composed." + test.name
			target := lifecycleComposedRequiredPolicyExtension(
				t,
				extensionID,
				test.targetVersion,
				test.targetVersionID,
				byte('g'+index),
			)
			var source *extensions.Extension
			if test.sourceVersion != "" {
				value := lifecycleComposedRequiredPolicyExtension(
					t, extensionID, test.sourceVersion, test.sourceVersionID, byte('m'+index),
				)
				source = &value
				if !test.activate {
					// Disable and uninstall operate on the current exact artifact.
					target = value
				}
			}
			manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
			routeRegistry := routes.NewRegistry()
			routeSchemas := lifecycleRouteSchemaPublication(t)
			registries := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
				Repository: &memoryLifecycleRegistryRepository{phase: LifecycleRegistryPublicationSource},
				Manager:    manager, Pages: pages.NewRegistry(nil), Routes: routeRegistry,
				RouteSchemas: routeSchemas, Services: hostapi.NewServiceRegistry(),
			})

			var sourceRuntime RuntimeInstanceSnapshot
			var sourceBinding extensions.LifecycleRuntimeBinding
			var err error
			if source != nil {
				err = manager.Start(ctx, *source)
				if err == nil {
					sourceRuntime, err = manager.ActiveRuntimeInstance(source.ID)
				}
				if err == nil {
					sourceBinding = lifecycleRegistryBinding(*source, sourceRuntime.Identity.InstanceID)
					err = registries.RestoreRoutePublications(ctx, []extensions.Extension{*source}, false)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if source != nil && test.activate && source.PackageDigest == target.PackageDigest {
				t.Fatal("source and target route policy artifacts have the same package digest")
			}
			if source != nil {
				assertLifecycleComposedRequiredPolicy(t, routeRegistry, *source, sourceBinding.RuntimeInstanceID)
			}

			var targetRuntime RuntimeInstanceSnapshot
			if test.activate {
				targetRuntime, err = manager.StageRuntimeInstance(ctx, target)
				if err == nil {
					_, err = manager.HealthRuntimeInstance(ctx, targetRuntime.Identity)
				}
			} else {
				targetRuntime = sourceRuntime
			}
			if err != nil {
				t.Fatal(err)
			}
			if source != nil && test.activate && sourceRuntime.Identity == targetRuntime.Identity {
				t.Fatal("source and target route policy artifacts share one runtime identity")
			}
			targetBinding := lifecycleRegistryBinding(target, targetRuntime.Identity.InstanceID)

			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			request := fixture.request
			request.Extension = target
			request.TargetExtension = target
			request.TargetBinding = targetBinding
			if test.operation == extensions.LifecycleMachineUpgrade || test.operation == extensions.LifecycleMachineRollback {
				request.SourceExtension = source
				request.SourceBinding = sourceBinding
			} else {
				request.SourceExtension = nil
				request.SourceBinding = targetBinding
			}
			request.AuthoritySnapshot = exactCoordinatorTestAuthority(
				t, target, request.AuthorityActorUserID, request.TrustGrantID,
			)
			fixture.request = request
			jobs := &lifecycleComposedPolicyJobs{
				composedBoundaryJobs: fixture.jobs, manager: manager, routes: routeRegistry,
			}
			fixture.boundary = NewComposedLifecycleHostBoundary(ComposedLifecycleHostBoundaryDependencies{
				Runtime: manager, Preflight: fixture.preflight, Migrations: fixture.migrations,
				Jobs: jobs, Registries: registries, State: fixture.state,
				Journal: fixture.journal, Cleanup: fixture.cleanup,
			})

			if _, err := fixture.boundary.RunLifecycleHostBoundary(ctx, request); err != nil {
				t.Fatal(err)
			}
			if test.activate {
				assertLifecycleComposedRequiredPolicy(t, routeRegistry, target, targetBinding.RuntimeInstanceID)
				return
			}
			assertLifecycleComposedPolicyEmpty(t, routeRegistry)
		})
	}
}

type lifecycleComposedPolicyJobs struct {
	*composedBoundaryJobs
	manager *Manager
	routes  *routes.Registry
}

func (j *lifecycleComposedPolicyJobs) ResumeLifecycleJobs(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	role extensions.LifecycleCoordinatorRuntimeRole,
) error {
	if err := j.composedBoundaryJobs.ResumeLifecycleJobs(ctx, request, mode, role); err != nil {
		return err
	}
	if _, err := j.routes.BuildExecutionPlan("POST", "/joint-replay"); !errors.Is(err, routes.ErrRouteNotFound) {
		return fmt.Errorf("%w: pre-resume route admission error = %v", ErrLifecycleBoundaryInvalid, err)
	}
	extension, binding, err := lifecycleJobRole(request, mode, role)
	if err != nil {
		return err
	}
	identity, err := lifecycleJobRuntimeIdentity(extension, binding)
	if err != nil {
		return err
	}
	resumed, err := j.manager.ResumeRuntimeInstance(identity)
	if err != nil {
		return err
	}
	return validateLifecycleBoundaryAdmission("resume composed route policy runtime", resumed, identity, false, true)
}

func lifecycleComposedRequiredPolicyExtension(
	t *testing.T,
	extensionID string,
	version string,
	versionID int64,
	digestByte byte,
) extensions.Extension {
	t.Helper()
	extension := jointReplayLifecycleExtension(t, extensionID, digestByte, true, "", "")
	extension.Version = version
	extension.ActiveVersionID = versionID
	extension.Manifest.Version = version
	extension.Manifest.Routes[0].Action = extensionmanifest.RouteActionAdd
	extension.Manifest.Routes[0].TargetID = ""

	fragment := &extension.Manifest.OpenAPI[0]
	fragmentPath := filepath.Join(extension.PackagePath, filepath.FromSlash(fragment.Path))
	body, err := os.ReadFile(fragmentPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	info, ok := document["info"].(map[string]any)
	if !ok {
		t.Fatal("joint replay OpenAPI fixture has no info object")
	}
	info["version"] = version
	body, err = yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fragmentPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	fragment.Digest = hex.EncodeToString(digest[:])
	packageFileFound := false
	for index := range extension.Manifest.PackageFiles {
		if extension.Manifest.PackageFiles[index].Path == fragment.Path {
			extension.Manifest.PackageFiles[index].Digest = fragment.Digest
			packageFileFound = true
			break
		}
	}
	if !packageFileFound {
		t.Fatalf("joint replay OpenAPI package file %q is missing", fragment.Path)
	}
	if err := extensionmanifest.Validate(extension.Manifest); err != nil {
		t.Fatalf("composed route policy manifest: %v", err)
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

func assertLifecycleComposedRequiredPolicy(
	t *testing.T,
	registry *routes.Registry,
	extension extensions.Extension,
	runtimeInstanceID string,
) {
	t.Helper()
	snapshot := registry.PublicationSnapshot()
	publication := snapshot.Publication
	wantPolicy := routes.RouteExecutionPolicy{
		RateLimit: "host.ip_write@1", Idempotency: "required.24h@1", IdempotencyRequired: true,
		RequestSizeBytes: 1 << 20, CORSPolicy: "host.cors.same_origin@1",
	}
	if publication.SafeMode || publication.Policies == nil || len(publication.Plugins) != 1 || len(publication.Policies) != 1 {
		t.Fatalf("composed route policy publication = %#v", publication)
	}
	binding := publication.Policies[0]
	route := extension.Manifest.Routes[0]
	if binding.Artifact.ExtensionID != extension.ID ||
		binding.Artifact.ExtensionVersion != extension.Version ||
		binding.Artifact.PackageDigest != extension.PackageDigest ||
		binding.Artifact.RuntimeInstanceID != runtimeInstanceID ||
		binding.RouteID != route.ID || binding.ContractVersion != route.ContractVersion ||
		binding.Method != "POST" || binding.Policy != wantPolicy {
		t.Fatalf("composed route policy binding = %#v", binding)
	}
	plan, err := registry.BuildExecutionPlan("POST", "/joint-replay")
	if err != nil {
		t.Fatal(err)
	}
	policy, bound := plan.ExecutionPolicy()
	if plan.Revision() != snapshot.Revision || !bound || policy != wantPolicy ||
		plan.Terminal().Provider.Artifact != binding.Artifact {
		t.Fatalf(
			"composed plan revision=%d policy=%#v bound=%t terminal=%#v snapshot=%d binding=%#v",
			plan.Revision(), policy, bound, plan.Terminal(), snapshot.Revision, binding,
		)
	}
}

func assertLifecycleComposedPolicyEmpty(t *testing.T, registry *routes.Registry) {
	t.Helper()
	publication := registry.PublicationSnapshot().Publication
	if publication.SafeMode || len(publication.Plugins) != 0 || publication.Policies == nil || len(publication.Policies) != 0 {
		t.Fatalf("empty composed route policy publication = %#v", publication)
	}
	if _, err := registry.BuildExecutionPlan("POST", "/joint-replay"); !errors.Is(err, routes.ErrRouteNotFound) {
		t.Fatalf("removed composed route plan error = %v", err)
	}
}

var _ LifecycleBoundaryJobs = (*lifecycleComposedPolicyJobs)(nil)
