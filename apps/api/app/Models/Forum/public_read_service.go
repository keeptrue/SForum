package forum

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// PublicReadService owns actor-aware public topic reads. The actor-independent
// base remains cacheable; later protected-content composition can use viewer.
type PublicReadService struct {
	*Service
	shortcodes *publicShortcodeRenderer
}

func NewPublicReadService(service *Service) *PublicReadService {
	return &PublicReadService{Service: service}
}

func (s *PublicReadService) WithShortcodeDispatcher(dispatcher PublicShortcodeDispatcher) *PublicReadService {
	if s != nil && s.Service != nil {
		if s.shortcodes == nil {
			s.shortcodes = newPublicShortcodeRenderer(s.Service.store)
		}
		s.shortcodes.dispatcher = dispatcher
		if protected, ok := dispatcher.(ProtectedShortcodeDispatcher); ok {
			s.shortcodes.protected = protected
		}
	}
	return s
}

// WithProtectedShortcodes injects the Host policy authority and request-only
// renderer. Production M8 leaves authorizer nil, so protected tags fail closed;
// focused tests use a fake authority/handler without registering product tags.
func (s *PublicReadService) WithProtectedShortcodes(authorizer ProtectedShortcodeAuthorizer, dispatcher ProtectedShortcodeDispatcher) *PublicReadService {
	if s != nil && s.Service != nil {
		if s.shortcodes == nil {
			s.shortcodes = newPublicShortcodeRenderer(s.Service.store)
		}
		s.shortcodes.authorizer = authorizer
		s.shortcodes.protected = dispatcher
	}
	return s
}

func (s *PublicReadService) GetTopic(ctx context.Context, topicID int64) (TopicDetail, error) {
	topic, err := s.Service.GetTopic(ctx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	topic, _ = s.shortcodes.renderTopic(ctx, topic, identity.Actor{})
	return topic, nil
}

func (s *PublicReadService) GetTopicBySlug(ctx context.Context, slug string) (TopicDetail, error) {
	topic, err := s.Service.GetTopicBySlug(ctx, slug)
	if err != nil {
		return TopicDetail{}, err
	}
	topic, _ = s.shortcodes.renderTopic(ctx, topic, identity.Actor{})
	return topic, nil
}

func (s *PublicReadService) ListComments(ctx context.Context, input CommentListInput) (CommentList, error) {
	list, err := s.Service.ListComments(ctx, input)
	if err != nil {
		return CommentList{}, err
	}
	list.Items, list.ProtectedContent = s.shortcodes.renderComments(ctx, list.Items, input.Viewer)
	return list, nil
}

func (s *PublicReadService) ListCommentRepliesForViewer(ctx context.Context, commentID int64, viewer identity.Actor) ([]Comment, bool, error) {
	items, err := s.Service.ListCommentRepliesForViewer(ctx, commentID, viewer)
	if err != nil {
		return nil, false, err
	}
	items, protected := s.shortcodes.renderComments(ctx, items, viewer)
	return items, protected, nil
}

func (s *PublicReadService) GetTopicForViewer(ctx context.Context, topicID int64, viewer identity.Actor) (TopicDetail, error) {
	topic, err := s.Service.GetTopic(ctx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	topic, topic.ProtectedContent = s.shortcodes.renderTopic(ctx, topic, viewer)
	return topic, nil
}

func (s *PublicReadService) GetTopicBySlugForViewer(ctx context.Context, slug string, viewer identity.Actor) (TopicDetail, error) {
	topic, err := s.Service.GetTopicBySlug(ctx, slug)
	if err != nil {
		return TopicDetail{}, err
	}
	topic, topic.ProtectedContent = s.shortcodes.renderTopic(ctx, topic, viewer)
	return topic, nil
}
