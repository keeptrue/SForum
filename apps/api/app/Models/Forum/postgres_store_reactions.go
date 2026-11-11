package forum

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ApplyReaction 在一个事务里完成「关系行 + 计数列 + 热门分 + 通知投影」。
//
// 幂等语义：已点赞再点赞、未点赞再取消都不重复计数，只返回当前权威状态。
// 计数与关系行同事务，因此漂移只可能来自 bug，不来自并发。
func (s *PostgresStore) ApplyReaction(ctx context.Context, input ApplyReactionRecord) (ReactionState, error) {
	if s == nil || s.pool == nil {
		return ReactionState{}, ErrInvalidReactionTarget
	}
	if input.Reaction == "" {
		input.Reaction = ReactionLike
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ReactionState{}, fmt.Errorf("begin reaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	applied, err := applyReactionRow(ctx, tx, input)
	if err != nil {
		return ReactionState{}, err
	}
	likeCount, err := applyReactionCount(ctx, tx, input, applied)
	if err != nil {
		return ReactionState{}, err
	}
	// 用户活动统计（计划 D3/D4）：
	//   - 只有真的增删了关系行（applied）才计数，幂等重放不重复累加；
	//   - 自赞返回 0,0，两个 like 计数都不动（内容行 like_count 已照常更新，那是热度）；
	//   - 取消点赞是关系行被删除，两个计数一起回退。
	if applied && input.Reaction == ReactionLike {
		received, given := activityDeltaForReaction(input.ActorUserID, input.RecipientUserID, input.Add)
		if err := bumpUserActivityTx(ctx, tx, input.RecipientUserID, activityMetricLikeReceivedCount, received); err != nil {
			return ReactionState{}, err
		}
		if err := bumpUserActivityTx(ctx, tx, input.ActorUserID, activityMetricLikeGivenCount, given); err != nil {
			return ReactionState{}, err
		}
	}
	if applied && input.Add && input.RecipientUserID > 0 && input.RecipientUserID != input.ActorUserID {
		if err := s.notifyReactionTx(ctx, tx, input); err != nil {
			return ReactionState{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ReactionState{}, fmt.Errorf("commit reaction: %w", err)
	}
	// D2：目标作者与操作者各评估一次（同一次入队会被 river 的 ByArgs 去重合并）。
	if applied {
		s.enqueueBadgeEvaluation(ctx, input.RecipientUserID)
		s.enqueueBadgeEvaluation(ctx, input.ActorUserID)
	}
	return ReactionState{
		TargetType: input.TargetType,
		TargetID:   input.TargetID,
		Reaction:   input.Reaction,
		Liked:      input.Add,
		LikeCount:  likeCount,
	}, nil
}

func applyReactionRow(ctx context.Context, tx pgx.Tx, input ApplyReactionRecord) (bool, error) {
	if input.Add {
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO content_reactions (user_id, target_type, target_id, reaction)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT ON CONSTRAINT content_reactions_unique DO NOTHING
			RETURNING id
		`, input.ActorUserID, input.TargetType, input.TargetID, input.Reaction).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("insert reaction: %w", err)
		}
		return true, nil
	}
	var id int64
	err := tx.QueryRow(ctx, `
		DELETE FROM content_reactions
		WHERE user_id = $1 AND target_type = $2 AND target_id = $3 AND reaction = $4
		RETURNING id
	`, input.ActorUserID, input.TargetType, input.TargetID, input.Reaction).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("delete reaction: %w", err)
	}
	return true, nil
}

// applyReactionCount 维护计数列；主题额外重算 hot_score（唯一公式入口）。
func applyReactionCount(ctx context.Context, tx pgx.Tx, input ApplyReactionRecord, applied bool) (int64, error) {
	switch input.TargetType {
	case ReactionTargetTopic:
		if applied && input.Add {
			var likeCount int64
			err := tx.QueryRow(ctx, `
				UPDATE topics
				SET like_count = like_count + 1,
				    hot_score = `+TopicHotScoreSQL("comment_count", "(like_count + 1)")+`
				WHERE id = $1
				RETURNING like_count
			`, input.TargetID).Scan(&likeCount)
			if err != nil {
				return 0, fmt.Errorf("increment topic like count: %w", err)
			}
			return likeCount, nil
		}
		if applied {
			var likeCount int64
			err := tx.QueryRow(ctx, `
				UPDATE topics
				SET like_count = GREATEST(like_count - 1, 0),
				    hot_score = `+TopicHotScoreSQL("comment_count", "GREATEST(like_count - 1, 0)")+`
				WHERE id = $1
				RETURNING like_count
			`, input.TargetID).Scan(&likeCount)
			if err != nil {
				return 0, fmt.Errorf("decrement topic like count: %w", err)
			}
			return likeCount, nil
		}
		var likeCount int64
		if err := tx.QueryRow(ctx, `SELECT like_count FROM topics WHERE id = $1`, input.TargetID).Scan(&likeCount); err != nil {
			return 0, fmt.Errorf("read topic like count: %w", err)
		}
		return likeCount, nil
	case ReactionTargetComment:
		if applied && input.Add {
			var likeCount int64
			err := tx.QueryRow(ctx, `
				UPDATE comments
				SET like_count = like_count + 1
				WHERE id = $1
				RETURNING like_count
			`, input.TargetID).Scan(&likeCount)
			if err != nil {
				return 0, fmt.Errorf("increment comment like count: %w", err)
			}
			return likeCount, nil
		}
		if applied {
			var likeCount int64
			err := tx.QueryRow(ctx, `
				UPDATE comments
				SET like_count = GREATEST(like_count - 1, 0)
				WHERE id = $1
				RETURNING like_count
			`, input.TargetID).Scan(&likeCount)
			if err != nil {
				return 0, fmt.Errorf("decrement comment like count: %w", err)
			}
			return likeCount, nil
		}
		var likeCount int64
		if err := tx.QueryRow(ctx, `SELECT like_count FROM comments WHERE id = $1`, input.TargetID).Scan(&likeCount); err != nil {
			return 0, fmt.Errorf("read comment like count: %w", err)
		}
		return likeCount, nil
	default:
		return 0, ErrInvalidReactionTarget
	}
}

func (s *PostgresStore) notifyReactionTx(ctx context.Context, tx pgx.Tx, input ApplyReactionRecord) error {
	if s.notifications == nil {
		return nil
	}
	if err := s.notifications.NotifyReactionTx(ctx, tx, ReactionNotificationInput{
		TargetType:      input.TargetType,
		TargetID:        input.TargetID,
		TopicID:         input.TopicID,
		ActorUserID:     input.ActorUserID,
		RecipientUserID: input.RecipientUserID,
	}); err != nil {
		return fmt.Errorf("create reaction notification: %w", err)
	}
	return nil
}

// LikedTargetIDs 一次查询回答一页里「我赞过哪些」，避免 N+1。
func (s *PostgresStore) LikedTargetIDs(ctx context.Context, viewerUserID int64, targetType string, targetIDs []int64) (map[int64]struct{}, error) {
	liked := make(map[int64]struct{}, len(targetIDs))
	if s == nil || s.pool == nil || viewerUserID <= 0 || len(targetIDs) == 0 {
		return liked, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT target_id
		FROM content_reactions
		WHERE user_id = $1 AND target_type = $2 AND reaction = $3 AND target_id = ANY($4)
	`, viewerUserID, targetType, ReactionLike, targetIDs)
	if err != nil {
		return nil, fmt.Errorf("list liked targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		liked[id] = struct{}{}
	}
	return liked, rows.Err()
}

// ListReactionUsers 返回最近点赞者；总数用窗口函数与分页同查。
func (s *PostgresStore) ListReactionUsers(ctx context.Context, targetType string, targetID int64, limit, offset int) (ReactionRoster, error) {
	roster := ReactionRoster{Items: []UserSummary{}}
	if s == nil || s.pool == nil || targetID <= 0 {
		return roster, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT users.id, users.username, users.display_name, users.email,
		  profiles.avatar_attachment_id,
		  attachments.id, attachments.public_id, attachments.owner_user_id,
		  attachments.content_type, attachments.status,
		  COUNT(*) OVER () AS total
		FROM content_reactions AS reactions
		JOIN users ON users.id = reactions.user_id
		LEFT JOIN user_profiles AS profiles ON profiles.user_id = users.id
		LEFT JOIN attachments ON attachments.id = profiles.avatar_attachment_id
		WHERE reactions.target_type = $1
		  AND reactions.target_id = $2
		  AND reactions.reaction = $3
		  AND users.status = 'active'
		ORDER BY reactions.created_at DESC, reactions.id DESC
		LIMIT $4 OFFSET $5
	`, targetType, targetID, ReactionLike, limit, offset)
	if err != nil {
		return roster, fmt.Errorf("list reaction users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			userID                       sql.NullInt64
			username, displayName, email sql.NullString
			avatarAttachmentID           sql.NullInt64
			attachmentID                 sql.NullInt64
			attachmentPublicID           sql.NullString
			attachmentOwnerID            sql.NullInt64
			attachmentContentType        sql.NullString
			attachmentStatus             sql.NullString
			total                        int64
		)
		if err := rows.Scan(
			&userID, &username, &displayName, &email,
			&avatarAttachmentID,
			&attachmentID, &attachmentPublicID, &attachmentOwnerID, &attachmentContentType, &attachmentStatus,
			&total,
		); err != nil {
			return roster, err
		}
		summary := userSummaryWithAvatar(
			s.avatarBuilder,
			userID, username, displayName, email,
			avatarAttachmentID, attachmentID, attachmentPublicID, attachmentOwnerID,
			attachmentContentType, attachmentStatus,
		)
		if summary != nil {
			roster.Items = append(roster.Items, *summary)
		}
		roster.Total = total
	}
	if err := rows.Err(); err != nil {
		return roster, err
	}
	roster.HasMore = int64(offset+len(roster.Items)) < roster.Total
	return roster, nil
}

// ReactionPreviews 一次批量查询返回每个目标的头像组预览（最多 limit 个）。
//
// 排序口径：viewer 自己优先（$3=0 匿名时 user_id=0 恒假，自然退化为最早优先），
// 其余按 created_at、id 最早在前。复用 content_reactions_roster_idx，不新增迁移。
// 无点赞的目标不出现在返回 map 里。
func (s *PostgresStore) ReactionPreviews(ctx context.Context, targetType string, targetIDs []int64, viewerUserID int64, limit int) (map[int64][]UserSummary, error) {
	previews := make(map[int64][]UserSummary, len(targetIDs))
	if s == nil || s.pool == nil || len(targetIDs) == 0 || limit <= 0 {
		return previews, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH ranked AS (
		  SELECT r.target_id, r.user_id, r.created_at, r.id,
		         ROW_NUMBER() OVER (
		           PARTITION BY r.target_id
		           ORDER BY (r.user_id = $3) DESC, r.created_at ASC, r.id ASC
		         ) AS rn
		  FROM content_reactions AS r
		  JOIN users ON users.id = r.user_id
		  WHERE r.target_type = $1
		    AND r.target_id = ANY($2)
		    AND r.reaction = $4
		    AND users.status = 'active'
		)
		SELECT ranked.target_id, users.id, users.username, users.display_name, users.email,
		       profiles.avatar_attachment_id, attachments.id, attachments.public_id,
		       attachments.owner_user_id, attachments.content_type, attachments.status
		FROM ranked
		JOIN users ON users.id = ranked.user_id
		LEFT JOIN user_profiles AS profiles ON profiles.user_id = users.id
		LEFT JOIN attachments ON attachments.id = profiles.avatar_attachment_id
		WHERE ranked.rn <= $5
		ORDER BY ranked.target_id, ranked.rn
	`, targetType, targetIDs, viewerUserID, ReactionLike, limit)
	if err != nil {
		return nil, fmt.Errorf("list reaction previews: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			targetID                     int64
			userID                       sql.NullInt64
			username, displayName, email sql.NullString
			avatarAttachmentID           sql.NullInt64
			attachmentID                 sql.NullInt64
			attachmentPublicID           sql.NullString
			attachmentOwnerID            sql.NullInt64
			attachmentContentType        sql.NullString
			attachmentStatus             sql.NullString
		)
		if err := rows.Scan(
			&targetID,
			&userID, &username, &displayName, &email,
			&avatarAttachmentID,
			&attachmentID, &attachmentPublicID, &attachmentOwnerID, &attachmentContentType, &attachmentStatus,
		); err != nil {
			return nil, err
		}
		summary := userSummaryWithAvatar(
			s.avatarBuilder,
			userID, username, displayName, email,
			avatarAttachmentID, attachmentID, attachmentPublicID, attachmentOwnerID,
			attachmentContentType, attachmentStatus,
		)
		if summary != nil {
			previews[targetID] = append(previews[targetID], *summary)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return previews, nil
}
