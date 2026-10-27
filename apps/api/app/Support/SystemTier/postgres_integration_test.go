package systemtier

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresSystemTierCLIDisableAcrossProcess(t *testing.T) {
	fixture := newSystemTierFixture(t)
	ctx := fixture.ctx

	// 进程 A：operator upsert
	cli := NewWithStore(fixture.store)
	if err := cli.Upsert(ctx, Member{
		ExtensionID: "sys.auth.pg", Role: RoleAuth, Priority: 1, Enabled: true, UpdatedBy: "cli",
	}); err != nil {
		t.Fatal(err)
	}

	// 进程 B：API 重启后可见
	store2, err := NewPostgresStore(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	api := NewWithStore(store2)
	order, err := api.LoadOrder(ctx, false)
	if err != nil || len(order) != 1 {
		t.Fatalf("load = %#v err=%v", order, err)
	}

	// Safe Mode 在加载代码前绕过
	if order, err := api.LoadOrder(ctx, true); err != nil || order != nil {
		t.Fatalf("safe mode = %#v err=%v", order, err)
	}

	// 进程 C：CLI disable（API 宕机仍可）
	if err := cli.Disable(ctx, "sys.auth.pg", "recovery"); err != nil {
		t.Fatal(err)
	}
	order, err = api.LoadOrder(ctx, false)
	if err != nil || len(order) != 0 {
		t.Fatalf("after disable = %#v err=%v", order, err)
	}
}

type systemTierFixture struct {
	ctx   context.Context
	admin *pgxpool.Pool
	pool  *pgxpool.Pool
	store *PostgresStore
}

func newSystemTierFixture(t *testing.T) *systemTierFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("system_tier_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	body, err := fs.ReadFile(migrations.Files(), "202607220046_runtime_rollout_plans.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.SplitN(string(body), "-- +goose Down", 2)[0]
	if _, err := pool.Exec(ctx, up); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	return &systemTierFixture{ctx: ctx, admin: admin, pool: pool, store: store}
}
