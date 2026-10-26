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
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestLifecycleContentMaterialFreezesExactAuthorityAndReachableDigestAliases(t *testing.T) {
	extension := lifecycleContentTestExtension(t, "1.0.0", strings.Repeat("a", 64), 401)
	material, err := buildLifecycleRegistryMaterial(extension, lifecycleRegistryBinding(extension, "content-digest-runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if material.contentPublication != nil {
		t.Fatal("content publication was built before exact lifecycle authority was frozen")
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Content: contentregistry.New()})
	if err := boundary.freezeContentMaterial(&material); err != nil {
		t.Fatal(err)
	}
	publication := material.contentPublication
	if publication == nil || publication.Artifact.ExtensionID != extension.ID ||
		publication.Artifact.ExtensionVersion != extension.Version ||
		publication.Artifact.PackageDigest != extension.PackageDigest ||
		publication.Artifact.VersionID != extension.ActiveVersionID ||
		publication.Artifact.RuntimeInstanceID != "content-digest-runtime" || len(publication.Content) != 1 ||
		publication.Content[0].Kind != contentregistry.KindBlock {
		t.Fatalf("frozen content publication = %#v", publication)
	}
	v1Digest, err := encodeLifecycleRegistryMaterialDigest(&material, false, false)
	if err != nil {
		t.Fatal(err)
	}
	v8Digest, err := encodeLifecycleRegistryMaterialDigestV8(&material)
	if err != nil {
		t.Fatal(err)
	}
	if material.digest != v8Digest || material.legacyDigest != v1Digest ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest}) || v8Digest == v1Digest {
		t.Fatalf("content material digests primary=%s legacy=%s aliases=%v", material.digest, material.legacyDigest,
			registryMaterialCompatibleDigests(&material))
	}

	extension.Manifest.Content[0].Handler = extension.ID + ".content.mutated"
	if publication.Content[0].Handler != extension.ID+".content.block.card" {
		t.Fatalf("Manifest mutation changed frozen content publication = %#v", publication.Content[0])
	}
	before := material.digest
	material.contentPublication.Content[0].Schema = extension.ID + ".content.schema.mutated@1"
	if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
		t.Fatal(err)
	}
	if material.digest == before ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest}) {
		t.Fatalf("content declaration drift digest=%s aliases=%v", material.digest, registryMaterialCompatibleDigests(&material))
	}
}

func TestLifecycleContentMaterialsUseSourceRestoreAndTargetOperationAuthority(t *testing.T) {
	source := lifecycleContentTestExtension(t, "1.0.0", strings.Repeat("c", 64), 402)
	target := lifecycleContentTestExtension(t, "2.0.0", strings.Repeat("d", 64), 403)
	sourceBinding := lifecycleRegistryBinding(source, "content-authority-source")
	targetBinding := lifecycleRegistryBinding(target, "content-authority-target")
	sourceMaterial, err := buildLifecycleRegistryMaterial(source, sourceBinding)
	if err != nil {
		t.Fatal(err)
	}
	targetMaterial, err := buildLifecycleRegistryMaterial(target, targetBinding)
	if err != nil {
		t.Fatal(err)
	}
	sourceImpact, targetImpact := strings.Repeat("e", 64), strings.Repeat("f", 64)
	authority := &staticAssetAuthority{
		restore:   map[string]string{source.ID: sourceImpact},
		operation: map[string]string{target.ID: targetImpact},
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Content: contentregistry.New(), AssetAuthority: authority,
	})
	request := lifecycleRegistryRequest(source, target, sourceBinding, targetBinding, 1)
	if err := boundary.freezeContentMaterials(context.Background(), request, &sourceMaterial, &targetMaterial); err != nil {
		t.Fatal(err)
	}
	if sourceMaterial.contentPublication == nil || targetMaterial.contentPublication == nil ||
		sourceMaterial.digest == targetMaterial.digest {
		t.Fatalf("source/target content authority = source %#v target %#v",
			sourceMaterial.contentPublication, targetMaterial.contentPublication)
	}
	if !reflect.DeepEqual(authority.calls, []string{"restore:" + source.ID, "operation:" + target.ID}) {
		t.Fatalf("content authority calls = %#v", authority.calls)
	}
}

