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
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestBuildLifecycleIdentityPublicationMapsCanonicalManifest(t *testing.T) {
	extension := lifecycleIdentityExtension("1.0.0", 41, "")
	extension.Manifest.PermissionDefinitions[0].RecommendedRoles = []string{"operator", "member"}
	extension.Manifest.PermissionDefinitions[0].Label = extensions.LocalizedText{
		ByLocale: map[string]string{"zh-CN": "管理资料", "en-US": "Manage profile"},
	}
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	if publication == nil || publication.Artifact.RuntimeInstanceID != "" || len(publication.Permissions) != 1 ||
		len(publication.Permissions[0].RecommendedRoles) != 2 || publication.Permissions[0].RecommendedRoles[0] != "member" ||
		publication.Permissions[0].Label != "Manage profile" || publication.Permissions[0].LabelLocales["zh-CN"] != "管理资料" ||
		publication.Identity == nil || len(publication.Identity.UserFields) != 1 {
		t.Fatalf("publication = %#v", publication)
	}

	// Returned material owns its slices and cannot be changed through Manifest aliases.
	extension.Manifest.PermissionDefinitions[0].RecommendedRoles[0] = "changed"
	extension.Manifest.PermissionDefinitions[0].Label.ByLocale["zh-CN"] = "changed"
	if publication.Permissions[0].RecommendedRoles[0] != "member" {
		t.Fatalf("publication aliases manifest: %#v", publication.Permissions[0])
	}
	if publication.Permissions[0].LabelLocales["zh-CN"] != "管理资料" {
		t.Fatalf("publication locale map aliases manifest: %#v", publication.Permissions[0])
	}
}

func TestBuildLifecycleIdentityPublicationRequiresRuntimeOnlyForExecutableIdentity(t *testing.T) {
	inert := lifecycleIdentityExtension("1.0.0", 42, "")
	inert.Manifest.Identity.Providers = []extensionmanifest.ManifestIdentityProvider{{
		ID: inert.ID + ".provider", ContractVersion: inert.ID + ".provider@1",
		Kind: "auth", Handler: "legacy.auth",
	}}
	if publication, err := buildLifecycleIdentityPublication(inert, extensions.LifecycleRuntimeBinding{}); err != nil ||
		publication == nil || publication.Artifact.RuntimeInstanceID != "" || len(publication.Identity.Providers) != 1 {
		t.Fatalf("inert publication = %#v, %v", publication, err)
	}

	executableProvider := lifecycleIdentityExtension("1.0.0", 47, "")
	executableProvider.Manifest.Identity.Providers = []extensionmanifest.ManifestIdentityProvider{{
		ID: executableProvider.ID + ".provider", ContractVersion: executableProvider.ID + ".provider@1",
		Kind: "risk", Handler: "identity.risk", Operations: []extensionmanifest.ManifestIdentityProviderOperation{{
			Name: "risk.evaluate", InputSchema: executableProvider.ID + ".risk.input@1",
			OutputSchema: executableProvider.ID + ".risk.output@1", TimeoutMS: 1000, FailurePolicy: "fail_closed",
		}},
	}}
	if _, err := buildLifecycleIdentityPublication(executableProvider, extensions.LifecycleRuntimeBinding{}); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
		t.Fatalf("operation provider without exact runtime error = %v", err)
	}
	providerBinding := extensions.LifecycleRuntimeBinding{
		ExtensionID: executableProvider.ID, ExtensionVersion: executableProvider.Version,
		PackageDigest: executableProvider.PackageDigest, VersionID: executableProvider.ActiveVersionID,
		RuntimeInstanceID: "identity-provider-runtime",
	}
	if _, err := buildLifecycleIdentityPublication(executableProvider, providerBinding); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
		t.Fatalf("operation provider without exact Schemas error = %v", err)
	}
	writeLifecycleIdentitySchema(t, &executableProvider, executableProvider.ID+".risk.input", "schemas/risk-input.json", "1", `{"type":"object"}`)
	writeLifecycleIdentitySchema(t, &executableProvider, executableProvider.ID+".risk.output", "schemas/risk-output.json", "1", `{"type":"object"}`)
	if publication, err := buildLifecycleIdentityPublication(executableProvider, providerBinding); err != nil ||
		publication == nil || publication.Artifact.RuntimeInstanceID != providerBinding.RuntimeInstanceID ||
		len(publication.Identity.Providers[0].Operations) != 1 ||
		publication.Identity.Providers[0].Operations[0].InputSchemaDigest == "" ||
		publication.Identity.Providers[0].Operations[0].OutputSchemaDigest == "" {
		t.Fatalf("operation provider publication = %#v, %v", publication, err)
	}

	executable := lifecycleIdentityExtension("1.0.0", 43, "risk")
	if _, err := buildLifecycleIdentityPublication(executable, extensions.LifecycleRuntimeBinding{}); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
		t.Fatalf("missing runtime error = %v", err)
	}
	binding := extensions.LifecycleRuntimeBinding{
		ExtensionID: executable.ID, ExtensionVersion: executable.Version,
		PackageDigest: executable.PackageDigest, VersionID: executable.ActiveVersionID,
		RuntimeInstanceID: "identity-runtime",
	}
	publication, err := buildLifecycleIdentityPublication(executable, binding)
	if err != nil || publication.Artifact.RuntimeInstanceID != binding.RuntimeInstanceID {
		t.Fatalf("executable publication = %#v, %v", publication, err)
	}
}

func TestLifecycleIdentityGraphRejectsForeignAndDriftedActiveArtifact(t *testing.T) {
	sourceExtension := lifecycleIdentityExtension("1.0.0", 44, "")
	source, err := buildLifecycleIdentityPublication(sourceExtension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(*source); err != nil {
		t.Fatal(err)
	}
	targetExtension := lifecycleIdentityExtension("2.0.0", 45, "")
	target, err := buildLifecycleIdentityPublication(targetExtension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	if graph, err := lifecycleIdentityGraph(registry.Snapshot(), sourceExtension.ID, target, source, target); err != nil || len(graph) != 1 || graph[0].Artifact != target.Artifact {
		t.Fatalf("upgrade graph = %#v, %v", graph, err)
	}
	if _, err := lifecycleIdentityGraph(registry.Snapshot(), sourceExtension.ID, target, target); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("foreign source error = %v", err)
	}
	drifted := *source
	drifted.Permissions = append([]identityregistry.PermissionDefinition(nil), source.Permissions...)
	drifted.Permissions[0].Description = "same artifact drift"
	if _, err := lifecycleIdentityGraph(registry.Snapshot(), sourceExtension.ID, target, &drifted, target); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("drift error = %v", err)
	}
}

