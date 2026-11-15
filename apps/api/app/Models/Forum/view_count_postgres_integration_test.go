package forum

import (
	"testing"
	"time"
)

// 浏览刷盘只更新计数，不得推进公开的「内容更新时间」（RV-52）。
//
// 旧实现里的 SET updated_at = now() 会把公开 updatedAt 的语义从「内容更新时间」
// 改写成「最近被看时间」：一次刷屏浏览就能让所有列表按「刚更新」排序。
func TestApplyViewCountDeltasDoesNotMoveUpdatedAt(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	author := fixture.insertUser(t, "view-count-author")
	topic := fixture.insertBareTopic(t, author, "view-count-updated-at")
	published := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := fixture.pool.Exec(fixture.ctx,
		`UPDATE topics SET updated_at = $2 WHERE id = $1`, topic.id, published); err != nil {
		t.Fatalf("pin topics.updated_at: %v", err)
	}

	store := NewPostgresStore(fixture.pool)
	if _, err := store.ApplyViewCountDeltas(fixture.ctx, map[int64]int64{topic.id: 5}); err != nil {
		t.Fatalf("apply view deltas: %v", err)
	}

	var viewCount, hotScore int64
	var updatedAt time.Time
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT view_count, hot_score, updated_at FROM topics WHERE id = $1`, topic.id).
		Scan(&viewCount, &hotScore, &updatedAt); err != nil {
		t.Fatalf("read topic counters: %v", err)
	}
	if viewCount != 5 || hotScore != 5 {
		t.Fatalf("view_count/hot_score = %d/%d, want 5/5", viewCount, hotScore)
	}
	if !updatedAt.Equal(published) {
		t.Fatalf("view flush moved topics.updated_at from %s to %s: viewing a topic must not rewrite its content update time", published, updatedAt)
	}
}
