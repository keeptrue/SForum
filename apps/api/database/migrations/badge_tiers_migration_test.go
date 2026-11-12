package migrations

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const badgeTiersMigration = "202611120001_badge_tiers.sql"

// 徽章等级表迁移（202611120001）的两条不变量：
//  1. 形状：建表 + 三行内置种子 + 把 tier CHECK 换成外键（含 ON UPDATE/DELETE 策略）；
//  2. 可逆：Down 必须先把非内置 key 的徽章落回 bronze，再拆外键、恢复原 CHECK、删表。
//
// 第 2 条是「迁移能不能回滚」的唯一保证：顺序错了外键会挡住 UPDATE，或者 DROP TABLE
// 会被引用挡住——两种都会让运维在事故现场卡在半路。

// TestBadgeTiersMigrationShape 固定迁移的 SQL 形状（不连库）。
func TestBadgeTiersMigrationShape(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgeTiersMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("badge tier migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"CREATE TABLE IF NOT EXISTS badge_tiers",
		"key TEXT PRIMARY KEY",
		"name_zh TEXT NOT NULL",
		"name_en TEXT NOT NULL",
		"color TEXT NOT NULL DEFAULT ''",
		"position INTEGER NOT NULL DEFAULT 0",
		"enabled BOOLEAN NOT NULL DEFAULT TRUE",
		"CHECK (key ~ '^[a-z][a-z0-9_]{1,31}$')",
		// 三行内置种子：position 与旧 Weight() 的排序等价（gold 0 / silver 1 / bronze 2）。
		"('gold', '金牌', 'Gold', 'i-lucide-crown', 'amber', 0, TRUE)",
		"('silver', '银牌', 'Silver', 'i-lucide-medal', 'slate', 1, TRUE)",
		"('bronze', '铜牌', 'Bronze', 'i-lucide-award', '#b45309', 2, TRUE)",
		"ON CONFLICT (key) DO NOTHING",
		// CHECK → 外键：等级的存在性从代码枚举变成表里的行。
		"ALTER TABLE badges DROP CONSTRAINT IF EXISTS badges_tier_check",
		"ADD CONSTRAINT badges_tier_fkey FOREIGN KEY (tier) REFERENCES badge_tiers(key)",
		"ON UPDATE CASCADE ON DELETE RESTRICT",
		"CREATE INDEX IF NOT EXISTS badges_tier_idx ON badges (tier)",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("badge tier migration Up missing %q", clause)
		}
	}

	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, clause := range []string{
		// 顺序即正确性：先迁数据，再拆外键，再恢复 CHECK，最后删表。
		"UPDATE badges SET tier = 'bronze' WHERE tier NOT IN ('bronze', 'silver', 'gold')",
		"ALTER TABLE badges DROP CONSTRAINT IF EXISTS badges_tier_fkey",
		"DROP INDEX IF EXISTS badges_tier_idx",
		"ADD CONSTRAINT badges_tier_check CHECK (tier IN ('bronze', 'silver', 'gold'))",
		"DROP TABLE IF EXISTS badge_tiers",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("badge tier migration Down missing %q", clause)
		}
	}
	order := []string{
		"UPDATE badges SET tier = 'bronze'",
		"DROP CONSTRAINT IF EXISTS badges_tier_fkey",
		"ADD CONSTRAINT badges_tier_check",
		"DROP TABLE IF EXISTS badge_tiers",
	}
	previous := -1
	for _, needle := range order {
		index := strings.Index(down, needle)
		if index < 0 {
			t.Fatalf("Down missing %q", needle)
		}
		if index < previous {
			t.Fatalf("Down step %q is out of order: a later step must not run before an earlier one", needle)
		}
		previous = index
	}
}

