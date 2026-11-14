package trustlevels_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 本文件钉住 M2 的判定语义：规则树作为「数值快筛之后的额外闸门」。
//
// 判定层是**纯函数 + 可注入求值器**，所以这一整组测试不需要数据库：fake 求值器能
// 精确摆出「成立 / 不成立 / 判不了」三态，并记录「哪些档位的规则被真的求值过」——
// 「数值不过就不查规则」这条语义的可观测形态正是后者。

// fakeRuleEvaluator 是 `RuleEvaluator` 的测试替身。
type fakeRuleEvaluator struct {
	// outcomes 按规则字节指定结论；未指定时默认「成立」。
	outcomes map[string]trustlevels.RuleOutcome
	// prepared 记录每次 Prepare 收到的规则集合（按调用顺序）。
	prepared [][]string
	// matched 记录 Match 实际求值过的规则（按调用顺序）——用来证明「没求值」。
	matched []string
	// leaves 是 LeafConditions 的返回值。
	leaves []trustlevels.TierCondition
}

func (f *fakeRuleEvaluator) Prepare(_ context.Context, rules []json.RawMessage) {
	batch := make([]string, 0, len(rules))
	for _, rule := range rules {
		batch = append(batch, string(rule))
	}
	f.prepared = append(f.prepared, batch)
}

func (f *fakeRuleEvaluator) Match(_ context.Context, rule json.RawMessage) trustlevels.RuleOutcome {
	f.matched = append(f.matched, string(rule))
	if outcome, ok := f.outcomes[string(rule)]; ok {
		return outcome
	}
	return trustlevels.RuleOutcome{Matched: true}
}

func (f *fakeRuleEvaluator) LeafConditions(context.Context, json.RawMessage) []trustlevels.TierCondition {
	return f.leaves
}

// rulesTierJSON 造一条带规则的合法档位（id 为空 = 新档；rule 为空串时省略该字段）。
func rulesTierJSON(requirement string, rule string) string {
	base := `{"id":"","name":{"zh":"档","en":"Tier"},"icon":"leaf","requirements":` + requirement +
		`,"limits":{},"earnable":[]`
	if rule == "" {
		return base + `}`
	}
	return base + `,"rule":` + rule + `}`
}

// policyWithTierRules 造一份 tiers 全部带规则的策略；requirements 全 0（数值不设门槛）。
func policyWithTierRules(t *testing.T, tiers int) (trustlevels.Policy, []string) {
	t.Helper()
	parts := make([]string, 0, tiers)
	rules := make([]string, 0, tiers)
	for index := 0; index < tiers; index++ {
		rule := ruleDoc(verifiedLeaf)
		if index > 0 {
			// 每档用不同的树，便于按字节区分「哪一档的规则被求值」。
			rule = ruleDoc(fmt.Sprintf(`{"op":"all","children":[%s]}`, verifiedLeaf))
			for pad := 0; pad < index; pad++ {
				rule = ruleDoc(fmt.Sprintf(`{"op":"all","children":[%s]}`, verifiedLeaf))
			}
		}
		rules = append(rules, rule)
		parts = append(parts, rulesTierJSON(`{}`, rule))
	}
	policy, err := trustlevels.DecodeConfig(tiersJSON(parts...))
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	return policy, rules
}

// TestLevelOfWithRulesEqualsLevelOfWithoutRules 是等价性回归（验收 2）。
//
// 未配规则的 policy 上，`LevelOf` 与 `LevelOfWithRules(nil)` 必须在**每一组**
// Activity/账龄快照上逐个相等：这条守的是「M2 没有偷偷改变既有判定」。
func TestLevelOfWithRulesEqualsLevelOfWithoutRules(t *testing.T) {
	policies := []trustlevels.Policy{
		trustlevels.DefaultPolicy(),
		mustPolicy(t, tiersJSON(
			rulesTierJSON(`{"days_visited":1}`, ``),
			rulesTierJSON(`{"comment_count":2}`, ``),
		)),
	}
	activities := []trustlevels.Activity{
		{},
		{DaysVisited: 3, CommentCount: 5},
		{DaysVisited: 15, TopicCount: 2, CommentCount: 20, LikeReceivedCount: 5, LikeGivenCount: 3},
		{DaysVisited: 45, TopicCount: 8, CommentCount: 80, LikeReceivedCount: 30, LikeGivenCount: 20, BookmarkCount: 5},
		{DaysVisited: 100, TopicCount: 25, CommentCount: 250, LikeReceivedCount: 100, LikeGivenCount: 80, BookmarkCount: 20},
		{DaysVisited: 10000, TopicCount: 1000, CommentCount: 10000},
	}
	ages := []time.Duration{0, 6 * 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour, 3650 * 24 * time.Hour}
	for pi, policy := range policies {
		for ai, activity := range activities {
			for _, age := range ages {
				want := trustlevels.LevelOf(activity, age, policy)
				got := trustlevels.LevelOfWithRules(context.Background(), activity, age, policy, nil)
				if got != want {
					t.Fatalf("policy#%d activity#%d age=%v: LevelOfWithRules(nil)=%d ≠ LevelOf=%d", pi, ai, age, got, want)
				}
			}
		}
	}
}

