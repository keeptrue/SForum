package badges

import (
	"context"
	"regexp"
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

// 多条件规则：按设计两种语言都为空（不是缺陷），防止有人为了「非空」把 AST 拼进文案。
func TestListUserBadgesLeavesRequirementTextEmptyForMultiConditionRule(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "bob", "active")
	fixture.requirementBadge(t, "active-and-verified", multiConditionRule, "", "")

	item, ok := fixture.userBadgesBySlug(t, userID)["active-and-verified"]
	if !ok {
		t.Fatal("enabled badge missing from profile wall")
	}
	if item.Badge.RequirementText.Zh != "" || item.Badge.RequirementText.En != "" {
		t.Fatalf("multi-condition rule must stay empty, got %+v", item.Badge.RequirementText)
	}
}

// 运营文案优先，且**逐语言独立回落**：只填中文时英文仍按规则生成。
func TestListUserBadgesPrefersOperatorTextPerLanguage(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "carol", "active")
	fixture.requirementBadge(t, "zh-only", singleConditionRule, "运营写的获取条件", "")
	fixture.requirementBadge(t, "both-languages", singleConditionRule, "运营写的获取条件", "Operator copy wins")

	bySlug := fixture.userBadgesBySlug(t, userID)

	zhOnly := bySlug["zh-only"].Badge.RequirementText
	if zhOnly.Zh != "运营写的获取条件" {
		t.Fatalf("operator zh text must win, got %q", zhOnly.Zh)
	}
	if zhOnly.En == "" || zhOnly.En == "运营写的获取条件" {
		t.Fatalf("empty en must fall back to generated text, got %q", zhOnly.En)
	}

	both := bySlug["both-languages"].Badge.RequirementText
	if both.Zh != "运营写的获取条件" || both.En != "Operator copy wins" {
		t.Fatalf("operator text must win in both languages, got %+v", both)
	}
}