func TestLifecycleIdentityMaterialsForPhaseOrientsPublicationAndRestore(t *testing.T) {
	sourceExtension := lifecycleIdentityExtension("1.0.0", 48, "")
	targetExtension := lifecycleIdentityExtension("2.0.0", 49, "")
	sourcePublication, err := buildLifecycleIdentityPublication(sourceExtension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	targetPublication, err := buildLifecycleIdentityPublication(targetExtension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	source := &lifecycleRegistryMaterial{extension: sourceExtension, identityPublication: sourcePublication}
	target := &lifecycleRegistryMaterial{extension: targetExtension, identityPublication: targetPublication}
	tests := []struct {
		name                    string
		phase                   LifecycleRegistryPublicationPhase
		source, target, desired *lifecycleRegistryMaterial
		wantSource, wantTarget  *lifecycleRegistryMaterial
	}{
		{name: "enable publish", phase: LifecycleRegistryPublicationTarget, target: target, desired: target, wantTarget: target},
		{name: "enable restore", phase: LifecycleRegistryPublicationSource, target: target, wantSource: target},
		{name: "upgrade publish", phase: LifecycleRegistryPublicationTarget, source: source, target: target, desired: target, wantSource: source, wantTarget: target},
		{name: "upgrade restore", phase: LifecycleRegistryPublicationSource, source: source, target: target, desired: source, wantSource: target, wantTarget: source},
		{name: "disable publish", phase: LifecycleRegistryPublicationTarget, source: source, wantSource: source},
		{name: "disable restore", phase: LifecycleRegistryPublicationSource, source: source, desired: source, wantTarget: source},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotSource, gotTarget := lifecycleIdentityMaterialsForPhase(test.phase, test.source, test.target)
			if gotSource != test.wantSource || gotTarget != test.wantTarget {
				t.Fatalf("oriented transition source=%p target=%p want source=%p target=%p", gotSource, gotTarget, test.wantSource, test.wantTarget)
			}
			if test.desired != nil && gotTarget != test.desired {
				t.Fatalf("desired publication is not the exact allowed target")
			}
			registry := identityregistry.New()
			if publication := lifecycleIdentityPublication(gotSource); publication != nil {
				if _, err := registry.Publish(*publication); err != nil {
					t.Fatal(err)
				}
			}
			store := &memoryIdentityPublicationStore{}
			boundary := &PostgresLifecycleBoundaryRegistries{identity: registry, identityStore: store, identitySet: true}
			request := LifecycleBoundaryRequest{TargetExtension: sourceExtension, ActorUserID: 41, AuditEventID: 81}
			if err := boundary.reconcileIdentity(t.Context(), request, gotSource, gotTarget, test.desired); err != nil {
				t.Fatal(err)
			}
			inputs := store.Inputs()
			if len(inputs) != 1 || !sameOptionalIdentityArtifact(inputs[0].AllowedSource, lifecycleIdentityArtifact(gotSource)) ||
				!sameOptionalIdentityArtifact(inputs[0].AllowedTarget, lifecycleIdentityArtifact(gotTarget)) {
				t.Fatalf("durable reconcile inputs = %#v", inputs)
			}
		})
	}
}

func sameOptionalIdentityArtifact(left, right *identityregistry.Artifact) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func TestLifecycleIdentityPublicationFailsClosedWithoutTransactionalStore(t *testing.T) {
	extension := lifecycleIdentityExtension("1.0.0", 50, "")
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	material := &lifecycleRegistryMaterial{extension: extension, identityPublication: publication}
	repository := &transactionalMemoryLifecycleRegistryRepository{memoryLifecycleRegistryRepository: &memoryLifecycleRegistryRepository{}}
	transaction := &postgresLifecycleRegistryTransaction{
		boundary: &PostgresLifecycleBoundaryRegistries{
			repository: repository, identityStore: &memoryIdentityPublicationStore{},
		},
		source: material,
	}
	if err := transaction.Restore(t.Context()); !errors.Is(err, ErrLifecycleRegistryPublicationUnavailable) {
		t.Fatalf("non-transactional Identity store error = %v", err)
	}
}

type transactionalMemoryLifecycleRegistryRepository struct {
	*memoryLifecycleRegistryRepository
}

func (r *transactionalMemoryLifecycleRegistryRepository) MoveLifecycleRegistryPublicationTx(
	_ context.Context,
	_ LifecycleRegistryPublicationRef,
	_ LifecycleRegistryPublicationPhase,
	apply func(pgx.Tx) error,
) error {
	return apply(nil)
}

func TestBuildLifecycleIdentityPublicationSkipsAbsentSurface(t *testing.T) {
	extension := lifecycleIdentityExtension("1.0.0", 46, "")
	extension.Manifest.Identity = nil
	extension.Manifest.PermissionDefinitions = nil
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil || publication != nil {
		t.Fatalf("publication = %#v, %v", publication, err)
	}
}

