package forumcontroller

// 收藏（bookmarks）HTTP 层用例：写端点的登录要求、产品开关关闭时的 403、
// 目标不可见/非法 ID 的 404、成功路径的响应形状，以及未装配协作者时的 503。
//
// 只走 HTTP 传输层：可见性读取复用 controllerForumStore，收藏存储换成内存
// fake，因此不需要数据库。

import (
	"context"
	"encoding/json"
	"io"
	nethttp "net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// forumBannedActorID 是测试装配里的封禁账号（见 newForumTestApp）。
const forumBannedActorID int64 = 99

// bookmarkHTTPStore 实现 BookmarkStore 的四个方法，记录调用参数供断言。
type bookmarkHTTPStore struct {
	state    forum.BookmarkState
	applyErr error

	applied []forum.BookmarkApplyInput

	list      forum.BookmarkList
	listErr   error
	listCalls int
}

func (s *bookmarkHTTPStore) ApplyBookmark(_ context.Context, input forum.BookmarkApplyInput) (forum.BookmarkState, error) {
	if s.applyErr != nil {
		return forum.BookmarkState{}, s.applyErr
	}
	s.applied = append(s.applied, input)
	return s.state, nil
}

func (s *bookmarkHTTPStore) BookmarkedTargetIDs(context.Context, int64, string, []int64) (map[int64]struct{}, error) {
	return map[int64]struct{}{}, nil
}

func (s *bookmarkHTTPStore) ListUserBookmarks(context.Context, int64, bool, int, int) (forum.BookmarkList, error) {
	s.listCalls++
	if s.listErr != nil {
		return forum.BookmarkList{}, s.listErr
	}
	return s.list, nil
}

// bookmarkHTTPGate 记录被查询的开关名，用来锁定 features.bookmarks 的契约。
type bookmarkHTTPGate struct {
	enabled bool
	err     error
	names   []string
}

func (g *bookmarkHTTPGate) IsFeatureEnabled(_ context.Context, name string) (bool, error) {
	g.names = append(g.names, name)
	return g.enabled, g.err
}

// newBookmarkHTTPApp 在既有测试 app 上装配收藏协作者；gate 为 nil 时视为开关开启。
func newBookmarkHTTPApp(t *testing.T, gate forum.FeatureGate) (*fiber.App, *controllerForumStore, *bookmarkHTTPStore) {
	t.Helper()
	store := &bookmarkHTTPStore{}
	app, _, forumStore := newForumTestApp(func(controller *Controller) {
		bookmarks := forum.NewBookmarkService(controller.service).WithStore(store)
		if gate != nil {
			bookmarks = bookmarks.WithFeatureGate(gate)
		}
		controller.WithBookmarks(bookmarks)
	})
	return app, forumStore, store
}

func decodeBookmarkState(t *testing.T, resp *nethttp.Response) forum.BookmarkState {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 bookmark state, got %d body=%s", resp.StatusCode, body)
	}
	var envelope forumTestEnvelope[forum.BookmarkState]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode bookmark state: %v", err)
	}
	return envelope.Data
}

func TestBookmarkHTTPWriteEndpointsRequireLogin(t *testing.T) {
	app, _, store := newBookmarkHTTPApp(t, nil)

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: nethttp.MethodPut, path: "/api/v1/topics/10/bookmark"},
		{method: nethttp.MethodDelete, path: "/api/v1/topics/10/bookmark"},
		{method: nethttp.MethodPut, path: "/api/v1/comments/20/bookmark"},
		{method: nethttp.MethodDelete, path: "/api/v1/comments/20/bookmark"},
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

func TestBookmarkHTTPBookmarksDisabledFeatureGateReturns403(t *testing.T) {
	gate := &bookmarkHTTPGate{enabled: false}
	app, _, store := newBookmarkHTTPApp(t, gate)
	cookie := loginForumUser(t, app, 1)

	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodPut, "/api/v1/topics/10/bookmark", nil, cookie),
		nethttp.StatusForbidden, forum.CodeBookmarksDisabled)
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/comments/20/bookmark", nil, cookie),
		nethttp.StatusForbidden, forum.CodeBookmarksDisabled)

	if len(store.applied) != 0 {
		t.Fatalf("disabled feature must not reach the store, got %d", len(store.applied))
	}
	if len(gate.names) == 0 {
		t.Fatal("expected feature gate lookups")
	}
	for _, name := range gate.names {
		if name != options.NameFeatureBookmarks {
			t.Fatalf("feature gate queried with %q, want %q", name, options.NameFeatureBookmarks)
		}
	}
}

