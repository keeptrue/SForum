package forum

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BookmarkReminderNotificationInput 是一条到期提醒的通知投影输入。
//
// RecipientUserID 是收藏行主本人（提醒没有 actor）；TopicID 在评论目标上用于
// 邮件里的主题跳转，主题目标时等于 TargetID。
type BookmarkReminderNotificationInput struct {
	BookmarkID      int64
	RecipientUserID int64
	TargetType      string
	TargetID        int64
	TopicID         int64
}

// BookmarkReminderNotifier 是到期提醒的可选通知投影。
//
// 与 NotificationWriter 分开声明：NotificationWriter 有多个测试 fake 实现，
// 为一个新能力改它们的签名只会制造噪音（与 ReactionPreviewStore 同姿态）。
type BookmarkReminderNotifier interface {
	NotifyBookmarkReminderTx(context.Context, pgx.Tx, BookmarkReminderNotificationInput) error
}

// bookmarkReminderClaim 是抢占到的一条到期提醒。
type bookmarkReminderClaim struct {
	bookmarkID int64
	userID     int64
	targetType string
	targetID   int64
	topicID    int64
	visible    bool
}

// RunDueBookmarkReminders 抢占到期提醒并在同一事务里写通知投影。
//
// 只发一次的保证来自 UPDATE ... WHERE reminded_at IS NULL RETURNING：并发执行
// 的两个 worker 里只有一个能把行翻成「已提醒」。目标不可见（主题删除/隐藏、
// 评论删除）时仍然写 reminded_at、但不发通知——提醒的是内容，内容没了就没有
// 可提醒的对象，留着行只会每轮重扫。
//
// 返回 (claimed, notified, err)：claimed 是抢到的行数，notified 是真正写了通知
// 投影的行数，两者之差就是「目标已不可见」的数量，运维可据此观察失效收藏。
func (s *PostgresStore) RunDueBookmarkReminders(ctx context.Context, now time.Time, limit int) (int, int, error) {
	if s == nil || s.pool == nil {
		return 0, 0, ErrInvalidBookmarkTarget
	}
	if limit <= 0 {
		return 0, 0, nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, 0, fmt.Errorf("begin bookmark reminders: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	rows, err := tx.Query(ctx, `
		WITH due AS (
		  SELECT bookmarks.id
		  FROM content_bookmarks AS bookmarks
		  WHERE bookmarks.remind_at IS NOT NULL
		    AND bookmarks.reminded_at IS NULL
		    AND bookmarks.remind_at <= $1
		  ORDER BY bookmarks.remind_at ASC, bookmarks.id ASC
		  LIMIT $2
		  FOR UPDATE SKIP LOCKED
		)
		UPDATE content_bookmarks AS bookmarks
		SET reminded_at = now(), updated_at = now()
		FROM due
		WHERE bookmarks.id = due.id AND bookmarks.reminded_at IS NULL
		RETURNING bookmarks.id, bookmarks.user_id, bookmarks.target_type, bookmarks.target_id,
		  CASE WHEN bookmarks.target_type = 'comment'
		       THEN COALESCE((SELECT comments.topic_id FROM comments WHERE comments.id = bookmarks.target_id), 0)
		       ELSE bookmarks.target_id END,
		  `+bookmarkTargetVisibleExpr+`
	`, now, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("claim due bookmark reminders: %w", err)
	}
	claims := make([]bookmarkReminderClaim, 0, limit)
	for rows.Next() {
		var claim bookmarkReminderClaim
		if err := rows.Scan(&claim.bookmarkID, &claim.userID, &claim.targetType, &claim.targetID, &claim.topicID, &claim.visible); err != nil {
			rows.Close()
			return 0, 0, err
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()

	notified := 0
	for _, claim := range claims {
		if !claim.visible || s.reminderNotifier() == nil {
			continue
		}
		if err := s.reminderNotifier().NotifyBookmarkReminderTx(ctx, tx, BookmarkReminderNotificationInput{
			BookmarkID:      claim.bookmarkID,
			RecipientUserID: claim.userID,
			TargetType:      claim.targetType,
			TargetID:        claim.targetID,
			TopicID:         claim.topicID,
		}); err != nil {
			return 0, 0, fmt.Errorf("notify bookmark reminder: %w", err)
		}
		notified++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit bookmark reminders: %w", err)
	}
	return len(claims), notified, nil
}

// reminderNotifier 把可选依赖收敛成一次断言：未装配时返回 nil（只标已提醒、不发）。
func (s *PostgresStore) reminderNotifier() BookmarkReminderNotifier {
	if s == nil {
		return nil
	}
	notifier, ok := s.notifications.(BookmarkReminderNotifier)
	if !ok {
		return nil
	}
	return notifier
}
