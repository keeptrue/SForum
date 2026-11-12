package badges

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// M3 语义红线的可复跑证据：插件事实 key 失效 → 进健康清单；规则不被静默改；
// 重新声明后自动恢复且不需要任何回填 job。
//
// 这三件事以前只有实现（`rule_health.go` 的 IssueMissingFactKey 分支）没有测试：
// `rg missing_fact_key` 只命中常量定义。红线没有证明就等于没有。

const factCountRuleJSON = `{"v":1,"root":{"type":"fact.count","params":{"factKey":"acme.wiki.pages","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}`

func decodeRule(t *testing.T, raw string) ruleengine.Node {
	t.Helper()
	node, err := ruleengine.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("decode rule: %v", err)
	}
	return node
}

func insertAutomaticBadgeWithRule(t *testing.T, fixture *badgesPGFixture, slug, name, rule string) int64 {
	t.Helper()
	var id int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, rule, enabled, position)
		VALUES ($1,$2,'gold','automatic',$3::jsonb,TRUE,0) RETURNING id`,
		slug, name, rule).Scan(&id); err != nil {
		t.Fatalf("insert badge with rule: %v", err)
	}
	return id
}

func badgeRuleSnapshot(t *testing.T, fixture *badgesPGFixture, badgeID int64) (string, string) {
	t.Helper()
	var ruleText string
	var updatedAt string
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT rule::text, updated_at::text FROM badges WHERE id = $1`, badgeID).Scan(&ruleText, &updatedAt); err != nil {
		t.Fatalf("snapshot rule: %v", err)
	}
	return ruleText, updatedAt
}

func factKeyIssues(t *testing.T, report []BadgeRuleHealth, badgeID int64) []RuleIssue {
	t.Helper()
	for _, item := range report {
		if item.BadgeID != badgeID {
			continue
		}
		issues := make([]RuleIssue, 0)
		for _, issue := range item.Issues {
			if issue.Kind == IssueMissingFactKey {
				issues = append(issues, issue)
			}
		}
		return issues
	}
	return nil
}

// TestRuleHealthReportsMissingFactKeyAndRestores 覆盖验收 1..3。
func TestRuleHealthReportsMissingFactKeyAndRestores(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	badgeID := insertAutomaticBadgeWithRule(t, fixture, "plugin-pages", "插件页面", factCountRuleJSON)
	store := NewPostgresStore(fixture.pool)
	ctx := context.Background()

	ruleBefore, updatedBefore := badgeRuleSnapshot(t, fixture, badgeID)

	// 1) 扩展卸载/停用：声明集合为空 → 失效引用进健康清单。
	store.SetFactKeyProvider(func() []string { return []string{} })
	report, err := store.RuleHealth(ctx)
	if err != nil {
		t.Fatalf("RuleHealth: %v", err)
	}
	issues := factKeyIssues(t, report, badgeID)
	if len(issues) != 1 {
		t.Fatalf("必须恰好一条 missing_fact_key，得到 %+v", report)
	}
	if issues[0].Kind != IssueMissingFactKey {
		t.Fatalf("kind = %q", issues[0].Kind)
	}
	if want := "root.params.factKey"; issues[0].Path != want {
		t.Fatalf("path = %q，want %q（前端据此定位到 factKey 输入）", issues[0].Path, want)
	}
	if issues[0].Ref != "acme.wiki.pages" {
		t.Fatalf("ref 必须逐字等于 factKey，得到 %q", issues[0].Ref)
	}
	if issues[0].Label == "" {
		t.Fatal("label 必须给运营可读文案（当前实现是「插件事实 <key>」）")
	}

	// 2) 不静默删/改规则：JSONB 原文逐字节相等 + updated_at 不变（双证）。
	ruleAfter, updatedAfter := badgeRuleSnapshot(t, fixture, badgeID)
	if ruleAfter != ruleBefore {
		t.Fatalf("健康检查改写了规则：\nbefore %s\nafter  %s", ruleBefore, ruleAfter)
	}
	if updatedAfter != updatedBefore {
		t.Fatalf("健康检查触发了 UPDATE：updated_at %s → %s", updatedBefore, updatedAfter)
	}

	// 3) 重新声明（同一 key，契约版本升到 @2）→ 同一条规则不再报失效。
	store.SetFactKeyProvider(func() []string { return []string{"acme.wiki.pages"} })
	restored, err := store.RuleHealth(ctx)
	if err != nil {
		t.Fatalf("RuleHealth after reinstall: %v", err)
	}
	if remaining := factKeyIssues(t, restored, badgeID); len(remaining) != 0 {
		t.Fatalf("重装后不应再有 missing_fact_key，得到 %+v", remaining)
	}
	// 恢复完全靠「实时合并候选」，不需要回填 job：这里没跑任何 job，规则也必须原样。
	ruleRestored, updatedRestored := badgeRuleSnapshot(t, fixture, badgeID)
	if ruleRestored != ruleBefore || updatedRestored != updatedBefore {
		t.Fatalf("恢复过程改动了规则/触发了写入：%s / %s", ruleRestored, updatedRestored)
	}
}

// TestEstimateRuleImpactNamesConditionTypeNotFactKey 覆盖验收 4。
//
// `unsupportedConditions` 只回 **条件类型**（`fact.count`），不回 factKey：
// 前者是稳定枚举，后者是插件字符串——把插件 key 塞进这个数组会让前端把它当条件名渲染，
// 而且长度/形态完全不可控。
func TestEstimateRuleImpactNamesConditionTypeNotFactKey(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := context.Background()

	onlyFact := decodeRule(t, `{"v":1,"root":{"type":"fact.count","params":{"factKey":"acme.wiki.pages","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}`)
	impact, err := store.EstimateRuleImpact(ctx, onlyFact)
	if err != nil {
		t.Fatalf("EstimateRuleImpact: %v", err)
	}
	if impact.Method != impactMethodUnsupported {
		t.Fatalf("method = %q，want %q", impact.Method, impactMethodUnsupported)
	}
	if len(impact.UnsupportedConditions) != 1 || impact.UnsupportedConditions[0] != "fact.count" {
		t.Fatalf("unsupportedConditions 必须恰好是 [fact.count]，得到 %v", impact.UnsupportedConditions)
	}
	if impact.Estimated != nil {
		t.Fatalf("不可估时 estimated 必须是 null（不是 0），得到 %v", *impact.Estimated)
	}

	// AND 里部分叶子不可估：仍给上界 + partial 标记，不可估项同样只报类型。
	mixed := decodeRule(t, `{"v":1,"root":{"op":"all","children":[`+
		`{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}},`+
		`{"type":"fact.count","params":{"factKey":"acme.wiki.pages","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}]}}`)
	partial, err := store.EstimateRuleImpact(ctx, mixed)
	if err != nil {
		t.Fatalf("EstimateRuleImpact(mixed): %v", err)
	}
	if partial.Method != impactMethodStrictestLeafPartial || !partial.Partial {
		t.Fatalf("method = %q partial = %v，want %q/true", partial.Method, partial.Partial, impactMethodStrictestLeafPartial)
	}
	if len(partial.UnsupportedConditions) != 1 || partial.UnsupportedConditions[0] != "fact.count" {
		t.Fatalf("partial 的 unsupportedConditions 也必须是 [fact.count]，得到 %v", partial.UnsupportedConditions)
	}
}
