package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const lifecycleStatePublicationsVersion = int64(202607140009)

func TestLifecycleStatePublicationsMigrationUpDownProtectionAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecycleCleanupRecordsVersion); err != nil {
		t.Fatalf("migrate isolated schema to lifecycle cleanup records: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleStatePublicationsVersion, true); err != nil {
		t.Fatalf("apply lifecycle state publication migration: %v", err)
	}
	assertLifecycleStatePublicationsSchema(t, ctx, db, true)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	operationID := insertLeaseTestOperation(t, ctx, tx)
	var extensionID, version, digest string
	if err := tx.QueryRowContext(ctx, `
		SELECT extension_id, extension_version, package_digest
		FROM extension_lifecycle_operations WHERE id = $1
	`, operationID).Scan(&extensionID, &version, &digest); err != nil {
		t.Fatal(err)
	}
	const stepID = "lifecycle.enable.05.host.enabled"
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO extension_lifecycle_publications (
			operation_id, operation, step_id, position, publication_mode,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id,
			first_attempt, last_attempt, runtime_attempts
		) VALUES (
			$1, 'enable', $2, 5, 'activate',
			$3, $4, $5, 41, 'target-runtime', 1, 1, '[]'::jsonb
		)
	`, operationID, stepID, extensionID, version, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO extension_lifecycle_state_publications (
			operation_id, operation, step_id, position, publication_mode, extension_id,
			source_status, source_active_version_id, source_active_version, source_active_package_digest,
			target_status, target_active_version_id, target_active_version, target_active_package_digest,
			first_attempt, last_attempt
		) VALUES (
			$1, 'enable', $2, 5, 'activate', $3,
			'installed', 41, $4, $5,
			'enabled', 41, $4, $5, 1, 1
		)
	`, operationID, stepID, extensionID, version, digest); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, lifecycleStatePublicationsVersion, false); err == nil {
		t.Fatal("state publication Down must refuse to delete recovery facts")
	}
	assertLifecycleStatePublicationsSchema(t, ctx, db, true)
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_lifecycle_state_publications WHERE operation_id = $1`, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleStatePublicationsVersion, false); err != nil {
		t.Fatalf("rollback empty lifecycle state publication migration: %v", err)
	}
	assertLifecycleStatePublicationsSchema(t, ctx, db, false)
	for _, table := range []string{
		"extensions", "extension_versions", "extension_lifecycle_operations",
		"extension_lifecycle_publications", "extension_lifecycle_cleanup_records",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("state publication Down removed prerequisite table %s", table)
		}
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleStatePublicationsVersion, true); err != nil {
		t.Fatalf("reapply lifecycle state publication migration: %v", err)
	}
	assertLifecycleStatePublicationsSchema(t, ctx, db, true)
}

func assertLifecycleStatePublicationsSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass(current_schema() || '.extension_lifecycle_state_publications') IS NOT NULL
	`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("lifecycle state publication table exists=%v, want %v", exists, want)
	}
	if !want {
		return
	}
	for _, column := range []string{
		"operation_id", "step_id", "publication_mode", "extension_id",
		"source_status", "source_active_version_id", "source_active_package_digest", "source_staged_version_id",
		"target_status", "target_active_version_id", "target_active_package_digest", "target_staged_version_id",
		"transaction_state", "first_attempt", "last_attempt", "revision",
	} {
		var found bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'extension_lifecycle_state_publications'
				  AND column_name = $1
			)
		`, column).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("lifecycle state publication column %s is missing", column)
		}
	}
}
