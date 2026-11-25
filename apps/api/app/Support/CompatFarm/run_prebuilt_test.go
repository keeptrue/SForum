package compatfarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestLoadVerifiedPrebuiltPluginUsesCleanSourceArtifact(t *testing.T) {
	repo, prebuiltRoot := preparePrebuiltPluginFixture(t)

	ext, path, ok, err := loadVerifiedPrebuiltPlugin(repo, prebuiltRoot, "sforum-smtp")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("verified prebuilt plugin was not reused")
	}
	if path != filepath.Join(prebuiltRoot, "plugins", "sforum-smtp") {
		t.Fatalf("prebuilt path = %q", path)
	}
	if len(ext.PackageDigest) != 64 {
		t.Fatalf("package digest = %q", ext.PackageDigest)
	}
	if _, err := extensions.VerifyRuntimeExecutableDigest(ext); err != nil {
		t.Fatalf("reused plugin failed executable verification: %v", err)
	}
}

func TestLoadVerifiedPrebuiltPluginSkipsDirtySource(t *testing.T) {
	repo, prebuiltRoot := preparePrebuiltPluginFixture(t)
	if err := os.WriteFile(
		filepath.Join(repo, "apps", "api", "dirty-source.txt"),
		[]byte("dirty"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	_, _, ok, err := loadVerifiedPrebuiltPlugin(repo, prebuiltRoot, "sforum-smtp")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("dirty source tree must build instead of reusing prebuilt artifacts")
	}
}

func TestLoadVerifiedPrebuiltPluginRejectsTamperedArtifact(t *testing.T) {
	repo, prebuiltRoot := preparePrebuiltPluginFixture(t)
	pluginPath := filepath.Join(prebuiltRoot, "plugins", "sforum-smtp", "backend", "plugin")
	if err := os.WriteFile(pluginPath, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, ok, err := loadVerifiedPrebuiltPlugin(repo, prebuiltRoot, "sforum-smtp")
	if ok {
		t.Fatal("tampered prebuilt artifact was accepted")
	}
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestBuildBuiltinPluginExplicitOverrideSkipsPrebuiltRoot(t *testing.T) {
	root := repoRoot(t)
	source := filepath.Join(root, "extensions", "builtin", "plugins", "sforum-smtp")
	_, path, err := buildBuiltinPlugin(
		context.Background(),
		root,
		t.TempDir(),
		"sforum-smtp",
		source,
		filepath.Join(t.TempDir(), "missing-prebuilt-root"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(path) == filepath.Clean(source) {
		t.Fatalf("build used source override directly instead of an isolated copy: %s", path)
	}
}

func preparePrebuiltPluginFixture(t *testing.T) (string, string) {
	t.Helper()
	sourcePackage := filepath.Join(repoRoot(t), "extensions", "builtin", "plugins", "sforum-smtp")
	repo := t.TempDir()
	sourceCopy := filepath.Join(repo, "extensions", "builtin", "plugins", "sforum-smtp")
	if err := copyDir(sourcePackage, sourceCopy); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "apps", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "compat-farm@example.test")
	runGit(t, repo, "config", "user.name", "Compat Farm")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "fixture")

	prebuiltRoot := filepath.Join(repo, "prebuilt")
	prebuiltPackage := filepath.Join(prebuiltRoot, "plugins", "sforum-smtp")
	if err := copyDir(sourceCopy, prebuiltPackage); err != nil {
		t.Fatal(err)
	}
	pluginPath := filepath.Join(prebuiltPackage, "backend", "plugin")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := []byte("prebuilt-plugin-fixture")
	if err := os.WriteFile(pluginPath, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	manifestPath := filepath.Join(prebuiltPackage, "sforum.extension.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		manifestPath,
		rewriteExecutableDigests(raw, hex.EncodeToString(digest[:])),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	return repo, prebuiltRoot
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
