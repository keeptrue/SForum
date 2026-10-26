package forum

import (
	"context"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// EditableSourceService owns authorization for loading canonical editor source.
// Its reads bypass actor-independent public caches after mutation-equivalent checks.
type EditableSourceService struct {
	service *Service
}

func NewEditableSourceService(service *Service) *EditableSourceService {
	return &EditableSourceService{service: service}
}

func (s *EditableSourceService) GetTopicEditSource(ctx context.Context, actor identity.Actor, topicID int64) (EditableContentSource, error) {
	if topicID <= 0 {
		return EditableContentSource{}, ErrTopicNotFound
	}
	topic, err := s.service.store.GetTopicForAction(ctx, topicID)
	if err != nil {
		return EditableContentSource{}, err
	}
	if topic.Status == TopicStatusHidden || topic.Status == TopicStatusDeleted || !canEditTopic(actor, topic) {
		return EditableContentSource{}, ErrTopicNotFound
	}
	settings, err := s.service.resolvedSettings(ctx)
	if err != nil {
		return EditableContentSource{}, err
	}
	if isAuthorOnlyTopicEdit(actor, topic) && !withinEditWindow(topic.CreatedAt, settings.TopicEditWindowMinutes, time.Now().UTC()) {
		return EditableContentSource{}, ErrEditWindowExpired
	}
	return s.service.store.GetTopicEditSource(ctx, topicID)
}

func (s *EditableSourceService) GetCommentEditSource(ctx context.Context, actor identity.Actor, commentID int64) (EditableContentSource, error) {
	if commentID <= 0 {
		return EditableContentSource{}, ErrCommentNotFound
	}
	comment, err := s.service.store.GetCommentSummary(ctx, commentID)
	if err != nil {
		return EditableContentSource{}, err
	}
	if comment.Status == CommentStatusHidden || comment.Status == CommentStatusDeleted || !canEditComment(actor, comment) {
		return EditableContentSource{}, ErrCommentNotFound
	}
	topic, err := s.service.store.GetTopicForAction(ctx, comment.TopicID)
	if err != nil || topic.Status == TopicStatusHidden || topic.Status == TopicStatusDeleted {
		return EditableContentSource{}, ErrCommentNotFound
	}
	settings, err := s.service.resolvedSettings(ctx)
	if err != nil {
		return EditableContentSource{}, err
	}
	if isAuthorOnlyCommentEdit(actor, comment) && !withinEditWindow(comment.CreatedAt, settings.CommentEditWindowMinutes, time.Now().UTC()) {
		return EditableContentSource{}, ErrEditWindowExpired
	}
	return s.service.store.GetCommentEditSource(ctx, commentID)
}