func TestLifecycleIdentityReconcileEnableUpgradeRollbackDisableAndReplay(t *testing.T) {
	sourceExtension := lifecycleIdentityExtension("1.0.0", 51, "")
	targetExtension := lifecycleIdentityExtension("2.0.0", 52, "")
	sourcePublication, err := buildLifecycleIdentityPublication(sourceExtension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	targetPublication, err := buildLifecycleIdentityPublication(targetExtension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	source := &lifecycleRegistryMaterial{extension: sourceExtension, identityPublication: sourcePublication}
	target := &lifecycleRegistryMaterial{extension: targetExtension, identityPublication: targetPublication}
	store := &memoryIdentityPublicationStore{}
	boundary := &PostgresLifecycleBoundaryRegistries{identity: identityregistry.New(), identityStore: store, identitySet: true}

	enable := LifecycleBoundaryRequest{TargetExtension: sourceExtension, ActorUserID: 41, AuditEventID: 81}
	if err := boundary.reconcileIdentity(t.Context(), enable, nil, source, source); err != nil {
		t.Fatal(err)
	}
	enabled := boundary.identity.Snapshot()
	if len(enabled.Publications) != 1 || enabled.Publications[0].Artifact != sourcePublication.Artifact {
		t.Fatalf("enabled snapshot = %#v", enabled)
	}
	if err := boundary.reconcileIdentity(t.Context(), enable, nil, source, source); err != nil {
		t.Fatal(err)
	}
	if replay := boundary.identity.Snapshot(); replay.Revision != enabled.Revision {
		t.Fatalf("exact replay revision = %d want %d", replay.Revision, enabled.Revision)
	}

	upgrade := LifecycleBoundaryRequest{TargetExtension: targetExtension, ActorUserID: 42, AuditEventID: 82}
	if err := boundary.reconcileIdentity(t.Context(), upgrade, source, target, target); err != nil {
		t.Fatal(err)
	}
	if active, found := boundary.identity.SnapshotPublication(sourceExtension.ID); !found || active.Artifact != targetPublication.Artifact {
		t.Fatalf("upgraded publication = %#v found=%t", active, found)
	}

	rollback := LifecycleBoundaryRequest{TargetExtension: sourceExtension, ActorUserID: 43, AuditEventID: 83}
	if err := boundary.reconcileIdentity(t.Context(), rollback, target, source, source); err != nil {
		t.Fatal(err)
	}
	if active, found := boundary.identity.SnapshotPublication(sourceExtension.ID); !found || active.Artifact != sourcePublication.Artifact {
		t.Fatalf("rolled back publication = %#v found=%t", active, found)
	}

	disable := LifecycleBoundaryRequest{TargetExtension: sourceExtension, ActorUserID: 44, AuditEventID: 84}
	if err := boundary.reconcileIdentity(t.Context(), disable, source, nil, nil); err != nil {
		t.Fatal(err)
	}
	disabled := boundary.identity.Snapshot()
	if len(disabled.Publications) != 0 || len(disabled.Tombstones) != 2 {
		t.Fatalf("disabled snapshot = %#v", disabled)
	}
	if calls := store.Inputs(); len(calls) != 5 || calls[2].Desired == nil || calls[3].Desired == nil || calls[4].Desired != nil {
		t.Fatalf("durable calls = %#v", calls)
	}
}

func TestLifecycleIdentityReconcileIsDurableFirst(t *testing.T) {
	extension := lifecycleIdentityExtension("1.0.0", 53, "")
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	material := &lifecycleRegistryMaterial{extension: extension, identityPublication: publication}
	durableErr := errors.New("durable identity unavailable")
	store := &memoryIdentityPublicationStore{failure: durableErr}
	registry := identityregistry.New()
	boundary := &PostgresLifecycleBoundaryRegistries{identity: registry, identityStore: store, identitySet: true}
	request := LifecycleBoundaryRequest{TargetExtension: extension, ActorUserID: 45, AuditEventID: 85}
	if err := boundary.reconcileIdentity(t.Context(), request, nil, material, material); !errors.Is(err, durableErr) {
		t.Fatalf("reconcile error = %v", err)
	}
	if snapshot := registry.Snapshot(); snapshot.Revision != 0 || len(snapshot.Publications) != 0 {
		t.Fatalf("process graph changed before durable commit: %#v", snapshot)
	}
}

func TestLifecycleIdentityRootOnlyRestartRejectsOrphanActivePublication(t *testing.T) {
	extension := lifecycleIdentityExtension("1.0.0", 58, "")
	extension.Status = extensions.StatusEnabled
	extension.Manifest.PermissionDefinitions = nil
	extension.Manifest.Identity.UserFields = nil
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil || publication == nil || publication.Identity == nil ||
		len(publication.Permissions) != 0 || len(publication.Identity.UserFields) != 0 {
		t.Fatalf("root-only publication=%#v err=%v", publication, err)
	}
	material := &lifecycleRegistryMaterial{extension: extension, identityPublication: publication}
	store := &memoryIdentityPublicationStore{}
	registry := identityregistry.New()
	boundary := &PostgresLifecycleBoundaryRegistries{
		identity: registry, identityStore: store, identitySet: true,
	}
	request := LifecycleBoundaryRequest{TargetExtension: extension, ActorUserID: 48, AuditEventID: 88}
	if err := boundary.reconcileIdentity(t.Context(), request, nil, material, material); err != nil {
		t.Fatal(err)
	}
	if snapshot := registry.Snapshot(); len(snapshot.Publications) != 1 || len(snapshot.Tombstones) != 0 {
		t.Fatalf("root-only enabled snapshot=%#v", snapshot)
	}

	restarted := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: store, identitySet: true,
	}
	if err := restarted.restoreIdentityPublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	if snapshot := restarted.identity.Snapshot(); len(snapshot.Publications) != 1 {
		t.Fatalf("root-only restart snapshot=%#v", snapshot)
	}

	orphan := extension
	orphan.Status = extensions.StatusDisabled
	if err := (&PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: store, identitySet: true,
	}).restoreIdentityPublications(t.Context(), []extensions.Extension{orphan}, false); !errors.Is(
		err, ErrLifecycleRegistryPublicationConflict,
	) {
		t.Fatalf("orphan active root restart error=%v", err)
	}

	if err := boundary.reconcileIdentity(t.Context(), request, material, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := restarted.restoreIdentityPublications(t.Context(), []extensions.Extension{orphan}, false); err != nil {
		t.Fatalf("retired root-only restart: %v", err)
	}
	if snapshot := restarted.identity.Snapshot(); len(snapshot.Publications) != 0 {
		t.Fatalf("retired root-only snapshot=%#v", snapshot)
	}
}

// Mirrors the normal-dev DB startup blocker: an enabled permission-only fixture
// (sforum.admin-surface-reference) with empty identity registry ledgers.
// Stores without LegacyPublicationAdopter never adopt; the owner is suppressed
// for this boot instead of taking the whole Host down.
func TestLifecycleIdentityRestoreSuppressesPermissionOnlyAdminSurfaceWithoutDurable(t *testing.T) {
	extension := adminSurfaceReferencePermissionOnlyExtension(5866)
	extension.Status = extensions.StatusEnabled
	store := &memoryIdentityPublicationStore{}
	boundary := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: store, identitySet: true,
	}
	if err := boundary.restoreIdentityPublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("missing durable restore error=%v", err)
	}
	if suppressed := boundary.SuppressedIdentityExtensions(); len(suppressed) != 1 ||
		suppressed[0] != "sforum.admin-surface-reference" {
		t.Fatalf("suppressed extensions=%v", suppressed)
	}
	if snapshot := boundary.identity.Snapshot(); snapshot.Revision != 0 || len(snapshot.Publications) != 0 {
		t.Fatalf("suppressed restore must not publish process graph: %#v", snapshot)
	}

	// Allowed path: reconcile first (as lifecycle enable does), then restart restore.
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil || publication == nil || publication.Identity != nil || len(publication.Permissions) != 1 {
		t.Fatalf("permission-only publication=%#v err=%v", publication, err)
	}
	material := &lifecycleRegistryMaterial{extension: extension, identityPublication: publication}
	request := LifecycleBoundaryRequest{TargetExtension: extension, ActorUserID: 49, AuditEventID: 89}
	if err := boundary.reconcileIdentity(t.Context(), request, nil, material, material); err != nil {
		t.Fatal(err)
	}
	restarted := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: store, identitySet: true,
	}
	if err := restarted.restoreIdentityPublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("permission-only restart after reconcile: %v", err)
	}
	if snapshot := restarted.identity.Snapshot(); len(snapshot.Publications) != 1 ||
		snapshot.Publications[0].Artifact.ExtensionID != extension.ID ||
		len(snapshot.Publications[0].Permissions) != 1 {
		t.Fatalf("permission-only restart snapshot=%#v", snapshot)
	}
}

