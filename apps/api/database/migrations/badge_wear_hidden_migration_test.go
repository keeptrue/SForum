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

const badgeWearHiddenMigration = "202611130004_badge_wear_hidden.sql"

// TestBadgeWearHiddenMigrationDeclaresHiddenFlag 是静态结构契约：不连库，
// 只钉住「改了就会静默失效」的子句。
//
// hidden 列引入第三种展示态（「不佩戴」）。IF NOT EXISTS + NOT NULL DEFAULT FALSE
// 让存量行自动落在「展示自动推导」，与旧语义一致；COMMENT 写进库里，否则下一个
// 读库的人不会知道这一位是「用户的明确选择」而不是「没有徽章」。
func TestBadgeWearHiddenMigrationDeclaresHiddenFlag(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgeWearHiddenMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("badge wear hidden migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	down := strings.Join(strings.Fields(parts[1]), " ")

	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"ALTER TABLE users ADD COLUMN IF NOT EXISTS top_badge_hidden BOOLEAN NOT NULL DEFAULT FALSE",
		"COMMENT ON COLUMN users.top_badge_hidden IS",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("Up 缺少子句：%s", clause)
		}
	}
	if !strings.Contains(down, "DROP COLUMN IF EXISTS top_badge_hidden") {
		t.Fatalf("Down 缺少删列子句：%s", down)
	}
}

// TestBadgeWearHiddenMigrationUpDownUp 在隔离 schema 里跑 up → down → up，证明可逆。
func TestBadgeWearHiddenMigrationUpDownUp(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgeWearHiddenMigration)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatalf("badge wear hidden migration has no Down section")
	}
	upScript, downScript := parts[0], parts[1]

	config, err := pgx.ParseConfig(testdb.IsolatedURL(t))
	if err != nil {
		t.Fatalf("parse isolated url: %v", err)
	}
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	schema := fmt.Sprintf("badge_wear_hidden_migration_%d", time.Now().UnixNano())
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

	if _, err := conn.Exec(ctx, `
		CREATE TABLE users (
		  id BIGSERIAL PRIMARY KEY,
		  top_badge_id BIGINT
		);
		INSERT INTO users (id, top_badge_id) VALUES (1, 7);
	`); err != nil {
		t.Fatalf("prepare minimal schema: %v", err)
	}

	hiddenColumnCount := func() int64 {
		return scalarInt(t, ctx, conn, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'top_badge_hidden'`)
	}

	// up：列出现、既有行拿到 FALSE、既有展示位不受影响、COMMENT 落地。
	if _, err := conn.Exec(ctx, upScript); err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := hiddenColumnCount(); got != 1 {
		t.Fatalf("top_badge_hidden missing after up (count = %d)", got)
	}
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM users WHERE id = 1 AND top_badge_hidden = FALSE`); got != 1 {
		t.Fatalf("既有行没有拿到 DEFAULT FALSE（count = %d）", got)
	}
	if got := scalarInt(t, ctx, conn, `SELECT top_badge_id FROM users WHERE id = 1`); got != 7 {
		t.Fatalf("up 改动了既有 top_badge_id：%d", got)
	}
	if got := scalarString(t, ctx, conn, `SELECT col_description('users'::regclass,
		(SELECT attnum FROM pg_attribute WHERE attrelid = 'users'::regclass AND attname = 'top_badge_hidden'))`); strings.TrimSpace(got) == "" {
		t.Fatal("top_badge_hidden 缺少 COMMENT：语义变化必须写在库里")
	}

	// 写入隐藏态后再 down：列消失、top_badge_id 保留（回滚回到自动推导语义）。
	if _, err := conn.Exec(ctx, `UPDATE users SET top_badge_id = NULL, top_badge_hidden = TRUE WHERE id = 1`); err != nil {
		t.Fatalf("set hidden: %v", err)
	}
	if _, err := conn.Exec(ctx, downScript); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := hiddenColumnCount(); got != 0 {
		t.Fatalf("top_badge_hidden still exists after down (count = %d)", got)
	}
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM users WHERE id = 1 AND top_badge_id IS NULL`); got != 1 {
		t.Fatalf("down 后 top_badge_id 应保持 up/down 前写入的 NULL（count = %d）", got)
	}

	// 再 up：回滚不是单向闸门；新列回到默认 FALSE。
	if _, err := conn.Exec(ctx, upScript); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if got := hiddenColumnCount(); got != 1 {
		t.Fatalf("top_badge_hidden missing after second up (count = %d)", got)
	}
}
