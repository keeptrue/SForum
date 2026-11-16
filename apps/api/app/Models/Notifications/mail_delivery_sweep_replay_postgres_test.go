package notifications

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// P2-6 的 store 侧证据：sweeper 只收超龄的非终态行，replay 只接受终态失败。
//
// fixture 用一次性集群 + 独立 schema，DDL 镜像
// database/migrations/202607110016_notifications_mail_deliveries.sql 的 mail_deliveries。

func TestSweepStaleDeliveriesOnlyTouchesAgedNonTerminalRows(t *testing.T) {
	fixture := newMailDeliveryPGFixture(t)
	store := fixture.store
	ctx := fixture.ctx

	agedQueued := fixture.insertDelivery(t, "aged-queued", DeliveryQueued, time.Now().Add(-2*time.Hour))
	agedSending := fixture.insertDelivery(t, "aged-sending", DeliverySending, time.Now().Add(-45*time.Minute))
	freshQueued := fixture.insertDelivery(t, "fresh-queued", DeliveryQueued, time.Now().Add(-1*time.Minute))
	freshSending := fixture.insertDelivery(t, "fresh-sending", DeliverySending, time.Now().Add(-2*time.Minute))
	agedSent := fixture.insertDelivery(t, "aged-sent", DeliverySent, time.Now().Add(-3*time.Hour))
	agedFailed := fixture.insertDelivery(t, "aged-failed", DeliveryFailed, time.Now().Add(-3*time.Hour))

	swept, err := store.SweepStaleDeliveries(ctx, StaleDeliveryThreshold)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 2 {
		t.Fatalf("swept=%d want 2（只有两条超龄的非终态行）", swept)
	}

	for _, id := range []int64{agedQueued, agedSending} {
		delivery, err := store.GetDelivery(ctx, id)
		if err != nil {
			t.Fatalf("load swept delivery %d: %v", id, err)
		}
		if delivery.Status != DeliveryFailed || delivery.Reason != "stale_swept" {
			t.Fatalf("delivery %d = %s/%q，want failed/stale_swept", id, delivery.Status, delivery.Reason)
		}
		if delivery.CompletedAt == nil {
			t.Fatalf("delivery %d 缺少 completed_at（终态必须收口）", id)
		}
	}
	// 新鲜行不能被误伤：它们可能正在发送中。
	for _, id := range []int64{freshQueued, freshSending} {
		delivery, err := store.GetDelivery(ctx, id)
		if err != nil {
			t.Fatalf("load fresh delivery %d: %v", id, err)
		}
		if delivery.Status == DeliveryFailed {
			t.Fatalf("新鲜行 %d 被 sweeper 误伤", id)
		}
	}
	// 终态行不动。
	for _, id := range []int64{agedSent, agedFailed} {
		delivery, err := store.GetDelivery(ctx, id)
		if err != nil {
			t.Fatalf("load terminal delivery %d: %v", id, err)
		}
		if delivery.Status == DeliveryFailed && delivery.Reason == "stale_swept" {
			t.Fatalf("终态行 %d 被 sweeper 改写", id)
		}
	}

	// 幂等：再扫一次什么都不动。
	again, err := store.SweepStaleDeliveries(ctx, StaleDeliveryThreshold)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again != 0 {
		t.Fatalf("second sweep=%d want 0（幂等）", again)
	}
}