func TestLifecycleIdentityRestoreAdoptsLegacyOnErrNotFoundOnly(t *testing.T) {
	extension := adminSurfaceReferencePermissionOnlyExtension(5866)
	extension.Status = extensions.StatusEnabled
	store := &memoryLegacyAdoptingIdentityStore{}
	boundary := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: store, identitySet: true,
	}
	if err := boundary.restoreIdentityPublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("legacy adopt restore: %v", err)
	}
	if store.adoptCalls != 1 || store.lastBatch != 1 {
		t.Fatalf("adopt calls=%d batch=%d want calls=1 batch=1", store.adoptCalls, store.lastBatch)
	}
	if snapshot := boundary.identity.Snapshot(); len(snapshot.Publications) != 1 ||
		snapshot.Publications[0].Artifact.ExtensionID != extension.ID {
		t.Fatalf("adopted process graph=%#v", snapshot)
	}

	// Safe Mode must never adopt third-party publications.
	safeStore := &memoryLegacyAdoptingIdentityStore{}
	safeBoundary := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: safeStore, identitySet: true,
	}
	if err := safeBoundary.restoreIdentityPublications(t.Context(), []extensions.Extension{extension}, true); err != nil {
		t.Fatalf("safe mode restore: %v", err)
	}
	if safeStore.adoptCalls != 0 {
		t.Fatalf("safe mode must not adopt: calls=%d", safeStore.adoptCalls)
	}
	if snapshot := safeBoundary.identity.Snapshot(); len(snapshot.Publications) != 0 {
		t.Fatalf("safe mode process graph=%#v", snapshot)
	}

	// Non-ErrNotFound validation failures must not invoke the adopter.
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	foreign := *publication
	foreign.Artifact.PackageDigest = strings.Repeat("f", 64)
	conflictRoot, err := identityregistryDesiredRootForTest(t, foreign)
	if err != nil {
		t.Fatal(err)
	}
	conflictStore := &memoryLegacyAdoptingIdentityStore{
		memoryIdentityPublicationStore: memoryIdentityPublicationStore{
			state: identityregistry.DurableState{RootTips: []identityregistry.DurableRootPublicationTip{conflictRoot}},
		},
	}
	conflictBoundary := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: conflictStore, identitySet: true,
	}
	if err := conflictBoundary.restoreIdentityPublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("artifact conflict restore error=%v", err)
	}
	if conflictStore.adoptCalls != 0 {
		t.Fatalf("adopter must not run on ErrArtifactConflict: calls=%d", conflictStore.adoptCalls)
	}
	if suppressed := conflictBoundary.SuppressedIdentityExtensions(); len(suppressed) != 1 || suppressed[0] != extension.ID {
		t.Fatalf("artifact conflict suppressed=%v", suppressed)
	}
	if snapshot := conflictBoundary.identity.Snapshot(); len(snapshot.Publications) != 0 {
		t.Fatalf("artifact conflict must not publish: %#v", snapshot)
	}
}

func TestLifecycleIdentityRestoreAdoptsLegacyBatchOnce(t *testing.T) {
	first := adminSurfaceReferencePermissionOnlyExtension(5866)
	first.Status = extensions.StatusEnabled
	second := adminSurfaceReferencePermissionOnlyExtension(5867)
	second.ID = "sforum.legacy-batch-second"
	second.Manifest.ID = second.ID
	second.ActiveVersionID = 5867
	second.PackageDigest = "91b964f80707b257f6f401faffb07fe0f0a6aa6b5833a6fab0cedaab77b3324f"
	second.Status = extensions.StatusEnabled
	second.Manifest.PermissionDefinitions = []extensions.ManifestPermissionDefinition{{
		Key: second.ID + ".manage", ContractVersion: second.ID + ".permission.manage@1",
		Label: extensions.LocalizedText{Default: "Second"}, Description: extensions.LocalizedText{Default: "Second"}, RecommendedRoles: []string{"administrator"},
		AssignmentPolicy: "host",
	}}

	store := &memoryLegacyAdoptingIdentityStore{}
	boundary := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: store, identitySet: true,
	}
	if err := boundary.restoreIdentityPublications(t.Context(), []extensions.Extension{second, first}, false); err != nil {
		t.Fatalf("batch legacy adopt restore: %v", err)
	}
	if store.adoptCalls != 1 || store.lastBatch != 2 {
		t.Fatalf("adopt calls=%d batch=%d want calls=1 batch=2", store.adoptCalls, store.lastBatch)
	}
	if snapshot := boundary.identity.Snapshot(); len(snapshot.Publications) != 2 {
		t.Fatalf("batch process graph=%#v", snapshot)
	}

	// Stores without adopter stay fail-closed with zero process graph.
	noAdopter := &memoryIdentityPublicationStore{}
	noBoundary := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: noAdopter, identitySet: true,
	}
	if err := noBoundary.restoreIdentityPublications(t.Context(), []extensions.Extension{first}, false); err != nil {
		t.Fatalf("no adopter error=%v", err)
	}
	if suppressed := noBoundary.SuppressedIdentityExtensions(); len(suppressed) != 1 || suppressed[0] != first.ID {
		t.Fatalf("no adopter suppressed=%v", suppressed)
	}
	if snapshot := noBoundary.identity.Snapshot(); len(snapshot.Publications) != 0 {
		t.Fatalf("no adopter must not publish: %#v", snapshot)
	}
}

