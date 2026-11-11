package badgescontroller

import (
	"encoding/json"
	"testing"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

func decodeBody(t *testing.T, raw string) badgeBody {
	t.Helper()
	var body badgeBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return body
}

func existingAutomaticBadge() badges.Badge {
	metric := badges.MetricDaysVisited
	threshold := int64(30)
	groupID := int64(2)
	return badges.Badge{
		ID: 3, Slug: "resident", Name: "老住户", Description: "累计访问 30 天",
		GroupID: &groupID, Tier: badges.TierSilver, Icon: "i-lucide-house",
		GrantMode: badges.GrantAutomatic, Metric: &metric, Threshold: &threshold,
		Enabled: true, Position: 1,
	}
}

// TestPatchEnabledOnlyKeepsOtherFields 是 2026-11-10 那个 422 的回归测试。
//
// 事故形态：badgeBody 的 Slug/Name 是非指针 string，PATCH 只发 {"enabled":false}
// 时它们留成空串，validateBadgeInput 判定 slug 为空 → 422。
// 后台「停用」开关只会发这一个字段，所以 PATCH 必须支持部分更新。
func TestPatchEnabledOnlyKeepsOtherFields(t *testing.T) {
	body := decodeBody(t, `{"enabled":false}`)
	input := body.mergeInto(existingAutomaticBadge())

	if input.Enabled {
		t.Fatal("enabled must be false")
	}
	if input.Slug != "resident" || input.Name != "老住户" || input.Tier != badges.TierSilver {
		t.Fatalf("identity fields must be preserved: %+v", input)
	}
	if input.Metric == nil || *input.Metric != badges.MetricDaysVisited || input.Threshold == nil || *input.Threshold != 30 {
		t.Fatalf("rule fields must be preserved: metric=%v threshold=%v", input.Metric, input.Threshold)
	}
}

// TestPatchNameOnlyKeepsEverythingElse 断言单字段更新不会冲掉其它字段。
func TestPatchNameOnlyKeepsEverythingElse(t *testing.T) {
	body := decodeBody(t, `{"name":"常住户"}`)
	input := body.mergeInto(existingAutomaticBadge())
	if input.Name != "常住户" || input.Slug != "resident" || !input.Enabled || input.Position != 1 {
		t.Fatalf("merge = %+v", input)
	}
	if input.Threshold == nil || *input.Threshold != 30 {
		t.Fatal("threshold must survive a rename")
	}
}

// TestPatchToManualDropsRuleFields 断言切到手动时规则作废。
//
// 不这么做会撞 validateBadgeInput 的「manual badge must not carry metric or threshold」，
// 表现为「切成手动就 422」——而后台开关一定会走这条路径。
func TestPatchToManualDropsRuleFields(t *testing.T) {
	body := decodeBody(t, `{"grantMode":"manual"}`)
	input := body.mergeInto(existingAutomaticBadge())
	if input.GrantMode != badges.GrantManual {
		t.Fatalf("grantMode = %q", input.GrantMode)
	}
	if input.Metric != nil || input.Threshold != nil {
		t.Fatalf("switching to manual must clear rule fields, got metric=%v threshold=%v", input.Metric, input.Threshold)
	}
}

// TestPatchExplicitNullClearsNullableField 断言显式 null 与字段缺失的区别。
func TestPatchExplicitNullClearsNullableField(t *testing.T) {
	cleared := decodeBody(t, `{"groupId":null}`)
	input := cleared.mergeInto(existingAutomaticBadge())
	if input.GroupID != nil {
		t.Fatalf("explicit null must clear groupId, got %v", *input.GroupID)
	}

	absent := decodeBody(t, `{"enabled":true}`)
	input = absent.mergeInto(existingAutomaticBadge())
	if input.GroupID == nil || *input.GroupID != 2 {
		t.Fatalf("absent field must keep groupId, got %v", input.GroupID)
	}
}

// TestCreateInputFillsRecommendedDefaults 断言创建路径仍要求 slug/name，
// 且缺省时用推荐默认值（enabled=true、默认图标）。
func TestCreateInputFillsRecommendedDefaults(t *testing.T) {
	body := decodeBody(t, `{"slug":"x","name":"X","tier":"gold","grantMode":"manual"}`)
	input := body.createInput()
	if !input.Enabled {
		t.Fatal("new badges default to enabled")
	}
	if input.Icon != "i-lucide-award" {
		t.Fatalf("icon default = %q", input.Icon)
	}
	// 创建时漏 slug 仍应被领域校验拦下（这里只确认空值不会被悄悄补成默认）。
	if empty := decodeBody(t, `{"name":"X"}`).createInput(); empty.Slug != "" {
		t.Fatalf("missing slug must stay empty so validation rejects it, got %q", empty.Slug)
	}
}

// TestGroupPatchIsPartialUpdate 断言分组 PATCH 与徽章 PATCH 语义一致。
//
// 事故形态（pm 发现）：badgeGroupBody 字段全是非指针，PATCH 只带 {"position":5}
// 时 description 会被写成 ""、position 之外的字段全被清零。
// 同一套 API 里两个 PATCH 语义不同是陷阱，而且这个陷阱会在「运营改个排序把
// 描述清空」时才暴露。
func TestGroupPatchIsPartialUpdate(t *testing.T) {
	current := badges.BadgeGroup{
		ID: 2, Key: "activity", Name: "活跃", Description: "持续回到社区", Position: 0,
	}

	var body badgeGroupBody
	if err := json.Unmarshal([]byte(`{"position":5}`), &body); err != nil {
		t.Fatal(err)
	}
	input := body.mergeInto(current)
	if input.Position != 5 {
		t.Fatalf("position = %d, want 5", input.Position)
	}
	if input.Description != "持续回到社区" || input.Key != "activity" || input.Name != "活跃" {
		t.Fatalf("group merge dropped fields: %+v", input)
	}

	// 显式给空串是「清空描述」，这是允许的（分组不需要 patchField）。
	var clear badgeGroupBody
	if err := json.Unmarshal([]byte(`{"description":""}`), &clear); err != nil {
		t.Fatal(err)
	}
	if got := clear.mergeInto(current); got.Description != "" {
		t.Fatalf("explicit empty description must clear, got %q", got.Description)
	}
}

// TestGroupCreateInputKeepsZeroValues 断言创建路径的默认值不被指针改写破坏。
func TestGroupCreateInputKeepsZeroValues(t *testing.T) {
	var body badgeGroupBody
	if err := json.Unmarshal([]byte(`{"key":"honor","name":"荣誉"}`), &body); err != nil {
		t.Fatal(err)
	}
	input := body.createInput()
	if input.Key != "honor" || input.Name != "荣誉" || input.Description != "" || input.Position != 0 {
		t.Fatalf("create input = %+v", input)
	}
}
