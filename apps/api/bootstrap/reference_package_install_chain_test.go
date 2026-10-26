package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	"github.com/keeptrue/sforum/apps/api/database/migrator"
)

// TestReferenceSEOFormalZipUploadTrustEnableRestartDisableUpgradeUninstall
// 证明参考包经正式 CLI 打包后，走生产 Service 全链：
// ZIP → inert install → super_admin trust → enable → 子进程启动 → restart →
// staged upgrade → rollback（DiscardStaged）→ disable → uninstall。
//
// 使用 PostgresStore、PostgresExecutableTrustStore、真实 Manager（ProtocolStarter）
// 与生命周期仓库；禁止 fake runtime / 测试权限捷径 / 手工 seed 扩展行。
func TestReferenceSEOFormalZipUploadTrustEnableRestartDisableUpgradeUninstall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reference formal ZIP install chain in short mode")
	}
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		databaseURL = "postgres://sforum:sforum@127.0.0.1:15432/sforum?sslmode=disable"
	}
	ctx := context.Background()
	database := newThemeE2EDatabase(t, databaseURL)
	if err := migrator.Up(ctx, migrator.Config{DatabaseURL: database.url}); err != nil {
		t.Fatalf("migrate isolated formal ZIP database: %v", err)
	}
	pool, err := pgxpool.New(ctx, database.url)
	if err != nil {
		t.Fatalf("postgres required for formal ZIP chain: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("postgres ping failed (formal ZIP must use real PG): %v", err)
	}

	// 真实 users 行：PostgresExecutableTrustStore 对 actor_user_id 有外键。
	super, tech := seedFormalZipActors(t, ctx, pool)
	repoRoot := formalZipRepoRoot(t)
	fixture := filepath.Join(repoRoot, "extensions/fixtures/plugins/sforum-seo-reference")
	// 每次运行使用唯一 semver 构建元数据，避免共享库同 digest 复用已删除的 package_path。
	runTag := fmt.Sprintf("%d", time.Now().UnixNano())
	versionV1 := "1.1.0+" + runTag
	versionV2 := "1.1.1+" + runTag

	// --- 构建 v1/v2 可上传 ZIP（正式 digest --write + package）---
	zipV1 := buildReferenceFormalZip(t, repoRoot, fixture, versionV1, "")
	zipV2 := buildReferenceFormalZip(t, repoRoot, fixture, versionV2, "")

	extensionRoot := t.TempDir()
	store := extensions.NewPostgresStore(pool)
	trustStore := extensions.NewPostgresExecutableTrustStore(pool)
	trust := extensions.NewExecutableTrustService(store, trustStore)
	// 真实 Manager：Enable 必须启动子进程（Protocol V2），禁止 recording 替身。
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: trust,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	service := extensions.NewServiceWithOptions(
		store, extensionRoot, "", manager,
		extensions.WithExecutableTrust(trust, true),
	)
	// 若共享库残留同 ID，先 disable/uninstall 再装（publication 历史时身份可保留）。
	if existing, getErr := store.Get(ctx, "sforum.seo-reference"); getErr == nil {
		if existing.Status == extensions.StatusEnabled {
			_, _ = service.Disable(ctx, super, existing.ID)
		}
		_ = service.Uninstall(ctx, super, existing.ID, extensions.UninstallInput{RetainPackage: true})
		// 硬删除若被 RESTRICT 挡住：后续 Install 会 stage；此时仍要求最终 Version 为本次 runTag。
	}

	zipBytesV1, err := os.ReadFile(zipV1)
	if err != nil {
		t.Fatal(err)
	}

	// --- super_admin 上传 + 静态预检（InstallArchive 惰性包，不执行代码）---
	// trust challenges 开启时，V3 允许 plugin.manage 做 inert 安装；执行边界在 trust/enable。
	installResult, err := service.InstallOrUpgradeArchive(ctx, super, extensions.ArchiveInput{
		FileName: "sforum.seo-reference.sforum.zip", Data: zipBytesV1,
	})
	if err != nil {
		t.Fatalf("super_admin InstallArchive: %v", err)
	}
	installed := installResult.Extension
	// 共享库残留活动版本时，新包会 stage：CAS 晋升为活动候选，保证后续 trust/enable 指向本次制品。
	if installed.StagedVersion != nil && installed.StagedVersion.Version == versionV1 {
		promoted, promoteErr := store.PromoteStagedVersion(ctx, extensions.StagedVersionCASInput{
			ExtensionID:                 installed.ID,
			ExpectedActiveVersionID:     installed.ActiveVersionID,
			ExpectedActiveVersion:       installed.Version,
			ExpectedActivePackageDigest: installed.PackageDigest,
			ExpectedStagedVersionID:     installed.StagedVersion.ID,
			ExpectedStagedVersion:       installed.StagedVersion.Version,
			ExpectedPackageDigest:       installed.StagedVersion.PackageDigest,
		})
		if promoteErr != nil {
			t.Fatalf("promote staged initial install: %v", promoteErr)
		}
		installed = promoted
	}
	if installed.ID != "sforum.seo-reference" || installed.Source != extensions.SourceUploaded {
		t.Fatalf("installed = %#v", installed)
	}
	if installed.Version != versionV1 {
		t.Fatalf("installed version = %s want %s", installed.Version, versionV1)
	}
	if installed.Status == extensions.StatusEnabled {
		t.Fatal("static install must not auto-enable")
	}
	if installed.PackageDigest == "" {
		t.Fatal("package digest missing after install")
	}
	if _, err := store.EnsureInitialPluginRuntimePublication(ctx); err != nil {
		t.Fatalf("ensure plugin runtime genesis before manual enable: %v", err)
	}
	assertPluginRuntimeGenesisHeader(t, ctx, pool)
	// 清理：测试结束卸载（若仍存在）。
	t.Cleanup(func() {
		_ = manager.Stop(context.Background(), installed)
		_ = service.Uninstall(context.Background(), super, installed.ID, extensions.UninstallInput{})
	})

	// --- super_admin trust challenge + enable ---
	challenge, err := trust.Challenge(ctx, super, installed.ID)
	if err != nil {
		t.Fatalf("trust challenge: %v", err)
	}
	if challenge.Token == "" {
		t.Fatal("empty trust challenge token")
	}
	// 拒绝：无 token enable
	if _, err := service.Enable(ctx, super, installed.ID, extensions.EnableInput{
		ConfirmCapabilities: true,
	}); err == nil {
		t.Fatal("enable without confirmation token must fail")
	}
	// 拒绝：tech_admin 不得 challenge / enable 可执行后端（硬失败，非 t.Log）。
	if _, err := trust.Challenge(ctx, tech, installed.ID); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("tech_admin trust challenge must deny: %v", err)
	}
	if _, err := service.Enable(ctx, tech, installed.ID, extensions.EnableInput{
		ConfirmCapabilities: true,
		ConfirmationToken:   challenge.Token,
	}); err == nil {
		// 硬失败：tech_admin 不得启用可执行后端（inert 安装边界在 trust/enable）。
		t.Fatal("tech_admin must not enable executable backend package")
	}
	enabled, err := service.Enable(ctx, super, installed.ID, extensions.EnableInput{
		ConfirmCapabilities: true,
		ConfirmationToken:   challenge.Token,
	})
	if err != nil {
		t.Fatalf("enable with trust: %v", err)
	}
	if enabled.Status != extensions.StatusEnabled {
		t.Fatalf("status after enable = %s", enabled.Status)
	}
	// 子进程证据：ActiveRuntimeInstance 必须有真实 InstanceID（非 HTTP BaseURL 替身）。
	active, err := manager.ActiveRuntimeInstance(enabled.ID)
	if err != nil {
		t.Fatalf("enable must start real subprocess: %v", err)
	}
	if strings.TrimSpace(active.Identity.InstanceID) == "" {
		t.Fatalf("missing runtime instance after enable: %#v", active)
	}
	if active.Target.BaseURL != "" {
		t.Fatalf("protocol v2 must not expose HTTP BaseURL target: %#v", active.Target)
	}
	status := manager.Status(ctx, enabled)
	if status.State != extensions.RuntimeRunning {
		t.Fatalf("runtime status after enable = %#v", status)
	}

	// --- restart：disable + enable（再次 challenge）---
	disabled, err := service.Disable(ctx, super, installed.ID)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.Status != extensions.StatusDisabled {
		t.Fatalf("status after disable = %s", disabled.Status)
	}
	if _, err := manager.ActiveRuntimeInstance(installed.ID); err == nil {
		t.Fatal("runtime must stop after disable")
	}
	restartChallenge, err := trust.Challenge(ctx, super, installed.ID)
	if err != nil {
		t.Fatalf("restart challenge: %v", err)
	}
	restarted, err := service.Enable(ctx, super, installed.ID, extensions.EnableInput{
		ConfirmCapabilities: true,
		ConfirmationToken:   restartChallenge.Token,
	})
	if err != nil {
		t.Fatalf("restart enable: %v", err)
	}
	if restarted.Status != extensions.StatusEnabled {
		t.Fatalf("status after restart = %s", restarted.Status)
	}
	if _, err := manager.ActiveRuntimeInstance(restarted.ID); err != nil {
		t.Fatalf("restart must start subprocess again: %v", err)
	}

	// --- upgrade：上传新版本 ZIP，静态 stage（不碰活动 runtime）---
	zipBytesV2, err := os.ReadFile(zipV2)
	if err != nil {
		t.Fatal(err)
	}
	beforeUpgradeInstance, err := manager.ActiveRuntimeInstance(restarted.ID)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := service.InstallOrUpgradeArchive(ctx, super, extensions.ArchiveInput{
		FileName: "sforum.seo-reference-1.1.1.sforum.zip", Data: zipBytesV2,
	})
	if err != nil {
		t.Fatalf("upgrade archive: %v", err)
	}
	if !upgraded.Upgraded || upgraded.Extension.StagedVersion == nil {
		t.Fatalf("expected staged upgrade: %#v", upgraded)
	}
	if upgraded.Extension.StagedVersion.Version != versionV2 {
		t.Fatalf("staged version = %s want %s", upgraded.Extension.StagedVersion.Version, versionV2)
	}
	// 静态 stage 不得替换活动 runtime 实例。
	afterStageInstance, err := manager.ActiveRuntimeInstance(restarted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStageInstance.Identity.InstanceID != beforeUpgradeInstance.Identity.InstanceID {
		t.Fatalf("staged install must not restart active runtime: before=%s after=%s",
			beforeUpgradeInstance.Identity.InstanceID, afterStageInstance.Identity.InstanceID)
	}

	// --- rollback：丢弃 staged 候选（生产 CAS DiscardStagedVersion）---
	// SEO 无 lifecycle V2 时 Service.Rollback 必须硬失败；staged 回退走 store CAS。
	if _, err := service.Rollback(ctx, super, installed.ID, extensions.RollbackInput{
		TargetVersion: versionV1, TargetPackageDigest: installed.PackageDigest,
		IdempotencyKey: "seo-rollback-1",
	}); err == nil {
		t.Fatal("SEO without lifecycle V2 must not silently Service.Rollback")
	}
	staged := upgraded.Extension.StagedVersion
	discarded, err := store.DiscardStagedVersion(ctx, extensions.StagedVersionCASInput{
		ExtensionID:                 installed.ID,
		ExpectedActiveVersionID:     upgraded.Extension.ActiveVersionID,
		ExpectedActiveVersion:       upgraded.Extension.Version,
		ExpectedActivePackageDigest: upgraded.Extension.PackageDigest,
		ExpectedStagedVersionID:     staged.ID,
		ExpectedStagedVersion:       staged.Version,
		ExpectedPackageDigest:       staged.PackageDigest,
	})
	if err != nil {
		t.Fatalf("DiscardStagedVersion (staged rollback): %v", err)
	}
	if discarded.StagedVersion != nil {
		t.Fatalf("staged must be cleared after discard: %#v", discarded.StagedVersion)
	}
	if discarded.Version != upgraded.Extension.Version || discarded.PackageDigest != upgraded.Extension.PackageDigest {
		t.Fatalf("active version must stay after staged discard: %#v", discarded)
	}

	// --- disable + uninstall ---
	if _, err := service.Disable(ctx, super, installed.ID); err != nil {
		t.Fatalf("disable before uninstall: %v", err)
	}
	if _, err := manager.ActiveRuntimeInstance(installed.ID); err == nil {
		t.Fatal("runtime must be stopped before uninstall")
	}
	// 从 store 再读 PackagePath（Postgres 权威）。
	current, err := store.Get(ctx, installed.ID)
	if err != nil {
		t.Fatalf("get before uninstall: %v", err)
	}
	packagePath := current.PackagePath
	if err := service.Uninstall(ctx, super, installed.ID, extensions.UninstallInput{}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	// 硬删除：无 publication 历史时行消失；有历史时身份保留但必须 disabled 且包文件已删。
	after, getErr := store.Get(ctx, installed.ID)
	if getErr == nil {
		if after.Status == extensions.StatusEnabled {
			t.Fatalf("uninstalled identity must not stay enabled: %#v", after)
		}
	} else if !errors.Is(getErr, extensions.ErrExtensionNotFound) {
		t.Fatalf("unexpected get after uninstall: %v", getErr)
	}
	if packagePath != "" {
		if _, err := os.Stat(packagePath); err == nil {
			t.Fatalf("package path must be removed after uninstall: %s", packagePath)
		} else if !errors.Is(err, os.ErrNotExist) && !os.IsNotExist(err) {
			t.Fatalf("package path after uninstall: %v (path=%s)", err, packagePath)
		}
	}
	// 卸载后不得再无活动子进程。
	if _, err := manager.ActiveRuntimeInstance(installed.ID); err == nil {
		t.Fatal("runtime must remain stopped after uninstall")
	}
}

// seedFormalZipActors 写入真实 users 行并返回 super_admin / tech_admin Actor。
// 禁止使用硬编码 ID=42 等不存在于 PostgreSQL 的捷径。
func seedFormalZipActors(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (super, tech identity.Actor) {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var superID, techID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Formal ZIP Super') RETURNING id
	`, "formal_super_"+suffix, "formal_super_"+suffix+"@example.test").Scan(&superID); err != nil {
		t.Fatalf("insert super_admin user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Formal ZIP Tech') RETURNING id
	`, "formal_tech_"+suffix, "formal_tech_"+suffix+"@example.test").Scan(&techID); err != nil {
		t.Fatalf("insert tech_admin user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = ANY($1::bigint[])`, []int64{superID, techID})
	})
	super = identity.Actor{
		ID: superID, Status: identity.UserStatusActive,
		RoleKeys: []string{identity.RoleSuperAdmin},
	}
	tech = identity.Actor{
		ID: techID, Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionPluginManage: true,
		},
	}
	return super, tech
}

