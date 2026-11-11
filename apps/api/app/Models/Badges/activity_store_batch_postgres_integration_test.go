package badges

import "testing"

// TestGetCountersManyReturnsOnlyExistingRows 钉住批量读的唯一一条 SQL：
// 空数组/非正 id/不存在的 id 不得让查询报错，缺失行不得出现在 map 里（缺席语义），
// 且读取本身不能改动原行。
func TestGetCountersManyReturnsOnlyExistingRows(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	first := fixture.user(t, "batch_activity_a", "active")
	second := fixture.user(t, "batch_activity_b", "active")
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO user_activity (user_id, topic_count, comment_count, like_received_count,
		                           like_given_count, bookmark_count, days_visited)
		VALUES ($1, 3, 1, 5, 2, 1, 7), ($2, 4, 2, 6, 3, 2, 9)
	`, first, second); err != nil {
		t.Fatalf("insert user_activity: %v", err)
	}

	store := NewPostgresActivityStore(fixture.pool)
	counters, err := store.GetCountersMany(fixture.ctx, []int64{first, second, first, 0, -1, 9_999_999})
	if err != nil {
		t.Fatalf("GetCountersMany: %v", err)
	}
	if len(counters) != 2 {
		t.Fatalf("返回 %d 行，期望 2（重复/非法/缺失 id 都不产出条目）", len(counters))
	}
	if got := counters[first]; got.TopicCount != 3 || got.DaysVisited != 7 {
		t.Fatalf("first counters = %+v，期望 topic=3 days=7", got)
	}
	if got := counters[second]; got.TopicCount != 4 || got.DaysVisited != 9 {
		t.Fatalf("second counters = %+v，期望 topic=4 days=9", got)
	}

	// 读取不得改动原行（GetCountersMany 只允许 SELECT）。
	var topicCount int64
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT topic_count FROM user_activity WHERE user_id = $1`, first).Scan(&topicCount); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if topicCount != 3 {
		t.Fatalf("读取后 first.topic_count = %d，期望仍为 3", topicCount)
	}

	// 空集与全非法集：必须是空 map + nil error，而不是 SQL 报错。
	for name, ids := range map[string][]int64{"empty": nil, "invalid": {0, -1}} {
		got, err := store.GetCountersMany(fixture.ctx, ids)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s 输入 => (%d rows, %v)，期望 (0, nil)", name, len(got), err)
		}
	}
}
