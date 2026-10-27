package migrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const pluginJobMigrationsVersion = int64(202607140003)

func TestPluginJobMigrationsMigrationContract(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecycleStepLeasesVersion); err != nil {
		t.Fatalf("migrate isolated schema to lifecycle step leases: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, pluginJobMigrationsVersion, true); err != nil {
		t.Fatalf("apply plugin job migration ledger: %v", err)
	}
	assertPluginJobMigrationLedgerSchema(t, ctx, db, true)

	source := json.RawMessage(`{"extensionId":"sforum.demo","extensionVersion":"1.0.0","artifactDigest":"sha256:old","jobName":"rebuild","jobContractVersion":"1","payloadSchemaId":"demo.rebuild","payloadSchemaVersion":"1"}`)
	target := json.RawMessage(`{"extensionId":"sforum.demo","extensionVersion":"2.0.0","artifactDigest":"sha256:new","jobName":"rebuild","jobContractVersion":"1","payloadSchemaId":"demo.rebuild","payloadSchemaVersion":"2"}`)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_plugin_job_migrations (
			old_job_id, extension_id, migration_id, source_contract,
			source_trust_grant_id, target_contract, target_trust_grant_id
		) VALUES (41, 'sforum.demo', 'rebuild-v1-v2', $1, 'grant-old', $2, 'grant-new')
	`, source, target); err != nil {
		t.Fatalf("insert pending plugin job migration claim: %v", err)
	}
	expectPluginJobLedgerConstraintViolation(t, ctx, db, `
		UPDATE extension_plugin_job_migrations SET new_job_id = 84 WHERE old_job_id = 41
	`)
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_plugin_job_migrations
		SET new_job_id = 84, completed_at = transaction_timestamp()
		WHERE old_job_id = 41
	`); err != nil {
		t.Fatalf("link plugin job replacement: %v", err)
	}
	expectPluginJobLedgerConstraintViolation(t, ctx, db, `
		INSERT INTO extension_plugin_job_migrations (
			old_job_id, extension_id, migration_id, source_contract,
			source_trust_grant_id, target_contract, target_trust_grant_id,
			new_job_id, completed_at
		) VALUES (42, 'sforum.demo', 'other', $1, 'grant-old', $2, 'grant-new', 84, transaction_timestamp())
	`, source, target)

	if _, err := provider.ApplyVersion(ctx, pluginJobMigrationsVersion, false); err != nil {
		t.Fatalf("rollback plugin job migration ledger: %v", err)
	}
	assertPluginJobMigrationLedgerSchema(t, ctx, db, false)
	if _, err := provider.ApplyVersion(ctx, pluginJobMigrationsVersion, true); err != nil {
		t.Fatalf("reapply plugin job migration ledger: %v", err)
	}
	assertPluginJobMigrationLedgerSchema(t, ctx, db, true)
}

func assertPluginJobMigrationLedgerSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass(current_schema() || '.extension_plugin_job_migrations') IS NOT NULL
	`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("plugin job migration ledger exists=%v, want %v", exists, want)
	}
}

func expectPluginJobLedgerConstraintViolation(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	statement string,
	args ...any,
) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, statement, args...); err == nil {
		t.Fatalf("expected plugin job ledger constraint violation for %s", strings.Join(strings.Fields(statement), " "))
	}
}
