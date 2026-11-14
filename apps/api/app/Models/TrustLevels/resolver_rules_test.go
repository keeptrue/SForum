package trustlevels_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 本文件钉住 `Evaluate()` / `Grants()` 与求值器的接线（M2）。

// countingEvaluatorFactory 记录「工厂被调用了几次」，并返回固定的求值器。
//
// 这条计数是性能判据 §6.1 的单元级形态：无规则站点**根本不该构造求值器**——
// 构造本身不查库，但一旦构造，后面就可能 Prepare；把闸放在构造之前才叫「零新增查询」。
type countingEvaluatorFactory struct {
	calls     int
	evaluator trustlevels.RuleEvaluator
	err       error
}

func (f *countingEvaluatorFactory) build(context.Context, int64) (trustlevels.RuleEvaluator, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.evaluator, nil
}

// TestEvaluateMatchesGrants 钉住两条出口共用同一份判定。
func TestEvaluateMatchesGrants(t *testing.T) {
	createdAt := time.Now().Add(-365 * 24 * time.Hour)
	resolver := trustlevels.NewResolver(
		stubActivityReader{activity: trustlevels.Activity{DaysVisited: 100, TopicCount: 25, CommentCount: 250, LikeReceivedCount: 100, LikeGivenCount: 80, BookmarkCount: 20}},
		stubPolicySource{policy: trustlevels.DefaultPolicy()},
	)
	evaluation, err := resolver.Evaluate(context.Background(), 7, identity.UserKindHuman, createdAt)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	level, _, err := resolver.Grants(context.Background(), 7, identity.UserKindHuman, createdAt)
	if err != nil {
		t.Fatalf("grants: %v", err)
	}
	if int(evaluation.Level) != level {
		t.Fatalf("Evaluate=%d ≠ Grants=%d（两条出口必须同一份判定）", evaluation.Level, level)
	}
	if evaluation.TierID != "leader" {
		t.Fatalf("TierID = %q（期望 leader）", evaluation.TierID)
	}
	if evaluation.Incomplete {
		t.Fatal("无规则站点不该出现 incomplete")
	}
}