type memoryLegacyAdoptingIdentityStore struct {
	memoryIdentityPublicationStore
	adoptCalls int
	adoptErr   error
	lastBatch  int
}

func (s *memoryLegacyAdoptingIdentityStore) AdoptLegacyPublications(
	_ context.Context,
	publications []identityregistry.Publication,
) (identityregistry.DurableState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adoptCalls++
	s.lastBatch = len(publications)
	if s.adoptErr != nil {
		return identityregistry.DurableState{}, s.adoptErr
	}
	if s.failure != nil {
		return identityregistry.DurableState{}, s.failure
	}
	for _, publication := range publications {
		s.state = reconcileMemoryIdentityState(s.state, identityregistry.ReconcilePublicationInput{
			ExtensionID: publication.Artifact.ExtensionID, AllowedTarget: &publication.Artifact,
			Desired: &publication, ActorUserID: 549, AuditEventID: 962,
		})
	}
	return cloneMemoryIdentityState(s.state), nil
}

func identityregistryDesiredRootForTest(
	t *testing.T,
	publication identityregistry.Publication,
) (identityregistry.DurableRootPublicationTip, error) {
	t.Helper()
	state := reconcileMemoryIdentityState(identityregistry.DurableState{}, identityregistry.ReconcilePublicationInput{
		ExtensionID: publication.Artifact.ExtensionID, AllowedTarget: &publication.Artifact,
		Desired: &publication, ActorUserID: 1, AuditEventID: 1,
	})
	if len(state.RootTips) != 1 {
		return identityregistry.DurableRootPublicationTip{}, fmt.Errorf("root tips=%d", len(state.RootTips))
	}
	return state.RootTips[0], nil
}

var _ identityregistry.LegacyPublicationAdopter = (*memoryLegacyAdoptingIdentityStore)(nil)

func adminSurfaceReferencePermissionOnlyExtension(versionID int64) extensions.Extension {
	const id = "sforum.admin-surface-reference"
	extension := extensions.Extension{
		ID: id, Type: extensions.TypePlugin, Version: "1.0.0", ActiveVersionID: versionID,
		PackageDigest: "81b964f80707b257f6f401faffb07fe0f0a6aa6b5833a6fab0cedaab77b3324f",
	}
	extension.Manifest = extensions.Manifest{ID: id, Type: extensions.TypePlugin, Version: "1.0.0"}
	extension.Manifest.PermissionDefinitions = []extensions.ManifestPermissionDefinition{{
		Key: id + ".manage", ContractVersion: id + ".permission.manage@1",
		Label:            extensions.LocalizedText{Default: "Use admin surface reference"},
		Description:      extensions.LocalizedText{Default: "View and invoke the reference plugin's admin surfaces."},
		RecommendedRoles: []string{"administrator"}, AssignmentPolicy: "host",
	}}
	return extension
}