func assertPluginRuntimeGenesisHeader(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var reason extensions.PluginRuntimePublicationReason
	var actorIsNull bool
	if err := pool.QueryRow(ctx, `
		SELECT reason, actor_user_id IS NULL
		FROM plugin_runtime_publications
		ORDER BY revision ASC
		LIMIT 1
	`).Scan(&reason, &actorIsNull); err != nil {
		t.Fatalf("load plugin runtime genesis: %v", err)
	}
	if reason != extensions.PluginRuntimePublicationStartupReconcile || !actorIsNull {
		t.Fatalf("unexpected plugin runtime genesis header: reason=%s actorIsNull=%t", reason, actorIsNull)
	}
}

// buildReferenceFormalZip 复制 fixture → 构建 backend → 正式 digest --write → package。
// version / extensionID 非空时改写 tmpl（仍由 digest 刷新摘要）。
func buildReferenceFormalZip(t *testing.T, repoRoot, fixture, version, extensionID string) string {
	t.Helper()
	pkgRoot := filepath.Join(t.TempDir(), "pkg-"+version)
	if err := os.CopyFS(pkgRoot, os.DirFS(fixture)); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
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
	// 确保仅从 tmpl materialize（禁止手写 digest）。
	_ = os.Remove(filepath.Join(pkgRoot, "sforum.extension.json"))

	tmplPath := filepath.Join(pkgRoot, "sforum.extension.json.tmpl")
	tmpl, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(tmpl)
	if extensionID != "" {
		updated = strings.Replace(updated, `"id": "sforum.seo-reference"`, `"id": "`+extensionID+`"`, 1)
	}
	if version != "" {
		// 替换默认 1.1.0（正式 digest 会重算 digest）。
		next := strings.Replace(updated, `"version": "1.1.0"`, `"version": "`+version+`"`, 1)
		if next == updated {
			t.Fatalf("failed to rewrite version to %s in tmpl", version)
		}
		updated = next
	}
	if err := os.WriteFile(tmplPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}

	apiRoot := filepath.Join(repoRoot, "apps/api")
	digestCmd := exec.Command("go", "run", "./cmd/sforum", "extension", "digest", "--write", pkgRoot)
	digestCmd.Dir = apiRoot
	digestCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := digestCmd.CombinedOutput(); err != nil {
		t.Fatalf("extension digest --write: %v\n%s", err, out)
	}
	// 若 version 仍需改写 materialize 后的 json。
	if version != "" {
		manifestPath := filepath.Join(pkgRoot, "sforum.extension.json")
		body, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"version": "`+version+`"`) {
			// 再写 version 并重新 digest。
			rewritten := rewriteFormalZipManifestVersion(string(body), version)
			if err := os.WriteFile(manifestPath, []byte(rewritten), 0o600); err != nil {
				t.Fatal(err)
			}
			digestCmd2 := exec.Command("go", "run", "./cmd/sforum", "extension", "digest", "--write", pkgRoot)
			digestCmd2.Dir = apiRoot
			if out, err := digestCmd2.CombinedOutput(); err != nil {
				t.Fatalf("extension digest --write (version): %v\n%s", err, out)
			}
		}
	}
	outZip := filepath.Join(t.TempDir(), "pkg-"+version+".sforum.zip")
	pkgCmd := exec.Command("go", "run", "./cmd/sforum", "extension", "package", pkgRoot, "--output", outZip)
	pkgCmd.Dir = apiRoot
	if out, err := pkgCmd.CombinedOutput(); err != nil {
		t.Fatalf("extension package: %v\n%s", err, out)
	}
	if _, err := os.Stat(outZip); err != nil {
		t.Fatalf("package zip missing: %v", err)
	}
	return outZip
}

func rewriteFormalZipManifestVersion(body, version string) string {
	const marker = `"version"`
	idx := strings.Index(body, marker)
	if idx < 0 {
		return body
	}
	rest := body[idx+len(marker):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return body
	}
	rest = rest[colon+1:]
	q1 := strings.Index(rest, `"`)
	if q1 < 0 {
		return body
	}
	q2 := strings.Index(rest[q1+1:], `"`)
	if q2 < 0 {
		return body
	}
	start := idx + len(marker) + colon + 1 + q1 + 1
	end := start + q2
	return body[:start] + version + body[end:]
}

func formalZipRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// apps/api/bootstrap -> repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