func TestReplayDeliveryOnlyAcceptsTerminalFailures(t *testing.T) {
	fixture := newMailDeliveryPGFixture(t)
	store := fixture.store
	ctx := fixture.ctx

	failed := fixture.insertDelivery(t, "replay-failed", DeliveryFailed, time.Now().Add(-time.Hour))
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE mail_deliveries SET error_summary='smtp down', completed_at=NOW() WHERE id=$1
	`, failed); err != nil {
		t.Fatal(err)
	}

	replayed, err := store.ReplayDelivery(ctx, failed)
	if err != nil {
		t.Fatalf("replay failed delivery: %v", err)
	}
	if replayed.Status != DeliveryQueued || replayed.Reason != "replay" || replayed.ErrorSummary != "" {
		t.Fatalf("replayed = %s/%q/%q，want queued/replay/空 error_summary", replayed.Status, replayed.Reason, replayed.ErrorSummary)
	}
	if replayed.CompletedAt != nil {
		t.Fatal("replay 必须清掉 completed_at")
	}

	// 已经是 queued：不允许再次重放（并发重放只有第一个赢）。
	if _, err := store.ReplayDelivery(ctx, failed); !errors.Is(err, ErrDeliveryNotReplayable) {
		t.Fatalf("second replay err=%v want ErrDeliveryNotReplayable", err)
	}

	// sent / skipped 都不是可重放状态。
	for _, status := range []string{DeliverySent, DeliverySkipped} {
		id := fixture.insertDelivery(t, "replay-"+status, status, time.Now())
		if _, err := store.ReplayDelivery(ctx, id); !errors.Is(err, ErrDeliveryNotReplayable) {
			t.Fatalf("%s: err=%v want ErrDeliveryNotReplayable", status, err)
		}
	}
	// 不存在的 id。
	if _, err := store.ReplayDelivery(ctx, 999999); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("missing delivery err=%v want ErrDeliveryNotFound", err)
	}
}

// TestFailReplayDeliveryCompensatesQueuedRow 覆盖重放入队失败的补偿：
// 刚被重置成 queued 的行必须能改回 failed（否则它停在「queued 但没有 job」，
// 既不会被重试也不会进终态），且不能覆盖已经推进到别的状态的行。
func TestFailReplayDeliveryCompensatesQueuedRow(t *testing.T) {
	fixture := newMailDeliveryPGFixture(t)
	store := fixture.store
	ctx := fixture.ctx

	queued := fixture.insertDelivery(t, "compensate-queued", DeliveryQueued, time.Now())
	if err := store.FailReplayDelivery(ctx, queued, "replay_enqueue_failed"); err != nil {
		t.Fatalf("compensate: %v", err)
	}
	delivery, err := store.GetDelivery(ctx, queued)
	if err != nil {
		t.Fatalf("load compensated delivery: %v", err)
	}
	if delivery.Status != DeliveryFailed || delivery.Reason != "replay_enqueue_failed" {
		t.Fatalf("compensated = %s/%q，want failed/replay_enqueue_failed", delivery.Status, delivery.Reason)
	}
	if delivery.CompletedAt == nil {
		t.Fatal("补偿成终态后必须写 completed_at")
	}

	// 已经被推进到 sent 的行不能被补偿覆盖（并发：另一个重放/worker 已经收敛）。
	sent := fixture.insertDelivery(t, "compensate-sent", DeliverySent, time.Now())
	if err := store.FailReplayDelivery(ctx, sent, "replay_enqueue_failed"); err != nil {
		t.Fatalf("compensate sent: %v", err)
	}
	untouched, err := store.GetDelivery(ctx, sent)
	if err != nil {
		t.Fatalf("load sent delivery: %v", err)
	}
	if untouched.Status != DeliverySent {
		t.Fatalf("sent 行被补偿覆盖成 %s", untouched.Status)
	}
}

type mailDeliveryPGFixture struct {
	ctx   context.Context
	admin *pgxpool.Pool
	pool  *pgxpool.Pool
	store *PostgresStore
}

func newMailDeliveryPGFixture(t *testing.T) *mailDeliveryPGFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("mail_deliveries_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, mailDeliveryFixtureSchemaSQL); err != nil {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatalf("create fixture schema: %v", err)
	}
	fixture := &mailDeliveryPGFixture{ctx: ctx, admin: admin, pool: pool, store: NewPostgresStore(pool)}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	return fixture
}

func (f *mailDeliveryPGFixture) insertDelivery(t *testing.T, key, status string, updatedAt time.Time) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO mail_deliveries (recipient, template_key, idempotency_key, status, updated_at, created_at)
		VALUES ($1, 'password_reset', $2, $3, $4, $4)
		RETURNING id
	`, "user@example.test", key, status, updatedAt).Scan(&id); err != nil {
		t.Fatalf("insert delivery %s: %v", key, err)
	}
	return id
}

const mailDeliveryFixtureSchemaSQL = `
CREATE TABLE mail_deliveries (
  id BIGSERIAL PRIMARY KEY,
  recipient TEXT NOT NULL,
  template_key TEXT NOT NULL,
  template_data JSONB NOT NULL DEFAULT '{}'::jsonb,
  idempotency_key TEXT NOT NULL UNIQUE,
  correlation_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sending', 'sent', 'failed', 'skipped')),
  extension_id TEXT NOT NULL DEFAULT '',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '',
  error_summary TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ
);
`
