package forum

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// BookmarkWriteInput 是一次 PUT /…/bookmark 的请求体。
//
// Visibility 允许为空：空串表示「仅补行/维持现状」（幂等重放）。
// 提醒是三态字段：
//   - RemindAt 非空 → 设定/更新提醒时间（并清掉 reminded_at）；
//   - ClearReminder=true → 清空 remind_at 与 reminded_at；
//   - 两者都没有 → 保持原值。
//
// 同时给出两者会被 service 拒绝（ErrBookmarkReminderConflict）。取消收藏时
// 整个输入被忽略。
type BookmarkWriteInput struct {
	Visibility    string     `json:"visibility,omitempty"`
	RemindAt      *time.Time `json:"remindAt,omitempty"`
	ClearReminder bool       `json:"clearReminder,omitempty"`
}

// BookmarkService 是收藏域的聚焦协作者。
//
// 与 ReactionService 同一形状：持有 *Service 以复用公开可见性规则，自己的依赖
// 放在自己身上。刻意不是 *Service 的接收者方法集（AGENTS.md 非增长基线）。
type BookmarkService struct {
	service  *Service
	store    BookmarkStore
	features FeatureGate
	// now 让提醒窗口校验可测；生产路径是 time.Now。
	now func() time.Time
}

func NewBookmarkService(service *Service) *BookmarkService {
	bookmarks := &BookmarkService{service: service, now: time.Now}
	if service != nil {
		// 生产装配里 store 是 *CachedStore（实现了 BookmarkStore），缓存失效随之生效。
		if store, ok := service.store.(BookmarkStore); ok {
			bookmarks.store = store
		}
	}
	return bookmarks
}

// WithStore 覆盖收藏存储；nil 时保持构造时的断言结果。
func (s *BookmarkService) WithStore(store BookmarkStore) *BookmarkService {
	if s == nil || store == nil {
		return s
	}
	s.store = store
	return s
}

// WithNowFunc 覆盖时钟（仅测试用）；nil 时回到 time.Now。
func (s *BookmarkService) WithNowFunc(now func() time.Time) *BookmarkService {
	if s == nil || now == nil {
		return s
	}
	s.now = now
	return s
}

// WithFeatureGate 注入站点产品开关。未装配时视为开启。
func (s *BookmarkService) WithFeatureGate(gate FeatureGate) *BookmarkService {
	if s == nil {
		return s
	}
	s.features = gate
	return s
}

func (s *BookmarkService) BookmarkTopic(ctx context.Context, actor identity.Actor, topicID int64, input BookmarkWriteInput) (BookmarkState, error) {
	return s.applyTopic(ctx, actor, topicID, true, input)
}

func (s *BookmarkService) UnbookmarkTopic(ctx context.Context, actor identity.Actor, topicID int64) (BookmarkState, error) {
	return s.applyTopic(ctx, actor, topicID, false, BookmarkWriteInput{})
}

func (s *BookmarkService) BookmarkComment(ctx context.Context, actor identity.Actor, commentID int64, input BookmarkWriteInput) (BookmarkState, error) {
	return s.applyComment(ctx, actor, commentID, true, input)
}

func (s *BookmarkService) UnbookmarkComment(ctx context.Context, actor identity.Actor, commentID int64) (BookmarkState, error) {
	return s.applyComment(ctx, actor, commentID, false, BookmarkWriteInput{})
}

// ListUserBookmarks 返回行主的收藏名单；本人可见私密行，他人只见公开行。
// 实现 forum.BookmarkLister，供 Profile 域经 bootstrap 注入。
func (s *BookmarkService) ListUserBookmarks(ctx context.Context, viewerID, ownerUserID int64, limit, offset int) (BookmarkList, error) {
	if err := s.enabled(ctx); err != nil {
		return BookmarkList{}, err
	}
	if s == nil || s.store == nil || s.service == nil {
		return BookmarkList{}, ErrInvalidBookmarkTarget
	}
	if ownerUserID <= 0 {
		return BookmarkList{}, ErrInvalidBookmarkTarget
	}
	return s.store.ListUserBookmarks(ctx, ownerUserID, viewerID == ownerUserID, clampBookmarkLimit(limit), clampBookmarkOffset(offset))
}

func (s *BookmarkService) applyTopic(ctx context.Context, actor identity.Actor, topicID int64, add bool, input BookmarkWriteInput) (BookmarkState, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return BookmarkState{}, err
	}
	if topicID <= 0 {
		return BookmarkState{}, ErrTopicNotFound
	}
	topic, err := s.service.store.GetTopic(ctx, topicID)
	if err != nil {
		return BookmarkState{}, err
	}
	if err := s.validateReminder(input); err != nil {
		return BookmarkState{}, err
	}
	state, err := s.store.ApplyBookmark(ctx, BookmarkApplyInput{
		ActorUserID:   actor.ID,
		TargetType:    BookmarkTargetTopic,
		TargetID:      topic.ID,
		TopicID:       topic.ID,
		Visibility:    input.Visibility,
		RemindAt:      input.RemindAt,
		ClearReminder: input.ClearReminder,
		Add:           add,
	})
	if err != nil {
		return BookmarkState{}, err
	}
	s.emit(ctx, actor, state)
	return state, nil
}

