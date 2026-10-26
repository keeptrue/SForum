package extensions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pages "github.com/zhuchunshu/sforum/apps/api/app/Support/Pages"
)

func TestPackageContentRootLegacyUploadedZipUsesFilesDir(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "files")
	if err := os.MkdirAll(filepath.Join(files, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "package.zip")
	if err := os.WriteFile(zipPath, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(files, "theme.json"), []byte(`{"pages":[],"skin":{"css":["assets/theme.css"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ext := Extension{
		Source:      SourceUploaded,
		PackagePath: zipPath,
	}
	got := PackageContentRoot(ext)
	if got != files {
		t.Fatalf("PackageContentRoot = %q, want %q", got, files)
	}
}

func TestPackageContentRootSnapshotDirUnchanged(t *testing.T) {
	root := t.TempDir()
	ext := Extension{
		Source:        SourceBuiltin,
		PackagePath:   root,
		PackageDigest: "abc",
	}
	if got := PackageContentRoot(ext); got != root {
		t.Fatalf("PackageContentRoot = %q, want %q", got, root)
	}
}

func TestServiceActivateThemeLegacyZipResolvesThemeJSON(t *testing.T) {
	// 回归：PackagePath=package.zip 时预检不得拼 package.zip/theme.json。
	root := t.TempDir()
	files := filepath.Join(root, "files")
	if err := os.MkdirAll(filepath.Join(files, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(files, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	themeJSON := `{
  "pages": [
    {
      "id": "legacy.theme.home",
      "action": "replace",
      "target": "forum.home",
      "template": "templates/home.html",
      "contract": "sforum.page.home@1"
    }
  ],
  "skin": {
    "css": ["assets/theme.css"],
    "tokens": "assets/tokens.css"
  }
}
`
	for rel, body := range map[string]string{
		"theme.json":          themeJSON,
		"assets/theme.css":    ":root { --sf-accent: #0891b2; }",
		"assets/tokens.css":   ":root { --sf-theme-radius: 0.5rem; }",
		"templates/home.html": `<div class="sf-page"><sf-home-page></sf-home-page></div>`,
	} {
		path := filepath.Join(files, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	zipPath := filepath.Join(root, "package.zip")
	if err := os.WriteFile(zipPath, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 同级 manifest（validateInstalledPackage 要求）
	if err := os.WriteFile(filepath.Join(root, ManifestFileName), []byte(`{"id":"legacy.theme","name":"Legacy","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	item := uploadedExtension("legacy.theme", TypeTheme)
	item.PackagePath = zipPath
	item.Version = "1.0.0"

	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	reg := pages.NewRegistry(nil)
	adapter := NewPageRegistryAdapter(reg)
	service := NewServiceWithOptions(
		store, t.TempDir(), "", LocalRuntimeManager{},
		WithPageRegistry(adapter),
	)

	active, err := service.ActivateTheme(context.Background(), extensionManager(), item.ID)
	if err != nil {
		t.Fatalf("ActivateTheme should resolve files/ for zip packages, got: %v", err)
	}
	if active.ID != item.ID || store.activeThemeID != item.ID {
		t.Fatalf("expected theme active, got %#v activeID=%q", active, store.activeThemeID)
	}
}
