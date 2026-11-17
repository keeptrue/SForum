package testartifact_test

import (
	"encoding/json"
	"os"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
	"github.com/keeptrue/sforum/apps/api/internal/testartifact"
)

// 工厂的产物必须直接过发布期闸门的判定：入口占位与两处 digest 的同步只要漏一处，
// 所有依赖它的集成测试都会红在一个看不出原因的地方。
func TestMaterializeProducesVerifiableArtifacts(t *testing.T) {
	factory := testartifact.New(t)
	for _, id := range []string{"fixture.plugin", "second.plugin", "fixture.theme"} {
		coords := factory.Materialize(t, id, "1.0.0")
		if info, err := os.Stat(coords.Path); err != nil || !info.IsDir() || coords.Digest == "" {
			t.Fatalf("%s: coords=%#v err=%v", id, coords, err)
		}
		if err := artifactintegrity.Verify(coords.Path, artifactintegrity.Member{
			ExtensionID: id, Version: "1.0.0", Digest: coords.Digest,
		}); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
}

// 同一 (id, version) 两次 Materialize 必须是两个不同的自洽制品:修复路径需要
// 「同版本、另一个可用 digest」的候选。
func TestMaterializeTwiceYieldsDistinctSelfConsistentArtifacts(t *testing.T) {
	factory := testartifact.New(t)
	first := factory.Materialize(t, "fixture.plugin", "1.0.0")
	second := factory.Materialize(t, "fixture.plugin", "1.0.0")
	if first.Digest == second.Digest {
		t.Fatalf("two materializations share digest %s", first.Digest)
	}
	for _, coords := range []testartifact.Coords{first, second} {
		if err := artifactintegrity.Verify(coords.Path, artifactintegrity.Member{
			ExtensionID: "fixture.plugin", Version: "1.0.0", Digest: coords.Digest,
		}); err != nil {
			t.Fatalf("digest %s: %v", coords.Digest, err)
		}
	}
}

func TestMaterializeManifestSyncsEntryDigest(t *testing.T) {
	manifest := extensionmanifest.Manifest{
		ManifestVersion: 3,
		ID:              "publication.integration",
		Name:            "Lifecycle Publication Fixture",
		Description:     "Exact lifecycle publication fixture.",
		URL:             "https://example.com/lifecycle-publication",
		Author:          extensionmanifest.ManifestAuthor{Name: "SForum"},
		Version:         "1.0.0",
		Type:            extensionmanifest.TypePlugin,
		SForumVersion:   ">=0.1.0",
		Backend: extensionmanifest.ManifestBackend{
			Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
			Digest: "caller-supplied-placeholder", HostAPIVersion: "sforum.host@2",
		},
		PackageFiles: []extensionmanifest.ManifestPackageFile{{
			ID: "publication.integration.backend", Kind: "executable",
			Path: "backend/plugin", Digest: "caller-supplied-placeholder",
		}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	coords := testartifact.New(t).MaterializeManifest(t, raw, nil)
	if err := artifactintegrity.Verify(coords.Path, artifactintegrity.Member{
		ExtensionID: "publication.integration", Version: "1.0.0", Digest: coords.Digest,
	}); err != nil {
		t.Fatalf("manifest artifact: %v", err)
	}
	var loaded extensionmanifest.Manifest
	if err := json.Unmarshal(coords.Manifest, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Backend.Digest == "caller-supplied-placeholder" ||
		loaded.PackageFiles[0].Digest != loaded.Backend.Digest {
		t.Fatalf("returned manifest kept unsynced digests: %#v", loaded.PackageFiles)
	}
}
