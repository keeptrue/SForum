package bootstrap

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// TestProfileActivityCountersAdapterReadsUserActivityColumns 是 K-04 的数据源断言：
// 适配器读出来的两个数字必须等于 user_activity 的对应列。
//
// 造数刻意让 like_received_count 与 like_given_count 取不同值（41 vs 7）：
// 两个列名只差一个词，读错了数字看着仍然像真的。
func TestProfileActivityCountersAdapterReadsUserActivityColumns(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("连接测试库失败：%v", err)
	}
	defer pool.Close()

	// 共享测试库里 users_id_seq 可能落后于 max(id)（有测试夹具显式插入 id）。
	// 先把序列对齐到 max(id)，否则下面的 nextval 会撞上既有行——这是夹具健壮性，
	// 不是产品行为；对齐后本测试在任何包之后运行都稳定。
	if _, err := pool.Exec(ctx, `
		SELECT setval(pg_get_serial_sequence('users', 'id'),
		              GREATEST((SELECT COALESCE(max(id), 0) FROM users), 1))`); err != nil {
		t.Fatalf("对齐 users 序列失败：%v", err)
	}
	username := fmt.Sprintf("profile_counts_%d", time.Now().UnixNano())
	var userID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Profile Counts')
		RETURNING id
	`, username, username+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("造用户失败：%v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_activity (
			user_id, topic_count, comment_count, like_received_count,
			like_given_count, bookmark_count, days_visited, last_visited_on
		)
		VALUES ($1, 5, 6, 41, 7, 3, 23, CURRENT_DATE)
	`, userID); err != nil {
		t.Fatalf("造 user_activity 失败：%v", err)
	}

	counters, err := profileActivityCounters{store: badges.NewPostgresActivityStore(pool)}.
		PublicActivityCountersFor(ctx, userID)
	if err != nil {
		t.Fatalf("适配器读失败：%v", err)
	}
	if counters.LikeReceivedCount != 41 {
		t.Fatalf("LikeReceivedCount = %d，期望 41（like_given_count 是 7，读错列会变成它）", counters.LikeReceivedCount)
	}
	if counters.DaysVisited != 23 {
		t.Fatalf("DaysVisited = %d，期望 23", counters.DaysVisited)
	}
}
