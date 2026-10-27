package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const (
	lifecyclePublicationsVersion   = int64(202607140007)
	lifecycleCleanupRecordsVersion = int64(202607140008)
)

func TestLifecycleCleanupRecordsMigrationUpDownUp(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecyclePublicationsVersion); err != nil {
		t.Fatalf("migrate isolated schema to publication journal: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleCleanupRecordsVersion, true); err != nil {
		t.Fatalf("apply lifecycle cleanup migration: %v", err)
	}
	assertLifecycleCleanupRecordsSchema(t, ctx, db, true)

	if _, err := provider.ApplyVersion(ctx, lifecycleCleanupRecordsVersion, false); err != nil {
		t.Fatalf("rollback empty lifecycle cleanup migration: %v", err)
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != lifecyclePublicationsVersion {
		t.Fatalf("version after cleanup Down=%d err=%v", version, err)
	}
	assertLifecycleCleanupRecordsSchema(t, ctx, db, false)
	for _, table := range []string{
		"extensions", "extension_versions", "extension_lifecycle_operations",
		"extension_lifecycle_steps", "extension_lifecycle_publications",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL
		`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("cleanup Down removed retained prerequisite table %s", table)
		}
	}

	if _, err := provider.ApplyVersion(ctx, lifecycleCleanupRecordsVersion, true); err != nil {
		t.Fatalf("reapply lifecycle cleanup migration: %v", err)
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != lifecycleCleanupRecordsVersion {
		t.Fatalf("version after cleanup Up=%d err=%v", version, err)
	}
	assertLifecycleCleanupRecordsSchema(t, ctx, db, true)
}

func assertLifecycleCleanupRecordsSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	var tableExists bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass(current_schema() || '.extension_lifecycle_cleanup_records') IS NOT NULL
	`).Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if tableExists != want {
		t.Fatalf("cleanup table exists=%v, want %v", tableExists, want)
	}
	if !want {
		return
	}
	for _, column := range []string{
		"cleanup_id", "operation_id", "step_id", "cleanup_mode", "status",
		"retained_package_digest", "retained_runtime_instance_id", "target_runtime_instance_id",
		"identity_recovery_evidence_retained", "package_recovery_evidence_retained",
		"runtime_recovery_evidence_retained", "physical_identity_present",
		"physical_package_present", "physical_runtime_recovery_present",
		"retention_marker", "export_artifact_id", "export_digest",
		"export_evidence", "purge_receipt_id", "purge_proof", "purge_proof_digest",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'extension_lifecycle_cleanup_records'
				  AND column_name = $1
			)
		`, column).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("cleanup column %s is missing", column)
		}
	}
	for _, index := range []string{
		"extension_lifecycle_cleanup_records_operation_idx",
		"extension_lifecycle_cleanup_records_one_tombstone_idx",
		"extension_lifecycle_cleanup_records_pending_idx",
		"extension_lifecycle_cleanup_records_retained_artifact_idx",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_indexes
				WHERE schemaname = current_schema() AND indexname = $1
			)
		`, index).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("cleanup index %s is missing", index)
		}
	}
}
