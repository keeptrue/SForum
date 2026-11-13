package forum

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

// topBadgeProjectionTail 是徽章投影归一化后的**逐字**形状（含等级外观三列）。
//
// 徽章四列 + 等级三列必须一起出现、一起排在列尾：只要少一列，扫描目的地就会
// 与列数错位，而位置扫描不会报「少了一列」，只会把后面的值读进前面的字段。
const topBadgeProjectionTail = "top_badge.slug, top_badge.name, top_badge.tier, top_badge.icon, " +
	"COALESCE(top_badge_tier.name_zh,''), COALESCE(top_badge_tier.name_en,''), COALESCE(top_badge_tier.color,'')"

// topBadgeJoinTail 是徽章 JOIN（徽章 + 等级启用 EXISTS + 等级外观）归一化后的逐字形状。
//
// EXISTS 的位置也是契约：它必须挂在 `LEFT JOIN badges` 的 ON 上（只把徽章列置 NULL），
// 不能写成 JOIN badge_tiers —— 那样「没有徽章」的行会整行消失，列表少行。
const topBadgeJoinTail = "LEFT JOIN badges top_badge ON top_badge.id = users.top_badge_id AND top_badge.enabled " +
	"AND EXISTS (SELECT 1 FROM badge_tiers AS top_badge_tier_ok WHERE top_badge_tier_ok.key = top_badge.tier AND top_badge_tier_ok.enabled) " +
	"LEFT JOIN badge_tiers AS top_badge_tier ON top_badge_tier.key = top_badge.tier"

// TestBadgeRefFromNullsHandlesMissingBadge 固定 NULL 路径：
// LEFT JOIN 没匹配上时七列全为 NULL，必须落成 nil 而不是空结构，
// 更不能用 string 直接扫（那会在「用户没有徽章」时报错）。
func TestBadgeRefFromNullsHandlesMissingBadge(t *testing.T) {
	null := sql.NullString{}
	if ref := badgeRefFromNulls(null, null, null, null, null, null, null); ref != nil {
		t.Fatalf("no badge must yield nil, got %+v", ref)
	}
	valid := sql.NullString{Valid: true}
	if ref := badgeRefFromNulls(sql.NullString{String: "", Valid: true}, valid, valid, valid, valid, valid, valid); ref != nil {
		t.Fatalf("empty slug must yield nil, got %+v", ref)
	}
	ref := badgeRefFromNulls(
		sql.NullString{String: "veteran", Valid: true},
		sql.NullString{String: "元老", Valid: true},
		sql.NullString{String: "gold", Valid: true},
		sql.NullString{String: "i-lucide-hourglass", Valid: true},
		sql.NullString{String: "金牌", Valid: true},
		sql.NullString{String: "Gold", Valid: true},
		sql.NullString{String: "#fac400", Valid: true},
	)
	if ref == nil || ref.Slug != "veteran" || ref.Name != "元老" || ref.Tier != "gold" || ref.Icon != "i-lucide-hourglass" {
		t.Fatalf("badge ref = %+v, want veteran/元老/gold/hourglass", ref)
	}
}

// TestBadgeRefFromNullsCarriesTierAppearance 固定「等级外观随徽章一起下发」。
//
// 图1 的根因是读模型里 tierColor/tierName 为空，前端于是按 tier key 回落内置
// amber → #92400e（棕）。外观必须来自 badge_tiers 行，而不是前端的等级映射表：
// 自 202611120001 起等级是运营可改的数据，硬编码映射在新增档位时必然退化。
func TestBadgeRefFromNullsCarriesTierAppearance(t *testing.T) {
	ref := badgeRefFromNulls(
		sql.NullString{String: "veteran", Valid: true},
		sql.NullString{String: "元老", Valid: true},
		sql.NullString{String: "gold", Valid: true},
		sql.NullString{String: "i-lucide-hourglass", Valid: true},
		sql.NullString{String: "金牌", Valid: true},
		sql.NullString{String: "Gold", Valid: true},
		sql.NullString{String: "#fac400", Valid: true},
	)
	if ref == nil {
		t.Fatal("badge ref must not be nil")
	}
	if ref.TierColor != "#fac400" {
		t.Fatalf("tierColor = %q, want #fac400 (外观必须来自 badge_tiers 行)", ref.TierColor)
	}
	if ref.TierName.Zh != "金牌" || ref.TierName.En != "Gold" {
		t.Fatalf("tierName = %+v, want {金牌 Gold}", ref.TierName)
	}
}

