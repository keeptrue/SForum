package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

const stableCoreViewsMigration = "202607140016_stable_core_views.sql"
const stableCoreViewsE10Migration = "202610180001_stable_core_views_e10.sql"

func TestFilesIncludesStableCoreViewsMigration(t *testing.T) {
	entries, err := fs.ReadDir(Files(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == stableCoreViewsMigration {
			return
		}
	}
	t.Fatalf("expected embedded migration %s", stableCoreViewsMigration)
}

func TestStableCoreViewsMigrationOwnsFilteredReadOnlyContracts(t *testing.T) {
	body, err := fs.ReadFile(Files(), stableCoreViewsMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("stable core views migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"CREATE SCHEMA sforum_core_v1 AUTHORIZATION CURRENT_USER",
		"REVOKE ALL ON SCHEMA sforum_core_v1 FROM PUBLIC",
		"CREATE VIEW sforum_core_v1.safe_users WITH (security_barrier = true, security_invoker = false)",
		"CREATE VIEW sforum_core_v1.forum_topics WITH (security_barrier = true, security_invoker = false)",
		"CREATE VIEW sforum_core_v1.forum_comments WITH (security_barrier = true, security_invoker = false)",
		"CREATE VIEW sforum_core_v1.public_entity_meta WITH (security_barrier = true, security_invoker = false)",
		"CREATE VIEW sforum_core_v1.public_attachment_metadata WITH (security_barrier = true, security_invoker = false)",
		"WHERE users.status = 'active' OFFSET 0",
		"WHERE topics.status IN ('active', 'locked')",
		"AND categories.visibility = 'public' OFFSET 0",
		"WHERE comments.status = 'active'",
		"AND definitions.visibility = 'public' OFFSET 0",
		"AND attachments.visibility = 'public'",
		"REVOKE ALL ON ALL TABLES IN SCHEMA sforum_core_v1 FROM PUBLIC",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("stable core views migration missing %q", clause)
		}
	}
	for _, forbidden := range []string{
		"users.email", "users.email_lower", "users.current_token_version",
		"posts.raw_content", "topics.ip_address", "topics.last_edit_ip",
		"comments.ip_address", "comments.last_edit_ip", "moderation_triggers",
		"attachments.provider", "attachments.object_key", "updated_by_user_id",
	} {
		if strings.Contains(" "+up+" ", forbidden) {
			t.Fatalf("stable core views migration exposes or grants %q", forbidden)
		}
	}
	for _, line := range strings.Split(parts[0], "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "GRANT ") {
			t.Fatalf("stable core views migration must not grant plugin authority: %q", strings.TrimSpace(line))
		}
	}
}

func TestStableCoreViewsE10MigrationOwnsFilteredReadOnlyContracts(t *testing.T) {
	body, err := fs.ReadFile(Files(), stableCoreViewsE10Migration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("stable core views E10 migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"CREATE VIEW sforum_core_v1.site_stats WITH (security_barrier = true, security_invoker = false)",
		"CREATE VIEW sforum_core_v1.public_categories WITH (security_barrier = true, security_invoker = false)",
		"CREATE VIEW sforum_core_v1.safe_user_avatars WITH (security_barrier = true, security_invoker = false)",
		"WHERE users.status = 'active' OFFSET 0",
		"AND category_groups.visibility = 'public' OFFSET 0",
		"AND attachments.visibility = 'public'",
		"AND attachments.deleted_at IS NULL",
		"REVOKE ALL ON ALL TABLES IN SCHEMA sforum_core_v1 FROM PUBLIC",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("stable core views E10 migration missing %q", clause)
		}
	}
	for _, forbidden := range []string{
		"users.email", "users.email_lower", "users.current_token_version",
		"posts.raw_content", "topics.ip_address", "topics.last_edit_ip",
		"comments.ip_address", "comments.last_edit_ip", "moderation_triggers",
		"attachments.provider", "attachments.object_key", "updated_by_user_id",
		"GRANT ",
	} {
		if strings.Contains(" "+up+" ", forbidden) {
			t.Fatalf("stable core views E10 migration exposes or grants %q", forbidden)
		}
	}
}

