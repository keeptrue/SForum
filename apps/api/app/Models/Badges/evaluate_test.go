package badges

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// TestEvaluateGrantsAtThreshold 断言达到阈值即授予，并重算 top_badge_id。
func TestEvaluateGrantsAtThreshold(t *testing.T) {
	store, activity := seedCatalog(t)
	activity.counters[7] = ActivityCounters{UserID: 7, DaysVisited: 7, TopicCount: 1}
	evaluator := NewEvaluator(store, activity)

	granted, err := evaluator.EvaluateUser(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 2 {
		t.Fatalf("granted = %d, want 2 (常客 7/7 与初次发帖 1/1)", len(granted))
	}
	if _, ok := store.top[7]; !ok {
		t.Fatal("top_badge_id must be recomputed after a new grant")
	}
}

// TestEvaluateSkipsBelowThreshold 断言未达标不授予。
func TestEvaluateSkipsBelowThreshold(t *testing.T) {
	store, activity := seedCatalog(t)
	activity.counters[7] = ActivityCounters{UserID: 7, DaysVisited: 6}
	evaluator := NewEvaluator(store, activity)

	granted, err := evaluator.EvaluateUser(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 0 {
		t.Fatalf("granted = %d, want 0 (6/7 未达标)", len(granted))
	}
	if len(store.grants[7]) != 0 {
		t.Fatal("no grant row should be written")
	}
}

// TestEvaluateIsIdempotent 断言重复评估不重复授予（差集语义）。
func TestEvaluateIsIdempotent(t *testing.T) {
	store, activity := seedCatalog(t)
	activity.counters[7] = ActivityCounters{UserID: 7, DaysVisited: 7, TopicCount: 1}
	evaluator := NewEvaluator(store, activity)

	first, err := evaluator.EvaluateUser(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	second, err := evaluator.EvaluateUser(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || len(second) != 0 {
		t.Fatalf("first=%d second=%d, want 2 then 0", len(first), len(second))
	}
	if len(store.grants[7]) != 2 {
		t.Fatalf("grant rows = %d, want 2 (不得重复插入)", len(store.grants[7]))
	}
	if len(store.lastGrants) != 2 {
		t.Fatalf("grant records = %d, want 2 (重复评估不得再写一条记录)", len(store.lastGrants))
	}
}

// TestEvaluateIgnoresDisabledAndManualBadges 断言停用徽章不参与评估、手动徽章永不被自动授予。
func TestEvaluateIgnoresDisabledAndManualBadges(t *testing.T) {
	store, activity := seedCatalog(t)
	// 把停用徽章的门槛拉低到必中，确认它仍不授予。
	store.badges[3].Threshold = ptrInt64(1)
	activity.counters[7] = ActivityCounters{UserID: 7, TopicCount: 100}
	evaluator := NewEvaluator(store, activity)

	granted, err := evaluator.EvaluateUser(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, badge := range granted {
		if badge.Slug == "disabled-badge" {
			t.Fatal("disabled badge must never be granted")
		}
		if badge.Slug == "quality-answer" {
			t.Fatal("manual badge must never be granted automatically")
		}
	}
}

// TestEvaluateNoActivityRowIsZero 断言没有 user_activity 行的用户按全 0 评估（懒创建）。
func TestEvaluateNoActivityRowIsZero(t *testing.T) {
	store, activity := seedCatalog(t)
	evaluator := NewEvaluator(store, activity)

	granted, err := evaluator.EvaluateUser(context.Background(), 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 0 {
		t.Fatalf("granted = %d, want 0", len(granted))
	}
}

// TestFastPathRuleRecognition 固定「什么形状能走快路径」。
//
// 快路径的存在理由是零回归：12 枚内置徽章的规则都是单 `activity.count` + `window.all`
// + `gte`，走它就不产生额外查询。多一个形状进快路径，都会让「快路径 = 内置徽章形状」
// 这条断言变得含糊。
func TestFastPathRuleRecognition(t *testing.T) {
	single := func(metric string, window map[string]any, op ruleengine.CompareOp, value int64) ruleengine.Node {
		return ruleengine.Node{
			Type:   conditions.KeyActivityCount,
			Params: map[string]any{"metric": metric, "window": window},
			Cmp:    &ruleengine.Compare{Op: op, Value: value},
		}
	}
	cases := []struct {
		name     string
		node     ruleengine.Node
		wantOK   bool
		wantMetr ActivityMetric
	}{
		{"内置形状", single("topic_count", map[string]any{"kind": "all"}, ruleengine.CompareGTE, 10), true, MetricTopicCount},
		{"窗口条件不快路径", single("topic_count", map[string]any{"kind": "recent_days", "days": 30}, ruleengine.CompareGTE, 10), false, ""},
		{"lte 不快路径", single("topic_count", map[string]any{"kind": "all"}, ruleengine.CompareLTE, 10), false, ""},
		{"value 0 不快路径", single("topic_count", map[string]any{"kind": "all"}, ruleengine.CompareGTE, 0), false, ""},
		{"未知指标不快路径", single("posts_read_count", map[string]any{"kind": "all"}, ruleengine.CompareGTE, 1), false, ""},
		{"组节点不快路径", ruleengine.Node{Op: ruleengine.OpAll, Children: []ruleengine.Node{
			single("topic_count", map[string]any{"kind": "all"}, ruleengine.CompareGTE, 1),
		}}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metric, value, ok := fastPathRule(tc.node)
			if ok != tc.wantOK {
				t.Fatalf("fastPathRule ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if metric != tc.wantMetr || value != tc.node.Cmp.Value {
				t.Fatalf("metric=%q value=%d", metric, value)
			}
		})
	}
}

// TestEvaluateAutomaticBadgeWithoutRuleIsNeverGranted 是 P-04 的反面保险：
// 自动徽章没有 rule（脏数据 / 手工改库）时判不满足，**绝不**回落到 metric/threshold。
// 若回落到旧两列，一条 metric 非空的脏徽章会重新变成「按旧阈值发」，掩盖 rule 丢失。
func TestEvaluateAutomaticBadgeWithoutRuleIsNeverGranted(t *testing.T) {
	store, activity := seedCatalog(t)
	store.badges = []Badge{{
		ID: 1, Slug: "legacy-only", Name: "Legacy", Tier: TierGold,
		GrantMode: GrantAutomatic, Enabled: true,
		Metric: metricPtr(MetricTopicCount), Threshold: ptrInt64(1),
	}}
	store.activityCounters[7] = ActivityCounters{TopicCount: 100}
	evaluator := NewEvaluator(store, activity)
	granted, err := evaluator.EvaluateWithCounters(context.Background(), 7, ActivityCounters{TopicCount: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 0 {
		t.Fatalf("没有 rule 的自动徽章不得授予，得到 %d 枚", len(granted))
	}
}

// TestEvaluateFastPathGrantsBuiltinShape 证明快路径仍然工作：规则命中 → 授予。
func TestEvaluateFastPathGrantsBuiltinShape(t *testing.T) {
	store, activity := seedCatalog(t)
	store.badges = []Badge{{
		ID: 2, Slug: "fast-path", Name: "Fast", Tier: TierGold,
		GrantMode: GrantAutomatic, Enabled: true,
		Rule: &ruleengine.Node{
			Type:   conditions.KeyActivityCount,
			Params: map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "all"}},
			Cmp:    &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 3},
		},
	}}
	store.activityCounters[8] = ActivityCounters{TopicCount: 3}
	evaluator := NewEvaluator(store, activity)
	granted, err := evaluator.EvaluateWithCounters(context.Background(), 8, ActivityCounters{TopicCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 1 || granted[0].Slug != "fast-path" {
		t.Fatalf("快路径应授予 1 枚，得到 %+v", granted)
	}
}

func metricPtr(metric ActivityMetric) *ActivityMetric { return &metric }
