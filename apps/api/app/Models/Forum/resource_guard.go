package forum

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TopicResourceGuardSubject is the minimal authoritative topic ownership state
// for Core topic edit/delete route guards. Owner identity is always loaded from
// PostgreSQL; callers must never supply an owner id.
type TopicResourceGuardSubject struct {
	TopicID      int64
	AuthorUserID int64
	Status       string
	Exists       bool
}

// CommentResourceGuardSubject is the minimal authoritative comment ownership
// state for Core comment update/delete route guards.
type CommentResourceGuardSubject struct {
	CommentID    int64
	AuthorUserID int64
	Status       string
	Exists       bool
}

// LoadTopicResourceGuardSubject loads current topic ownership for a production
// route guard. Missing rows return ErrTopicNotFound so the guard can fail closed.
// 作者注销后 author_user_id 被 FK 置为 NULL（ON DELETE SET NULL），这里归一成 0，
// 交给上层按「作者已注销」fail-closed，而不是把记录误判成不存在。
func (s *PostgresStore) LoadTopicResourceGuardSubject(ctx context.Context, topicID int64) (TopicResourceGuardSubject, error) {
	if s == nil || s.pool == nil || ctx == nil || topicID <= 0 {
		return TopicResourceGuardSubject{}, ErrInvalidTopic
	}
	var subject TopicResourceGuardSubject
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(author_user_id, 0), status FROM topics WHERE id = $1
	`, topicID).Scan(&subject.TopicID, &subject.AuthorUserID, &subject.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopicResourceGuardSubject{}, ErrTopicNotFound
	}
	if err != nil {
		return TopicResourceGuardSubject{}, fmt.Errorf("load topic resource guard subject: %w", err)
	}
	subject.Exists = true
	return subject, nil
}

// LoadCommentResourceGuardSubject loads current comment ownership for a
// production route guard. Missing rows return ErrCommentNotFound.
// 同 topic：NULL 作者归一成 0，记录本身仍然存在。
func (s *PostgresStore) LoadCommentResourceGuardSubject(ctx context.Context, commentID int64) (CommentResourceGuardSubject, error) {
	if s == nil || s.pool == nil || ctx == nil || commentID <= 0 {
		return CommentResourceGuardSubject{}, ErrCommentNotFound
	}
	var subject CommentResourceGuardSubject
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(author_user_id, 0), status FROM comments WHERE id = $1
	`, commentID).Scan(&subject.CommentID, &subject.AuthorUserID, &subject.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return CommentResourceGuardSubject{}, ErrCommentNotFound
	}
	if err != nil {
		return CommentResourceGuardSubject{}, fmt.Errorf("load comment resource guard subject: %w", err)
	}
	subject.Exists = true
	return subject, nil
}
