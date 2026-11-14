package badges

import (
	"context"
	"errors"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// factVariantRule 构造一条 `fact.count` 规则（可选 variant）。
func factVariantRule(factKey string, variant string) ruleengine.Node {
	params := map[string]any{
		"factKey": factKey,
		"window":  map[string]any{"kind": "all"},
	}
	if variant != "" {
		params["variant"] = variant
	}
	cmp := ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 1}
	return ruleengine.Node{Type: conditions.KeyFactCount, Params: params, Cmp: &cmp}
}

func variantsFixture() []conditions.FactCandidate {
	return []conditions.FactCandidate{{
		Key:      "acme.wiki.pages",
		Variants: []conditions.FactVariant{{Value: "tutorial"}, {Value: "profile"}},
	}}
}

// TestValidateBadgeRuleAcceptsDeclaredVariant 断言合法 variant 能保存。
//
// 这是 M3 的原始故障回归：`variant` 是 `ParamEnum + Options:nil`，引擎侧曾把它当
// 空集合 → 任何带 variant 的规则 100% 422。引擎放开后，域层必须接管成员校验。
func TestValidateBadgeRuleAcceptsDeclaredVariant(t *testing.T) {
	store := NewPostgresStore(nil)
	store.SetFactCandidates(func() []conditions.FactCandidate { return variantsFixture() })
	if err := store.ValidateBadgeRule(context.Background(), 7, factVariantRule("acme.wiki.pages", "tutorial")); err != nil {
		t.Fatalf("声明过的 variant 必须能保存，得到 %v", err)
	}
	// 省略 variant = 汇总所有变体，同样合法。
	if err := store.ValidateBadgeRule(context.Background(), 7, factVariantRule("acme.wiki.pages", "")); err != nil {
		t.Fatalf("省略 variant 必须能保存，得到 %v", err)
	}
}

// TestValidateBadgeRuleRejectsUndeclaredVariant 断言未声明的 variant 是 422。
//
// 变异自检：删掉 `rule_store.go` 里 `conditions.VariantDeclared` 那条判断，这条红。
func TestValidateBadgeRuleRejectsUndeclaredVariant(t *testing.T) {
	store := NewPostgresStore(nil)
	store.SetFactCandidates(func() []conditions.FactCandidate { return variantsFixture() })
	err := store.ValidateBadgeRule(context.Background(), 7, factVariantRule("acme.wiki.pages", "tutorials"))
	if !errors.Is(err, ErrBadgeRuleReferenceInvalid) {
		t.Fatalf("未声明 variant 必须映射成 reference_invalid，得到 %v", err)
	}
}

// TestValidateBadgeRuleAllowsRuleWhenExtensionGone 断言候选为空时**放行保存**。
//
// 扩展被卸载后运营仍然要能编辑/保存这条规则（规则是运营资产）；该条件在求值期
// 由 resolver 转 unknown，不授予但也绝不返 0。拦住保存会让规则变成只读死数据。
func TestValidateBadgeRuleAllowsRuleWhenExtensionGone(t *testing.T) {
	declared := NewPostgresStore(nil)
	declared.SetFactCandidates(func() []conditions.FactCandidate { return variantsFixture() })
	if err := declared.ValidateBadgeRule(context.Background(), 7, factVariantRule("gone.ext.thing", "whatever")); err != nil {
		t.Fatalf("未声明 factKey 必须放行保存，得到 %v", err)
	}

	empty := NewPostgresStore(nil)
	empty.SetFactCandidates(func() []conditions.FactCandidate { return []conditions.FactCandidate{} })
	if err := empty.ValidateBadgeRule(context.Background(), 7, factVariantRule("acme.wiki.pages", "tutorials")); err != nil {
		t.Fatalf("没有候选（全部卸载）时必须放行保存，得到 %v", err)
	}

	// 未装配 provider（nil）也不能因此拒规则。
	unwired := NewPostgresStore(nil)
	if err := unwired.ValidateBadgeRule(context.Background(), 7, factVariantRule("acme.wiki.pages", "tutorials")); err != nil {
		t.Fatalf("未装配候选来源时不得拒绝，得到 %v", err)
	}
}