func TestBookmarkHTTPReturns404ForInvisibleTargets(t *testing.T) {
	app, forumStore, store := newBookmarkHTTPApp(t, nil)
	cookie := loginForumUser(t, app, 1)

	forumStore.topicErr = forum.ErrTopicNotFound
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodPut, "/api/v1/topics/10/bookmark", nil, cookie),
		nethttp.StatusNotFound, forum.CodeTopicNotFound)

	forumStore.topicErr = nil
	forumStore.commentErr = forum.ErrCommentNotFound
	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodPut, "/api/v1/comments/20/bookmark", nil, cookie),
		nethttp.StatusNotFound, forum.CodeCommentNotFound)

	if len(store.applied) != 0 {
		t.Fatalf("invisible targets must not reach the store, got %d", len(store.applied))
	}
}

func TestBookmarkHTTPInvalidTargetIDsReturn404(t *testing.T) {
	app, _, store := newBookmarkHTTPApp(t, nil)
	cookie := loginForumUser(t, app, 1)

	for _, test := range []struct {
		name   string
		path   string
		reason string
	}{
		{name: "topic zero", path: "/api/v1/topics/0/bookmark", reason: forum.CodeTopicNotFound},
		{name: "topic non numeric", path: "/api/v1/topics/abc/bookmark", reason: forum.CodeTopicNotFound},
		{name: "comment zero", path: "/api/v1/comments/0/bookmark", reason: forum.CodeCommentNotFound},
		{name: "comment non numeric", path: "/api/v1/comments/abc/bookmark", reason: forum.CodeCommentNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			resp := performForumRequest(t, app, nethttp.MethodPut, test.path, nil, cookie)
			assertReactionError(t, resp, nethttp.StatusNotFound, test.reason)
		})
	}
	if len(store.applied) != 0 {
		t.Fatalf("invalid target must not reach the store, got %d", len(store.applied))
	}
}

func TestBookmarkHTTPPutTopicReturnsState(t *testing.T) {
	app, _, store := newBookmarkHTTPApp(t, nil)
	store.state = forum.BookmarkState{
		TargetType:          forum.BookmarkTargetTopic,
		TargetID:            10,
		Visibility:          forum.BookmarkVisibilityPublic,
		Bookmarked:          true,
		PublicBookmarkCount: 4,
	}
	cookie := loginForumUser(t, app, 1)

	state := decodeBookmarkState(t, performForumRequest(t, app, nethttp.MethodPut, "/api/v1/topics/10/bookmark",
		[]byte(`{"visibility":"public"}`), cookie))
	if !state.Bookmarked || state.Visibility != forum.BookmarkVisibilityPublic ||
		state.PublicBookmarkCount != 4 || state.TargetType != forum.BookmarkTargetTopic || state.TargetID != 10 {
		t.Fatalf("unexpected bookmark state %#v", state)
	}
	if len(store.applied) != 1 || store.applied[0].Visibility != forum.BookmarkVisibilityPublic || !store.applied[0].Add {
		t.Fatalf("unexpected apply input %#v", store.applied)
	}
}

func TestBookmarkHTTPPutWithoutBodyDefaultsToKeepCurrentVisibility(t *testing.T) {
	app, _, store := newBookmarkHTTPApp(t, nil)
	store.state = forum.BookmarkState{TargetType: forum.BookmarkTargetComment, TargetID: 20, Visibility: forum.BookmarkVisibilityPrivate, Bookmarked: true}
	cookie := loginForumUser(t, app, 1)

	state := decodeBookmarkState(t, performForumRequest(t, app, nethttp.MethodPut, "/api/v1/comments/20/bookmark", nil, cookie))
	if !state.Bookmarked || state.Visibility != forum.BookmarkVisibilityPrivate {
		t.Fatalf("unexpected bookmark state %#v", state)
	}
	if len(store.applied) != 1 || store.applied[0].Visibility != "" {
		t.Fatalf("empty body must keep current visibility, got %#v", store.applied)
	}
}

