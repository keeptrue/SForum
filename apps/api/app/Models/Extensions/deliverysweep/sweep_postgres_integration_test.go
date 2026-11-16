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

	agedQueued := fixture.insert(t, "aged-queued", "queued", time.Now().Add(-2*time.Hour), time.Time{})
	agedRunning := fixture.insert(t, "aged-running", "running", time.Now().Add(-45*time.Minute), time.Time{})
	freshQueued := fixture.insert(t, "fresh-queued", "queued", time.Now().Add(-1*time.Minute), time.Time{})
	freshRunning := fixture.insert(t, "fresh-running", "running", time.Now().Add(-2*time.Minute), time.Time{})
	agedSucceeded := fixture.insert(t, "aged-succeeded", "succeeded", time.Now().Add(-3*time.Hour), time.Time{})
	agedFailed := fixture.insert(t, "aged-failed", "failed", time.Now().Add(-3*time.Hour), time.Time{})

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
	fresh := fixture.insert(t, "fresh", "queued", time.Now().Add(-time.Minute), time.Time{})

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

// F3：亚秒阈值必须回退默认，而不是被秒级截断成 0 秒后一把扫掉在飞行。
func TestSweepStaleEventDeliveriesSubSecondThresholdFallsBack(t *testing.T) {
	fixture := newSweepFixture(t)
	fresh := fixture.insert(t, "fresh-subsecond", "queued", time.Now().Add(-time.Minute), time.Time{})

	swept, err := fixture.sweeper.SweepStaleEventDeliveries(fixture.ctx, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 0 {
		t.Fatalf("swept = %d, want 0（500ms 阈值必须回退 15 分钟，不能截断成 0 秒）", swept)
	}
	if status, _, _ := fixture.load(t, fresh); status != "queued" {
		t.Fatalf("fresh delivery = %s, want queued", status)
	}
}

// F2：判据必须是 updated_at 而不是 created_at。
//
// 场景：一条 running 行 created_at 在 2 小时前，但 1 分钟前才被 worker 认领
// （updated_at 刚被推进）——它是**正在跑**的投递，不能当成「调度侧丢了」。
// 这条用例专门守住「把 WHERE 改成 created_at 就误杀在飞行」这个退化。
func TestSweepStaleEventDeliveriesUsesUpdatedAtNotCreatedAt(t *testing.T) {
	fixture := newSweepFixture(t)
	runningJustClaimed := fixture.insert(t, "running-just-claimed", "running",
		time.Now().Add(-2*time.Hour), time.Now().Add(-time.Minute))
	// 对照：同样老的 created_at，但 updated_at 也老 → 真·中途行，必须被扫。
	stalledRunning := fixture.insert(t, "stalled-running", "running",
		time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))

	swept, err := fixture.sweeper.SweepStaleEventDeliveries(fixture.ctx, deliverysweep.StaleEventDeliveryThreshold)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 1 {
		t.Fatalf("swept = %d, want 1（只有 updated_at 也超龄的那条）", swept)
	}
	if status, reason, _ := fixture.load(t, runningJustClaimed); status != "running" || reason != "" {
		t.Fatalf("刚认领的 running 行被误杀：%s/%q", status, reason)
	}
	if status, _, _ := fixture.load(t, stalledRunning); status != "failed" {
		t.Fatalf("真·中途 running 行未被收口：%s", status)
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

// insert 造一行投递；updatedAt 为零值时与 createdAt 相同（历史夹具的默认形态）。
func (f *sweepFixture) insert(t *testing.T, correlation, status string, createdAt, updatedAt time.Time) int64 {
	t.Helper()
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO extension_event_deliveries
		  (extension_id, event_name, event_kind, status, correlation_id, created_at, updated_at)
		VALUES ('observe.plugin', 'topic.created', 'observe', $1, $2, $3, $3)
		RETURNING id`, status, correlation, createdAt.UTC()).Scan(&id); err != nil {
		t.Fatalf("insert delivery %s: %v", correlation, err)
	}
	if !updatedAt.Equal(createdAt) {
		if _, err := f.pool.Exec(f.ctx,
			`UPDATE extension_event_deliveries SET updated_at = $2 WHERE id = $1`, id, updatedAt.UTC()); err != nil {
			t.Fatalf("set updated_at for %s: %v", correlation, err)
		}
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
