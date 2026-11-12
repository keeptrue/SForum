package badges

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// 资料页徽章墙（ListUserBadges）的「获得条件」文案测试。
//
// 这一层必须证明的是**读路径真的选了 rule / requirement_summary_\***：文案由
// `Badge.Public()` → `BuildRequirementText` 生成，查询漏列时不会报错，只会让
// `RequirementText` 恒为零值——目录页有文案、资料页空白，前端把整行隐藏。
// 所以断言必须是**值级 + 双向**（有文案 vs 设计上就该空），而不是「字段存在」。
//
// 造数用 SQL：fixture 的 `badge()` helper 固定单叶子 + 无运营文案，
// 覆盖不到「多条件」「运营文案优先」这两类分支。

const (
	// 单 activity.count 叶子 + window.all + gte：BuildRequirementText 的快路径。
	singleConditionRule = `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":5}}}`
	// 两个子节点：不是快路径形状，按设计不生成文案。
	multiConditionRule = `{"v":1,"root":{"op":"all","children":[` +
		`{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":5}},` +
		`{"type":"identity.email_verified"}]}}`
)

// astWordPattern 用词边界匹配 AST 词汇：夹具里的英文文案故意含 "topics"，
// 证明 `\bop\b` 不会把 "topics" 误判成泄露（子串匹配会假红）。
var astWordPattern = regexp.MustCompile(`(?i)\b(cmp|params|children|op|rule|type|metric|window)\b`)

func (f *badgesPGFixture) requirementBadge(t *testing.T, slug, rule, summaryZh, summaryEn string) {
	t.Helper()
	grantMode := "automatic"
	var ruleArg any = rule
	if rule == "" {
		grantMode = "manual"
		ruleArg = nil
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, rule,
		                    requirement_summary_zh, requirement_summary_en, enabled)
		VALUES ($1,$1,'bronze',$2,$3::jsonb,$4,$5,TRUE)`,
		slug, grantMode, ruleArg, summaryZh, summaryEn); err != nil {
		t.Fatal(err)
	}
}

func (f *badgesPGFixture) userBadgesBySlug(t *testing.T, userID int64) map[string]UserBadge {
	t.Helper()
	items, err := NewPostgresStore(f.pool).ListUserBadges(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListUserBadges: %v", err)
	}
	bySlug := make(map[string]UserBadge, len(items))
	for _, item := range items {
		bySlug[item.Badge.Slug] = item
	}
	return bySlug
}

// 单条件规则：两种语言都必须有文案，且不含 AST 词汇。
func TestListUserBadgesGeneratesRequirementTextForSingleConditionRule(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "alice", "active")
	fixture.requirementBadge(t, "ten-topics", singleConditionRule, "", "")

	item, ok := fixture.userBadgesBySlug(t, userID)["ten-topics"]
	if !ok {
		t.Fatal("enabled badge missing from profile wall")
	}
	if item.Badge.RequirementText.Zh == "" || item.Badge.RequirementText.En == "" {
		t.Fatalf("single-condition rule must yield bilingual text, got %+v", item.Badge.RequirementText)
	}
	// 英文样本必须真的含 "topics"：词边界断言才有意义。
	if !regexp.MustCompile(`\btopics\b`).MatchString(item.Badge.RequirementText.En) {
		t.Fatalf("en text must mention the metric label, got %q", item.Badge.RequirementText.En)
	}
	for _, text := range []string{item.Badge.RequirementText.Zh, item.Badge.RequirementText.En} {
		if match := astWordPattern.FindString(text); match != "" {
			t.Fatalf("requirement text must not leak AST vocabulary %q: %q", match, text)
		}
	}
}

// 多条件规则：**必须**有文案（手填已移除，空串就是公开面空白这起报障本身）。
func TestListUserBadgesGeneratesRequirementTextForMultiConditionRule(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "bob", "active")
	fixture.requirementBadge(t, "active-and-verified", multiConditionRule, "", "")

	item, ok := fixture.userBadgesBySlug(t, userID)["active-and-verified"]
	if !ok {
		t.Fatal("enabled badge missing from profile wall")
	}
	text := item.Badge.RequirementText
	if text.Zh == "" || text.En == "" {
		t.Fatalf("multi-condition rule must yield bilingual text, got %+v", text)
	}
	if !strings.Contains(text.Zh, "并且") {
		t.Fatalf("multi-condition zh text must join with 并且, got %q", text.Zh)
	}
	for _, value := range []string{text.Zh, text.En} {
		if match := astWordPattern.FindString(value); match != "" {
			t.Fatalf("requirement text must not leak AST vocabulary %q: %q", match, value)
		}
	}
}

// 手工徽章（无规则）：固定句，且**不再**读废弃的手写覆盖列。
func TestListUserBadgesManualBadgeIgnoresDeprecatedOperatorText(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "carol", "active")
	fixture.requirementBadge(t, "manual-copy", "", "运营写的获取条件", "Operator copy wins")

	item, ok := fixture.userBadgesBySlug(t, userID)["manual-copy"]
	if !ok {
		t.Fatal("enabled badge missing from profile wall")
	}
	text := item.Badge.RequirementText
	if text.Zh != manualRequirementZh || text.En != manualRequirementEn {
		t.Fatalf("hand-written copy must be ignored (deprecated), got %+v", text)
	}
}

// 公开目录里每一枚徽章都必须有文案（含手工徽章）。
//
// 用 12 枚覆盖真实种子形状的组合（单条件 / 多条件 / 带窗口 / 手工），同时验证目录
// 扫描的两段式投影：扫描 → 一次名字解析 → 渲染（不是逐行查库）。
func TestPublicCatalogBadgesAllHaveRequirementText(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	windowed := `{"v":1,"root":{"type":"activity.count",` +
		`"params":{"metric":"comment_count","window":{"kind":"recent_days","days":30}},` +
		`"cmp":{"op":"gte","value":5}}}`
	for index := 0; index < 10; index++ {
		fixture.requirementBadge(t, "auto-"+string(rune('a'+index)), singleConditionRule, "", "")
	}
	fixture.requirementBadge(t, "windowed", windowed, "", "")
	fixture.requirementBadge(t, "multi", multiConditionRule, "", "")
	fixture.requirementBadge(t, "manual", "", "", "")

	entries, _, err := NewPostgresStore(fixture.pool).ListPublicCatalog(context.Background(), PublicCatalogLimit)
	if err != nil {
		t.Fatalf("ListPublicCatalog: %v", err)
	}
	if len(entries) != 13 {
		t.Fatalf("catalog should list all 13 seeded badges, got %d", len(entries))
	}
	for _, entry := range entries {
		text := entry.RequirementText
		if text.Zh == "" || text.En == "" {
			t.Fatalf("badge %q has empty requirement text: %+v", entry.Slug, text)
		}
		for _, value := range []string{text.Zh, text.En} {
			if match := astWordPattern.FindString(value); match != "" {
				t.Fatalf("badge %q leaks AST vocabulary %q: %q", entry.Slug, match, value)
			}
		}
	}
}
