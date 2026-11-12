package badgescontroller

import (
	"encoding/json"
	"reflect"
	"testing"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	ruleengine "github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 后台徽章行（adminBadgeResponse）的 wire 形状测试。
//
// 这一层是编辑器读「现值」的唯一入口：`rule` 丢了下发，前端打开既有自动徽章
// 会显示「规则缺失」；`requirementSummary` 丢了，后续编辑的**整体替换语义**
// 会拿空串覆盖掉运营已经填好的文案。两者都不会在领域测试里暴露，因为领域层
// 一直读得到（见 Models/Badges/postgres_store.go 的 badgeColumnsSQL）。
//
// 全部是**值级**断言：只断言键存在的话，`rule: null` 与 `rule: {...}` 无法区分。

const (
	ruleRawTenTopics = `{"v":1,"root":{"type":"activity.topic_count","cmp":{"op":"gte","value":5}}}`
	// 形状合法 JSON、条件名未知：领域层解码失败（Rule=nil）但 RuleRaw 保留，
	// 正是 rule_health 里 undecodable_rule 那一类。
	ruleRawUndecodable = `{"v":1,"root":{"type":"nope.condition"}}`
)

func adminBadgeWire(t *testing.T, badge badges.Badge) map[string]any {
	t.Helper()
	body, err := json.Marshal(toAdminBadge(badge))
	if err != nil {
		t.Fatalf("marshal admin badge: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode admin badge: %v (%s)", err, body)
	}
	return wire
}

func assertRuleObject(t *testing.T, wire map[string]any) {
	t.Helper()
	rule, ok := wire["rule"].(map[string]any)
	if !ok {
		t.Fatalf("rule must be a JSON object, got %v (%T)", wire["rule"], wire["rule"])
	}
	version, ok := rule["v"].(float64)
	if !ok || version != 1 {
		t.Fatalf("rule.v: want 1, got %v", rule["v"])
	}
	root, ok := rule["root"].(map[string]any)
	if !ok || len(root) == 0 {
		t.Fatalf("rule.root must be a non-empty object, got %v", rule["root"])
	}
}

// 自动徽章：rule 透出 RuleRaw 原文，requirementSummary 与领域值逐字相等。
func TestAdminBadgeEmitsRuleRawVerbatim(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		// node 非 nil 表示解码成功；nil 表示脏规则（RuleRaw 仍在）。
		node *ruleengine.Node
	}{
		{name: "decoded", raw: ruleRawTenTopics, node: &ruleengine.Node{Type: "activity.topic_count"}},
		{name: "undecodable", raw: ruleRawUndecodable, node: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			badge := badges.Badge{
				ID: 7, Slug: "ten-topics", Name: "十帖", Tier: badges.TierGold,
				GrantMode: badges.GrantAutomatic, Enabled: true,
				Rule:                 tc.node,
				RuleRaw:              json.RawMessage(tc.raw),
				RequirementSummaryZh: "发满五帖",
				RequirementSummaryEn: "Publish five topics",
			}
			wire := adminBadgeWire(t, badge)

			assertKeys(t, "admin badge", wire, []string{
				"id", "slug", "name", "description", "groupId", "groupName",
				"tier", "tierName", "tierColor", "icon", "grantMode", "metric",
				"threshold", "rule", "requirementSummary", "enabled", "position",
				"grantedCount", "createdAt", "updatedAt",
			})

			assertRuleObject(t, wire)

			// 原文透出：解码器不认识的键也要在，否则编辑器「打开再保存」会静默改写规则。
			var want any
			if err := json.Unmarshal([]byte(tc.raw), &want); err != nil {
				t.Fatalf("decode fixture rule: %v", err)
			}
			if !reflect.DeepEqual(wire["rule"], want) {
				t.Fatalf("rule must be透出原文:\nwant %s\ngot  %v", tc.raw, wire["rule"])
			}

			summary, ok := wire["requirementSummary"].(map[string]any)
			if !ok {
				t.Fatalf("requirementSummary must be an object, got %v", wire["requirementSummary"])
			}
			if summary["zh"] != "发满五帖" || summary["en"] != "Publish five topics" {
				t.Fatalf("requirementSummary must be domain values verbatim, got %v", summary)
			}
		})
	}
}

// 手写徽章：rule 键必须在、值必须是 null；requirementSummary 两个键也在（无 omitempty）。
func TestAdminBadgeEmitsNullRuleForManualBadge(t *testing.T) {
	wire := adminBadgeWire(t, badges.Badge{
		ID: 8, Slug: "founder", Name: "创始人", Tier: badges.TierBronze,
		GrantMode: badges.GrantManual, Enabled: true,
	})

	value, present := wire["rule"]
	if !present {
		t.Fatal("manual badge must still carry the rule key (value null)")
	}
	if value != nil {
		t.Fatalf("manual badge rule must be null, got %v", value)
	}

	summary, ok := wire["requirementSummary"].(map[string]any)
	if !ok {
		t.Fatalf("requirementSummary must be an object even when empty, got %v", wire["requirementSummary"])
	}
	if zh, present := summary["zh"]; !present || zh != "" {
		t.Fatalf("requirementSummary.zh must be present with empty string, got %v", summary)
	}
	if en, present := summary["en"]; !present || en != "" {
		t.Fatalf("requirementSummary.en must be present with empty string, got %v", summary)
	}
}
