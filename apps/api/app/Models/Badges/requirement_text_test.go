package badges

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 本文件是「公开说明自动生成」的主测试。三条不可退让的性质：
//  1. 13 种条件**全部**有句子，新增条件不写句子直接判红；
//  2. 任何形状的规则（深嵌套、超宽、脏数据）都给得出**非空**文案；
//  3. 文案是公开可见的：不含 AST 词汇、不含参数 slug、长度有上限。

// astVocabularyPattern 用词边界判定内部词汇：夹具英文里出现 "topics" 是合法的，
// 子串匹配会把 "topics" 里的 "op" 误判成泄露（那会让测试变成噪声源）。
var astVocabularyPattern = regexp.MustCompile(`(?i)\b(cmp|params|children|op|rule|type|metric|window|slug)\b`)

// rawSlugPattern 是「参数取值被当成文案」的判据：指标 slug、条件 key、参数名。
var rawSlugPattern = regexp.MustCompile(
	`(?i)\b(topic_count|comment_count|like_received_count|like_given_count|bookmark_count|days_visited|` +
		`minDays|targetSlug|targetKind|topicId|roleKey|providerId|factKey|recent_days|` +
		`(activity|identity|object|content|badge|fact)\.[a-z_]+)\b`)

// resolvedNames 是「真名都能解析到」的样本集合。
func resolvedNames() conditions.RefNames {
	names := conditions.NewRefNames()
	names.AddCategory("general", "综合讨论")
	names.AddTag("go", "Go 语言")
	names.AddRole("moderator", "版主")
	names.AddBadge("veteran", "老用户")
	names.AddProvider("github", "GitHub")
	names.AddTopic(42, "社区公约")
	names.AddFact("checkin", "签到", "check-in")
	names.AddFactVariant("checkin", "daily", "每日")
	return names
}

func gte(value int64) *ruleengine.Compare {
	return &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: value}
}

func leaf(typ string, params map[string]any, cmp *ruleengine.Compare) ruleengine.Node {
	return ruleengine.Node{Type: typ, Params: params, Cmp: cmp}
}

// phraseSamples 是 13 种条件的代表叶子 + 期望句子（golden）。
//
// 期望句逐字写死：这正是「运营读到的措辞」的唯一可审位置；改措辞就必须改这里，
// 于是措辞变更不可能悄悄发生。
func phraseSamples() []struct {
	name   string
	rule   ruleengine.Node
	wantZh string
	wantEn string
} {
	allTime := map[string]any{"kind": ruleengine.WindowKindAll}
	recent := func(days int) map[string]any {
		return map[string]any{"kind": ruleengine.WindowKindRecentDays, "days": float64(days)}
	}
	return []struct {
		name   string
		rule   ruleengine.Node
		wantZh string
		wantEn string
	}{
		{
			name:   "activity.count/all-time",
			rule:   leaf(conditions.KeyActivityCount, map[string]any{"metric": "topic_count", "window": allTime}, gte(10)),
			wantZh: "累计发布主题 ≥ 10",
			wantEn: "topics published ≥ 10",
		},
		{
			name:   "activity.count/window",
			rule:   leaf(conditions.KeyActivityCount, map[string]any{"metric": "days_visited", "window": recent(30)}, gte(7)),
			wantZh: "最近 30 天内访问天数 ≥ 7",
			wantEn: "days visited ≥ 7 in the last 30 days",
		},
		{
			name:   "activity.streak",
			rule:   leaf(conditions.KeyActivityStreak, map[string]any{"minDays": float64(7)}, gte(7)),
			wantZh: "连续访问 ≥ 7 天",
			wantEn: "visit streak ≥ 7 days",
		},
		{
			name:   "object.engagement",
			rule:   leaf(conditions.KeyObjectEngagement, map[string]any{"action": "like", "topicId": float64(42)}, nil),
			wantZh: "点赞过主题「社区公约」",
			wantEn: `liked the topic "社区公约"`,
		},
		{
			name: "object.authoring/category-window",
			rule: leaf(conditions.KeyObjectAuthoring, map[string]any{
				"targetKind": "category", "targetSlug": "general", "window": recent(30),
			}, gte(5)),
			wantZh: "最近 30 天内在分类「综合讨论」发布主题 ≥ 5 篇",
			wantEn: `topics published in category "综合讨论" ≥ 5 in the last 30 days`,
		},
		{
			name:   "identity.account_age",
			rule:   leaf(conditions.KeyIdentityAccountAge, nil, gte(30)),
			wantZh: "账户年龄 ≥ 30 天",
			wantEn: "account age ≥ 30 days",
		},
		{
			name:   "identity.email_verified",
			rule:   leaf(conditions.KeyIdentityEmailVerify, nil, nil),
			wantZh: "邮箱已验证",
			wantEn: "email verified",
		},
		{
			name:   "identity.oauth_link",
			rule:   leaf(conditions.KeyIdentityOAuthLink, map[string]any{"providerId": "github"}, nil),
			wantZh: "已绑定「GitHub」登录",
			wantEn: `"GitHub" sign-in linked`,
		},
		{
			name:   "identity.role",
			rule:   leaf(conditions.KeyIdentityRole, map[string]any{"roleKey": "moderator"}, nil),
			wantZh: "拥有「版主」角色",
			wantEn: `holds the "版主" role`,
		},
		{
			name:   "identity.avatar",
			rule:   leaf(conditions.KeyIdentityAvatar, nil, nil),
			wantZh: "已设置头像",
			wantEn: "avatar set",
		},
		{
			name:   "content.pinned",
			rule:   leaf(conditions.KeyContentPinned, nil, gte(1)),
			wantZh: "主题被置顶 ≥ 1 篇",
			wantEn: "topics pinned ≥ 1",
		},
		{
			name:   "content.hidden",
			rule:   leaf(conditions.KeyContentHidden, nil, gte(1)),
			wantZh: "主题被隐藏 ≥ 1 篇",
			wantEn: "topics hidden ≥ 1",
		},
		{
			name:   "badge.owned",
			rule:   leaf(conditions.KeyBadgeOwned, map[string]any{"slug": "veteran"}, nil),
			wantZh: "已拥有徽章「老用户」",
			wantEn: `owns the "老用户" badge`,
		},
		{
			name: "fact.count/window-variant",
			rule: leaf(conditions.KeyFactCount, map[string]any{
				"factKey": "checkin", "variant": "daily", "window": recent(7),
			}, gte(3)),
			wantZh: "最近 7 天内「签到（每日）」≥ 3",
			wantEn: `"check-in (每日)" total ≥ 3 in the last 7 days`,
		},
	}
}

