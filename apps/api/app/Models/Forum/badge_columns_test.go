package forum

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

// TestBadgeRefFromNullsHandlesMissingBadge 固定 NULL 路径：
// LEFT JOIN 没匹配上时四列全为 NULL，必须落成 nil 而不是空结构，
// 更不能用 string 直接扫（那会在「用户没有徽章」时报错）。
func TestBadgeRefFromNullsHandlesMissingBadge(t *testing.T) {
	if ref := badgeRefFromNulls(sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}); ref != nil {
		t.Fatalf("no badge must yield nil, got %+v", ref)
	}
	if ref := badgeRefFromNulls(sql.NullString{String: "", Valid: true}, sql.NullString{Valid: true}, sql.NullString{Valid: true}, sql.NullString{Valid: true}); ref != nil {
		t.Fatalf("empty slug must yield nil, got %+v", ref)
	}
	ref := badgeRefFromNulls(
		sql.NullString{String: "veteran", Valid: true},
		sql.NullString{String: "元老", Valid: true},
		sql.NullString{String: "gold", Valid: true},
		sql.NullString{String: "i-lucide-hourglass", Valid: true},
	)
	if ref == nil || ref.Slug != "veteran" || ref.Name != "元老" || ref.Tier != "gold" || ref.Icon != "i-lucide-hourglass" {
		t.Fatalf("badge ref = %+v, want veteran/元老/gold/hourglass", ref)
	}
}

// TestReadSQLProjectsTopBadgeAtColumnTail 固定三个读 SQL 的徽章投影位置。
//
// 位置是契约：扫描器是位置扫描，徽章列必须追加在列尾；插在中间会让所有
// 后续列错位——而且不会报错，只会读出脏数据。
func TestReadSQLProjectsTopBadgeAtColumnTail(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		tail string
	}{
		{"topic summary", topicSummarySQL(), "FROM topics"},
		{"topic detail", topicDetailSQL(), "FROM topics"},
		{"comment", commentSelectSQL(), "FROM comments"},
	}
	for _, tc := range cases {
		normalized := strings.Join(strings.Fields(tc.sql), " ")
		if !strings.Contains(normalized, "top_badge.slug, top_badge.name, top_badge.tier, top_badge.icon") {
			t.Fatalf("%s SQL missing badge projection", tc.name)
		}
		if !strings.Contains(normalized, "LEFT JOIN badges top_badge ON top_badge.id = users.top_badge_id AND top_badge.enabled") {
			t.Fatalf("%s SQL missing badge join (or missing enabled filter)", tc.name)
		}
		// 徽章列必须紧邻 FROM：中间不允许再插别的投影。
		projection := normalized[:strings.Index(normalized, tc.tail)]
		if !strings.HasSuffix(strings.TrimSpace(projection), "top_badge.slug, top_badge.name, top_badge.tier, top_badge.icon") {
			t.Fatalf("%s SQL must project badge columns last, got tail: ...%s", tc.name, tailOf(projection, 90))
		}
	}
}

// TestTopBadgeJoinSkipsDisabledBadges 固定停用徽章不进读模型（历史授予行保留但不展示）。
func TestTopBadgeJoinSkipsDisabledBadges(t *testing.T) {
	if !strings.Contains(topBadgeJoinSQL(), "AND top_badge.enabled") {
		t.Fatal("top badge join must filter disabled badges")
	}
}

func tailOf(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[len(value)-n:]
}

// TestEveryTopicSummarySelectUsesTheSharedColumnList 是 2026-11-10 首页 500 的
// 结构性修复。
//
// 事故形态：scanTopicSummaryWithAvatar 有 42 个目的地，但 list_topics 与 search_live
// 各自手写了 38 列的拷贝，漏掉徽章 4 列 → 「number of field descriptions must equal
// number of destinations, got 38 and 42」，首页对所有人 500。
//
// 补 4 列只能治这一次。真正的修法是：列清单只有一份（topicSummaryColumns），
// 任何调用 scanTopicSummaryWithAvatar 的 SELECT 都必须用它，不许手写。
// 这条测试覆盖「所有调用点」而不是「一个」——上一版测试只盯 topicSummarySQL，
// 给了虚假的安全感。
func TestEveryTopicSummarySelectUsesTheSharedColumnList(t *testing.T) {
	files := []string{"topic_summary_sql.go", "list_topics.go", "search_live.go"}
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		source := string(body)
		if !strings.Contains(source, "topicSummaryColumns()") {
			t.Fatalf("%s must build its SELECT from topicSummaryColumns()", name)
		}
		// 除了列清单的唯一定义处，任何文件都不许再手写这段投影。
		if name != "topic_summary_sql.go" && strings.Contains(source, `plainTextPrefixSQL("posts.plain_text")`) {
			t.Fatalf("%s hand-writes the topic summary column list; use topicSummaryColumns()", name)
		}
	}

	// 列清单本身必须带徽章投影，否则共享也白搭。
	columns := strings.Join(strings.Fields(topicSummaryColumns()), " ")
	if !strings.Contains(columns, "top_badge.slug, top_badge.name, top_badge.tier, top_badge.icon") {
		t.Fatal("topicSummaryColumns must project the top badge columns")
	}
	if !strings.HasSuffix(strings.TrimSpace(columns), "top_badge.slug, top_badge.name, top_badge.tier, top_badge.icon") {
		t.Fatal("badge columns must stay at the column tail")
	}
}
