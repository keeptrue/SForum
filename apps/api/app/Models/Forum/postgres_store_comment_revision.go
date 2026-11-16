package forum

import (
	"context"
	"fmt"
	"time"
)

// CommentRevisionState 读取公开评论修订状态：一次 topics 主键读 + 一次
// comments_topic_created_idx 反向尾部读，都是索引命中，且不经评论列表缓存
// （列表缓存有 20/40s TTL，用它会发出过期的「一切正常」信号）。
func (s *PostgresStore) CommentRevisionState(ctx context.Context, topicID int64) (CommentRevisionState, error) {
	var state CommentRevisionState
	// 主题没有任何 active 评论时尾部 LEFT JOIN 为 NULL，用可空目标承接再归一。
	var lastID *int64
	var lastCreatedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT topics.comment_count, topics.comment_revision,
		       tail.id, tail.created_at
		FROM topics
		LEFT JOIN LATERAL (
			SELECT comments.id, comments.created_at
			FROM comments
			WHERE comments.topic_id = topics.id AND comments.status = 'active'
			ORDER BY comments.created_at DESC, comments.id DESC
			LIMIT 1
		) AS tail ON TRUE
		WHERE topics.id = $1
	`, topicID).Scan(&state.CommentCount, &state.Revision, &lastID, &lastCreatedAt)
	if err != nil {
		return CommentRevisionState{}, fmt.Errorf("topic comment revision: %w", err)
	}
	if lastID != nil {
		state.LastCommentID = *lastID
	}
	state.LastCommentCreatedAt = lastCreatedAt
	return state, nil
}

var _ CommentRevisionStore = (*PostgresStore)(nil)
