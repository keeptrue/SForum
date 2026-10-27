package migrator

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const lifecycleRegistryPublicationsVersion = int64(202607140011)

func TestLifecycleRegistryPublicationsMigrationUpDownProtectionAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecycleJobPublicationsVersion); err != nil {
		t.Fatalf("migrate isolated schema to lifecycle jobs: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleRegistryPublicationsVersion, true); err != nil {
		t.Fatalf("apply lifecycle registry publication migration: %v", err)
	}
	assertLifecycleRegistryPublicationsSchema(t, ctx, db, true)

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
	var publicationID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO extension_lifecycle_publications (
			operation_id, operation, step_id, position, publication_mode,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id,
			first_attempt, last_attempt, runtime_attempts
		) VALUES ($1, 'enable', $2, 5, 'activate', $3, $4, $5, 41, 'target-runtime', 1, 1, '[]'::jsonb)
		RETURNING id
	`, operationID, stepID, extensionID, version, digest).Scan(&publicationID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO extension_lifecycle_registry_publications (
			publication_id, operation_id, operation, step_id, position, publication_mode,
			source_plan_digest,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id, target_plan_digest,
			first_attempt, last_attempt
		) VALUES ($1, $2, 'enable', $3, 5, 'activate', $4, $5, $6, $7, 41, 'target-runtime', $8, 1, 1)
	`, publicationID, operationID, stepID, strings.Repeat("c", 64), extensionID, version, digest, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, lifecycleRegistryPublicationsVersion, false); err == nil {
		t.Fatal("registry publication Down must refuse to delete recovery facts")
	}
	assertLifecycleRegistryPublicationsSchema(t, ctx, db, true)
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_lifecycle_registry_publications WHERE operation_id = $1`, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleRegistryPublicationsVersion, false); err != nil {
		t.Fatalf("rollback empty lifecycle registry publication migration: %v", err)
	}
	assertLifecycleRegistryPublicationsSchema(t, ctx, db, false)
	for _, table := range []string{"extension_lifecycle_operations", "extension_lifecycle_publications", "extension_lifecycle_job_publications"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("registry publication Down removed prerequisite table %s", table)
		}
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleRegistryPublicationsVersion, true); err != nil {
		t.Fatalf("reapply lifecycle registry publication migration: %v", err)
	}
	assertLifecycleRegistryPublicationsSchema(t, ctx, db, true)
}

func assertLifecycleRegistryPublicationsSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass(current_schema() || '.extension_lifecycle_registry_publications') IS NOT NULL
	`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("lifecycle registry publication table exists=%v, want %v", exists, want)
	}
	if !want {
		return
	}
	for _, column := range []string{
		"publication_id", "operation_id", "step_id", "publication_mode",
		"source_plan_digest", "target_plan_digest", "source_runtime_instance_id",
		"target_runtime_instance_id", "transaction_state", "last_attempt", "revision",
	} {
		var found bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'extension_lifecycle_registry_publications'
				  AND column_name = $1
			)
		`, column).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("lifecycle registry publication column %s is missing", column)
		}
	}
}
