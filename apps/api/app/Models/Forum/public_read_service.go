package forum

import (
	"context"
	"log/slog"

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
	// trustLevels 可选：为作者补信任等级（整页一次批量）；nil 时不发字段、不查询。
	trustLevels *AuthorTrustLevelReader
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

// WithAuthorTrustLevels 注入作者等级来源；返回自身以保持链式装配。
//
// 装饰发生在请求级，不进缓存；nil 或来源失败时读面自动不带 authorTrustLevel。
func (s *PublicReadService) WithAuthorTrustLevels(source AuthorTrustLevelSource) *PublicReadService {
	if s != nil {
		s.trustLevels = NewAuthorTrustLevelReader(source)
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
	// 信任等级与徽章同为 actor 无关，但它是派生值，永不进缓存：每次读面按当前
	// 策略重算，运营改阈值后下一次请求即生效。
	batchTopics := []TopicSummary{topic.TopicSummary}
	s.decorateTopicTrustLevels(ctx, batchTopics)
	topic.AuthorTrustLevel = batchTopics[0].AuthorTrustLevel
}

// decorateTopicTrustLevels 批量补主题作者等级（整页一次调用）。
//
// **fail-open 是刻意的**：等级徽章是增强面，拿不到就不发字段，绝不让列表/详情 500
// （隐私与可用性都要求）。但静默吞错会让「徽章集体消失」变成无线索故障，所以失败时
// 打一条 Debug 留痕：平时不刷屏，排障时把日志级别开到 debug 就能看到原因。
func (s *PublicReadService) decorateTopicTrustLevels(ctx context.Context, topics []TopicSummary) {
	if s == nil || s.trustLevels == nil || len(topics) == 0 {
		return
	}
	authorIDs := topicAuthorIDs(topics)
	levels, err := s.trustLevels.TrustLevelBatch(ctx, authorIDs)
	if err != nil {
		slog.DebugContext(ctx, "forum: skip author trust level decoration",
			"authorCount", len(authorIDs), "error", err)
		return
	}
	applyTopicAuthorTrustLevels(topics, levels)
}

// decorateCommentTrustLevels 批量补评论作者等级（整页一次调用）。
//
// 失败姿态与主题同：fail-open 到「不带等级」，并留一条 Debug（含作者数与 error）。
func (s *PublicReadService) decorateCommentTrustLevels(ctx context.Context, comments []Comment) {
	if s == nil || s.trustLevels == nil || len(comments) == 0 {
		return
	}
	authorIDs := commentAuthorIDs(comments)
	levels, err := s.trustLevels.TrustLevelBatch(ctx, authorIDs)
	if err != nil {
		slog.DebugContext(ctx, "forum: skip author trust level decoration",
			"authorCount", len(authorIDs), "error", err)
		return
	}
	applyCommentAuthorTrustLevels(comments, levels)
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
	s.decorateCommentTrustLevels(ctx, comments)
}

// ListTopics 在 Service.ListTopics 之上补作者信任等级（整页一次批量装载）。
//
// 装饰在缓存之后：等级是派生值，缓存一份就等于把「运营改阈值」冻结到 TTL。
// 未装配来源（或开关关闭、装载失败）时行为与 Service.ListTopics 完全一致。
func (s *PublicReadService) ListTopics(ctx context.Context, input TopicListInput) (TopicList, error) {
	list, err := s.Service.ListTopics(ctx, input)
	if err != nil {
		return list, err
	}
	s.decorateTopicTrustLevels(ctx, list.Items)
	return list, nil
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

// GetTopic 是匿名公开详情读（无 viewer 状态）。
//
// 这里也走 decorateTopic：导出方法的响应形状必须与 ...ForViewer 一致，否则插件或
// 未来调用点会拿到「同一份 DTO 却缺 authorTrustLevel」。代价是匿名路径多一次策略
// 读取——控制器当前全走 ForViewer，所以生产热路径不受影响。
func (s *PublicReadService) GetTopic(ctx context.Context, topicID int64) (TopicDetail, error) {
	topic, err := s.Service.GetTopic(ctx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	topic, _ = s.shortcodes.renderTopic(ctx, topic, identity.Actor{})
	s.decorateTopic(ctx, identity.Actor{}, &topic)
	return topic, nil
}

// GetTopicBySlug 与 GetTopic 同口径：匿名 slug 读也补装饰字段。
func (s *PublicReadService) GetTopicBySlug(ctx context.Context, slug string) (TopicDetail, error) {
	topic, err := s.Service.GetTopicBySlug(ctx, slug)
	if err != nil {
		return TopicDetail{}, err
	}
	topic, _ = s.shortcodes.renderTopic(ctx, topic, identity.Actor{})
	s.decorateTopic(ctx, identity.Actor{}, &topic)
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
