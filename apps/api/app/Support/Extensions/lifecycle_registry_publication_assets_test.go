package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

type staticAssetAuthority struct {
	mu        sync.Mutex
	operation map[string]string
	restore   map[string]string
	onRestore func()
	calls     []string
}

func (a *staticAssetAuthority) OperationImpactDigest(
	_ context.Context,
	operationID int64,
	extension extensions.Extension,
) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, "operation:"+extension.ID)
	if operationID <= 0 || a.operation[extension.ID] == "" {
		return "", extensions.ErrTrustGrantNotFound
	}
	return a.operation[extension.ID], nil
}

func (a *staticAssetAuthority) RestoreImpactDigest(
	_ context.Context,
	extension extensions.Extension,
) (string, error) {
	a.mu.Lock()
	a.calls = append(a.calls, "restore:"+extension.ID)
	impact := a.restore[extension.ID]
	hook := a.onRestore
	a.onRestore = nil
	a.mu.Unlock()
	if hook != nil {
		hook()
	}
	if impact == "" {
		return "", extensions.ErrTrustGrantNotFound
	}
	return impact, nil
}

type staticAssetAdmission struct {
	denied map[string]error
}

func (a staticAssetAdmission) ValidatePublishedIdentity(
	_ context.Context,
	extension extensions.Extension,
	artifact assetregistry.Artifact,
) error {
	if extension.ID != artifact.ExtensionID || extension.Version != artifact.ExtensionVersion ||
		extension.PackageDigest != artifact.PackageDigest || extension.Status != extensions.StatusEnabled {
		return extensions.ErrPublicFrontendUnavailable
	}
	return a.denied[extension.ID]
}

func TestLifecycleAssetRestartRestoresAssetOnlyGraphAndSafeModeKeepsOnlyCore(t *testing.T) {
	ctx := context.Background()
	owner := lifecycleAssetTestExtension(t, "owner.assets", nil)
	consumer := lifecycleAssetTestExtension(t, "consumer.assets", []string{owner.Manifest.Assets[0].Handle})
	// Provider has no L2 component: the manifest Asset declaration alone must publish.
	owner.Manifest.Components[0].L2Component = ""
	authority := &staticAssetAuthority{restore: map[string]string{
		owner.ID: strings.Repeat("a", 64), consumer.ID: strings.Repeat("b", 64),
	}}

	for restart := 0; restart < 2; restart++ {
		assets := assetregistry.New()
		boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
			Assets: assets, AssetAuthority: authority, AssetAdmission: staticAssetAdmission{},
		})
		if err := boundary.restoreAssetPublications(ctx, []extensions.Extension{consumer, owner}, false); err != nil {
			t.Fatalf("restart %d restore: %v", restart, err)
		}
		snapshot := assets.Snapshot()
		if snapshot.Revision != 1 || len(snapshot.Publications) != 2 {
			t.Fatalf("restart %d snapshot=%#v", restart, snapshot)
		}
		ownerPublication, ok := assets.SnapshotPublication(owner.ID)
		if !ok || len(ownerPublication.Assets) != 1 {
			t.Fatalf("asset-only owner publication=%#v ok=%t", ownerPublication, ok)
		}
		plan, err := assets.Plan(assetregistry.PlanRequest{
			Handles: []string{consumer.Manifest.Components[0].ID + ".l2.entry"},
		})
		if err != nil || len(plan) < 2 || plan[0].Handle != owner.Manifest.Assets[0].Handle {
			t.Fatalf("dependency plan=%#v err=%v", plan, err)
		}
	}

	assets := assetregistry.New()
	core := lifecycleCoreAssetPublication()
	ownerPublication, err := extensions.BuildPublicAssetPublication(owner, authority.restore[owner.ID])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assets.ReplaceAll([]assetregistry.Publication{core, ownerPublication}); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Assets: assets})
	if err := boundary.restoreAssetPublications(ctx, []extensions.Extension{owner}, true); err != nil {
		t.Fatalf("safe mode restore: %v", err)
	}
	snapshot := assets.Snapshot()
	if len(snapshot.Publications) != 1 || snapshot.Publications[0].Artifact != core.Artifact {
		t.Fatalf("safe mode did not preserve only Host assets: %#v", snapshot)
	}
}

