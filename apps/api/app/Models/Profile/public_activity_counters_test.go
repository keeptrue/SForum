package profile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeActivityCounters struct {
	counters PublicActivityCounters
	err      error
	calls    int
}

func (f *fakeActivityCounters) PublicActivityCountersFor(context.Context, int64) (PublicActivityCounters, error) {
	f.calls++
	return f.counters, f.err
}

func newCountersProfileStore() *fakeStore {
	return &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7, Bio: "hello"},
		stats:   ProfileStats{TopicCount: 3, CommentCount: 12},
	}
}

// TestPublicProfileIncludesActivityCounters 是 K-04 的接线断言：
// 资料页必须带获赞数与访问天数，且值来自计数来源（不是恒 0）。
func TestPublicProfileIncludesActivityCounters(t *testing.T) {
	counters := &fakeActivityCounters{counters: PublicActivityCounters{LikeReceivedCount: 41, DaysVisited: 23}}
	service := NewService(newCountersProfileStore()).WithActivityCounters(counters)

	result, err := service.GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetPublicProfile error: %v", err)
	}
	if counters.calls != 1 {
		t.Fatalf("计数来源被调用 %d 次，期望 1 次", counters.calls)
	}
	if result.LikeReceivedCount != 41 || result.DaysVisited != 23 {
		t.Fatalf("计数没进 payload：获赞 = %d，访问天数 = %d", result.LikeReceivedCount, result.DaysVisited)
	}

	// wire 形状：前端读的是 json 键名，键写错等于字段不存在。
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"likeReceivedCount", "daysVisited"} {
		value, ok := payload[key]
		if !ok {
			t.Fatalf("响应缺字段 %q：%s", key, encoded)
		}
		if _, isNumber := value.(float64); !isNumber {
			t.Fatalf("%q 不是数字：%v", key, value)
		}
	}
}

// TestPublicProfileFailsWhenCountersUnavailable 锁定失败姿态：
// 计数读失败时整页失败，而不是静默返回 0——「恒显示 0 获赞」看起来像真的，
// 比缺字段更难被发现（K-04 的原始症状）。
func TestPublicProfileFailsWhenCountersUnavailable(t *testing.T) {
	broken := errors.New("user_activity unavailable")
	counters := &fakeActivityCounters{err: broken}
	service := NewService(newCountersProfileStore()).WithActivityCounters(counters)

	if _, err := service.GetPublicProfile(context.Background(), "alice"); !errors.Is(err, broken) {
		t.Fatalf("计数读失败被吞掉了：err = %v", err)
	}
}

// TestPublicProfileWithoutCountersStaysZero 是对照组：未接线时两个计数为 0，
// 与本次接线之前逐字节等价（装配层漏接会退化成这个状态，所以要单独钉住）。
func TestPublicProfileWithoutCountersStaysZero(t *testing.T) {
	result, err := NewService(newCountersProfileStore()).GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetPublicProfile error: %v", err)
	}
	if result.LikeReceivedCount != 0 || result.DaysVisited != 0 {
		t.Fatalf("未接线时计数应为 0，实际：%d / %d", result.LikeReceivedCount, result.DaysVisited)
	}
}
