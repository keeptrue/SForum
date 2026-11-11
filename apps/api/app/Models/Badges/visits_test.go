package badges

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakeActivityStore 是 ActivityStore 的内存实现。
// RecordVisit 刻意复刻 user_visits 的 (user_id, visited_on) 复合主键语义：
// 同一天第二次返回 false 且计数不变。
type fakeActivityStore struct {
	mu     sync.Mutex
	visits map[string]bool
	days   map[int64]int64
	calls  int
	err    error
}

func newFakeActivityStore() *fakeActivityStore {
	return &fakeActivityStore{visits: map[string]bool{}, days: map[int64]int64{}}
}

func (f *fakeActivityStore) BumpTx(context.Context, pgx.Tx, int64, ActivityMetric, int64) error {
	return nil
}

func (f *fakeActivityStore) GetCounters(_ context.Context, userID int64) (ActivityCounters, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ActivityCounters{UserID: userID, DaysVisited: f.days[userID]}, nil
}

func (f *fakeActivityStore) RecordVisit(_ context.Context, userID int64, day time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	key := userIDDayKey(userID, day)
	if f.visits[key] {
		return false, nil
	}
	f.visits[key] = true
	f.days[userID]++
	return true, nil
}

func (f *fakeActivityStore) Recompute(context.Context, int64) (ActivityCounters, error) {
	return ActivityCounters{}, nil
}

func (f *fakeActivityStore) daysVisited(userID int64) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.days[userID]
}

func (f *fakeActivityStore) recordCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func userIDDayKey(userID int64, day time.Time) string {
	return VisitDedupKey(userID, day)
}

// fakeDeduper 复刻 Redis SETNX：首次 true、同日后续 false。
type fakeDeduper struct {
	mu   sync.Mutex
	seen map[string]bool
	err  error
}

func newFakeDeduper() *fakeDeduper { return &fakeDeduper{seen: map[string]bool{}} }

func (d *fakeDeduper) MarkOnce(_ context.Context, key string, _ time.Duration) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return false, d.err
	}
	if d.seen[key] {
		return false, nil
	}
	d.seen[key] = true
	return true, nil
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// TestVisitDedupKeyFormat 固定去重键形状：badges:visit:{userID}:{YYYY-MM-DD}。
func TestVisitDedupKeyFormat(t *testing.T) {
	day := time.Date(2026, 11, 10, 15, 4, 5, 0, time.UTC)
	if got, want := VisitDedupKey(42, day), "badges:visit:42:2026-11-10"; got != want {
		t.Fatalf("VisitDedupKey = %q, want %q", got, want)
	}
}

// TestVisitRecorderSameDayCountsOnce 断言「同一天重复访问只算 1 天」。
func TestVisitRecorderSameDayCountsOnce(t *testing.T) {
	store := newFakeActivityStore()
	recorder := NewVisitRecorderWithDeduper(store, newFakeDeduper(), nil)
	day := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	recorder.now = fixedClock(day)

	recorder.Record(context.Background(), 7)
	recorder.Record(context.Background(), 7)
	recorder.Record(context.Background(), 7)

	if got := store.daysVisited(7); got != 1 {
		t.Fatalf("days_visited = %d, want 1 (同日只算一天)", got)
	}
	// 去重生效时不该反复写库：只有首次穿透到 store。
	if got := store.recordCalls(); got != 1 {
		t.Fatalf("RecordVisit calls = %d, want 1 (Redis 去重应挡住后两次)", got)
	}
}

// TestVisitRecorderNextDayCountsAgain 断言跨天 +1。
func TestVisitRecorderNextDayCountsAgain(t *testing.T) {
	store := newFakeActivityStore()
	deduper := newFakeDeduper()
	recorder := NewVisitRecorderWithDeduper(store, deduper, nil)

	day := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	recorder.now = fixedClock(day)
	recorder.Record(context.Background(), 7)

	// 第二天：key 变了，必须重新计数。
	recorder.now = fixedClock(day.Add(24 * time.Hour))
	recorder.Record(context.Background(), 7)

	if got := store.daysVisited(7); got != 2 {
		t.Fatalf("days_visited = %d, want 2 (跨天应 +1)", got)
	}
}

// TestVisitRecorderDegradesWhenDeduperUnavailable 断言 Redis 不可用时的降级：
// 仍然写库（多一次冲突写入），计数由 user_visits 主键兜住，不会重复累加。
func TestVisitRecorderDegradesWhenDeduperUnavailable(t *testing.T) {
	store := newFakeActivityStore()
	deduper := newFakeDeduper()
	deduper.err = errors.New("redis down")
	recorder := NewVisitRecorderWithDeduper(store, deduper, nil)
	day := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	recorder.now = fixedClock(day)

	recorder.Record(context.Background(), 7)
	recorder.Record(context.Background(), 7)

	if got := store.daysVisited(7); got != 1 {
		t.Fatalf("days_visited = %d, want 1 (降级后仍由主键保证幂等)", got)
	}
	// 降级意味着每次都尝试写库：这正是「多一次冲突写入，无害」。
	if got := store.recordCalls(); got != 2 {
		t.Fatalf("RecordVisit calls = %d, want 2 (降级时每次都写库)", got)
	}
}

