package forum

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// bumpContentRevisionOnDeleteTx 在删除事务内推进内容版本号。
//
// 删除前拿到的 expectedRevision 必须立即失效：即使内容之后被恢复（status 回到
// active），旧令牌也不能通过 CAS。GREATEST 兼容 current_revision=0 的迁移期旧行
// （其有效版本按 1 回读），保证 bump 后的有效版本严格递增。
func bumpContentRevisionOnDeleteTx(ctx context.Context, tx pgx.Tx, postID int64) error {
	if _, err := tx.Exec(ctx, `
		UPDATE posts
		SET current_revision = GREATEST(current_revision, 1) + 1
		WHERE id = $1
	`, postID); err != nil {
		return fmt.Errorf("bump content revision on delete: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteComment(ctx context.Context, commentID int64) (Comment, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Comment{}, fmt.Errorf("begin delete comment: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var topicID int64
	var parentID *int64
	var prevStatus string
	var contentID int64
	if err := tx.QueryRow(ctx, `
		SELECT topic_id, parent_comment_id, status, content_id
		FROM comments
		WHERE id = $1 AND status <> 'deleted'
		FOR UPDATE
	`, commentID).Scan(&topicID, &parentID, &prevStatus, &contentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Comment{}, ErrCommentNotFound
		}
		return Comment{}, fmt.Errorf("lock comment for delete: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE comments
		SET status = 'deleted', deleted_at = COALESCE(deleted_at, now()), updated_at = now()
		WHERE id = $1
	`, commentID); err != nil {
		return Comment{}, fmt.Errorf("soft delete comment: %w", err)
	}
	if err := bumpContentRevisionOnDeleteTx(ctx, tx, contentID); err != nil {
		return Comment{}, err
	}
	// 仅 active 评论曾 +1 计数，删除时对称回滚（对齐 moderation workbench）。
	if prevStatus == CommentStatusActive {
		if parentID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE comments
				SET reply_count = GREATEST(reply_count - 1, 0), updated_at = now()
				WHERE id = $1
			`, *parentID); err != nil {
				return Comment{}, fmt.Errorf("decrement parent reply count: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET comment_count = GREATEST(comment_count - 1, 0),
			    hot_score = `+TopicHotScoreSQL("GREATEST(comment_count - 1, 0)", "like_count")+`,
			    updated_at = now()
			WHERE id = $1
		`, topicID); err != nil {
			return Comment{}, fmt.Errorf("decrement topic comment count: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE categories
			SET comment_count = GREATEST(comment_count - 1, 0), updated_at = now()
			WHERE id = (SELECT category_id FROM topics WHERE id = $1)
		`, topicID); err != nil {
			return Comment{}, fmt.Errorf("decrement category comment count: %w", err)
		}
	}
	if err := replaceForumAttachmentReferences(ctx, tx, "comment", commentID, 0, nil); err != nil {
		return Comment{}, err
	}

	comment, err := getCommentByID(ctx, tx, commentID, s.avatarBuilder)
	if err != nil {
		return Comment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Comment{}, fmt.Errorf("commit delete comment: %w", err)
	}
	return comment, nil
}
