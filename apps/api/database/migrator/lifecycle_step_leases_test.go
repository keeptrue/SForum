package migrator

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

const (
	lifecycleOperationLedgerVersion = int64(202607140001)
	lifecycleStepLeasesVersion      = int64(202607140002)
)

func TestLifecycleStepLeaseMigrationContract(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("DATABASE_URL is required for migrator integration test")
	}
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecycleOperationLedgerVersion); err != nil {
		t.Fatalf("migrate isolated schema to lifecycle ledger: %v", err)
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != lifecycleOperationLedgerVersion {
		t.Fatalf("base migration version=%d err=%v", version, err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleStepLeasesVersion, true); err != nil {
		t.Fatalf("apply lifecycle step lease migration: %v", err)
	}
	assertLifecycleStepLeaseSchema(t, ctx, db, true)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	operationID := insertLeaseTestOperation(t, ctx, tx)
	stepID := insertLeaseTestStep(t, ctx, tx, operationID, "lease.primary")

	expectLeaseConstraintViolation(t, ctx, tx, `
		UPDATE extension_lifecycle_steps
		SET lease_owner_token = 'owner-without-window', lease_revision = 1
		WHERE id = $1
	`, stepID)
	expectLeaseConstraintViolation(t, ctx, tx, `
		UPDATE extension_lifecycle_steps
		SET lease_expires_at = transaction_timestamp() + interval '1 minute',
		    lease_heartbeat_at = transaction_timestamp()
		WHERE id = $1
	`, stepID)

	if _, err := tx.ExecContext(ctx, `
		UPDATE extension_lifecycle_steps
		SET lease_owner_token = 'owner-primary',
		    lease_heartbeat_at = transaction_timestamp(),
		    lease_expires_at = transaction_timestamp() + interval '1 minute',
		    lease_revision = lease_revision + 1
		WHERE id = $1
	`, stepID); err != nil {
		t.Fatalf("claim valid lifecycle step lease: %v", err)
	}
	expectLeaseConstraintViolation(t, ctx, tx, `
		UPDATE extension_lifecycle_steps
		SET status = 'succeeded', completed_at = transaction_timestamp()
		WHERE id = $1
	`, stepID)
	if _, err := tx.ExecContext(ctx, `
		UPDATE extension_lifecycle_steps
		SET lease_owner_token = '', lease_expires_at = NULL,
		    lease_heartbeat_at = NULL, lease_revision = lease_revision + 1
		WHERE id = $1
	`, stepID); err != nil {
		t.Fatalf("release lifecycle step lease: %v", err)
	}
	var releasedOwner string
	var releasedExpiry, releasedHeartbeat sql.NullTime
	var releasedRevision int64
	if err := tx.QueryRowContext(ctx, `
		SELECT lease_owner_token, lease_expires_at, lease_heartbeat_at, lease_revision
		FROM extension_lifecycle_steps WHERE id = $1
	`, stepID).Scan(&releasedOwner, &releasedExpiry, &releasedHeartbeat, &releasedRevision); err != nil {
		t.Fatal(err)
	}
	if releasedOwner != "" || releasedExpiry.Valid || releasedHeartbeat.Valid || releasedRevision != 2 {
		t.Fatalf("released lease owner=%q expiry=%#v heartbeat=%#v revision=%d", releasedOwner, releasedExpiry, releasedHeartbeat, releasedRevision)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE extension_lifecycle_steps
		SET status = 'succeeded', completed_at = transaction_timestamp()
		WHERE id = $1
	`, stepID); err != nil {
		t.Fatalf("complete released lifecycle step: %v", err)
	}

	takeoverStepID := insertLeaseTestStep(t, ctx, tx, operationID, "lease.takeover")
	if _, err := tx.ExecContext(ctx, `
		UPDATE extension_lifecycle_steps
		SET lease_owner_token = 'owner-expired',
		    lease_heartbeat_at = transaction_timestamp() - interval '2 minutes',
		    lease_expires_at = transaction_timestamp() - interval '1 minute',
		    lease_revision = 1
		WHERE id = $1
	`, takeoverStepID); err != nil {
		t.Fatalf("seed expired lifecycle step lease: %v", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE extension_lifecycle_steps
		SET lease_owner_token = 'owner-takeover',
		    lease_heartbeat_at = transaction_timestamp(),
		    lease_expires_at = transaction_timestamp() + interval '1 minute',
		    lease_revision = lease_revision + 1
		WHERE id = $1 AND status IN ('planned', 'running', 'waiting')
		  AND lease_revision = 1
		  AND (lease_owner_token = '' OR lease_expires_at <= transaction_timestamp())
	`, takeoverStepID)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("expired lease takeover affected %d rows", affected)
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE extension_lifecycle_steps
		SET lease_heartbeat_at = transaction_timestamp(),
		    lease_expires_at = transaction_timestamp() + interval '1 minute',
		    lease_revision = lease_revision + 1
		WHERE id = $1 AND lease_owner_token = 'owner-expired' AND lease_revision = 1
		  AND lease_expires_at > transaction_timestamp()
	`, takeoverStepID)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 0 {
		t.Fatalf("stale owner heartbeat affected %d rows", affected)
	}
	var takeoverOwner string
	var takeoverRevision int64
	if err := tx.QueryRowContext(ctx, `
		SELECT lease_owner_token, lease_revision
		FROM extension_lifecycle_steps WHERE id = $1
	`, takeoverStepID).Scan(&takeoverOwner, &takeoverRevision); err != nil {
		t.Fatal(err)
	}
	if takeoverOwner != "owner-takeover" || takeoverRevision != 2 {
		t.Fatalf("takeover owner=%q revision=%d", takeoverOwner, takeoverRevision)
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE extension_lifecycle_steps
		SET lease_heartbeat_at = transaction_timestamp(),
		    lease_expires_at = transaction_timestamp() + interval '2 minutes',
		    lease_revision = lease_revision + 1
		WHERE id = $1 AND lease_owner_token = 'owner-takeover' AND lease_revision = 2
		  AND lease_expires_at > transaction_timestamp()
	`, takeoverStepID)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("current owner heartbeat affected %d rows", affected)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, lifecycleStepLeasesVersion, false); err != nil {
		t.Fatalf("rollback lifecycle step lease migration: %v", err)
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != lifecycleOperationLedgerVersion {
		t.Fatalf("version after lease Down=%d err=%v", version, err)
	}
	assertLifecycleStepLeaseSchema(t, ctx, db, false)
	for _, table := range []string{"extension_lifecycle_operations", "extension_lifecycle_steps"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("lease Down removed retained table %s", table)
		}
	}

	if _, err := provider.ApplyVersion(ctx, lifecycleStepLeasesVersion, true); err != nil {
		t.Fatalf("reapply lifecycle step lease migration: %v", err)
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != lifecycleStepLeasesVersion {
		t.Fatalf("version after lease Up=%d err=%v", version, err)
	}
	assertLifecycleStepLeaseSchema(t, ctx, db, true)
}

