package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

const contentBookmarksMigration = "202611100001_content_bookmarks.sql"

// TestContentBookmarksMigrationDeclaresRelationCountsAndSeeds 固定收藏切片的
// 结构契约：关系行（唯一 + 目标正数 + 两档可见性 + 提醒列）、公开计数列、
// 三个索引、稳定读视图、通知类型种子。Down 必须对称拆除且不误伤其它表。
func TestContentBookmarksMigrationDeclaresRelationCountsAndSeeds(t *testing.T) {
	body, err := fs.ReadFile(Files(), contentBookmarksMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("content bookmarks migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"SET LOCAL lock_timeout = '5s'",
		"SET LOCAL statement_timeout = '1min'",
		// 关系行：用户删除 CASCADE；目标不加外键（多态，内容软删）。
		"CREATE TABLE IF NOT EXISTS content_bookmarks",
		"user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE",
		"target_type TEXT NOT NULL",
		"target_id BIGINT NOT NULL",
		"visibility TEXT NOT NULL DEFAULT 'private'",
		"remind_at TIMESTAMPTZ",
		"reminded_at TIMESTAMPTZ",
		"CONSTRAINT content_bookmarks_target_type_check CHECK (target_type IN ('topic', 'comment'))",
		"CONSTRAINT content_bookmarks_visibility_check CHECK (visibility IN ('private', 'public'))",
		"CONSTRAINT content_bookmarks_target_positive CHECK (target_id > 0)",
		"CONSTRAINT content_bookmarks_unique UNIQUE (user_id, target_type, target_id)",
		// 三个索引：公开名单、本人 keyset、到期扫描（部分索引）。
		"CREATE INDEX IF NOT EXISTS content_bookmarks_public_idx ON content_bookmarks (target_type, target_id, created_at DESC, id DESC) WHERE visibility = 'public'",
		"CREATE INDEX IF NOT EXISTS content_bookmarks_user_idx ON content_bookmarks (user_id, created_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS content_bookmarks_due_idx ON content_bookmarks (remind_at) WHERE remind_at IS NOT NULL AND reminded_at IS NULL",
		// 计数列：只统计公开收藏，主题与评论各自反规范化。
		"ALTER TABLE topics ADD COLUMN IF NOT EXISTS public_bookmark_count BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE comments ADD COLUMN IF NOT EXISTS public_bookmark_count BIGINT NOT NULL DEFAULT 0",
		// 非负约束：NOT VALID + VALIDATE，避免阻塞式全表校验。
		"ALTER TABLE topics ADD CONSTRAINT topics_public_bookmark_count_nonnegative CHECK (public_bookmark_count >= 0) NOT VALID",
		"ALTER TABLE comments ADD CONSTRAINT comments_public_bookmark_count_nonnegative CHECK (public_bookmark_count >= 0) NOT VALID",
		"ALTER TABLE topics VALIDATE CONSTRAINT topics_public_bookmark_count_nonnegative",
		"ALTER TABLE comments VALIDATE CONSTRAINT comments_public_bookmark_count_nonnegative",
		// 通知类型：用户主动设定的提醒，站内与邮件默认开，web_push 默认关。
		"'bookmark_reminder', 1, 1, 'engagement', TRUE, FALSE",
		"('bookmark_reminder', 'in_app', TRUE, TRUE, TRUE, FALSE)",
		"('bookmark_reminder', 'email', TRUE, TRUE, TRUE, FALSE)",
		"('bookmark_reminder', 'web_push', FALSE, FALSE, TRUE, FALSE)",
		"ON CONFLICT (type) DO NOTHING",
		"ON CONFLICT (type, channel) DO NOTHING",
		"UPDATE notification_policy_revisions SET revision = revision + 1, updated_at = now() WHERE singleton = TRUE",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("content bookmarks migration missing %q", clause)
		}
	}
	for _, forbidden := range []string{
		"users.email", "users.email_lower", "moderation_triggers",
		"topics.ip_address", "comments.ip_address", "posts.raw_content",
	} {
		if strings.Contains(" "+up+" ", forbidden) {
			t.Fatalf("content bookmarks migration exposes %q", forbidden)
		}
	}
	for _, line := range strings.Split(parts[0], "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "GRANT ") {
			t.Fatalf("content bookmarks migration must not grant plugin authority: %q", strings.TrimSpace(line))
		}
	}

	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, clause := range []string{
		"DROP VIEW IF EXISTS sforum_core_v1.forum_bookmarks",
		"ALTER TABLE comments DROP CONSTRAINT IF EXISTS comments_public_bookmark_count_nonnegative",
		"ALTER TABLE topics DROP CONSTRAINT IF EXISTS topics_public_bookmark_count_nonnegative",
		"ALTER TABLE comments DROP COLUMN IF EXISTS public_bookmark_count",
		"ALTER TABLE topics DROP COLUMN IF EXISTS public_bookmark_count",
		"DROP INDEX IF EXISTS content_bookmarks_due_idx",
		"DROP INDEX IF EXISTS content_bookmarks_user_idx",
		"DROP INDEX IF EXISTS content_bookmarks_public_idx",
		"DROP TABLE IF EXISTS content_bookmarks",
		"DELETE FROM notification_preferences WHERE type = 'bookmark_reminder'",
		"DELETE FROM notification_type_policies WHERE type = 'bookmark_reminder'",
		"DELETE FROM notification_type_descriptors WHERE type = 'bookmark_reminder'",
		"UPDATE notification_policy_revisions SET revision = revision + 1, updated_at = now() WHERE singleton = TRUE",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("content bookmarks Down missing %q", clause)
		}
	}
	// Down 必须窄且非级联：只拆自己创建的对象，绝不 CASCADE 或整表删除。
	for _, forbidden := range []string{"CASCADE", "DROP TABLE users", "DROP TABLE topics", "DROP TABLE comments", "TRUNCATE"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("content bookmarks Down contains %q", forbidden)
		}
	}
}