// TestEveryConditionHasPhrase 是「新增条件必须同时写句子」的注册锁。
//
// 数量与 `Metrics()` 不同：条件目录是产品面，多一条少一条都要在这里显式改，
// 于是「加了条件但忘了 Phrase」不可能等到公开面空白才被发现。
func TestEveryConditionHasPhrase(t *testing.T) {
	definitions := conditions.Core().Definitions()
	if len(definitions) != 13 {
		t.Fatalf("条件目录应为 13 种，得到 %d 种", len(definitions))
	}
	samples := make(map[string]ruleengine.Node, len(phraseSamples()))
	for _, sample := range phraseSamples() {
		samples[sample.rule.Type] = sample.rule
	}
	for _, definition := range definitions {
		if definition.Phrase == nil {
			t.Fatalf("条件 %q 没有 Phrase：新增条件必须同时写公开说明", definition.Key)
		}
		if definition.LabelZh == "" || definition.LabelEn == "" {
			t.Fatalf("条件 %q 缺双语标签", definition.Key)
		}
		// 只判 nil 是不够的：把某个 Phrase 改成 `return "", ""` 仍然通过。
		// 这里用最小合法参数**实际调用一次**，空串当场判红。
		sample, ok := samples[definition.Key]
		if !ok {
			t.Fatalf("条件 %q 没有 golden 样本：新增条件必须同时补样例句子", definition.Key)
		}
		zh, en := definition.Phrase(sample.Params, sample.Cmp, conditions.NewRefNames())
		if strings.TrimSpace(zh) == "" || strings.TrimSpace(en) == "" {
			t.Fatalf("条件 %q 的 Phrase 返回了空句子：zh=%q en=%q", definition.Key, zh, en)
		}
	}
}

// TestPhraseGolden 逐条固定 13 种条件的措辞，并同时验证「干净」：
// 非空、无 AST 词汇、无原始 slug、单句长度在上限内。
func TestPhraseGolden(t *testing.T) {
	names := resolvedNames()
	for _, sample := range phraseSamples() {
		t.Run(sample.name, func(t *testing.T) {
			rule := sample.rule
			text := BuildRuleRequirementText(&rule, names)
			if text.Zh != sample.wantZh {
				t.Fatalf("zh = %q, want %q", text.Zh, sample.wantZh)
			}
			if text.En != sample.wantEn {
				t.Fatalf("en = %q, want %q", text.En, sample.wantEn)
			}
			assertPublicTextIsClean(t, text)
		})
	}
}

