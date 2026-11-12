package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

const badgesMigration = "202611100002_badges_and_user_activity.sql"

// TestBadgesMigrationDeclaresActivityDomainAndSeeds 固定徽章 + 用户活动切片的
// 结构契约：统计表与日访问记录、徽章定义（分组/档位/授予模式/规则一致性）、
// 授予关系（唯一 + 双索引）、users.top_badge_id 反规范化列、三个稳定读视图、
// 通知类型种子、内置 3 分组 + 12 徽章。Down 必须严格逆序且不误伤其它表。
func TestBadgesMigrationDeclaresActivityDomainAndSeeds(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgesMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("badges migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"SET LOCAL lock_timeout = '5s'",
		"SET LOCAL statement_timeout = '1min'",

		// 统计表：一行一用户，所有计数非负，行懒创建不需要回填。
		"CREATE TABLE IF NOT EXISTS user_activity",
		"user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE",
		"topic_count BIGINT NOT NULL DEFAULT 0 CHECK (topic_count >= 0)",
		"comment_count BIGINT NOT NULL DEFAULT 0 CHECK (comment_count >= 0)",
		"like_received_count BIGINT NOT NULL DEFAULT 0 CHECK (like_received_count >= 0)",
		"like_given_count BIGINT NOT NULL DEFAULT 0 CHECK (like_given_count >= 0)",
		"bookmark_count BIGINT NOT NULL DEFAULT 0 CHECK (bookmark_count >= 0)",
		"days_visited INTEGER NOT NULL DEFAULT 0 CHECK (days_visited >= 0)",
		"posts_read_count BIGINT NOT NULL DEFAULT 0 CHECK (posts_read_count >= 0)",
		"read_time_seconds BIGINT NOT NULL DEFAULT 0 CHECK (read_time_seconds >= 0)",
		"last_visited_on DATE",

		// 日访问记录：复合主键就是幂等键。
		"CREATE TABLE IF NOT EXISTS user_visits",
		"visited_on DATE NOT NULL",
		"PRIMARY KEY (user_id, visited_on)",
		"CREATE INDEX IF NOT EXISTS user_visits_recent_idx ON user_visits (visited_on DESC)",

		// 分组与徽章定义。
		"CREATE TABLE IF NOT EXISTS badge_groups",
		"key TEXT NOT NULL UNIQUE",
		"CREATE TABLE IF NOT EXISTS badges",
		"slug TEXT NOT NULL UNIQUE",
		"group_id BIGINT REFERENCES badge_groups(id) ON DELETE SET NULL",
		"tier TEXT NOT NULL CHECK (tier IN ('bronze','silver','gold'))",
		"icon TEXT NOT NULL DEFAULT 'i-lucide-award'",
		"grant_mode TEXT NOT NULL CHECK (grant_mode IN ('automatic','manual'))",
		"enabled BOOLEAN NOT NULL DEFAULT TRUE",
		// 规则一致性：自动徽章必须 metric + 正数 threshold，手动徽章必须两者皆空。
		"CONSTRAINT badges_rule_check CHECK ( (grant_mode = 'automatic' AND metric IS NOT NULL AND threshold IS NOT NULL AND threshold > 0) OR (grant_mode = 'manual' AND metric IS NULL AND threshold IS NULL) )",
		// 指标白名单必须与 user_activity 的计数列一一对应。
		"CONSTRAINT badges_metric_check CHECK ( metric IS NULL OR metric IN ( 'topic_count','comment_count','like_received_count', 'like_given_count','bookmark_count','days_visited' ) )",
		"CREATE INDEX IF NOT EXISTS badges_enabled_auto_idx ON badges (metric) WHERE enabled AND grant_mode = 'automatic'",

		// 授予关系：一人一徽章唯一；granted_by 只做审计。
		"CREATE TABLE IF NOT EXISTS user_badges",
		"badge_id BIGINT NOT NULL REFERENCES badges(id) ON DELETE CASCADE",
		"granted_by BIGINT REFERENCES users(id) ON DELETE SET NULL",
		"CONSTRAINT user_badges_unique UNIQUE (user_id, badge_id)",
		"CREATE INDEX IF NOT EXISTS user_badges_user_idx ON user_badges (user_id, granted_at DESC)",
		"CREATE INDEX IF NOT EXISTS user_badges_badge_idx ON user_badges (badge_id, granted_at DESC)",

		// 帖子旁只显示最高级 1 个：反规范化列，可空。
		"ALTER TABLE users ADD COLUMN IF NOT EXISTS top_badge_id BIGINT REFERENCES badges(id) ON DELETE SET NULL",

		// 三个稳定读视图：同一套安全视图修饰符。
		"DROP VIEW IF EXISTS sforum_core_v1.forum_badges; CREATE VIEW sforum_core_v1.forum_badges WITH (security_barrier = true, security_invoker = false)",
		"FROM public.badges LEFT JOIN public.badge_groups ON badge_groups.id = badges.group_id WHERE badges.enabled OFFSET 0",
		"COMMENT ON VIEW sforum_core_v1.forum_badges IS",
		"CREATE OR REPLACE VIEW sforum_core_v1.forum_user_badges WITH (security_barrier = true, security_invoker = false)",
		"JOIN public.users ON users.id = user_badges.user_id AND users.status = 'active'",
		"JOIN public.badges ON badges.id = user_badges.badge_id AND badges.enabled OFFSET 0",
		"COMMENT ON VIEW sforum_core_v1.forum_user_badges IS",
		"CREATE OR REPLACE VIEW sforum_core_v1.forum_user_activity WITH (security_barrier = true, security_invoker = false)",
		"JOIN public.users ON users.id = user_activity.user_id AND users.status = 'active' OFFSET 0",
		"COMMENT ON VIEW sforum_core_v1.forum_user_activity IS",

		// 通知类型：站内默认开，邮件与 web_push 默认关。
		"'badge_granted', 1, 1, 'engagement', TRUE, FALSE",
		"('badge_granted', 'in_app', TRUE, TRUE, TRUE, FALSE)",
		"('badge_granted', 'email', FALSE, FALSE, TRUE, FALSE)",
		"('badge_granted', 'web_push', FALSE, FALSE, TRUE, FALSE)",
		"ON CONFLICT (type) DO NOTHING",
		"ON CONFLICT (type, channel) DO NOTHING",
		"UPDATE notification_policy_revisions SET revision = revision + 1, updated_at = now() WHERE singleton = TRUE",

		// 内置默认数据：可重放、可被运营修改。
		"INSERT INTO badge_groups (key, name, description, position)",
		"('activity', '活跃', '持续回到社区', 0)",
		"('content', '内容', '发帖、讨论与被认可', 1)",
		"('honor', '荣誉', '由运营手动授予', 2)",
		"ON CONFLICT (key) DO NOTHING",
		"INSERT INTO badges (slug, name, description, group_id, tier, icon, grant_mode, metric, threshold, position)",
		"ON CONFLICT (slug) DO NOTHING",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("badges migration missing %q", clause)
		}
	}

	// 12 个内置徽章逐个固定 slug + 中文名，避免改名时静默漂移。
	for _, seed := range []string{
		"'regular-visitor', '常客'",
		"'resident', '老住户'",
		"'veteran', '元老'",
		"'first-topic', '初次发帖'",
		"'topic-author', '主题作者'",
		"'discussant', '讨论者'",
		"'rising-star', '小有名气'",
		"'well-received', '广受好评'",
		"'widely-acclaimed', '众望所归'",
		"'collector', '收藏家'",
		"'community-contribution', '社区贡献'",
		"'quality-answer', '优质解答'",
	} {
		if !strings.Contains(up, seed) {
			t.Fatalf("badges migration missing seed %q", seed)
		}
	}
	// 自动徽章的 metric/threshold 组合必须与计划表逐条一致。
	for _, rule := range []string{
		"'automatic', 'days_visited', 7, 0)",
		"'automatic', 'days_visited', 30, 1)",
		"'automatic', 'days_visited', 180, 2)",
		"'automatic', 'topic_count', 1, 0)",
		"'automatic', 'topic_count', 10, 1)",
		"'automatic', 'comment_count', 25, 2)",
		"'automatic', 'like_received_count', 10, 3)",
		"'automatic', 'like_received_count', 100, 4)",
		"'automatic', 'like_received_count', 500, 5)",
		"'automatic', 'bookmark_count', 10, 6)",
		"'manual', NULL, NULL, 0)",
		"'manual', NULL, NULL, 1)",
	} {
		if !strings.Contains(up, rule) {
			t.Fatalf("badges migration missing rule %q", rule)
		}
	}
	// 图标可辨识性是准入条件（2026-11-10 设计评审，12px 判据）：这 7 个是改版后
	// 定稿值；被淘汰的 crown / pen-tool 一类编码等级或轮廓过碎的图标不得回流。
	for _, icon := range []string{
		"'i-lucide-calendar'", "'i-lucide-hourglass'", "'i-lucide-sprout'",
		"'i-lucide-square-pen'", "'i-lucide-message-circle'",
		"'i-lucide-thumbs-up'", "'i-lucide-users'",
	} {
		if !strings.Contains(up, icon) {
			t.Fatalf("badges migration missing approved icon %q", icon)
		}
	}
	for _, icon := range []string{
		"'i-lucide-calendar-check'", "'i-lucide-crown'", "'i-lucide-pen-line'",
		"'i-lucide-pen-tool'", "'i-lucide-messages-square'",
		"'i-lucide-sparkles'", "'i-lucide-heart-handshake'", "'i-lucide-users-round'",
	} {
		if strings.Contains(up, icon) {
			t.Fatalf("badges migration still uses retired icon %q", icon)
		}
	}

	// 12 个徽章都必须挂到某个分组上（不允许 group_id 留空）。
	if got := strings.Count(up, "(SELECT id FROM badge_groups WHERE key = "); got != 12 {
		t.Fatalf("expected 12 badge seeds to resolve a badge group, got %d", got)
	}

	// 徽章与统计口径只认 users.status / badges.enabled，绝不把内容可见性规则
	// 复制进这个数据域（那是 forum_topics / forum_comments 的职责）。
	for _, forbidden := range []string{
		"topics.status", "topics.deleted_at", "categories.visibility",
		"comments.status", "comments.deleted_at",
	} {
		if strings.Contains(" "+up+" ", forbidden) {
			t.Fatalf("badges migration must not re-declare content visibility %q", forbidden)
		}
	}
	// 稳定视图列清单里不允许出现隐私列（granted_by 只在基表上）。
	for _, forbidden := range []string{
		"users.email", "users.email_lower", "users.current_token_version",
		"moderation_triggers", "posts.raw_content", "ip_address",
	} {
		if strings.Contains(" "+up+" ", forbidden) {
			t.Fatalf("badges migration exposes %q", forbidden)
		}
	}
	for _, line := range strings.Split(parts[0], "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "GRANT ") {
			t.Fatalf("badges migration must not grant plugin authority: %q", strings.TrimSpace(line))
		}
	}
	// 徽章目录必须能让插件只靠稳定数据面重建「目录 + 获得条件」：
	// grant_mode / metric / threshold 三个规则列要在视图列清单里。
	// 它们与「未采集的阅读指标」不是一类东西——后者仍然禁止输出。
	badgesView := up[strings.Index(up, "CREATE VIEW sforum_core_v1.forum_badges"):]
	badgesView = badgesView[:strings.Index(badgesView, "OFFSET 0")]
	for _, column := range []string{"badges.grant_mode", "badges.metric", "badges.threshold"} {
		if !strings.Contains(badgesView, column) {
			t.Fatalf("forum_badges view must expose %q so plugins can rebuild earn conditions", column)
		}
	}
	for _, forbidden := range []string{"posts_read_count", "read_time_seconds"} {
		if strings.Contains(badgesView, forbidden) {
			t.Fatalf("forum_badges view must not expose uncollected read metric %q", forbidden)
		}
	}

	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, clause := range []string{
		"DROP VIEW IF EXISTS sforum_core_v1.forum_user_activity",
		"DROP VIEW IF EXISTS sforum_core_v1.forum_user_badges",
		"DROP VIEW IF EXISTS sforum_core_v1.forum_badges",
		"ALTER TABLE users DROP COLUMN IF EXISTS top_badge_id",
		"DROP INDEX IF EXISTS user_badges_badge_idx",
		"DROP INDEX IF EXISTS user_badges_user_idx",
		"DROP TABLE IF EXISTS user_badges",
		"DROP INDEX IF EXISTS badges_enabled_auto_idx",
		"DROP TABLE IF EXISTS badges",
		"DROP TABLE IF EXISTS badge_groups",
		"DROP INDEX IF EXISTS user_visits_recent_idx",
		"DROP TABLE IF EXISTS user_visits",
		"DROP TABLE IF EXISTS user_activity",
		"DELETE FROM notification_preferences WHERE type = 'badge_granted'",
		"DELETE FROM notification_type_policies WHERE type = 'badge_granted'",
		"DELETE FROM notification_type_descriptors WHERE type = 'badge_granted'",
		"UPDATE notification_policy_revisions SET revision = revision + 1, updated_at = now() WHERE singleton = TRUE",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("badges Down missing %q", clause)
		}
	}
	// Down 必须严格逆序：视图 → users 列 → 关系表 → 定义表 → 分组表 → 访问记录 → 统计表。
	for _, ordered := range [][2]string{
		{"DROP VIEW IF EXISTS sforum_core_v1.forum_badges", "ALTER TABLE users DROP COLUMN IF EXISTS top_badge_id"},
		{"ALTER TABLE users DROP COLUMN IF EXISTS top_badge_id", "DROP TABLE IF EXISTS badges"},
		{"DROP TABLE IF EXISTS user_badges", "DROP TABLE IF EXISTS badges"},
		{"DROP TABLE IF EXISTS badges", "DROP TABLE IF EXISTS badge_groups"},
		{"DROP TABLE IF EXISTS user_visits", "DROP TABLE IF EXISTS user_activity"},
		{"DROP TABLE IF EXISTS user_activity", "DELETE FROM notification_type_policies WHERE type = 'badge_granted'"},
		// 策略引用描述符（ON DELETE RESTRICT），必须先删策略再删描述符。
		{"DELETE FROM notification_type_policies WHERE type = 'badge_granted'", "DELETE FROM notification_type_descriptors WHERE type = 'badge_granted'"},
	} {
		first := strings.Index(down, ordered[0])
		second := strings.Index(down, ordered[1])
		if first < 0 || second < 0 || first > second {
			t.Fatalf("badges Down ordering violated: %q must precede %q", ordered[0], ordered[1])
		}
	}
	// Down 必须窄且非级联：只拆自己创建的对象。
	for _, forbidden := range []string{"CASCADE", "DROP TABLE users", "TRUNCATE"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("badges Down contains %q", forbidden)
		}
	}
}
