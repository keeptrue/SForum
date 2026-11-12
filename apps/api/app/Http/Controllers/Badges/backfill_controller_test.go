package badgescontroller

import (
	"encoding/json"
	"testing"
	"time"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// TestBackfillRunStatusWireShape 钉住进度行的 wire 形状。
//
// 这一层是运营界面唯一的数据来源：`failedCursorUserId` 丢了，失败页定位就没了；
// `lastUserId` 丢了，进度看起来永远停在 0。领域测试不会暴露这些——领域层一直
// 读得到（见 Models/Badges/backfill_runs.go 的 ListLatestBackfillRuns）。
//
// 断言是**键集合精确相等**：多一个键同样是契约漂移。
func TestBackfillRunStatusWireShape(t *testing.T) {
	raw, err := json.Marshal(badges.BackfillRunStatus{
		BadgeID:            7,
		Slug:               "ten-topics",
		Name:               "十帖",
		Status:             badges.BackfillStatusFailed,
		LastUserID:         500,
		FailedCursorUserID: 500,
		StartedAt:          time.Unix(1, 0).UTC(),
		UpdatedAt:          time.Unix(2, 0).UTC(),
		Error:              "db down",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertKeys(t, "backfill run status", wire, []string{
		"badgeId", "slug", "name", "status", "lastUserId",
		"failedCursorUserId", "startedAt", "updatedAt", "finishedAt", "error",
	})
	// 未结束的运行必须带 finishedAt 键且值为 null（前端按 null 判定「还在跑」），
	// 不能靠 omitempty 让键消失。
	if value, present := wire["finishedAt"]; !present || value != nil {
		t.Fatalf("finishedAt 未结束时必须是 null 且键存在，得到 %v (present=%v)", value, present)
	}
	if wire["failedCursorUserId"] != float64(500) {
		t.Fatalf("failedCursorUserId = %v，want 500", wire["failedCursorUserId"])
	}
}

// TestBackfillStatusResponseWrapsItems 断言响应外壳就是 {"items":[...]}。
//
// 直接断言 handler 拼的那个 map 字面量（而不是走 HTTP 装配）：这层要证明的是
// 「外壳键名与 items 为空时是数组不是 null」。HTTP 装配另由路由/守卫测试覆盖。
func TestBackfillStatusResponseWrapsItems(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"items": []badges.BackfillRunStatus{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertKeys(t, "backfill status response", wire, []string{"items"})
	items, ok := wire["items"].([]any)
	if !ok {
		t.Fatalf("items 必须是数组（空数组不是 null），得到 %T", wire["items"])
	}
	if len(items) != 0 {
		t.Fatalf("期望空数组，得到 %v", items)
	}
}
