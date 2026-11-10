package forum

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// bookmarkTestStore 复用 serviceFakeStore 提供 Store 的其余方法，只覆盖收藏域
// 与两条可见性读取。生产装配里这两件事都在 *CachedStore 上。
type bookmarkTestStore struct {
	*serviceFakeStore
	topic   TopicDetail
	comment CommentSummary
	topicOK bool

	applied []BookmarkApplyInput
	state   BookmarkState
	err     error

	markedCalls [][]int64
	marked      map[int64]struct{}

	list      BookmarkList
	listCalls int
}

func (s *bookmarkTestStore) GetTopic(context.Context, int64) (TopicDetail, error) {
	if !s.topicOK {
		return TopicDetail{}, ErrTopicNotFound
	}
	return s.topic, nil
}

func (s *bookmarkTestStore) GetCommentSummary(context.Context, int64) (CommentSummary, error) {
	if s.comment.ID == 0 {
		return CommentSummary{}, ErrCommentNotFound
	}
	return s.comment, nil
}

func (s *bookmarkTestStore) ApplyBookmark(_ context.Context, input BookmarkApplyInput) (BookmarkState, error) {
	if s.err != nil {
		return BookmarkState{}, s.err
	}
	s.applied = append(s.applied, input)
	return s.state, nil
}

func (s *bookmarkTestStore) BookmarkedTargetIDs(_ context.Context, _ int64, _ string, targetIDs []int64) (map[int64]struct{}, error) {
	s.markedCalls = append(s.markedCalls, append([]int64(nil), targetIDs...))
	return s.marked, nil
}

func (s *bookmarkTestStore) ListUserBookmarks(context.Context, int64, bool, int, int) (BookmarkList, error) {
	s.listCalls++
	return s.list, nil
}

type bookmarkTestFeatureGate struct {
	enabled bool
	err     error
	names   []string
}

func (g *bookmarkTestFeatureGate) IsFeatureEnabled(_ context.Context, name string) (bool, error) {
	g.names = append(g.names, name)
	return g.enabled, g.err
}

func newBookmarkTestService(store *bookmarkTestStore) *BookmarkService {
	service := NewService(ServiceConfig{Store: store})
	return NewBookmarkService(service).WithStore(store)
}

func bookmarkActor(id int64) identity.Actor {
	return identity.Actor{ID: id, Status: identity.UserStatusActive}
}

// TestBookmarkServicePutIsIdempotentAndCarriesVisibility 固定服务层的幂等口径：
// 重复 PUT 仍然只表达「新增/维持」意图（Add=true），可见性与提醒原样下传，
// 真正的去重由 store 的唯一约束 + ON CONFLICT DO NOTHING 完成。
func TestBookmarkServicePutIsIdempotentAndCarriesVisibility(t *testing.T) {
	store := &bookmarkTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7, AuthorUserID: 42}},
		topicOK:          true,
		state:            BookmarkState{TargetType: BookmarkTargetTopic, TargetID: 7, Visibility: BookmarkVisibilityPublic, Bookmarked: true, PublicBookmarkCount: 1},
	}
	service := newBookmarkTestService(store)

	for i := 0; i < 2; i++ {
		state, err := service.BookmarkTopic(context.Background(), bookmarkActor(9), 7, BookmarkWriteInput{Visibility: BookmarkVisibilityPublic})
		if err != nil {
			t.Fatalf("BookmarkTopic #%d: %v", i, err)
		}
		if !state.Bookmarked || state.Visibility != BookmarkVisibilityPublic {
			t.Fatalf("unexpected state #%d: %#v", i, state)
		}
	}
	if len(store.applied) != 2 {
		t.Fatalf("expected two store calls, got %d", len(store.applied))
	}
	for _, input := range store.applied {
		if !input.Add || input.Visibility != BookmarkVisibilityPublic || input.TargetType != BookmarkTargetTopic {
			t.Fatalf("unexpected apply input: %#v", input)
		}
		if input.ActorUserID != 9 || input.TargetID != 7 || input.TopicID != 7 {
			t.Fatalf("unexpected apply identity: %#v", input)
		}
	}
}

// TestBookmarkServiceDeleteAlwaysAsksStoreToRemove 固定删除语义：删除只表达
// 「移除」，不携带可见性；不存在的行由 store 幂等返回当前状态。
func TestBookmarkServiceDeleteAlwaysAsksStoreToRemove(t *testing.T) {
	store := &bookmarkTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7}},
		topicOK:          true,
		state:            BookmarkState{TargetType: BookmarkTargetTopic, TargetID: 7, Visibility: BookmarkVisibilityPrivate, Bookmarked: false},
	}
	service := newBookmarkTestService(store)

	state, err := service.UnbookmarkTopic(context.Background(), bookmarkActor(9), 7)
	if err != nil {
		t.Fatalf("UnbookmarkTopic: %v", err)
	}
	if state.Bookmarked {
		t.Fatalf("delete must report bookmarked=false: %#v", state)
	}
	if len(store.applied) != 1 || store.applied[0].Add || store.applied[0].Visibility != "" {
		t.Fatalf("unexpected apply input: %#v", store.applied)
	}
}

