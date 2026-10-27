package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

type lifecyclePurgeFixture struct {
	pool       *pgxpool.Pool
	ctx        context.Context
	request    extensionsruntime.LifecycleCleanupPurgeRequest
	extension  string
	versionID  int64
	operation  int64
	cleanupRow string
}

// newLifecyclePurgeFixture 造一条「已 GC 过历史版本、即将卸载」的扩展，
// 给 5 张 RESTRICT 子表各写一行；pinned 为真时再把它钉进一条不可变
// runtime publication（生产里 marketplace / fetch-url 那批扩展的形状）。
func newLifecyclePurgeFixture(t *testing.T, pinned bool) *lifecyclePurgeFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	extensionID := fmt.Sprintf("bootstrap.purge.fk.%d", time.Now().UnixNano())
	cleanupID := strings.ReplaceAll(extensionID, ".", "-")
	username := strings.ReplaceAll(extensionID, ".", "_")

	var actorUserID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Bootstrap Purge')
		RETURNING id
	`, username, username+"@example.test").Scan(&actorUserID); err != nil {
		t.Fatal(err)
	}

	packagePath := filepath.Join(t.TempDir(), extensionID, "artifact")
	if err := os.MkdirAll(packagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "plugin.json"), []byte("purge fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	packageDigest, err := extensionpackage.DigestTree(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"manifestVersion": 3, "id": extensionID, "type": "plugin",
		"name": "Bootstrap Purge", "version": "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system, is_deletable)
		VALUES ($1, 'plugin', 'Bootstrap Purge', 'disabled', 'uploaded', false, true)
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, '1.0.0', $2::jsonb, $3, $4)
		RETURNING id
	`, extensionID, manifest, packagePath, packageDigest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id = $2 WHERE id = $1`, extensionID, versionID); err != nil {
		t.Fatal(err)
	}

	var operationID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, artifact_digests,
			operation, state, plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot, requested_by_user_id, removal_mode,
			terminal_result, started_at, completed_at
		) VALUES (
			$1, '1.0.0', $2, '{}'::jsonb,
			'uninstall', 'uninstalling', 'bootstrap.purge@1', $3, repeat('a', 64),
			'builtin', '{}'::jsonb, $4, 'preserve',
			'succeeded', statement_timestamp(), statement_timestamp()
		)
		RETURNING id
	`, extensionID, packageDigest, cleanupID, actorUserID).Scan(&operationID); err != nil {
		t.Fatal(err)
	}

	const retentionMarker = "retained-data-bootstrap"
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_lifecycle_cleanup_records (
			cleanup_id, operation_id, operation, step_id, position,
			first_attempt, last_attempt, cleanup_mode, record_kind, status,
			retained_extension_id, retained_extension_version, retained_package_digest,
			retained_version_id, retained_runtime_instance_id, retained_package_path,
			identity_snapshot, package_snapshot, runtime_recovery_snapshot,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id, target_package_path,
			retention_marker
		) VALUES (
			$1, $2, 'uninstall', 'lifecycle.uninstall.06.host.uninstalling', 6,
			1, 1, 'uninstall_preserve', 'uninstall_tombstone', 'pending',
			$3, '1.0.0', $4,
			$5, 'runtime-purge', $6,
			'{"extension":"retained"}'::jsonb,
			'{"package":"retained"}'::jsonb,
			'{"runtime":"retained"}'::jsonb,
			$3, '1.0.0', $4,
			$5, '', $6,
			$7
		)
	`, cleanupID, operationID, extensionID, packageDigest, versionID, packagePath,
		retentionMarker); err != nil {
		t.Fatal(err)
	}

	// 5 张 RESTRICT 子表各一行；gc 表额外多一行，用来验证审计计数不是"写 0 也能过"。
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_version_gc_removals (extension_id, extension_version, package_digest, package_path)
		VALUES ($1, '0.8.0', $2, $3), ($1, '0.9.0', $2, $3)
	`, extensionID, strings.Repeat("b", 64), packagePath+"/v0"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_missing_artifact_removals (
			extension_id, extension_type, extension_version, package_digest, package_path, data_mode
		) VALUES ($1, 'plugin', '1.0.0', $2, $3, 'preserve')
	`, extensionID, packageDigest, packagePath); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_builtin_removals (
			extension_id, extension_type, extension_version_id, extension_version, package_digest
		) VALUES ($1, 'plugin', $2, '1.0.0', $3)
	`, extensionID, versionID, packageDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachment_storage_instances (id, extension_id, name)
		VALUES (gen_random_uuid(), $1, 'purge fixture storage')
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_channel_selections (channel, extension_id, artifact_digest)
		VALUES ('web_push', $1, $2)
		ON CONFLICT (channel) DO UPDATE SET extension_id = EXCLUDED.extension_id,
			artifact_digest = EXCLUDED.artifact_digest, updated_at = statement_timestamp()
	`, extensionID, packageDigest); err != nil {
		t.Fatal(err)
	}

	if pinned {
		// 不可变 publication：header + member 必须同一事务写完（full-set 一致性是
		// DEFERRABLE 约束触发器，只在提交时校验）。
		digest, err := extensions.PluginRuntimeMembersDigest([]extensions.PluginRuntimeMember{{
			ExtensionID: extensionID, ExtensionVersionID: versionID,
			ExtensionVersion: "1.0.0", PackageDigest: packageDigest,
		}})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var revision int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO plugin_runtime_publications (member_count, members_digest, reason, actor_user_id)
			VALUES (1, $1, 'startup_reconcile', NULL) RETURNING revision
		`, digest).Scan(&revision); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_runtime_publication_members (
				publication_revision, extension_id, extension_version_id,
				extension_version, package_digest
			) VALUES ($1, $2, $3, '1.0.0', $4)
		`, revision, extensionID, versionID, packageDigest); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}

	return &lifecyclePurgeFixture{
		pool: pool, ctx: ctx, extension: extensionID, versionID: versionID,
		operation: operationID, cleanupRow: cleanupID,
		request: extensionsruntime.LifecycleCleanupPurgeRequest{
			CleanupID:                cleanupID,
			OperationID:              operationID,
			CleanupMode:              extensionsruntime.LifecycleBoundaryCleanupPreserve,
			RetainedExtensionID:      extensionID,
			RetainedExtensionVersion: "1.0.0",
			RetainedPackageDigest:    packageDigest,
			RetainedVersionID:        versionID,
			RetainedPackagePath:      packagePath,
			RetentionMarker:          retentionMarker,
		},
	}
}

// expectedRestrictCounts 是夹具给每张子表写的行数。
func expectedRestrictCounts() map[string]int {
	return map[string]int{
		"extension_version_gc_removals":       2,
		"extension_missing_artifact_removals": 1,
		"extension_builtin_removals":          1,
		"attachment_storage_instances":        1,
		"notification_channel_selections":     1,
	}
}

func (f *lifecyclePurgeFixture) countRestrictChildren(t *testing.T) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(restrictedExtensionChildTables))
	for _, table := range restrictedExtensionChildTables {
		var count int
		if err := f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM `+table+` WHERE extension_id = $1`, f.extension,
		).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func (f *lifecyclePurgeFixture) purge(t *testing.T) error {
	t.Helper()
	identity, err := beginProductionLifecycleCleanupIdentity(f.ctx, f.pool, f.request)
	if err != nil {
		return err
	}
	// 与生产调用方一致：失败必须回滚，否则事务一直持有 extensions 的行锁。
	defer identity.rollback()
	return identity.commitPurge(f.ctx)
}