func TestLifecycleAssetRestartRestoresEditorOnlyL2Publication(t *testing.T) {
	extension := lifecycleEditorTestExtension(t, "1.0.0", strings.Repeat("31", 32), 601)
	assets := assetregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Assets: assets,
		AssetAuthority: &staticAssetAuthority{restore: map[string]string{
			extension.ID: strings.Repeat("a", 64),
		}},
		AssetAdmission: staticAssetAdmission{},
	})
	if err := boundary.restoreAssetPublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	publication, found := assets.SnapshotPublication(extension.ID)
	if !found || len(publication.Assets) != 1 ||
		publication.Assets[0].Path != extension.Manifest.Editor[0].L2Module ||
		len(publication.Assets[0].Scope) != 1 {
		t.Fatalf("editor-only asset publication=%+v found=%t", publication, found)
	}
	plan, err := assets.Plan(assetregistry.PlanRequest{IncludeGlobal: true})
	if err != nil || len(plan) != 0 {
		t.Fatalf("editor-only asset leaked into global plan: plan=%+v err=%v", plan, err)
	}
}

func TestLifecycleAssetRestoreUsesCapturedRevisionAndNeverOverwritesWatcher(t *testing.T) {
	ctx := context.Background()
	extension := lifecycleAssetTestExtension(t, "restore.cas.assets", nil)
	assets := assetregistry.New()
	authority := &staticAssetAuthority{restore: map[string]string{
		extension.ID: strings.Repeat("a", 64),
	}}
	unrelated := lifecycleAssetTestExtension(t, "watcher.assets", nil)
	unrelatedPublication, err := extensions.BuildPublicAssetPublication(unrelated, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	authority.onRestore = func() {
		if _, publishErr := assets.Publish(unrelatedPublication); publishErr != nil {
			t.Errorf("watcher publication: %v", publishErr)
		}
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Assets: assets, AssetAuthority: authority, AssetAdmission: staticAssetAdmission{},
	})
	err = boundary.restoreAssetPublications(ctx, []extensions.Extension{extension}, false)
	if !errors.Is(err, ErrLifecycleRegistryPublicationConflict) ||
		!errors.Is(err, assetregistry.ErrRevisionConflict) {
		t.Fatalf("stale restore error=%v", err)
	}
	if _, ok := assets.SnapshotPublication(unrelated.ID); !ok {
		t.Fatal("stale restore overwrote watcher publication")
	}
	if _, ok := assets.SnapshotPublication(extension.ID); ok {
		t.Fatal("stale restore partially published desired artifact")
	}
}

func TestLifecycleAssetRestartDropsRevokedOwnerDependencyClosure(t *testing.T) {
	ctx := context.Background()
	owner := lifecycleAssetTestExtension(t, "revoked.owner", nil)
	consumer := lifecycleAssetTestExtension(t, "revoked.consumer", []string{owner.Manifest.Assets[0].Handle})
	transitive := lifecycleAssetTestExtension(t, "revoked.transitive", []string{consumer.Manifest.Assets[0].Handle})
	unrelated := lifecycleAssetTestExtension(t, "revoked.unrelated", nil)
	assets := assetregistry.New()
	core := lifecycleCoreAssetPublication()
	if _, err := assets.Publish(core); err != nil {
		t.Fatal(err)
	}
	authority := &staticAssetAuthority{restore: map[string]string{
		consumer.ID: strings.Repeat("a", 64), transitive.ID: strings.Repeat("b", 64),
		unrelated.ID: strings.Repeat("c", 64),
		// owner intentionally has no live/durable authority after revoke.
	}}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Assets: assets, AssetAuthority: authority, AssetAdmission: staticAssetAdmission{},
	})
	if err := boundary.restoreAssetPublications(
		ctx, []extensions.Extension{transitive, consumer, unrelated, owner}, false,
	); err != nil {
		t.Fatalf("restart restore after owner revoke: %v", err)
	}
	snapshot := assets.Snapshot()
	if len(snapshot.Publications) != 2 {
		t.Fatalf("restart closure snapshot=%#v", snapshot)
	}
	if _, ok := assets.SnapshotPublication(owner.ID); ok {
		t.Fatal("restart restored revoked owner")
	}
	if _, ok := assets.SnapshotPublication(consumer.ID); ok {
		t.Fatal("restart retained direct dependent")
	}
	if _, ok := assets.SnapshotPublication(transitive.ID); ok {
		t.Fatal("restart retained transitive dependent")
	}
	if _, ok := assets.SnapshotPublication(unrelated.ID); !ok {
		t.Fatal("restart removed unrelated publication")
	}
	if publication, ok := assets.SnapshotPublication(core.Artifact.ExtensionID); !ok || publication.Artifact != core.Artifact {
		t.Fatal("restart removed Host core publication")
	}
}

