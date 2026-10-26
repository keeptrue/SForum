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
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestLifecycleMediaMaterialFreezesExactAuthorityAndReachableDigestAliases(t *testing.T) {
	extension := lifecycleMediaTestExtension(t, "1.0.0", strings.Repeat("a", 64), 501)
	material, err := buildLifecycleRegistryMaterial(extension, lifecycleRegistryBinding(extension, "media-digest-runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if material.mediaPublication != nil {
		t.Fatal("media publication was built before exact lifecycle authority was frozen")
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Media: mediaregistry.New()})
	impact := strings.Repeat("b", 64)
	if err := boundary.freezeMediaMaterial(&material, impact); err != nil {
		t.Fatal(err)
	}
	publication := material.mediaPublication
	if publication == nil || publication.Artifact.ExtensionID != extension.ID ||
		publication.Artifact.ImpactDigest != impact ||
		publication.Artifact.RuntimeInstanceID != "media-digest-runtime" ||
		len(publication.Processors) != 1 || publication.Processors[0].Stage != mediaregistry.StageTransform ||
		len(publication.Policies) != 1 || len(publication.Variants) != 1 ||
		publication.Variants[0].Name != "thumb" || publication.Variants[0].OutputMIME != "image/webp" {
		t.Fatalf("frozen media publication = %#v", publication)
	}
	// 变体必须绑定 exact 本包 processor，禁用插件后不得保留可写破坏 original 的路径。
	if publication.Variants[0].ProcessorOwnerExtensionID != extension.ID ||
		publication.Variants[0].ProcessorPackageDigest != extension.PackageDigest ||
		publication.Variants[0].ProcessorID != publication.Processors[0].ID {
		t.Fatalf("variant source-of-truth binding = %#v", publication.Variants[0])
	}
	v1Digest, err := encodeLifecycleRegistryMaterialDigest(&material, false, false)
	if err != nil {
		t.Fatal(err)
	}
	v9Digest, err := encodeLifecycleRegistryMaterialDigestV9(&material)
	if err != nil {
		t.Fatal(err)
	}
	if material.digest != v9Digest || material.legacyDigest != v1Digest ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest}) || v9Digest == v1Digest {
		t.Fatalf("media material digests primary=%s legacy=%s aliases=%v", material.digest, material.legacyDigest,
			registryMaterialCompatibleDigests(&material))
	}

	extension.Manifest.Media[0].Handler = extension.ID + ".media.mutated"
	if publication.Processors[0].Handler != extension.ID+".media.image" {
		t.Fatalf("Manifest mutation changed frozen media publication = %#v", publication.Processors[0])
	}
	before := material.digest
	material.mediaPublication.Processors[0].Priority++
	if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
		t.Fatal(err)
	}
	if material.digest == before ||
		!reflect.DeepEqual(registryMaterialCompatibleDigests(&material), []string{v1Digest}) {
		t.Fatalf("media declaration drift digest=%s aliases=%v", material.digest, registryMaterialCompatibleDigests(&material))
	}
}

func TestLifecycleMediaMaterialsUseSourceRestoreAndTargetOperationAuthority(t *testing.T) {
	source := lifecycleMediaTestExtension(t, "1.0.0", strings.Repeat("c", 64), 502)
	target := lifecycleMediaTestExtension(t, "2.0.0", strings.Repeat("d", 64), 503)
	sourceBinding := lifecycleRegistryBinding(source, "media-authority-source")
	targetBinding := lifecycleRegistryBinding(target, "media-authority-target")
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
		Media: mediaregistry.New(), AssetAuthority: authority,
	})
	request := lifecycleRegistryRequest(source, target, sourceBinding, targetBinding, 1)
	if err := boundary.freezeMediaMaterials(context.Background(), request, &sourceMaterial, &targetMaterial); err != nil {
		t.Fatal(err)
	}
	if sourceMaterial.mediaPublication.Artifact.ImpactDigest != sourceImpact ||
		targetMaterial.mediaPublication.Artifact.ImpactDigest != targetImpact ||
		sourceMaterial.digest == targetMaterial.digest {
		t.Fatalf("source/target media authority = source %#v target %#v",
			sourceMaterial.mediaPublication.Artifact, targetMaterial.mediaPublication.Artifact)
	}
	if !reflect.DeepEqual(authority.calls, []string{"restore:" + source.ID, "operation:" + target.ID}) {
		t.Fatalf("media authority calls = %#v", authority.calls)
	}
}

