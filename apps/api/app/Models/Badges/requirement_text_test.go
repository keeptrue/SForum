package badges

import (
	"regexp"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// TestBuildRequirementTextNeverLeaksAST 固定公开面文案的两条约束：
//  1. 运营填的优先，逐语言独立回落；
//  2. 生成的句子只含「指标名 + 阈值」，**不含任何内部词汇**（rule/cmp/params/op）。
func TestBuildRequirementTextNeverLeaksAST(t *testing.T) {
	single := &ruleengine.Node{
		Type:   "activity.count",
		Params: map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "all"}},
		Cmp:    &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 10},
	}

	generated := BuildRequirementText(Badge{Rule: single})
	if generated.Zh != "发布主题 ≥ 10" || generated.En != "topics published ≥ 10" {
		t.Fatalf("单条件规则应生成双语句子，得到 %+v", generated)
	}
	// 按**整词**判，不是子串：`topics published` 里含 "op" 但不是内部词汇。
	// 小审那条 M1.1-13 查的是响应里的 JSON 键（`"op"`/`"cmp"`），这里是同一判据的
	// 文案版本——用词边界避免把正常英文单词判成泄露。
	leak := regexp.MustCompile(`(?i)\b(rule|cmp|params|children|op|activity\.count)\b`)
	if match := leak.FindString(generated.Zh + " " + generated.En); match != "" {
		t.Fatalf("生成文案泄露了内部词汇 %q：%+v", match, generated)
	}

	// 运营填的中文优先，英文缺失时独立回落生成。
	mixed := BuildRequirementText(Badge{
		Rule:                 single,
		RequirementSummaryZh: "运营写的条件",
	})
	if mixed.Zh != "运营写的条件" || mixed.En != "topics published ≥ 10" {
		t.Fatalf("逐语言回落失败：%+v", mixed)
	}

	// 多条件规则：不生成（宁可空，也不拼内部结构）。
	group := &ruleengine.Node{Op: ruleengine.OpAll, Children: []ruleengine.Node{*single}}
	if text := BuildRequirementText(Badge{Rule: group}); text.Zh != "" || text.En != "" {
		t.Fatalf("多条件规则不应生成文案：%+v", text)
	}
	// 手工徽章：没有规则，只有运营文本。
	if text := BuildRequirementText(Badge{RequirementSummaryZh: "手动授予"}); text.Zh != "手动授予" || text.En != "" {
		t.Fatalf("手工徽章只出运营文本：%+v", text)
	}
}