func TestLifecycleAssetPlanUpgradeStaleFenceAndFailureCompensation(t *testing.T) {
	ctx := context.Background()
	assets := assetregistry.New()
	source := lifecycleAssetTestExtension(t, "lifecycle.assets", nil)
	target := lifecycleAssetUpgrade(t, source, "2.0.0")
	consumer := lifecycleAssetTestExtension(t, "lifecycle.consumer", []string{source.Manifest.Assets[0].Handle})
	sourcePublication := mustLifecycleAssetPublication(t, source, strings.Repeat("c", 64))
	consumerPublication := mustLifecycleAssetPublication(t, consumer, strings.Repeat("e", 64))
	if _, err := assets.ReplaceAll([]assetregistry.Publication{consumerPublication, sourcePublication}); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Assets: assets})
	sourceMaterial := lifecycleAssetMaterial(t, source, sourcePublication, true)
	targetPublication := mustLifecycleAssetPublication(t, target, strings.Repeat("d", 64))
	targetMaterial := lifecycleAssetMaterial(t, target, targetPublication, true)

	plan, err := boundary.prepareAssetPlan(sourceMaterial, targetMaterial)
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.applyAssetPlan(ctx, plan, LifecycleRegistryPublicationTarget); err != nil {
		t.Fatalf("publish target: %v", err)
	}
	published, ok := assets.SnapshotPublication(target.ID)
	if !ok || published.Artifact != targetPublication.Artifact {
		t.Fatalf("target publication=%#v ok=%t", published, ok)
	}
	if err := boundary.applyAssetPlan(ctx, plan, LifecycleRegistryPublicationSource); err != nil {
		t.Fatalf("failure compensation: %v", err)
	}
	restored := assets.Snapshot()
	if len(restored.Publications) != 2 {
		t.Fatalf("compensation lost cross-owner graph: %#v", restored)
	}
	if publication, ok := assets.SnapshotPublication(source.ID); !ok || publication.Artifact != sourcePublication.Artifact {
		t.Fatalf("compensation source=%#v ok=%t", publication, ok)
	}
	if _, ok := assets.SnapshotPublication(consumer.ID); !ok {
		t.Fatal("compensation did not restore dependent consumer")
	}

	stalePlan, err := boundary.prepareAssetPlan(sourceMaterial, targetMaterial)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := lifecycleAssetTestExtension(t, "unrelated.assets", nil)
	if _, err := assets.Publish(mustLifecycleAssetPublication(t, unrelated, strings.Repeat("f", 64))); err != nil {
		t.Fatal(err)
	}
	err = boundary.applyAssetPlan(ctx, stalePlan, LifecycleRegistryPublicationTarget)
	if !errors.Is(err, assetregistry.ErrRevisionConflict) {
		t.Fatalf("stale upgrade error=%v", err)
	}
	if publication, ok := assets.SnapshotPublication(source.ID); !ok || publication.Artifact != sourcePublication.Artifact {
		t.Fatal("stale upgrade replaced exact source")
	}
	if _, ok := assets.SnapshotPublication(unrelated.ID); !ok {
		t.Fatal("stale upgrade overwrote unrelated watcher publication")
	}
}

func TestLifecycleAssetDeactivateQuarantinesClosureAndRestoreRecoversIt(t *testing.T) {
	ctx := context.Background()
	owner := lifecycleAssetTestExtension(t, "closure.owner", nil)
	consumer := lifecycleAssetTestExtension(t, "closure.consumer", []string{owner.Manifest.Assets[0].Handle})
	ownerPublication := mustLifecycleAssetPublication(t, owner, strings.Repeat("a", 64))
	consumerPublication := mustLifecycleAssetPublication(t, consumer, strings.Repeat("b", 64))
	assets := assetregistry.New()
	if _, err := assets.ReplaceAll([]assetregistry.Publication{consumerPublication, ownerPublication}); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Assets: assets})
	source := lifecycleAssetMaterial(t, owner, ownerPublication, true)
	plan, err := boundary.prepareAssetPlan(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.applyAssetPlan(ctx, plan, LifecycleRegistryPublicationTarget); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if snapshot := assets.Snapshot(); len(snapshot.Publications) != 0 || len(snapshot.Assets) != 0 {
		t.Fatalf("deactivate left transitive dependents: %#v", snapshot)
	}
	if err := boundary.applyAssetPlan(ctx, plan, LifecycleRegistryPublicationSource); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if snapshot := assets.Snapshot(); len(snapshot.Publications) != 2 || len(snapshot.Assets) != 4 {
		t.Fatalf("restore lost exact closure: %#v", snapshot)
	}
}

