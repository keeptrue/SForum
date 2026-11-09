package forum

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// ReactionService 是点赞域的聚焦协作者。
//
// 刻意不是 *Service 的接收者方法集：Forum 的 Service 已在上限（77），AGENTS.md
// 要求跨能力时抽协作者。形状与 CitationService / TaxonomyService 一致：持有
// *Service 以便复用公开可见性规则，自己的依赖放在自己身上。
type ReactionService struct {
	service  *Service
	store    ReactionStore
	features FeatureGate
}

func NewReactionService(service *Service) *ReactionService {
	reactions := &ReactionService{service: service}
	if service != nil {
		// 生产装配里 store 是 *CachedStore（实现了 ReactionStore），缓存失效随之生效。
		if store, ok := service.store.(ReactionStore); ok {
			reactions.store = store
		}
	}
	return reactions
}

// WithStore 覆盖反应存储；nil 时保持构造时的断言结果。
func (s *ReactionService) WithStore(store ReactionStore) *ReactionService {
	if s == nil || store == nil {
		return s
	}
	s.store = store
	return s
}

// WithFeatureGate 注入站点产品开关。未装配时视为开启。
func (s *ReactionService) WithFeatureGate(gate FeatureGate) *ReactionService {
	if s == nil {
		return s
	}
	s.features = gate
	return s
}

func (s *ReactionService) LikeTopic(ctx context.Context, actor identity.Actor, topicID int64) (ReactionState, error) {
	return s.applyTopic(ctx, actor, topicID, true)
}

func (s *ReactionService) UnlikeTopic(ctx context.Context, actor identity.Actor, topicID int64) (ReactionState, error) {
	return s.applyTopic(ctx, actor, topicID, false)
}

func (s *ReactionService) LikeComment(ctx context.Context, actor identity.Actor, commentID int64) (ReactionState, error) {
	return s.applyComment(ctx, actor, commentID, true)
}

func (s *ReactionService) UnlikeComment(ctx context.Context, actor identity.Actor, commentID int64) (ReactionState, error) {
	return s.applyComment(ctx, actor, commentID, false)
}

// ListTopicLikes 返回主题的点赞名单（公开可读，受可见性与开关约束）。
func (s *ReactionService) ListTopicLikes(ctx context.Context, topicID int64, limit, offset int) (ReactionRoster, error) {
	if err := s.enabled(ctx); err != nil {
		return ReactionRoster{}, err
	}
	if s == nil || s.store == nil || s.service == nil {
		return ReactionRoster{}, ErrInvalidReactionTarget
	}
	if topicID <= 0 {
		return ReactionRoster{}, ErrTopicNotFound
	}
	if _, err := s.service.store.GetTopic(ctx, topicID); err != nil {
		return ReactionRoster{}, err
	}
	return s.store.ListReactionUsers(ctx, ReactionTargetTopic, topicID, clampRosterLimit(limit), clampRosterOffset(offset))
}

// ListCommentLikes 返回评论的点赞名单；评论必须 active 且所属主题公开可见。
func (s *ReactionService) ListCommentLikes(ctx context.Context, commentID int64, limit, offset int) (ReactionRoster, error) {
	if err := s.enabled(ctx); err != nil {
		return ReactionRoster{}, err
	}
	if s == nil || s.store == nil || s.service == nil {
		return ReactionRoster{}, ErrInvalidReactionTarget
	}
	comment, err := s.visibleComment(ctx, commentID)
	if err != nil {
		return ReactionRoster{}, err
	}
	return s.store.ListReactionUsers(ctx, ReactionTargetComment, comment.ID, clampRosterLimit(limit), clampRosterOffset(offset))
}