// 生产清单必须与 schema 实况一致：从 pg_constraint 枚举 extensions 上
// confdeltype='r' 的外键，而不是信任一份手写表名列表——生产漏加第 6 张表也会被抓到。
func TestRestrictedExtensionChildTablesMatchSchema(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
		SELECT DISTINCT child.relname
		FROM pg_constraint AS con
		JOIN pg_class AS child ON child.oid = con.conrelid
		WHERE con.contype = 'f'
		  AND con.confrelid = 'extensions'::regclass
		  AND con.confdeltype = 'r'
		ORDER BY child.relname
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	schema := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		schema = append(schema, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	production := append([]string(nil), restrictedExtensionChildTables...)
	sort.Strings(production)
	if strings.Join(schema, ",") != strings.Join(production, ",") {
		t.Fatalf("RESTRICT child tables drifted from schema:\n schema=%v\n production=%v", schema, production)
	}
}

// 修复后：版本没进过 publication 的扩展可以硬删，五张子表全部清空，
// 且审计里的 cleared 计数逐表等于实际行数（断言 1+2+3）。
func TestLifecycleCleanupPurgeClearsAllRestrictChildren(t *testing.T) {
	fixture := newLifecyclePurgeFixture(t, false)
	expected := expectedRestrictCounts()
	if counts := fixture.countRestrictChildren(t); fmt.Sprint(counts) != fmt.Sprint(expected) {
		t.Fatalf("fixture restrict counts = %#v, want %#v", counts, expected)
	}
	if err := fixture.purge(t); err != nil {
		t.Fatalf("purge with restrict children must succeed after the fix: %v", err)
	}

	var extensionRows int
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT count(*) FROM extensions WHERE id = $1`, fixture.extension,
	).Scan(&extensionRows); err != nil {
		t.Fatal(err)
	}
	if extensionRows != 0 {
		t.Fatalf("extensions rows after purge = %d, want 0", extensionRows)
	}
	for table, count := range fixture.countRestrictChildren(t) {
		if count != 0 {
			t.Fatalf("%s rows after purge = %d, want 0", table, count)
		}
	}

	// 审计必须锁数值：只断言表名出现的话，写 0 或写错数都能过。
	var cleared []byte
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT (metadata->'cleared')::text FROM audit_events
		WHERE action = 'extension.uninstall'
		  AND metadata->>'cleanupId' = $1
	`, fixture.cleanupRow).Scan(&cleared); err != nil {
		t.Fatalf("purge must leave audit evidence: %v", err)
	}
	var recorded map[string]int
	if err := json.Unmarshal(cleared, &recorded); err != nil {
		t.Fatalf("cleared counts are not a JSON object: %v (%s)", err, cleared)
	}
	for table, want := range expected {
		if got := recorded[table]; got != want {
			t.Fatalf("audit cleared[%s] = %d, want %d (all=%s)", table, got, want, cleared)
		}
	}
}

