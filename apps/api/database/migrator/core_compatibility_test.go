package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCoreUpgradeCompatibilityBlocksExactTrustedRawExtension(t *testing.T) {
	assertCoreUpgradeCompatibilityLifecycle(
		t,
		`{"database":{"authority":"raw_core","coreCompatibility":">=1.0.0 <2.0.0"}}`,
		`{"database":{"authority":"raw_core","coreCompatibility":">=1.0.0 <2.0.0"}}`,
	)
}

func TestCoreUpgradeCompatibilityBlocksExactTrustedAdditiveRawGrant(t *testing.T) {
	assertCoreUpgradeCompatibilityLifecycle(
		t,
		`{"database":{"grants":["raw_core","core_views"],"coreCompatibility":">=1.0.0 <2.0.0"}}`,
		`{"database":{"grants":["core_views","raw_core"],"coreCompatibility":">=1.0.0 <2.0.0"}}`,
	)
}

func TestCoreUpgradeCompatibilityBlocksExactTrustedAdditiveKernelGrant(t *testing.T) {
	assertCoreUpgradeCompatibilityLifecycle(
		t,
		`{"database":{"grants":["own_schema","kernel"],"coreCompatibility":">=1.0.0 <2.0.0"}}`,
		`{"database":{"grants":["kernel","own_schema"],"coreCompatibility":">=1.0.0 <2.0.0"}}`,
	)
}

func assertCoreUpgradeCompatibilityLifecycle(t *testing.T, manifest, trustImpact string) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required for core compatibility integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Prepare the shared schema at whatever release this checkout reports:
	// the migrated Core version follows the nearest git tag, so pinning a
	// literal target would either be a downgrade guard trip or a no-op that
	// hides schema drift. The compatibility targets asserted below stay
	// explicit, because they are the values under test.
	if err := Up(ctx, Config{DatabaseURL: databaseURL}); err != nil {
		t.Fatalf("prepare current schema: %v", err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	extensionID := fmt.Sprintf("p5.core-compatibility.%d", time.Now().UnixNano())
	digest := strings.Repeat("a", 64)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system)
		VALUES ($1, 'plugin', 'P5 compatibility fixture', 'installed', 'uploaded', false)
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM extension_trust_grants WHERE extension_id = $1`, extensionID)
		_, _ = db.ExecContext(context.Background(), `UPDATE extensions SET active_version_id = NULL WHERE id = $1`, extensionID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM extensions WHERE id = $1`, extensionID)
		_ = db.Close()
	})
	var versionID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, '1.0.0', $2::jsonb, '/tmp/inert-p5-compatibility-fixture', $3)
		RETURNING id
	`, extensionID, manifest, digest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extensions SET status = 'enabled', active_version_id = $2 WHERE id = $1
	`, extensionID, versionID); err != nil {
		t.Fatal(err)
	}
	var grantID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_trust_grants (
			extension_id, extension_version, package_digest, action,
			artifact_digests, impact_document, impact_digest
		) VALUES (
			$1, '1.0.0', $2, 'enable', '{}'::jsonb, $3::jsonb, repeat('b', 64)
		) RETURNING id
	`, extensionID, digest, trustImpact).Scan(&grantID); err != nil {
		t.Fatal(err)
	}

	if err := checkCoreUpgradeCompatibility(ctx, db, "1.9.9", true); err != nil {
		t.Fatalf("compatible target was blocked: %v", err)
	}
	if err := checkCoreUpgradeCompatibility(ctx, db, "2.0.0", true); !errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("incompatible target was not blocked: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_versions
		SET manifest = jsonb_set(
			manifest, '{database,coreCompatibility}', to_jsonb('>=1.0.0 <3.0.0'::TEXT)
		)
		WHERE id = $1
	`, versionID); err != nil {
		t.Fatal(err)
	}
	if err := checkCoreUpgradeCompatibility(ctx, db, "2.0.0", true); !errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("constraint-only exact-trust drift was not blocked: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE extension_versions SET manifest = $2::jsonb WHERE id = $1`, versionID, manifest,
	); err != nil {
		t.Fatal(err)
	}
	// 「未知」不等于「更低」：让扩展与信任声明一致地要求 >=3.0.0，此时只有
	// 约束本身能否决。未声明 target（dev 无 tag 时的哨兵 1.0.0）必须跳过比对，
	// 否则没有 tag 的开发机上所有 >=3.x 扩展都会被误判不兼容。
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_versions
		SET manifest = jsonb_set(manifest, '{database,coreCompatibility}', to_jsonb('>=3.0.0'::TEXT))
		WHERE id = $1
	`, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_trust_grants
		SET impact_document = jsonb_set(
			impact_document, '{database,coreCompatibility}', to_jsonb('>=3.0.0'::TEXT)
		)
		WHERE id = $1
	`, grantID); err != nil {
		t.Fatal(err)
	}
	if err := checkCoreUpgradeCompatibility(ctx, db, "1.0.0", false); err != nil {
		t.Fatalf("undeclared target was compared against an extension declaration: %v", err)
	}
	if err := checkCoreUpgradeCompatibility(ctx, db, "1.0.0", true); !errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("declared older target was not blocked by the same declaration: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE extension_trust_grants SET revoked_at = now() WHERE id = $1`, grantID); err != nil {
		t.Fatal(err)
	}
	if err := checkCoreUpgradeCompatibility(ctx, db, "2.0.0", true); err != nil {
		t.Fatalf("revoked raw authority still blocked upgrade: %v", err)
	}
}

func TestCoreUpgradeCompatibilityRejectsInvalidTargetVersion(t *testing.T) {
	err := checkCoreUpgradeCompatibility(context.Background(), nil, "not-semver", true)
	if err == nil || errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("invalid target version error = %v", err)
	}
}

// 未声明 target 必须在任何 SQL 之前返回：传 nil 查询器时若仍去读 schema 会
// 得到 "database is required"，从而暴露「跳过」其实发生在查询之后。
func TestCoreUpgradeCompatibilitySkipsUndeclaredTargetBeforeQuerying(t *testing.T) {
	if err := checkCoreUpgradeCompatibility(context.Background(), nil, "1.0.0", false); err != nil {
		t.Fatalf("undeclared target touched the database or rejected the target: %v", err)
	}
	err := checkCoreUpgradeCompatibility(context.Background(), nil, "1.0.0", true)
	if err == nil || errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("declared target skipped the compatibility query: %v", err)
	}
}
