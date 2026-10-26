package extensionpackage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestSnapshotUploadedCanonicalizesManifestAndReusesIdenticalContent(t *testing.T) {
	destination := t.TempDir()
	firstManifest := []byte(`{
  "version": "1.0.0",
  "manifestVersion": 3,
  "author": {"name": "SForum Test"},
  "url": "https://example.com/demo",
  "description": "Snapshot test plugin.",
  "name": "Demo Plugin",
  "sforumVersion": ">=0.1.0",
  "type": "plugin",
  "id": "demo.plugin"
}`)
	secondManifest := []byte(`{"manifestVersion":3,"id":"demo.plugin","type":"plugin","name":"Demo Plugin","description":"Snapshot test plugin.","url":"https://example.com/demo","author":{"name":"SForum Test"},"sforumVersion":">=0.1.0","version":"1.0.0"}`)

	first, err := SnapshotUploaded(destination, firstManifest, []File{
		{Path: `frontend\\admin\\components\\Cell.vue`, Mode: 0o644, Body: []byte("<template>cell</template>")},
		{Path: "backend/plugin", Mode: 0o755, Body: []byte("backend")},
	})
	if err != nil {
		t.Fatalf("create first snapshot: %v", err)
	}
	second, err := SnapshotUploaded(destination, secondManifest, []File{
		{Path: "backend/plugin", Mode: 0o755, Body: []byte("backend")},
		{Path: "frontend/admin/components/Cell.vue", Mode: 0o644, Body: []byte("<template>cell</template>")},
	})
	if err != nil {
		t.Fatalf("reuse identical snapshot: %v", err)
	}

	if first.Digest != second.Digest || first.Root != second.Root {
		t.Fatalf("normalized identical packages diverged: first=%#v second=%#v", first, second)
	}
	wantRoot := filepath.Join(destination, "demo.plugin", "1.0.0", first.Digest)
	if first.Root != wantRoot {
		t.Fatalf("unexpected snapshot root: want=%s got=%s", wantRoot, first.Root)
	}
	if first.Manifest != second.Manifest || strings.Contains(first.Manifest, "\n") {
		t.Fatalf("manifest was not canonicalized: first=%q second=%q", first.Manifest, second.Manifest)
	}
	onDiskManifest, err := os.ReadFile(filepath.Join(first.Root, "sforum.extension.json"))
	if err != nil {
		t.Fatalf("read canonical manifest: %v", err)
	}
	if string(onDiskManifest) != first.Manifest {
		t.Fatalf("snapshot manifest mismatch: snapshot=%q disk=%q", first.Manifest, onDiskManifest)
	}
	if _, err := os.Stat(filepath.Join(first.Root, "frontend", "admin", "components", "Cell.vue")); err != nil {
		t.Fatalf("normalized component path missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(first.Root, "package.zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("storage wrapper must not be copied, got %v", err)
	}
	recalculated, err := DigestTree(first.Root)
	if err != nil {
		t.Fatalf("recalculate snapshot digest: %v", err)
	}
	if recalculated != first.Digest {
		t.Fatalf("snapshot digest is not reproducible: want=%s got=%s", first.Digest, recalculated)
	}
}

func TestSnapshotUploadedNormalizesHighModeFlagsButKeepsPermissions(t *testing.T) {
	destination := t.TempDir()
	manifest := snapshotTestManifest("mode.plugin", "1.0.0")
	body := []byte("same")

	withHighFlags, err := SnapshotUploaded(destination, manifest, []File{{
		Path: "component.vue",
		Mode: 0o644 | fs.ModeSetuid | fs.ModeTemporary,
		Body: body,
	}})
	if err != nil {
		t.Fatalf("snapshot with high mode flags: %v", err)
	}
	plain, err := SnapshotUploaded(destination, manifest, []File{{Path: "component.vue", Mode: 0o644, Body: body}})
	if err != nil {
		t.Fatalf("snapshot with plain mode: %v", err)
	}
	changedPermissions, err := SnapshotUploaded(destination, manifest, []File{{Path: "component.vue", Mode: 0o600, Body: body}})
	if err != nil {
		t.Fatalf("snapshot with changed permissions: %v", err)
	}

	if withHighFlags.Digest != plain.Digest {
		t.Fatalf("non-permission mode flags changed digest: flagged=%s plain=%s", withHighFlags.Digest, plain.Digest)
	}
	if plain.Digest == changedPermissions.Digest {
		t.Fatalf("permission bits did not change digest: %s", plain.Digest)
	}
	info, err := os.Stat(filepath.Join(withHighFlags.Root, "component.vue"))
	if err != nil {
		t.Fatalf("stat snapshotted component: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("high mode flags were not normalized, got %o", info.Mode().Perm())
	}
}

func TestSnapshotUploadedKeepsDifferentContentForSameIDAndVersion(t *testing.T) {
	destination := t.TempDir()
	manifest := snapshotTestManifest("changed.plugin", "1.0.0")

	first, err := SnapshotUploaded(destination, manifest, []File{{Path: "component.vue", Mode: 0o644, Body: []byte("first")}})
	if err != nil {
		t.Fatalf("create first snapshot: %v", err)
	}
	second, err := SnapshotUploaded(destination, manifest, []File{{Path: "component.vue", Mode: 0o644, Body: []byte("second")}})
	if err != nil {
		t.Fatalf("create second snapshot: %v", err)
	}

	if first.Digest == second.Digest || first.Root == second.Root {
		t.Fatalf("different package content reused one snapshot: first=%#v second=%#v", first, second)
	}
	firstBody, err := os.ReadFile(filepath.Join(first.Root, "component.vue"))
	if err != nil {
		t.Fatalf("read first snapshot: %v", err)
	}
	secondBody, err := os.ReadFile(filepath.Join(second.Root, "component.vue"))
	if err != nil {
		t.Fatalf("read second snapshot: %v", err)
	}
	if string(firstBody) != "first" || string(secondBody) != "second" {
		t.Fatalf("snapshot contents were overwritten: first=%q second=%q", firstBody, secondBody)
	}
}

func TestSnapshotUploadedVerifiesExistingSnapshotBeforeReuse(t *testing.T) {
	destination := t.TempDir()
	manifest := snapshotTestManifest("conflict.plugin", "1.0.0")
	files := []File{{Path: "component.vue", Mode: 0o644, Body: []byte("approved")}}

	snapshot, err := SnapshotUploaded(destination, manifest, files)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshot.Root, "component.vue"), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper snapshot: %v", err)
	}

	_, err = SnapshotUploaded(destination, manifest, files)
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}
	tampered, readErr := os.ReadFile(filepath.Join(snapshot.Root, "component.vue"))
	if readErr != nil {
		t.Fatalf("read conflicting snapshot: %v", readErr)
	}
	if string(tampered) != "tampered" {
		t.Fatalf("conflicting snapshot was silently overwritten: %q", tampered)
	}
}

