package badges

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GetCountersMany 批量读多行统计快照（一条 SQL，禁止按 id 循环）。
//
// 返回的 map 只含**真实存在的行**：行是懒创建的，调用方需要区分「没有行」与
// 「全 0」时用 ok 判定（单数的 GetCounters 刻意不区分，两个入口语义不同）。
func (s *PostgresActivityStore) GetCountersMany(ctx context.Context, userIDs []int64) (map[int64]ActivityCounters, error) {
	if s == nil || s.pool == nil {
		return map[int64]ActivityCounters{}, nil
	}
	ids := uniqueActivityUserIDs(userIDs)
	if len(ids) == 0 {
		return map[int64]ActivityCounters{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, topic_count, comment_count, like_received_count,
		       like_given_count, bookmark_count, days_visited, last_visited_on, updated_at
		FROM user_activity
		WHERE user_id = ANY($1)
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("load user activity batch: %w", err)
	}
	defer rows.Close()
	counters := make(map[int64]ActivityCounters, len(ids))
	for rows.Next() {
		var item ActivityCounters
		if err := rows.Scan(
			&item.UserID, &item.TopicCount, &item.CommentCount,
			&item.LikeReceivedCount, &item.LikeGivenCount, &item.BookmarkCount,
			&item.DaysVisited, &item.LastVisitedOn, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan user activity batch: %w", err)
		}
		counters[item.UserID] = item
	}
	if err := rows.Err(); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("iterate user activity batch: %w", err)
	}
	return counters, nil
}

// uniqueActivityUserIDs 去重并剔除非正 id；SQL 参数必须干净，避免 ANY() 上空值报错。
func uniqueActivityUserIDs(userIDs []int64) []int64 {
	seen := make(map[int64]bool, len(userIDs))
	out := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
