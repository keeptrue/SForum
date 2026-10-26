package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestLifecycleComponentRegistryDefaultsToOwnedInstance(t *testing.T) {
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{})
	if boundary.ComponentRegistry() == nil {
		t.Fatal("lifecycle boundary did not create its component registry")
	}
	var nilBoundary *PostgresLifecycleBoundaryRegistries
	if nilBoundary.ComponentRegistry() != nil {
		t.Fatal("nil lifecycle boundary returned a component registry")
	}
}

func TestLifecycleComponentPublicationUsesHostIdentityAndRestoresExactSource(t *testing.T) {
	ctx := context.Background()
	repository := &memoryLifecycleRegistryRepository{phase: LifecycleRegistryPublicationSource}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	components := NewComponentRegistry()
	services := hostapi.NewServiceRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Repository: repository, Manager: manager, Pages: pages.NewRegistry(nil),
		Routes: routes.NewRegistry(), RouteSchemas: lifecycleRouteSchemaPublication(t),
		Services: services, Components: components,
	})

	source := lifecycleComponentTestExtension(t, "1.0.0", strings.Repeat("a", 64), 81, "/component-source")
	if err := manager.Start(ctx, source); err != nil {
		t.Fatal(err)
	}
	sourceRuntime, err := manager.ActiveRuntimeInstance(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceBinding := lifecycleRegistryBinding(source, sourceRuntime.Identity.InstanceID)
	sourceComponentID := componentPackageRuntimeInstanceID(source)
	if sourceComponentID == sourceBinding.RuntimeInstanceID {
		t.Fatal("component publication borrowed the process runtime identity")
	}
	if err := components.ReplaceRuntime(source, sourceComponentID); err != nil {
		t.Fatal(err)
	}

	target := lifecycleComponentTestExtension(t, "2.0.0", strings.Repeat("b", 64), 82, "/component-target")
	targetRuntime, err := manager.StageRuntimeInstance(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(ctx, targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	targetBinding := lifecycleRegistryBinding(target, targetRuntime.Identity.InstanceID)
	targetComponentID := componentPackageRuntimeInstanceID(target)
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
	if err := services.ReplaceExtension(target.ID, []hostapi.ServiceRegistration{
		lifecycleServiceRegistration(target, targetBinding.RuntimeInstanceID),
	}); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	targetSnapshot, ok := components.RuntimeSnapshot(target.ID)
	if !ok || targetSnapshot.InstanceID != targetComponentID ||
		targetSnapshot.Extension.PackageDigest != target.PackageDigest || targetSnapshot.InstanceID == targetBinding.RuntimeInstanceID {
		t.Fatalf("target component runtime = %#v, %t", targetSnapshot, ok)
	}
	revision := components.Snapshot().Revision
	if err := transaction.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if repeated := components.Snapshot().Revision; repeated != revision {
		t.Fatalf("idempotent component publish changed revision from %d to %d", revision, repeated)
	}

	if err := transaction.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	restored, ok := components.RuntimeSnapshot(source.ID)
	if !ok || restored.InstanceID != sourceComponentID || restored.Extension.PackageDigest != source.PackageDigest {
		t.Fatalf("restored component runtime = %#v, %t", restored, ok)
	}
	if removed, err := components.RemoveRuntime(source.ID, targetComponentID); removed ||
		!errors.Is(err, ErrComponentRegistryConflict) {
		t.Fatalf("stale target removal = %t, %v", removed, err)
	}
}

func TestLifecycleComponentDeactivationRemovesAndRestoresHostPublication(t *testing.T) {
	ctx := context.Background()
	repository := &memoryLifecycleRegistryRepository{phase: LifecycleRegistryPublicationSource}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	components := NewComponentRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Repository: repository, Manager: manager, Pages: pages.NewRegistry(nil), Routes: routes.NewRegistry(),
		RouteSchemas: lifecycleRouteSchemaPublication(t), Services: hostapi.NewServiceRegistry(), Components: components,
	})
	extension := lifecycleComponentTestExtension(t, "1.0.0", strings.Repeat("c", 64), 83, "/component-disable")
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := lifecycleRegistryBinding(extension, runtime.Identity.InstanceID)
	hostInstanceID := componentPackageRuntimeInstanceID(extension)
	if err := components.ReplaceRuntime(extension, hostInstanceID); err != nil {
		t.Fatal(err)
	}
	request := LifecycleBoundaryRequest{
		OperationID: 303, Operation: extensions.LifecycleMachineDisable, Position: 3,
		StepID: "lifecycle.disable.03.host.disabled", Attempt: 1,
		SourceExtension: &extension, TargetExtension: extension,
		SourceBinding: binding, TargetBinding: binding, ActorUserID: 42, AuditEventID: 86,
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
	if snapshot, ok := components.RuntimeSnapshot(extension.ID); ok {
		t.Fatalf("deactivated component runtime = %#v", snapshot)
	}
	if err := transaction.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot, ok := components.RuntimeSnapshot(extension.ID); !ok || snapshot.InstanceID != hostInstanceID {
		t.Fatalf("restored deactivated component runtime = %#v, %t", snapshot, ok)
	}
}

func TestLifecycleComponentStartupRestoresUnorderedStaticGraphAndSafeModeCoreOnly(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	components := NewComponentRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routes.NewRegistry(), RouteSchemas: lifecycleRouteSchemaPublication(t),
		Components: components,
	})
	ownerID := "component.boot-owner"
	ownerTarget := ownerID + ".component.card"
	ownerContract := ownerTarget + "@1"
	owner := componentTestExtension(t, ownerID, extensions.TypePlugin,
		componentTestContribution(ownerID, "card", extensionmanifest.ComponentActionAdd, 0, "", ""),
	)
	consumerID := "component.boot-consumer"
	consumer := componentTestExtension(t, consumerID, extensions.TypePlugin,
		componentTestContribution(
			consumerID, "wrap-card", extensionmanifest.ComponentActionWrap, 10, ownerTarget, ownerContract,
		),
	)
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{
		ID: ownerID, Version: "^1.0.0", Kind: "required",
	}}
	themeID := "component.boot-theme"
	theme := componentTestExtension(t, themeID, extensions.TypeTheme,
		componentTestContribution(
			themeID, "hide-home", extensionmanifest.ComponentActionHide, 5,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	items := []extensions.Extension{consumer, theme, owner}
	if err := boundary.RestoreRoutePublications(ctx, items, false); err != nil {
		t.Fatal(err)
	}
	plan, err := components.ResolvePlan(ownerTarget, ownerContract)
	if err != nil || plan.Target.Provider == nil || len(plan.Contributions) != 1 ||
		plan.Contributions[0].Artifact.ExtensionID != consumerID {
		t.Fatalf("restored unordered component graph = %#v, %v", plan, err)
	}
	if snapshot, ok := components.RuntimeSnapshot(consumerID); !ok ||
		snapshot.InstanceID != componentPackageRuntimeInstanceID(consumer) ||
		strings.TrimSpace(snapshot.Extension.Manifest.Backend.Entry) != "" {
		t.Fatalf("restored no-backend component = %#v, %t", snapshot, ok)
	}
	corePlan, err := components.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil || len(corePlan.Contributions) != 1 || corePlan.Contributions[0].Artifact.ExtensionID != themeID {
		t.Fatalf("restored theme component = %#v, %v", corePlan, err)
	}

	if err := boundary.RestoreRoutePublications(ctx, items, true); err != nil {
		t.Fatal(err)
	}
	safe := components.Snapshot()
	if len(safe.Contributions) != 0 || len(safe.Conflicts) != 0 || len(safe.Selections) != 0 {
		t.Fatalf("Safe Mode component snapshot = %#v", safe)
	}
	if _, ok := components.RuntimeSnapshot(ownerID); ok {
		t.Fatal("Safe Mode retained an extension component runtime")
	}
	if _, err := components.ResolvePlan(ownerTarget, ownerContract); !errors.Is(err, ErrComponentRegistryTargetNotFound) {
		t.Fatalf("Safe Mode extension target = %v", err)
	}
	if corePlan, err := components.ResolvePlan(componentTestCoreTarget, componentTestCoreContract); err != nil ||
		len(corePlan.Contributions) != 0 || !corePlan.Target.Core {
		t.Fatalf("Safe Mode Core plan = %#v, %v", corePlan, err)
	}
}

func TestLifecycleComponentTransitionRejectsProcessRuntimeIdentity(t *testing.T) {
	source := lifecycleComponentTestExtension(t, "1.0.0", strings.Repeat("d", 64), 84, "/component-fence-source")
	target := lifecycleComponentTestExtension(t, "2.0.0", strings.Repeat("e", 64), 85, "/component-fence-target")
	sourceBinding := lifecycleRegistryBinding(source, "process-source")
	targetBinding := lifecycleRegistryBinding(target, "process-target")
	sourceMaterial, err := buildLifecycleRegistryMaterial(source, sourceBinding)
	if err != nil {
		t.Fatal(err)
	}
	targetMaterial, err := buildLifecycleRegistryMaterial(target, targetBinding)
	if err != nil {
		t.Fatal(err)
	}
	components := NewComponentRegistry()
	if err := components.ReplaceRuntime(source, sourceBinding.RuntimeInstanceID); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Components: components})
	before := components.Snapshot()
	err = boundary.validateComponentTransition(&sourceMaterial, &targetMaterial)
	if !errors.Is(err, ErrLifecycleRegistryPublicationConflict) || !errors.Is(err, ErrComponentRegistryConflict) {
		t.Fatalf("process component identity validation = %v", err)
	}
	after := components.Snapshot()
	if after.Revision != before.Revision || len(after.Contributions) != len(before.Contributions) {
		t.Fatalf("failed exact validation changed snapshot: before=%#v after=%#v", before, after)
	}
}

func lifecycleComponentTestExtension(
	t *testing.T,
	version string,
	digestSeed string,
	versionID int64,
	pagePath string,
) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, digestSeed, versionID, pagePath)
	extension.Manifest.Components = []extensions.ManifestComponent{{
		ID: "registry.demo.component.hide-home", ContractVersion: "registry.demo.component.hide-home@1",
		Action:   extensionmanifest.ComponentActionHide,
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
	}}
	document, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(extension.PackagePath, extensionmanifest.ManifestFileName), document, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest, err = extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	return extension
}
