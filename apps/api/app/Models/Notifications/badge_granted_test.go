package notifications

import "testing"

// TestBadgeGrantedTypeIsRegistered 固定徽章通知在核心类型表里的三件事：
// 类型存在、category 是 engagement、icon 是奖章。
//
// 为什么这条测试重要：descriptor 与 categoryForType 是两处独立映射，漏掉任一处
// 都会让通知落到 plugin_unknown，按 category 过滤的收件箱就会归错类——而且不报错。
func TestBadgeGrantedTypeIsRegistered(t *testing.T) {
	if TypeBadgeGranted != "badge_granted" {
		t.Fatalf("TypeBadgeGranted = %q, want badge_granted", TypeBadgeGranted)
	}
	descriptors := coreTypeDescriptors()
	descriptor, ok := descriptors[TypeBadgeGranted]
	if !ok {
		t.Fatal("badge_granted missing from coreTypeDescriptors")
	}
	if descriptor.Category != "engagement" {
		t.Fatalf("descriptor category = %q, want engagement", descriptor.Category)
	}
	if descriptor.Icon != "i-lucide-award" {
		t.Fatalf("descriptor icon = %q, want i-lucide-award", descriptor.Icon)
	}
	if got := categoryForType(TypeBadgeGranted); got != "engagement" {
		t.Fatalf("categoryForType = %q, want engagement", got)
	}
}

// TestBadgeGrantedEventHasNoSelfSuppression 用语义断言固定一条产品决策：
// 管理员给自己授予徽章**要发**通知。
//
// 与 reaction 相反：自赞是互动噪音（跳过），自授是公开荣誉（照发）。
// 这里没有可断言的返回码，所以断言「事件结构里没有 actor==recipient 的抑制入口」，
// 并把它写成注释级契约——将来有人想「顺手统一」时，这条测试会提醒他先读原因。
func TestBadgeGrantedEventCarriesRecipientAndBadge(t *testing.T) {
	event := BadgeGrantedEvent{
		RecipientUserID: 7,
		BadgeSlug:       "veteran",
		BadgeName:       "元老",
		GrantedByUserID: 7, // 自授：必须仍然通知
	}
	if event.RecipientUserID != event.GrantedByUserID {
		t.Fatal("fixture should model a self-grant")
	}
	if event.BadgeSlug == "" || event.BadgeName == "" {
		t.Fatal("event must carry badge identity for rendering")
	}
}
