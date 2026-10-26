package forumcontroller

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	nethttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	"github.com/keeptrue/sforum/apps/api/app/Support/Localization"
	"github.com/keeptrue/sforum/apps/api/config"
)

var errCommentStreamTestStop = errors.New("comment stream test stop")

// commentLiveStore 在既有 forum.Store 替身上补出修订号能力：
// 按脚本依次返回状态，脚本耗尽后返回错误，让 SSE 流在测试里自行结束
// （生产里流靠连接寿命与客户端重连结束，不依赖读取失败）。
type commentLiveStore struct {
	*controllerForumStore
	states   []forum.CommentRevisionState
	topicErr error
	calls    int
}

func (s *commentLiveStore) CommentRevisionState(context.Context, int64) (forum.CommentRevisionState, error) {
	s.calls++
	if len(s.states) == 0 {
		return forum.CommentRevisionState{}, errCommentStreamTestStop
	}
	state := s.states[0]
	s.states = s.states[1:]
	return state, nil
}

func (s *commentLiveStore) GetTopic(ctx context.Context, topicID int64) (forum.TopicDetail, error) {
	if s.topicErr != nil {
		return forum.TopicDetail{}, s.topicErr
	}
	return s.controllerForumStore.GetTopic(ctx, topicID)
}

func newCommentLiveApp(store *commentLiveStore, live *forum.CommentLiveService) *fiber.App {
	controller := NewController(forum.NewService(forum.ServiceConfig{Store: store, Settings: store, Publisher: nil}), controllerForumActors{}, nil).
		WithCommentLive(live)
	return apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})
}

func TestTopicCommentRevisionReturnsPublicState(t *testing.T) {
	last := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	store := &commentLiveStore{
		controllerForumStore: &controllerForumStore{},
		states: []forum.CommentRevisionState{
			{Revision: 12, CommentCount: 40, LastCommentID: 903, LastCommentCreatedAt: &last},
		},
	}
	app := newCommentLiveApp(store, forum.NewCommentLiveService(store))

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/comments/revision", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data forum.CommentRevisionState `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	state := envelope.Data
	if state.Revision != 12 || state.CommentCount != 40 || state.LastCommentID != 903 {
		t.Fatalf("state = %#v, want revision 12 count 40 last 903", state)
	}
	if state.LastCommentCreatedAt == nil || !state.LastCommentCreatedAt.Equal(last) {
		t.Fatalf("lastCommentCreatedAt = %v, want %v", state.LastCommentCreatedAt, last)
	}
	if !strings.Contains(string(body), `"commentCount":40`) {
		t.Fatalf("envelope keys must stay camelCase: %s", body)
	}
}

func TestTopicCommentRevisionHidesInvisibleTopic(t *testing.T) {
	store := &commentLiveStore{
		controllerForumStore: &controllerForumStore{},
		topicErr:             forum.ErrTopicNotFound,
	}
	app := newCommentLiveApp(store, forum.NewCommentLiveService(store))

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/comments/revision", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestTopicCommentLiveEndpointsRequireWiring(t *testing.T) {
	store := &commentLiveStore{controllerForumStore: &controllerForumStore{}}
	app := newCommentLiveApp(store, nil)

	for _, path := range []string{"/api/v1/topics/10/comments/revision", "/api/v1/topics/10/comments/stream"} {
		resp := performForumRequest(t, app, nethttp.MethodGet, path, nil, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != nethttp.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503: %s", path, resp.StatusCode, body)
		}
		var envelope struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("%s decode %s: %v", path, body, err)
		}
		if want := localization.Message("zh-CN", forum.CodeCommentStreamUnavailable); envelope.Message != want {
			t.Fatalf("%s message = %q, want %q", path, envelope.Message, want)
		}
	}
}

func TestTopicCommentStreamRespectsGuestReadPolicy(t *testing.T) {
	store := &commentLiveStore{controllerForumStore: &controllerForumStore{guestRead: "login_required"}}
	app := newCommentLiveApp(store, forum.NewCommentLiveService(store))

	for _, path := range []string{
		"/api/v1/topics/10/comments/revision",
		"/api/v1/topics/10/comments/stream",
	} {
		resp := performForumRequest(t, app, nethttp.MethodGet, path, nil, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != nethttp.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401: %s", path, resp.StatusCode, body)
		}
	}
}