func TestLifecycleContentPublicationUpgradeRollbackDisableAndStaleCAS(t *testing.T) {
	ctx := context.Background()
	registry := contentregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Content: registry})
	if boundary.ContentRegistry() != registry {
		t.Fatal("lifecycle boundary created a second Content Registry")
	}
	source := lifecycleContentTestExtension(t, "1.0.0", strings.Repeat("1", 64), 404)
	target := lifecycleContentTestExtension(t, "2.0.0", strings.Repeat("2", 64), 405)
	sourceMaterial := lifecycleContentTestMaterial(t, source, "content-source-runtime")
	targetMaterial := lifecycleContentTestMaterial(t, target, "content-target-runtime")
	if _, err := registry.Publish(*sourceMaterial.contentPublication); err != nil {
		t.Fatal(err)
	}

	drift := sourceMaterial
	drifted := *sourceMaterial.contentPublication
	drifted.Content = append([]contentregistry.Declaration(nil), drifted.Content...)
	drifted.Content[0].Schema = source.ID + ".content.schema.drift@1"
	drift.contentPublication = &drifted
	if err := boundary.validateContentTransition(&drift, &targetMaterial); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact content drift passed validation: %v", err)
	}
	if err := boundary.reconcileContent(ctx, source.ID, &drift, &targetMaterial, &drift); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact content drift passed reconciliation: %v", err)
	}
	if err := boundary.validateContentTransition(&sourceMaterial, &targetMaterial); err != nil {
		t.Fatalf("validate content transition: %v", err)
	}
	if err := boundary.reconcileContent(ctx, source.ID, &sourceMaterial, &targetMaterial, &targetMaterial); err != nil {
		t.Fatalf("publish target content: %v", err)
	}
	assertLifecycleContentArtifact(t, registry, targetMaterial.contentPublication.Artifact)
	if err := boundary.reconcileContent(ctx, source.ID, &sourceMaterial, &targetMaterial, &sourceMaterial); err != nil {
		t.Fatalf("restore source content: %v", err)
	}
	assertLifecycleContentArtifact(t, registry, sourceMaterial.contentPublication.Artifact)
	if err := boundary.reconcileContent(ctx, source.ID, &sourceMaterial, nil, nil); err != nil {
		t.Fatalf("disable content publication: %v", err)
	}
	if _, found := registry.SnapshotPublication(source.ID); found {
		t.Fatal("disabled content publication remains active")
	}

	if _, err := registry.Publish(*targetMaterial.contentPublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileContent(ctx, source.ID, &sourceMaterial, nil, nil); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale source removed replacement content publication: %v", err)
	}
	assertLifecycleContentArtifact(t, registry, targetMaterial.contentPublication.Artifact)
}

func TestLifecycleContentStartupRestoreSafeModeCoreOnlyAndRevisionFence(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	registry := contentregistry.New()
	core := lifecycleCoreContentPublication(t, "core.content.bootstrap", '5')
	if _, err := registry.Publish(core); err != nil {
		t.Fatal(err)
	}
	extension := lifecycleContentTestExtension(t, "1.0.0", strings.Repeat("6", 64), 406)
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	impact := strings.Repeat("7", 64)
	authority := &staticAssetAuthority{restore: map[string]string{extension.ID: impact}}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Pages: pages.NewRegistry(nil), Routes: routes.NewRegistry(),
		RouteSchemas: lifecycleRouteSchemaPublication(t), Content: registry, AssetAuthority: authority,
	})
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("restore content publication: %v", err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := contentregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	}
	assertLifecycleContentArtifact(t, registry, expected)

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, true); err != nil {
		t.Fatalf("restore content Safe Mode: %v", err)
	}
	safe := registry.Snapshot()
	if !safe.SafeMode || len(safe.Publications) != 1 || safe.Publications[0].Artifact != core.Artifact ||
		len(safe.Content) != 1 {
		t.Fatalf("Safe Mode content snapshot = %#v", safe)
	}
	if err := boundary.RestoreContentPublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("leave content Safe Mode: %v", err)
	}
	assertLifecycleContentArtifact(t, registry, expected)
}

