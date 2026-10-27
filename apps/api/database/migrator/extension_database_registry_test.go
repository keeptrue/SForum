package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const extensionDatabaseRegistryVersion = int64(202607140013)

func TestExtensionDatabaseRegistryUpDownProtectionAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecycleMigrationProofsVersion); err != nil {
		t.Fatalf("migrate isolated schema to lifecycle migration proofs: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseRegistryVersion, true); err != nil {
		t.Fatalf("apply extension database registry: %v", err)
	}
	assertExtensionDatabaseRegistrySchema(t, ctx, db, true)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_resources (
			extension_id, schema_name, owner_role_name, runtime_role_name
		) VALUES (
			'fixture.database', 'sforum_ext_s_fixture_aaaaaaaaaaaaaaaaaaaa',
			'sforum_ext_o_fixture_aaaaaaaaaaaaaaaaaaaa',
			'sforum_ext_r_fixture_aaaaaaaaaaaaaaaaaaaa'
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseRegistryVersion, false); err == nil {
		t.Fatal("database registry Down must refuse to delete retained evidence")
	}
	assertExtensionDatabaseRegistrySchema(t, ctx, db, true)

	if _, err := db.ExecContext(ctx, `DELETE FROM extension_database_resources WHERE extension_id = 'fixture.database'`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseRegistryVersion, false); err != nil {
		t.Fatalf("rollback empty extension database registry: %v", err)
	}
	assertExtensionDatabaseRegistrySchema(t, ctx, db, false)

	for _, table := range []string{
		"extension_migration_ledger", "extension_lifecycle_operations",
		"extension_lifecycle_migration_proofs",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("database registry Down removed prerequisite table %s", table)
		}
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseRegistryVersion, true); err != nil {
		t.Fatalf("reapply extension database registry: %v", err)
	}
	assertExtensionDatabaseRegistrySchema(t, ctx, db, true)
}

func assertExtensionDatabaseRegistrySchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	for _, table := range []string{
		"extension_database_resources", "extension_database_grants",
		"extension_database_credentials", "extension_database_migration_plans",
		"extension_database_migration_steps", "extension_database_migration_state",
		"extension_database_migration_proofs",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("extension database table %s exists=%v, want %v", table, exists, want)
		}
	}
	if !want {
		return
	}
	for _, column := range []string{
		"plan_digest", "dry_run_digest", "status", "current_step", "total_steps",
		"failure_code", "warning_code", "has_non_transactional", "target_ready",
		"source_resume_safe", "revision",
	} {
		var found bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM information_schema.columns
			  WHERE table_schema = current_schema()
			    AND table_name = 'extension_database_migration_plans'
			    AND column_name = $1
			)
		`, column).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("extension database plan column %s is missing", column)
		}
	}
}
