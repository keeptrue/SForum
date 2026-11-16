package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 真实 Postgres 上验证「delivery 行与 River 入队同事务」：提交则两者都在，
// 入队失败则一行都不留。SQL 直接取自 migrations 里的真实 DDL，避免测试
// 自带一份会漂移的表结构。
func TestPostgresObserveDeliveryIsAtomicWithEnqueue(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("event_delivery_%d", time.Now().UnixNano())
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

	store := extensions.NewPostgresStore(pool)
	extension := runtimeExtension("observe.plugin")
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicCreated, Kind: appevents.KindObserve}}

	// ① 入队失败：事务回滚，extension_event_deliveries 必须保持 0 行。
	failing := observeTestManager(t, store, JobDispatcherAdapter{
		Dispatcher: supportjobs.NewDispatcher(staticRiverClient{err: errors.New("river insert failed")}),
	}, pool)
	if err := failing.Start(ctx, extension); err != nil {
		t.Fatalf("start: %v", err)
	}
	result := failing.Emit(ctx, appevents.Envelope{
		Name: appevents.TopicCreated, Kind: appevents.KindObserve, CorrelationID: "corr-pg-fail",
	})
	if result.OK || result.Reason != "extension.delivery_enqueue_failed" {
		t.Fatalf("expected enqueue failure, got %#v", result)
	}
	if rows := countEventDeliveries(t, ctx, pool); rows != 0 {
		t.Fatalf("enqueue failure left %d orphan delivery row(s)", rows)
	}

	// ② 正常路径：提交后行真的在库里，且状态是 queued（等 worker 消费）。
	working := observeTestManager(t, store, JobDispatcherAdapter{
		Dispatcher: supportjobs.NewDispatcher(staticRiverClient{}),
	}, pool)
	if err := working.Start(ctx, extension); err != nil {
		t.Fatalf("start: %v", err)
	}
	result = working.Emit(ctx, appevents.Envelope{
		Name: appevents.TopicCreated, Kind: appevents.KindObserve, CorrelationID: "corr-pg-ok",
	})
	if !result.OK {
		t.Fatalf("observe emit failed: %#v", result)
	}
	if rows := countEventDeliveries(t, ctx, pool); rows != 1 {
		t.Fatalf("committed delivery rows = %d, want 1", rows)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM extension_event_deliveries`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != extensions.DeliveryQueued {
		t.Fatalf("delivery status = %q, want queued", status)
	}
}

// staticRiverClient 只实现 Dispatcher 需要的三个方法；err 非空时入队失败，
// 为空时返回一个空结果（不真的写 river_job 表，本用例只关心事务边界）。
type staticRiverClient struct{ err error }

func (c staticRiverClient) Insert(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, c.err
}

func (c staticRiverClient) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &rivertype.JobInsertResult{}, nil
}

func (c staticRiverClient) InsertMany(context.Context, []river.InsertManyParams) ([]*rivertype.JobInsertResult, error) {
	return nil, c.err
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

func countEventDeliveries(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_event_deliveries`).Scan(&rows); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	return rows
}
