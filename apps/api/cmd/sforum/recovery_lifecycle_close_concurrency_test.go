package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newLifecycleCloseSecondPool 造第二个独立连接池：只有两个 pool 才谈得上
// "真并发"，同一个 pool 的连接复用会掩盖行锁与 CAS 的实际行为。
func newLifecycleCloseSecondPool(t *testing.T, fixture *lifecycleClosePostgresFixture) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(fixture.databaseURL)
	if err != nil {
		t.Fatalf("parse fixture database url: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = fixture.schema + ",public"
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open second pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// R6：两个 pool 同时关同一条 operation，只许一方成功，审计只许一行。
// 断言的是"加锁后重判 predicate + revision CAS"，不是先查后写。
func TestLifecycleCloseConcurrentPoolsCloseExactlyOnce(t *testing.T) {
	fixture := newLifecycleClosePostgresFixture(t)
	ctx := fixture.ctx
	operationID := insertLifecycleCloseOperation(
		t, ctx, fixture.pool, "close.concurrent.plugin", "disable:concurrent",
	)
	second := newLifecycleCloseSecondPool(t, fixture)
	opts := lifecycleCloseOptions{
		OperationID: operationID,
		Reason:      "two operators closed the same operation at once",
	}

	pools := []*pgxpool.Pool{fixture.pool, second}
	errs := make([]error, len(pools))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index, pool := range pools {
		wg.Add(1)
		go func(index int, pool *pgxpool.Pool) {
			defer wg.Done()
			<-start
			_, errs[index] = closeLifecycleOperationOutOfBand(ctx, pool, opts)
		}(index, pool)
	}
	close(start)
	wg.Wait()

	successes, alreadyClosed := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errLifecycleCloseAlreadyClosed):
			alreadyClosed++
		default:
			t.Fatalf("unexpected concurrent close error: %v", err)
		}
	}
	if successes != 1 || alreadyClosed != 1 {
		t.Fatalf("concurrent close: %d success, %d already-closed; want exactly 1/1", successes, alreadyClosed)
	}

	_, terminal, errorCode, revision, completedAt := loadLifecycleCloseOperation(t, ctx, fixture.pool, operationID)
	if completedAt == nil || terminal != "cancelled" || errorCode != "lifecycle.cli_closed" || revision != 2 {
		t.Fatalf("closed operation = terminal=%s errorCode=%s revision=%d completedAt=%v",
			terminal, errorCode, revision, completedAt)
	}
	if open := countLifecycleCloseSteps(t, ctx, fixture.pool, operationID, "running"); open != 0 {
		t.Fatalf("running steps after close = %d, want 0", open)
	}

	var audits int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action = 'extension.cli_recovery'
		  AND metadata->>'mode' = 'lifecycle_close'
		  AND (metadata->>'operationId')::bigint = $1
	`, operationID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("concurrent close wrote %d audit row(s), want exactly 1", audits)
	}
}

// R9：审计写失败必须整笔回滚——不允许"修复成功但没审计"。
func TestLifecycleCloseRollsBackWhenAuditWriteFails(t *testing.T) {
	fixture := newLifecycleClosePostgresFixture(t)
	ctx := fixture.ctx
	operationID := insertLifecycleCloseOperation(
		t, ctx, fixture.pool, "close.audit.plugin", "disable:audit-failure",
	)
	insertLifecycleCloseStep(t, ctx, fixture.pool, operationID, "disable.stop", "-1 minute")

	// schema 隔离库里的 audit_events 只属于本用例：加一个必然报错的触发器，
	// 让审计写入在事务尾部失败，从而验证整笔回滚。
	if _, err := fixture.pool.Exec(ctx, `
		CREATE FUNCTION reject_cli_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'audit write rejected by test';
		END $$;
		CREATE TRIGGER reject_cli_audit_events
			BEFORE INSERT ON audit_events
			FOR EACH ROW EXECUTE FUNCTION reject_cli_audit();
	`); err != nil {
		t.Fatalf("install audit rejection trigger: %v", err)
	}

	opts := lifecycleCloseOptions{
		OperationID: operationID,
		Reason:      "closing with a broken audit table",
		Force:       true,
	}
	if _, err := closeLifecycleOperationOutOfBand(ctx, fixture.pool, opts); err == nil {
		t.Fatal("close must fail when the audit write fails")
	}

	_, terminal, _, revision, completedAt := loadLifecycleCloseOperation(t, ctx, fixture.pool, operationID)
	if completedAt != nil || terminal != "" || revision != 1 {
		t.Fatalf("audit failure must roll the close back: terminal=%q revision=%d completedAt=%v",
			terminal, revision, completedAt)
	}
	if running := countLifecycleCloseSteps(t, ctx, fixture.pool, operationID, "running"); running != 1 {
		t.Fatalf("step state after rolled-back close = %d running, want 1", running)
	}
}
