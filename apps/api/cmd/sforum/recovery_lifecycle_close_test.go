package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/spf13/cobra"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// lifecycleClosePostgresFixture 提供只含 lifecycle 账本 + audit_events 的
// schema 隔离库：比 recovery fixture 更小的基底（不需要 extensions/
// extension_versions），并叠加 users / extension_trust_grants 存根以满足
// lifecycle 迁移的外键。
type lifecycleClosePostgresFixture struct {
	ctx         context.Context
	pool        *pgxpool.Pool
	schema      string
	databaseURL string
}

func newLifecycleClosePostgresFixture(t *testing.T) *lifecycleClosePostgresFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("lifecycle_close_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cleanupSchema := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE extension_trust_grants (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE audit_events (
			id BIGSERIAL PRIMARY KEY,
			action TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb
		);
	`); err != nil {
		_ = db.Close()
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		_ = db.Close()
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607140001, true); err != nil {
		_ = db.Close()
		cleanupSchema()
		admin.Close()
		t.Fatalf("apply lifecycle ledger migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607140002, true); err != nil {
		_ = db.Close()
		cleanupSchema()
		admin.Close()
		t.Fatalf("apply lifecycle step lease migration: %v", err)
	}
	if err := db.Close(); err != nil {
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		cleanupSchema()
		admin.Close()
		t.Fatal(err)
	}
	fixture := &lifecycleClosePostgresFixture{ctx: ctx, pool: pool, schema: schema, databaseURL: databaseURL}
	t.Cleanup(func() {
		pool.Close()
		admin.Close()
		cleanupSchema()
	})
	return fixture
}

// insertLifecycleCloseOperation 落一条开放 operation；统一把 created_at
// 回拨 10 分钟以允许后续 created_at 约束下的时间操作。
func insertLifecycleCloseOperation(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	extensionID, idempotencyKey string,
) int64 {
	t.Helper()
	var operationID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, artifact_digests,
			operation, plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot, created_at, updated_at
		) VALUES (
			$1, '1.0.0', $2, '{}'::jsonb, 'disable', 'close.test@1', $3, $4,
			'builtin', '{}'::jsonb, now() - interval '10 minutes', now() - interval '10 minutes'
		) RETURNING id
	`, extensionID, strings.Repeat("c", 64), idempotencyKey, strings.Repeat("d", 64),
	).Scan(&operationID); err != nil {
		t.Fatalf("insert lifecycle close operation: %v", err)
	}
	return operationID
}

// insertLifecycleCloseStep 落一条 running step；leaseExpiry 为偏移（负=过期）。
func insertLifecycleCloseStep(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, operationID int64, stepID, leaseExpiry string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, status,
			started_at, lease_owner_token, lease_expires_at, lease_heartbeat_at, lease_revision,
			created_at
		) VALUES (
			$1, $2, 'disable', 'close.test@1', 'running',
			now() - interval '9 minutes', $3, now() + $4::interval, now() - interval '9 minutes', 1,
			now() - interval '10 minutes'
		)
	`, operationID, stepID, "lifecycle-coordinator:test:"+stepID, leaseExpiry); err != nil {
		t.Fatalf("insert lifecycle close step: %v", err)
	}
}

func loadLifecycleCloseOperation(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, operationID int64,
) (state, terminal, errorCode string, revision int64, completedAt *time.Time) {
	t.Helper()
	if err := pool.QueryRow(ctx, `
		SELECT state, COALESCE(terminal_result, ''), error_code, revision, completed_at
		FROM extension_lifecycle_operations WHERE id = $1
	`, operationID).Scan(&state, &terminal, &errorCode, &revision, &completedAt); err != nil {
		t.Fatalf("load lifecycle close operation: %v", err)
	}
	return state, terminal, errorCode, revision, completedAt
}

func countLifecycleCloseSteps(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, operationID int64, status string,
) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM extension_lifecycle_steps
		WHERE operation_id = $1 AND status = $2
	`, operationID, status).Scan(&count); err != nil {
		t.Fatalf("count lifecycle close steps: %v", err)
	}
	return count
}

