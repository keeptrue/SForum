package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

// extensionGCPostgresFixture 提供只含版本 GC 所需表族的 schema 隔离库：
// 扩展/版本基底（202607050005 → 202607100004 → 202607140005）、开放操作判据
// 所需的 lifecycle 账本（202607140001）、发布引用判据所需的发布账本
// （202607160027）、GC 自身的锚点与确认表（202610170002），外加这些迁移外键
// 依赖的最小存根。
type extensionGCPostgresFixture struct {
	ctx         context.Context
	pool        *pgxpool.Pool
	schema      string
	databaseURL string // 带 search_path 的 URL：CLI 进程用它连进隔离 schema
	baseDir     string
	extensionID string
	versionIDs  map[string]int64
}

func newExtensionGCPostgresFixture(t *testing.T) *extensionGCPostgresFixture {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("extension_gc_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cleanupSchema := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	_, dbErr := db.ExecContext(ctx, `
		CREATE TABLE users (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE roles (id BIGSERIAL PRIMARY KEY, key TEXT NOT NULL);
		CREATE TABLE permissions (key TEXT PRIMARY KEY, module TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '');
		CREATE TABLE role_permissions (role_id BIGINT NOT NULL, permission_key TEXT NOT NULL, PRIMARY KEY (role_id, permission_key));
		CREATE TABLE extension_trust_grants (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE audit_events (
			id BIGSERIAL PRIMARY KEY,
			action TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb
		);
	`)
	if dbErr != nil {
		_ = db.Close()
		cleanupSchema()
		admin.Close()
		t.Fatal(dbErr)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		_ = db.Close()
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	for _, version := range []int64{
		202607050005, 202607100004, 202607140001, 202607140005, 202607160027, 202610170002,
	} {
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			_ = db.Close()
			cleanupSchema()
			admin.Close()
			t.Fatalf("apply migration %d: %v", version, err)
		}
	}
	_ = db.Close()

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}

	// CLI 进程只能拿到一个 URL 字符串：把 search_path 编码进 URL，
	// 让命令内部的 withRecoveryPool 也落进隔离 schema。
	binding := databaseURL
	if strings.Contains(binding, "?") {
		binding += "&"
	} else {
		binding += "?"
	}
	binding += "search_path=" + url.QueryEscape(schema+",public")

	fixture := &extensionGCPostgresFixture{
		ctx:         ctx,
		pool:        pool,
		schema:      schema,
		databaseURL: binding,
		baseDir:     t.TempDir(),
		extensionID: fmt.Sprintf("test.gc-cli.%d", time.Now().UnixNano()),
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupSchema()
		admin.Close()
	})
	fixture.seedSweepableExtension(t)
	return fixture
}

// seedSweepableExtension 直接落三行版本：v2 是 active（最新发布），v3 与 v1
// 是历史行；v1 installed_at 最早 → rank2 → 默认 retain=1 下可回收；v3 rank1
// → 窗口内挡。目录真实存在，且 v1 目录里有一个文件。
func (f *extensionGCPostgresFixture) seedSweepableExtension(t *testing.T) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extensions (id, type, name, status) VALUES ($1, 'plugin', 'GC CLI Fixture', 'enabled')
	`, f.extensionID); err != nil {
		t.Fatalf("seed extension: %v", err)
	}
	versions := []struct {
		version   string
		digest    string
		path      string
		installed string
	}{
		{"1.0.0", strings.Repeat("a", 64), filepath.Join(f.baseDir, "v1"), "now() - interval '30 days'"},
		{"2.0.0", strings.Repeat("b", 64), filepath.Join(f.baseDir, "v2"), "now() - interval '10 days'"},
		{"3.0.0", strings.Repeat("c", 64), filepath.Join(f.baseDir, "v3"), "now() - interval '5 days'"},
	}
	ids := map[string]int64{}
	for _, entry := range versions {
		if err := os.MkdirAll(entry.path, 0o755); err != nil {
			t.Fatalf("seed version dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(entry.path, "package.bin"), []byte(strings.Repeat("x", 256)), 0o644); err != nil {
			t.Fatalf("seed version file: %v", err)
		}
		var id int64
		if err := f.pool.QueryRow(f.ctx, fmt.Sprintf(`
			INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest, installed_at)
			VALUES ($1, $2, '{}'::jsonb, $3, $4, %s)
			RETURNING id
		`, entry.installed), f.extensionID, entry.version, entry.path, entry.digest).Scan(&id); err != nil {
			t.Fatalf("seed version %s: %v", entry.version, err)
		}
		ids[entry.version] = id
	}
	if _, err := f.pool.Exec(f.ctx, `
		UPDATE extensions SET active_version_id = $1 WHERE id = $2
	`, ids["2.0.0"], f.extensionID); err != nil {
		t.Fatalf("set active version: %v", err)
	}
	f.versionIDs = ids
}

func (f *extensionGCPostgresFixture) runGC(t *testing.T, args ...string) (string, error) {
	t.Helper()
	output := &bytes.Buffer{}
	cmd := newExtensionGCCommand()
	cmd.SetOut(output)
	cmd.SetErr(output)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func (f *extensionGCPostgresFixture) countVersions(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM extension_versions WHERE extension_id = $1`, f.extensionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

var extensionGCTokenPattern = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

func TestExtensionGCDryRunListsCandidatesAndEmitsToken(t *testing.T) {
	fixture := newExtensionGCPostgresFixture(t)
	before := fixture.countVersions(t)

	output, err := fixture.runGC(t, "--json", "--database-url", fixture.databaseURL)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	var report extensionGCDryRunReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("parse dry-run json: %v\n%s", err, output)
	}
	if report.Mode != "dry-run" || report.ReclaimableCount != 1 {
		t.Fatalf("dry-run report = mode %s reclaimable %d, want dry-run/1", report.Mode, report.ReclaimableCount)
	}
	if len(report.Confirmation.Token) != 64 {
		t.Fatalf("confirmation token missing from dry-run output")
	}
	if report.BlockedCounts[extensions.VersionGCBlockedWithinRetention] != 1 {
		t.Fatalf("blocked counts = %#v, want one within_retention", report.BlockedCounts)
	}
	// dry-run 零副作用：版本行数不变。
	if after := fixture.countVersions(t); after != before {
		t.Fatalf("dry-run mutated version rows: %d -> %d", before, after)
	}
}

func TestExtensionGCExecuteConsumesTokenOnceAndIsIdempotent(t *testing.T) {
	fixture := newExtensionGCPostgresFixture(t)

	output, err := fixture.runGC(t, "--json", "--database-url", fixture.databaseURL)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	var dry extensionGCDryRunReport
	if err := json.Unmarshal([]byte(output), &dry); err != nil {
		t.Fatalf("parse dry-run json: %v", err)
	}
	token := dry.Confirmation.Token

	resultJSON, err := fixture.runGC(t, "--json", "--confirm-token", token, "--database-url", fixture.databaseURL)
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, resultJSON)
	}
	var executed extensionGCExecuteReport
	if err := json.Unmarshal([]byte(resultJSON), &executed); err != nil {
		t.Fatalf("parse execute json: %v\n%s", err, resultJSON)
	}
	if executed.Mode != "execute" || len(executed.Result.Removed) != 1 || executed.Result.Removed[0].Version != "1.0.0" {
		t.Fatalf("execute result = %#v, want exactly v1 removed", executed.Result)
	}
	if executed.Result.BytesFreed <= 0 {
		t.Fatalf("bytes freed = %d, want > 0", executed.Result.BytesFreed)
	}
	// 台账行已删、锚点与审计可查。
	if after := fixture.countVersions(t); after != 2 {
		t.Fatalf("version rows after gc = %d, want 2", after)
	}
	var anchors, audits int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM extension_version_gc_removals WHERE extension_id = $1
	`, fixture.extensionID).Scan(&anchors); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM audit_events WHERE action = 'extension.version_gc'
	`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if anchors != 1 || audits != 1 {
		t.Fatalf("anchors=%d audits=%d, want 1/1", anchors, audits)
	}
	// token 一次性：重放拒绝。
	if _, err := fixture.runGC(t, "--json", "--confirm-token", token, "--database-url", fixture.databaseURL); err == nil {
		t.Fatalf("replayed token was accepted")
	}
	// 幂等：新 dry-run + execute 安全（无可回收候选）。
	secondDry, err := fixture.runGC(t, "--json", "--database-url", fixture.databaseURL)
	if err != nil {
		t.Fatalf("second dry-run: %v", err)
	}
	var second extensionGCDryRunReport
	if err := json.Unmarshal([]byte(secondDry), &second); err != nil {
		t.Fatalf("parse second dry-run: %v", err)
	}
	if second.ReclaimableCount != 0 {
		t.Fatalf("second plan reclaimable = %d, want 0", second.ReclaimableCount)
	}
	if _, err := fixture.runGC(t, "--json", "--confirm-token", second.Confirmation.Token, "--database-url", fixture.databaseURL); err != nil {
		t.Fatalf("idempotent execute: %v", err)
	}
}

func TestExtensionGCTokenBindsTheExactPlan(t *testing.T) {
	fixture := newExtensionGCPostgresFixture(t)

	output, err := fixture.runGC(t, "--json", "--database-url", fixture.databaseURL)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	var dry extensionGCDryRunReport
	if err := json.Unmarshal([]byte(output), &dry); err != nil {
		t.Fatalf("parse dry-run json: %v", err)
	}
	// 选项漂移：token 与 dry-run 计划绑定，换 --retain 即失效。
	if _, err := fixture.runGC(t, "--json", "--confirm-token", dry.Confirmation.Token, "--retain", "3", "--database-url", fixture.databaseURL); err == nil {
		t.Fatalf("token accepted for a drifted plan")
	}
	if count := fixture.countVersions(t); count != 3 {
		t.Fatalf("drifted execute deleted rows: %d remain", count)
	}
}

func TestExtensionGCFlagContract(t *testing.T) {
	fixture := newExtensionGCPostgresFixture(t)
	// --dry-run 与 --confirm-token 互斥。
	if _, err := fixture.runGC(t, "--dry-run", "--confirm-token", strings.Repeat("a", 64), "--database-url", fixture.databaseURL); err == nil {
		t.Fatalf("--dry-run with --confirm-token was accepted")
	}
	// 负数保留参数拒绝。
	if _, err := fixture.runGC(t, "--retain", "-1", "--database-url", fixture.databaseURL); err == nil {
		t.Fatalf("negative --retain was accepted")
	}
	if _, err := fixture.runGC(t, "--retain-days", "-5", "--database-url", fixture.databaseURL); err == nil {
		t.Fatalf("negative --retain-days was accepted")
	}
}
