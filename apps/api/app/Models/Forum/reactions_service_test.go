package forum

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// reactionTestStore 复用 serviceFakeStore 提供 Store 的其余方法，只覆盖点赞域
// 与两条可见性读取。生产装配里这两件事都在 *CachedStore 上。
type reactionTestStore struct {
	*serviceFakeStore
	topic   TopicDetail
	comment CommentSummary
	topicOK bool
	// applied 记录每次切换；reply 决定「这次是否真的改变了关系」。
	applied []ApplyReactionRecord
	state   ReactionState
	err     error
	// previews / previewErr 供 ReactionPreviews fake 返回。
	previews   map[int64][]UserSummary
	previewErr error
}

func (s *reactionTestStore) GetTopic(context.Context, int64) (TopicDetail, error) {
	if !s.topicOK {
		return TopicDetail{}, ErrTopicNotFound
	}
	return s.topic, nil
}

func (s *reactionTestStore) GetCommentSummary(context.Context, int64) (CommentSummary, error) {
	if s.comment.ID == 0 {
		return CommentSummary{}, ErrCommentNotFound
	}
	return s.comment, nil
}

func (s *reactionTestStore) ApplyReaction(_ context.Context, input ApplyReactionRecord) (ReactionState, error) {
	if s.err != nil {
		return ReactionState{}, s.err
	}
	s.applied = append(s.applied, input)
	return s.state, nil
}

func (s *reactionTestStore) LikedTargetIDs(context.Context, int64, string, []int64) (map[int64]struct{}, error) {
	return map[int64]struct{}{}, nil
}

func (s *reactionTestStore) ListReactionUsers(context.Context, string, int64, int, int) (ReactionRoster, error) {
	return ReactionRoster{Items: []UserSummary{}}, nil
}

// ReactionPreviews 让 fake 满足 ReactionPreviewStore；预览为空表示无点赞者。
func (s *reactionTestStore) ReactionPreviews(context.Context, string, []int64, int64, int) (map[int64][]UserSummary, error) {
	if s.previewErr != nil {
		return nil, s.previewErr
	}
	return s.previews, nil
}

type reactionTestFeatureGate struct {
	enabled bool
	err     error
	calls   int
}

func (g *reactionTestFeatureGate) IsFeatureEnabled(context.Context, string) (bool, error) {
	g.calls++
	return g.enabled, g.err
}

func newReactionTestService(store *reactionTestStore) *ReactionService {
	service := NewService(ServiceConfig{Store: store})
	return NewReactionService(service).WithStore(store)
}

func reactionActor(id int64) identity.Actor {
	return identity.Actor{ID: id, Status: identity.UserStatusActive}
}

func TestReactionServiceLikeTopicPassesRecipientAndReaction(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7, AuthorUserID: 42}},
		topicOK:          true,
		state:            ReactionState{TargetType: ReactionTargetTopic, TargetID: 7, Reaction: ReactionLike, Liked: true, LikeCount: 3},
	}
	service := newReactionTestService(store)

	state, err := service.LikeTopic(context.Background(), reactionActor(9), 7)
	if err != nil {
		t.Fatalf("LikeTopic: %v", err)
	}
	if !state.Liked || state.LikeCount != 3 {
		t.Fatalf("unexpected state: %#v", state)
	}
	if len(store.applied) != 1 {
		t.Fatalf("expected one applied reaction, got %d", len(store.applied))
	}
	applied := store.applied[0]
	if applied.RecipientUserID != 42 || applied.ActorUserID != 9 || !applied.Add || applied.TopicID != 7 {
		t.Fatalf("unexpected record: %#v", applied)
	}
}

func TestReactionServiceRejectsHiddenTopic(t *testing.T) {
	store := &reactionTestStore{serviceFakeStore: newServiceFakeStore()}
	service := newReactionTestService(store)

	if _, err := service.LikeTopic(context.Background(), reactionActor(9), 7); !errors.Is(err, ErrTopicNotFound) {
		t.Fatalf("want ErrTopicNotFound, got %v", err)
	}
	if len(store.applied) != 0 {
		t.Fatal("hidden topic must not reach the store")
	}
}