// TestNoRulesNeverBuildsEvaluator 是性能判据 §6.1 的单元形态：零新增查询的前提。
func TestNoRulesNeverBuildsEvaluator(t *testing.T) {
	factory := &countingEvaluatorFactory{evaluator: &fakeRuleEvaluator{}}
	resolver := trustlevels.NewResolver(
		stubActivityReader{activity: trustlevels.Activity{DaysVisited: 100}},
		stubPolicySource{policy: trustlevels.DefaultPolicy()}, // 默认策略没有任何 rule
	).WithRuleEvaluators(factory.build)

	if _, _, err := resolver.Grants(context.Background(), 7, identity.UserKindHuman, time.Now()); err != nil {
		t.Fatalf("grants: %v", err)
	}
	if _, err := resolver.Evaluate(context.Background(), 7, identity.UserKindHuman, time.Now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if factory.calls != 0 {
		t.Fatalf("无规则站点构造了求值器 %d 次：这条路径上没有任何规则要求值", factory.calls)
	}
}

// TestEvaluateReportsIncomplete 钉住「单档规则失败不降级整次解析」。
//
// 这是 D18 的核心口径：`Grants` 仍返回一个可信的等级，`Incomplete` 只外传给 job / 进度
// 读面（事件抑制用），不用来把整次解析标成「未解析」。
func TestEvaluateReportsIncomplete(t *testing.T) {
	rule := ruleDoc(verifiedLeaf)
	policy := mustPolicy(t, tiersJSON(rulesTierJSON(`{}`, ""), rulesTierJSON(`{}`, rule)))
	evaluator := &fakeRuleEvaluator{outcomes: map[string]trustlevels.RuleOutcome{
		rule: {Matched: false, Incomplete: true, Problems: []error{errors.New("条件查询失败")}},
	}}
	resolver := trustlevels.NewResolver(
		stubActivityReader{activity: trustlevels.Activity{}},
		stubPolicySource{policy: policy},
	).WithRuleEvaluators(func(context.Context, int64) (trustlevels.RuleEvaluator, error) { return evaluator, nil })

	evaluation, err := resolver.Evaluate(context.Background(), 7, identity.UserKindHuman, time.Now().Add(-365*24*time.Hour))
	if err != nil {
		t.Fatalf("规则失败不该把整次解析降级成 error（D18）：%v", err)
	}
	if !evaluation.Incomplete {
		t.Fatal("规则判不了时必须标记 Incomplete（M4 的 job 靠它抑制事件）")
	}
	if len(evaluation.Errors) == 0 {
		t.Fatal("Incomplete 必须带逐条问题，否则日志里看不出为什么")
	}
	if evaluation.Level != 1 {
		t.Fatalf("TL2 的规则判不了 → 该档不成立 → TL1，得到 %d", evaluation.Level)
	}
	// Grants 路径同样给出 TL1（不回退成「未解析」）。
	level, granted, err := resolver.Grants(context.Background(), 7, identity.UserKindHuman, time.Now().Add(-365*24*time.Hour))
	if err != nil || level != 1 {
		t.Fatalf("Grants = (%d, %v, %v)（期望 TL1 且无错）", level, granted, err)
	}
}

// TestEvaluationIncompleteIsReportedForEventSuppression 是 M4 事件抑制的前置之判据。
//
// job 尚未落地（M4），所以这里钉的是**判定层提供的信号**：incomplete=true 时
// `Evaluation` 必须明确置位，且等级照常给出。M4 的 `recompute_user` 只需
// `if evaluation.Incomplete { return }` 即可，不需要再判断任何别的状态。
func TestEvaluationIncompleteIsReportedForEventSuppression(t *testing.T) {
	rule := ruleDoc(verifiedLeaf)
	policy := mustPolicy(t, tiersJSON(rulesTierJSON(`{}`, rule)))
	evaluator := &fakeRuleEvaluator{outcomes: map[string]trustlevels.RuleOutcome{
		rule: {Incomplete: true, Problems: []error{errors.New("未声明 factKey")}},
	}}
	resolver := trustlevels.NewResolver(
		stubActivityReader{activity: trustlevels.Activity{}},
		stubPolicySource{policy: policy},
	).WithRuleEvaluators(func(context.Context, int64) (trustlevels.RuleEvaluator, error) { return evaluator, nil })

	evaluation, err := resolver.Evaluate(context.Background(), 7, identity.UserKindHuman, time.Now().Add(-365*24*time.Hour))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// 模拟 M4 job 的唯一判断：incomplete → 不比较、不发事件。
	emit := !evaluation.Incomplete
	if emit {
		t.Fatal("incomplete=true 时 job 侧不得发 trust_level.changed（否则 DB 抖动会造成事件洪水）")
	}
}

// TestEvaluateActivityFailureIsFatal 钉住「统计读失败 → 整次未解析（与今天一致）」。
func TestEvaluateActivityFailureIsFatal(t *testing.T) {
	resolver := trustlevels.NewResolver(
		stubActivityReader{err: errors.New("timeout")},
		stubPolicySource{policy: trustlevels.DefaultPolicy()},
	)
	if _, err := resolver.Evaluate(context.Background(), 7, identity.UserKindHuman, time.Now()); err == nil {
		t.Fatal("统计读失败必须返回 error，让装载方把 Actor 标成未解析")
	}
}

// TestEvaluateBotsStayNewcomer 钉住机器人不进判定（D25）。
func TestEvaluateBotsStayNewcomer(t *testing.T) {
	resolver := trustlevels.NewResolver(
		stubActivityReader{activity: trustlevels.Activity{DaysVisited: 10000}},
		stubPolicySource{policy: trustlevels.DefaultPolicy()},
	)
	evaluation, err := resolver.Evaluate(context.Background(), 7, identity.UserKindBot, time.Now())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if evaluation.Level != trustlevels.LevelNewcomer || evaluation.TierID != trustlevels.NewcomerID {
		t.Fatalf("机器人应为 TL0，得到 %+v", evaluation)
	}
	if len(evaluation.Errors) != 0 || evaluation.Incomplete {
		t.Fatalf("机器人不该产生规则问题：%+v", evaluation)
	}
}

// TestNextTierConditionsOnlyForFlatAll 钉住公开面句子的可见条件。
func TestNextTierConditionsOnlyForFlatAll(t *testing.T) {
	flat := ruleDoc(`{"op":"all","children":[` + verifiedLeaf + `]}`)
	sentences := []trustlevels.TierCondition{{Satisfied: false, Text: trustlevels.LocalizedName{Zh: "邮箱已验证", En: "email verified"}}}

	newResolver := func(evaluator trustlevels.RuleEvaluator) *trustlevels.Resolver {
		return trustlevels.NewResolver(stubActivityReader{}, stubPolicySource{policy: trustlevels.DefaultPolicy()}).
			WithRuleEvaluators(func(context.Context, int64) (trustlevels.RuleEvaluator, error) { return evaluator, nil })
	}

	rule := flat
	policy := mustPolicy(t, tiersJSON(rulesTierJSON(`{}`, ""), rulesTierJSON(`{}`, rule)))
	if got := newResolver(&fakeRuleEvaluator{leaves: sentences}).NextTierConditions(context.Background(), 7, policy, 1); len(got) != 1 {
		t.Fatalf("扁平 all 应返回逐条句子，得到 %d 条", len(got))
	}

	// 已满级 / 无规则 / 无求值器：三种情况都不给句子。
	if got := newResolver(&fakeRuleEvaluator{leaves: sentences}).NextTierConditions(context.Background(), 7, policy, 2); len(got) != 0 {
		t.Fatalf("已满级不该有句子，得到 %d 条", len(got))
	}
	plain := mustPolicy(t, tiersJSON(rulesTierJSON(`{}`, ""), rulesTierJSON(`{}`, "")))
	if got := newResolver(&fakeRuleEvaluator{leaves: sentences}).NextTierConditions(context.Background(), 7, plain, 1); len(got) != 0 {
		t.Fatalf("下一档没有规则时不该有句子，得到 %d 条", len(got))
	}
	var nilResolver *trustlevels.Resolver
	if got := nilResolver.NextTierConditions(context.Background(), 7, policy, 1); len(got) != 0 {
		t.Fatalf("nil resolver 不该 panic 也不该返回句子")
	}
}

// TestRuleOfTracksActiveRanks 钉住 rank → 规则 的折算（停用档不占 rank）。
func TestRuleOfTracksActiveRanks(t *testing.T) {
	rule := ruleDoc(verifiedLeaf)
	retired := `{"id":"tier_9","name":{"zh":"停用","en":"Retired"},"icon":"crown","requirements":{},"limits":{},"earnable":[],"retired":true,"rule":` + rule + `}`
	policy := mustPolicy(t, tiersJSON(
		rulesTierJSON(`{}`, ""),
		retired,
		rulesTierJSON(`{}`, rule),
	))
	if got := policy.RuleOf(2); string(got) != rule {
		t.Fatalf("TL2 的规则取错：%q", string(got))
	}
	if got := policy.RuleOf(1); len(got) != 0 {
		t.Fatalf("TL1 没有规则却取到了 %q", string(got))
	}
	if got := policy.RuleOf(trustlevels.LevelNewcomer); len(got) != 0 {
		t.Fatal("TL0 不该有规则")
	}
	if !policy.HasRules() {
		t.Fatal("HasRules 应为 true")
	}
	if trustlevels.DefaultPolicy().HasRules() {
		t.Fatal("默认策略不该有规则")
	}
}

// TestConditionsEvaluatorRequiresResolver 钉住 nil 安全（未装配时不 panic）。
func TestConditionsEvaluatorRequiresResolver(t *testing.T) {
	if trustlevels.NewRuleEvaluator(nil) != nil {
		t.Fatal("NewRuleEvaluator(nil) 应返回 nil，调用方据此走 fail-closed 分支")
	}
}

var _ = json.Marshal // 保持 encoding/json 导入（json 在 fake 签名里用到）
