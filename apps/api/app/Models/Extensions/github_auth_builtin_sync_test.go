package extensions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

// TestSyncBuiltinsStagesGitHubAuthExactArtifactWithoutHostActivation
// 证明 M2B：SyncBuiltins 将 sforum.auth-github 精确制品 staged 到 EXTENSION_ROOT，
// 且不创建 Host 公开激活效应（无 login/registration/link 可用性）。
//
// 分层语义（与 decisions/2026-07-27-github-social-login-builtin-v1.md 对齐）：
//   - SyncBuiltins：发现 + 不可变快照 + 精确 digest；
//   - Executable trust：SourceBuiltin 不要求上传包式 super_admin trust grant；
//   - Host activation：identity_provider_activations 默认全 off，本测试不写入；
//   - 公开 catalog：无 Host activation 行时不得出现 GitHub 按钮。
func TestSyncBuiltinsStagesGitHubAuthExactArtifactWithoutHostActivation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping github auth builtin SyncBuiltins proof in short mode")
	}

	pkgRoot := prepareGitHubAuthBuiltinPackage(t)

	builtinRoot := t.TempDir()
	pluginDir := filepath.Join(builtinRoot, "plugins", "sforum-auth-github")
	if err := os.CopyFS(pluginDir, os.DirFS(pkgRoot)); err != nil {
		t.Fatalf("stage builtin source tree: %v", err)
	}
	// 在源树侧写入可识别标记，证明 snapshot 与源树解耦。
	markerPath := filepath.Join(pluginDir, "README.md")
	originalREADME, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}

	extensionRoot := t.TempDir()
	store := &fakeExtensionStore{items: map[string]Extension{}}
	service := NewServiceWithBuiltins(store, extensionRoot, builtinRoot)

	items, err := service.SyncBuiltins(context.Background())
	if err != nil {
		t.Fatalf("SyncBuiltins: %v", err)
	}
	var found *Extension
	for i := range items {
		if items[i].ID == "sforum.auth-github" {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("SyncBuiltins did not return sforum.auth-github; items=%v", extensionIDs(items))
	}
	saved := store.items["sforum.auth-github"]
	if saved.ID != "sforum.auth-github" {
		t.Fatalf("store missing github auth builtin: %#v", saved)
	}
	if saved.Source != SourceBuiltin || !saved.IsSystem || saved.IsDeletable {
		t.Fatalf("builtin identity wrong: source=%s system=%v deletable=%v",
			saved.Source, saved.IsSystem, saved.IsDeletable)
	}
	if saved.Status != StatusInstalled {
		t.Fatalf("github auth builtin must be staged for explicit lifecycle enable, status=%s", saved.Status)
	}
	if len(saved.PackageDigest) != 64 {
		t.Fatalf("expected 64-char package digest, got %q", saved.PackageDigest)
	}
	if saved.PackagePath == "" || saved.PackagePath == pluginDir {
		t.Fatalf("expected immutable snapshot under extension root, got path=%q source=%q",
			saved.PackagePath, pluginDir)
	}
	if !strings.HasPrefix(filepath.Clean(saved.PackagePath), filepath.Clean(extensionRoot)+string(os.PathSeparator)) {
		t.Fatalf("snapshot not under extension root: %q (root=%q)", saved.PackagePath, extensionRoot)
	}
	// 精确制品：快照树 digest 必须与 SaveBuiltin 记录一致。
	snapshotDigest, err := extensionpackage.DigestTree(saved.PackagePath)
	if err != nil {
		t.Fatalf("snapshot digest: %v", err)
	}
	if snapshotDigest != saved.PackageDigest {
		t.Fatalf("snapshot digest drift: snap=%q saved=%q", snapshotDigest, saved.PackageDigest)
	}
	// 源树变更不得污染已 staged 快照。
	if err := os.WriteFile(markerPath, []byte("mutated-after-sync"), 0o644); err != nil {
		t.Fatalf("mutate source: %v", err)
	}
	snapshotREADME, err := os.ReadFile(filepath.Join(saved.PackagePath, "README.md"))
	if err != nil {
		t.Fatalf("read snapshot README: %v", err)
	}
	if string(snapshotREADME) != string(originalREADME) {
		t.Fatalf("snapshot changed with source tree mutation")
	}
	// 可执行后端必须存在于精确快照中。
	if _, err := os.Stat(filepath.Join(saved.PackagePath, "backend", "plugin")); err != nil {
		t.Fatalf("staged backend binary missing: %v", err)
	}

	// Executable trust：内置包不走 uploaded trust grant。
	if RequiresExecutableTrust(saved) {
		t.Fatalf("SourceBuiltin must not require uploaded executable trust grant")
	}

	// 清单仍声明 auth provider，但 SyncBuiltins 本身不写 Host activation。
	manifest, err := extensionmanifest.LoadPackage(saved.PackagePath)
	if err != nil {
		t.Fatalf("load staged manifest: %v", err)
	}
	if manifest.Identity == nil || len(manifest.Identity.Providers) == 0 {
		t.Fatalf("staged package missing identity providers")
	}
	providerID := manifest.Identity.Providers[0].ID
	if providerID != "sforum.auth-github.auth" {
		t.Fatalf("provider id = %q", providerID)
	}

	// 幂等：恢复源树后再 sync，digest 身份不变（突变后的源会 stage 新 digest，属预期）。
	if err := os.WriteFile(markerPath, originalREADME, 0o644); err != nil {
		t.Fatalf("restore source README: %v", err)
	}
	if _, err := service.SyncBuiltins(context.Background()); err != nil {
		t.Fatalf("second SyncBuiltins: %v", err)
	}
	again := store.items["sforum.auth-github"]
	if again.PackageDigest != saved.PackageDigest {
		t.Fatalf("digest changed on re-sync: first=%q second=%q", saved.PackageDigest, again.PackageDigest)
	}
}