func TestReactionServiceRejectsInactiveComment(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7, AuthorUserID: 42}},
		topicOK:          true,
		comment:          CommentSummary{ID: 11, TopicID: 7, Status: CommentStatusDeleted},
	}
	service := newReactionTestService(store)

	if _, err := service.LikeComment(context.Background(), reactionActor(9), 11); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("want ErrCommentNotFound, got %v", err)
	}
	if len(store.applied) != 0 {
		t.Fatal("deleted comment must not reach the store")
	}
}

func TestReactionServiceLikeCommentUsesCommentTarget(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7, AuthorUserID: 42}},
		topicOK:          true,
		comment:          CommentSummary{ID: 11, TopicID: 7, AuthorUserID: 5, Status: CommentStatusActive},
		state:            ReactionState{TargetType: ReactionTargetComment, TargetID: 11, Reaction: ReactionLike, Liked: true, LikeCount: 1},
	}
	service := newReactionTestService(store)

	if _, err := service.LikeComment(context.Background(), reactionActor(9), 11); err != nil {
		t.Fatalf("LikeComment: %v", err)
	}
	applied := store.applied[0]
	if applied.TargetType != ReactionTargetComment || applied.TargetID != 11 || applied.TopicID != 7 || applied.RecipientUserID != 5 {
		t.Fatalf("unexpected record: %#v", applied)
	}
}

func TestReactionServiceRequiresActiveActor(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7, AuthorUserID: 42}},
		topicOK:          true,
	}
	service := newReactionTestService(store)

	for _, actor := range []identity.Actor{{ID: 0, Status: identity.UserStatusActive}, {ID: 9, Status: identity.UserStatusBanned}} {
		if _, err := service.LikeTopic(context.Background(), actor, 7); !errors.Is(err, identity.ErrPermissionDenied) {
			t.Fatalf("actor %#v: want permission denied, got %v", actor, err)
		}
	}
}

func TestReactionServiceHonoursFeatureGate(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		topic:            TopicDetail{TopicSummary: TopicSummary{ID: 7, AuthorUserID: 42}},
		topicOK:          true,
	}
	gate := &reactionTestFeatureGate{enabled: false}
	service := newReactionTestService(store).WithFeatureGate(gate)

	if _, err := service.LikeTopic(context.Background(), reactionActor(9), 7); !errors.Is(err, ErrReactionsDisabled) {
		t.Fatalf("want ErrReactionsDisabled, got %v", err)
	}
	if len(store.applied) != 0 {
		t.Fatal("disabled reactions must not reach the store")
	}

	gate.enabled = true
	if _, err := service.LikeTopic(context.Background(), reactionActor(9), 7); err != nil {
		t.Fatalf("enabled LikeTopic: %v", err)
	}
}

func TestReactionServiceDecorateTopicSkipsAnonymous(t *testing.T) {
	store := &reactionTestStore{serviceFakeStore: newServiceFakeStore()}
	service := newReactionTestService(store)
	topic := TopicDetail{TopicSummary: TopicSummary{ID: 7}}

	if err := service.DecorateTopic(context.Background(), identity.Actor{}, &topic); err != nil {
		t.Fatalf("DecorateTopic: %v", err)
	}
	if topic.LikedByMe {
		t.Fatal("anonymous viewers must never get likedByMe")
	}
}