func TestLegacyRuntimeContentPublishesQuarantinesAndRollsBackExactArtifact(t *testing.T) {
	ctx := t.Context()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	registry := contentregistry.New()
	extension := lifecycleContentTestExtension(t, "1.0.0", strings.Repeat("8", 64), 408)
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Content: registry,
	})
	published, err := boundary.PublishRuntimeContent(ctx, extension)
	if err != nil || published == nil {
		t.Fatalf("publish legacy runtime content: %v", err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := contentregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	}
	assertLifecycleContentArtifact(t, registry, expected)

	quarantined, err := boundary.QuarantineRuntimeContent(ctx, extension)
	if err != nil || quarantined == nil {
		t.Fatalf("quarantine legacy runtime content: %v", err)
	}
	if _, found := registry.SnapshotPublication(extension.ID); found || manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatal("quarantine left content publication or runtime admission open")
	}
	if err := quarantined.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertLifecycleContentArtifact(t, registry, expected)
	if !manager.RuntimeInstanceAvailable(runtime.Identity) {
		t.Fatal("content rollback did not restore runtime admission")
	}
}

func lifecycleContentTestExtension(t *testing.T, version, seed string, versionID int64) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, seed, versionID, "/content-"+strings.ReplaceAll(version, ".", "-"))
	// Handler-only block：Schema 引用与 Manifest 校验一致；不声明 Renderer，
	// 避免要求同包 template 包文件（lifecycle 测试夹具不带 content templates）。
	extension.Manifest.Content = []extensions.ManifestContent{{
		ID: extension.ID + ".content.block.card", ContractVersion: extension.ID + ".content.block.card@1",
		Kind: contentregistry.KindBlock, Handler: extension.ID + ".content.block.card",
		Schema: extension.ID + ".content.schema@1",
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

func lifecycleContentTestMaterial(
	t *testing.T,
	extension extensions.Extension,
	runtimeInstanceID string,
) lifecycleRegistryMaterial {
	t.Helper()
	material, err := buildLifecycleRegistryMaterial(extension, lifecycleRegistryBinding(extension, runtimeInstanceID))
	if err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Content: contentregistry.New()})
	if err := boundary.freezeContentMaterial(&material); err != nil {
		t.Fatal(err)
	}
	return material
}

func assertLifecycleContentArtifact(t *testing.T, registry *contentregistry.Registry, expected contentregistry.Artifact) {
	t.Helper()
	publication, found := registry.SnapshotPublication(expected.ExtensionID)
	if !found || publication.Artifact != expected || len(publication.Content) != 1 ||
		publication.Content[0].Handler != expected.ExtensionID+".content.block.card" {
		t.Fatalf("content publication = %#v, found=%t, expected=%#v", publication, found, expected)
	}
}

func lifecycleCoreContentPublication(t *testing.T, id string, marker byte) contentregistry.Publication {
	t.Helper()
	artifact, err := contentregistry.NewCoreArtifact(
		id, "1.0.0", strings.Repeat(string(marker), 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	return contentregistry.Publication{Artifact: artifact, Content: []contentregistry.Declaration{{
		ID: id + ".block.card", ContractVersion: id + ".block.card@1",
		Kind: contentregistry.KindBlock, Schema: id + ".schema@1",
		Renderer: id + ".block.card.render",
	}}}
}