// 旧用例 `TestBadgeRefFromNullsToleratesMissingTierRow` 已删除。
//
// 它断言的是「等级行缺失时徽章仍在、只是外观为空」。那条口径已被 2026-11-13 的
// 裁决推翻：等级行缺失/停用等价于这一档不存在，读模型必须整枚不展示
// （与 topBadgeOrderSQL 的 `AND t.enabled`、RecomputeTopBadge、SetWornBadge、
// ResolveNotificationBadgeTarget、DisplayBadge 同口径）。
//
// 新的判据在 SQL 层（下面的 TestTopBadgeJoinSkipsDisabledBadges）与真库层
// （badge_tier_appearance_postgres_integration_test.go 的
// TestPostgresReadHidesBadgeWhenTierIsDisabled）。保留一个「函数能容忍空外观」的
// 单测只会描述一条生产走不到的路径，反而给「等级缺失仍展示」留下假绿。

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
		if !strings.Contains(normalized, topBadgeProjectionTail) {
			t.Fatalf("%s SQL missing badge projection (4 badge columns + 3 tier appearance columns)", tc.name)
		}
		if !strings.Contains(normalized, topBadgeJoinTail) {
			t.Fatalf("%s SQL missing badge join or tier appearance join", tc.name)
		}
		// 徽章列必须紧邻 FROM：中间不允许再插别的投影。
		projection := normalized[:strings.Index(normalized, tc.tail)]
		if !strings.HasSuffix(strings.TrimSpace(projection), topBadgeProjectionTail) {
			t.Fatalf("%s SQL must project badge columns last, got tail: ...%s", tc.name, tailOf(projection, 90))
		}
	}
}

// TestTopBadgeJoinSkipsDisabledBadges 固定两个可见性条件都不进读模型：
// 徽章停用（历史授予行保留但不展示）与等级停用（「停用一档 = 这一档不存在」）。
//
// 少任何一个都会让 chip 显示一枚佩戴面板/通知目标已经判为不可展示的徽章。
func TestTopBadgeJoinSkipsDisabledBadges(t *testing.T) {
	normalized := strings.Join(strings.Fields(topBadgeJoinSQL()), " ")
	if !strings.Contains(normalized, "AND top_badge.enabled") {
		t.Fatal("top badge join must filter disabled badges")
	}
	tierFilter := "AND EXISTS (SELECT 1 FROM badge_tiers AS top_badge_tier_ok " +
		"WHERE top_badge_tier_ok.key = top_badge.tier AND top_badge_tier_ok.enabled)"
	if !strings.Contains(normalized, tierFilter) {
		t.Fatalf("top badge join must filter badges whose tier is disabled, got %q", normalized)
	}
}

// TestTopBadgeJoinIsLeftJoinOnTierTable 固定两件事：
// 外观 JOIN 仍是 LEFT JOIN（它只取名称/颜色），且没有任何 INNER JOIN badge_tiers
// 把「没有徽章」的行丢掉。
//
// 可见性由 ON 上的 EXISTS 负责，不是靠这个 JOIN 的方向——两者职责分开之后，
// 改外观 JOIN 不会顺手改掉可见性口径。
func TestTopBadgeJoinIsLeftJoinOnTierTable(t *testing.T) {
	normalized := strings.Join(strings.Fields(topBadgeJoinSQL()), " ")
	if !strings.Contains(normalized, "LEFT JOIN badge_tiers AS top_badge_tier ON top_badge_tier.key = top_badge.tier") {
		t.Fatalf("top badge join must LEFT JOIN badge_tiers on the tier key, got %q", normalized)
	}
	// 只允许 LEFT JOIN / EXISTS 子查询两种出现方式：任何 INNER JOIN 都会把
	// 「作者没有徽章」的行整行丢掉，列表少行是比徽章缺失更严重的回归。
	if strings.Contains(normalized, "INNER JOIN badge_tiers") {
		t.Fatalf("badge_tiers must never be inner-joined (it would drop rows without a badge), got %q", normalized)
	}
	if got := strings.Count(normalized, "badge_tiers"); got != 2 {
		t.Fatalf("badge_tiers 只应出现在 EXISTS 过滤与外观 JOIN 两处，got %d in %q", got, normalized)
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
	if !strings.Contains(columns, topBadgeProjectionTail) {
		t.Fatal("topicSummaryColumns must project the top badge columns plus tier appearance")
	}
	if !strings.HasSuffix(strings.TrimSpace(columns), topBadgeProjectionTail) {
		t.Fatal("badge columns must stay at the column tail")
	}
}