func TestLifecycleAssetConcurrentPreparedPlansHaveSingleCASWinner(t *testing.T) {
	ctx := context.Background()
	for attempt := 0; attempt < 32; attempt++ {
		assets := assetregistry.New()
		source := lifecycleAssetTestExtension(t, "race.assets", nil)
		target := lifecycleAssetUpgrade(t, source, "2.0.0")
		sourcePublication := mustLifecycleAssetPublication(t, source, strings.Repeat("a", 64))
		targetPublication := mustLifecycleAssetPublication(t, target, strings.Repeat("b", 64))
		if _, err := assets.Publish(sourcePublication); err != nil {
			t.Fatal(err)
		}
		boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Assets: assets})
		sourceMaterial := lifecycleAssetMaterial(t, source, sourcePublication, true)
		targetMaterial := lifecycleAssetMaterial(t, target, targetPublication, true)
		left, err := boundary.prepareAssetPlan(sourceMaterial, targetMaterial)
		if err != nil {
			t.Fatal(err)
		}
		right, err := boundary.prepareAssetPlan(sourceMaterial, targetMaterial)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, plan := range []*lifecycleAssetPlan{left, right} {
			plan := plan
			go func() {
				<-start
				results <- boundary.applyAssetPlan(ctx, plan, LifecycleRegistryPublicationTarget)
			}()
		}
		close(start)
		succeeded, conflicted := 0, 0
		for range 2 {
			switch err := <-results; {
			case err == nil:
				succeeded++
			case errors.Is(err, assetregistry.ErrRevisionConflict):
				conflicted++
			default:
				t.Fatalf("unexpected race result: %v", err)
			}
		}
		if succeeded != 1 || conflicted != 1 {
			t.Fatalf("attempt %d: succeeded=%d conflicted=%d", attempt, succeeded, conflicted)
		}
	}
}

