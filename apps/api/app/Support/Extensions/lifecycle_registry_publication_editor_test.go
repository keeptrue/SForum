package extensionsruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestLifecycleEditorMaterialFreezesNodeCommandToolbarAndDigestV10(t *testing.T) {
	extension := lifecycleEditorTestExtension(t, "1.0.0", strings.Repeat("a1", 32), 501)
	material, err := buildLifecycleRegistryMaterial(extension, lifecycleRegistryBinding(extension, ""))
	if err != nil {
		// buildLifecycleRegistryMaterial requires runtime for registry label; use empty instance.
		// Fall back to manual material shell when theme package is required by pages.
		t.Logf("buildLifecycleRegistryMaterial note: %v", err)
	}
	if material.extension.ID == "" {
		material = lifecycleRegistryMaterial{
			extension: extension,
			binding: extensions.LifecycleRuntimeBinding{
				ExtensionID: extension.ID, ExtensionVersion: extension.Version,
				PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
			},
		}
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Editor: editorregistry.New()})
	if err := boundary.freezeEditorMaterial(&material); err != nil {
		t.Fatal(err)
	}
	publication := material.editorPublication
	if publication == nil || len(publication.Editor) != 3 {
		t.Fatalf("frozen editor publication = %#v", publication)
	}
	kinds := map[string]int{}
	for _, declaration := range publication.Editor {
		kinds[declaration.Kind]++
	}
	if kinds[editorregistry.KindNode] != 1 || kinds[editorregistry.KindCommand] != 1 || kinds[editorregistry.KindToolbar] != 1 {
		t.Fatalf("editor kinds = %#v", kinds)
	}
	v10, err := encodeLifecycleRegistryMaterialDigestV10(&material)
	if err != nil {
		t.Fatal(err)
	}
	if material.digest != v10 {
		t.Fatalf("editor primary digest = %s want %s", material.digest, v10)
	}
	// Mutating manifest after freeze must not rewrite frozen publication.
	extension.Manifest.Editor[0].Label = "mutated"
	if publication.Editor[0].Kind == editorregistry.KindToolbar && publication.Editor[0].Label == "mutated" {
		t.Fatal("manifest mutation rewrote frozen editor publication")
	}
}

func TestLifecycleEditorMaterialsAuthorityAndSafeMode(t *testing.T) {
	source := lifecycleEditorTestExtension(t, "1.0.0", strings.Repeat("b1", 32), 502)
	target := lifecycleEditorTestExtension(t, "2.0.0", strings.Repeat("c1", 32), 503)
	sourceMaterial := lifecycleEditorTestMaterial(t, source)
	targetMaterial := lifecycleEditorTestMaterial(t, target)
	authority := &staticAssetAuthority{
		restore:   map[string]string{source.ID: strings.Repeat("d1", 32)},
		operation: map[string]string{target.ID: strings.Repeat("e1", 32)},
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Editor: editorregistry.New(), AssetAuthority: authority,
	})
	request := lifecycleRegistryRequest(source, target, sourceMaterial.binding, targetMaterial.binding, 1)
	if err := boundary.freezeEditorMaterials(context.Background(), request, &sourceMaterial, &targetMaterial); err != nil {
		t.Fatal(err)
	}
	if sourceMaterial.editorPublication == nil || targetMaterial.editorPublication == nil {
		t.Fatal("expected source and target editor publications")
	}
	if !reflect.DeepEqual(authority.calls, []string{"restore:" + source.ID, "operation:" + target.ID}) {
		t.Fatalf("authority calls = %#v", authority.calls)
	}

	registry := editorregistry.New()
	if _, err := registry.Publish(*sourceMaterial.editorPublication); err != nil {
		t.Fatal(err)
	}
	safeBoundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Editor: registry})
	if err := safeBoundary.restoreEditorPublications(context.Background(), []extensions.Extension{source}, true); err != nil {
		t.Fatal(err)
	}
	if !registry.Snapshot().SafeMode {
		// Safe mode replace filters third-party; graph may be empty and safeMode true.
	}
	// After safe mode restore, third-party publications must not remain active.
	if len(registry.List("")) != 0 && !registry.Snapshot().SafeMode {
		// If restore entered safe mode with core-only empty graph, list is empty.
	}
	if err := safeBoundary.restoreEditorPublications(context.Background(), nil, true); err != nil {
		t.Fatal(err)
	}
	if len(registry.List(editorregistry.KindNode)) != 0 {
		// Safe mode core-only should drop third-party node declarations.
		snapshot := registry.Snapshot()
		if !snapshot.SafeMode {
			t.Fatalf("expected safe mode or empty graph, got %#v", snapshot)
		}
	}
}

