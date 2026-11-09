package forumcontroller

// 点赞（reactions）HTTP 层用例：名单端点的空名单形状、目标不可见/非法 ID 的
// 404 映射、产品开关关闭时的 403、写端点的登录要求。
//
// 这些用例只走 HTTP 传输层：可见性读取复用 controllerForumStore，反应存储换成
// 内存 fake，因此不需要数据库。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"testing"

	"github.com/gofiber/fiber/v3"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// reactionHTTPStore 只实现 ReactionStore 的四个方法，记录调用参数供断言。
type reactionHTTPStore struct {
	roster  forum.ReactionRoster
	listErr error

	state    forum.ReactionState
	applyErr error

	applied []forum.ApplyReactionRecord
	lists   []reactionHTTPListCall

	// previews / previewErr 供 ReactionPreviews fake 返回（M1 头像组预览）。
	previews   map[int64][]forum.UserSummary
	previewErr error
}

type reactionHTTPListCall struct {
	targetType string
	targetID   int64
	limit      int
	offset     int
}

func (s *reactionHTTPStore) ApplyReaction(_ context.Context, input forum.ApplyReactionRecord) (forum.ReactionState, error) {
	if s.applyErr != nil {
		return forum.ReactionState{}, s.applyErr
	}
	s.applied = append(s.applied, input)
	return s.state, nil
}

func (s *reactionHTTPStore) LikedTargetIDs(context.Context, int64, string, []int64) (map[int64]struct{}, error) {
	return map[int64]struct{}{}, nil
}

func (s *reactionHTTPStore) ListReactionUsers(_ context.Context, targetType string, targetID int64, limit, offset int) (forum.ReactionRoster, error) {
	s.lists = append(s.lists, reactionHTTPListCall{targetType: targetType, targetID: targetID, limit: limit, offset: offset})
	if s.listErr != nil {
		return forum.ReactionRoster{}, s.listErr
	}
	return s.roster, nil
}

func (s *reactionHTTPStore) ReactionPreviews(context.Context, string, []int64, int64, int) (map[int64][]forum.UserSummary, error) {
	if s.previewErr != nil {
		return nil, s.previewErr
	}
	return s.previews, nil
}

// reactionHTTPGate 记录被查询的开关名，用来锁定 features.reactions 的契约。
type reactionHTTPGate struct {
	enabled bool
	err     error
	names   []string
}

func (g *reactionHTTPGate) IsFeatureEnabled(_ context.Context, name string) (bool, error) {
	g.names = append(g.names, name)
	return g.enabled, g.err
}

// newReactionHTTPApp 在既有测试 app 上装配点赞协作者；gate 为 nil 时视为开关开启。
func newReactionHTTPApp(t *testing.T, gate forum.FeatureGate) (*fiber.App, *controllerForumStore, *reactionHTTPStore) {
	t.Helper()
	store := &reactionHTTPStore{}
	app, _, forumStore := newForumTestApp(func(controller *Controller) {
		reactions := forum.NewReactionService(controller.service).WithStore(store)
		if gate != nil {
			reactions = reactions.WithFeatureGate(gate)
		}
		controller.WithReactions(reactions)
	})
	return app, forumStore, store
}

func decodeReactionRoster(t *testing.T, resp *nethttp.Response) forum.ReactionRoster {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 roster, got %d body=%s", resp.StatusCode, body)
	}
	var envelope forumTestEnvelope[forum.ReactionRoster]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode roster: %v", err)
	}
	return envelope.Data
}

