package badges

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
	"time"
)

func seedCatalog(t *testing.T) (*fakeStore, *fakeActivity) {
	t.Helper()
	store := newFakeStore()
	activity := newFakeActivity()
	groups := []BadgeGroup{
		{ID: 1, Key: "content", Name: "内容", Position: 1},
		{ID: 2, Key: "activity", Name: "活跃", Position: 0},
		{ID: 3, Key: "empty", Name: "空组", Position: 2},
	}
	store.groups = groups
	contentKey, activityKey := "content", "activity"
	seven, one, ten := int64(7), int64(1), int64(10)
	metricDays := MetricDaysVisited
	metricTopic := MetricTopicCount
	// 自 202611120002 起自动徽章的判定只认 rule；夹具按迁移生成的同款形状给上，
	// metric/threshold 保留（旧列仍在表里，但评估不再读它们）。
	store.badges = []Badge{
		{ID: 1, Slug: "regular-visitor", Name: "常客", GroupID: ptrInt64(2), GroupKey: &activityKey,
			Tier: TierBronze, Icon: "i-lucide-calendar", GrantMode: GrantAutomatic,
			Metric: &metricDays, Threshold: &seven, Rule: builtinRule(MetricDaysVisited, 7),
			Enabled: true, Position: 0},
		{ID: 2, Slug: "first-topic", Name: "初次发帖", GroupID: ptrInt64(1), GroupKey: &contentKey,
			Tier: TierBronze, Icon: "i-lucide-sprout", GrantMode: GrantAutomatic,
			Metric: &metricTopic, Threshold: &one, Rule: builtinRule(MetricTopicCount, 1),
			Enabled: true, Position: 0},
		{ID: 3, Slug: "quality-answer", Name: "优质解答", GroupID: ptrInt64(1), GroupKey: &contentKey,
			Tier: TierSilver, Icon: "i-lucide-circle-check-big", GrantMode: GrantManual,
			Enabled: true, Position: 1},
		{ID: 4, Slug: "disabled-badge", Name: "已停用", GroupKey: &contentKey,
			Tier: TierGold, Icon: "i-lucide-trophy", GrantMode: GrantAutomatic,
			Metric: &metricTopic, Threshold: &ten, Rule: builtinRule(MetricTopicCount, 10),
			Enabled: false, Position: 2},
		{ID: 5, Slug: "loose-badge", Name: "未分组", Tier: TierGold,
			Icon: "i-lucide-star", GrantMode: GrantManual, Enabled: true, Position: 3},
	}
	return store, activity
}

func ptrInt64(value int64) *int64 { return &value }

// builtinRule 生成迁移 202611120002 为内置自动徽章写入的同款规则：
// 单 `activity.count` 叶子 + window.all + gte。
func builtinRule(metric ActivityMetric, threshold int64) *ruleengine.Node {
	return &ruleengine.Node{
		Type: conditions.KeyActivityCount,
		Params: map[string]any{
			"metric": string(metric),
			"window": map[string]any{"kind": ruleengine.WindowKindAll},
		},
		Cmp: &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: threshold},
	}
}

