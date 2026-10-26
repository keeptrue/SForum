package forum

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// CommentCreateGuardSubject is the minimal authoritative topic state required
// before a trusted replacement may create a comment.
type CommentCreateGuardSubject struct {
	TopicID int64
	Status  string
	Exists  bool
}

// applyCommentBeforeCreate keeps the authoritative topic and parent identity
// outside the plugin patch surface; only content may be replaced.
func (s *Service) applyCommentBeforeCreate(ctx context.Context, actor identity.Actor, input CreateCommentInput) (CreateCommentInput, error) {
	payload := map[string]any{
		"actorUserId": actor.ID,
		"topicId":     input.TopicID,
		"content":     input.Content,
	}
	if input.ParentID != nil {
		payload["parentId"] = *input.ParentID
	}
	envelope := appevents.NewEnvelope(appevents.CommentBeforeCreate, payload)
	envelope.ActorUserID = actor.ID
	envelope.ResourceType = "comment"
	// 创建前尚无 commentId；用 topicId 作关联资源便于投递日志检索。
	envelope.ResourceID = strconv.FormatInt(input.TopicID, 10)
	result := s.events.Emit(ctx, envelope)
	if !result.OK {
		return CreateCommentInput{}, appevents.Reject(result)
	}
	if len(result.Patch) == 0 {
		return input, nil
	}
	if value, ok := contentInputFromPatch(result.Patch["content"]); ok {
		input.Content = value
	}
	return input, nil
}

func (s *PostgresStore) LoadCommentCreateGuardSubject(ctx context.Context, topicID int64) (CommentCreateGuardSubject, error) {
	if s == nil || s.pool == nil || ctx == nil || topicID <= 0 {
		return CommentCreateGuardSubject{}, ErrInvalidTopic
	}
	var subject CommentCreateGuardSubject
	err := s.pool.QueryRow(ctx, `
		SELECT id, status FROM topics WHERE id = $1
	`, topicID).Scan(&subject.TopicID, &subject.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return CommentCreateGuardSubject{}, ErrTopicNotFound
	}
	if err != nil {
		return CommentCreateGuardSubject{}, fmt.Errorf("load comment create guard subject: %w", err)
	}
	subject.Exists = true
	return subject, nil
}