// 版本被不可变 publication 钉住时：不硬删身份，也不动 5 张子表，
// 扩展保持 disabled，receipt 标 identityPurged=false（与正常卸载同一条语义）。
func TestLifecycleCleanupPurgeRetainsIdentityWhenPublished(t *testing.T) {
	fixture := newLifecyclePurgeFixture(t, true)
	expected := expectedRestrictCounts()
	if err := fixture.purge(t); err != nil {
		t.Fatalf("purge of a published identity must converge, not fail: %v", err)
	}

	var status string
	var extensionRows int
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT count(*), COALESCE(max(status), '') FROM extensions WHERE id = $1`, fixture.extension,
	).Scan(&extensionRows, &status); err != nil {
		t.Fatal(err)
	}
	if extensionRows != 1 || status != "disabled" {
		t.Fatalf("published identity must be retained as disabled: rows=%d status=%q", extensionRows, status)
	}
	if counts := fixture.countRestrictChildren(t); fmt.Sprint(counts) != fmt.Sprint(expected) {
		t.Fatalf("retained identity must not clear restrict children: %#v", counts)
	}
	var members int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM plugin_runtime_publication_members WHERE extension_version_id = $1
	`, fixture.versionID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 1 {
		t.Fatalf("publication evidence must stay append-only: members=%d", members)
	}
}

// 断言 4 按顺序性验：active_version_id 漂移时 purge 必须失败，
// 且失败后 extensions 行、cleanup_records 行、五张子表都还在。
func TestLifecycleCleanupPurgeKeepsLedgerWhenIdentityDrifts(t *testing.T) {
	fixture := newLifecyclePurgeFixture(t, false)
	var otherVersionID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, '2.0.0', '{}'::jsonb, $2, $3)
		RETURNING id
	`, fixture.extension, fixture.request.RetainedPackagePath, strings.Repeat("c", 64)).Scan(&otherVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx,
		`UPDATE extensions SET active_version_id = $2 WHERE id = $1`,
		fixture.extension, otherVersionID,
	); err != nil {
		t.Fatal(err)
	}

	if err := fixture.purge(t); err == nil {
		t.Fatal("purge must stay fail-closed when the active artifact drifted")
	}
	var extensionRows, ledgerRows int
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT count(*) FROM extensions WHERE id = $1`, fixture.extension,
	).Scan(&extensionRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT count(*) FROM extension_lifecycle_cleanup_records WHERE cleanup_id = $1`,
		fixture.cleanupRow,
	).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if extensionRows != 1 || ledgerRows != 1 {
		t.Fatalf("failed purge must leave identity and ledger intact: extension=%d ledger=%d",
			extensionRows, ledgerRows)
	}
	if counts := fixture.countRestrictChildren(t); fmt.Sprint(counts) != fmt.Sprint(expectedRestrictCounts()) {
		t.Fatalf("failed purge must not touch restrict children: %#v", counts)
	}
}
