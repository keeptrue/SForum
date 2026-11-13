package forum

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 本文件钉住「论坛读模型的徽章外观来自 badge_tiers 行」这条真源。
//
// 事故形态（图1）：`GET /api/v1/topics/{id}/comments` 返回
// `topBadge.tierColor=""` / `tierName:{zh:"",en:""}`，前端于是按 tier key 回落
// 内置 amber → #92400e（棕），而运营在后台给 gold 配的是 #fac400。
// 根因是读 SQL 只投影了徽章四列、没有 JOIN badge_tiers。
//
// 为什么必须是真库测试：列顺序/列数错位在位置扫描里不会报错，只会把值读进
// 前面的字段；只有真正跑一遍 SELECT 才能证明「七列 → 七个目的地」对得上。
type badgeAppearanceGraph struct {
	UserID     int64
	CategoryID int64
	GroupID    int64
	PostID     int64
	TopicID    int64
	CommentID  int64
	BadgeID    int64
}

const (
	// 运营在后台把 gold 改成的识别色（不是迁移种子里的 'amber'）。
	badgeAppearanceTierColor = "#fac400"
	badgeAppearanceTierZh    = "金牌"
	badgeAppearanceTierEn    = "Gold"
)

func newBadgeAppearanceGraph(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) badgeAppearanceGraph {
	t.Helper()
	graph := badgeAppearanceGraph{}

	// gold 档位由迁移种下（color='amber'）；这里改成运营实际配置的 #fac400，
	// 证明读路径取的是**表里的值**而不是种子或前端映射表。测试结束恢复原值，
	// 免得同包其它测试读到被改过的种子。
	var previousColor string
	if err := pool.QueryRow(ctx, `SELECT color FROM badge_tiers WHERE key = 'gold'`).Scan(&previousColor); err != nil {
		t.Fatalf("load gold tier: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE badge_tiers SET name_zh = $1, name_en = $2, color = $3, enabled = TRUE WHERE key = 'gold'
	`, badgeAppearanceTierZh, badgeAppearanceTierEn, badgeAppearanceTierColor); err != nil {
		t.Fatalf("configure gold tier: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			UPDATE badge_tiers SET color = $1, enabled = TRUE WHERE key = 'gold'
		`, previousColor)
	})

	username := "badge_appearance_" + suffix
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1, $1, $2, $2, $1, 'active') RETURNING id
	`, username, username+"@example.test").Scan(&graph.UserID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// 手动徽章：badges_rule_check 要求 metric/threshold 同时为空。
	if err := pool.QueryRow(ctx, `
		INSERT INTO badges (slug, name, description, tier, icon, grant_mode, enabled, position)
		VALUES ($1, $2, $3, 'gold', 'i-lucide-crown', 'manual', TRUE, 0) RETURNING id
	`, "appearance-"+suffix, "外观测试徽章", "读路径必须带出等级外观").Scan(&graph.BadgeID); err != nil {
		t.Fatalf("insert badge: %v", err)
	}
	// top_badge_id 是读 SQL 的唯一入口（badge_tiers 经 badges.tier 关联）。
	if _, err := pool.Exec(ctx, `UPDATE users SET top_badge_id = $2 WHERE id = $1`, graph.UserID, graph.BadgeID); err != nil {
		t.Fatalf("wear badge: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO category_groups (slug, name) VALUES ($1, $2) RETURNING id
	`, "badge-appearance-group-"+suffix, "Badge Appearance Group "+suffix).Scan(&graph.GroupID); err != nil {
		t.Fatalf("insert category group: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO categories (group_id, slug, name) VALUES ($1, $2, $3) RETURNING id
	`, graph.GroupID, "badge-appearance-"+suffix, "Badge Appearance "+suffix).Scan(&graph.CategoryID); err != nil {
		t.Fatalf("insert category: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO posts (raw_content, html_content, plain_text, content_hash)
		VALUES ('body', '<p>body</p>', 'body', $1) RETURNING id
	`, "badge-appearance-hash-"+suffix).Scan(&graph.PostID); err != nil {
		t.Fatalf("insert post: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO topics (category_id, author_user_id, content_id, title, slug)
		VALUES ($1, $2, $3, $4, $5) RETURNING id
	`, graph.CategoryID, graph.UserID, graph.PostID, "Badge Appearance Topic "+suffix, "badge-appearance-topic-"+suffix).Scan(&graph.TopicID); err != nil {
		t.Fatalf("insert topic: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO comments (topic_id, content_id, author_user_id, path_key, depth)
		VALUES ($1, $2, $3, '000001', 0) RETURNING id
	`, graph.TopicID, graph.PostID, graph.UserID).Scan(&graph.CommentID); err != nil {
		t.Fatalf("insert comment: %v", err)
	}

	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM comments WHERE id = $1`, graph.CommentID)
		_, _ = pool.Exec(cleanup, `DELETE FROM topics WHERE id = $1`, graph.TopicID)
		_, _ = pool.Exec(cleanup, `DELETE FROM posts WHERE id = $1`, graph.PostID)
		_, _ = pool.Exec(cleanup, `DELETE FROM categories WHERE id = $1`, graph.CategoryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM category_groups WHERE id = $1`, graph.GroupID)
		_, _ = pool.Exec(cleanup, `UPDATE users SET top_badge_id = NULL WHERE id = $1`, graph.UserID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, graph.UserID)
		_, _ = pool.Exec(cleanup, `DELETE FROM badges WHERE id = $1`, graph.BadgeID)
	})
	return graph
}