func TestBookmarkServiceRejectsAnonymousAndDisabledFeature(t *testing.T) {
	store := &bookmarkTestStore{serviceFakeStore: newServiceFakeStore(), topicOK: true}
	service := newBookmarkTestService(store)

	if _, err := service.BookmarkTopic(context.Background(), identity.Actor{}, 7, BookmarkWriteInput{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("want permission denied for anonymous, got %v", err)
	}

	gate := &bookmarkTestFeatureGate{enabled: false}
	service = service.WithFeatureGate(gate)
	if _, err := service.BookmarkTopic(context.Background(), bookmarkActor(9), 7, BookmarkWriteInput{}); !errors.Is(err, ErrBookmarksDisabled) {
		t.Fatalf("want ErrBookmarksDisabled, got %v", err)
	}
	if len(gate.names) != 1 || gate.names[0] != NameFeatureBookmarks {
		t.Fatalf("feature gate names = %#v", gate.names)
	}
	if len(store.applied) != 0 {
		t.Fatal("disabled feature must not reach the store")
	}
}

func TestBookmarkServiceRejectsInvisibleTargets(t *testing.T) {
	store := &bookmarkTestStore{serviceFakeStore: newServiceFakeStore()}
	service := newBookmarkTestService(store)

	if _, err := service.BookmarkTopic(context.Background(), bookmarkActor(9), 7, BookmarkWriteInput{}); !errors.Is(err, ErrTopicNotFound) {
		t.Fatalf("want ErrTopicNotFound, got %v", err)
	}
	if _, err := service.BookmarkComment(context.Background(), bookmarkActor(9), 11, BookmarkWriteInput{}); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("want ErrCommentNotFound, got %v", err)
	}
	if len(store.applied) != 0 {
		t.Fatal("invisible targets must not reach the store")
	}
}

// TestBookmarkServiceCommentOnHiddenTopicNormalizesToNotFound 固定存在性预言机
// 的封堵：主题不可见时评论归一成「评论不存在」。
func TestBookmarkServiceCommentOnHiddenTopicNormalizesToNotFound(t *testing.T) {
	store := &bookmarkTestStore{
		serviceFakeStore: newServiceFakeStore(),
		comment:          CommentSummary{ID: 11, TopicID: 99, Status: CommentStatusActive},
	}
	service := newBookmarkTestService(store)

	if _, err := service.BookmarkComment(context.Background(), bookmarkActor(9), 11, BookmarkWriteInput{}); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("want ErrCommentNotFound, got %v", err)
	}
}

// TestBookmarkServiceDecoratesWholePageWithOneBatchQuery 固定「整页一次批量查询」：
// 一页评论（含嵌套子评论）只允许一次 BookmarkedTargetIDs 调用。
func TestBookmarkServiceDecoratesWholePageWithOneBatchQuery(t *testing.T) {
	store := &bookmarkTestStore{
		serviceFakeStore: newServiceFakeStore(),
		marked:           map[int64]struct{}{2: {}},
	}
	service := newBookmarkTestService(store)

	comments := []Comment{
		{ID: 1},
		{ID: 2, Children: []Comment{{ID: 3}}},
	}
	if err := service.DecorateComments(context.Background(), bookmarkActor(9), comments); err != nil {
		t.Fatalf("DecorateComments: %v", err)
	}
	if len(store.markedCalls) != 1 {
		t.Fatalf("expected one batch query, got %d", len(store.markedCalls))
	}
	if len(store.markedCalls[0]) != 3 {
		t.Fatalf("batch ids = %#v", store.markedCalls[0])
	}
	if !comments[1].BookmarkedByMe || comments[0].BookmarkedByMe || comments[1].Children[0].BookmarkedByMe {
		t.Fatalf("decoration mismatch: %#v", comments)
	}
}

// TestBookmarkServiceDecorationSkipsAnonymous 固定匿名路径：不发查询、不改模型。
func TestBookmarkServiceDecorationSkipsAnonymous(t *testing.T) {
	store := &bookmarkTestStore{serviceFakeStore: newServiceFakeStore()}
	service := newBookmarkTestService(store)

	topic := TopicDetail{TopicSummary: TopicSummary{ID: 7}}
	if err := service.DecorateTopic(context.Background(), identity.Actor{}, &topic); err != nil {
		t.Fatalf("DecorateTopic: %v", err)
	}
	if len(store.markedCalls) != 0 {
		t.Fatalf("anonymous decoration must not query: %#v", store.markedCalls)
	}
	if topic.BookmarkedByMe {
		t.Fatal("anonymous viewer must never get bookmarkedByMe")
	}
}