// TestRuleGatesPromotion 钉住「数值过 + 规则不过 → 该档不成立，继续降档」。
func TestRuleGatesPromotion(t *testing.T) {
	// 两档，数值都过；TL2 的规则不成立。
	rule1, rule2 := ruleDoc(verifiedLeaf), ruleDoc(fmt.Sprintf(`{"op":"all","children":[%s]}`, verifiedLeaf))
	policy := mustPolicy(t, tiersJSON(rulesTierJSON(`{}`, rule1), rulesTierJSON(`{}`, rule2)))
	activity := trustlevels.Activity{}
	age := 3650 * 24 * time.Hour

	evaluator := &fakeRuleEvaluator{outcomes: map[string]trustlevels.RuleOutcome{
		rule2: {Matched: false},
	}}
	if got := trustlevels.LevelOfWithRules(context.Background(), activity, age, policy, evaluator); got != 1 {
		t.Fatalf("TL2 规则不成立时应降到 TL1，得到 %d", got)
	}
	// 反向：规则成立就回到 TL2（否则「永远降档」也会让上面那条绿）。
	evaluator = &fakeRuleEvaluator{}
	if got := trustlevels.LevelOfWithRules(context.Background(), activity, age, policy, evaluator); got != 2 {
		t.Fatalf("两档规则都成立时应为 TL2，得到 %d", got)
	}
}

// TestNumericFailureSkipsRuleEvaluation 钉住「数值不过就不查规则」（D19-c 的语义面）。
//
// 只断「等级更低」会放过「其实查了、只是结果没用」；所以断言 Match 的调用记录里
// **完全没有**该档的规则。
func TestNumericFailureSkipsRuleEvaluation(t *testing.T) {
	rule1 := ruleDoc(verifiedLeaf)
	rule2 := ruleDoc(fmt.Sprintf(`{"op":"all","children":[%s]}`, verifiedLeaf))
	policy := mustPolicy(t, tiersJSON(
		rulesTierJSON(`{"comment_count":5}`, rule1),
		rulesTierJSON(`{"topic_count":1000}`, rule2), // 永不满足
	))
	evaluator := &fakeRuleEvaluator{}
	level := trustlevels.LevelOfWithRules(context.Background(), trustlevels.Activity{CommentCount: 5}, 3650*24*time.Hour, policy, evaluator)
	if level != 1 {
		t.Fatalf("TL2 数值不过，应停在 TL1，得到 %d", level)
	}
	for _, matched := range evaluator.matched {
		if matched == rule2 {
			t.Fatal("TL2 数值不过却求值了它的规则（数值预筛失效）")
		}
	}
	// Prepare 只装「数值已过 且 带规则」的档位：TL2 不在里面。
	if len(evaluator.prepared) != 1 {
		t.Fatalf("Prepare 应只调用一次，得到 %d 次", len(evaluator.prepared))
	}
	batch := evaluator.prepared[0]
	if len(batch) != 1 || batch[0] != rule1 {
		t.Fatalf("Prepare 只该收到 TL1 的规则，得到 %v", batch)
	}
}

// TestAccountAgeGateAppliesTL1Rule 钉住「账龄闸内 TL1 的规则同样生效」。
func TestAccountAgeGateAppliesTL1Rule(t *testing.T) {
	rule1 := ruleDoc(verifiedLeaf)
	policy := mustPolicy(t, tiersJSON(rulesTierJSON(`{}`, rule1)))
	newcomerAge := time.Hour

	pass := trustlevels.LevelOfWithRules(context.Background(), trustlevels.Activity{}, newcomerAge, policy, &fakeRuleEvaluator{})
	if pass != trustlevels.LevelMember {
		t.Fatalf("账龄闸内 TL1 规则成立应为 TL1，得到 %d", pass)
	}
	fail := trustlevels.LevelOfWithRules(context.Background(), trustlevels.Activity{}, newcomerAge, policy,
		&fakeRuleEvaluator{outcomes: map[string]trustlevels.RuleOutcome{rule1: {Matched: false}}})
	if fail != trustlevels.LevelNewcomer {
		t.Fatalf("账龄闸内 TL1 规则不成立应为 TL0，得到 %d", fail)
	}
}