func assertReactionError(t *testing.T, resp *nethttp.Response, wantStatus int, wantReason string) {
	t.Helper()
	defer resp.Body.Close()
	var envelope forumTestEnvelope[forumTestErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if resp.StatusCode != wantStatus || envelope.Code != wantStatus || envelope.Data.Reason != wantReason {
		t.Fatalf("expected %d %q, got status=%d code=%d reason=%q",
			wantStatus, wantReason, resp.StatusCode, envelope.Code, envelope.Data.Reason)
	}
}

func TestReactionHTTPTopicLikesReturnsEmptyRoster(t *testing.T) {
	app, _, store := newReactionHTTPApp(t, nil)
	store.roster = forum.ReactionRoster{Items: []forum.UserSummary{}}

	roster := decodeReactionRoster(t, performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/likes", nil, nil))
	// items 必须是 [] 而不是 null：前端把空名单当数组消费。
	if roster.Items == nil || len(roster.Items) != 0 || roster.Total != 0 || roster.HasMore {
		t.Fatalf("unexpected empty roster %#v", roster)
	}
	if len(store.lists) != 1 {
		t.Fatalf("expected one roster read, got %d", len(store.lists))
	}
	call := store.lists[0]
	if call.targetType != forum.ReactionTargetTopic || call.targetID != 10 ||
		call.limit != forum.ReactionRosterDefaultLimit || call.offset != 0 {
		t.Fatalf("unexpected roster read %#v", call)
	}

	// HTTP 层分页参数经过 service 的边界钳制后才落库。
	roster = decodeReactionRoster(t, performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/likes?limit=500&offset=-5", nil, nil))
	if roster.Total != 0 {
		t.Fatalf("unexpected roster total %#v", roster)
	}
	if len(store.lists) != 2 {
		t.Fatalf("expected two roster reads, got %d", len(store.lists))
	}
	call = store.lists[1]
	if call.limit != forum.ReactionRosterMaxLimit || call.offset != 0 {
		t.Fatalf("expected clamped pagination, got %#v", call)
	}
}

func TestReactionHTTPCommentLikesReturnsEmptyRoster(t *testing.T) {
	app, _, store := newReactionHTTPApp(t, nil)
	store.roster = forum.ReactionRoster{Items: []forum.UserSummary{}}

	roster := decodeReactionRoster(t, performForumRequest(t, app, nethttp.MethodGet, "/api/v1/comments/20/likes", nil, nil))
	if roster.Items == nil || len(roster.Items) != 0 || roster.Total != 0 || roster.HasMore {
		t.Fatalf("unexpected empty roster %#v", roster)
	}
	if len(store.lists) != 1 {
		t.Fatalf("expected one roster read, got %d", len(store.lists))
	}
	call := store.lists[0]
	// 服务层用可见评论的真实 ID 落库，而不是直接透传路径参数。
	if call.targetType != forum.ReactionTargetComment || call.targetID != 20 ||
		call.limit != forum.ReactionRosterDefaultLimit || call.offset != 0 {
		t.Fatalf("unexpected roster read %#v", call)
	}
}

func TestReactionHTTPLikesReturnNotFoundForMissingTargets(t *testing.T) {
	app, forumStore, store := newReactionHTTPApp(t, nil)

	forumStore.topicErr = forum.ErrTopicNotFound
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/likes", nil, nil),
		nethttp.StatusNotFound, forum.CodeTopicNotFound)
	if len(store.lists) != 0 {
		t.Fatalf("invisible topic must not reach the roster store, got %d reads", len(store.lists))
	}

	forumStore.topicErr = nil
	forumStore.commentErr = forum.ErrCommentNotFound
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodGet, "/api/v1/comments/20/likes", nil, nil),
		nethttp.StatusNotFound, forum.CodeCommentNotFound)
	if len(store.lists) != 0 {
		t.Fatalf("invisible comment must not reach the roster store, got %d reads", len(store.lists))
	}
}

func TestReactionHTTPLikesReturnNotFoundForInvalidTargetIDs(t *testing.T) {
	app, _, store := newReactionHTTPApp(t, nil)

	for _, test := range []struct {
		name   string
		path   string
		reason string
	}{
		{name: "topic zero", path: "/api/v1/topics/0/likes", reason: forum.CodeTopicNotFound},
		{name: "topic negative", path: "/api/v1/topics/-7/likes", reason: forum.CodeTopicNotFound},
		{name: "topic non numeric", path: "/api/v1/topics/abc/likes", reason: forum.CodeTopicNotFound},
		{name: "comment zero", path: "/api/v1/comments/0/likes", reason: forum.CodeCommentNotFound},
		{name: "comment negative", path: "/api/v1/comments/-7/likes", reason: forum.CodeCommentNotFound},
		{name: "comment non numeric", path: "/api/v1/comments/abc/likes", reason: forum.CodeCommentNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			// paramInt 用 strconv.Atoi 且忽略错误：非数字落 0、负数保留，
			// service 对 ID<=0 返回未找到，因此这里是 404 而不是 400。
			resp := performForumRequest(t, app, nethttp.MethodGet, test.path, nil, nil)
			assertReactionError(t, resp, nethttp.StatusNotFound, test.reason)
		})
	}
	if len(store.lists) != 0 {
		t.Fatalf("invalid target must not reach the roster store, got %d reads", len(store.lists))
	}
}

