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

const badgeGrantDedupeScopeMigration = "202611140001_badge_grant_notification_dedupe_scope.sql"

// TestBadgeGrantDedupeScopeMigrationDeclaresRecipientScope 是**静态**结构契约。
//
// 这一刀修的是「全局唯一键 + 不含收件人的键 = 后来者被静默吞掉」。静态断言钉住
// 三件改了就会静默失效的事：
//  1. 键必须由 recipient_user_id 参与拼装（只带 slug 就是原 bug）；
//  2. 判据必须用 `dedupe_key = 'badge:' || payload->>'badgeSlug'`，不能用
//     `LIKE 'badge:%'`——后者会把新格式行也一起改写，且无法在 slug 含冒号时定位；
//  3. Down 故意为空：反向退键会撞全局唯一约束（见文件内注释）。
func TestBadgeGrantDedupeScopeMigrationDeclaresRecipientScope(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgeGrantDedupeScopeMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("dedupe scope migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"SET LOCAL lock_timeout = '5s'",
		"SET LOCAL statement_timeout = '1min'",
		"SET dedupe_key = 'badge:' || recipient_user_id || ':' || (payload->>'badgeSlug')",
		"WHERE type = 'badge_granted'",
		"dedupe_key = 'badge:' || (payload->>'badgeSlug')",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("Up 缺少子句：%s", clause)
		}
	}
	if strings.Contains(up, "LIKE 'badge:%'") {
		t.Fatal("判据不能用 LIKE 'badge:%'：会连带改写已是新格式的行")
	}
	if strings.Contains(down, "UPDATE notifications") {
		t.Fatal("Down 不得反向改写 dedupe_key：会撞全局唯一约束")
	}
}

// TestBadgeGrantDedupeScopeMigrationRewritesLegacyKeys 在隔离 schema 里真跑一遍 Up。
//
// 为什么必须真跑：`'badge:' || recipient_user_id || ':' || …` 里 BIGINT 与 TEXT 的
// 拼接、`payload ? 'badgeSlug'` 的空值保护、以及「新格式行不被二次改写」这三条
// 静态断言都证明不了。
func TestBadgeGrantDedupeScopeMigrationRewritesLegacyKeys(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgeGrantDedupeScopeMigration)
	if err != nil {
		t.Fatal(err)
	}
	upScript := strings.SplitN(string(body), "-- +goose Down", 2)[0]

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

	schema := fmt.Sprintf("badge_dedupe_scope_%d", time.Now().UnixNano())
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

	// 最小 schema：只要 notifications 的 type / recipient_user_id / payload / dedupe_key，
	// 加上真实 DDL 里的全局唯一约束——**约束本身就是 bug 的前提**，去掉它就测不出问题。
	if _, err := conn.Exec(ctx, `
		CREATE TABLE users (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE notifications (
		  id BIGSERIAL PRIMARY KEY,
		  recipient_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		  type TEXT NOT NULL,
		  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
		  dedupe_key TEXT NOT NULL UNIQUE
		);
		INSERT INTO users (id) VALUES (1),(2),(3);
		-- 历史行：两个用户、两枚不同徽章，键都不含收件人（原 bug 的形态）。
		INSERT INTO notifications (recipient_user_id, type, payload, dedupe_key) VALUES
		  (1, 'badge_granted', '{"badgeSlug":"first-topic","badgeName":"初次发帖"}'::jsonb, 'badge:first-topic'),
		  (2, 'badge_granted', '{"badgeSlug":"topic-author","badgeName":"主题作者"}'::jsonb, 'badge:topic-author'),
		  (2, 'reply', '{}'::jsonb, 'comment:9:reply:2'),
		-- slug 含冒号：按位置切分会切错，只有等值判据才对。
		  (3, 'badge_granted', '{"badgeSlug":"vip:gold","badgeName":"金卡"}'::jsonb, 'badge:vip:gold');
	`); err != nil {
		t.Fatalf("prepare minimal schema: %v", err)
	}

	// 先证明 bug 的机制存在：全局唯一约束下，第二个用户拿同一枚徽章时用旧键
	// `badge:{slug}` 会被直接拒掉。没有这条前置断言，后面的「修复有效」可能只是
	// 因为约束根本不存在。
	if _, err := conn.Exec(ctx, `
		INSERT INTO notifications (recipient_user_id, type, payload, dedupe_key)
		VALUES (3, 'badge_granted', '{"badgeSlug":"first-topic"}'::jsonb, 'badge:first-topic')`); err == nil {
		t.Fatal("旧格式键必须受全局唯一约束约束（否则这条测试没在测 bug 本身）")
	}

	if _, err := conn.Exec(ctx, upScript); err != nil {
		t.Fatalf("up: %v", err)
	}

	for userID, want := range map[int64]string{
		1: "badge:1:first-topic",
		2: "badge:2:topic-author",
		3: "badge:3:vip:gold",
	} {
		got := scalarString(t, ctx, conn, fmt.Sprintf(
			`SELECT dedupe_key FROM notifications WHERE recipient_user_id=%d AND type='badge_granted'`, userID))
		if got != want {
			t.Fatalf("user %d dedupe_key = %q, want %q", userID, got, want)
		}
	}

	// 非徽章行一个字节都不许动。
	if got := scalarString(t, ctx, conn, `SELECT dedupe_key FROM notifications WHERE type='reply'`); got != "comment:9:reply:2" {
		t.Fatalf("reply 行被误改：%q", got)
	}

	// 幂等：再跑一次不得报错、不得二次拼装（`badge:1:1:first-topic` 那种形态）。
	if _, err := conn.Exec(ctx, upScript); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if got := scalarString(t, ctx, conn, `SELECT dedupe_key FROM notifications WHERE recipient_user_id=1 AND type='badge_granted'`); got != "badge:1:first-topic" {
		t.Fatalf("第二次 up 改坏了键：%q", got)
	}

	// 改写后两个用户各自能落一行：这正是修复后的运行期形态，也是原 bug 下
	// 唯一约束会挡住的场景。
	if _, err := conn.Exec(ctx, `
		INSERT INTO notifications (recipient_user_id, type, payload, dedupe_key)
		VALUES (3, 'badge_granted', '{"badgeSlug":"first-topic"}'::jsonb, 'badge:3:first-topic')`); err != nil {
		t.Fatalf("修复后第二个用户必须能落同一徽章的通知：%v", err)
	}
	// 键仍然全局唯一：同一用户重复授予同一徽章会被约束挡住（幂等语义不变）。
	if _, err := conn.Exec(ctx, `
		INSERT INTO notifications (recipient_user_id, type, payload, dedupe_key)
		VALUES (3, 'badge_granted', '{"badgeSlug":"first-topic"}'::jsonb, 'badge:3:first-topic')`); err == nil {
		t.Fatal("新键仍必须唯一：同一用户同一徽章不得写第二行")
	}
}