func TestLifecycleIdentityRestartSafeModeAndConditionalDependencies(t *testing.T) {
	withoutIdentity := lifecycleIdentityExtension("1.0.0", 54, "")
	withoutIdentity.Manifest.Identity = nil
	withoutIdentity.Manifest.PermissionDefinitions = nil
	if err := (*PostgresLifecycleBoundaryRegistries)(nil).restoreIdentityPublications(
		t.Context(), []extensions.Extension{withoutIdentity}, false,
	); err != nil {
		t.Fatalf("no-declaration restore required dependencies: %v", err)
	}
	if err := (&PostgresLifecycleBoundaryRegistries{identitySet: true, identity: identityregistry.New()}).
		restoreIdentityPublications(t.Context(), nil, false); !errors.Is(err, ErrLifecycleRegistryPublicationUnavailable) {
		t.Fatalf("explicit partial dependency error = %v", err)
	}

	extension := lifecycleIdentityExtension("1.0.0", 55, "")
	extension.Status = extensions.StatusEnabled
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryIdentityPublicationStore{}
	if _, err := store.Reconcile(t.Context(), identityregistry.ReconcilePublicationInput{
		ExtensionID: extension.ID, AllowedTarget: &publication.Artifact, Desired: publication,
		ActorUserID: 46, AuditEventID: 86,
	}); err != nil {
		t.Fatal(err)
	}
	registry := identityregistry.New()
	coreArtifact, err := identityregistry.NewCoreArtifact("core.identity", "1.0.0", strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	core := identityregistry.Publication{Artifact: coreArtifact, Permissions: []identityregistry.PermissionDefinition{{
		Key: "core.identity.access", ContractVersion: "core.identity.access@1",
		Label: "Access", Description: "Core identity access.", AssignmentPolicy: "host",
	}}}
	if _, err := registry.Publish(core); err != nil {
		t.Fatal(err)
	}
	boundary := &PostgresLifecycleBoundaryRegistries{
		identity: registry, identityStore: store, identitySet: true,
	}
	if err := boundary.restoreIdentityPublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	if snapshot := registry.Snapshot(); snapshot.SafeMode || len(snapshot.Publications) != 2 {
		t.Fatalf("restart snapshot = %#v", snapshot)
	}
	if err := boundary.restoreIdentityPublications(t.Context(), []extensions.Extension{extension}, true); err != nil {
		t.Fatal(err)
	}
	safe := registry.Snapshot()
	if !safe.SafeMode || len(safe.Publications) != 1 || !safe.Publications[0].Artifact.Core || len(safe.Tombstones) != 2 {
		t.Fatalf("safe mode snapshot = %#v", safe)
	}
	if err := boundary.restoreIdentityPublications(t.Context(), []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	if restored := registry.Snapshot(); restored.SafeMode || len(restored.Publications) != 2 || len(restored.Tombstones) != 2 {
		t.Fatalf("restored snapshot = %#v", restored)
	}
	if _, err := store.Reconcile(t.Context(), identityregistry.ReconcilePublicationInput{
		ExtensionID: extension.ID, AllowedSource: &publication.Artifact,
		ActorUserID: 47, AuditEventID: 87,
	}); err != nil {
		t.Fatal(err)
	}
	staleBoundary := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: store, identitySet: true,
	}
	if err := staleBoundary.restoreIdentityPublications(
		t.Context(), []extensions.Extension{extension}, false,
	); err != nil {
		t.Fatalf("tombstoned restart error = %v", err)
	}
	if suppressed := staleBoundary.SuppressedIdentityExtensions(); len(suppressed) != 1 || suppressed[0] != extension.ID {
		t.Fatalf("tombstoned restart suppressed=%v", suppressed)
	}
	if snapshot := staleBoundary.identity.Snapshot(); len(snapshot.Publications) != 0 {
		t.Fatalf("tombstoned restart must not publish: %#v", snapshot)
	}
	recovery := &memoryLifecycleIdentityStartupRecovery{store: store}
	recoveredBoundary := &PostgresLifecycleBoundaryRegistries{
		identity: identityregistry.New(), identityStore: store, identitySet: true,
		identityRecovery: recovery,
	}
	if err := recoveredBoundary.restoreIdentityPublications(
		t.Context(), []extensions.Extension{extension}, false,
	); err != nil {
		t.Fatalf("recover exact tombstoned publication: %v", err)
	}
	if recovery.calls != 1 {
		t.Fatalf("startup recovery calls=%d want=1", recovery.calls)
	}
	if snapshot := recoveredBoundary.identity.Snapshot(); len(snapshot.Publications) != 1 ||
		snapshot.Publications[0].Artifact.ExtensionID != extension.ID {
		t.Fatalf("recovered identity snapshot=%#v", snapshot)
	}
}

func TestLegacyRuntimeIdentityPublicationPublishesAndRetiresExecutableProvider(t *testing.T) {
	ctx := t.Context()
	extension := lifecycleIdentityExtension("1.0.0", 61, "")
	extension.Status = extensions.StatusEnabled
	providerID := extension.ID + ".auth"
	extension.Manifest.Identity.Providers = []extensionmanifest.ManifestIdentityProvider{{
		ID: providerID, ContractVersion: providerID + "@1",
		Kind: "auth", Handler: extension.ID + ".auth",
		Operations: []extensionmanifest.ManifestIdentityProviderOperation{{
			Name: "login.start", InputSchema: extension.ID + ".auth.start.input@1",
			OutputSchema: extension.ID + ".auth.start.output@1", TimeoutMS: 1000,
			FailurePolicy: "fail_closed",
		}},
	}}
	writeLifecycleIdentitySchema(
		t, &extension, extension.ID+".auth.start.input", "schemas/auth-start-input.json",
		"1", `{"type":"object"}`,
	)
	writeLifecycleIdentitySchema(
		t, &extension, extension.ID+".auth.start.output", "schemas/auth-start-output.json",
		"1", `{"type":"object"}`,
	)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	registry := identityregistry.New()
	store := &memoryIdentityPublicationStore{}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Identity: registry, IdentityStore: store,
	})

	published, err := boundary.PublishRuntimeIdentity(ctx, extension, 101, 201)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := registry.ResolveProvider(providerID)
	if err != nil || provider.Artifact.RuntimeInstanceID == "" ||
		provider.Artifact.PackageDigest != extension.PackageDigest {
		t.Fatalf("published provider = %#v, %v", provider, err)
	}
	inputs := store.Inputs()
	if len(inputs) != 1 || inputs[0].AllowedTarget == nil || inputs[0].AllowedTarget.RuntimeInstanceID == "" ||
		inputs[0].ActorUserID != 101 || inputs[0].AuditEventID != 201 {
		t.Fatalf("publish reconcile inputs = %#v", inputs)
	}

	retired, err := boundary.QuarantineRuntimeIdentity(ctx, extension, 102, 202)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveProvider(providerID); !errors.Is(err, identityregistry.ErrNotFound) {
		t.Fatalf("retired provider error = %v", err)
	}
	if err := retired.Rollback(); err != nil {
		t.Fatal(err)
	}
	if provider, err := registry.ResolveProvider(providerID); err != nil ||
		provider.Artifact.RuntimeInstanceID == "" {
		t.Fatalf("rollback restored provider = %#v, %v", provider, err)
	}
	if err := published.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveProvider(providerID); !errors.Is(err, identityregistry.ErrNotFound) {
		t.Fatalf("publish rollback provider error = %v", err)
	}
}