// TestListUserBadgesSectionsFollowGroupPosition 断言分组顺序与未分组兜底。
func TestListUserBadgesSectionsFollowGroupPosition(t *testing.T) {
	store, activity := seedCatalog(t)
	activity.counters[7] = ActivityCounters{UserID: 7, DaysVisited: 3, TopicCount: 2}
	service := NewService(store, activity)

	sections, err := service.ListUserBadges(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 3 {
		t.Fatalf("sections = %d, want 3 (activity/content/未分组；空组必须被丢掉)", len(sections))
	}
	if sections[0].Key != "activity" || sections[1].Key != "content" {
		t.Fatalf("section order = %q,%q; want activity,content (按 position)", sections[0].Key, sections[1].Key)
	}
	if sections[2].Key != "" || sections[2].Name != "未分组" {
		t.Fatalf("last section = %q/%q, want ungrouped fallback", sections[2].Key, sections[2].Name)
	}
	// 停用徽章不得出现。
	for _, section := range sections {
		for _, item := range section.Badges {
			if item.Badge.Slug == "disabled-badge" {
				t.Fatal("disabled badge must not appear in the public wall")
			}
		}
	}
}

// TestListUserBadgesProgressUsesCounters 断言进度来自 user_activity 快照，
// 手动徽章没有阈值。
func TestListUserBadgesProgressUsesCounters(t *testing.T) {
	store, activity := seedCatalog(t)
	activity.counters[7] = ActivityCounters{UserID: 7, DaysVisited: 3, TopicCount: 2}
	service := NewService(store, activity)

	sections, err := service.ListUserBadges(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	bySlug := map[string]UserBadge{}
	for _, section := range sections {
		for _, item := range section.Badges {
			bySlug[item.Badge.Slug] = item
		}
	}
	regular := bySlug["regular-visitor"]
	if regular.Progress.Current != 3 || regular.Progress.Threshold != 7 {
		t.Fatalf("regular-visitor progress = %+v, want 3/7", regular.Progress)
	}
	if regular.Progress.Done() {
		t.Fatal("3/7 must not be done")
	}
	firstTopic := bySlug["first-topic"]
	if !firstTopic.Progress.Done() {
		t.Fatalf("first-topic 2/1 must be done, got %+v", firstTopic.Progress)
	}
	manual := bySlug["quality-answer"]
	if manual.Progress.Threshold != 0 || manual.Progress.Done() {
		t.Fatalf("manual badge progress = %+v, want zero threshold", manual.Progress)
	}
}

// TestPublicBadgeDropsAdminFields 断言公开结构不含运营与审计字段。
func TestPublicBadgeDropsAdminFields(t *testing.T) {
	badge := Badge{
		ID: 1, Slug: "x", Name: "X", Tier: TierGold, Icon: "i-lucide-star",
		GrantMode: GrantManual, Enabled: true, GrantedCount: 9, Position: 2,
	}
	public := badge.Public()
	if public.Slug != "x" || public.Position != 2 {
		t.Fatalf("public badge lost fields: %+v", public)
	}
	// 结构体层面就不存在这些字段，编译期即保证；这里固定「不该有」的字段不会被误加回来。
	// （用反射逐字段核对会在字段重命名时产生噪音，故只核对取值来源。）
	if public.GroupKey != nil {
		t.Fatal("public badge must not carry group name/id")
	}
}

// TestTopBadgesForReturnsEnabledOnly 断言停用徽章不作为 top badge 展示。
func TestTopBadgesForReturnsEnabledOnly(t *testing.T) {
	store, activity := seedCatalog(t)
	service := NewService(store, activity)
	store.grants[7] = map[int64]time.Time{4: {}}
	store.top[7] = 4 // 指向停用徽章

	refs, err := service.TopBadgesFor(context.Background(), []int64{7})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := refs[7]; ok {
		t.Fatal("disabled badge must not be exposed as top badge")
	}
}

// TestRecomputeTopBadgePrefersGoldThenPosition 断言排序规则：
// gold > silver > bronze，同级 position 升序，再 granted_at 降序。
func TestRecomputeTopBadgePrefersGoldThenPosition(t *testing.T) {
	store := newFakeStore()
	store.badges = []Badge{
		{ID: 1, Slug: "bronze", Tier: TierBronze, Position: 0, Enabled: true},
		{ID: 2, Slug: "gold-late", Tier: TierGold, Position: 5, Enabled: true},
		{ID: 3, Slug: "gold-early", Tier: TierGold, Position: 2, Enabled: true},
	}
	store.grants[7] = map[int64]time.Time{}
	store.grantOrder[7] = map[int64]int{}
	store.insertGrant(7, 1, 0, "")
	store.insertGrant(7, 2, 0, "")
	store.insertGrant(7, 3, 0, "")

	if err := store.RecomputeTopBadge(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if got := store.top[7]; got != 3 {
		t.Fatalf("top badge = %d, want 3 (gold 内 position 更小的)", got)
	}

	// 同级同 position 时按 granted_at 降序：后授予的赢。
	store.badges = append(store.badges, Badge{ID: 4, Slug: "gold-same", Tier: TierGold, Position: 2, Enabled: true})
	store.insertGrant(7, 4, 0, "")
	if err := store.RecomputeTopBadge(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if got := store.top[7]; got != 4 {
		t.Fatalf("top badge = %d, want 4 (同级同 position 取最近授予)", got)
	}
}

// TestRecomputeTopBadgeClearsWhenAllDisabled 断言没有可展示徽章时落成 NULL。
func TestRecomputeTopBadgeClearsWhenAllDisabled(t *testing.T) {
	store := newFakeStore()
	store.badges = []Badge{{ID: 1, Slug: "x", Tier: TierGold, Enabled: false}}
	store.grants[7] = map[int64]time.Time{}
	store.grantOrder[7] = map[int64]int{}
	store.insertGrant(7, 1, 0, "")
	store.top[7] = 1

	if err := store.RecomputeTopBadge(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.top[7]; ok {
		t.Fatal("top badge must be cleared when every granted badge is disabled")
	}
}