func TestBookmarkHTTPDeleteReturnsUnbookmarkedState(t *testing.T) {
	app, _, store := newBookmarkHTTPApp(t, nil)
	store.state = forum.BookmarkState{TargetType: forum.BookmarkTargetTopic, TargetID: 10, Visibility: forum.BookmarkVisibilityPrivate, Bookmarked: false}
	cookie := loginForumUser(t, app, 1)

	state := decodeBookmarkState(t, performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/topics/10/bookmark", nil, cookie))
	if state.Bookmarked {
		t.Fatalf("delete must report bookmarked=false: %#v", state)
	}
	if len(store.applied) != 1 || store.applied[0].Add {
		t.Fatalf("unexpected apply input %#v", store.applied)
	}
}

func TestBookmarkHTTPInvalidBodyReturns422(t *testing.T) {
	app, _, store := newBookmarkHTTPApp(t, nil)
	cookie := loginForumUser(t, app, 1)

	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodPut, "/api/v1/topics/10/bookmark", []byte(`{"remindAt":"not-a-time"}`), cookie),
		nethttp.StatusUnprocessableEntity, "forum.bookmark_body_invalid")
	if len(store.applied) != 0 {
		t.Fatalf("invalid body must not reach the store, got %d", len(store.applied))
	}
}

func TestBookmarkHTTPEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
	// 未装配收藏协作者时，写端点以 503 明确失败，而不是静默 200/404。
	app, _, _ := newForumTestApp()
	cookie := loginForumUser(t, app, 1)

	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodPut, "/api/v1/topics/10/bookmark", nil, cookie),
		nethttp.StatusServiceUnavailable, "forum.bookmarks_unavailable")
}

// TestBookmarkHTTPBannedActorReturns403 固定封禁账号：写路由 403 permission.denied，
// 且不触达 store（与点赞写路由同一口径）。
func TestBookmarkHTTPBannedActorReturns403(t *testing.T) {
	app, forumStore, store := newBookmarkHTTPApp(t, nil)
	_ = forumStore
	cookie := loginForumUser(t, app, forumBannedActorID)

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: nethttp.MethodPut, path: "/api/v1/topics/10/bookmark"},
		{method: nethttp.MethodPut, path: "/api/v1/comments/20/bookmark"},
		{method: nethttp.MethodDelete, path: "/api/v1/topics/10/bookmark"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			assertReactionError(t,
				performForumRequest(t, app, test.method, test.path, nil, cookie),
				nethttp.StatusForbidden, "permission.denied")
		})
	}
	if len(store.applied) != 0 {
		t.Fatalf("banned actor must not reach the store, got %d", len(store.applied))
	}
}

// TestBookmarkHTTPClearReminderReachesStore 固定三态在传输层不被丢掉：
// clearReminder=true 必须原样下传（旧实现丢字段 → 提醒永远清不掉）。
func TestBookmarkHTTPClearReminderReachesStore(t *testing.T) {
	app, _, store := newBookmarkHTTPApp(t, nil)
	store.state = forum.BookmarkState{TargetType: forum.BookmarkTargetTopic, TargetID: 10, Visibility: forum.BookmarkVisibilityPrivate, Bookmarked: true}
	cookie := loginForumUser(t, app, 1)

	state := decodeBookmarkState(t, performForumRequest(t, app, nethttp.MethodPut, "/api/v1/topics/10/bookmark",
		[]byte(`{"clearReminder":true}`), cookie))
	if state.RemindAt != nil || state.RemindedAt != nil {
		t.Fatalf("cleared state must not carry reminder times: %#v", state)
	}
	if len(store.applied) != 1 || !store.applied[0].ClearReminder || store.applied[0].RemindAt != nil {
		t.Fatalf("clearReminder must reach the store: %#v", store.applied)
	}
}

