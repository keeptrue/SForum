package migrator

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const extensionDatabaseDispositionsVersion = int64(202607140014)

func TestExtensionDatabaseDispositionsUpDownProtectionAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, extensionDatabaseRegistryVersion); err != nil {
		t.Fatalf("migrate isolated schema to database registry: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseDispositionsVersion, true); err != nil {
		t.Fatalf("apply extension database dispositions: %v", err)
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass(current_schema() || '.extension_database_dispositions') IS NOT NULL
	`).Scan(&exists); err != nil || !exists {
		t.Fatalf("disposition table exists=%v err=%v", exists, err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_dispositions (
			cleanup_id, operation_id, cleanup_mode,
			extension_id, extension_version_id, extension_version, package_digest,
			schema_name, owner_role_name, runtime_role_name
		) VALUES (
			'cleanup-fixture', 41, 'uninstall_preserve',
			'fixture.database', 42, '1.0.0', repeat('a', 64),
			'sforum_ext_s_fixture_aaaaaaaaaaaaaaaaaaaa',
			'sforum_ext_o_fixture_aaaaaaaaaaaaaaaaaaaa',
			'sforum_ext_r_fixture_aaaaaaaaaaaaaaaaaaaa'
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseDispositionsVersion, false); err == nil {
		t.Fatal("database disposition Down must refuse durable evidence")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_database_dispositions`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseDispositionsVersion, false); err != nil {
		t.Fatalf("rollback empty extension database dispositions: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseDispositionsVersion, true); err != nil {
		t.Fatalf("reapply extension database dispositions: %v", err)
	}
}