func (s *ReactionService) applyTopic(ctx context.Context, actor identity.Actor, topicID int64, add bool) (ReactionState, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return ReactionState{}, err
	}
	if topicID <= 0 {
		return ReactionState{}, ErrTopicNotFound
	}
	topic, err := s.service.store.GetTopic(ctx, topicID)
	if err != nil {
		return ReactionState{}, err
	}
	state, err := s.store.ApplyReaction(ctx, ApplyReactionRecord{
		ActorUserID:     actor.ID,
		TargetType:      ReactionTargetTopic,
		TargetID:        topic.ID,
		TopicID:         topic.ID,
		RecipientUserID: topic.AuthorUserID,
		Reaction:        ReactionLike,
		Add:             add,
	})
	if err != nil {
		return ReactionState{}, err
	}
	s.emit(ctx, actor, state)
	return state, nil
}

func (s *ReactionService) applyComment(ctx context.Context, actor identity.Actor, commentID int64, add bool) (ReactionState, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return ReactionState{}, err
	}
	comment, err := s.visibleComment(ctx, commentID)
	if err != nil {
		return ReactionState{}, err
	}
	state, err := s.store.ApplyReaction(ctx, ApplyReactionRecord{
		ActorUserID:     actor.ID,
		TargetType:      ReactionTargetComment,
		TargetID:        comment.ID,
		TopicID:         comment.TopicID,
		RecipientUserID: comment.AuthorUserID,
		Reaction:        ReactionLike,
		Add:             add,
	})
	if err != nil {
		return ReactionState{}, err
	}
	s.emit(ctx, actor, state)
	return state, nil
}

// visibleComment 复用公开评论的可见性口径：评论 active，且所属主题仍公开可见。
func (s *ReactionService) visibleComment(ctx context.Context, commentID int64) (CommentSummary, error) {
	if commentID <= 0 {
		return CommentSummary{}, ErrCommentNotFound
	}
	comment, err := s.service.store.GetCommentSummary(ctx, commentID)
	if err != nil {
		return CommentSummary{}, err
	}
	if comment.Status != CommentStatusActive {
		return CommentSummary{}, ErrCommentNotFound
	}
	if _, err := s.service.store.GetTopic(ctx, comment.TopicID); err != nil {
		// 主题不可见时归一成「评论不存在」：否则隐藏主题下存在的评论返回
		// topic_not_found、不存在的评论返回 comment_not_found，两者可被用来
		// 枚举隐藏主题里的评论 ID（存在性预言机）。
		if errors.Is(err, ErrTopicNotFound) {
			return CommentSummary{}, ErrCommentNotFound
		}
		return CommentSummary{}, err
	}
	return comment, nil
}

func (s *ReactionService) authorize(ctx context.Context, actor identity.Actor) error {
	if s == nil || s.store == nil {
		return ErrInvalidReactionTarget
	}
	if actor.ID <= 0 || actor.Status != identity.UserStatusActive {
		return identity.ErrPermissionDenied
	}
	return s.enabled(ctx)
}

func (s *ReactionService) enabled(ctx context.Context) error {
	if s == nil || s.features == nil {
		return nil
	}
	enabled, err := s.features.IsFeatureEnabled(ctx, options.NameFeatureReactions)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrReactionsDisabled
	}
	return nil
}

// emit 在提交后发出观察事件；事件是派生面，失败不阻断已成立的点赞。
func (s *ReactionService) emit(ctx context.Context, actor identity.Actor, state ReactionState) {
	if s == nil || s.service == nil || s.service.events == nil {
		return
	}
	name := appevents.ReactionRemoved
	if state.Liked {
		name = appevents.ReactionCreated
	}
	s.service.events.Emit(ctx, appevents.Envelope{
		Name:          name,
		Kind:          appevents.KindObserve,
		ActorUserID:   actor.ID,
		ResourceType:  state.TargetType,
		ResourceID:    strconv.FormatInt(state.TargetID, 10),
		CorrelationID: appevents.NewID(),
		Payload: map[string]any{
			"targetType":  state.TargetType,
			"targetId":    state.TargetID,
			"reaction":    state.Reaction,
			"actorUserId": actor.ID,
		},
		OccurredAt: time.Now().UTC(),
	})
}

