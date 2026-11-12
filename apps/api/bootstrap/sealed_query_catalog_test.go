package bootstrap

import (
	"slices"
	"testing"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

// sealedCoreQueryCatalogIDs 是密封 Core Query 目录的**字面期望集合**（L-06，bootstrap 侧）。
//
// 同一份清单在 `app/Http/Controllers/Extensions/query_catalog_test.go` 里还有一份
// （`sealedQueryCatalogIDs`）——**两处同源，改一处必须同批改另一处**。为什么允许两份：
// bootstrap 断言的是**装配产物**（publication/snapshot），controller 断言的是 **HTTP 响应**，
// 两个包各自必须能独立红；共用一个包会把两个独立失败面耦合成一个。
//
// 为什么不再用 `12 + len(ShortcodeProjectionQueryIDs())`：那个表达式在徽章三条查询落地时
// 只会说「19 != 22」，不说多了哪几条，于是红了一整天没人认领。纯计数断言没有信息量。
//
// 构成：15 stable（含 1 条 consent-gated 的 core.query.identity.self_email）
// + 7 shortcode 投影 = 23。四条徽章查询来自基线提交 f63ad2cf9 与规则引擎那一刀。
var sealedCoreQueryCatalogIDs = []string{
	// stable（16）
	"core.query.identity.self_email",
	"core.query.public_attachment.by_public_id",
	"core.query.public_badges.list",
	"core.query.public_bookmarks.list",
	"core.query.public_categories.list",
	"core.query.public_comments.list",
	"core.query.public_reactions.list",
	"core.query.public_topic.by_id",
	"core.query.public_topics.list",
	"core.query.public_user_activity.list",
	"core.query.public_user_badge_facts.list",
	"core.query.public_user_badges.list",
	"core.query.safe_user.by_id",
	"core.query.safe_user.by_username",
	"core.query.safe_users.batch",
	"core.query.site_stats.current",
	// shortcode 投影（7）
	"core.query.shortcode.author_decisions.batch",
	"core.query.shortcode.friend_links.list",
	"core.query.shortcode.public_categories.batch",
	"core.query.shortcode.public_comments.batch",
	"core.query.shortcode.public_topics.batch",
	"core.query.shortcode.public_users.batch",
	"core.query.shortcode.reply_eligibility.batch",
}

// sealedCoreQueryCatalogBadgeIDs 单独点名：徽章那三条被吞掉过一次，专门留一条读得懂的断言。
var sealedCoreQueryCatalogBadgeIDs = []string{
	"core.query.public_badges.list",
	"core.query.public_user_badges.list",
	"core.query.public_user_activity.list",
}

// assertSealedCoreQueryCatalogIDs 校验装配产物里的 Core Query 集合。
//
// 顺序不能反：先点名徽章，再比总数与集合。删掉一条徽章查询时总数会先红（22→21），
// 点名那句就永远不可达——那正是 L-06 的「红了但红错地方」。
func assertSealedCoreQueryCatalogIDs(t *testing.T, got []string) {
	t.Helper()

	// 清单自身的完整性：重复条目会让「总数相等」变成假绿。
	wantSorted := slices.Clone(sealedCoreQueryCatalogIDs)
	slices.Sort(wantSorted)
	if len(slices.Compact(slices.Clone(wantSorted))) != len(wantSorted) {
		t.Fatalf("sealedCoreQueryCatalogIDs 自身有重复条目：%v", wantSorted)
	}

	gotIDs := make(map[string]struct{}, len(got))
	for _, id := range got {
		gotIDs[id] = struct{}{}
	}

	// 1. 先点名徽章三条。
	for _, badgeQueryID := range sealedCoreQueryCatalogBadgeIDs {
		if _, ok := gotIDs[badgeQueryID]; !ok {
			t.Fatalf("徽章查询 %q 不在密封 Core Query 目录里（基线 f63ad2cf9 落地的稳定定义）", badgeQueryID)
		}
	}

	// 2. 总数与字面集合一致。
	if len(got) != len(sealedCoreQueryCatalogIDs) {
		t.Fatalf("密封 Core Query 目录条数 = %d，期望 %d", len(got), len(sealedCoreQueryCatalogIDs))
	}

	// 3. 双向集合相等：多一条或少一条都红，且列出完整差异。
	gotSorted := slices.Clone(got)
	slices.Sort(gotSorted)
	if !slices.Equal(gotSorted, wantSorted) {
		t.Fatalf("密封 Core Query 目录集合与期望不一致\n实际：%v\n期望：%v", gotSorted, wantSorted)
	}
}

// publicationQueryIDs 把 publication 的声明摊平成 ID 列表。
func publicationQueryIDs(queries []queryregistry.QueryDeclaration) []string {
	ids := make([]string, 0, len(queries))
	for _, declaration := range queries {
		ids = append(ids, declaration.ID)
	}
	return ids
}
