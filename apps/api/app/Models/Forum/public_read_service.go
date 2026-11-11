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
	// reactions 可选：为登录访客补 likedByMe。装饰发生在缓存之后，永不进缓存。
	reactions ReactionReader
	// bookmarks 可选：为登录访客补 bookmarkedByMe。与 reactions 同一契约：
	// 缓存之后、请求级、fail-open。
	bookmarks BookmarkReader
	// badges 可选：为作者补最高级徽章。与上面两个**相反**：徽章是 actor 无关的，
	// 同一份结果对所有人相同，因此它可以随主题/评论进缓存，不必每请求重算。
	// 缓存命中时这里通常无事可做（读模型已带 TopBadge）；它兜的是未带徽章的路径。
	badges BadgeReader
}

func NewPublicReadService(service *Service) *PublicReadService {
	return &PublicReadService{Service: service}
}

// WithReactions 注入点赞装饰器；nil 时读模型不含 likedByMe（匿名路径的默认形状）。
func (s *PublicReadService) WithReactions(reader ReactionReader) *PublicReadService {
	if s != nil {
		s.reactions = reader
	}
	return s
}

// WithBookmarks 注入收藏装饰器；nil 时读模型不含 bookmarkedByMe。
func (s *PublicReadService) WithBookmarks(reader BookmarkReader) *PublicReadService {
	if s != nil {
		s.bookmarks = reader
	}
	return s
}

// WithBadges 注入徽章装饰器；nil 时读模型不含 topBadge。
func (s *PublicReadService) WithBadges(reader BadgeReader) *PublicReadService {
	if s != nil {
		s.badges = reader
	}
	return s
}

func (s *PublicReadService) decorateTopic(ctx context.Context, viewer identity.Actor, topic *TopicDetail) {
	if s == nil || topic == nil {
		return
	}
	// 装饰失败必须 fail-open：点赞/收藏状态是增强面，不能把公开读打成 500。
	// 两件事各自独立、互不短路：一个失败不影响另一个的补齐。
	if s.reactions != nil {
		_ = s.reactions.DecorateTopic(ctx, viewer, topic)
	}
	if s.bookmarks != nil {
		_ = s.bookmarks.DecorateTopic(ctx, viewer, topic)
	}
	// 徽章是 actor 无关的：只补还没填过的字段，缓存里已有的不覆盖。
	if s.badges != nil && topic.TopBadge == nil {
		batch := []TopicSummary{topic.TopicSummary}
		if err := s.badges.DecorateTopics(ctx, batch); err == nil {
			topic.TopBadge = batch[0].TopBadge
			if topic.Author != nil && batch[0].Author != nil {
				topic.Author.TopBadge = batch[0].Author.TopBadge
			}
			if topic.LastReplyAuthor != nil && batch[0].LastReplyAuthor != nil {
				topic.LastReplyAuthor.TopBadge = batch[0].LastReplyAuthor.TopBadge
			}
		}
	}
}

func (s *PublicReadService) decorateComments(ctx context.Context, viewer identity.Actor, comments []Comment) {
	if s == nil || len(comments) == 0 {
		return
	}
	if s.reactions != nil {
		_ = s.reactions.DecorateComments(ctx, viewer, comments)
	}
	if s.bookmarks != nil {
		_ = s.bookmarks.DecorateComments(ctx, viewer, comments)
	}
	if s.badges != nil {
		_ = s.badges.DecorateComments(ctx, comments)
	}
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
	s.decorateComments(ctx, input.Viewer, list.Items)
	return list, nil
}

func (s *PublicReadService) ListCommentRepliesForViewer(ctx context.Context, commentID int64, viewer identity.Actor) ([]Comment, bool, error) {
	items, err := s.Service.ListCommentRepliesForViewer(ctx, commentID, viewer)
	if err != nil {
		return nil, false, err
	}
	items, protected := s.shortcodes.renderComments(ctx, items, viewer)
	s.decorateComments(ctx, viewer, items)
	return items, protected, nil
}

func (s *PublicReadService) GetTopicForViewer(ctx context.Context, topicID int64, viewer identity.Actor) (TopicDetail, error) {
	topic, err := s.Service.GetTopic(ctx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	topic, topic.ProtectedContent = s.shortcodes.renderTopic(ctx, topic, viewer)
	s.decorateTopic(ctx, viewer, &topic)
	return topic, nil
}

func (s *PublicReadService) GetTopicBySlugForViewer(ctx context.Context, slug string, viewer identity.Actor) (TopicDetail, error) {
	topic, err := s.Service.GetTopicBySlug(ctx, slug)
	if err != nil {
		return TopicDetail{}, err
	}
	topic, topic.ProtectedContent = s.shortcodes.renderTopic(ctx, topic, viewer)
	s.decorateTopic(ctx, viewer, &topic)
	return topic, nil
}