// TestBookmarkServiceRejectsBannedActor 固定封禁账号不能写收藏：与点赞域同一
// 口径（Status != active → permission denied），且请求不会到达 store。
func TestBookmarkServiceRejectsBannedActor(t *testing.T) {
	store := &bookmarkTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7}},
		topicOK:          true,
	}
	service := newBookmarkTestService(store)

	for _, actor := range []identity.Actor{
		{ID: 0, Status: identity.UserStatusActive},
		{ID: 9, Status: identity.UserStatusBanned},
		{ID: 9, Status: identity.UserStatusDisabled},
	} {
		if _, err := service.BookmarkTopic(context.Background(), actor, 7, BookmarkWriteInput{}); !errors.Is(err, identity.ErrPermissionDenied) {
			t.Fatalf("actor %#v: want permission denied, got %v", actor, err)
		}
	}
	if len(store.applied) != 0 {
		t.Fatalf("rejected actors must not reach the store, got %d", len(store.applied))
	}
}

// TestBookmarkServiceClearReminderReachesStore 固定清除分支：clearReminder 必须
// 原样下传给 store（旧实现丢掉了它，于是提醒永远清不掉）。
func TestBookmarkServiceClearReminderReachesStore(t *testing.T) {
	store := &bookmarkTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7}},
		topicOK:          true,
		state:            BookmarkState{TargetType: BookmarkTargetTopic, TargetID: 7, Visibility: BookmarkVisibilityPrivate, Bookmarked: true},
	}
	service := newBookmarkTestService(store)

	if _, err := service.BookmarkTopic(context.Background(), bookmarkActor(9), 7, BookmarkWriteInput{ClearReminder: true}); err != nil {
		t.Fatalf("BookmarkTopic(clear): %v", err)
	}
	if len(store.applied) != 1 || !store.applied[0].ClearReminder || store.applied[0].RemindAt != nil {
		t.Fatalf("clearReminder must reach the store alone: %#v", store.applied)
	}
}

// TestBookmarkServiceRejectsClearAndSetTogether 固定三态冲突：同时给 remindAt 与
// clearReminder 必须 422，而不是猜一个优先级（静默丢弃会让前端显示「已保存」）。
func TestBookmarkServiceRejectsClearAndSetTogether(t *testing.T) {
	store := &bookmarkTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7}},
		topicOK:          true,
	}
	service := newBookmarkTestService(store)
	remindAt := time.Now().Add(time.Hour)

	_, err := service.BookmarkTopic(context.Background(), bookmarkActor(9), 7, BookmarkWriteInput{RemindAt: &remindAt, ClearReminder: true})
	if !errors.Is(err, ErrBookmarkReminderConflict) {
		t.Fatalf("want ErrBookmarkReminderConflict, got %v", err)
	}
	if len(store.applied) != 0 {
		t.Fatal("conflicting reminder request must not reach the store")
	}
}

// TestBookmarkServiceValidatesReminderWindow 固定提醒时间窗口：早于 now-60s 与
// 晚于 now+2y 都拒绝；窗口内的值原样下传。
func TestBookmarkServiceValidatesReminderWindow(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		offset  time.Duration
		wantErr error
	}{
		{name: "past beyond skew", offset: -2 * time.Minute, wantErr: ErrBookmarkReminderInPast},
		{name: "within clock skew", offset: -30 * time.Second},
		{name: "one hour ahead", offset: time.Hour},
		{name: "just inside two years", offset: 729 * 24 * time.Hour},
		{name: "beyond two years", offset: 731 * 24 * time.Hour, wantErr: ErrBookmarkReminderTooFar},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := &bookmarkTestStore{
				serviceFakeStore: newServiceFakeStore(),
				topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7}},
				topicOK:          true,
			}
			service := newBookmarkTestService(store).WithNowFunc(func() time.Time { return now })
			remindAt := now.Add(test.offset)

			_, err := service.BookmarkTopic(context.Background(), bookmarkActor(9), 7, BookmarkWriteInput{RemindAt: &remindAt})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("want %v, got %v", test.wantErr, err)
				}
				if len(store.applied) != 0 {
					t.Fatal("rejected reminder must not reach the store")
				}
				return
			}
			if err != nil {
				t.Fatalf("in-window reminder rejected: %v", err)
			}
			if len(store.applied) != 1 || store.applied[0].RemindAt == nil || !store.applied[0].RemindAt.Equal(remindAt) {
				t.Fatalf("reminder must reach the store unchanged: %#v", store.applied)
			}
		})
	}
}

// TestBookmarkServiceDeleteIgnoresReminderValidation 固定删除语义：取消收藏
// 不看提醒字段（三态只属于新增/更新）。
func TestBookmarkServiceDeleteIgnoresReminderValidation(t *testing.T) {
	store := &bookmarkTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7}},
		topicOK:          true,
		state:            BookmarkState{TargetType: BookmarkTargetTopic, TargetID: 7, Bookmarked: false},
	}
	service := newBookmarkTestService(store)

	if _, err := service.UnbookmarkTopic(context.Background(), bookmarkActor(9), 7); err != nil {
		t.Fatalf("UnbookmarkTopic: %v", err)
	}
	if len(store.applied) != 1 || store.applied[0].ClearReminder || store.applied[0].RemindAt != nil {
		t.Fatalf("delete must not carry reminder state: %#v", store.applied)
	}
}