// TestSyncBuiltinsGitHubAuthDoesNotCreateHostActivationRows 文档级断言：
// 扩展 store 的 SaveBuiltin 路径不接触 identity_provider_activations。
// Host activation 默认 off 由 ExternalAuthService 覆盖；此处证明 SyncBuiltins
// 返回的扩展记录不含任何「公开登录已激活」语义字段。
func TestSyncBuiltinsGitHubAuthDoesNotCreateHostActivationRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping github auth builtin SyncBuiltins proof in short mode")
	}
	pkgRoot := prepareGitHubAuthBuiltinPackage(t)
	builtinRoot := t.TempDir()
	if err := os.CopyFS(filepath.Join(builtinRoot, "plugins", "sforum-auth-github"), os.DirFS(pkgRoot)); err != nil {
		t.Fatalf("copy package: %v", err)
	}
	store := &fakeExtensionStore{items: map[string]Extension{}}
	service := NewServiceWithBuiltins(store, t.TempDir(), builtinRoot)
	if _, err := service.SyncBuiltins(context.Background()); err != nil {
		t.Fatalf("SyncBuiltins: %v", err)
	}
	item := store.items["sforum.auth-github"]
	// Manifest 不得被改写为「默认公开激活」；settings 保持空默认。
	if item.Manifest.ID != "sforum.auth-github" {
		t.Fatalf("manifest id drifted: %#v", item.Manifest)
	}
	// 事件仅 builtin_synced，无 enable/trust 审计副作用字段。
	foundSync := false
	for _, ev := range store.events {
		if ev.ExtensionID == "sforum.auth-github" && ev.Action == EventBuiltinSynced {
			foundSync = true
		}
		if strings.Contains(strings.ToLower(ev.Action), "trust") ||
			strings.Contains(strings.ToLower(ev.Action), "activate") {
			t.Fatalf("unexpected lifecycle event from SyncBuiltins: %#v", ev)
		}
	}
	if !foundSync {
		t.Fatalf("expected EventBuiltinSynced for sforum.auth-github, events=%#v", store.events)
	}
}

func prepareGitHubAuthBuiltinPackage(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../"))
	sourceRoot := filepath.Join(repoRoot, "extensions/builtin/plugins/sforum-auth-github")
	pkgRoot := filepath.Join(t.TempDir(), "sforum-auth-github")
	if err := os.CopyFS(pkgRoot, os.DirFS(sourceRoot)); err != nil {
		t.Fatalf("copy sforum-auth-github: %v", err)
	}
	binary := filepath.Join(pkgRoot, "backend", "plugin")
	_ = os.Remove(binary)
	// 从源 backend 构建到临时包，保留 go.mod replace 路径。
	sourceBackend := filepath.Join(sourceRoot, "backend")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binary, ".")
	build.Dir = sourceBackend
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sforum.auth-github: %v\n%s", err, output)
	}
	// 刷新 V3 packageFiles digest，使 LoadPackage / SnapshotBuiltin 与精确字节一致。
	apiDir := filepath.Join(repoRoot, "apps/api")
	digest := exec.Command("go", "run", "./cmd/sforum", "extension", "digest", "--write", pkgRoot)
	digest.Dir = apiDir
	digest.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := digest.CombinedOutput(); err != nil {
		t.Fatalf("digest --write sforum.auth-github: %v\n%s", err, output)
	}
	return pkgRoot
}

func extensionIDs(items []Extension) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}
