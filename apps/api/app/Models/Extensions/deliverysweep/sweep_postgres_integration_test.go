package deliverysweep_test

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	deliverysweep "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/deliverysweep"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 真库断言：stale 收口只碰超龄的非终态行，且幂等。
//
// 为什么必须真库：WHERE 条件（status + updated_at 阈值）就是这条修复的全部内容，
// 用 fake 测等于自己出题自己答。表结构直接取 migrations 里的真实 DDL，避免测试
// 自带一份会漂移的 schema。
func TestSweepStaleEventDeliveriesTouchesOnlyAgedNonTerminalRows(t *testing.T) {
	fixture := newSweepFixture(t)
	ctx := fixture.ctx

	agedQueued := fixture.insert(t, "aged-queued", "queued", time.Now().Add(-2*time.Hour))
	agedRunning := fixture.insert(t, "aged-running", "running", time.Now().Add(-45*time.Minute))
	freshQueued := fixture.insert(t, "fresh-queued", "queued", time.Now().Add(-1*time.Minute))
	freshRunning := fixture.insert(t, "fresh-running", "running", time.Now().Add(-2*time.Minute))
	agedSucceeded := fixture.insert(t, "aged-succeeded", "succeeded", time.Now().Add(-3*time.Hour))
	agedFailed := fixture.insert(t, "aged-failed", "failed", time.Now().Add(-3*time.Hour))

	swept, err := fixture.sweeper.SweepStaleEventDeliveries(ctx, deliverysweep.StaleEventDeliveryThreshold)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 2 {
		t.Fatalf("swept = %d, want 2（只有两条超龄的非终态行）", swept)
	}
	for _, id := range []int64{agedQueued, agedRunning} {
		status, reason, completedAt := fixture.load(t, id)
		if status != "failed" || reason != deliverysweep.ReasonStaleSwept {
			t.Fatalf("delivery %d = %s/%q, want failed/%q", id, status, reason, deliverysweep.ReasonStaleSwept)
		}
		if completedAt == nil {
			t.Fatalf("delivery %d 缺少 completed_at（终态必须收口）", id)
		}
	}
	// 未超龄的行必须原样不动：这是防误杀在飞投递的唯一证据。
	for _, id := range []int64{freshQueued, freshRunning} {
		status, reason, _ := fixture.load(t, id)
		want := "queued"
		if id == freshRunning {
			want = "running"
		}
		if status != want || reason != "" {
			t.Fatalf("fresh delivery %d = %s/%q, want %s/\"\"", id, status, reason, want)
		}
	}
	// 终态行不该被二次改写（succeeded 不能被降级成 failed）。
	for id, want := range map[int64]string{agedSucceeded: "succeeded", agedFailed: "failed"} {
		status, _, _ := fixture.load(t, id)
		if status != want {
			t.Fatalf("terminal delivery %d = %s, want %s", id, status, want)
		}
	}

	// 幂等：第二轮没有可收口的行。
	again, err := fixture.sweeper.SweepStaleEventDeliveries(ctx, deliverysweep.StaleEventDeliveryThreshold)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again != 0 {
		t.Fatalf("second sweep = %d, want 0（幂等）", again)
	}
}

// 零/负阈值回退到默认值，而不是把所有在飞的行一把扫掉。
func TestSweepStaleEventDeliveriesFallsBackToDefaultThreshold(t *testing.T) {
	fixture := newSweepFixture(t)
	fresh := fixture.insert(t, "fresh", "queued", time.Now().Add(-time.Minute))

	swept, err := fixture.sweeper.SweepStaleEventDeliveries(fixture.ctx, 0)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 0 {
		t.Fatalf("swept = %d, want 0（阈值 0 必须回退默认 15 分钟）", swept)
	}
	if status, _, _ := fixture.load(t, fresh); status != "queued" {
		t.Fatalf("fresh delivery = %s, want queued", status)
	}
}

type sweepFixture struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	sweeper *deliverysweep.PostgresSweeper
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("delivery_sweep_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop private schema %s: %v", schema, err)
		}
		admin.Close()
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	for _, name := range []string{
		"202607050005_extensions.sql",
		"202607060004_extension_event_deliveries.sql",
	} {
		if _, err := pool.Exec(ctx, migrationUpSQL(t, name)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id, type, name) VALUES ('observe.plugin', 'plugin', 'Observe')`); err != nil {
		t.Fatal(err)
	}
	return &sweepFixture{ctx: ctx, pool: pool, sweeper: deliverysweep.NewPostgresSweeper(pool)}
}

func (f *sweepFixture) insert(t *testing.T, correlation, status string, updatedAt time.Time) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO extension_event_deliveries
		  (extension_id, event_name, event_kind, status, correlation_id, created_at, updated_at)
		VALUES ('observe.plugin', 'topic.created', 'observe', $1, $2, $3, $3)
		RETURNING id`, status, correlation, updatedAt.UTC()).Scan(&id); err != nil {
		t.Fatalf("insert delivery %s: %v", correlation, err)
	}
	return id
}

func (f *sweepFixture) load(t *testing.T, id int64) (status, reason string, completedAt *time.Time) {
	t.Helper()
	if err := f.pool.QueryRow(f.ctx,
		`SELECT status, reason, completed_at FROM extension_event_deliveries WHERE id = $1`, id).
		Scan(&status, &reason, &completedAt); err != nil {
		t.Fatalf("load delivery %d: %v", id, err)
	}
	return status, reason, completedAt
}

func migrationUpSQL(t *testing.T, name string) string {
	t.Helper()
	content, err := fs.ReadFile(migrations.Files(), name)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	up, _, _ := strings.Cut(string(content), "-- +goose Down")
	lines := make([]string, 0, 32)
	for _, line := range strings.Split(up, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "-- +goose") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