func assertPublicTextIsClean(t *testing.T, text RequirementText) {
	t.Helper()
	if strings.TrimSpace(text.Zh) == "" || strings.TrimSpace(text.En) == "" {
		t.Fatalf("公开说明不得为空：%+v", text)
	}
	if utf8.RuneCountInString(text.Zh) > maxTextRunes || utf8.RuneCountInString(text.En) > maxTextRunes {
		t.Fatalf("公开说明超长（zh %d / en %d）：%+v",
			utf8.RuneCountInString(text.Zh), utf8.RuneCountInString(text.En), text)
	}
	for _, value := range []string{text.Zh, text.En} {
		if match := astVocabularyPattern.FindString(value); match != "" {
			t.Fatalf("公开说明泄露内部词汇 %q：%q", match, value)
		}
		if match := rawSlugPattern.FindString(value); match != "" {
			t.Fatalf("公开说明泄露原始取值 %q：%q", match, value)
		}
	}
}

// TestGroupComposition 覆盖 all / any / not 与「子句过多只列前三条」。
func TestGroupComposition(t *testing.T) {
	names := resolvedNames()
	first := leaf(conditions.KeyActivityCount, map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "all"}}, gte(10))
	second := leaf(conditions.KeyIdentityEmailVerify, nil, nil)
	third := leaf(conditions.KeyIdentityAvatar, nil, nil)

	all := ruleengine.Node{Op: ruleengine.OpAll, Children: []ruleengine.Node{first, second, third}}
	text := BuildRuleRequirementText(&all, names)
	if text.Zh != "累计发布主题 ≥ 10，并且邮箱已验证，并且已设置头像" {
		t.Fatalf("all 连接不符合预期：%q", text.Zh)
	}
	if text.En != "topics published ≥ 10, and email verified, and avatar set" {
		t.Fatalf("all 英文连接不符合预期：%q", text.En)
	}

	any := ruleengine.Node{Op: ruleengine.OpAny, Children: []ruleengine.Node{first, second}}
	if got := BuildRuleRequirementText(&any, names); got.Zh != "累计发布主题 ≥ 10，或者邮箱已验证" ||
		got.En != "topics published ≥ 10, or email verified" {
		t.Fatalf("any 连接不符合预期：%+v", got)
	}

	not := ruleengine.Node{Op: ruleengine.OpNot, Children: []ruleengine.Node{second}}
	if got := BuildRuleRequirementText(&not, names); got.Zh != "不满足：邮箱已验证" || got.En != "Not: email verified" {
		t.Fatalf("not 连接不符合预期：%+v", got)
	}

	// 嵌套的 not 必须小写：`A and Not: B` 等于句中出现大写句首。
	mixed := ruleengine.Node{Op: ruleengine.OpAll, Children: []ruleengine.Node{first, not}}
	mixedText := BuildRuleRequirementText(&mixed, names)
	if mixedText.En != "topics published ≥ 10, and not: email verified" {
		t.Fatalf("嵌套 not 英文应小写：%q", mixedText.En)
	}
	if mixedText.Zh != "累计发布主题 ≥ 10，并且不满足：邮箱已验证" {
		t.Fatalf("嵌套 not 中文不符合预期：%q", mixedText.Zh)
	}

	// 5 个条件 → 只列前 3 + 总数（压缩不是丢信息：总数仍然给出）。
	wide := ruleengine.Node{Op: ruleengine.OpAll, Children: []ruleengine.Node{
		first, second, third,
		leaf(conditions.KeyIdentityAccountAge, nil, gte(30)),
		leaf(conditions.KeyContentPinned, nil, gte(1)),
	}}
	wideText := BuildRuleRequirementText(&wide, names)
	if wideText.Zh != "满足全部 5 个条件，例如：累计发布主题 ≥ 10、邮箱已验证、已设置头像" {
		t.Fatalf("超宽规则中文压缩不符合预期：%q", wideText.Zh)
	}
	if wideText.En != "Meets all 5 conditions, e.g. topics published ≥ 10, email verified and avatar set" {
		t.Fatalf("超宽规则英文压缩不符合预期：%q", wideText.En)
	}
	assertPublicTextIsClean(t, wideText)
}