func TestRestoreLegacyRuntimeIdentityDurableOrientsCompensation(t *testing.T) {
	extension := lifecycleIdentityExtension("1.0.0", 62, "")
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	material := &lifecycleRegistryMaterial{extension: extension, identityPublication: publication}
	store := &memoryIdentityPublicationStore{}
	boundary := &PostgresLifecycleBoundaryRegistries{identityStore: store}
	request := LifecycleBoundaryRequest{TargetExtension: extension, ActorUserID: 103, AuditEventID: 203}

	if err := boundary.restoreLegacyRuntimeIdentityDurable(
		t.Context(), request, material, nil, nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := boundary.restoreLegacyRuntimeIdentityDurable(
		t.Context(), request, nil, material, material,
	); err != nil {
		t.Fatal(err)
	}
	inputs := store.Inputs()
	if len(inputs) != 2 || inputs[0].AllowedSource != nil || inputs[0].AllowedTarget == nil ||
		inputs[0].Desired == nil || inputs[0].Desired.Artifact != *inputs[0].AllowedTarget {
		t.Fatalf("retire compensation input = %#v", inputs)
	}
	if inputs[1].AllowedSource == nil || inputs[1].AllowedTarget != nil || inputs[1].Desired != nil ||
		*inputs[1].AllowedSource != material.identityPublication.Artifact {
		t.Fatalf("publish compensation input = %#v", inputs)
	}
}

func TestLifecycleIdentityDigestIsAdditiveAndAbsentSurfaceIsByteCompatible(t *testing.T) {
	withoutIdentity := lifecycleIdentityExtension("1.0.0", 56, "")
	withoutIdentity.Manifest.Identity = nil
	withoutIdentity.Manifest.PermissionDefinitions = nil
	material := &lifecycleRegistryMaterial{extension: withoutIdentity}
	legacy, err := encodeLifecycleRegistryMaterialDigest(material, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshLifecycleRegistryMaterialDigest(material); err != nil {
		t.Fatal(err)
	}
	if material.digest != legacy || material.legacyDigest != "" || len(material.compatibleDigests) != 0 {
		t.Fatalf("absent Identity changed digest: %#v", material)
	}

	withIdentity := lifecycleIdentityExtension("1.0.0", 57, "")
	publication, err := buildLifecycleIdentityPublication(withIdentity, extensions.LifecycleRuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	identityMaterial := &lifecycleRegistryMaterial{extension: withIdentity, identityPublication: publication}
	prior, err := encodeLifecycleRegistryMaterialDigest(identityMaterial, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshLifecycleRegistryMaterialDigest(identityMaterial); err != nil {
		t.Fatal(err)
	}
	wantV6, err := encodeLifecycleRegistryMaterialDigestV6(identityMaterial)
	if err != nil {
		t.Fatal(err)
	}
	if identityMaterial.digest != wantV6 || identityMaterial.digest == prior ||
		!reflect.DeepEqual(identityMaterial.compatibleDigests, []string{prior}) {
		t.Fatalf("Identity digest = %#v", identityMaterial)
	}
}

type memoryIdentityPublicationStore struct {
	mu      sync.Mutex
	state   identityregistry.DurableState
	inputs  []identityregistry.ReconcilePublicationInput
	failure error
}

type memoryLifecycleIdentityStartupRecovery struct {
	store *memoryIdentityPublicationStore
	calls int
}

func (r *memoryLifecycleIdentityStartupRecovery) RecoverStaleIdentityPublication(
	ctx context.Context,
	publication identityregistry.Publication,
) (identityregistry.DurableState, bool, error) {
	r.calls++
	state, err := r.store.Reconcile(ctx, identityregistry.ReconcilePublicationInput{
		ExtensionID: publication.Artifact.ExtensionID, AllowedSource: &publication.Artifact,
		AllowedTarget: &publication.Artifact, Desired: &publication,
		ActorUserID: 48, AuditEventID: 88,
	})
	return state, err == nil, err
}

func (s *memoryIdentityPublicationStore) Reconcile(
	_ context.Context,
	input identityregistry.ReconcilePublicationInput,
) (identityregistry.DurableState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, input)
	if input.Desired != nil && (input.AllowedTarget == nil || input.Desired.Artifact != *input.AllowedTarget) {
		return identityregistry.DurableState{}, identityregistry.ErrInvalid
	}
	if s.failure != nil {
		return identityregistry.DurableState{}, s.failure
	}
	s.state = reconcileMemoryIdentityState(s.state, input)
	return cloneMemoryIdentityState(s.state), nil
}

func (s *memoryIdentityPublicationStore) LoadDurableState(context.Context) (identityregistry.DurableState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return identityregistry.DurableState{}, s.failure
	}
	return cloneMemoryIdentityState(s.state), nil
}

func (s *memoryIdentityPublicationStore) Inputs() []identityregistry.ReconcilePublicationInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]identityregistry.ReconcilePublicationInput(nil), s.inputs...)
}

func reconcileMemoryIdentityState(
	state identityregistry.DurableState,
	input identityregistry.ReconcilePublicationInput,
) identityregistry.DurableState {
	desired := memoryIdentityDeclarations(input.Desired)
	owners := make(map[string]identityregistry.DurableOwner, len(state.Owners)+len(desired))
	tips := make(map[string]identityregistry.DurableDeclarationTip, len(state.Tips)+len(desired))
	for _, owner := range state.Owners {
		owners[owner.IdentityKind+"\x00"+owner.StableID] = owner
	}
	for _, tip := range state.Tips {
		tips[tip.IdentityKind+"\x00"+tip.StableID] = tip
	}
	desiredKeys := make(map[string]bool, len(desired))
	for _, declaration := range desired {
		key := declaration.IdentityKind + "\x00" + declaration.StableID
		desiredKeys[key] = true
		owners[key] = identityregistry.DurableOwner{
			IdentityKind: declaration.IdentityKind, StableID: declaration.StableID,
			OwnerExtensionID: input.ExtensionID,
		}
		if current, found := tips[key]; found {
			declaration.Revision = current.Revision
			if current.RegistryState != identityregistry.RegistryStateActive ||
				current.ExtensionVersionID != declaration.ExtensionVersionID ||
				current.PackageDigest != declaration.PackageDigest {
				declaration.Revision++
			}
		}
		tips[key] = declaration
	}
	for key, tip := range tips {
		if tip.OwnerExtensionID != input.ExtensionID || desiredKeys[key] || tip.RegistryState != identityregistry.RegistryStateActive {
			continue
		}
		tip.RegistryState = identityregistry.RegistryStateTombstone
		tip.Revision++
		tips[key] = tip
	}
	rootTips := append([]identityregistry.DurableRootPublicationTip(nil), state.RootTips...)
	var currentRoot *identityregistry.DurableRootPublicationTip
	for index := range rootTips {
		if rootTips[index].OwnerExtensionID != input.ExtensionID ||
			currentRoot != nil && rootTips[index].Revision <= currentRoot.Revision {
			continue
		}
		candidate := rootTips[index]
		currentRoot = &candidate
	}
	if input.Desired == nil {
		if currentRoot != nil && currentRoot.RegistryState == identityregistry.RegistryStateActive {
			tombstone := *currentRoot
			tombstone.Revision++
			tombstone.RegistryState = identityregistry.RegistryStateTombstone
			tombstone.ActorUserID = input.ActorUserID
			tombstone.AuditEventID = input.AuditEventID
			rootTips = append(rootTips, tombstone)
		}
	} else {
		desiredRoot := memoryIdentityRootTip(*input.Desired, input.ActorUserID, input.AuditEventID)
		if currentRoot == nil {
			rootTips = append(rootTips, desiredRoot)
		} else if currentRoot.RegistryState != identityregistry.RegistryStateActive ||
			currentRoot.ExtensionVersionID != desiredRoot.ExtensionVersionID ||
			currentRoot.PackageDigest != desiredRoot.PackageDigest {
			desiredRoot.Revision = currentRoot.Revision + 1
			rootTips = append(rootTips, desiredRoot)
		}
	}
	result := identityregistry.DurableState{
		Owners: make([]identityregistry.DurableOwner, 0, len(owners)),
		Tips:   make([]identityregistry.DurableDeclarationTip, 0, len(tips)),
	}
	for _, owner := range owners {
		result.Owners = append(result.Owners, owner)
	}
	for _, tip := range tips {
		result.Tips = append(result.Tips, tip)
	}
	// Keep the latest root tip per owner so multi-plugin batch adoption does not
	// drop sibling history when reconciling one extension at a time.
	latestByOwner := make(map[string]identityregistry.DurableRootPublicationTip, len(rootTips))
	for _, tip := range rootTips {
		current, found := latestByOwner[tip.OwnerExtensionID]
		if !found || tip.Revision > current.Revision {
			latestByOwner[tip.OwnerExtensionID] = tip
		}
	}
	for _, tip := range latestByOwner {
		result.RootTips = append(result.RootTips, tip)
	}
	return result
}

