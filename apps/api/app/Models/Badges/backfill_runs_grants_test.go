package badges

import (
	"context"
	"testing"
)

// GAP-2 / B-02：**有授予行、没有 run 行 → items 里不出现该徽章**。
//
// 这是「不从授予数反推进度」的唯一反向证据：如果哪天有人为了「显示得更满」而拿
// `user_badges` 行数合成一个假 run，这条会立刻红。宁可空 items，也不给运营一个
// 编出来的进度。
func TestBackfillRunsIgnoreGrantRows(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "alice", "active")
	badgeID := fixture.badge(t, "ten-topics", "gold", 0, "topic_count", 10)
	ctx := context.Background()

	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO user_badges (user_id, badge_id, grant_reason) VALUES ($1,$2,'recompute')`,
		userID, badgeID); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	// 断言前置条件：授予行确实存在（否则用例变成「空库当然空」的空转）。
	var grants int64
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM user_badges WHERE badge_id = $1`, badgeID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 1 {
		t.Fatalf("fixture 必须真的写入授予行，得到 %d", grants)
	}

	runs := NewPostgresBackfillRuns(fixture.pool)
	items, err := runs.ListLatestBackfillRuns(ctx)
	if err != nil {
		t.Fatalf("ListLatestBackfillRuns: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("没有 run 行时 items 必须为空（不得从授予数反推），得到 %+v", items)
	}
}
