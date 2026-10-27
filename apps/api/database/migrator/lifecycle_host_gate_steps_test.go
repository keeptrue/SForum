package migrator

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const lifecycleHostGateStepsVersion = int64(202607140004)

func TestLifecycleHostGateStepsMigrationContract(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, pluginJobMigrationsVersion); err != nil {
		t.Fatalf("migrate isolated schema to plugin job ledger: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecycleHostGateStepsVersion, true); err != nil {
		t.Fatalf("apply lifecycle Host gate migration: %v", err)
	}
	assertLifecycleActionConstraint(t, ctx, db, true, true)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	operationID := insertLeaseTestOperation(t, ctx, tx)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, status
		) VALUES ($1, 'lifecycle.enable.01.host.starting', 'host.gate', 'host.gate.test@1', 'planned')
	`, operationID); err != nil {
		t.Fatalf("insert Host gate step: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, lifecycleHostGateStepsVersion, false); err != nil {
		t.Fatalf("rollback lifecycle Host gate migration: %v", err)
	}
	assertLifecycleActionConstraint(t, ctx, db, false, false)
	var retained int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM extension_lifecycle_steps WHERE lifecycle_action = 'host.gate'
	`).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("retained Host gate rows=%d err=%v", retained, err)
	}
	expectLifecycleHostGateWrite(t, ctx, db, operationID, "host.gate", false)
	expectLifecycleHostGateWrite(t, ctx, db, operationID, "enable", true)

	if _, err := provider.ApplyVersion(ctx, lifecycleHostGateStepsVersion, true); err != nil {
		t.Fatalf("reapply lifecycle Host gate migration: %v", err)
	}
	assertLifecycleActionConstraint(t, ctx, db, true, true)
	expectLifecycleHostGateWrite(t, ctx, db, operationID, "host.gate", true)
}

func assertLifecycleActionConstraint(t *testing.T, ctx context.Context, db *sql.DB, allowsHost, validated bool) {
	t.Helper()
	var definition string
	var isValidated bool
	if err := db.QueryRowContext(ctx, `
		SELECT pg_get_constraintdef(oid), convalidated
		FROM pg_constraint
		WHERE conrelid = 'extension_lifecycle_steps'::regclass
		  AND conname = 'extension_lifecycle_steps_lifecycle_action_check'
	`).Scan(&definition, &isValidated); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(definition, "host.gate") != allowsHost || isValidated != validated {
		t.Fatalf("lifecycle action constraint=%q validated=%t", definition, isValidated)
	}
}

func expectLifecycleHostGateWrite(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	operationID int64,
	action string,
	wantSuccess bool,
) {
	t.Helper()
	stepID := "constraint." + action + "." + strings.ReplaceAll(t.Name(), "/", ".")
	_, err := db.ExecContext(ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, status
		) VALUES ($1, $2, $3, 'host.gate.test@1', 'planned')
	`, operationID, stepID, action)
	if (err == nil) != wantSuccess {
		t.Fatalf("write lifecycle action %q err=%v, want success=%t", action, err, wantSuccess)
	}
}