func TestLifecycleAssetTransactionRestoresAfterLaterRegistryFamilyFailure(t *testing.T) {
	ctx := context.Background()
	repository := &memoryLifecycleRegistryRepository{phase: LifecycleRegistryPublicationSource}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	serviceRegistry := hostapi.NewServiceRegistry()
	assets := assetregistry.New()
	source := lifecycleRegistryAssetOnlyExtension(t, "1.0.0", strings.Repeat("d", 64), 1, "/asset-source")
	target := lifecycleRegistryAssetOnlyExtension(t, "2.0.0", strings.Repeat("e", 64), 2, "/asset-target")
	authority := &staticAssetAuthority{
		restore:   map[string]string{source.ID: strings.Repeat("a", 64)},
		operation: map[string]string{target.ID: strings.Repeat("b", 64)},
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Repository: repository, Manager: manager, Pages: pages.NewRegistry(nil), Routes: routes.NewRegistry(),
		RouteSchemas: lifecycleRouteSchemaPublication(t), Services: serviceRegistry,
		Assets: assets, AssetAuthority: authority, AssetAdmission: staticAssetAdmission{},
	})

	if err := manager.Start(ctx, source); err != nil {
		t.Fatal(err)
	}
	sourceRuntime, err := manager.ActiveRuntimeInstance(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceBinding := lifecycleRegistryBinding(source, sourceRuntime.Identity.InstanceID)
	targetRuntime, err := manager.StageRuntimeInstance(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(ctx, targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	targetBinding := lifecycleRegistryBinding(target, targetRuntime.Identity.InstanceID)

	consumer := lifecycleAssetTestExtension(t, "transaction.consumer", []string{source.Manifest.Assets[0].Handle})
	sourcePublication := mustLifecycleAssetPublication(t, source, authority.restore[source.ID])
	consumerPublication := mustLifecycleAssetPublication(t, consumer, strings.Repeat("c", 64))
	if _, err := assets.ReplaceAll([]assetregistry.Publication{consumerPublication, sourcePublication}); err != nil {
		t.Fatal(err)
	}
	transaction, err := boundary.PrepareLifecycleRegistryPublication(
		ctx,
		lifecycleRegistryRequest(source, target, sourceBinding, targetBinding, 1),
		LifecycleBoundaryActivate,
	)
	if err != nil {
		t.Fatalf("prepare transaction: %v", err)
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
	if err := manager.WaitDrain(ctx, targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishDrainedRuntimeInstance(ctx, targetRuntime.Identity); err != nil {
		t.Fatal(err)
	}
	if err := serviceRegistry.ReplaceExtension(source.ID, []hostapi.ServiceRegistration{
		lifecycleServiceRegistration(source, "foreign-runtime"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Publish(ctx); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("later-family publish error=%v", err)
	}
	targetPublication := mustLifecycleAssetPublication(t, target, authority.operation[target.ID])
	if publication, ok := assets.SnapshotPublication(target.ID); !ok || publication.Artifact != targetPublication.Artifact {
		t.Fatalf("asset target did not apply before later failure: %#v ok=%t", publication, ok)
	}
	if !serviceRegistry.UnregisterExtension(source.ID) {
		t.Fatal("failed to remove injected foreign service family")
	}
	if err := transaction.Restore(ctx); err != nil {
		t.Fatalf("transaction restore: %v", err)
	}
	if publication, ok := assets.SnapshotPublication(source.ID); !ok || publication.Artifact != sourcePublication.Artifact {
		t.Fatalf("transaction did not restore source: %#v ok=%t", publication, ok)
	}
	if publication, ok := assets.SnapshotPublication(consumer.ID); !ok || publication.Artifact != consumerPublication.Artifact {
		t.Fatalf("transaction did not restore dependent closure: %#v ok=%t", publication, ok)
	}
}

func TestLifecycleAssetAuthorityDocumentBindsExactImpact(t *testing.T) {
	extension := lifecycleAssetTestExtension(t, "authority.assets", nil)
	extension.Source = extensions.SourceUploaded
	impact := extensions.TrustImpact{
		SchemaVersion: extensions.TrustImpactSchemaV2, Action: extensions.TrustActionEnable,
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		ExtensionType: extension.Type, Source: extension.Source, PackageDigest: extension.PackageDigest,
		ArtifactDigests: map[string]string{"package": extension.PackageDigest}, Digest: strings.Repeat("c", 64),
	}
	authority := extensions.LifecycleAuthoritySnapshot{
		SchemaVersion: extensions.LifecycleAuthoritySnapshotSchemaV1,
		AuthorityType: extensions.LifecycleAuthorityTrustGrant, ActorUserID: 7, Impact: impact,
		Grant: &extensions.TrustGrant{
			ID: 8, ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, Action: extensions.TrustActionEnable,
			ImpactDigest: impact.Digest,
		},
	}
	document, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := lifecycleAssetAuthorityImpact(extension, document); err != nil || got != impact.Digest {
		t.Fatalf("impact=%q err=%v", got, err)
	}
	changed := extension
	changed.PackageDigest = strings.Repeat("d", 64)
	if _, err := lifecycleAssetAuthorityImpact(changed, document); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("changed package authority error=%v", err)
	}
}

func TestLifecycleAssetMaterialFreezesOperationAndRestoreAuthoritiesIntoDigest(t *testing.T) {
	ctx := context.Background()
	source := lifecycleAssetTestExtension(t, "freeze.assets", nil)
	target := lifecycleAssetUpgrade(t, source, "2.0.0")
	authority := &staticAssetAuthority{
		restore:   map[string]string{source.ID: strings.Repeat("a", 64)},
		operation: map[string]string{target.ID: strings.Repeat("b", 64)},
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Assets: assetregistry.New(), AssetAuthority: authority, AssetAdmission: staticAssetAdmission{},
	})
	sourceMaterial := &lifecycleRegistryMaterial{extension: source}
	targetMaterial := &lifecycleRegistryMaterial{extension: target}
	if err := refreshLifecycleRegistryMaterialDigest(sourceMaterial); err != nil {
		t.Fatal(err)
	}
	if err := refreshLifecycleRegistryMaterialDigest(targetMaterial); err != nil {
		t.Fatal(err)
	}
	sourceDigestBefore, targetDigestBefore := sourceMaterial.digest, targetMaterial.digest
	request := LifecycleBoundaryRequest{OperationID: 91, TargetExtension: target}
	if err := boundary.freezeAssetMaterials(ctx, request, sourceMaterial, targetMaterial); err != nil {
		t.Fatal(err)
	}
	if sourceMaterial.assetPublication == nil || targetMaterial.assetPublication == nil ||
		sourceMaterial.assetPublication.Artifact.ImpactDigest != strings.Repeat("a", 64) ||
		targetMaterial.assetPublication.Artifact.ImpactDigest != strings.Repeat("b", 64) ||
		sourceMaterial.assetPublication.Artifact.OwnerKind != assetregistry.OwnerKindPlugin ||
		targetMaterial.assetPublication.Artifact.OwnerKind != assetregistry.OwnerKindPlugin ||
		sourceMaterial.digest == sourceDigestBefore || targetMaterial.digest == targetDigestBefore ||
		sourceMaterial.legacyDigest != sourceDigestBefore || targetMaterial.legacyDigest != targetDigestBefore {
		t.Fatalf("frozen source=%#v target=%#v", sourceMaterial, targetMaterial)
	}
	if !reflect.DeepEqual(registryMaterialCompatibleDigests(sourceMaterial), []string{sourceDigestBefore}) ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(targetMaterial), []string{targetDigestBefore}) {
		t.Fatalf("legacy aliases source=%v target=%v",
			registryMaterialCompatibleDigests(sourceMaterial), registryMaterialCompatibleDigests(targetMaterial))
	}
	record := lifecycleRegistryPublicationRecord{
		Fence:        requestFenceForAssetDigestTest(request),
		SourceDigest: sourceMaterial.legacyDigest,
		TargetDigest: targetMaterial.legacyDigest,
	}
	resume := PrepareLifecycleRegistryPublicationInput{
		Fence: record.Fence, SourceDigest: sourceMaterial.digest, TargetDigest: targetMaterial.digest,
		CompatibleSourceDigests: registryMaterialCompatibleDigests(sourceMaterial),
		CompatibleTargetDigests: registryMaterialCompatibleDigests(targetMaterial),
	}
	if !record.matchesInput(resume) || !validLifecycleRegistryPrepareInput(resume) {
		t.Fatal("real asset-bearing @2 material did not resume its exact @1 in-flight digest")
	}
	foreign := resume
	foreign.CompatibleTargetDigests = []string{strings.Repeat("f", 64)}
	if record.matchesInput(foreign) {
		t.Fatal("unrelated legacy digest matched real asset-bearing material")
	}
	authority.mu.Lock()
	calls := append([]string(nil), authority.calls...)
	authority.mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"restore:" + source.ID, "operation:" + target.ID}) {
		t.Fatalf("authority calls=%v", calls)
	}
}

func requestFenceForAssetDigestTest(request LifecycleBoundaryRequest) lifecyclePublicationFence {
	return lifecyclePublicationFence{
		OperationID: request.OperationID, Operation: extensions.LifecycleMachineUpgrade,
		StepID: "registry", Position: 1, Mode: LifecycleBoundaryActivate, Attempt: 1,
	}
}

func TestLifecycleRegistryDigestCompatibilityIsExplicitAndBounded(t *testing.T) {
	legacySource := strings.Repeat("a", 64)
	legacyTarget := strings.Repeat("b", 64)
	currentSource := strings.Repeat("c", 64)
	currentTarget := strings.Repeat("d", 64)
	fence := lifecyclePublicationFence{
		OperationID: 7, Operation: extensions.LifecycleMachineUpgrade, StepID: "registry",
		Position: 1, Mode: LifecycleBoundaryActivate, Attempt: 1,
	}
	record := lifecycleRegistryPublicationRecord{
		Fence: fence, SourceDigest: legacySource, TargetDigest: legacyTarget,
	}
	input := PrepareLifecycleRegistryPublicationInput{
		Fence: fence, SourceDigest: currentSource, TargetDigest: currentTarget,
		CompatibleSourceDigests: []string{legacySource}, CompatibleTargetDigests: []string{legacyTarget},
	}
	if !record.matchesInput(input) || !validLifecycleRegistryPrepareInput(input) {
		t.Fatal("explicit legacy plan aliases were not accepted")
	}
	input.CompatibleTargetDigests = nil
	if record.matchesInput(input) {
		t.Fatal("stored legacy target matched without its explicit alias")
	}
	secondLegacyTarget := strings.Repeat("e", 64)
	// Host allows up to nine compatible digests so Editor @10 (and earlier
	// Media @9 families) can resume any exact prior material generation.
	input.CompatibleTargetDigests = []string{legacyTarget, secondLegacyTarget}
	if !validLifecycleRegistryPrepareInput(input) {
		t.Fatal("two bounded schema-generation aliases were rejected")
	}
	for n, seed := range []string{"f", "0", "1", "2", "3", "4", "5"} {
		input.CompatibleTargetDigests = append(
			input.CompatibleTargetDigests, strings.Repeat(seed, 64),
		)
		if !validLifecycleRegistryPrepareInput(input) {
			t.Fatalf("%d bounded schema aliases were rejected", n+3)
		}
	}
	if len(input.CompatibleTargetDigests) != 9 {
		t.Fatalf("expected nine aliases before fence, got %d", len(input.CompatibleTargetDigests))
	}
	input.CompatibleTargetDigests = append(input.CompatibleTargetDigests, strings.Repeat("6", 64))
	if validLifecycleRegistryPrepareInput(input) {
		t.Fatal("more than nine compatibility aliases widened the durable fence")
	}
	input.CompatibleTargetDigests = []string{legacyTarget, legacyTarget}
	if validLifecycleRegistryPrepareInput(input) {
		t.Fatal("duplicate compatibility aliases widened the durable fence")
	}
}

func lifecycleAssetMaterial(
	t *testing.T,
	extension extensions.Extension,
	publication assetregistry.Publication,
	admitted bool,
) *lifecycleRegistryMaterial {
	t.Helper()
	material := &lifecycleRegistryMaterial{
		extension: extension, assetPublication: &publication, assetAdmitted: admitted,
	}
	if err := refreshLifecycleRegistryMaterialDigest(material); err != nil {
		t.Fatal(err)
	}
	return material
}

func mustLifecycleAssetPublication(
	t *testing.T,
	extension extensions.Extension,
	impact string,
) assetregistry.Publication {
	t.Helper()
	publication, err := extensions.BuildPublicAssetPublication(extension, impact)
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func lifecycleAssetUpgrade(
	t *testing.T,
	source extensions.Extension,
	version string,
) extensions.Extension {
	t.Helper()
	target := source
	target.Version = version
	target.Manifest.Version = version
	if err := os.WriteFile(filepath.Join(target.PackagePath, "upgrade-marker.txt"), []byte(version), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(target.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	target.PackageDigest = digest
	return target
}

func lifecycleRegistryAssetOnlyExtension(
	t *testing.T,
	version,
	seed string,
	versionID int64,
	pagePath string,
) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, seed, versionID, pagePath)
	// Keep every later family empty so the test can inject one precise service
	// failure after the Asset family has swapped.
	extension.Manifest.Events = nil
	extension.Manifest.Hooks = nil
	extension.Manifest.Providers = nil
	extension.Manifest.AdminSurfaces = nil
	extension.Manifest.Services = nil
	extension.Manifest.Routes = nil
	extension.Manifest.OpenAPI = nil
	if err := os.Remove(filepath.Join(extension.PackagePath, "theme.json")); err != nil {
		t.Fatal(err)
	}
	stylePath := "frontend/public/runtime.css"
	styleBody := []byte(".runtime-" + strings.ReplaceAll(version, ".", "-") + " {}\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(extension.PackagePath, stylePath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extension.PackagePath, stylePath), styleBody, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := bytesDigestHex(styleBody)
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: extension.ID + ".file.runtime-style", Kind: "asset", Path: stylePath, Digest: digest,
	})
	extension.Manifest.Assets = []extensions.ManifestAsset{{
		Handle: extension.ID + ".asset.runtime", ContractVersion: extension.ID + ".asset.runtime@1",
		Type: "style", Path: stylePath, Digest: digest, Loading: "blocking",
	}}
	manifest, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(extension.PackagePath, extensionmanifest.ManifestFileName), manifest, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest, err = extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	return extension
}

func lifecycleCoreAssetPublication() assetregistry.Publication {
	return assetregistry.Publication{
		Artifact: assetregistry.Artifact{
			ExtensionID: "core.assets", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("1", 64), ImpactDigest: strings.Repeat("2", 64),
			OwnerKind: assetregistry.OwnerKindCore, Core: true,
		},
		Assets: []assetregistry.Declaration{{
			Handle: "core.asset.host", ContractVersion: "sforum.asset.host@1",
			Type: "script", Path: "host.mjs", Digest: strings.Repeat("3", 64),
		}},
	}
}

func lifecycleAssetTestExtension(t *testing.T, id string, assetDependencies []string) extensions.Extension {
	t.Helper()
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(id, "card", "add", 0, "", ""),
	)
	root := extension.PackagePath
	entryPath := "frontend/public/card.mjs"
	stylePath := "frontend/public/card.css"
	entryBody := []byte("export async function mount() {}\n")
	styleBody := []byte(".demo {}\n")
	for name, body := range map[string][]byte{entryPath: entryBody, stylePath: styleBody} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entryDigest := bytesDigestHex(entryBody)
	styleDigest := bytesDigestHex(styleBody)
	extension.Status = extensions.StatusEnabled
	extension.Source = extensions.SourceUploaded
	extension.Manifest.Backend = extensions.ManifestBackend{
		Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2",
	}
	extension.Manifest.Lifecycle = &extensions.ManifestLifecycle{ContractVersion: id + ".lifecycle@1"}
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles,
		extensions.ManifestPackageFile{ID: id + ".file.card", Kind: "frontend", Path: entryPath, Digest: entryDigest},
		extensions.ManifestPackageFile{ID: id + ".file.style", Kind: "asset", Path: stylePath, Digest: styleDigest},
	)
	extension.Manifest.Assets = []extensions.ManifestAsset{{
		Handle: id + ".asset.style", ContractVersion: id + ".asset.style@1",
		Type: "style", Path: stylePath, Digest: styleDigest,
		Dependencies: append([]string(nil), assetDependencies...),
		Scope:        []string{id + ".component.card"}, Loading: "blocking",
	}}
	if len(extension.Manifest.Components) == 0 {
		t.Fatal("component fixture missing contribution")
	}
	extension.Manifest.Components[0].L2Component = id + ".file.card"
	packageDigest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest = packageDigest
	return extension
}