// TestDeepNestingAlwaysRenders 覆盖「深 5 / 叶 32」的极端规则：不空、不超长。
func TestDeepNestingAlwaysRenders(t *testing.T) {
	names := resolvedNames()
	leaves := make([]ruleengine.Node, 0, 32)
	for index := 0; index < 32; index++ {
		leaves = append(leaves, leaf(conditions.KeyActivityCount, map[string]any{
			"metric": "topic_count", "window": map[string]any{"kind": "all"},
		}, gte(int64(index+1))))
	}
	deep := ruleengine.Node{Op: ruleengine.OpAll, Children: leaves}
	for depth := 0; depth < 4; depth++ {
		deep = ruleengine.Node{Op: ruleengine.OpAll, Children: []ruleengine.Node{deep}}
	}
	text := BuildRuleRequirementText(&deep, names)
	assertPublicTextIsClean(t, text)
	if !strings.Contains(text.Zh, "32 个条件") || !strings.Contains(text.En, "32 conditions") {
		t.Fatalf("极端规则应给概括句（总数 32）：%+v", text)
	}
}

// TestManualBadgeHasFixedSentence：手工徽章没有规则，给固定句而不是空串。
func TestManualBadgeHasFixedSentence(t *testing.T) {
	text := BuildRequirementText(Badge{}, conditions.NewRefNames())
	if text.Zh != manualRequirementZh || text.En != manualRequirementEn {
		t.Fatalf("手工徽章应给固定句，得到 %+v", text)
	}
	// 提交的草稿规则为空时，预览端点走同一条路径（同输入同输出）。
	if preview := BuildRuleRequirementText(nil, conditions.NewRefNames()); preview != text {
		t.Fatalf("预览与公开面对空规则必须一致：%+v vs %+v", preview, text)
	}
}

// TestBrokenRuleDoesNotClaimManual：解不开的规则不能被说成「手动授予」。
func TestBrokenRuleDoesNotClaimManual(t *testing.T) {
	text := BuildRequirementText(Badge{RuleBroken: true}, conditions.NewRefNames())
	if text.Zh == manualRequirementZh {
		t.Fatalf("脏规则不得谎称手动授予：%+v", text)
	}
	assertPublicTextIsClean(t, text)
}

// TestUnknownConditionIsOpaque：插件卸载后留下的条件 不应泄露条件 key。
func TestUnknownConditionIsOpaque(t *testing.T) {
	rule := ruleengine.Node{Type: "plugin.removed-condition"}
	text := BuildRuleRequirementText(&rule, conditions.NewRefNames())
	if text.Zh != genericConditionZh || text.En != genericConditionEn {
		t.Fatalf("未知条件应给泛称句，得到 %+v", text)
	}
	if strings.Contains(text.Zh+text.En, "removed-condition") {
		t.Fatalf("未知条件不得泄露 key：%+v", text)
	}
}

// TestNamesFallBackToGeneric：解析不到真名时回落泛称（公开面永不空白的原因之一）。
func TestNamesFallBackToGeneric(t *testing.T) {
	rule := leaf(conditions.KeyObjectAuthoring, map[string]any{
		"targetKind": "tag", "targetSlug": "deleted-tag", "window": map[string]any{"kind": "all"},
	}, gte(3))
	text := BuildRuleRequirementText(&rule, conditions.NewRefNames())
	if !strings.Contains(text.Zh, "指定标签") || !strings.Contains(text.En, "the selected tag") {
		t.Fatalf("未解析的标签应回落泛称，得到 %+v", text)
	}
	// 命中真名时不得再出现泛称。
	hitRule := leaf(conditions.KeyObjectAuthoring, map[string]any{
		"targetKind": "tag", "targetSlug": "go", "window": map[string]any{"kind": "all"},
	}, gte(3))
	hit := BuildRuleRequirementText(&hitRule, resolvedNames())
	if strings.Contains(hit.Zh, "指定标签") {
		t.Fatalf("命中真名时不应回落泛称：%+v", hit)
	}
}