// TestBookmarkHTTPReminderConflictsAndWindowReturn422 固定提醒请求的 422 家族：
// 冲突、过去时间、超上限各自的具名 reason。
func TestBookmarkHTTPReminderConflictsAndWindowReturn422(t *testing.T) {
	cases := []struct {
		name   string
		store  *bookmarkHTTPStore
		body   []byte
		reason string
	}{
		{
			name:   "clear and set together",
			store:  &bookmarkHTTPStore{},
			body:   []byte(`{"clearReminder":true,"remindAt":"2099-01-01T00:00:00Z"}`),
			reason: forum.CodeBookmarkReminderConflict,
		},
		{
			name:   "reminder in the past",
			store:  &bookmarkHTTPStore{},
			body:   []byte(`{"remindAt":"2020-01-01T00:00:00Z"}`),
			reason: forum.CodeBookmarkReminderInPast,
		},
		{
			name:  "too many pending reminders",
			store: &bookmarkHTTPStore{applyErr: forum.ErrBookmarkReminderLimit},
			// 窗口内的合法时间：上限由 store 在同一事务里判，不在这里。
			body:   []byte(`{"remindAt":"` + time.Now().UTC().Add(time.Hour).Format(time.RFC3339) + `"}`),
			reason: forum.CodeBookmarkReminderLimit,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			app, _, _ := newForumTestApp(func(controller *Controller) {
				bookmarks := forum.NewBookmarkService(controller.service).WithStore(test.store)
				controller.WithBookmarks(bookmarks)
			})
			store := test.store
			cookie := loginForumUser(t, app, 1)

			assertReactionError(t,
				performForumRequest(t, app, nethttp.MethodPut, "/api/v1/topics/10/bookmark", test.body, cookie),
				nethttp.StatusUnprocessableEntity, test.reason)
			if len(store.applied) != 0 {
				t.Fatalf("rejected reminder must not reach the store: %#v", store.applied)
			}
		})
	}
}

// TestBookmarkHTTPTooFarReminderReturns422 固定前瞻窗口：2 年之后的时间被拒绝。
func TestBookmarkHTTPTooFarReminderReturns422(t *testing.T) {
	app, _, store := newBookmarkHTTPApp(t, nil)
	cookie := loginForumUser(t, app, 1)
	far := time.Now().UTC().AddDate(3, 0, 0).Format(time.RFC3339)

	assertReactionError(t,
		performForumRequest(t, app, nethttp.MethodPut, "/api/v1/topics/10/bookmark",
			[]byte(`{"remindAt":"`+far+`"}`), cookie),
		nethttp.StatusUnprocessableEntity, forum.CodeBookmarkReminderTooFar)
	if len(store.applied) != 0 {
		t.Fatalf("too-far reminder must not reach the store, got %d", len(store.applied))
	}
}

// TestBookmarkHTTPDeleteIgnoresMalformedBody 固定 DELETE 不读 body：删除语义
// 只看行是否存在，带畸形 JSON 的 DELETE 也必须成功（不能变成 422），因为
// 控制器在 !add 时根本不解析 body。契约里 DELETE 因此不声明 422。
func TestBookmarkHTTPDeleteIgnoresMalformedBody(t *testing.T) {
	app, _, store := newBookmarkHTTPApp(t, nil)
	store.state = forum.BookmarkState{TargetType: forum.BookmarkTargetTopic, TargetID: 10, Visibility: forum.BookmarkVisibilityPrivate, Bookmarked: false}
	cookie := loginForumUser(t, app, 1)

	malformed := []byte(`{"visibility": "public", `)
	state := decodeBookmarkState(t, performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/topics/10/bookmark", malformed, cookie))
	if state.Bookmarked {
		t.Fatalf("delete must report bookmarked=false: %#v", state)
	}
	if len(store.applied) != 1 || store.applied[0].Add {
		t.Fatalf("malformed DELETE body must be ignored, got %#v", store.applied)
	}

	// 评论目标同样忽略 body。
	store.applied = nil
	store.state = forum.BookmarkState{TargetType: forum.BookmarkTargetComment, TargetID: 20, Visibility: forum.BookmarkVisibilityPrivate, Bookmarked: false}
	if got := decodeBookmarkState(t, performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/comments/20/bookmark", []byte(`not-json-at-all`), cookie)); got.Bookmarked {
		t.Fatalf("comment delete must report bookmarked=false: %#v", got)
	}
	if len(store.applied) != 1 || store.applied[0].Add {
		t.Fatalf("malformed comment DELETE body must be ignored, got %#v", store.applied)
	}
}