func bytesDigestHex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestLifecycleAssetPlanRetryingConvergesAfterConcurrentPublication(t *testing.T) {
	ctx := context.Background()
	assets := assetregistry.New()
	source := lifecycleAssetTestExtension(t, "lifecycle.assets", nil)
	target := lifecycleAssetUpgrade(t, source, "2.0.0")
	sourcePublication := mustLifecycleAssetPublication(t, source, strings.Repeat("c", 64))
	if _, err := assets.ReplaceAll([]assetregistry.Publication{sourcePublication}); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Assets: assets})
	sourceMaterial := lifecycleAssetMaterial(t, source, sourcePublication, true)
	targetPublication := mustLifecycleAssetPublication(t, target, strings.Repeat("d", 64))
	targetMaterial := lifecycleAssetMaterial(t, target, targetPublication, true)

	plan, err := boundary.prepareAssetPlan(sourceMaterial, targetMaterial)
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated owner publishes between registering (prepare) and activation
	// (apply). The exact graph for this owner is still consistent, so the
	// activation must converge instead of failing the lifecycle operation.
	unrelated := lifecycleAssetTestExtension(t, "unrelated.assets", nil)
	if _, err := assets.Publish(mustLifecycleAssetPublication(t, unrelated, strings.Repeat("f", 64))); err != nil {
		t.Fatal(err)
	}
	if err := boundary.applyAssetPlanRetrying(
		ctx, plan, LifecycleRegistryPublicationTarget, sourceMaterial, targetMaterial,
	); err != nil {
		t.Fatalf("retrying apply: %v", err)
	}
	published, ok := assets.SnapshotPublication(target.ID)
	if !ok || published.Artifact != targetPublication.Artifact {
		t.Fatalf("target publication=%#v ok=%t", published, ok)
	}
	if _, ok := assets.SnapshotPublication(unrelated.ID); !ok {
		t.Fatal("retry dropped the concurrent unrelated publication")
	}
	// Source and target share one extension id: the surviving publication must
	// be the exact target artifact, never the retired source one.
	if publication, ok := assets.SnapshotPublication(source.ID); !ok || publication.Artifact == sourcePublication.Artifact {
		t.Fatalf("retry kept the retired source artifact: %#v ok=%t", publication, ok)
	}

	// A publication that is neither source nor target still fails closed: the
	// plan is prepared while the exact target is still published, then another
	// writer replaces this owner with an unknown artifact.
	foreignPlan, err := boundary.prepareAssetPlan(sourceMaterial, targetMaterial)
	if err != nil {
		t.Fatal(err)
	}
	foreign := mustLifecycleAssetPublication(t, lifecycleAssetUpgrade(t, target, "3.0.0"), strings.Repeat("e", 64))
	if _, err := assets.PublishIfArtifact(targetPublication.Artifact, foreign); err != nil {
		t.Fatal(err)
	}
	if err := boundary.applyAssetPlanRetrying(
		ctx, foreignPlan, LifecycleRegistryPublicationTarget, sourceMaterial, targetMaterial,
	); !errors.Is(err, assetregistry.ErrArtifactConflict) {
		t.Fatalf("non-exact owner publication error=%v", err)
	}
	if publication, ok := assets.SnapshotPublication(target.ID); !ok || publication.Artifact != foreign.Artifact {
		t.Fatalf("fail-closed apply must keep the foreign artifact: %#v ok=%t", publication, ok)
	}
}

func TestLifecycleItemsWithoutDropsSuppressedExtensions(t *testing.T) {
	items := []extensions.Extension{{ID: "keep.one"}, {ID: "drop.me"}, {ID: "keep.two"}}
	remaining := lifecycleItemsWithout(items, []string{"drop.me"})
	if len(remaining) != 2 || remaining[0].ID != "keep.one" || remaining[1].ID != "keep.two" {
		t.Fatalf("remaining=%v", remaining)
	}
	if got := lifecycleItemsWithout(items, nil); len(got) != len(items) {
		t.Fatalf("nil suppression changed items: %v", got)
	}
}