func TestSnapshotUploadedRejectsUnsafePath(t *testing.T) {
	_, err := SnapshotUploaded(t.TempDir(), snapshotTestManifest("unsafe.plugin", "1.0.0"), []File{{
		Path: "../outside.vue",
		Mode: 0o644,
		Body: []byte("outside"),
	}})
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected ErrInvalidPath, got %v", err)
	}
}

func TestSnapshotUploadedRejectsAmbiguousPaths(t *testing.T) {
	tests := []struct {
		name  string
		files []File
	}{
		{
			name: "duplicate normalized path",
			files: []File{
				{Path: "frontend//Cell.vue", Mode: 0o644},
				{Path: "frontend/Cell.vue", Mode: 0o644},
			},
		},
		{
			name:  "manifest overwrite",
			files: []File{{Path: "sforum.extension.json", Mode: 0o644}},
		},
		{
			name: "file directory collision",
			files: []File{
				{Path: "assets", Mode: 0o644},
				{Path: "assets-copy", Mode: 0o644},
				{Path: "assets/icon.svg", Mode: 0o644},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := SnapshotUploaded(t.TempDir(), snapshotTestManifest("ambiguous.plugin", "1.0.0"), test.files)
			if !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("expected ErrInvalidPath, got %v", err)
			}
		})
	}
}

func TestSnapshotUploadedRejectsNonRegularFileMode(t *testing.T) {
	_, err := SnapshotUploaded(t.TempDir(), snapshotTestManifest("link.plugin", "1.0.0"), []File{{
		Path: "alias.vue",
		Mode: fs.ModeSymlink | 0o777,
		Body: []byte("component.vue"),
	}})
	if !errors.Is(err, ErrSymlink) {
		t.Fatalf("expected ErrSymlink, got %v", err)
	}
}