func openIsolatedLifecycleLeaseMigrationDB(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) (*sql.DB, *goose.Provider) {
	t.Helper()
	adminDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConn, err := adminDB.Conn(ctx)
	if err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	// Older migrations inspect the global PostgreSQL catalog. Serialize temporary
	// schema creation/drop across test processes so one run cannot invalidate
	// another run's catalog snapshot.
	if _, err := adminConn.ExecContext(ctx, `SELECT pg_advisory_lock(202607140002)`); err != nil {
		adminConn.Close()
		adminDB.Close()
		t.Fatal(err)
	}
	schema := fmt.Sprintf("sforum_lease_%d", time.Now().UnixNano())
	if _, err := adminConn.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		_, _ = adminConn.ExecContext(ctx, `SELECT pg_advisory_unlock(202607140002)`)
		adminConn.Close()
		adminDB.Close()
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		_, _ = adminConn.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		_, _ = adminConn.ExecContext(ctx, `SELECT pg_advisory_unlock(202607140002)`)
		adminConn.Close()
		adminDB.Close()
		t.Fatal(err)
	}
	// A single connection keeps the temporary search_path scoped to this provider.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, `SET search_path TO `+schema+`, public`); err != nil {
		db.Close()
		_, _ = adminConn.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		_, _ = adminConn.ExecContext(ctx, `SELECT pg_advisory_unlock(202607140002)`)
		adminConn.Close()
		adminDB.Close()
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		db.Close()
		_, _ = adminConn.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		_, _ = adminConn.ExecContext(ctx, `SELECT pg_advisory_unlock(202607140002)`)
		adminConn.Close()
		adminDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = adminConn.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		_, _ = adminConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(202607140002)`)
		adminConn.Close()
		adminDB.Close()
	})
	return db, provider
}

func assertLifecycleStepLeaseSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	for _, column := range []string{"lease_owner_token", "lease_expires_at", "lease_revision", "lease_heartbeat_at"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'extension_lifecycle_steps'
				  AND column_name = $1
			)
		`, column).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("lifecycle step lease column %s exists=%v, want %v", column, exists, want)
		}
	}
	var indexDefinition string
	err := db.QueryRowContext(ctx, `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = 'extension_lifecycle_steps_claimable_idx'
	`).Scan(&indexDefinition)
	if !want {
		if err == sql.ErrNoRows {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("claimable index survived lease Down: %s", indexDefinition)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, clause := range []string{"lease_expires_at", "NULLS FIRST", "planned", "running", "waiting"} {
		if !strings.Contains(indexDefinition, clause) {
			t.Fatalf("claimable index %q missing %q", indexDefinition, clause)
		}
	}
}

func insertLeaseTestOperation(t *testing.T, ctx context.Context, tx *sql.Tx) int64 {
	t.Helper()
	var operationID int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, operation,
			plan_version, idempotency_key, request_fingerprint, authority_type
		) VALUES ($1, '1.0.0', $2, 'enable', 'lease.test@1', $3, $4, 'builtin')
		RETURNING id
	`, fmt.Sprintf("lease.integration.%d", time.Now().UnixNano()), strings.Repeat("a", 64),
		fmt.Sprintf("lease:%d", time.Now().UnixNano()), strings.Repeat("b", 64)).Scan(&operationID)
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

func insertLeaseTestStep(t *testing.T, ctx context.Context, tx *sql.Tx, operationID int64, stepID string) int64 {
	t.Helper()
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, status
		) VALUES ($1, $2, 'enable', 'lease.test@1', 'planned')
		RETURNING id
	`, operationID, stepID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func expectLeaseConstraintViolation(t *testing.T, ctx context.Context, tx *sql.Tx, statement string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, `SAVEPOINT lease_constraint_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, statement, args...); err == nil {
		t.Fatalf("expected lease constraint violation for %s", strings.Join(strings.Fields(statement), " "))
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT lease_constraint_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT lease_constraint_check`); err != nil {
		t.Fatal(err)
	}
}
