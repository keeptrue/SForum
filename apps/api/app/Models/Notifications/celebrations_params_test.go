package notifications

import (
	"encoding/json"
	"testing"
)

// 庆祝参数白名单（`celebrationParams`）的单测。
//
// 为什么单独钉：参数是从通知 payload 里**摘**出来的，摘漏一个键，前端就只能渲染
// 半张卡；摘多一个键（透传），就把「庆祝层能看到什么」变成一张无界表。
// 真库集成测试覆盖不到「旧 payload 没有 badgeSlug」这种历史数据形状，所以那一条
// 留在这里。

// TestCelebrationParamsCarriesBadgeSlug 断言 badge_granted 带出 slug 与名称。
func TestCelebrationParamsCarriesBadgeSlug(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"badgeSlug": "veteran",
		"badgeName": "元老",
		"badgeId":   42,
		"locale":    "zh-CN",
		"extra":     "must-not-leak",
	})
	if err != nil {
		t.Fatal(err)
	}
	params := celebrationParams(TypeBadgeGranted, payload)
	if params["badgeSlug"] != "veteran" {
		t.Fatalf("params.badgeSlug = %q, want veteran", params["badgeSlug"])
	}
	if params["badgeName"] != "元老" {
		t.Fatalf("params.badgeName = %q, want 元老", params["badgeName"])
	}
	for _, forbidden := range []string{"badgeId", "locale", "extra"} {
		if _, exists := params[forbidden]; exists {
			t.Fatalf("params 不得透传 %q（白名单纪律）：%v", forbidden, params)
		}
	}
}

// TestCelebrationParamsOmitsMissingBadgeSlug 固定旧数据兼容：键缺失就不输出。
//
// 旧通知行（badgeSlug 入 payload 之前授予的）只有 badgeName。此时**不能**补一个
// 空串：前端按「键存在」决定是否去查当前徽章资料，空串会变成一次必然 404 的请求，
// 而缺键让它直接渲染旧文案。
func TestCelebrationParamsOmitsMissingBadgeSlug(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"badgeName": "元老"})
	if err != nil {
		t.Fatal(err)
	}
	params := celebrationParams(TypeBadgeGranted, payload)
	if _, exists := params["badgeSlug"]; exists {
		t.Fatalf("缺 badgeSlug 时不得输出该键（旧数据兼容），得到 %v", params)
	}
	if params["badgeName"] != "元老" {
		t.Fatalf("params.badgeName = %q, want 元老", params["badgeName"])
	}
}

// TestCelebrationParamsEmptySlugIsNotAKey 覆盖「键存在但值为空串」的脏数据：
// 与缺键同一种处理（不输出），否则前端会拿空 slug 去请求。
func TestCelebrationParamsEmptySlugIsNotAKey(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"badgeName": "元老", "badgeSlug": ""})
	if err != nil {
		t.Fatal(err)
	}
	if params := celebrationParams(TypeBadgeGranted, payload); len(params) != 1 || params["badgeName"] != "元老" {
		t.Fatalf("空 slug 不得成为参数键，得到 %v", params)
	}
}

// TestCelebrationParamsIgnoresNonBadgeTypes 断言插件/其它类型的 payload 一律不摘键。
func TestCelebrationParamsIgnoresNonBadgeTypes(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"badgeSlug": "veteran", "badgeName": "元老"})
	if err != nil {
		t.Fatal(err)
	}
	if params := celebrationParams("sforum.test.milestone", payload); len(params) != 0 {
		t.Fatalf("非 badge_granted 类型不得带徽章参数，得到 %v", params)
	}
}
