package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtensionTestCommandEventsFixture(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	fixture := filepath.Join(repoRoot, "extensions/fixtures/plugins/sforum-contract-events")

	cmd := newRootCommand()
	cmd.SetArgs([]string{"extension", "test", fixture})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("extension test: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "PASS") {
		t.Fatalf("expected PASS:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "sforum.contract.events") {
		t.Fatalf("expected id:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "event.known") {
		t.Fatalf("expected event.known check:\n%s", out.String())
	}
}

func TestExtensionTestCommandSMTPPackage(t *testing.T) {
	// V3 packageFiles 绑定真实二进制摘要；在临时目录构建并刷新 digest，
	// 避免依赖 gitignored 工作树产物或平台摘要漂移。
	smtpRoot := prepareBuiltinPluginPackage(t, "sforum-smtp")

	cmd := newRootCommand()
	cmd.SetArgs([]string{"extension", "test", smtpRoot})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("extension test smtp: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "sforum.smtp") {
		t.Fatalf("expected smtp id:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "provider.slot_ok") {
		t.Fatalf("expected provider.slot_ok check:\n%s", out.String())
	}
}

// prepareBuiltinPluginPackage 复制受保护内置插件到临时目录，构建 Linux 后端并刷新 V3 digest。
func prepareBuiltinPluginPackage(t *testing.T, pluginDirName string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	sourceRoot := filepath.Join(repoRoot, "extensions/builtin/plugins", pluginDirName)
	pkgRoot := filepath.Join(t.TempDir(), pluginDirName)
	if err := os.CopyFS(pkgRoot, os.DirFS(sourceRoot)); err != nil {
		t.Fatalf("copy %s package: %v", pluginDirName, err)
	}
	backendRoot := filepath.Join(sourceRoot, "backend")
	binary := filepath.Join(pkgRoot, "backend", "plugin")
	// 删除可能从源树拷入的本地产物，强制使用本次确定性构建。
	_ = os.Remove(binary)
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binary, ".")
	build.Dir = backendRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s backend: %v\n%s", pluginDirName, err, output)
	}
	digestCmd := newRootCommand()
	digestCmd.SetArgs([]string{"extension", "digest", "--write", pkgRoot})
	var digestOut strings.Builder
	digestCmd.SetOut(&digestOut)
	digestCmd.SetErr(&digestOut)
	if err := digestCmd.Execute(); err != nil {
		t.Fatalf("refresh %s digest: %v\n%s", pluginDirName, err, digestOut.String())
	}
	return pkgRoot
}

func TestExtensionTestCommandContentPolicyV2Package(t *testing.T) {
	// 额外证明 Linux 后端构建可复现，再走 extension test 契约门禁。
	pkgRoot := prepareBuiltinPluginPackage(t, "sforum-content-policy")
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	sourceBackend := filepath.Join(repoRoot, "extensions/builtin/plugins/sforum-content-policy/backend")
	firstBody, err := os.ReadFile(filepath.Join(pkgRoot, "backend", "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	secondBinary := filepath.Join(t.TempDir(), "plugin")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", secondBinary, ".")
	build.Dir = sourceBackend
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build reproducible Linux content-policy v2: %v\n%s", err, output)
	}
	secondBody, err := os.ReadFile(secondBinary)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatalf("content-policy Linux build is not reproducible: %x != %x", sha256.Sum256(firstBody), sha256.Sum256(secondBody))
	}

	cmd := newRootCommand()
	cmd.SetArgs([]string{"extension", "test", pkgRoot})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("extension test content-policy: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "sforum.content-policy") {
		t.Fatalf("expected content-policy id:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "event.known") {
		t.Fatalf("expected event.known checks:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "contribution.point_ok") {
		t.Fatalf("expected contribution checks:\n%s", out.String())
	}
}

func TestDockerBuildsProtectedBuiltinBackendsAndValidatesV3Digest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dockerfile := filepath.Join(filepath.Dir(file), "../../Dockerfile")
	body, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.HasPrefix(text, "FROM golang:1.26.11-alpine AS base\n") {
		t.Fatal("Dockerfile must keep the RV-18 Go toolchain base as its first line")
	}
	if count := strings.Count(text, "CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /usr/local/bin/sforum ./cmd/sforum"); count != 1 {
		t.Errorf("Dockerfile builds the sforum CLI %d times, want exactly once", count)
	}
	if strings.Contains(text, "go run ./cmd/sforum") {
		t.Error("Dockerfile must not recompile the sforum CLI through go run")
	}
	for _, required := range []string{
		`jobs="${SFORUM_BUILTIN_BUILD_JOBS:-4}"`,
		`pkg="/app/extensions/builtin/plugins/$id"`,
		`cd "$pkg/backend"`,
		`CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin .`,
		`sforum extension digest --write "$pkg"`,
		`sforum extension validate "$pkg"`,
		`sforum extension test "$pkg"`,
		"wait_batch $batch",
		"kill \"$other\"",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Dockerfile is missing protected builtin gate %q", required)
		}
	}
	for _, plugin := range []string{
		"sforum-smtp:0",
		"sforum-content-policy:1",
		"sforum-storage-fs:0",
		"sforum-storage-s3:0",
		"sforum-search-site:0",
		"sforum-auth-github:0",
		"sforum-web-push:0",
		"sforum-shortcodes:0",
	} {
		if !strings.Contains(text, plugin) {
			t.Errorf("Dockerfile concurrency list is missing protected builtin %q", plugin)
		}
	}
	const builtinCopy = "COPY --from=build --chown=sforum:sforum /app/extensions/builtin /app/extensions/builtin"
	if count := strings.Count(text, builtinCopy); count != 1 {
		t.Errorf("Dockerfile copies protected builtins into %d final images, want only api", count)
	}
	for _, required := range []string{
		"FROM golang:1.26.11-alpine AS migrate-build",
		"COPY --from=migrate-build /out/sforum-migrate /usr/local/bin/sforum-migrate",
		"-o /out/sforum-api ./cmd/api",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Dockerfile is missing build-stage contract %q", required)
		}
	}
	if strings.Contains(text, "AS worker") || strings.Contains(text, "sforum-worker") {
		t.Error("Dockerfile must not retain the retired standalone worker target")
	}
	migrateBuild := text[strings.Index(text, "FROM golang:1.26.11-alpine AS migrate-build"):strings.Index(text, "FROM alpine:3.24 AS api")]
	if strings.Contains(migrateBuild, "extensions/builtin") {
		t.Error("migrate-build must not build or copy protected builtins")
	}
}