// 用例 9：关闭一条开放 operation；校验终态、step、审计与 --json 输出。
func TestExtensionRecoveryLifecycleCloseClosesOpenOperation(t *testing.T) {
	fixture := newLifecycleClosePostgresFixture(t)
	ctx := fixture.ctx
	opts := lifecycleCloseOptions{
		OperationID: insertLifecycleCloseOperation(t, ctx, fixture.pool, "close.target.plugin", "disable:first"),
		Reason:      "process died mid-disable; closing to unblock startup",
	}
	insertLifecycleCloseStep(t, ctx, fixture.pool, opts.OperationID, "disable.01.host", "-1 minute")
	insertLifecycleCloseStep(t, ctx, fixture.pool, opts.OperationID, "disable.02.host", "-2 minutes")

	closed, err := closeLifecycleOperationOutOfBand(ctx, fixture.pool, opts)
	if err != nil {
		t.Fatalf("lifecycle close: %v", err)
	}
	if closed.OperationID != opts.OperationID || closed.TerminalResult != "cancelled" ||
		closed.ErrorCode != "lifecycle.cli_closed" || closed.ClosedSteps != 2 || closed.Forced {
		t.Fatalf("summary=%#v", closed)
	}
	state, terminal, errorCode, revision, completedAt := loadLifecycleCloseOperation(t, ctx, fixture.pool, opts.OperationID)
	if state != "failed" || terminal != "cancelled" || errorCode != "lifecycle.cli_closed" ||
		revision != 2 || completedAt == nil {
		t.Fatalf("operation after close: %s/%s/%s rev=%d completed=%v", state, terminal, errorCode, revision, completedAt)
	}
	if countLifecycleCloseSteps(t, ctx, fixture.pool, opts.OperationID, "failed") != 2 {
		t.Fatal("open steps were not closed")
	}
	var audited bool
	if err := fixture.pool.QueryRow(ctx, `
		SELECT metadata->>'mode' = 'lifecycle_close'
		   AND metadata->>'extensionId' = 'close.target.plugin'
		   AND metadata->>'reason' = $2
		FROM audit_events
		WHERE action = 'extension.cli_recovery' AND metadata->>'operationId' = $1
	`, fmt.Sprintf("%d", opts.OperationID), opts.Reason).Scan(&audited); err != nil || !audited {
		t.Fatalf("lifecycle close audit row: audited=%v err=%v", audited, err)
	}
}