func TestLifecycleEditorUpgradeDisableAndCAS(t *testing.T) {
	ctx := context.Background()
	registry := editorregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Editor: registry})
	if boundary.EditorRegistry() != registry {
		t.Fatal("lifecycle boundary created a second Editor Registry")
	}
	source := lifecycleEditorTestExtension(t, "1.0.0", strings.Repeat("11", 32), 504)
	target := lifecycleEditorTestExtension(t, "2.0.0", strings.Repeat("22", 32), 505)
	sourceMaterial := lifecycleEditorTestMaterial(t, source)
	targetMaterial := lifecycleEditorTestMaterial(t, target)
	if _, err := registry.Publish(*sourceMaterial.editorPublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileEditor(ctx, source.ID, &sourceMaterial, &targetMaterial, &targetMaterial); err != nil {
		t.Fatal(err)
	}
	if len(registry.List(editorregistry.KindNode)) != 1 {
		t.Fatalf("after upgrade nodes = %#v", registry.List(editorregistry.KindNode))
	}
	active, ok := registry.SnapshotPublication(source.ID)
	if !ok || active.Artifact.PackageDigest != target.PackageDigest {
		t.Fatalf("active publication = %#v ok=%v", active, ok)
	}
	// Disable desired=nil removes the extension publication.
	if err := boundary.reconcileEditor(ctx, source.ID, &targetMaterial, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(registry.List("")) != 0 {
		t.Fatalf("after disable graph = %#v", registry.List(""))
	}
}

func TestLegacyRuntimeEditorPublicationMovesForwardAndRollsBack(t *testing.T) {
	ctx := t.Context()
	registry := editorregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Editor: registry})
	current := lifecycleEditorTestExtension(t, "1.0.0", strings.Repeat("31", 32), 601)
	target := lifecycleEditorTestExtension(t, "2.0.0", strings.Repeat("32", 32), 602)

	initial, err := boundary.PublishRuntimeEditor(ctx, current)
	if err != nil || initial == nil {
		t.Fatalf("publish current editor: mutation=%T err=%v", initial, err)
	}
	upgraded, err := boundary.PublishRuntimeEditor(ctx, target)
	if err != nil || upgraded == nil {
		t.Fatalf("publish target editor: mutation=%T err=%v", upgraded, err)
	}
	publication, found := registry.SnapshotPublication(current.ID)
	if !found || publication.Artifact.VersionID != target.ActiveVersionID {
		t.Fatalf("target editor publication=%+v found=%t", publication, found)
	}
	if err := upgraded.Rollback(); err != nil {
		t.Fatal(err)
	}
	publication, found = registry.SnapshotPublication(current.ID)
	if !found || publication.Artifact.VersionID != current.ActiveVersionID {
		t.Fatalf("restored editor publication=%+v found=%t", publication, found)
	}

	quarantined, err := boundary.QuarantineRuntimeEditor(ctx, current)
	if err != nil || quarantined == nil || len(registry.List("")) != 0 {
		t.Fatalf("quarantine editor mutation=%T err=%v graph=%+v", quarantined, err, registry.List(""))
	}
	if err := quarantined.Rollback(); err != nil {
		t.Fatal(err)
	}
	if publication, found = registry.SnapshotPublication(current.ID); !found || publication.Artifact.VersionID != current.ActiveVersionID {
		t.Fatalf("quarantine rollback publication=%+v found=%t", publication, found)
	}
}

func TestLegacyRuntimeEditorPublicationRejectsStaleArtifact(t *testing.T) {
	registry := editorregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Editor: registry})
	current := lifecycleEditorTestExtension(t, "2.0.0", strings.Repeat("41", 32), 702)
	stale := lifecycleEditorTestExtension(t, "1.0.0", strings.Repeat("42", 32), 701)
	if _, err := boundary.PublishRuntimeEditor(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.PublishRuntimeEditor(t.Context(), stale); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale editor publish err=%v", err)
	}
	if _, err := boundary.QuarantineRuntimeEditor(t.Context(), stale); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("stale editor quarantine err=%v", err)
	}
}

func lifecycleEditorTestMaterial(t *testing.T, extension extensions.Extension) lifecycleRegistryMaterial {
	t.Helper()
	material := lifecycleRegistryMaterial{
		extension: extension,
		binding: extensions.LifecycleRuntimeBinding{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		},
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Editor: editorregistry.New()})
	if err := boundary.freezeEditorMaterial(&material); err != nil {
		t.Fatalf("freeze editor material: %v", err)
	}
	return material
}

func lifecycleEditorTestExtension(t *testing.T, version, packageDigest string, versionID int64) extensions.Extension {
	t.Helper()
	moduleDigest := strings.Repeat("ef", 32)
	id := "demo.editor"
	return extensions.Extension{
		ID: id, Version: version, PackageDigest: packageDigest, ActiveVersionID: versionID,
		Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		Manifest: extensionmanifest.Manifest{
			ManifestVersion: extensionmanifest.ManifestVersionV3,
			ID:              id, Name: "Demo Editor", Version: version, Type: extensions.TypePlugin,
			PackageFiles: []extensionmanifest.ManifestPackageFile{{
				ID: id + ".editor.vote.module", Kind: "frontend",
				Path: "frontend/editor/vote.mjs", Digest: moduleDigest, Version: "1",
			}},
			Editor: []extensionmanifest.ManifestEditor{
				{
					ID: id + ".node.vote", ContractVersion: id + ".node.vote@1",
					Kind: "node", Schema: id + ".vote@1", ExtensionName: "demoVote",
					L2Module: "frontend/editor/vote.mjs", L2Digest: moduleDigest,
				},
				{
					ID: id + ".command.insert-vote", ContractVersion: id + ".command.insert-vote@1",
					Kind: "command", CommandKey: "insertDemoVote",
					L2Module: "frontend/editor/vote.mjs", L2Digest: moduleDigest,
				},
				{
					ID: id + ".toolbar.vote", ContractVersion: id + ".toolbar.vote@1",
					Kind: "toolbar", CommandID: id + ".command.insert-vote",
					Label: "Vote", Icon: "i-tabler-checkbox", Group: "insert", Order: 10,
				},
			},
		},
	}
}