// TestRuleFailureIsFailClosed 钉住三种「判不了」都不放行：缺求值器 / 引擎 unknown / 脏规则。
//
// 用**两档**策略：TL1 不配规则（兜底永远成立）、TL2 配规则。规则判不了时必须是 TL1——
// 一档策略看不出差别（「至少 TL1」的既有兜底本来就会返回 1）。
func TestRuleFailureIsFailClosed(t *testing.T) {
	rule := ruleDoc(verifiedLeaf)
	policy := mustPolicy(t, tiersJSON(rulesTierJSON(`{}`, ""), rulesTierJSON(`{}`, rule)))
	age := 3650 * 24 * time.Hour

	cases := []struct {
		name      string
		evaluator trustlevels.RuleEvaluator
	}{
		{"缺少求值器", nil},
		{"引擎判不了（unknown）", &fakeRuleEvaluator{outcomes: map[string]trustlevels.RuleOutcome{
			rule: {Matched: false, Incomplete: true, Problems: []error{errors.New("查询失败")}},
		}}},
		{"脏规则", &fakeRuleEvaluator{outcomes: map[string]trustlevels.RuleOutcome{
			rule: {Incomplete: true, Problems: []error{errors.New("decode failed")}},
		}}},
		// 最隐蔽的一条：unknown 在 `not` 下面**不能**变成成立。三值语义由引擎负责，
		// 这里钉的是判定层不把 Incomplete 当 Matched 用。
		{"unknown 且 Matched=false", &fakeRuleEvaluator{outcomes: map[string]trustlevels.RuleOutcome{
			rule: {Matched: false, Incomplete: true},
		}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			level := trustlevels.LevelOfWithRules(context.Background(), trustlevels.Activity{}, age, policy, testCase.evaluator)
			if level != 1 {
				t.Fatalf("规则判不了时该档必须不成立（fail-closed）→ TL1，得到 %d", level)
			}
		})
	}
}

// TestRuleOnLowerTierNotEvaluatedWhenHigherTierMatches 钉住短路：高档成立即返回。
func TestRuleOnLowerTierNotEvaluatedWhenHigherTierMatches(t *testing.T) {
	rule1 := ruleDoc(verifiedLeaf)
	rule2 := ruleDoc(fmt.Sprintf(`{"op":"all","children":[%s]}`, verifiedLeaf))
	policy := mustPolicy(t, tiersJSON(rulesTierJSON(`{}`, rule1), rulesTierJSON(`{}`, rule2)))
	evaluator := &fakeRuleEvaluator{}
	if level := trustlevels.LevelOfWithRules(context.Background(), trustlevels.Activity{}, 3650*24*time.Hour, policy, evaluator); level != 2 {
		t.Fatalf("应为 TL2，得到 %d", level)
	}
	for _, matched := range evaluator.matched {
		if matched == rule1 {
			t.Fatal("TL2 已成立却继续求值 TL1 的规则（短路失效）")
		}
	}
}

// TestRetiredTierRuleIsNeverEvaluated 钉住停用档不参与判定（含它的规则）。
func TestRetiredTierRuleIsNeverEvaluated(t *testing.T) {
	rule1 := ruleDoc(verifiedLeaf)
	retired := `{"id":"tier_7","name":{"zh":"停用","en":"Retired"},"icon":"crown","requirements":{},"limits":{},"earnable":[],"retired":true,"rule":` + rule1 + `}`
	active := `{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]}`
	policy := mustPolicy(t, tiersJSON(active, retired))
	evaluator := &fakeRuleEvaluator{}
	if level := trustlevels.LevelOfWithRules(context.Background(), trustlevels.Activity{}, 3650*24*time.Hour, policy, evaluator); level != 1 {
		t.Fatalf("停用档不参与判定，应为 TL1，得到 %d", level)
	}
	if len(evaluator.matched) != 0 {
		t.Fatalf("停用档的规则不该被求值，得到 %v", evaluator.matched)
	}
}

// mustPolicy 解码一份配置，失败即 fatal。
func mustPolicy(t *testing.T, raw string) trustlevels.Policy {
	t.Helper()
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	return policy
}