// TestBadgeTiersMigrationUpDownUp 在隔离库里跑 up → down → up，证明可逆。
//
// 用独立的 schema + 升级前的 badges 表（带 tier CHECK），因为整条迁移链已经跑过
// 一次（badge_tiers 存在），直接对已迁移库重放 Up 会在 ADD CONSTRAINT 处撞重名。
// 这里要验的正是「从一个装了旧 CHECK 的库出发，up 能升上去、down 能干净退回来」。
func TestBadgeTiersMigrationUpDownUp(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgeTiersMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("badge tier migration has no Down section")
	}
	upScript, downScript := parts[0], parts[1]

	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	schema := fmt.Sprintf("badge_tiers_migration_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+quoted+",public"); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	// 升级前的 badges 表：只有 tier CHECK，没有等级表。
	if _, err := conn.Exec(ctx, `
		CREATE TABLE badges (
		  id BIGSERIAL PRIMARY KEY,
		  slug TEXT NOT NULL UNIQUE,
		  name TEXT NOT NULL,
		  tier TEXT NOT NULL CHECK (tier IN ('bronze','silver','gold')),
		  enabled BOOLEAN NOT NULL DEFAULT TRUE
		)`); err != nil {
		t.Fatalf("create legacy badges table: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO badges (slug, name, tier) VALUES ('veteran','老兵','gold')`); err != nil {
		t.Fatalf("seed legacy badge: %v", err)
	}

	// 每条脚本在自己的事务里跑完再提交：迁移里的 SET LOCAL 需要事务作用域，
	// 而失败时也必须整体回滚（半升级的库比没升级的库更难救）。
	run := func(label string, script string) {
		t.Helper()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("%s: begin: %v", label, err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := execScript(ctx, tx, script); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("%s: commit: %v", label, err)
		}
	}

	// up：等级表出现、三行种子、外键生效（未知档位被拒）。
	run("up", upScript)
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM badge_tiers`); got != 3 {
		t.Fatalf("badge_tiers rows = %d, want 3", got)
	}
	if got := scalarString(t, ctx, conn, `SELECT tier FROM badges WHERE slug = 'veteran'`); got != "gold" {
		t.Fatalf("legacy badge tier = %q, want gold", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO badges (slug, name, tier) VALUES ('x','x','platinum')`); err == nil {
		t.Fatal("foreign key must reject an unknown tier after up")
	}
	// 自定义档位 + 引用它的徽章：这是 Down 必须迁移掉的那类数据。
	if _, err := conn.Exec(ctx, `
		INSERT INTO badge_tiers (key, name_zh, name_en, position) VALUES ('limited','限定','Limited', 5)`); err != nil {
		t.Fatalf("insert custom tier: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO badges (slug, name, tier) VALUES ('event','活动','limited')`); err != nil {
		t.Fatalf("insert badge on custom tier: %v", err)
	}

	// down：自定义档位的徽章落回 bronze，等级表消失，旧 CHECK 恢复。
	run("down", downScript)
	if got := scalarString(t, ctx, conn, `SELECT tier FROM badges WHERE slug = 'event'`); got != "bronze" {
		t.Fatalf("custom-tier badge tier = %q, want bronze after down", got)
	}
	// 只看当前 schema：整条迁移链在 public 里也建过 badge_tiers，不限定 schema 会永远查到 1。
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM pg_class WHERE relname = 'badge_tiers' AND relnamespace = current_schema()::regnamespace`); got != 0 {
		t.Fatalf("badge_tiers still exists after down (count = %d)", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO badges (slug, name, tier) VALUES ('y','y','limited')`); err == nil {
		t.Fatal("the legacy CHECK must reject a non-builtin tier after down")
	}
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM badges`); got != 2 {
		t.Fatalf("badges rows = %d, want 2 (data must survive the rollback)", got)
	}

	// 再 up：回滚不是单向闸门。
	run("second up", upScript)
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM badge_tiers`); got != 3 {
		t.Fatalf("badge_tiers rows after second up = %d, want 3", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO badges (slug, name, tier) VALUES ('z','z','platinum')`); err == nil {
		t.Fatal("foreign key must be back in place after the second up")
	}
}

func scalarInt(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string) int64 {
	t.Helper()
	var value int64
	if err := conn.QueryRow(ctx, sql).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return value
}

func scalarString(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string) string {
	t.Helper()
	var value string
	if err := conn.QueryRow(ctx, sql).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return value
}