func TestStableCoreViewsMigrationDownIsNarrowAndNonCascading(t *testing.T) {
	body, err := fs.ReadFile(Files(), stableCoreViewsMigration)
	if err != nil {
		t.Fatal(err)
	}
	down := strings.Join(strings.Fields(strings.SplitN(string(body), "-- +goose Down", 2)[1]), " ")
	for _, view := range []string{
		"public_attachment_metadata", "public_entity_meta", "forum_comments", "forum_topics", "safe_users",
	} {
		if !strings.Contains(down, "DROP VIEW IF EXISTS sforum_core_v1."+view) {
			t.Fatalf("stable core views Down does not drop %s", view)
		}
	}
	if !strings.Contains(down, "DROP SCHEMA IF EXISTS sforum_core_v1") {
		t.Fatal("stable core views Down does not drop its schema")
	}
	for _, forbidden := range []string{"CASCADE", "DROP TABLE", "DROP ROLE", "DELETE FROM", "TRUNCATE"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("stable core views Down contains %q", forbidden)
		}
	}
}

const stableCoreViewsReactionsMigration = "202610280001_content_reactions.sql"

// TestStableCoreViewsReactionsMigrationMirrorsForumVisibilityPredicates 把点赞
// 关系面纳入稳定视图清单，并逐字核对它与 forum_topics / forum_comments 共用同一套
// 可见性谓词——插件读面不允许出现第二套「什么算公开」的规则。
//
// 已知取舍（不要为它改视图）：forum_reactions 视图没有 topic_id 列，因此评论点赞
// 无法在视图层按主题聚合；需要主题维度的调用方必须先经 forum_comments 解析
// topic_id，或按 target_id 精确读取。视图刻意只暴露关系行本身，避免为聚合再复制
// 一份主题可见性判断。
func TestStableCoreViewsReactionsMigrationMirrorsForumVisibilityPredicates(t *testing.T) {
	baseBody, err := fs.ReadFile(Files(), stableCoreViewsMigration)
	if err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(Files(), stableCoreViewsReactionsMigration)
	if err != nil {
		t.Fatal(err)
	}
	baseUp := strings.Join(strings.Fields(strings.SplitN(string(baseBody), "-- +goose Down", 2)[0]), " ")
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("content reactions migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"CREATE OR REPLACE VIEW sforum_core_v1.forum_reactions WITH (security_barrier = true, security_invoker = false)",
		"FROM public.content_reactions AS reactions",
		"JOIN public.users ON users.id = reactions.user_id",
		"WHERE users.status = 'active'",
		"OFFSET 0",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("forum_reactions view missing %q", clause)
		}
	}
	// 与 forum_topics / forum_comments 逐字一致：基础视图里必须有同一段谓词，
	// 点赞视图里也必须原样重复，两边都不允许单独漂移。
	for _, predicate := range []string{
		"topics.status IN ('active', 'locked')",
		"topics.deleted_at IS NULL",
		"categories.visibility = 'public'",
		"comments.status = 'active'",
		"comments.deleted_at IS NULL",
	} {
		if !strings.Contains(baseUp, predicate) {
			t.Fatalf("base stable core views no longer declare %q", predicate)
		}
		if !strings.Contains(up, predicate) {
			t.Fatalf("forum_reactions view does not mirror %q", predicate)
		}
	}
	for _, forbidden := range []string{
		"users.email", "users.email_lower", "users.current_token_version",
		"moderation_triggers", "ip_address", "last_edit_ip", "posts.raw_content",
	} {
		if strings.Contains(" "+up+" ", forbidden) {
			t.Fatalf("forum_reactions view exposes %q", forbidden)
		}
	}
	down := strings.Join(strings.Fields(parts[1]), " ")
	if !strings.Contains(down, "DROP VIEW IF EXISTS sforum_core_v1.forum_reactions") {
		t.Fatal("content reactions Down does not drop forum_reactions")
	}
}

