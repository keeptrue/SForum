package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const lifecycleJobPublicationsVersion = int64(202607140010)

func TestLifecycleJobPublicationsMigrationUpDownProtectionAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecycleStatePublicationsVersion); err != nil {
		t.Fatalf("migrate isolated schema to lifecycle state publications: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleJobPublicationsVersion, true); err != nil {
		t.Fatalf("apply lifecycle job publication migration: %v", err)
	}
	assertLifecycleJobPublicationsSchema(t, ctx, db, true)

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
		INSERT INTO extension_lifecycle_job_publications (
			operation_id, operation, step_id, position, publication_mode,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id,
			first_attempt, last_attempt, source_snapshot, target_snapshot,
			reconciliation_plan
		) VALUES (
			$1, 'enable', $2, 5, 'activate',
			$3, $4, $5, 41, 'target-runtime', 1, 1,
			'{"enabled":false}'::jsonb, '{"enabled":true}'::jsonb,
			'{"schema":"sforum.lifecycle.job-reconciliation-plan@1"}'::jsonb
		)
	`, operationID, stepID, extensionID, version, digest); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, lifecycleJobPublicationsVersion, false); err == nil {
		t.Fatal("job publication Down must refuse to delete reconciliation evidence")
	}
	assertLifecycleJobPublicationsSchema(t, ctx, db, true)
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_lifecycle_job_publications WHERE operation_id = $1`, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleJobPublicationsVersion, false); err != nil {
		t.Fatalf("rollback empty lifecycle job publication migration: %v", err)
	}
	assertLifecycleJobPublicationsSchema(t, ctx, db, false)
	for _, table := range []string{
		"extension_lifecycle_operations", "extension_lifecycle_publications",
		"extension_lifecycle_state_publications",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("job publication Down removed prerequisite table %s", table)
		}
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleJobPublicationsVersion, true); err != nil {
		t.Fatalf("reapply lifecycle job publication migration: %v", err)
	}
	assertLifecycleJobPublicationsSchema(t, ctx, db, true)
}

func assertLifecycleJobPublicationsSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass(current_schema() || '.extension_lifecycle_job_publications') IS NOT NULL
	`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("lifecycle job publication table exists=%v, want %v", exists, want)
	}
	if !want {
		return
	}
	for _, column := range []string{
		"operation_id", "step_id", "publication_mode", "source_snapshot", "target_snapshot",
		"reconciliation_plan", "publication_state", "reconciliation_state",
		"reconciliation_attempt", "reconciliation_result", "reconciliation_error",
		"reconciled_by_user_id", "reconciliation_audit_event_id", "revision",
	} {
		var found bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'extension_lifecycle_job_publications'
				  AND column_name = $1
			)
		`, column).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("lifecycle job publication column %s is missing", column)
		}
	}
}
