package notifications

import "testing"

// TestReactionNotificationCategoryMatchesDescriptor 固定「类型目录声明的 category」
// 与「投影行实际写入的 category」一致；两者分叉会让按 category 过滤的收件箱把
// 点赞通知归到 plugin_unknown。
func TestReactionNotificationCategoryMatchesDescriptor(t *testing.T) {
	descriptor, ok := coreTypeDescriptors()[TypeReaction]
	if !ok {
		t.Fatalf("reaction descriptor is not registered")
	}
	if descriptor.Category != "engagement" {
		t.Fatalf("reaction descriptor category=%q want engagement", descriptor.Category)
	}
	if got := categoryForType(TypeReaction); got != descriptor.Category {
		t.Fatalf("categoryForType(reaction)=%q want %q", got, descriptor.Category)
	}
}