func TestReactionServiceDecorateCommentsWalksChildren(t *testing.T) {
	store := &reactionTestStore{serviceFakeStore: newServiceFakeStore()}
	// 只声明第 11 条被赞；装饰器必须递归到嵌套子评论。
	store.state = ReactionState{}
	service := NewReactionService(NewService(ServiceConfig{Store: store})).WithStore(&likedTargetsStore{
		reactionTestStore: store,
		liked:             map[int64]struct{}{11: {}},
	})
	comments := []Comment{{ID: 11}, {ID: 12, Children: []Comment{{ID: 13}}}}

	if err := service.DecorateComments(context.Background(), reactionActor(9), comments); err != nil {
		t.Fatalf("DecorateComments: %v", err)
	}
	if !comments[0].LikedByMe {
		t.Fatal("root comment should be liked")
	}
	if comments[1].LikedByMe || comments[1].Children[0].LikedByMe {
		t.Fatal("unliked comments must stay false")
	}
}

// likedTargetsStore 只覆盖批量查询，返回固定集合。
type likedTargetsStore struct {
	*reactionTestStore
	liked map[int64]struct{}
}

func (s *likedTargetsStore) LikedTargetIDs(context.Context, int64, string, []int64) (map[int64]struct{}, error) {
	return s.liked, nil
}

// TestReactionServiceDecorateTopicFillsPreviewForAnonymous 固定 M1：匿名访客
// 跳过 likedByMe，但头像组预览照常补齐（首屏 SSR 不再额外请求）。
func TestReactionServiceDecorateTopicFillsPreviewForAnonymous(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		previews: map[int64][]UserSummary{
			7: {{ID: 3}, {ID: 5}},
		},
	}
	service := newReactionTestService(store)
	topic := TopicDetail{TopicSummary: TopicSummary{ID: 7}}

	if err := service.DecorateTopic(context.Background(), identity.Actor{}, &topic); err != nil {
		t.Fatalf("DecorateTopic: %v", err)
	}
	if topic.LikedByMe {
		t.Fatal("anonymous viewers must never get likedByMe")
	}
	if len(topic.LikedBy) != 2 || topic.LikedBy[0].ID != 3 {
		t.Fatalf("anonymous topic preview = %+v, want two likers", topic.LikedBy)
	}
}

// TestReactionServiceDecorateTopicLoggedInKeepsSelfFirst 固定登录访客：likedByMe
// 与预览并存，预览里自己排第一（排序口径由 SQL 保证，这里锁定透传契约）。
func TestReactionServiceDecorateTopicLoggedInKeepsSelfFirst(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		previews: map[int64][]UserSummary{
			7: {{ID: 9}, {ID: 3}},
		},
	}
	service := newReactionTestService(store).WithStore(&likedTargetsStore{
		reactionTestStore: store,
		liked:             map[int64]struct{}{7: {}},
	})
	topic := TopicDetail{TopicSummary: TopicSummary{ID: 7}}

	if err := service.DecorateTopic(context.Background(), reactionActor(9), &topic); err != nil {
		t.Fatalf("DecorateTopic: %v", err)
	}
	if !topic.LikedByMe {
		t.Fatal("logged-in liker should see likedByMe")
	}
	if len(topic.LikedBy) != 2 || topic.LikedBy[0].ID != 9 {
		t.Fatalf("preview = %+v, want self (9) first", topic.LikedBy)
	}
}

// TestReactionServiceDecorateCommentsFillsPreviewForAnonymous 固定评论预览：
// 含嵌套子评论，一次批量查询覆盖整页。
func TestReactionServiceDecorateCommentsFillsPreviewForAnonymous(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		previews: map[int64][]UserSummary{
			11: {{ID: 4}},
			13: {{ID: 6}},
		},
	}
	service := newReactionTestService(store)
	comments := []Comment{{ID: 11}, {ID: 12, Children: []Comment{{ID: 13}}}}

	if err := service.DecorateComments(context.Background(), identity.Actor{}, comments); err != nil {
		t.Fatalf("DecorateComments: %v", err)
	}
	if len(comments[0].LikedBy) != 1 || comments[0].LikedBy[0].ID != 4 {
		t.Fatalf("root preview = %+v", comments[0].LikedBy)
	}
	if comments[1].LikedBy != nil {
		t.Fatalf("unliked comment must carry no preview: %+v", comments[1].LikedBy)
	}
	if len(comments[1].Children[0].LikedBy) != 1 || comments[1].Children[0].LikedBy[0].ID != 6 {
		t.Fatalf("nested child preview = %+v", comments[1].Children[0].LikedBy)
	}
}