func TestLifecycleMediaPublicationUpgradeRollbackDisableAndStaleCAS(t *testing.T) {
	ctx := context.Background()
	registry := mediaregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Media: registry})
	if boundary.MediaRegistry() != registry {
		t.Fatal("lifecycle boundary created a second Media Registry")
	}
	source := lifecycleMediaTestExtension(t, "1.0.0", strings.Repeat("1", 64), 504)
	target := lifecycleMediaTestExtension(t, "2.0.0", strings.Repeat("2", 64), 505)
	sourceMaterial := lifecycleMediaTestMaterial(t, source, "media-source-runtime", strings.Repeat("3", 64))
	targetMaterial := lifecycleMediaTestMaterial(t, target, "media-target-runtime", strings.Repeat("4", 64))
	if _, err := registry.Publish(*sourceMaterial.mediaPublication); err != nil {
		t.Fatal(err)
	}

	drift := sourceMaterial
	drifted := *sourceMaterial.mediaPublication
	drifted.Processors = append([]mediaregistry.ProcessorDeclaration(nil), drifted.Processors...)
	drifted.Processors[0].Priority++
	drift.mediaPublication = &drifted
	if err := boundary.validateMediaTransition(&drift, &targetMaterial); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact media drift passed validation: %v", err)
	}
	if err := boundary.reconcileMedia(ctx, source.ID, &drift, &targetMaterial, &drift); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact media drift passed reconciliation: %v", err)
	}
	if err := boundary.validateMediaTransition(&sourceMaterial, &targetMaterial); err != nil {
		t.Fatalf("validate media transition: %v", err)
	}
	if err := boundary.reconcileMedia(ctx, source.ID, &sourceMaterial, &targetMaterial, &targetMaterial); err != nil {
		t.Fatalf("publish target media: %v", err)
	}
	assertLifecycleMediaArtifact(t, registry, targetMaterial.mediaPublication.Artifact)
	if err := boundary.reconcileMedia(ctx, source.ID, &sourceMaterial, &targetMaterial, &sourceMaterial); err != nil {
		t.Fatalf("restore source media: %v", err)
	}
	assertLifecycleMediaArtifact(t, registry, sourceMaterial.mediaPublication.Artifact)
	if err := boundary.reconcileMedia(ctx, source.ID, &sourceMaterial, nil, nil); err != nil {
		t.Fatalf("disable media publication: %v", err)
	}
	if _, found := registry.SnapshotPublication(source.ID); found {
		t.Fatal("disabled media publication remains active")
	}

	if _, err := registry.Publish(*targetMaterial.mediaPublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileMedia(ctx, source.ID, &sourceMaterial, nil, nil); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale source removed replacement media publication: %v", err)
	}
	assertLifecycleMediaArtifact(t, registry, targetMaterial.mediaPublication.Artifact)
}

func TestLifecycleMediaStartupRestoreSafeModeCoreOnlyAndRevisionFence(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	registry := mediaregistry.New()
	core := lifecycleCoreMediaPublication(t, "core.media.bootstrap", '5')
	if _, err := registry.Publish(core); err != nil {
		t.Fatal(err)
	}
	extension := lifecycleMediaTestExtension(t, "1.0.0", strings.Repeat("6", 64), 506)
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	impact := strings.Repeat("7", 64)
	authority := &staticAssetAuthority{restore: map[string]string{extension.ID: impact}}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Pages: pages.NewRegistry(nil), Routes: routes.NewRegistry(),
		RouteSchemas: lifecycleRouteSchemaPublication(t), Media: registry, AssetAuthority: authority,
	})
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("restore media publication: %v", err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := mediaregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: impact,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: runtime.Identity.InstanceID,
	}
	assertLifecycleMediaArtifact(t, registry, expected)

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, true); err != nil {
		t.Fatalf("restore media Safe Mode: %v", err)
	}
	safe := registry.Snapshot()
	if !safe.SafeMode || len(safe.Publications) != 1 || safe.Publications[0].Artifact != core.Artifact {
		t.Fatalf("Safe Mode media snapshot = %#v", safe)
	}
	if err := boundary.RestoreMediaPublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("leave media Safe Mode: %v", err)
	}
	assertLifecycleMediaArtifact(t, registry, expected)
}

func lifecycleMediaTestExtension(t *testing.T, version, seed string, versionID int64) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, seed, versionID, "/media-"+strings.ReplaceAll(version, ".", "-"))
	extension.Manifest.Media = []extensions.ManifestMediaPipeline{{
		ID: extension.ID + ".media.image", ContractVersion: extension.ID + ".media.image@1",
		Action: "add", MIMEs: []string{"image/png"}, Handler: extension.ID + ".media.image",
		// 空 Permission → Host 默认 attachment.upload（lifecycle 映射层）。
		Transforms: []extensionmanifest.ManifestMediaTransform{{
			ID: "thumbnail", Variant: "thumb", Format: "webp", Width: 320, Height: 240,
		}},
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

func lifecycleMediaTestMaterial(
	t *testing.T,
	extension extensions.Extension,
	runtimeInstanceID string,
	impactDigest string,
) lifecycleRegistryMaterial {
	t.Helper()
	material, err := buildLifecycleRegistryMaterial(extension, lifecycleRegistryBinding(extension, runtimeInstanceID))
	if err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Media: mediaregistry.New()})
	if err := boundary.freezeMediaMaterial(&material, impactDigest); err != nil {
		t.Fatal(err)
	}
	return material
}

func assertLifecycleMediaArtifact(t *testing.T, registry *mediaregistry.Registry, expected mediaregistry.Artifact) {
	t.Helper()
	publication, found := registry.SnapshotPublication(expected.ExtensionID)
	if !found || publication.Artifact != expected || len(publication.Processors) != 1 ||
		publication.Processors[0].Handler != expected.ExtensionID+".media.image" ||
		len(publication.Variants) != 1 {
		t.Fatalf("media publication = %#v, found=%t, expected=%#v", publication, found, expected)
	}
}

func lifecycleCoreMediaPublication(t *testing.T, id string, marker byte) mediaregistry.Publication {
	t.Helper()
	artifact, err := mediaregistry.NewCoreArtifact(
		id, "1.0.0", strings.Repeat(string(marker), 64), strings.Repeat(string(marker+1), 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	return mediaregistry.Publication{Artifact: artifact, Policies: []mediaregistry.MIMEPolicyDeclaration{{
		ID: id + ".policy", ContractVersion: id + ".policy@1", Purpose: "general",
		RequiredPermission: "attachment.upload", AllowedMIMEs: []string{"image/*"},
		StrictDeclaredMIME: true, Budget: mediaregistry.DefaultBudget(),
	}}}
}
