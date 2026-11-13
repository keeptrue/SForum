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

const badgeWearMigration = "202611130003_badge_wear.sql"

// TestBadgeWearMigrationDeclaresManualFlag 是**静态**结构契约：不连库，
// 只钉住四个「改了就会静默失效」的子句。
//
// 这一刀没有新表，唯一的 schema 变化就是一列 + 两条 COMMENT。COMMENT 不是装饰：
// `top_badge_id` 的语义从「自动推导的最高级」变成「当前展示的徽章」，
// 不写下来，下一个读库的人会按旧语义写一条覆盖用户选择的 SQL。
func TestBadgeWearMigrationDeclaresManualFlag(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgeWearMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("badge wear migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	down := strings.Join(strings.Fields(parts[1]), " ")

	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		// IF NOT EXISTS + NOT NULL DEFAULT FALSE：既有行立刻有值，无需回填。
		"ALTER TABLE users ADD COLUMN IF NOT EXISTS top_badge_manual BOOLEAN NOT NULL DEFAULT FALSE",
		"COMMENT ON COLUMN users.top_badge_id IS",
		"COMMENT ON COLUMN users.top_badge_manual IS",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("Up 缺少子句：%s", clause)
		}
	}
	if !strings.Contains(down, "DROP COLUMN IF EXISTS top_badge_manual") {
		t.Fatalf("Down 缺少删列子句：%s", down)
	}
}

// TestBadgeWearMigrationUpDownUp 在隔离 schema 里跑 up → down → up，证明可逆。
//
// 为什么必须真跑：静态断言只能证明「写了 DROP COLUMN」，证明不了在有数据时
// 加列/删列真的能来回（NOT NULL DEFAULT 的回填、COMMENT 对不存在列的报错、
// 以及删列后 top_badge_id 是否被误伤）。
func TestBadgeWearMigrationUpDownUp(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgeWearMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatalf("badge wear migration has no Down section")
	}
	upScript, downScript := parts[0], parts[1]

	config, err := pgx.ParseConfig(testdb.IsolatedURL(t))
	if err != nil {
		t.Fatalf("parse isolated url: %v", err)
	}
	// 整个脚本一条 Query 发出去：simple protocol 的多语句在同一个隐式事务里执行，
	// 与 migrator 执行 Goose 脚本的方式一致。
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	schema := fmt.Sprintf("badge_wear_migration_%d", time.Now().UnixNano())
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

	// 迁移的 COMMENT 指向 users.top_badge_id，所以最小前置表必须带上它。
	if _, err := conn.Exec(ctx, `
		CREATE TABLE users (
		  id BIGSERIAL PRIMARY KEY,
		  top_badge_id BIGINT
		);
		INSERT INTO users (id, top_badge_id) VALUES (1, 7);
	`); err != nil {
		t.Fatalf("prepare minimal schema: %v", err)
	}

	manualColumnCount := func() int64 {
		return scalarInt(t, ctx, conn, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'top_badge_manual'`)
	}

	// up：列出现、既有行拿到 FALSE、既有展示位不受影响、COMMENT 落地。
	if _, err := conn.Exec(ctx, upScript); err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := manualColumnCount(); got != 1 {
		t.Fatalf("top_badge_manual missing after up (count = %d)", got)
	}
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM users WHERE id = 1 AND top_badge_manual = FALSE`); got != 1 {
		t.Fatalf("既有行没有拿到 DEFAULT FALSE（count = %d）", got)
	}
	if got := scalarInt(t, ctx, conn, `SELECT top_badge_id FROM users WHERE id = 1`); got != 7 {
		t.Fatalf("up 改动了既有 top_badge_id：%d", got)
	}
	if got := scalarString(t, ctx, conn, `SELECT col_description('users'::regclass,
		(SELECT attnum FROM pg_attribute WHERE attrelid = 'users'::regclass AND attname = 'top_badge_manual'))`); strings.TrimSpace(got) == "" {
		t.Fatal("top_badge_manual 缺少 COMMENT：语义变化必须写在库里")
	}

	// 写入佩戴标记后再 down：列消失、top_badge_id 保留（回滚只丢「用户选择」这一位）。
	if _, err := conn.Exec(ctx, `UPDATE users SET top_badge_manual = TRUE WHERE id = 1`); err != nil {
		t.Fatalf("set manual: %v", err)
	}
	if _, err := conn.Exec(ctx, downScript); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := manualColumnCount(); got != 0 {
		t.Fatalf("top_badge_manual still exists after down (count = %d)", got)
	}
	if got := scalarInt(t, ctx, conn, `SELECT top_badge_id FROM users WHERE id = 1`); got != 7 {
		t.Fatalf("down 误删了 top_badge_id：%d", got)
	}

	// 再 up：回滚不是单向闸门；新列回到默认 FALSE（旧的 TRUE 不该被记住）。
	if _, err := conn.Exec(ctx, upScript); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if got := manualColumnCount(); got != 1 {
		t.Fatalf("top_badge_manual missing after second up (count = %d)", got)
	}
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM users WHERE id = 1 AND top_badge_manual = FALSE`); got != 1 {
		t.Fatalf("second up 后的行不是默认 FALSE（count = %d）", got)
	}
}
