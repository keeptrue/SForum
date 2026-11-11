package badges

import (
	"context"
	"testing"
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

// TestRuleSatisfiedRejectsDirtyRules 断言脏规则（缺 metric/threshold）不达标而不是崩溃。
func TestRuleSatisfiedRejectsDirtyRules(t *testing.T) {
	counters := ActivityCounters{TopicCount: 100}
	dirty := Badge{ID: 1, GrantMode: GrantAutomatic, Enabled: true}
	if ruleSatisfied(dirty, counters) {
		t.Fatal("rule without metric/threshold must not be satisfied")
	}
	zero := Badge{ID: 2, GrantMode: GrantAutomatic, Enabled: true,
		Metric: metricPtr(MetricTopicCount), Threshold: ptrInt64(0)}
	if ruleSatisfied(zero, counters) {
		t.Fatal("threshold 0 must not be satisfied")
	}
	unknown := Badge{ID: 3, GrantMode: GrantAutomatic, Enabled: true,
		Metric: metricPtr("posts_read_count"), Threshold: ptrInt64(1)}
	if ruleSatisfied(unknown, counters) {
		t.Fatal("non-whitelisted metric must not be satisfied")
	}
}

func metricPtr(metric ActivityMetric) *ActivityMetric { return &metric }