func TestReactionHTTPLikesRespectGuestReadPolicy(t *testing.T) {
	app, forumStore, store := newReactionHTTPApp(t, nil)
	forumStore.guestRead = "login_required"
	store.roster = forum.ReactionRoster{Items: []forum.UserSummary{}}

	// 名单端点先走 requireGuestRead：guest.read=login_required 时匿名读 401。
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/likes", nil, nil),
		nethttp.StatusUnauthorized, forum.CodeGuestLoginRequired)
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodGet, "/api/v1/comments/20/likes", nil, nil),
		nethttp.StatusUnauthorized, forum.CodeGuestLoginRequired)
	if len(store.lists) != 0 {
		t.Fatalf("guest read denial must not reach the roster store, got %d reads", len(store.lists))
	}

	cookie := loginForumUser(t, app, 1)
	if roster := decodeReactionRoster(t, performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/likes", nil, cookie)); roster.Total != 0 {
		t.Fatalf("unexpected roster %#v", roster)
	}
}

func TestReactionHTTPWriteEndpointsRequireLogin(t *testing.T) {
	app, _, store := newReactionHTTPApp(t, nil)

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: nethttp.MethodPost, path: "/api/v1/topics/10/like"},
		{method: nethttp.MethodDelete, path: "/api/v1/topics/10/like"},
		{method: nethttp.MethodPost, path: "/api/v1/comments/20/like"},
		{method: nethttp.MethodDelete, path: "/api/v1/comments/20/like"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			resp := performForumRequest(t, app, test.method, test.path, nil, nil)
			assertReactionError(t, resp, nethttp.StatusUnauthorized, "auth.required")
		})
	}
	if len(store.applied) != 0 {
		t.Fatalf("anonymous writes must not reach the store, got %d", len(store.applied))
	}
}

func TestReactionHTTPReactionsDisabledFeatureGateReturns403(t *testing.T) {
	gate := &reactionHTTPGate{enabled: false}
	app, _, store := newReactionHTTPApp(t, gate)
	cookie := loginForumUser(t, app, 1)

	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/like", nil, cookie),
		nethttp.StatusForbidden, forum.CodeReactionsDisabled)
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/comments/20/like", nil, cookie),
		nethttp.StatusForbidden, forum.CodeReactionsDisabled)
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/likes", nil, nil),
		nethttp.StatusForbidden, forum.CodeReactionsDisabled)
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodGet, "/api/v1/comments/20/likes", nil, nil),
		nethttp.StatusForbidden, forum.CodeReactionsDisabled)

	if len(store.applied) != 0 || len(store.lists) != 0 {
		t.Fatalf("disabled feature must not reach the store: applied=%d lists=%d", len(store.applied), len(store.lists))
	}
	if len(gate.names) == 0 {
		t.Fatal("expected feature gate lookups")
	}
	for _, name := range gate.names {
		if name != options.NameFeatureReactions {
			t.Fatalf("feature gate queried with %q, want %q", name, options.NameFeatureReactions)
		}
	}
}

func TestReactionHTTPEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
	// 未装配点赞协作者时，名单与写端点都以 503 明确失败，而不是静默 200/404。
	app, _, _ := newForumTestApp()
	cookie := loginForumUser(t, app, 1)

	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/likes", nil, nil),
		nethttp.StatusServiceUnavailable, "forum.reactions_unavailable")
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/like", nil, cookie),
		nethttp.StatusServiceUnavailable, "forum.reactions_unavailable")
}

func TestReactionHTTPLikeTopicReturnsState(t *testing.T) {
	app, _, store := newReactionHTTPApp(t, nil)
	store.state = forum.ReactionState{
		TargetType: forum.ReactionTargetTopic,
		TargetID:   10,
		Reaction:   forum.ReactionLike,
		Liked:      true,
		LikeCount:  3,
	}
	cookie := loginForumUser(t, app, 1)

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/like", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected 200 like, got %d body=%s", resp.StatusCode, body)
	}
	defer resp.Body.Close()
	var envelope forumTestEnvelope[forum.ReactionState]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if !envelope.Data.Liked || envelope.Data.LikeCount != 3 ||
		envelope.Data.TargetType != forum.ReactionTargetTopic || envelope.Data.TargetID != 10 ||
		envelope.Data.Reaction != forum.ReactionLike {
		t.Fatalf("unexpected reaction state %#v", envelope.Data)
	}

	resp = performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/topics/10/like", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected 200 unlike, got %d body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()

	if len(store.applied) != 2 {
		t.Fatalf("expected two applied reactions, got %d", len(store.applied))
	}
	like := store.applied[0]
	if like.ActorUserID != 1 || like.TargetType != forum.ReactionTargetTopic ||
		like.TargetID != 10 || like.TopicID != 10 || like.Reaction != forum.ReactionLike || !like.Add {
		t.Fatalf("unexpected like record %#v", like)
	}
	unlike := store.applied[1]
	if unlike.ActorUserID != 1 || unlike.TargetType != forum.ReactionTargetTopic || unlike.Add {
		t.Fatalf("unexpected unlike record %#v", unlike)
	}
}