func memoryIdentityRootTip(
	publication identityregistry.Publication,
	actorUserID, auditEventID int64,
) identityregistry.DurableRootPublicationTip {
	publication.Artifact.RuntimeInstanceID = ""
	raw, _ := json.Marshal(publication)
	sum := sha256.Sum256(append([]byte("sforum.identity-registry.root-publication@1\x00"), raw...))
	return identityregistry.DurableRootPublicationTip{
		OwnerExtensionID: publication.Artifact.ExtensionID,
		Revision:         1, RegistryState: identityregistry.RegistryStateActive,
		ExtensionVersionID: publication.Artifact.VersionID,
		ExtensionVersion:   publication.Artifact.ExtensionVersion,
		PackageDigest:      publication.Artifact.PackageDigest,
		SchemaVersion:      identityregistry.SchemaVersion,
		PublicationDigest:  hex.EncodeToString(sum[:]), PublicationJSON: raw,
		ActorUserID: actorUserID, AuditEventID: auditEventID,
	}
}

func memoryIdentityDeclarations(publication *identityregistry.Publication) []identityregistry.DurableDeclarationTip {
	if publication == nil {
		return nil
	}
	artifact := publication.Artifact
	makeTip := func(kind, id, contract, digest string) identityregistry.DurableDeclarationTip {
		return identityregistry.DurableDeclarationTip{
			IdentityKind: kind, StableID: id, OwnerExtensionID: artifact.ExtensionID,
			Revision: 1, RegistryState: identityregistry.RegistryStateActive,
			ExtensionVersionID: artifact.VersionID, ExtensionVersion: artifact.ExtensionVersion,
			PackageDigest: artifact.PackageDigest, ContractVersion: contract,
			DeclarationDigest: digest,
		}
	}
	result := make([]identityregistry.DurableDeclarationTip, 0, len(publication.Permissions))
	for _, permission := range publication.Permissions {
		result = append(result, makeTip(
			identityregistry.TombstoneKindPermission, permission.Key, permission.ContractVersion,
			memoryIdentityDeclarationDigest(identityregistry.TombstoneKindPermission, permission),
		))
	}
	if publication.Identity != nil {
		for _, field := range publication.Identity.UserFields {
			result = append(result, makeTip(
				identityregistry.TombstoneKindUserField, field.ID, field.ContractVersion,
				memoryIdentityDeclarationDigest(identityregistry.TombstoneKindUserField, field),
			))
		}
		for _, provider := range publication.Identity.Providers {
			result = append(result, makeTip(
				identityregistry.TombstoneKindProvider, provider.ID, provider.ContractVersion,
				memoryIdentityDeclarationDigest(identityregistry.TombstoneKindProvider, provider),
			))
		}
	}
	return result
}

func memoryIdentityDeclarationDigest(kind string, declaration any) string {
	raw, _ := json.Marshal(declaration)
	sum := sha256.Sum256(append([]byte(kind+"\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

func cloneMemoryIdentityState(input identityregistry.DurableState) identityregistry.DurableState {
	result := identityregistry.DurableState{
		Owners:   append([]identityregistry.DurableOwner(nil), input.Owners...),
		Tips:     append([]identityregistry.DurableDeclarationTip(nil), input.Tips...),
		RootTips: append([]identityregistry.DurableRootPublicationTip(nil), input.RootTips...),
	}
	for index := range result.RootTips {
		result.RootTips[index].PublicationJSON = append([]byte(nil), result.RootTips[index].PublicationJSON...)
	}
	return result
}

var _ identityregistry.PublicationStore = (*memoryIdentityPublicationStore)(nil)

func lifecycleIdentityExtension(version string, versionID int64, executable string) extensions.Extension {
	id := "fixture.identity.lifecycle"
	extension := extensions.Extension{
		ID: id, Type: extensions.TypePlugin, Version: version, ActiveVersionID: versionID,
		PackageDigest: strings.Repeat(string(rune('a'+versionID%6)), 64),
	}
	extension.Manifest = extensions.Manifest{ID: id, Type: extensions.TypePlugin, Version: version}
	extension.Manifest.PermissionDefinitions = []extensions.ManifestPermissionDefinition{{
		Key: id + ".profile", ContractVersion: id + ".profile@1", Label: extensions.LocalizedText{Default: "Profile"},
		Description: extensions.LocalizedText{Default: "Manage plugin profiles."}, AssignmentPolicy: "host",
	}}
	extension.Manifest.Identity = &extensions.ManifestIdentity{
		ContractVersion: id + ".contract@1", SessionPolicy: "core.session.default",
		UserFields: []extensionmanifest.ManifestIdentityUserField{{
			ID: id + ".field.code", ContractVersion: id + ".field.code@1",
			Type: "string", Schema: id + ".field.code.schema@1", ReadPermission: id + ".profile",
		}},
	}
	if executable == "risk" {
		extension.Manifest.Identity.RiskHooks = []string{id + ".risk.login"}
	}
	return extension
}

func writeLifecycleIdentitySchema(
	t *testing.T,
	extension *extensions.Extension,
	id, path, version, schema string,
) {
	t.Helper()
	if extension.PackagePath == "" {
		extension.PackagePath = t.TempDir()
	}
	target := filepath.Join(extension.PackagePath, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(schema)
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: id, Kind: "schema", Path: path, Digest: hex.EncodeToString(digest[:]), Version: version,
	})
}