// DecorateTopic 为公开详情补上「当前访客是否已赞」与点赞者头像组预览。
//
// 两件事各自 fail-open、互不短路：匿名访客跳过 likedByMe 但仍补预览；
// likedByMe 查询失败只意味着没有 likedByMe，不能连带吞掉预览（否则登录访客
// 会退化成空头像组）。预览先跑，likedByMe 的失败因此无法短路它。
func (s *ReactionService) DecorateTopic(ctx context.Context, viewer identity.Actor, topic *TopicDetail) error {
	if s == nil || s.store == nil || topic == nil {
		return nil
	}
	s.decoratePreviews(ctx, viewer.ID, ReactionTargetTopic, []int64{topic.ID}, func(previews map[int64][]UserSummary) {
		topic.LikedBy = previews[topic.ID]
	})
	if viewer.ID > 0 {
		liked, err := s.store.LikedTargetIDs(ctx, viewer.ID, ReactionTargetTopic, []int64{topic.ID})
		if err != nil {
			slog.WarnContext(ctx, "forum reactions: decorate topic failed", "topicId", topic.ID, "err", err)
			return nil
		}
		_, topic.LikedByMe = liked[topic.ID]
	}
	return nil
}

// DecorateComments 一次性补齐一页评论（含嵌套子评论）的 likedByMe 与预览。
// 与 DecorateTopic 同姿态：预览与 likedByMe 各自 fail-open，互不短路。
func (s *ReactionService) DecorateComments(ctx context.Context, viewer identity.Actor, comments []Comment) error {
	if s == nil || s.store == nil || len(comments) == 0 {
		return nil
	}
	ids := collectCommentIDs(comments, nil)
	if len(ids) == 0 {
		return nil
	}
	s.decoratePreviews(ctx, viewer.ID, ReactionTargetComment, ids, func(previews map[int64][]UserSummary) {
		applyCommentLikedBy(comments, previews)
	})
	if viewer.ID > 0 {
		liked, err := s.store.LikedTargetIDs(ctx, viewer.ID, ReactionTargetComment, ids)
		if err != nil {
			slog.WarnContext(ctx, "forum reactions: decorate comments failed", "count", len(ids), "err", err)
			return nil
		}
		applyCommentLiked(comments, liked)
	}
	return nil
}

// decoratePreviews 跑一次批量预览查询并交给 apply 回填。预览是首屏头像组的
// 增强面：查询失败只告警返回，不把公开读打成 500（fail-open）。
func (s *ReactionService) decoratePreviews(ctx context.Context, viewerUserID int64, targetType string, targetIDs []int64, apply func(map[int64][]UserSummary)) {
	store, ok := s.store.(ReactionPreviewStore)
	if !ok || len(targetIDs) == 0 {
		return
	}
	previews, err := store.ReactionPreviews(ctx, targetType, targetIDs, viewerUserID, ReactionPreviewLimit)
	if err != nil {
		slog.WarnContext(ctx, "forum reactions: decorate previews failed", "targetType", targetType, "count", len(targetIDs), "err", err)
		return
	}
	if len(previews) == 0 {
		return
	}
	apply(previews)
}

func collectCommentIDs(comments []Comment, ids []int64) []int64 {
	for i := range comments {
		ids = append(ids, comments[i].ID)
		ids = collectCommentIDs(comments[i].Children, ids)
	}
	return ids
}

func applyCommentLiked(comments []Comment, liked map[int64]struct{}) {
	for i := range comments {
		_, comments[i].LikedByMe = liked[comments[i].ID]
		applyCommentLiked(comments[i].Children, liked)
	}
}

func applyCommentLikedBy(comments []Comment, previews map[int64][]UserSummary) {
	for i := range comments {
		comments[i].LikedBy = previews[comments[i].ID]
		applyCommentLikedBy(comments[i].Children, previews)
	}
}

func clampRosterLimit(limit int) int {
	if limit <= 0 {
		return ReactionRosterDefaultLimit
	}
	if limit > ReactionRosterMaxLimit {
		return ReactionRosterMaxLimit
	}
	return limit
}

func clampRosterOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > ReactionRosterMaxOffset {
		return ReactionRosterMaxOffset
	}
	return offset
}
