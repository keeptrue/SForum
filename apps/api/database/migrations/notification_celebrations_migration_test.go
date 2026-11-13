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

const celebrationMigration = "202611130002_notification_celebrations.sql"

// TestCelebrationMigrationDeclaresPresentationFlagAndCursor 是**静态**结构契约：
// 不连库，只钉住四个「改了就会静默失效」的子句。
func TestCelebrationMigrationDeclaresPresentationFlagAndCursor(t *testing.T) {
	body, err := fs.ReadFile(Files(), celebrationMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("celebration migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"SET LOCAL lock_timeout = '5s'",
		"SET LOCAL statement_timeout = '1min'",
		// 标记必须用 jsonb 合并（`||`）而不是整段替换：整段替换会丢掉既有 icon。
		`UPDATE notification_type_descriptors SET presentation = presentation || '{"celebration":true}'::jsonb`,
		"WHERE type = 'badge_granted'",
		// 游标：主键即用户，非负 CHECK，updated_at 供运维看最近一次推进。
		"CREATE TABLE IF NOT EXISTS user_celebration_cursors",
		"user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE",
		"cursor_notification_id BIGINT NOT NULL DEFAULT 0 CHECK (cursor_notification_id >= 0)",
		"updated_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"COMMENT ON TABLE user_celebration_cursors",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("Up 缺少子句：%s", clause)
		}
	}
	for _, clause := range []string{
		"DROP TABLE IF EXISTS user_celebration_cursors",
		// Down 必须只去掉这一个键（`-` 而不是整段覆盖），否则回滚会抹掉运营自定义的
		// label/body/icon。
		"SET presentation = presentation - 'celebration'",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("Down 缺少子句：%s", clause)
		}
	}
}

// TestCelebrationMigrationUpDownUp 在隔离 schema 里跑 up → down → up，证明可逆。
//
// 为什么必须真跑：静态断言只能证明「写了 DROP」，证明不了「先建表再退标记」在
// 有数据时真的能退干净（例如 CHECK、FK、jsonb 运算符的优先级）。
//
// 三个前置表在各 schema 里自建最小版本：迁移只依赖
// users(id) / notifications(type) / notification_type_descriptors(type,presentation,updated_at)。
func TestCelebrationMigrationUpDownUp(t *testing.T) {
	body, err := fs.ReadFile(Files(), celebrationMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatalf("celebration migration has no Down section")
	}
	upScript, downScript := parts[0], parts[1]

	config, err := pgx.ParseConfig(testdb.IsolatedURL(t))
	if err != nil {
		t.Fatalf("parse isolated url: %v", err)
	}
	// 整个脚本一条 Query 发出去：迁移里有 `DO $$ … $$` 块，按分号切分会把它切碎；
	// simple protocol 的多语句在同一个隐式事务里执行，`SET LOCAL` 因此有意义。
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	schema := fmt.Sprintf("celebration_migration_%d", time.Now().UnixNano())
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
		CREATE TABLE users (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE notification_type_descriptors (
		  type TEXT PRIMARY KEY,
		  presentation JSONB NOT NULL DEFAULT '{}'::jsonb,
		  active BOOLEAN NOT NULL DEFAULT TRUE,
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE notifications (
		  id BIGSERIAL PRIMARY KEY,
		  recipient_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		  type TEXT NOT NULL
		);
		INSERT INTO notification_type_descriptors (type, presentation)
		VALUES ('badge_granted', '{"icon":"i-lucide-award"}'::jsonb);
		INSERT INTO users (id) VALUES (1);
	`); err != nil {
		t.Fatalf("prepare minimal schema: %v", err)
	}

	// up：标记出现、既有 icon 保留、游标表可写。
	if _, err := conn.Exec(ctx, upScript); err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := scalarString(t, ctx, conn, `SELECT presentation->>'celebration' FROM notification_type_descriptors WHERE type='badge_granted'`); got != "true" {
		t.Fatalf("celebration flag = %q, want true", got)
	}
	if got := scalarString(t, ctx, conn, `SELECT presentation->>'icon' FROM notification_type_descriptors WHERE type='badge_granted'`); got != "i-lucide-award" {
		t.Fatalf("既有 icon 被覆盖：%q", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO user_celebration_cursors (user_id, cursor_notification_id) VALUES (1, 5)`); err != nil {
		t.Fatalf("insert cursor: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO user_celebration_cursors (user_id, cursor_notification_id) VALUES (1, 9) ON CONFLICT (user_id) DO UPDATE SET cursor_notification_id = GREATEST(user_celebration_cursors.cursor_notification_id, EXCLUDED.cursor_notification_id)`); err != nil {
		t.Fatalf("GREATEST upsert: %v", err)
	}
	if got := scalarInt(t, ctx, conn, `SELECT cursor_notification_id FROM user_celebration_cursors WHERE user_id=1`); got != 9 {
		t.Fatalf("cursor = %d, want 9", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO user_celebration_cursors (user_id, cursor_notification_id) VALUES (1, -1) ON CONFLICT (user_id) DO UPDATE SET cursor_notification_id = EXCLUDED.cursor_notification_id`); err == nil {
		t.Fatal("负游标必须被 CHECK 拒绝")
	}

	// down：表消失、标记被去掉、icon 仍在、其它类型不受影响。
	if _, err := conn.Exec(ctx, `INSERT INTO notification_type_descriptors (type, presentation) VALUES ('reply', '{"celebration":true}'::jsonb)`); err != nil {
		t.Fatalf("seed other type: %v", err)
	}
	if _, err := conn.Exec(ctx, downScript); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM pg_class WHERE relname = 'user_celebration_cursors' AND relnamespace = current_schema()::regnamespace`); got != 0 {
		t.Fatalf("user_celebration_cursors still exists after down (count = %d)", got)
	}
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM notification_type_descriptors WHERE type='badge_granted' AND presentation ? 'celebration'`); got != 0 {
		t.Fatalf("celebration key survived down (count = %d)", got)
	}
	if got := scalarString(t, ctx, conn, `SELECT presentation->>'icon' FROM notification_type_descriptors WHERE type='badge_granted'`); got != "i-lucide-award" {
		t.Fatalf("down 抹掉了 icon：%q", got)
	}
	if got := scalarString(t, ctx, conn, `SELECT presentation->>'celebration' FROM notification_type_descriptors WHERE type='reply'`); got != "true" {
		t.Fatalf("down 误伤了其它类型的标记：%q", got)
	}

	// 再 up：回滚不是单向闸门。
	if _, err := conn.Exec(ctx, upScript); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if got := scalarString(t, ctx, conn, `SELECT presentation->>'celebration' FROM notification_type_descriptors WHERE type='badge_granted'`); got != "true" {
		t.Fatalf("second up flag = %q, want true", got)
	}
	if got := scalarInt(t, ctx, conn, `SELECT count(*) FROM pg_class WHERE relname = 'user_celebration_cursors' AND relnamespace = current_schema()::regnamespace`); got != 1 {
		t.Fatalf("user_celebration_cursors missing after second up (count = %d)", got)
	}
}