// assertBadgeAppearance 是两条读路径共用的判据：外观必须来自 badge_tiers 行。
func assertBadgeAppearance(t *testing.T, label string, ref *BadgeRef) {
	t.Helper()
	if ref == nil {
		t.Fatalf("%s: topBadge 不得为 nil（作者佩戴了启用中的徽章）", label)
	}
	if ref.Slug == "" || ref.Name != "外观测试徽章" {
		t.Fatalf("%s: topBadge = %+v, want slug + 名称", label, ref)
	}
	if ref.Tier != "gold" {
		t.Fatalf("%s: tier = %q, want gold", label, ref.Tier)
	}
	if ref.TierColor != badgeAppearanceTierColor {
		t.Fatalf("%s: tierColor = %q, want %q（外观必须来自 badge_tiers，不是前端映射）", label, ref.TierColor, badgeAppearanceTierColor)
	}
	if ref.TierName.Zh != badgeAppearanceTierZh || ref.TierName.En != badgeAppearanceTierEn {
		t.Fatalf("%s: tierName = %+v, want {%s %s}", label, ref.TierName, badgeAppearanceTierZh, badgeAppearanceTierEn)
	}
}

// TestPostgresCommentReadCarriesTierAppearance 是图1 的回归测试：
// 评论读模型必须带出等级双语名与识别色。
func TestPostgresCommentReadCarriesTierAppearance(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	graph := newBadgeAppearanceGraph(t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()))

	store := NewPostgresStore(pool)
	list, err := store.ListComments(ctx, CommentListInput{TopicID: graph.TopicID, View: "flat", Page: 1, PerPage: 20})
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("comments = %d, want 1", len(list.Items))
	}
	comment := list.Items[0]
	if comment.ID != graph.CommentID {
		t.Fatalf("comment id = %d, want %d", comment.ID, graph.CommentID)
	}
	assertBadgeAppearance(t, "comment read model", comment.TopBadge)
}

// TestPostgresTopicDetailCarriesTierAppearance 覆盖另一条独立扫描器
// （topicDetailSQL + scanTopicDetailWithAvatar）：两条路径各自手写目的地，
// 只测一条会让另一条在下次加列时静默错位。
func TestPostgresTopicDetailCarriesTierAppearance(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	graph := newBadgeAppearanceGraph(t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()))

	store := NewPostgresStore(pool)
	detail, err := store.GetTopic(ctx, graph.TopicID)
	if err != nil {
		t.Fatalf("get topic: %v", err)
	}
	// 详情把徽章放在摘要层（TopicDetail 内嵌 TopicSummary）：那一枚就是作者的最高级徽章，
	// 帖子旁展示的也是它。UserSummary.TopBadge 只服务于「最近回复作者」这类旁位。
	assertBadgeAppearance(t, "topic detail read model", detail.TopBadge)
}