// TestReactionHTTPTopicDetailCarriesLikedByPreview 固定 M1：主题详情 JSON 携带
// likedBy 头像组预览，匿名与登录访客都能在首屏拿到（SSR 零额外请求）。
func TestReactionHTTPTopicDetailCarriesLikedByPreview(t *testing.T) {
	app, _, store := newReactionHTTPApp(t, nil)
	store.previews = map[int64][]forum.UserSummary{
		10: {{ID: 3}, {ID: 5}},
	}

	guest := decodeTopicDetailPreview(t, performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10", nil, nil))
	if len(guest.LikedBy) != 2 || guest.LikedBy[0].ID != 3 {
		t.Fatalf("anonymous likedBy = %+v, want two likers", guest.LikedBy)
	}
	if guest.LikedByMe {
		t.Fatal("anonymous likedByMe must stay false")
	}

	cookie := loginForumUser(t, app, 9)
	loggedIn := decodeTopicDetailPreview(t, performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10", nil, cookie))
	if len(loggedIn.LikedBy) != 2 {
		t.Fatalf("logged-in likedBy = %+v", loggedIn.LikedBy)
	}
}

// TestReactionHTTPCommentListCarriesLikedByPreview 固定 M1：评论列表 JSON 每条
// （含匿名响应）都携带 likedBy 预览。
func TestReactionHTTPCommentListCarriesLikedByPreview(t *testing.T) {
	app, _, store := newReactionHTTPApp(t, nil)
	store.previews = map[int64][]forum.UserSummary{
		20: {{ID: 4}},
		21: {{ID: 6}},
	}

	guest := decodeCommentListPreview(t, performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/comments?view=tree", nil, nil))
	if len(guest.Items) == 0 {
		t.Fatal("comment list must not be empty")
	}
	if len(guest.Items[0].LikedBy) != 1 || guest.Items[0].LikedBy[0].ID != 4 {
		t.Fatalf("root likedBy = %+v", guest.Items[0].LikedBy)
	}
	if len(guest.Items[0].Children[0].LikedBy) != 1 || guest.Items[0].Children[0].LikedBy[0].ID != 6 {
		t.Fatalf("child likedBy = %+v", guest.Items[0].Children[0].LikedBy)
	}

	cookie := loginForumUser(t, app, 9)
	loggedIn := decodeCommentListPreview(t, performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/comments?view=tree", nil, cookie))
	if len(loggedIn.Items[0].LikedBy) != 1 {
		t.Fatalf("logged-in root likedBy = %+v", loggedIn.Items[0].LikedBy)
	}
}

// TestReactionHTTPPreviewFailureFailsOpen 固定预览查询失败时详情仍是 200。
func TestReactionHTTPPreviewFailureFailsOpen(t *testing.T) {
	app, _, store := newReactionHTTPApp(t, nil)
	store.previewErr = errors.New("preview boom")

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("preview failure must fail open, got status=%d", resp.StatusCode)
	}
}

func decodeTopicDetailPreview(t *testing.T, resp *nethttp.Response) forum.TopicDetail {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 topic detail, got %d body=%s", resp.StatusCode, body)
	}
	var envelope forumTestEnvelope[forum.TopicDetail]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode topic detail: %v", err)
	}
	return envelope.Data
}

func decodeCommentListPreview(t *testing.T, resp *nethttp.Response) forum.CommentList {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 comment list, got %d body=%s", resp.StatusCode, body)
	}
	var envelope forumTestEnvelope[forum.CommentList]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode comment list: %v", err)
	}
	return envelope.Data
}
