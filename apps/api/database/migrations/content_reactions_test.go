package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

const contentReactionsMigration = "202610280001_content_reactions.sql"

// TestContentReactionsMigrationDeclaresRelationCountsAndSeeds 固定点赞切片的
// 结构契约：关系行（唯一 + 目标正数 + 单类型）、反规范化计数列、稳定读视图、
// 通知类型种子。Down 必须对称拆除且不误伤其它表。
func TestContentReactionsMigrationDeclaresRelationCountsAndSeeds(t *testing.T) {
	body, err := fs.ReadFile(Files(), contentReactionsMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("content reactions migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"SET LOCAL lock_timeout = '5s'",
		"SET LOCAL statement_timeout = '1min'",
		// 关系行：用户删除 CASCADE；目标不加外键（多态，内容软删）。
		"CREATE TABLE IF NOT EXISTS content_reactions",
		"user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE",
		"target_type TEXT NOT NULL",
		"target_id BIGINT NOT NULL",
		"reaction TEXT NOT NULL DEFAULT 'like'",
		"CONSTRAINT content_reactions_target_type_check CHECK (target_type IN ('topic', 'comment'))",
		"CONSTRAINT content_reactions_reaction_check CHECK (reaction IN ('like'))",
		"CONSTRAINT content_reactions_target_positive CHECK (target_id > 0)",
		"CONSTRAINT content_reactions_unique UNIQUE (user_id, target_type, target_id, reaction)",
		// 名单读路径索引（最近点赞者）与按用户回填索引。
		"CREATE INDEX IF NOT EXISTS content_reactions_roster_idx ON content_reactions (target_type, target_id, reaction, created_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS content_reactions_user_idx ON content_reactions (user_id, created_at DESC)",
		// 计数列：主题与评论各自反规范化。
		"ALTER TABLE topics ADD COLUMN IF NOT EXISTS like_count BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE comments ADD COLUMN IF NOT EXISTS like_count BIGINT NOT NULL DEFAULT 0",
		// 非负约束：NOT VALID + VALIDATE，避免阻塞式全表校验。
		"ALTER TABLE topics ADD CONSTRAINT topics_like_count_nonnegative CHECK (like_count >= 0) NOT VALID",
		"ALTER TABLE comments ADD CONSTRAINT comments_like_count_nonnegative CHECK (like_count >= 0) NOT VALID",
		"ALTER TABLE topics VALIDATE CONSTRAINT topics_like_count_nonnegative",
		"ALTER TABLE comments VALIDATE CONSTRAINT comments_like_count_nonnegative",
		// 通知类型：站内默认开，邮件/推送默认关，推荐值同默认值。
		"'reaction', 1, 1, 'engagement', TRUE, FALSE",
		"('reaction', 'in_app', TRUE, TRUE, TRUE, FALSE)",
		"('reaction', 'email', FALSE, FALSE, TRUE, FALSE)",
		"('reaction', 'web_push', FALSE, FALSE, TRUE, FALSE)",
		"ON CONFLICT (type) DO NOTHING",
		"ON CONFLICT (type, channel) DO NOTHING",
		"UPDATE notification_policy_revisions SET revision = revision + 1, updated_at = now() WHERE singleton = TRUE",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("content reactions migration missing %q", clause)
		}
	}
	for _, forbidden := range []string{
		"users.email", "users.email_lower", "moderation_triggers",
		"topics.ip_address", "comments.ip_address", "posts.raw_content",
	} {
		if strings.Contains(" "+up+" ", forbidden) {
			t.Fatalf("content reactions migration exposes %q", forbidden)
		}
	}
	for _, line := range strings.Split(parts[0], "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "GRANT ") {
			t.Fatalf("content reactions migration must not grant plugin authority: %q", strings.TrimSpace(line))
		}
	}

	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, clause := range []string{
		"DROP VIEW IF EXISTS sforum_core_v1.forum_reactions",
		"ALTER TABLE comments DROP CONSTRAINT IF EXISTS comments_like_count_nonnegative",
		"ALTER TABLE topics DROP CONSTRAINT IF EXISTS topics_like_count_nonnegative",
		"ALTER TABLE comments DROP COLUMN IF EXISTS like_count",
		"ALTER TABLE topics DROP COLUMN IF EXISTS like_count",
		"DROP INDEX IF EXISTS content_reactions_user_idx",
		"DROP INDEX IF EXISTS content_reactions_roster_idx",
		"DROP TABLE IF EXISTS content_reactions",
		"DELETE FROM notification_preferences WHERE type = 'reaction'",
		"DELETE FROM notification_type_policies WHERE type = 'reaction'",
		"DELETE FROM notification_type_descriptors WHERE type = 'reaction'",
		"UPDATE notification_policy_revisions SET revision = revision + 1, updated_at = now() WHERE singleton = TRUE",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("content reactions Down missing %q", clause)
		}
	}
	// Down 必须窄且非级联：只拆自己创建的对象，绝不 CASCADE 或整表删除。
	for _, forbidden := range []string{"CASCADE", "DROP TABLE users", "DROP TABLE topics", "DROP TABLE comments", "TRUNCATE"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("content reactions Down contains %q", forbidden)
		}
	}
}
