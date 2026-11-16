package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	adminoverviewcontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/AdminOverview"
	attachmentscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Attachments"
	forumcontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Forum"
	profilecontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Profile"
	sitechromecontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/SiteChrome"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

// 端点级证据：读路径的「拿不到主体」必须按原因回答，而不是按调用点的历史习惯回答。
//
// 这一组刻意构造 service=nil 的控制器 —— 只要 handler 继续往 service 走就会崩，
// 所以「503/401 而不是 500」本身就证明 actor 门在调用 service 之前已经答完。
// 覆盖三类改造前各不相同、但根因相同的答案：
//   - Profile publicProfile / publicBookmarks：旧实现 slog.Warn + 匿名 200（唯一确认的 fail-open）
//   - Attachments get/content：旧实现 return err → 500
//   - SiteChrome navigation：旧实现 return err → 500
func TestReadPathEndpointsAnswerDependencyFailuresAs503(t *testing.T) {
	type endpoint struct {
		name   string
		path   string
		module string
	}
	cases := []endpoint{
		{name: "profile public", path: "/api/v1/profiles/alice", module: "profile"},
		{name: "profile bookmarks", path: "/api/v1/profiles/alice/bookmarks", module: "profile"},
		{name: "attachment detail", path: "/api/v1/attachments/abc123", module: "attachments"},
		{name: "attachment content", path: "/api/v1/attachments/abc123/content", module: "attachments"},
		{name: "site navigation", path: "/api/v1/site/navigation", module: "sitechrome"},
		// Forum 5 处 ReadActor 接线：至少钉住 topic 与 comments（另 3 处同一助手、同一文件）。
		{name: "forum topic", path: "/api/v1/topics/1", module: "forum"},
		{name: "forum comments", path: "/api/v1/topics/1/comments", module: "forum"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+" / D session store down", func(t *testing.T) {
			app := readPathApp(t, testCase.module, readPathSessionStore(errors.New("redis: connection refused")),
				staticReadPathActorStore{actor: identity.Actor{ID: 7, Status: identity.UserStatusActive}})
			response := readPathRequest(t, app, testCase.path, "sforum_session=stale-session-cookie")
			defer response.Body.Close()
			if response.StatusCode != nethttp.StatusServiceUnavailable {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("a session store outage must answer 503, got %d (%s)", response.StatusCode, body)
			}
			if got := response.Header.Get(fiber.HeaderRetryAfter); got != "1" {
				t.Fatalf("expected Retry-After: 1, got %q", got)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			var envelope apiEnvelope[apiErrorData]
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode envelope %s: %v", body, err)
			}
			if envelope.Code != nethttp.StatusServiceUnavailable || envelope.Data.Reason != "service.not_ready" {
				t.Fatalf("expected service.not_ready, got %s", body)
			}
		})

		t.Run(testCase.name+" / C subject missing", func(t *testing.T) {
			store := readPathSessionStore(nil)
			app := readPathApp(t, testCase.module, store,
				errorReadPathActorStore{err: identity.ErrUserNotFound})
			response := readPathRequest(t, app, testCase.path, readPathLoginCookie(t, store))
			defer response.Body.Close()
			if response.StatusCode != nethttp.StatusUnauthorized {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("a missing subject must answer 401, got %d (%s)", response.StatusCode, body)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			var envelope apiEnvelope[apiErrorData]
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode envelope %s: %v", body, err)
			}
			if envelope.Data.Reason != "auth.required" {
				t.Fatalf("expected auth.required, got %s", body)
			}
		})
	}
}

// 公开读的「无凭据」路径必须一字不变：仍然按匿名继续（这里 service=nil 会崩成 500，
// 但 500 证明门已通过；关键是否定断言 —— 不是 401、不是 503）。
func TestReadPathEndpointsLetAnonymousRequestsPassTheActorGate(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		path   string
		module string
	}{
		{name: "profile public", path: "/api/v1/profiles/alice", module: "profile"},
		{name: "attachment detail", path: "/api/v1/attachments/abc123", module: "attachments"},
		{name: "site navigation", path: "/api/v1/site/navigation", module: "sitechrome"},
		// Forum 5 处 ReadActor 接线：至少钉住 topic 与 comments（另 3 处同一助手、同一文件）。
		{name: "forum topic", path: "/api/v1/topics/1", module: "forum"},
		{name: "forum comments", path: "/api/v1/topics/1/comments", module: "forum"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := readPathApp(t, testCase.module, readPathSessionStore(nil), staticReadPathActorStore{})
			response := readPathRequest(t, app, testCase.path, "")
			defer response.Body.Close()
			if response.StatusCode == nethttp.StatusUnauthorized || response.StatusCode == nethttp.StatusServiceUnavailable {
				t.Fatalf("anonymous request must not be answered as 401/503, got %d", response.StatusCode)
			}
		})
	}
}