// 用例 9b：--json 输出可直接反序列化为取证摘要。
func TestExtensionRecoveryLifecycleCloseJSONOutput(t *testing.T) {
	fixture := newLifecycleClosePostgresFixture(t)
	operationID := insertLifecycleCloseOperation(t, fixture.ctx, fixture.pool, "close.json.plugin", "disable:first")
	insertLifecycleCloseStep(t, fixture.ctx, fixture.pool, operationID, "disable.01.host", "-1 minute")

	cmd := newExtensionRecoveryLifecycleCloseCommand()
	output := &bytes.Buffer{}
	cmd.SetOut(output)
	cmd.SetErr(&bytes.Buffer{})
	// 命令自建连接池：必须显式带上 fixture 的 search_path 才能落在隔离 schema。
	if err := cmd.Flags().Set("database-url", fixture.databaseURL+"&search_path="+fixture.schema+",public"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("operation-id", fmt.Sprintf("%d", operationID)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("reason", "closing orphaned operation"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("lifecycle-close command: %v", err)
	}
	var summary lifecycleCloseOperation
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatalf("parse --json output %q: %v", output.String(), err)
	}
	if summary.OperationID != operationID || summary.ExtensionID != "close.json.plugin" ||
		summary.Operation != "disable" || summary.PreviousState != "planned" ||
		summary.TerminalResult != "cancelled" || summary.ErrorCode != "lifecycle.cli_closed" ||
		summary.ClosedSteps != 1 {
		t.Fatalf("json summary=%#v", summary)
	}
}

// 用例 10：仍有活跃 lease 且无 --force 时拒绝；--force 才放行。
func TestExtensionRecoveryLifecycleCloseRejectsActiveLeaseWithoutForce(t *testing.T) {
	fixture := newLifecycleClosePostgresFixture(t)
	ctx := fixture.ctx
	operationID := insertLifecycleCloseOperation(t, ctx, fixture.pool, "close.lease.plugin", "disable:first")
	insertLifecycleCloseStep(t, ctx, fixture.pool, operationID, "disable.01.host", "1 minute")

	opts := lifecycleCloseOptions{OperationID: operationID, Reason: "not dead yet"}
	if _, err := closeLifecycleOperationOutOfBand(ctx, fixture.pool, opts); !errors.Is(err, errLifecycleCloseLeaseActive) {
		t.Fatalf("without force err=%v, want errLifecycleCloseLeaseActive", err)
	}
	if _, terminal, _, _, completedAt := loadLifecycleCloseOperation(t, ctx, fixture.pool, operationID); terminal != "" || completedAt != nil {
		t.Fatalf("rejected close mutated the operation: terminal=%q completed=%v", terminal, completedAt)
	}
	opts.Force = true
	closed, err := closeLifecycleOperationOutOfBand(ctx, fixture.pool, opts)
	if err != nil || closed.ClosedSteps != 1 || !closed.Forced {
		t.Fatalf("forced close summary=%#v err=%v", closed, err)
	}
}

// 用例 11：已关闭的操作报出明确错误；不存在的操作同样拒绝。
func TestExtensionRecoveryLifecycleCloseRejectsClosedOrMissingOperation(t *testing.T) {
	fixture := newLifecycleClosePostgresFixture(t)
	ctx := fixture.ctx
	closedID := insertLifecycleCloseOperation(t, ctx, fixture.pool, "close.closed.plugin", "disable:first")
	// 复刻现场 operation 703 的形态：uninstall 成功收尾但 state 仍停在阶段名
	// uninstalling。终态判据只有 completed_at + terminal_result，报错必须把它们
	// 一起说出来，否则「已关闭」会和观测到的 uninstalling 互相矛盾。
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE extension_lifecycle_operations
		SET operation = 'uninstall', removal_mode = 'preserve',
		    state = 'uninstalling', terminal_result = 'succeeded',
		    completed_at = statement_timestamp(), updated_at = statement_timestamp()
		WHERE id = $1
	`, closedID); err != nil {
		t.Fatal(err)
	}
	opts := lifecycleCloseOptions{OperationID: closedID, Reason: "second attempt"}
	_, err := closeLifecycleOperationOutOfBand(ctx, fixture.pool, opts)
	if !errors.Is(err, errLifecycleCloseAlreadyClosed) {
		t.Fatalf("closed operation err=%v, want errLifecycleCloseAlreadyClosed", err)
	}
	for _, fact := range []string{"state=uninstalling", "terminal=succeeded", "completedAt="} {
		if !strings.Contains(err.Error(), fact) {
			t.Fatalf("already-closed error must report %q: %v", fact, err)
		}
	}

	opts.OperationID = 42_000_000
	if _, err := closeLifecycleOperationOutOfBand(ctx, fixture.pool, opts); !errors.Is(err, errLifecycleCloseNotFound) {
		t.Fatalf("missing operation err=%v, want errLifecycleCloseNotFound", err)
	}
}

// 参数校验不触库：--operation-id 与 --reason 的前置拒绝。
func TestExtensionRecoveryLifecycleCloseValidatesArgumentsWithoutDatabase(t *testing.T) {
	run := func(setup func(*cobra.Command)) error {
		cmd := newExtensionRecoveryLifecycleCloseCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		setup(cmd)
		return cmd.Execute()
	}
	if err := run(func(cmd *cobra.Command) {}); err == nil || !strings.Contains(err.Error(), "--operation-id") {
		t.Fatalf("missing operation id err=%v", err)
	}
	if err := run(func(cmd *cobra.Command) {
		_ = cmd.Flags().Set("operation-id", "115")
	}); err == nil || !strings.Contains(err.Error(), "--reason") {
		t.Fatalf("missing reason err=%v", err)
	}
	if err := run(func(cmd *cobra.Command) {
		_ = cmd.Flags().Set("operation-id", "115")
		_ = cmd.Flags().Set("reason", "   ")
	}); err == nil || !strings.Contains(err.Error(), "--reason") {
		t.Fatalf("blank reason err=%v", err)
	}
}
