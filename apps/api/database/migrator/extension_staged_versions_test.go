package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const extensionStagedVersionsVersion = int64(202607140005)

func TestExtensionStagedVersionsMigrationContract(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecycleHostGateStepsVersion); err != nil {
		t.Fatalf("migrate isolated schema to lifecycle Host gates: %v", err)
	}

	activeID := insertStagedVersionTestExtension(t, ctx, db, "staged.plugin", "1.0.0", true)
	candidateID := insertStagedVersionTestVersion(t, ctx, db, "staged.plugin", "2.0.0")
	foreignID := insertStagedVersionTestExtension(t, ctx, db, "foreign.plugin", "1.0.0", false)

	if _, err := provider.ApplyVersion(ctx, extensionStagedVersionsVersion, true); err != nil {
		t.Fatalf("apply staged version migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extensions SET staged_version_id = $2 WHERE id = $1
	`, "staged.plugin", foreignID); err == nil {
		t.Fatal("cross-extension staged version must violate exact extension ownership")
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extensions SET staged_version_id = $2 WHERE id = $1
	`, "staged.plugin", candidateID); err != nil {
		t.Fatalf("stage same-extension candidate: %v", err)
	}

	var gotActive, gotStaged int64
	var status string
	if err := db.QueryRowContext(ctx, `
		SELECT active_version_id, staged_version_id, status
		FROM extensions WHERE id = 'staged.plugin'
	`).Scan(&gotActive, &gotStaged, &status); err != nil {
		t.Fatal(err)
	}
	if gotActive != activeID || gotStaged != candidateID || status != "enabled" {
		t.Fatalf("active=%d staged=%d status=%q", gotActive, gotStaged, status)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM extension_versions WHERE id = $1`, candidateID); err != nil {
		t.Fatalf("delete staged candidate: %v", err)
	}
	var stagedCleared bool
	if err := db.QueryRowContext(ctx, `
		SELECT staged_version_id IS NULL FROM extensions WHERE id = 'staged.plugin'
	`).Scan(&stagedCleared); err != nil || !stagedCleared {
		t.Fatalf("staged candidate delete cleared=%t err=%v", stagedCleared, err)
	}

	if _, err := provider.ApplyVersion(ctx, extensionStagedVersionsVersion, false); err != nil {
		t.Fatalf("rollback staged version migration: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT active_version_id, status FROM extensions WHERE id = 'staged.plugin'
	`).Scan(&gotActive, &status); err != nil {
		t.Fatal(err)
	}
	if gotActive != activeID || status != "enabled" {
		t.Fatalf("rollback changed active extension: active=%d status=%q", gotActive, status)
	}
	if _, err := provider.ApplyVersion(ctx, extensionStagedVersionsVersion, true); err != nil {
		t.Fatalf("reapply staged version migration: %v", err)
	}
}

func insertStagedVersionTestExtension(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	id string,
	version string,
	enabled bool,
) int64 {
	t.Helper()
	status := "installed"
	if enabled {
		status = "enabled"
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', $1, $2)
	`, id, status); err != nil {
		t.Fatalf("insert extension %s: %v", id, err)
	}
	versionID := insertStagedVersionTestVersion(t, ctx, db, id, version)
	if _, err := db.ExecContext(ctx, `
		UPDATE extensions SET active_version_id = $2 WHERE id = $1
	`, id, versionID); err != nil {
		t.Fatalf("activate extension %s: %v", id, err)
	}
	return versionID
}

func insertStagedVersionTestVersion(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	extensionID string,
	version string,
) int64 {
	t.Helper()
	var versionID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES (
			$1::text, $2::text,
			jsonb_build_object('id', $1::text, 'version', $2::text, 'type', 'plugin'),
			'/tmp/' || $1::text || '/' || $2::text,
			md5($1::text || ':' || $2::text) || md5($2::text || ':' || $1::text)
		)
		RETURNING id
	`, extensionID, version).Scan(&versionID); err != nil {
		t.Fatalf("insert version %s@%s: %v", extensionID, version, err)
	}
	return versionID
}