func (s *BookmarkService) applyComment(ctx context.Context, actor identity.Actor, commentID int64, add bool, input BookmarkWriteInput) (BookmarkState, error) {
	if err := s.authorize(ctx, actor); err != nil {
		return BookmarkState{}, err
	}
	comment, err := s.visibleComment(ctx, commentID)
	if err != nil {
		return BookmarkState{}, err
	}
	if err := s.validateReminder(input); err != nil {
		return BookmarkState{}, err
	}
	state, err := s.store.ApplyBookmark(ctx, BookmarkApplyInput{
		ActorUserID:   actor.ID,
		TargetType:    BookmarkTargetComment,
		TargetID:      comment.ID,
		TopicID:       comment.TopicID,
		Visibility:    input.Visibility,
		RemindAt:      input.RemindAt,
		ClearReminder: input.ClearReminder,
		Add:           add,
	})
	if err != nil {
		return BookmarkState{}, err
	}
	s.emit(ctx, actor, state)
	return state, nil
}

// validateReminder 在 service 层校验提醒请求（不是只在 controller 里校验，
// 否则其它调用方可以绕过）。
//
// 三态语义由这里把关：既要清除又要设置是自相矛盾的请求，必须拒绝而不是
// 猜一个优先级——静默丢弃其中一个会让用户看到「已保存」但值没变。
func (s *BookmarkService) validateReminder(input BookmarkWriteInput) error {
	if input.ClearReminder && input.RemindAt != nil {
		return ErrBookmarkReminderConflict
	}
	if input.RemindAt == nil {
		return nil
	}
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	current := now().UTC()
	remindAt := input.RemindAt.UTC()
	if remindAt.Before(current.Add(-BookmarkReminderClockSkew)) {
		return ErrBookmarkReminderInPast
	}
	if remindAt.After(current.AddDate(0, 0, BookmarkReminderMaxAheadDays)) {
		return ErrBookmarkReminderTooFar
	}
	return nil
}

// visibleComment 复用公开评论的可见性口径：评论 active，且所属主题仍公开可见。
// 与 ReactionService.visibleComment 同姿态：隐藏主题下的评论归一成「评论不存在」，
// 避免存在性预言机。
func (s *BookmarkService) visibleComment(ctx context.Context, commentID int64) (CommentSummary, error) {
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
		if errors.Is(err, ErrTopicNotFound) {
			return CommentSummary{}, ErrCommentNotFound
		}
		return CommentSummary{}, err
	}
	return comment, nil
}

func (s *BookmarkService) authorize(ctx context.Context, actor identity.Actor) error {
	if s == nil || s.store == nil {
		return ErrInvalidBookmarkTarget
	}
	if actor.ID <= 0 || actor.Status != identity.UserStatusActive {
		return identity.ErrPermissionDenied
	}
	return s.enabled(ctx)
}

func (s *BookmarkService) enabled(ctx context.Context) error {
	if s == nil || s.features == nil {
		return nil
	}
	enabled, err := s.features.IsFeatureEnabled(ctx, NameFeatureBookmarks)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrBookmarksDisabled
	}
	return nil
}

// emit 在提交后发出观察事件；事件是派生面，失败不阻断已成立的收藏。
func (s *BookmarkService) emit(ctx context.Context, actor identity.Actor, state BookmarkState) {
	if s == nil || s.service == nil || s.service.events == nil {
		return
	}
	name := appevents.BookmarkRemoved
	if state.Bookmarked {
		name = appevents.BookmarkCreated
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
			"visibility":  state.Visibility,
			"actorUserId": actor.ID,
		},
		OccurredAt: time.Now().UTC(),
	})
}

// DecorateTopic 为公开详情补上「当前访客是否已收藏」。fail-open：查询失败只
// 意味着没有 bookmarkedByMe，不能把公开读打成 500。
func (s *BookmarkService) DecorateTopic(ctx context.Context, viewer identity.Actor, topic *TopicDetail) error {
	if s == nil || s.store == nil || topic == nil || viewer.ID <= 0 {
		return nil
	}
	marked, err := s.store.BookmarkedTargetIDs(ctx, viewer.ID, BookmarkTargetTopic, []int64{topic.ID})
	if err != nil {
		slog.WarnContext(ctx, "forum bookmarks: decorate topic failed", "topicId", topic.ID, "err", err)
		return nil
	}
	_, topic.BookmarkedByMe = marked[topic.ID]
	return nil
}

// DecorateComments 一次性补齐一页评论（含嵌套子评论）的 bookmarkedByMe。
// 与 DecorateTopic 同姿态：整页一次批量查询，fail-open。
func (s *BookmarkService) DecorateComments(ctx context.Context, viewer identity.Actor, comments []Comment) error {
	if s == nil || s.store == nil || len(comments) == 0 || viewer.ID <= 0 {
		return nil
	}
	ids := collectCommentIDs(comments, nil)
	if len(ids) == 0 {
		return nil
	}
	marked, err := s.store.BookmarkedTargetIDs(ctx, viewer.ID, BookmarkTargetComment, ids)
	if err != nil {
		slog.WarnContext(ctx, "forum bookmarks: decorate comments failed", "count", len(ids), "err", err)
		return nil
	}
	applyCommentBookmarked(comments, marked)
	return nil
}

func applyCommentBookmarked(comments []Comment, marked map[int64]struct{}) {
	for i := range comments {
		_, comments[i].BookmarkedByMe = marked[comments[i].ID]
		applyCommentBookmarked(comments[i].Children, marked)
	}
}

func clampBookmarkLimit(limit int) int {
	if limit <= 0 {
		return BookmarkListDefaultLimit
	}
	if limit > BookmarkListMaxLimit {
		return BookmarkListMaxLimit
	}
	return limit
}

func clampBookmarkOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > BookmarkListMaxOffset {
		return BookmarkListMaxOffset
	}
	return offset
}
