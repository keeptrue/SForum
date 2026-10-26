package main

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestBuildExtensionPackageProducesZipAndSBOM(t *testing.T) {
	// Use a tiny synthetic tree that still validates via LoadPackage if a builtin exists.
	// Fall back to packaging any directory with a minimal manifest when fixtures unavailable.
	root := t.TempDir()
	manifest := `{
  "manifestVersion": 3,
  "id": "demo.package",
  "name": "Demo Package",
  "version": "1.0.0",
  "type": "plugin",
  "packageFiles": [
    {"path": "sforum.extension.json", "digest": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}
  ]
}`
	// Fix digest after write - use digest refresh path: write file then recompute.
	if err := os.WriteFile(filepath.Join(root, "sforum.extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// For LoadPackage V3, packageFiles digests must match. Recompute.
	sum, err := digestPackageRelativeFile(root, "sforum.extension.json")
	if err != nil {
		// If helper unavailable shape differs, just package raw without validate.
		t.Logf("digest helper: %v", err)
	} else {
		manifest = strings.Replace(manifest, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", sum, 1)
		if err := os.WriteFile(filepath.Join(root, "sforum.extension.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	zipPath := filepath.Join(t.TempDir(), "demo.sforum.zip")
	// Call build directly; validation may fail on incomplete V3 — still exercise zip path.
	// Prefer buildExtensionPackage without LoadPackage for unit focus.
	result, err := buildExtensionPackage(root, zipPath, packageBuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.PackageDigest == "" || result.FileCount < 1 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(result.ZipPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.SBOMPath); err != nil {
		t.Fatal(err)
	}
}

func TestBuildExtensionPackageExcludeSource(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("sforum.extension.json", `{"id":"demo.release"}`, 0o644)
	write("backend/main.go", "package main\n", 0o644)
	write("backend/go.mod", "module demo\n", 0o644)
	write("backend/plugin", "#!/bin/sh\necho ok\n", 0o755)
	write("frontend/settings.mjs", "export default {}\n", 0o644)
	write("frontend/settings.mjs.map", "{}\n", 0o644)
	write("frontend/Widget.vue", "<template></template>\n", 0o644)
	write("testdata/fixture.json", "{}\n", 0o644)
	write("README.md", "# demo\n", 0o644)

	// 默认：源码一并打入
	fullZip := filepath.Join(t.TempDir(), "full.sforum.zip")
	full, err := buildExtensionPackage(root, fullZip, packageBuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fullNames := zipEntryNames(t, full.ZipPath)
	for _, want := range []string{"backend/main.go", "backend/go.mod", "frontend/Widget.vue", "testdata/fixture.json"} {
		if !fullNames[want] {
			t.Fatalf("default package missing source file %s; got %#v", want, fullNames)
		}
	}

	// --exclude-source：只保留运行时与说明文件
	releaseZip := filepath.Join(t.TempDir(), "release.sforum.zip")
	release, err := buildExtensionPackage(root, releaseZip, packageBuildOptions{ExcludeSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if release.SkippedCount < 4 {
		t.Fatalf("expected source skips, got %#v", release)
	}
	names := zipEntryNames(t, release.ZipPath)
	for _, want := range []string{"sforum.extension.json", "backend/plugin", "frontend/settings.mjs", "README.md"} {
		if !names[want] {
			t.Fatalf("release package missing %s; got %#v", want, names)
		}
	}
	for _, deny := range []string{"backend/main.go", "backend/go.mod", "frontend/Widget.vue", "frontend/settings.mjs.map", "testdata/fixture.json"} {
		if names[deny] {
			t.Fatalf("release package should omit %s; got %#v", deny, names)
		}
	}
}

func TestIsPackageSourceFile(t *testing.T) {
	cases := map[string]bool{
		"backend/main.go":           true,
		"backend/go.mod":            true,
		"backend/plugin":            false,
		"frontend/settings.mjs":     false,
		"frontend/settings.css":     false,
		"frontend/settings.mjs.map": true,
		"frontend/Widget.vue":       true,
		"manifest/settings.json":    false,
		"sforum.extension.json":     false,
		"README.md":                 false,
	}
	for path, want := range cases {
		if got := isPackageSourceFile(path); got != want {
			t.Fatalf("isPackageSourceFile(%q)=%v want %v", path, got, want)
		}
	}
}

func zipEntryNames(t *testing.T, zipPath string) map[string]bool {
	t.Helper()
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := make(map[string]bool, len(r.File))
	for _, f := range r.File {
		out[f.Name] = true
	}
	return out
}

// TestReferenceSEOPackageIsInstallableViaFormalDigestAndZip proves P13
// independent installability: build binary → extension digest --write
// (auto-materialize tmpl) → extension package ZIP → LoadPackage.
// 不得手算 SHA 替换 token。
func TestReferenceSEOPackageIsInstallableViaFormalDigestAndZip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reference package build in short mode")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	fixture := filepath.Join(repoRoot, "extensions/fixtures/plugins/sforum-seo-reference")
	pkgRoot := filepath.Join(t.TempDir(), "sforum.seo-reference")
	if err := os.CopyFS(pkgRoot, os.DirFS(fixture)); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	// 本地 go.mod replace 指向仓库 apps/api（开发构建需要，非 digest token）。
	goModPath := filepath.Join(pkgRoot, "backend", "go.mod")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	goMod = []byte(strings.ReplaceAll(string(goMod), "../../../../../apps/api", filepath.Join(repoRoot, "apps/api")))
	if err := os.WriteFile(goModPath, goMod, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(pkgRoot, "backend", "plugin")
	build := exec.Command("go", "build", "-mod=mod", "-trimpath", "-buildvcs=false", "-o", binaryPath, ".")
	build.Dir = filepath.Join(pkgRoot, "backend")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build backend: %v\n%s", err, out)
	}
	// 故意不写 sforum.extension.json：digest --write 必须从 .tmpl materialize。
	if _, err := os.Stat(filepath.Join(pkgRoot, extensionmanifest.ManifestFileName)); err == nil {
		_ = os.Remove(filepath.Join(pkgRoot, extensionmanifest.ManifestFileName))
	}
	digestCmd := newRootCommand()
	digestCmd.SetArgs([]string{"extension", "digest", "--write", pkgRoot})
	var digestOut strings.Builder
	digestCmd.SetOut(&digestOut)
	digestCmd.SetErr(&digestOut)
	if err := digestCmd.Execute(); err != nil {
		t.Fatalf("extension digest --write: %v\n%s", err, digestOut.String())
	}
	if !strings.Contains(digestOut.String(), "materialized") {
		t.Fatalf("expected tmpl materialize, got: %s", digestOut.String())
	}
	// Formal package ZIP（生产 CLI，无测试专用 Host shortcut）。
	zipPath := filepath.Join(t.TempDir(), "sforum.seo-reference.sforum.zip")
	pkgCmd := newRootCommand()
	pkgCmd.SetArgs([]string{"extension", "package", pkgRoot, "-o", zipPath})
	var pkgOut strings.Builder
	pkgCmd.SetOut(&pkgOut)
	pkgCmd.SetErr(&pkgOut)
	if err := pkgCmd.Execute(); err != nil {
		t.Fatalf("extension package: %v\n%s", err, pkgOut.String())
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatal(err)
	}
	extractRoot := filepath.Join(t.TempDir(), "extracted")
	if err := unzipTo(t, zipPath, extractRoot); err != nil {
		t.Fatal(err)
	}
	loadRoot := extractRoot
	if _, err := os.Stat(filepath.Join(extractRoot, extensionmanifest.ManifestFileName)); err != nil {
		entries, _ := os.ReadDir(extractRoot)
		if len(entries) == 1 && entries[0].IsDir() {
			loadRoot = filepath.Join(extractRoot, entries[0].Name())
		}
	}
	manifest, err := extensionmanifest.LoadPackage(loadRoot)
	if err != nil {
		t.Fatalf("LoadPackage from ZIP extract: %v root=%s", err, loadRoot)
	}
	if manifest.ID != "sforum.seo-reference" {
		t.Fatalf("id = %s", manifest.ID)
	}
	if len(manifest.SEO) < 6 {
		t.Fatalf("SEO surfaces = %d", len(manifest.SEO))
	}
	if manifest.Backend.Digest == "" || strings.Trim(manifest.Backend.Digest, "0") == "" {
		t.Fatalf("backend digest must be real (not all zeros): %q", manifest.Backend.Digest)
	}
	t.Logf("coverage.installable_zip=seo digest+package+LoadPackage digest=%s", manifest.Backend.Digest)
}

func unzipTo(t *testing.T, zipPath, dest string) error {
	t.Helper()
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		target := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)+string(os.PathSeparator)) &&
			filepath.Clean(target) != filepath.Clean(dest) {
			return os.ErrInvalid
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := out.ReadFrom(rc)
		_ = out.Close()
		_ = rc.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}