const stableCoreViewsBookmarksMigration = "202611100001_content_bookmarks.sql"

// TestStableCoreViewsBookmarksMigrationMirrorsForumVisibilityPredicates 把收藏
// 关系面纳入稳定视图清单，并逐字核对它与 forum_topics / forum_comments 共用同一套
// 可见性谓词——插件读面不允许出现第二套「什么算公开」的规则。
//
// 收藏视图比点赞视图多两条硬约束：只输出 visibility='public' 的行（私密收藏是
// 行主私有数据），且永不输出 remind_at / reminded_at（提醒是私有计划数据）。
func TestStableCoreViewsBookmarksMigrationMirrorsForumVisibilityPredicates(t *testing.T) {
	baseBody, err := fs.ReadFile(Files(), stableCoreViewsMigration)
	if err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(Files(), stableCoreViewsBookmarksMigration)
	if err != nil {
		t.Fatal(err)
	}
	baseUp := strings.Join(strings.Fields(strings.SplitN(string(baseBody), "-- +goose Down", 2)[0]), " ")
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("content bookmarks migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"CREATE OR REPLACE VIEW sforum_core_v1.forum_bookmarks WITH (security_barrier = true, security_invoker = false)",
		"FROM public.content_bookmarks AS bookmarks",
		"JOIN public.users ON users.id = bookmarks.user_id",
		"WHERE users.status = 'active'",
		"AND bookmarks.visibility = 'public'",
		"OFFSET 0",
		"COMMENT ON VIEW sforum_core_v1.forum_bookmarks IS",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("forum_bookmarks view missing %q", clause)
		}
	}
	// 与 forum_topics / forum_comments 逐字一致：基础视图里必须有同一段谓词，
	// 收藏视图里也必须原样重复，两边都不允许单独漂移。
	for _, predicate := range []string{
		"topics.status IN ('active', 'locked')",
		"topics.deleted_at IS NULL",
		"categories.visibility = 'public'",
		"comments.status = 'active'",
		"comments.deleted_at IS NULL",
	} {
		if !strings.Contains(baseUp, predicate) {
			t.Fatalf("base stable core views no longer declare %q", predicate)
		}
		if !strings.Contains(up, predicate) {
			t.Fatalf("forum_bookmarks view does not mirror %q", predicate)
		}
	}
	for _, forbidden := range []string{
		"users.email", "users.email_lower", "users.current_token_version",
		"moderation_triggers", "ip_address", "last_edit_ip", "posts.raw_content",
	} {
		if strings.Contains(" "+up+" ", forbidden) {
			t.Fatalf("forum_bookmarks view exposes %q", forbidden)
		}
	}
	// 视图列清单里不允许出现提醒字段（它们是行主私有计划数据）。
	viewBody := up[strings.Index(up, "CREATE OR REPLACE VIEW sforum_core_v1.forum_bookmarks"):]
	viewBody = viewBody[:strings.Index(viewBody, "OFFSET 0")]
	for _, forbidden := range []string{"remind_at", "reminded_at"} {
		if strings.Contains(viewBody, forbidden) {
			t.Fatalf("forum_bookmarks view must not output %q", forbidden)
		}
	}
	down := strings.Join(strings.Fields(parts[1]), " ")
	if !strings.Contains(down, "DROP VIEW IF EXISTS sforum_core_v1.forum_bookmarks") {
		t.Fatal("content bookmarks Down does not drop forum_bookmarks")
	}
}

const stableCoreViewsBadgesMigration = "202611100002_badges_and_user_activity.sql"

