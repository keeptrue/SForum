package profile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

type fakeTopBadges struct {
	refs  map[int64]forum.BadgeRef
	err   error
	calls int
}

func (f *fakeTopBadges) TopBadgesFor(context.Context, []int64) (map[int64]forum.BadgeRef, error) {
	f.calls++
	return f.refs, f.err
}

func newTopBadgeProfileStore() *fakeStore {
	return &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7, Bio: "hello"},
		stats:   ProfileStats{TopicCount: 3, CommentCount: 12},
	}
}

// TestPublicProfileIncludesTopBadge 是公开资料页展示位的接线断言：
// topBadge 必须来自注入的读口（users.top_badge_id 的投影），且 wire 键名与
// 帖子/评论旁 chip 一致——前端读的是 json 键，键写错等于字段不存在。
func TestPublicProfileIncludesTopBadge(t *testing.T) {
	reader := &fakeTopBadges{refs: map[int64]forum.BadgeRef{
		7: {
			Slug:      "first-topic",
			Name:      "初次发帖",
			Tier:      "bronze",
			Icon:      "i-lucide-sprout",
			TierName:  forum.BadgeTierName{Zh: "铜牌", En: "Bronze"},
			TierColor: "#b45309",
		},
	}}
	service := NewService(newTopBadgeProfileStore()).WithTopBadgeReader(reader)

	result, err := service.GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetPublicProfile error: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("徽章读口被调用 %d 次，期望 1 次", reader.calls)
	}
	if result.TopBadge == nil || result.TopBadge.Slug != "first-topic" {
		t.Fatalf("展示徽章没进 payload：%#v", result.TopBadge)
	}

	// wire 形状：chip 的五个键一个都不能少，tierName 还要带双语。
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	badge, ok := payload["topBadge"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺对象字段 topBadge：%s", encoded)
	}
	for _, key := range []string{"slug", "name", "tier", "tierName", "tierColor"} {
		if _, ok := badge[key]; !ok {
			t.Fatalf("topBadge 缺字段 %q：%s", key, encoded)
		}
	}
	tierName, ok := badge["tierName"].(map[string]any)
	if !ok || tierName["zh"] != "铜牌" || tierName["en"] != "Bronze" {
		t.Fatalf("tierName 不是双语对象：%v", badge["tierName"])
	}
}

// TestPublicProfileOmitsTopBadgeWhenReaderFails 锁定 fail-soft 姿态：
// 徽章是装饰，读失败只省略字段——资料页不该为它整页 500。
func TestPublicProfileOmitsTopBadgeWhenReaderFails(t *testing.T) {
	broken := errors.New("badges unavailable")
	reader := &fakeTopBadges{err: broken}
	service := NewService(newTopBadgeProfileStore()).WithTopBadgeReader(reader)

	result, err := service.GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("徽章读失败不该让资料页失败：%v", err)
	}
	if result.TopBadge != nil {
		t.Fatalf("读失败时不应带出徽章：%#v", result.TopBadge)
	}
	if reader.calls != 1 {
		t.Fatalf("徽章读口被调用 %d 次，期望 1 次（要真试过才知道失败）", reader.calls)
	}
}

// TestPublicProfileWithoutTopBadgeReader 是对照组：未接线时字段缺省，
// 与本次接线之前逐字节等价（装配层漏接会退化成这个状态，所以要单独钉住）。
func TestPublicProfileWithoutTopBadgeReader(t *testing.T) {
	result, err := NewService(newTopBadgeProfileStore()).GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetPublicProfile error: %v", err)
	}
	if result.TopBadge != nil {
		t.Fatalf("未接线时不应带出徽章：%#v", result.TopBadge)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := payload["topBadge"]; ok {
		t.Fatalf("未接线时 topBadge 键必须整体省略：%s", encoded)
	}
}
