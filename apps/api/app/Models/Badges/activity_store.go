package badges

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ActivityStore 是统计层的窄接口。
//
// 写路径只需要 BumpTx（它接收调用方已开启的事务，统计与业务写同生共死）；
// GetCounters / RecordVisit / Recompute 供读路径、访问采集与后台重算使用。
type ActivityStore interface {
	BumpTx(ctx context.Context, tx pgx.Tx, userID int64, metric ActivityMetric, delta int64) error
	GetCounters(ctx context.Context, userID int64) (ActivityCounters, error)
	RecordVisit(ctx context.Context, userID int64, day time.Time) (bool, error)
	Recompute(ctx context.Context, userID int64) (ActivityCounters, error)
}

// PostgresActivityStore 是 ActivityStore 的生产实现。
type PostgresActivityStore struct {
	pool *pgxpool.Pool
}

func NewPostgresActivityStore(pool *pgxpool.Pool) *PostgresActivityStore {
	return &PostgresActivityStore{pool: pool}
}

// BumpTx 在调用方事务内累加一个计数。
//
// delta 可以为负（取消点赞/取消收藏），结果由 SQL 里的 GREATEST(...,0) 钳到 0。
// delta 为 0 或 userID 非正时是 no-op：调用方不必自己判空。
func (s *PostgresActivityStore) BumpTx(ctx context.Context, tx pgx.Tx, userID int64, metric ActivityMetric, delta int64) error {
	if userID <= 0 || delta == 0 {
		return nil
	}
	column, ok := metric.Column()
	if !ok {
		return fmt.Errorf("badges: unknown activity metric %q", metric)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(bumpActivitySQL, column), userID, delta); err != nil {
		return fmt.Errorf("bump user activity %s: %w", column, err)
	}
	return nil
}

// GetCounters 读一行统计快照。
//
// 行是懒创建的：从未产生过统计事件的用户没有行，这里返回全 0 且不报错，
// 让调用方不必区分「没有行」与「全 0」——两种情况的业务含义相同。
func (s *PostgresActivityStore) GetCounters(ctx context.Context, userID int64) (ActivityCounters, error) {
	if s == nil || s.pool == nil || userID <= 0 {
		return ActivityCounters{UserID: userID}, nil
	}
	var counters ActivityCounters
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, topic_count, comment_count, like_received_count,
		       like_given_count, bookmark_count, days_visited, last_visited_on, updated_at
		FROM user_activity
		WHERE user_id = $1
	`, userID).Scan(
		&counters.UserID, &counters.TopicCount, &counters.CommentCount,
		&counters.LikeReceivedCount, &counters.LikeGivenCount, &counters.BookmarkCount,
		&counters.DaysVisited, &counters.LastVisitedOn, &counters.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivityCounters{UserID: userID}, nil
	}
	if err != nil {
		return ActivityCounters{}, fmt.Errorf("load user activity: %w", err)
	}
	return counters, nil
}

// RecordVisit 写一行日访问记录，并只在真的插入成功时把 days_visited +1。
//
// 返回的 bool 表示「今天是不是第一次」：false 说明该 (user, day) 已存在，
// 计数不再变化——这正是重复访问只算 1 天的保证。
func (s *PostgresActivityStore) RecordVisit(ctx context.Context, userID int64, day time.Time) (bool, error) {
	if s == nil || s.pool == nil || userID <= 0 {
		return false, nil
	}
	visitedOn := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin record visit: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	tag, err := tx.Exec(ctx, recordVisitSQL, userID, visitedOn)
	if err != nil {
		return false, fmt.Errorf("insert user visit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// 今天已经记过：幂等退出，不 commit（没有写入）。
		return false, nil
	}
	if _, err := tx.Exec(ctx, bumpDaysVisitedSQL, userID, visitedOn); err != nil {
		return false, fmt.Errorf("bump days visited: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit record visit: %w", err)
	}
	return true, nil
}

// Recompute 按真值表修复「关系行派生」的计数，用于后台重算按钮兜底。
//
// 刻意只重算四类：days_visited（user_visits）、like_given / like_received
// （content_reactions，且排除自赞）、bookmark_count（content_bookmarks）。
// topic_count 与 comment_count 不在这里重算：它们记的是「贡献已发生过」，
// 内容软删或被审核驳回都不回退（计划 D4）。用现存内容行数去覆盖它们，等于把
// 「你做过什么」改成「你现在还有什么」，与资料页的双源语义（D1）直接冲突。
//
// 自赞不计入：like_received 的 SQL 显式排除 r.user_id = 目标作者，
// 与写路径的 D3 口径保持一致。
func (s *PostgresActivityStore) Recompute(ctx context.Context, userID int64) (ActivityCounters, error) {
	if s == nil || s.pool == nil || userID <= 0 {
		return ActivityCounters{UserID: userID}, nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ActivityCounters{}, fmt.Errorf("begin recompute activity: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// 行懒创建：先保证有行，UPDATE 才有落点。
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_activity (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING
	`, userID); err != nil {
		return ActivityCounters{}, fmt.Errorf("ensure user activity row: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE user_activity AS activity
		SET days_visited       = stats.days_visited,
		    like_given_count   = stats.like_given,
		    like_received_count = stats.like_received,
		    bookmark_count     = stats.bookmarks,
		    updated_at         = now()
		FROM (
			SELECT
				(SELECT COUNT(*) FROM user_visits WHERE user_id = $1) AS days_visited,
				(SELECT COUNT(*) FROM content_bookmarks WHERE user_id = $1) AS bookmarks,
				(SELECT COUNT(*) FROM content_reactions WHERE user_id = $1 AND reaction = 'like') AS like_given,
				(
					SELECT COUNT(*)
					FROM content_reactions AS reactions
					JOIN topics ON topics.id = reactions.target_id
					WHERE reactions.target_type = 'topic'
					  AND reactions.reaction = 'like'
					  AND topics.author_user_id = $1
					  AND reactions.user_id <> $1
				) + (
					SELECT COUNT(*)
					FROM content_reactions AS reactions
					JOIN comments ON comments.id = reactions.target_id
					WHERE reactions.target_type = 'comment'
					  AND reactions.reaction = 'like'
					  AND comments.author_user_id = $1
					  AND reactions.user_id <> $1
				) AS like_received
		) AS stats
		WHERE activity.user_id = $1
	`, userID); err != nil {
		return ActivityCounters{}, fmt.Errorf("recompute user activity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ActivityCounters{}, fmt.Errorf("commit recompute activity: %w", err)
	}
	return s.GetCounters(ctx, userID)
}
