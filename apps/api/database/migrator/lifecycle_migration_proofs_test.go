package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const lifecycleMigrationProofsVersion = int64(202607140012)

func TestLifecycleMigrationProofsUpDownProtectionAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecycleRegistryPublicationsVersion); err != nil {
		t.Fatalf("migrate isolated schema to registry publication: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleMigrationProofsVersion, true); err != nil {
		t.Fatalf("apply lifecycle migration proofs: %v", err)
	}
	assertLifecycleMigrationProofSchema(t, ctx, db, true)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	operationID := insertLeaseTestOperation(t, ctx, tx)
	if _, err := tx.ExecContext(ctx, `
		UPDATE extension_lifecycle_operations SET operation = 'upgrade' WHERE id = $1
	`, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO extension_lifecycle_migration_proofs (
			operation_id, operation, migration_mode, step_id, position,
			source_extension_id, source_extension_version, source_package_digest,
			source_version_id, source_migrations_digest,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_migrations_digest, plan_digest
		) VALUES (
			$1, 'upgrade', 'upgrade', 'lifecycle.upgrade.04.host.migrating', 4,
			'sforum.lifecycle.fixture', '1.0.0', repeat('a', 64), 41, repeat('b', 64),
			'sforum.lifecycle.fixture', '2.0.0', repeat('c', 64), 42, repeat('d', 64), repeat('e', 64)
		)
	`, operationID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, lifecycleMigrationProofsVersion, false); err == nil {
		t.Fatal("migration proof Down must refuse to delete durable evidence")
	}
	assertLifecycleMigrationProofSchema(t, ctx, db, true)
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_lifecycle_migration_proofs WHERE operation_id = $1`, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleMigrationProofsVersion, false); err != nil {
		t.Fatalf("rollback empty lifecycle migration proof migration: %v", err)
	}
	assertLifecycleMigrationProofSchema(t, ctx, db, false)
	for _, table := range []string{
		"extension_migration_ledger", "extension_lifecycle_operations",
		"extension_lifecycle_publications", "extension_lifecycle_registry_publications",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("migration proof Down removed prerequisite table %s", table)
		}
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleMigrationProofsVersion, true); err != nil {
		t.Fatalf("reapply lifecycle migration proof migration: %v", err)
	}
	assertLifecycleMigrationProofSchema(t, ctx, db, true)
}

func assertLifecycleMigrationProofSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass(current_schema() || '.extension_lifecycle_migration_proofs') IS NOT NULL
	`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("lifecycle migration proof table exists=%v, want %v", exists, want)
	}
	if !want {
		return
	}
	for _, column := range []string{
		"operation_id", "migration_mode", "step_id", "source_package_digest",
		"source_migrations_digest", "target_package_digest", "target_migrations_digest",
		"plan_digest", "first_attempt", "last_attempt", "status", "target_ready",
		"source_resume_safe", "proof_kind", "proof_id", "proof_digest", "revision",
	} {
		var found bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM information_schema.columns
			  WHERE table_schema = current_schema()
			    AND table_name = 'extension_lifecycle_migration_proofs'
			    AND column_name = $1
			)
		`, column).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("lifecycle migration proof column %s is missing", column)
		}
	}
}