func TestBuiltinDevStagingSyncPrunesRemovedExcludedPluginBinary(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync is not available")
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	script := filepath.Join(repoRoot, "scripts/build-builtin-plugins.sh")

	fakeRoot := t.TempDir()
	sourcePlugin := filepath.Join(fakeRoot, "extensions/builtin/plugins/sforum-current")
	if err := os.MkdirAll(sourcePlugin, 0o755); err != nil {
		t.Fatalf("create source plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourcePlugin, "sforum.extension.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write source manifest: %v", err)
	}

	stagingRoot := filepath.Join(t.TempDir(), "builtin-dev")
	staleBinary := filepath.Join(stagingRoot, "plugins/sforum-removed/backend/plugin")
	if err := os.MkdirAll(filepath.Dir(staleBinary), 0o755); err != nil {
		t.Fatalf("create stale plugin directory: %v", err)
	}
	if err := os.WriteFile(staleBinary, []byte("stale"), 0o755); err != nil {
		t.Fatalf("write stale plugin binary: %v", err)
	}

	cmd := exec.Command("bash", "-c", `
source "$1"
ROOT_DIR="$2"
STAGING_ROOT="$3"
prepare_staging_tree
`, "bash", script, fakeRoot, stagingRoot)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepare staging tree: %v\n%s", err, output)
	}

	if _, err := os.Stat(filepath.Join(stagingRoot, "plugins/sforum-removed")); !os.IsNotExist(err) {
		t.Fatalf("removed plugin survived staging sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingRoot, "plugins/sforum-current/sforum.extension.json")); err != nil {
		t.Fatalf("current plugin was not synchronized: %v", err)
	}
}
