package badges

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// measureForRule 的边界：只有「单叶 + count 型 + gte + value>0」可测。
//
// 每一条拒绝都对应一个真实会误导用户的情况：lte 是「不超过」、布尔没有数值、
// 多条件组无法用一个进度条表达。它们必须返回 nil（无进度），而不是 0/0。
func TestMeasureForRuleOnlyAcceptsSingleCountGTE(t *testing.T) {
	params := map[string]any{
		"metric": "topic_count",
		"window": map[string]any{"kind": ruleengine.WindowKindAll},
	}
	countLeaf := func(op ruleengine.CompareOp, value int64) *ruleengine.Node {
		return &ruleengine.Node{
			Type: conditions.KeyActivityCount, Params: params,
			Cmp: &ruleengine.Compare{Op: op, Value: value},
		}
	}
	booleanLeaf := &ruleengine.Node{Type: conditions.KeyIdentityEmailVerify}
	group := func(op ruleengine.GroupOp, children ...ruleengine.Node) *ruleengine.Node {
		return &ruleengine.Node{Op: op, Children: children}
	}

	cases := []struct {
		name string
		rule *ruleengine.Node
		want bool
	}{
		{"gte count", countLeaf(ruleengine.CompareGTE, 3), true},
		{"lte count", countLeaf(ruleengine.CompareLTE, 3), false},
		{"gt count", countLeaf(ruleengine.CompareGT, 3), false},
		{"lt count", countLeaf(ruleengine.CompareLT, 3), false},
		{"eq count", countLeaf(ruleengine.CompareEQ, 3), false},
		{"gte zero", countLeaf(ruleengine.CompareGTE, 0), false},
		{"gte negative", countLeaf(ruleengine.CompareGTE, -1), false},
		{"boolean leaf", booleanLeaf, false},
		{"all group of two", group(ruleengine.OpAll, *countLeaf(ruleengine.CompareGTE, 1), *countLeaf(ruleengine.CompareGTE, 2)), false},
		{"all group of one", group(ruleengine.OpAll, *countLeaf(ruleengine.CompareGTE, 1)), false},
		{"any group", group(ruleengine.OpAny, *countLeaf(ruleengine.CompareGTE, 1)), false},
		{"not group", group(ruleengine.OpNot, *countLeaf(ruleengine.CompareGTE, 1)), false},
		{"unknown condition", &ruleengine.Node{
			Type: "plugin.unknown", Cmp: &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 1},
		}, false},
		{"nil rule", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := measureForRule(tc.rule)
			if tc.want {
				if got == nil {
					t.Fatalf("want a measure, got nil")
				}
				if got.Threshold != tc.rule.Cmp.Value {
					t.Fatalf("threshold = %d, want %d", got.Threshold, tc.rule.Cmp.Value)
				}
				if got.Leaf.Type != tc.rule.Type {
					t.Fatalf("leaf type = %q, want %q", got.Leaf.Type, tc.rule.Type)
				}
				return
			}
			if got != nil {
				t.Fatalf("want no measure, got %+v", got)
			}
		})
	}
}

// Public() 从规则派生 Measure；legacy 两列即使是数字也不参与。
func TestBadgePublicDerivesMeasureFromRuleOnly(t *testing.T) {
	threshold := int64(7)
	metric := MetricTopicCount

	// 新规则徽章：metric/threshold 为 NULL，只有 rule。
	ruleOnly := Badge{
		Slug: "ten-topics", GrantMode: GrantAutomatic,
		Rule: builtinRule(MetricTopicCount, 10),
	}
	public := ruleOnly.Public(conditions.NewRefNames())
	if public.Measure == nil || public.Measure.Threshold != 10 {
		t.Fatalf("rule-only badge measure = %+v, want threshold 10", public.Measure)
	}
	if public.Measure.Leaf.Type != conditions.KeyActivityCount {
		t.Fatalf("leaf type = %q, want %q", public.Measure.Leaf.Type, conditions.KeyActivityCount)
	}

	// legacy 遗留：有 metric/threshold 但没有规则 → 没有可测的东西。
	legacyOnly := Badge{
		Slug: "legacy", GrantMode: GrantAutomatic,
		Metric: &metric, Threshold: &threshold,
	}
	if got := legacyOnly.Public(conditions.NewRefNames()).Measure; got != nil {
		t.Fatalf("legacy metric/threshold must not produce a measure, got %+v", got)
	}
}

// Measure 是内部字段：json:"-" 必须让它从领域 wire 里彻底消失。
//
// 这条防止「有人把 tag 改成 measure」把规则结构（leaf/params/threshold）
// 顺手带进公开响应。
func TestPublicBadgeMeasureNeverSerializes(t *testing.T) {
	badge := Badge{Slug: "ten-topics", GrantMode: GrantAutomatic, Rule: builtinRule(MetricTopicCount, 10)}
	raw, err := json.Marshal(badge.Public(conditions.NewRefNames()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lowered := strings.ToLower(string(raw))
	for _, forbidden := range []string{"measure", "leaf", "threshold\":10"} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("public badge wire must not contain %q: %s", forbidden, raw)
		}
	}
}