// TestStableCoreViewsBadgesMigrationFiltersByUserStatusAndBadgeEnabled 把徽章与
// 用户活动纳入稳定视图清单。
//
// 与点赞/收藏不同，这个数据域刻意不参与内容可见性：徽章是用户身份，统计是用户
// 维度计数，只按 users.status = 'active' 与 badges.enabled 过滤。因此这里除了核对
// 安全视图修饰符，还要反向断言视图里没有出现 forum_topics / forum_comments 那套
// 「什么算公开」的谓词——插件读面不允许出现第二套可见性规则。
//
// 已知取舍（不要为它改视图）：阅读类指标（posts_read_count / read_time_seconds）
// 在 v1 不采集，视图不输出它们；输出 0 会让调用方误以为是真实读数。
func TestStableCoreViewsBadgesMigrationFiltersByUserStatusAndBadgeEnabled(t *testing.T) {
	body, err := fs.ReadFile(Files(), stableCoreViewsBadgesMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("badges migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")

	views := map[string][]string{
		"forum_badges": {
			// 规则列是插件重建「获得条件」的唯一来源；阅读指标仍然不输出。
			"badges.grant_mode, badges.metric, badges.threshold",
			"WHERE badges.enabled OFFSET 0",
		},
		"forum_user_badges": {
			"AND users.status = 'active'",
			"AND badges.enabled OFFSET 0",
		},
		"forum_user_activity": {
			"AND users.status = 'active' OFFSET 0",
		},
	}
	for view, clauses := range views {
		// forum_badges 用 DROP + CREATE（后续迁移会加宽它，OR REPLACE 重放会 42P16），
		// 其余视图仍是 OR REPLACE。断言两种声明形态都接受，但都必须带 security 选项。
		marker := "CREATE OR REPLACE VIEW sforum_core_v1." + view
		if !strings.Contains(up, marker) {
			marker = "CREATE VIEW sforum_core_v1." + view
		}
		if !strings.Contains(up, marker+" WITH (security_barrier = true, security_invoker = false) AS") {
			t.Fatalf("stable core view %s is not declared with security_barrier/security_invoker", view)
		}
		// 每个视图都必须带 COMMENT，说明它为什么这么过滤。
		if !strings.Contains(up, "COMMENT ON VIEW sforum_core_v1."+view+" IS") {
			t.Fatalf("stable core view %s has no COMMENT ON VIEW", view)
		}
		start := strings.Index(up, marker)
		rest := up[start:]
		end := strings.Index(rest, "OFFSET 0")
		if end < 0 {
			t.Fatalf("stable core view %s has no OFFSET 0", view)
		}
		// 切片包含 OFFSET 0 本身，便于逐字核对谓词与 OFFSET 的相对位置。
		viewBody := rest[:end+len("OFFSET 0")]
		for _, clause := range clauses {
			if !strings.Contains(viewBody, clause) {
				t.Fatalf("stable core view %s missing %q", view, clause)
			}
		}
		// 列清单里不允许出现隐私列与未采集的阅读指标。
		for _, forbidden := range []string{
			"email", "current_token_version", "granted_by", "grant_reason",
			"posts_read_count", "read_time_seconds", "ip_address",
		} {
			if strings.Contains(viewBody, forbidden) {
				t.Fatalf("stable core view %s must not output %q", view, forbidden)
			}
		}
		// 徽章/统计不参与内容可见性：不允许把主题/分类/评论谓词搬进来。
		for _, forbidden := range []string{
			"topics.status", "topics.deleted_at", "categories.visibility",
			"comments.status", "comments.deleted_at",
		} {
			if strings.Contains(viewBody, forbidden) {
				t.Fatalf("stable core view %s must not re-declare content visibility %q", view, forbidden)
			}
		}
	}

	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, view := range []string{"forum_user_activity", "forum_user_badges", "forum_badges"} {
		if !strings.Contains(down, "DROP VIEW IF EXISTS sforum_core_v1."+view) {
			t.Fatalf("badges Down does not drop %s", view)
		}
	}
}