// TestBuildRequirementTextSharesPreviewImplementation：公开面与预览是同一实现。
//
// 断言的是「同输入同输出」，而不是两处各自看起来对：任何让预览与公开面分叉的
// 改动（预览自己拼句子）都会在这里失败。
func TestBuildRequirementTextSharesPreviewImplementation(t *testing.T) {
	names := resolvedNames()
	for _, sample := range phraseSamples() {
		rule := sample.rule
		public := BuildRequirementText(Badge{Slug: "sample", Name: "样本", Rule: &rule}, names)
		preview := BuildRuleRequirementText(&rule, names)
		if public != preview {
			t.Fatalf("%s：公开面 %+v 与预览 %+v 不一致", sample.name, public, preview)
		}
	}
}

// TestLongNamesDegradeToSummary 是长度降级的**可达**用例。
//
// 判据：删掉 `maxTextRunes` 降级分支后，必须有一条测试变红。构造方式是 4 叶 all +
// 每个叶子带 24 rune（名字截断上限）的分类名：单句都在 100 以内，但整段超过 160。
func TestLongNamesDegradeToSummary(t *testing.T) {
	names := conditions.NewRefNames()
	longName := strings.Repeat("长", conditions.MaxRefNameRunes)
	children := make([]ruleengine.Node, 0, 4)
	for index := 0; index < 4; index++ {
		slug := fmt.Sprintf("category-%d", index)
		names.AddCategory(slug, longName)
		children = append(children, leaf(conditions.KeyObjectAuthoring, map[string]any{
			"targetKind": "category", "targetSlug": slug,
			"window": map[string]any{"kind": ruleengine.WindowKindAll},
		}, gte(5)))
	}
	rule := ruleengine.Node{Op: ruleengine.OpAll, Children: children}

	text := BuildRuleRequirementText(&rule, names)
	if !strings.Contains(text.Zh, "最关键的是") || !strings.Contains(text.En, "most important") {
		t.Fatalf("超长规则应降级为概括句：%+v", text)
	}
	if !strings.Contains(text.Zh, "满足 4 个条件") {
		t.Fatalf("概括句应给出条件总数：%q", text.Zh)
	}
	assertPublicTextIsClean(t, text)
}

// TestPhraseSentinelsRejectUnknownParamValues 是「名词必须来自文案表」的哨兵。
//
// 合法取值（`category` / `tag`）与文案逐字相同，所以「拼参数值」与「查文案表」
// 在 golden 上无法区分；用**非法取值**才分得开：任何参数原值都不许出现在句子里。
func TestPhraseSentinelsRejectUnknownParamValues(t *testing.T) {
	cases := []struct {
		name   string
		rule   ruleengine.Node
		banned string
	}{
		{
			name: "object.authoring/unknown targetKind",
			rule: leaf(conditions.KeyObjectAuthoring, map[string]any{
				"targetKind": "region", "targetSlug": "general",
				"window": map[string]any{"kind": ruleengine.WindowKindAll},
			}, gte(3)),
			banned: "region",
		},
		{
			name:   "object.engagement/unknown action",
			rule:   leaf(conditions.KeyObjectEngagement, map[string]any{"action": "poked", "topicId": float64(42)}, nil),
			banned: "poked",
		},
		{
			name:   "activity.count/unknown metric",
			rule:   leaf(conditions.KeyActivityCount, map[string]any{"metric": "karma_sentinel", "window": map[string]any{"kind": ruleengine.WindowKindAll}}, gte(3)),
			banned: "karma_sentinel",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := BuildRuleRequirementText(&tc.rule, resolvedNames())
			assertPublicTextIsClean(t, text)
			for _, value := range []string{text.Zh, text.En} {
				if strings.Contains(strings.ToLower(value), tc.banned) {
					t.Fatalf("参数原值 %q 泄漏进公开说明：%q", tc.banned, value)
				}
			}
		})
	}
}

// TestFactPhraseDoesNotRepeatTotal：兜底名与模板只能有一边带 "total"。
func TestFactPhraseDoesNotRepeatTotal(t *testing.T) {
	rule := leaf(conditions.KeyFactCount, map[string]any{
		"factKey": "undeclared", "window": map[string]any{
			"kind": ruleengine.WindowKindRecentDays, "days": float64(7),
		},
	}, gte(3))
	text := BuildRuleRequirementText(&rule, conditions.NewRefNames())
	if count := strings.Count(strings.ToLower(text.En), "total"); count != 1 {
		t.Fatalf("英文事实句应恰有一个 total，得到 %d：%q", count, text.En)
	}
	if !strings.Contains(text.Zh, "插件事实计数") {
		t.Fatalf("中文事实句应回落泛称：%q", text.Zh)
	}
}