func TestSnapshotBuiltinCopiesCanonicalPackage(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	writeSnapshotTestFile(t, source, "sforum.extension.json", snapshotTestManifestString("builtin.plugin", "2.0.0"), 0o600)
	writeSnapshotTestFile(t, source, "frontend/admin/component.vue", "<template>builtin</template>", 0o644)

	snapshot, err := SnapshotBuiltin(source, destination)
	if err != nil {
		t.Fatalf("snapshot builtin: %v", err)
	}
	wantRoot := filepath.Join(destination, "builtin.plugin", "2.0.0", snapshot.Digest)
	if snapshot.Root != wantRoot {
		t.Fatalf("unexpected builtin snapshot root: want=%s got=%s", wantRoot, snapshot.Root)
	}
	if _, err := os.Stat(filepath.Join(snapshot.Root, "frontend", "admin", "component.vue")); err != nil {
		t.Fatalf("builtin component missing: %v", err)
	}
	recalculated, err := DigestTree(snapshot.Root)
	if err != nil {
		t.Fatalf("digest builtin snapshot: %v", err)
	}
	if recalculated != snapshot.Digest {
		t.Fatalf("builtin snapshot digest mismatch: want=%s got=%s", snapshot.Digest, recalculated)
	}
}

func TestSnapshotUploadedMergesIncludesAndKeepsPartials(t *testing.T) {
	destination := t.TempDir()
	rootBody := []byte(`{
	  "manifestVersion": 3,
  "id": "includes.plugin",
  "name": "Includes Plugin",
  "description": "Uses includes.",
  "url": "https://example.com/includes",
  "author": {"name": "SForum Test"},
  "version": "1.0.0",
  "type": "plugin",
  "sforumVersion": ">=0.1.0",
  "includes": {
    "langs": "manifest/langs",
    "settings": "manifest/settings.json"
  }
}`)
	snapshot, err := SnapshotUploaded(destination, rootBody, []File{
		{Path: "manifest/langs/zh-CN.json", Mode: 0o644, Body: []byte(`{"name":"包含插件"}`)},
		{Path: "manifest/settings.json", Mode: 0o644, Body: []byte(`[{"key":"enabled","label":"Enabled","type":"boolean","default":"true"}]`)},
	})
	if err != nil {
		t.Fatalf("SnapshotUploaded with includes: %v", err)
	}
	if !strings.Contains(snapshot.Manifest, `"enabled"`) {
		t.Fatalf("merged manifest missing settings: %s", snapshot.Manifest)
	}
	if strings.Contains(snapshot.Manifest, `"includes"`) {
		t.Fatalf("canonical merged manifest must not retain includes: %s", snapshot.Manifest)
	}
	if _, err := os.Stat(filepath.Join(snapshot.Root, "manifest", "langs", "zh-CN.json")); err != nil {
		t.Fatalf("partial should remain on disk: %v", err)
	}
	// 快照入口为合并结果，可直接 LoadPackage（无需 includes）。
	loaded, err := extensionmanifest.LoadPackage(snapshot.Root)
	if err != nil {
		t.Fatalf("LoadPackage snapshot: %v", err)
	}
	if len(loaded.Settings) != 1 || loaded.Settings[0].Key != "enabled" {
		t.Fatalf("unexpected settings: %#v", loaded.Settings)
	}
}

func TestSnapshotBuiltinRejectsSymlink(t *testing.T) {
	source := t.TempDir()
	writeSnapshotTestFile(t, source, "sforum.extension.json", snapshotTestManifestString("builtin-link.plugin", "1.0.0"), 0o600)
	writeSnapshotTestFile(t, source, "component.vue", "<template />", 0o644)
	if err := os.Symlink("component.vue", filepath.Join(source, "alias.vue")); err != nil {
		t.Fatalf("create builtin symlink: %v", err)
	}

	_, err := SnapshotBuiltin(source, t.TempDir())
	if !errors.Is(err, ErrSymlink) {
		t.Fatalf("expected ErrSymlink, got %v", err)
	}
}

func snapshotTestManifest(id string, version string) []byte {
	return []byte(snapshotTestManifestString(id, version))
}

func snapshotTestManifestString(id string, version string) string {
	return `{"manifestVersion":3,"id":"` + id + `","name":"Snapshot Test","description":"Snapshot package test.","url":"https://example.com/snapshot","author":{"name":"SForum Test"},"version":"` + version + `","type":"plugin","sforumVersion":">=0.1.0"}`
}

func writeSnapshotTestFile(t *testing.T, root string, relativePath string, body string, mode os.FileMode) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", relativePath, err)
	}
	if err := os.WriteFile(target, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", relativePath, err)
	}
}