func TestCommentStreamSubscribeErrorMapping(t *testing.T) {
	limited := commentStreamSubscribeError(forum.ErrCommentRevisionConnectionLimit)
	var fiberError *fiber.Error
	if !errors.As(limited, &fiberError) || fiberError.Code != fiber.StatusTooManyRequests {
		t.Fatalf("limit mapping = %#v", limited)
	}
	if got := commentStreamSubscribeError(forum.ErrCommentRevisionWakeUnavailable); got != nil {
		t.Fatalf("wake unavailable must degrade to reconcile ticks, got %#v", got)
	}
	if got := commentStreamSubscribeError(nil); got != nil {
		t.Fatalf("nil error mapping = %#v", got)
	}
	other := errors.New("boom")
	if got := commentStreamSubscribeError(other); !errors.Is(got, other) {
		t.Fatalf("unexpected mapping = %#v", got)
	}
}

func TestCommentStreamSuppressesMatchingCursor(t *testing.T) {
	store := &commentLiveStore{
		controllerForumStore: &controllerForumStore{},
		states:               []forum.CommentRevisionState{{Revision: 8, CommentCount: 41, LastCommentID: 904}},
	}
	live := forum.NewCommentLiveService(store)
	var output bytes.Buffer
	streamCommentRevisionEvents(context.Background(), bufio.NewWriter(&output), live, 10, nil, 7,
		forum.CommentRevisionState{Revision: 7, CommentCount: 40}, time.Hour, time.Millisecond)

	body := output.String()
	if strings.Contains(body, "id: 7\n") {
		t.Fatalf("matching cursor must not emit its own revision: %q", body)
	}
	if !strings.Contains(body, `id: 8
event: revision
data: {"revision":8,"commentCount":41,"lastCommentId":904}`) {
		t.Fatalf("missing advanced revision event: %q", body)
	}
}

func TestCommentStreamSendsInitialStateAndOnlyRevisionFacts(t *testing.T) {
	store := &commentLiveStore{
		controllerForumStore: &controllerForumStore{},
		states:               []forum.CommentRevisionState{{Revision: 9}},
	}
	live := forum.NewCommentLiveService(store)
	var output bytes.Buffer
	streamCommentRevisionEvents(context.Background(), bufio.NewWriter(&output), live, 10, nil, 0,
		forum.CommentRevisionState{Revision: 7, CommentCount: 40}, time.Hour, time.Millisecond)

	body := output.String()
	if !strings.Contains(body, "id: 7\nevent: revision\ndata: {\"revision\":7,\"commentCount\":40}\n\n") {
		t.Fatalf("missing initial revision event: %q", body)
	}
	// 载荷只含修订事实：不携带正文、作者或任何会话相关投影。
	for _, forbidden := range []string{"htmlContent", "author", "content", "recipient"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("stream leaked %q: %q", forbidden, body)
		}
	}
}

func TestCommentStreamWakeDrivenReconcile(t *testing.T) {
	store := &commentLiveStore{
		controllerForumStore: &controllerForumStore{},
		states:               []forum.CommentRevisionState{{Revision: 3, CommentCount: 2}},
	}
	live := forum.NewCommentLiveService(store)
	wakes := make(chan struct{}, 1)
	wakes <- struct{}{}
	var output bytes.Buffer
	streamCommentRevisionEvents(context.Background(), bufio.NewWriter(&output), live, 10, wakes, 1,
		forum.CommentRevisionState{Revision: 1, CommentCount: 1}, time.Hour, time.Millisecond)
	if !strings.Contains(output.String(), "id: 3\nevent: revision\n") {
		t.Fatalf("wake-driven reconcile failed: %q", output.String())
	}
}

func TestCommentStreamHeartbeatsWhileIdle(t *testing.T) {
	store := &commentLiveStore{
		controllerForumStore: &controllerForumStore{},
		states: []forum.CommentRevisionState{
			{Revision: 5}, {Revision: 5}, {Revision: 5},
		},
	}
	live := forum.NewCommentLiveService(store)
	var output bytes.Buffer
	streamCommentRevisionEvents(context.Background(), bufio.NewWriter(&output), live, 10, nil, 5,
		forum.CommentRevisionState{Revision: 5}, 2*time.Millisecond, time.Millisecond)
	if !strings.Contains(output.String(), ": heartbeat\n\n") {
		t.Fatalf("missing heartbeat: %q", output.String())
	}
	if strings.Contains(output.String(), "event: revision") {
		t.Fatalf("unchanged revision must not emit events: %q", output.String())
	}
}
