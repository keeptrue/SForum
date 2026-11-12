package badgescontroller

import (
	"encoding/json"
	"testing"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// M3 红线：公开面的进度形状**不变**。
//
// 值级断言的理由：`{"current":0,"threshold":3}` 与 `{"current":0,"threshold":3,"mode":"..."}`
// 的差别只有值级比较能抓；只断言「键存在」会让新增的 `mode`/`satisfied`/`total`
// 一路飘过去，而前端按 `mode` 分支渲染的代码根本还没写。

// TestBadgeProgressWireStaysTwoNumericKeys 断言领域 wire 就是两个数字键。
func TestBadgeProgressWireStaysTwoNumericKeys(t *testing.T) {
	raw, err := json.Marshal(badges.UserBadge{Progress: badges.BadgeProgress{Current: 0, Threshold: 3}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertKeys(t, "user badge", wire, []string{"badge", "earned", "grantedAt", "progress"})

	progress, ok := wire["progress"].(map[string]any)
	if !ok {
		t.Fatalf("progress 必须是对象，得到 %T", wire["progress"])
	}
	assertKeys(t, "progress", progress, []string{"current", "threshold"})
	// 数字而不是字符串：`current` 若变成 "0/3" 这类展示串，前端就无法比较。
	if current, ok := progress["current"].(float64); !ok || current != 0 {
		t.Fatalf("progress.current 必须是数字 0，得到 %#v", progress["current"])
	}
	if threshold, ok := progress["threshold"].(float64); !ok || threshold != 3 {
		t.Fatalf("progress.threshold 必须是数字 3，得到 %#v", progress["threshold"])
	}
}

// TestProfileItemProgressKeepsFlatShape 断言资料页 DTO 的扁平形状与「无模式字段」。
//
// 含 `fact.count` 的徽章没有可枚举的阈值语义，因此 profile DTO **不产出进度**
// （不是「产出 0/0 的占位」）：条件式进度会把「判不了」伪装成「0%」。
func TestProfileItemProgressKeepsFlatShape(t *testing.T) {
	withThreshold := profileItemWire(t, badges.UserBadge{
		Badge:    badges.PublicBadge{Slug: "ten-topics", GrantMode: badges.GrantAutomatic},
		Progress: badges.BadgeProgress{Current: 2, Threshold: 3},
	})
	if current, ok := withThreshold["current"].(float64); !ok || current != 2 {
		t.Fatalf("current 必须是数字 2，得到 %#v", withThreshold["current"])
	}
	if threshold, ok := withThreshold["threshold"].(float64); !ok || threshold != 3 {
		t.Fatalf("threshold 必须是数字 3，得到 %#v", withThreshold["threshold"])
	}

	// 插件事实徽章：Threshold 为 0 → 两个键都不出现。
	factBased := profileItemWire(t, badges.UserBadge{
		Badge:    badges.PublicBadge{Slug: "plugin-pages", GrantMode: badges.GrantAutomatic},
		Progress: badges.BadgeProgress{},
	})
	for _, key := range []string{"current", "threshold", "mode", "satisfied", "total"} {
		if _, present := factBased[key]; present {
			t.Fatalf("事实徽章不得输出 %q（键集合见下）：%v", key, factBased)
		}
	}
}

func profileItemWire(t *testing.T, entry badges.UserBadge) map[string]any {
	t.Helper()
	raw, err := json.Marshal(toProfileBadgeItem(entry))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return wire
}