// addBadgeLessComment 在同一主题下再加一条「作者没有任何徽章」的评论。
//
// 存在的唯一目的：证明 topBadgeJoinSQL 的可见性过滤只把徽章列置 NULL，不会把行丢掉。
// 改成 INNER JOIN 之后评论列表会少行，而只看「有徽章作者」的用例完全看不出这种回归。
func addBadgeLessComment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, graph badgeAppearanceGraph, suffix string) (userID, commentID int64) {
	t.Helper()
	username := "badge_appearance_plain_" + suffix
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1, $1, $2, $2, $1, 'active') RETURNING id
	`, username, username+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("insert badge-less user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO comments (topic_id, content_id, author_user_id, path_key, depth)
		VALUES ($1, $2, $3, '000002', 0) RETURNING id
	`, graph.TopicID, graph.PostID, userID).Scan(&commentID); err != nil {
		t.Fatalf("insert badge-less comment: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM comments WHERE id = $1`, commentID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID, commentID
}

// TestPostgresReadHidesBadgeWhenTierIsDisabled 是「停档 = 这一档不存在」的回归测试。
//
// 场景是真实窗口：运营停用 gold 档，重算还没跑，`users.top_badge_id` 仍指向那枚徽章。
// 读路径必须已经把它判为不可展示——否则 chip 挂着 gold、佩戴面板说「已回落到 silver」、
// 通知目标判不可见，三个面互相打脸。
//
// 两件事一起钉：
//  1. 停档后 topBadge 为 nil（评论读模型 + 主题详情两条扫描器）；
//  2. 行数不变（同一主题里还有一条零徽章作者的评论）——过滤只许发生在徽章列上，
//     不许把行丢掉（INNER JOIN 回归）。
func TestPostgresReadHidesBadgeWhenTierIsDisabled(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	graph := newBadgeAppearanceGraph(t, ctx, pool, suffix)
	_, plainCommentID := addBadgeLessComment(t, ctx, pool, graph, suffix)

	store := NewPostgresStore(pool)
	input := CommentListInput{TopicID: graph.TopicID, View: "flat", Page: 1, PerPage: 20}

	// 反证起点：停档前两条评论都在，且作者那枚徽章确实带外观（否则下面的 nil 无法归因）。
	before, err := store.ListComments(ctx, input)
	if err != nil {
		t.Fatalf("list comments before: %v", err)
	}
	if len(before.Items) != 2 {
		t.Fatalf("comments before = %d, want 2", len(before.Items))
	}
	assertBadgeAppearance(t, "comment before tier disabled", commentByID(t, before.Items, graph.CommentID).TopBadge)

	// 停档：徽章本身仍启用，users.top_badge_id 仍指向它（模拟重算还没跑）。
	if _, err := pool.Exec(ctx, `UPDATE badge_tiers SET enabled = FALSE WHERE key = 'gold'`); err != nil {
		t.Fatalf("disable gold tier: %v", err)
	}

	list, err := store.ListComments(ctx, input)
	if err != nil {
		t.Fatalf("list comments after: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("comments after = %d, want 2（徽章过滤不得丢行：这是 INNER JOIN 回归的信号）", len(list.Items))
	}
	for _, item := range list.Items {
		if item.TopBadge != nil {
			t.Fatalf("停档后 topBadge 必须为 nil，comment %d 得到 %+v", item.ID, item.TopBadge)
		}
	}
	if got := commentByID(t, list.Items, plainCommentID); got.ID != plainCommentID {
		t.Fatalf("零徽章作者的评论被丢掉了：%+v", got)
	}

	detail, err := store.GetTopic(ctx, graph.TopicID)
	if err != nil {
		t.Fatalf("get topic: %v", err)
	}
	if detail.TopBadge != nil {
		t.Fatalf("停档后主题详情的 topBadge 必须为 nil，得到 %+v", detail.TopBadge)
	}

	// 库里的脏值仍在：上面的 nil 是读路径判出来的，不是「有人把 top_badge_id 清了」。
	var topBadgeID *int64
	if err := pool.QueryRow(ctx, `SELECT top_badge_id FROM users WHERE id = $1`, graph.UserID).Scan(&topBadgeID); err != nil {
		t.Fatalf("read worn badge: %v", err)
	}
	if topBadgeID == nil || *topBadgeID != graph.BadgeID {
		t.Fatalf("top_badge_id = %v, want %d（用例前提：停档后重算未跑）", topBadgeID, graph.BadgeID)
	}
}

// commentByID 按 id 取评论；取不到直接失败，避免零值评论冒充断言对象。
func commentByID(t *testing.T, items []Comment, id int64) Comment {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("comment %d not found in %d items", id, len(items))
	return Comment{}
}
