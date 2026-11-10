package notifications

import "testing"

// TestBookmarkReminderCategoryMatchesDescriptor 固定「类型目录声明的 category」与
// 「投影行实际写入的 category」一致；分叉会让收藏提醒落到 plugin_unknown，按
// category 过滤的收件箱就会归错类。
func TestBookmarkReminderCategoryMatchesDescriptor(t *testing.T) {
	descriptor, ok := coreTypeDescriptors()[TypeBookmarkReminder]
	if !ok {
		t.Fatalf("bookmark_reminder descriptor is not registered")
	}
	if descriptor.Category != "engagement" {
		t.Fatalf("bookmark_reminder descriptor category=%q want engagement", descriptor.Category)
	}
	if descriptor.Icon != "i-lucide-bookmark" {
		t.Fatalf("bookmark_reminder descriptor icon=%q", descriptor.Icon)
	}
	if got := categoryForType(TypeBookmarkReminder); got != descriptor.Category {
		t.Fatalf("categoryForType(bookmark_reminder)=%q want %q", got, descriptor.Category)
	}
	if TypeBookmarkReminder != "bookmark_reminder" {
		t.Fatalf("notification type drifted: %q", TypeBookmarkReminder)
	}
}