// TestReactionServiceDecoratePreviewFailOpen 固定 fail-open：预览查询失败只
// 告警返回，不把公开读打成 500；likedByMe 不受影响。
func TestReactionServiceDecoratePreviewFailOpen(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		previewErr:       errors.New("preview boom"),
	}
	service := newReactionTestService(store).WithStore(&likedTargetsStore{
		reactionTestStore: store,
		liked:             map[int64]struct{}{7: {}},
	})
	topic := TopicDetail{TopicSummary: TopicSummary{ID: 7}}

	if err := service.DecorateTopic(context.Background(), reactionActor(9), &topic); err != nil {
		t.Fatalf("preview failure must fail open, got %v", err)
	}
	if !topic.LikedByMe {
		t.Fatal("likedByMe decoration must survive preview failure")
	}
	if topic.LikedBy != nil {
		t.Fatalf("failed preview must leave LikedBy empty: %+v", topic.LikedBy)
	}

	comments := []Comment{{ID: 11}}
	if err := service.DecorateComments(context.Background(), identity.Actor{}, comments); err != nil {
		t.Fatalf("comments preview failure must fail open, got %v", err)
	}
	if comments[0].LikedBy != nil {
		t.Fatalf("failed comments preview must leave LikedBy empty: %+v", comments[0].LikedBy)
	}
}

// failingLikedStore 让 LikedTargetIDs 报错，固定「likedByMe 失败不得短路预览」。
type failingLikedStore struct {
	*reactionTestStore
}

func (s *failingLikedStore) LikedTargetIDs(context.Context, int64, string, []int64) (map[int64]struct{}, error) {
	return nil, errors.New("liked targets boom")
}

// TestReactionServiceDecorateKeepsPreviewWhenLikedByMeFails 固定 P2-1：
// LikedTargetIDs 失败时预览仍然被填充（两条装饰路径各自 fail-open，互不短路），
// 公开读也不返回错误。
func TestReactionServiceDecorateKeepsPreviewWhenLikedByMeFails(t *testing.T) {
	store := &reactionTestStore{
		serviceFakeStore: newServiceFakeStore(),
		previews: map[int64][]UserSummary{
			7:  {{ID: 9}, {ID: 3}},
			11: {{ID: 4}},
			13: {{ID: 6}},
		},
	}
	service := NewReactionService(NewService(ServiceConfig{Store: store})).WithStore(&failingLikedStore{reactionTestStore: store})
	viewer := reactionActor(9)

	topic := TopicDetail{TopicSummary: TopicSummary{ID: 7}}
	if err := service.DecorateTopic(context.Background(), viewer, &topic); err != nil {
		t.Fatalf("likedByMe failure must fail open, got %v", err)
	}
	if topic.LikedByMe {
		t.Fatal("failed likedByMe lookup must leave LikedByMe false")
	}
	if len(topic.LikedBy) != 2 || topic.LikedBy[0].ID != 9 {
		t.Fatalf("preview must survive likedByMe failure: %+v", topic.LikedBy)
	}

	comments := []Comment{{ID: 11}, {ID: 12, Children: []Comment{{ID: 13}}}}
	if err := service.DecorateComments(context.Background(), viewer, comments); err != nil {
		t.Fatalf("likedByMe failure must fail open for comments, got %v", err)
	}
	if len(comments[0].LikedBy) != 1 || comments[0].LikedBy[0].ID != 4 {
		t.Fatalf("root preview must survive likedByMe failure: %+v", comments[0].LikedBy)
	}
	if len(comments[1].Children[0].LikedBy) != 1 || comments[1].Children[0].LikedBy[0].ID != 6 {
		t.Fatalf("nested preview must survive likedByMe failure: %+v", comments[1].Children[0].LikedBy)
	}
}