// TestVisitRecorderNilDeduperWritesEveryTime 断言无 Redis 的部署（deduper=nil）
// 退化为每次都写库，幂等完全交给主键。
func TestVisitRecorderNilDeduperWritesEveryTime(t *testing.T) {
	store := newFakeActivityStore()
	recorder := NewVisitRecorderWithDeduper(store, nil, nil)
	day := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	recorder.now = fixedClock(day)

	recorder.Record(context.Background(), 7)
	recorder.Record(context.Background(), 7)

	if got := store.daysVisited(7); got != 1 {
		t.Fatalf("days_visited = %d, want 1", got)
	}
	if got := store.recordCalls(); got != 2 {
		t.Fatalf("RecordVisit calls = %d, want 2", got)
	}
}

// TestVisitRecorderSkipsAnonymousAndNeverPanics 断言匿名跳过与 nil 安全。
func TestVisitRecorderSkipsAnonymousAndNeverPanics(t *testing.T) {
	store := newFakeActivityStore()
	recorder := NewVisitRecorderWithDeduper(store, newFakeDeduper(), nil)
	recorder.Record(context.Background(), 0)
	recorder.Record(context.Background(), -3)
	if got := store.recordCalls(); got != 0 {
		t.Fatalf("RecordVisit calls = %d, want 0 (匿名必须跳过)", got)
	}

	var nilRecorder *VisitRecorder
	nilRecorder.Record(context.Background(), 7) // 不得 panic

	noStore := NewVisitRecorderWithDeduper(nil, newFakeDeduper(), nil)
	noStore.Record(context.Background(), 7) // 未装配 store 时静默返回
}

// TestVisitRecorderStoreErrorIsSilent 断言写库失败不外泄（fail-open）。
func TestVisitRecorderStoreErrorIsSilent(t *testing.T) {
	store := newFakeActivityStore()
	store.err = errors.New("db down")
	recorder := NewVisitRecorderWithDeduper(store, nil, nil)
	recorder.Record(context.Background(), 7)
	if got := store.recordCalls(); got != 1 {
		t.Fatalf("RecordVisit calls = %d, want 1", got)
	}
}

// TestActivityMetricWhitelist 固定指标白名单：与迁移 badges_metric_check 逐字一致，
// 且列名永远来自白名单（SQL 拼接不接受外部输入）。
func TestActivityMetricWhitelist(t *testing.T) {
	want := map[ActivityMetric]string{
		MetricTopicCount:        "topic_count",
		MetricCommentCount:      "comment_count",
		MetricLikeReceivedCount: "like_received_count",
		MetricLikeGivenCount:    "like_given_count",
		MetricBookmarkCount:     "bookmark_count",
		MetricDaysVisited:       "days_visited",
	}
	if got := len(ActivityMetrics()); got != len(want) {
		t.Fatalf("ActivityMetrics() = %d, want %d", got, len(want))
	}
	for metric, column := range want {
		got, ok := metric.Column()
		if !ok || got != column {
			t.Fatalf("Column(%q) = (%q, %v), want %q", metric, got, ok, column)
		}
		if !metric.Valid() {
			t.Fatalf("%q should be valid", metric)
		}
	}
	if ActivityMetric("posts_read_count").Valid() {
		t.Fatal("posts_read_count 是未采集的阅读指标，不得进入白名单")
	}
	if _, ok := ActivityMetric("topic_count; DROP TABLE users").Column(); ok {
		t.Fatal("非白名单输入必须被拒绝")
	}
}

// TestActivityCountersValue 断言按指标取值的映射。
func TestActivityCountersValue(t *testing.T) {
	counters := ActivityCounters{
		TopicCount: 1, CommentCount: 2, LikeReceivedCount: 3,
		LikeGivenCount: 4, BookmarkCount: 5, DaysVisited: 6,
	}
	cases := map[ActivityMetric]int64{
		MetricTopicCount: 1, MetricCommentCount: 2, MetricLikeReceivedCount: 3,
		MetricLikeGivenCount: 4, MetricBookmarkCount: 5, MetricDaysVisited: 6,
	}
	for metric, want := range cases {
		if got := counters.Value(metric); got != want {
			t.Fatalf("Value(%q) = %d, want %d", metric, got, want)
		}
	}
	if got := counters.Value(ActivityMetric("nope")); got != 0 {
		t.Fatalf("unknown metric Value = %d, want 0", got)
	}
}

// TestBumpActivitySQLKeepsDeltaClamped 断言累加 SQL 的核心性质：
// 行懒创建（ON CONFLICT DO UPDATE）+ 负 delta 钳到 0（不回绕成负数）。
func TestBumpActivitySQLKeepsDeltaClamped(t *testing.T) {
	for _, fragment := range []string{
		"INSERT INTO user_activity",
		"ON CONFLICT (user_id) DO UPDATE",
		"GREATEST(user_activity.%[1]s + $2, 0)",
		"GREATEST($2, 0)",
	} {
		if !strings.Contains(bumpActivitySQL, fragment) {
			t.Fatalf("bumpActivitySQL missing %q", fragment)
		}
	}
}
