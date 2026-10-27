package runtimerollout

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresRolloutRestartAndConcurrentCreate(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := fixture.ctx
	src := strings.Repeat("a", 64)
	dst := strings.Repeat("b", 64)

	svc := NewWithStore(fixture.store)
	plan, err := svc.CreatePlan(ctx, "demo.pg.rollout", src, dst, "admin", 50, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AckNode(ctx, plan.PlanID, "api-1", PhaseStaged, HealthHealthy, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AckNode(ctx, plan.PlanID, "api-2", PhaseStaged, HealthHealthy, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MarkMigrationReady(ctx, plan.PlanID, "admin"); err != nil {
		t.Fatal(err)
	}

	// 重启：新 store/service 从 PostgreSQL 恢复。
	store2, err := NewPostgresStore(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := NewWithStore(store2)
	reloaded, err := svc2.Get(ctx, plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.MigrationReady || reloaded.Phase != PhaseStaged {
		t.Fatalf("reload = %#v", reloaded)
	}
	if len(reloaded.NodeAcks) != 2 {
		t.Fatalf("acks lost: %#v", reloaded.NodeAcks)
	}

	// 同 extension 并发 Create 只能一个赢家。
	const n = 8
	var wait sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			s, err := NewPostgresStore(fixture.pool)
			if err != nil {
				errs <- err
				return
			}
			_, err = NewWithStore(s).CreatePlan(ctx, "demo.pg.race", strings.Repeat("c", 64), strings.Repeat("d", 64), "admin", 10, 2)
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	wins, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			wins++
		} else if err == ErrConflict {
			conflicts++
		} else {
			t.Fatalf("unexpected create err: %v", err)
		}
	}
	if wins != 1 || conflicts != n-1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}

	// 完成 canary → drain → promote → rollback。
	if _, err := svc2.SelectCanary(ctx, plan.PlanID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.AckNode(ctx, plan.PlanID, "api-1", PhaseCanary, HealthHealthy, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.AckNode(ctx, plan.PlanID, "api-2", PhaseCanary, HealthHealthy, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.BeginDrain(ctx, plan.PlanID, "admin"); err != nil {
		t.Fatal(err)
	}
	promoted, err := svc2.PromoteAtomic(ctx, plan.PlanID, "admin")
	if err != nil || promoted.Phase != PhaseActive {
		t.Fatalf("promote = %#v err=%v", promoted, err)
	}
	rolled, err := svc2.Rollback(ctx, plan.PlanID, "admin", "integration")
	if err != nil || rolled.Phase != PhaseRolledBack {
		t.Fatalf("rollback = %#v err=%v", rolled, err)
	}
}

type rolloutFixture struct {
	ctx    context.Context
	admin  *pgxpool.Pool
	pool   *pgxpool.Pool
	store  *PostgresStore
	schema string
}

func newRolloutFixture(t *testing.T) *rolloutFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("runtime_rollout_%d", time.Now().UnixNano())
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
	config.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	// 建表 + revision CAS 列（生产路径两个迁移顺序一致）。
	for _, name := range []string{
		"202607220046_runtime_rollout_plans.sql",
		"202607220047_runtime_rollout_revision.sql",
	} {
		body, err := fs.ReadFile(migrations.Files(), name)
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
	}
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &rolloutFixture{ctx: ctx, admin: admin, pool: pool, store: store, schema: schema}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	return fixture
}