func readPathApp(
	t *testing.T,
	module string,
	store *session.Store,
	users identity.ActorStore,
) *fiber.App {
	t.Helper()
	sessions := authsession.NewManager(store, authsession.Config{HashSecret: "read-path-endpoint-test"})
	cfg := config.Config{
		AppName: "SForum", AppEnv: "test", AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"},
	}
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{})
	api := app.Group("/api/v1")
	switch module {
	case "profile":
		profilecontroller.NewController(nil, users, sessions, readPathForumPolicy{}).RegisterRoutes(api)
	case "attachments":
		attachmentscontroller.NewController(nil, users, sessions).RegisterRoutes(api)
	case "sitechrome":
		sitechromecontroller.NewController(nil, users, sessions).RegisterRoutes(api)
	case "adminoverview":
		adminoverviewcontroller.NewController(nil, users, sessions).RegisterRoutes(api)
	case "forum":
		// 真实的 forum.Service（默认设置解析器，GuestRead=public）：这样 requireGuestRead
		// 不需要数据库即可放行，502/503 的分歧只剩「actor 门」这一处。store 为 nil，
		// 所以匿名用例会在门之后崩成 500 —— 断言的是「没被 401/503 拦下」。
		forumcontroller.NewController(forum.NewService(forum.ServiceConfig{}), users, sessions).RegisterRoutes(api)
	default:
		t.Fatalf("unknown module %q", module)
	}
	return app
}

func readPathRequest(t *testing.T, app *fiber.App, path string, cookie string) *nethttp.Response {
	t.Helper()
	request := httptest.NewRequest(nethttp.MethodGet, path, nil)
	if cookie != "" {
		request.Header.Set(fiber.HeaderCookie, cookie)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return response
}

// readPathLoginCookie 签发一条真会话来模拟「客户端确实带了凭据」。
func readPathLoginCookie(t *testing.T, store *session.Store) string {
	t.Helper()
	sessions := authsession.NewManager(store, authsession.Config{HashSecret: "read-path-endpoint-test"})
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		if _, err := sessions.Start(c, 7); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	response, err := app.Test(httptest.NewRequest(nethttp.MethodPost, "/login", nil))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer response.Body.Close()
	cookies := response.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected a session cookie")
	}
	return cookies[0].Name + "=" + cookies[0].Value
}

func readPathSessionStore(storeErr error) *session.Store {
	cfg := session.Config{Extractor: extractors.FromCookie("sforum_session")}
	if storeErr != nil {
		// 注意：不能用 server_test.go 的 failingSessionStorage —— 那个假的 Get 返回
		// (nil,nil)，只在 Set 上失败（注册路径用例）。读路径要的是「读就失败」。
		cfg.Storage = readPathFailingStorage{err: storeErr}
	}
	return session.NewStore(cfg)
}

type readPathFailingStorage struct{ err error }

func (s readPathFailingStorage) GetWithContext(context.Context, string) ([]byte, error) {
	return nil, s.err
}

func (s readPathFailingStorage) Get(string) ([]byte, error) { return nil, s.err }

func (s readPathFailingStorage) SetWithContext(context.Context, string, []byte, time.Duration) error {
	return s.err
}

func (s readPathFailingStorage) Set(string, []byte, time.Duration) error { return s.err }

func (s readPathFailingStorage) DeleteWithContext(context.Context, string) error { return s.err }

func (s readPathFailingStorage) Delete(string) error { return s.err }

func (s readPathFailingStorage) ResetWithContext(context.Context) error { return s.err }

func (s readPathFailingStorage) Reset() error { return s.err }

func (s readPathFailingStorage) Close() error { return nil }

type readPathForumPolicy struct{}

func (readPathForumPolicy) ForumReadPolicySnapshot() (string, string, uint64, bool) {
	return "public", "hidden", 1, true
}

type staticReadPathActorStore struct{ actor identity.Actor }

func (s staticReadPathActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

type errorReadPathActorStore struct{ err error }

func (s errorReadPathActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return identity.Actor{}, s.err
}
