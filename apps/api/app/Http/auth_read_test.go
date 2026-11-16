package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// 读路径主体解析的三分法是本轮 B1 的核心：同一次「拿不到主体」可能是五件不同的
// 事，答案也必须不同。这个文件逐类钉住分类结果，防止任何一类被合并回「一律 401」
// 或「一律匿名」。
func TestReadActorClassifiesEveryFailureKind(t *testing.T) {
	active := identity.Actor{ID: 7, Status: identity.UserStatusActive}

	const (
		wantAnonymous = "anonymous"
		want401       = "401"
		want503       = "503"
	)
	cases := []struct {
		name  string
		users identity.ActorStore
		// breakSessionStore 模拟 Redis 不可用（m.store.Get 失败）。
		breakSessionStore bool
		// credential 决定客户端是否带 cookie：none / valid（真签发）/ stale（已过期）。
		credential  string
		want        string
		wantActorID int64
	}{
		{name: "A no credential", users: staticReadActorStore{actor: active}, want: wantAnonymous},
		{
			// B：会话层把「cookie 值无法解析」折成全新匿名会话（fiber v3.4.0
			// middleware/session/store.go:136-158 与 getSessionID：提取失败返回空 id），
			// 所以 B 与 A 在 HTTP 层不可区分；区分它们需要给 CurrentUserID 换三态返回。
			name: "B unparsable credential collapses to anonymous", users: staticReadActorStore{actor: active},
			credential: "stale", want: wantAnonymous,
		},
		{
			name: "A/B usable subject resolves", users: staticReadActorStore{actor: active},
			credential: "valid", want: "",
			wantActorID: active.ID,
		},
		{
			name: "C subject missing", users: errorReadActorStore{err: identity.ErrUserNotFound},
			credential: "valid", want: want401,
		},
		{
			name:       "C subject disabled",
			users:      staticReadActorStore{actor: identity.Actor{ID: 7, Status: identity.UserStatusDisabled}},
			credential: "valid", want: want401,
		},
		{
			name:       "C subject banned",
			users:      staticReadActorStore{actor: identity.Actor{ID: 7, Status: identity.UserStatusBanned}},
			credential: "valid", want: want401,
		},
		{
			name: "D session store unavailable", users: staticReadActorStore{actor: active},
			breakSessionStore: true, credential: "stale", want: want503,
		},
		{
			name: "D identity store unavailable", users: errorReadActorStore{err: identity.ErrStoreUnavailable},
			credential: "valid", want: want503,
		},
		{
			// E：认不出来的错误必须 fail-closed。放开成匿名等于把「未知」当「无害」。
			name: "E unrecognized error", users: errorReadActorStore{err: errors.New("dependency added later")},
			credential: "valid", want: want503,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var storeErr error
			if testCase.breakSessionStore {
				storeErr = errors.New("redis: connection refused")
			}
			sessions := authsession.NewManager(
				readActorSessionStore(storeErr), authsession.Config{HashSecret: "auth-read-test"},
			)
			app := fiber.New()
			app.Post("/login", func(c fiber.Ctx) error {
				if _, err := sessions.Start(c, 7); err != nil {
					return err
				}
				return c.SendStatus(fiber.StatusNoContent)
			})
			var (
				gotActor identity.Actor
				gotErr   error
			)
			app.Get("/probe", func(c fiber.Ctx) error {
				gotActor, gotErr = ReadActor(c, sessions, testCase.users)
				return c.SendStatus(fiber.StatusNoContent)
			})

			cookie := ""
			switch testCase.credential {
			case "valid":
				if resp, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil)); err == nil {
					resp.Body.Close()
					if cookies := resp.Cookies(); len(cookies) > 0 {
						cookie = cookies[0].Name + "=" + cookies[0].Value
					}
				}
				if cookie == "" {
					t.Fatal("expected the login to issue a session cookie")
				}
			case "stale":
				// 格式合法但存储里不存在的 sid：正是「过期/被清理的会话」。
				cookie = "sforum_session=expired-session-id"
			}
			request := httptest.NewRequest(fiber.MethodGet, "/probe", nil)
			if cookie != "" {
				request.Header.Set(fiber.HeaderCookie, cookie)
			}
			response, err := app.Test(request)
			if err != nil {
				t.Fatalf("probe request: %v", err)
			}
			response.Body.Close()

			switch testCase.want {
			case wantAnonymous:
				if gotErr != nil || gotActor.ID != 0 {
					t.Fatalf("expected zero actor + nil error, got actor=%d err=%v", gotActor.ID, gotErr)
				}
			case want401:
				var fiberErr *fiber.Error
				if !errors.As(gotErr, &fiberErr) || fiberErr.Code != fiber.StatusUnauthorized {
					t.Fatalf("expected 401 auth.required, got %v", gotErr)
				}
				if fiberErr.Message != "auth.required" {
					t.Fatalf("expected the auth.required reason, got %q", fiberErr.Message)
				}
			case want503:
				cause, ok := readPathDependencyCause(gotErr)
				if !ok {
					t.Fatalf("expected a dependency failure (→503), got %v", gotErr)
				}
				if cause == nil {
					t.Fatal("dependency failure must keep its original cause for the log")
				}
			default:
				if gotErr != nil || gotActor.ID != testCase.wantActorID {
					t.Fatalf("expected actor id %d, got %d (err=%v)", testCase.wantActorID, gotActor.ID, gotErr)
				}
			}
		})
	}
}

// 未识别的账号状态不能当成不可用：真实存储只会写 active/disabled/banned，
// 零值 Status 表示「这条路径不关心状态」。判成 401 会让每一处假 store 都变成
// 「账号被封」，也会把一处代码疏漏放大成站点级登录故障。
func TestReadActorDoesNotRejectUnknownStatus(t *testing.T) {
	sessions := authsession.NewManager(readActorSessionStore(nil), authsession.Config{HashSecret: "auth-read-test"})
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := sessions.Start(c, 7)
		return err
	})
	var (
		got    identity.Actor
		gotErr error
	)
	app.Get("/probe", func(c fiber.Ctx) error {
		got, gotErr = ReadActor(c, sessions, staticReadActorStore{actor: identity.Actor{ID: 7}})
		return c.SendStatus(fiber.StatusNoContent)
	})
	loginResp, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	loginResp.Body.Close()
	cookies := loginResp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected a session cookie")
	}
	request := httptest.NewRequest(fiber.MethodGet, "/probe", nil)
	request.Header.Set(fiber.HeaderCookie, cookies[0].Name+"="+cookies[0].Value)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	response.Body.Close()
	if gotErr != nil || got.ID != 7 {
		t.Fatalf("unknown status must not be treated as unusable: actor=%d err=%v", got.ID, gotErr)
	}
}

// 一次 Redis 抖动会让之后每个公开读都命中同一条分支：逐条打日志会把第一条证据
// 埋掉。限频必须做到「一个窗口一条」，但**不能**把主体不存在这类确定结论也记成故障。
func TestLogActorDependencyIsThrottledAndSkipsSubjectFailures(t *testing.T) {
	previousLogger := slog.Default()
	var buffer safeBuffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	// 每个用例独立窗口：进程级限频器在测试之间不该互相吞日志。
	readDependencyLog.mu.Lock()
	readDependencyLog.last = time.Time{}
	readDependencyLog.mu.Unlock()

	app := fiber.New()
	app.Get("/probe", func(c fiber.Ctx) error {
		LogActorDependency(c, authsession.ErrStoreUnavailable)
		LogActorDependency(c, authsession.ErrStoreUnavailable)
		LogActorDependency(c, identity.ErrUserNotFound)
		return c.SendStatus(fiber.StatusNoContent)
	})
	response, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/probe", nil))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	response.Body.Close()

	records := 0
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if payload["msg"] == "api read path dependency unavailable" {
			records++
			if payload["path"] != "/probe" {
				t.Fatalf("log must carry the request path, got %#v", payload["path"])
			}
		}
	}
	if records != 1 {
		t.Fatalf("expected exactly one throttled dependency log, got %d", records)
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type staticReadActorStore struct{ actor identity.Actor }

func (s staticReadActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

type errorReadActorStore struct{ err error }

func (s errorReadActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return identity.Actor{}, s.err
}

// readActorSessionStore 复刻生产装配的 cookie 名：默认 session_id 与生产不一致，
// 用它写测试会得到「cookie 根本没被读」的假绿。
func readActorSessionStore(storeErr error) *session.Store {
	cfg := session.Config{Extractor: extractors.FromCookie("sforum_session")}
	if storeErr != nil {
		cfg.Storage = readActorFailingStorage{err: storeErr}
	}
	return session.NewStore(cfg)
}

type readActorFailingStorage struct{ err error }

func (s readActorFailingStorage) GetWithContext(context.Context, string) ([]byte, error) {
	return nil, s.err
}

func (s readActorFailingStorage) Get(string) ([]byte, error) { return nil, s.err }

func (s readActorFailingStorage) SetWithContext(context.Context, string, []byte, time.Duration) error {
	return s.err
}

func (s readActorFailingStorage) Set(string, []byte, time.Duration) error { return s.err }

func (s readActorFailingStorage) DeleteWithContext(context.Context, string) error { return s.err }

func (s readActorFailingStorage) Delete(string) error { return s.err }

func (s readActorFailingStorage) ResetWithContext(context.Context) error { return s.err }

func (s readActorFailingStorage) Reset() error { return s.err }

func (s readActorFailingStorage) Close() error { return nil }

// OptionalActor 是「拿不到 viewer 就按 guest 渲染」的唯一入口（Page Registry 的
// 渲染 fallback 走它）。它的现有调用方都会把 error 吞掉，所以这一层必须自己留证据：
// 删掉这行日志，Pages 的静默降级就变成观测盲区，而测试必须因此变红。
func TestOptionalActorLogsDependencyFailures(t *testing.T) {
	previousLogger := slog.Default()
	var buffer safeBuffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	readDependencyLog.mu.Lock()
	readDependencyLog.last = time.Time{}
	readDependencyLog.mu.Unlock()

	sessions := authsession.NewManager(
		readActorSessionStore(errors.New("redis: connection refused")),
		authsession.Config{HashSecret: "optional-actor-log"},
	)
	app := fiber.New()
	var gotErr error
	// 路由注册在 /guest-render：日志必须带请求路径，用同一条路径断言「记录的是这次请求」。
	app.Get("/guest-render", func(c fiber.Ctx) error {
		_, gotErr = OptionalActor(c, sessions, staticReadActorStore{})
		return c.SendStatus(fiber.StatusNoContent)
	})

	withCredential := httptest.NewRequest(fiber.MethodGet, "/guest-render", nil)
	withCredential.Header.Set(fiber.HeaderCookie, "sforum_session=stale-session-cookie")
	response, err := app.Test(withCredential)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	response.Body.Close()

	if !errors.Is(gotErr, authsession.ErrStoreUnavailable) {
		t.Fatalf("OptionalActor must keep returning the dependency sentinel, got %v", gotErr)
	}
	records := countDependencyLogs(t, buffer.String())
	if records != 1 {
		t.Fatalf("a swallowed dependency failure must still be logged once, got %d", records)
	}
	if !strings.Contains(buffer.String(), "guest-render") {
		t.Fatalf("the log must carry the request path, got %s", buffer.String())
	}

	// 无凭据不触发会话读取，也就不该产生依赖故障日志。
	readDependencyLog.mu.Lock()
	readDependencyLog.last = time.Time{}
	readDependencyLog.mu.Unlock()
	before := buffer.String()
	anonymous := httptest.NewRequest(fiber.MethodGet, "/guest-render", nil)
	response, err = app.Test(anonymous)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	response.Body.Close()
	if gotErr != nil {
		t.Fatalf("anonymous requests must stay anonymous, got %v", gotErr)
	}
	if extra := strings.TrimPrefix(buffer.String(), before); extra != "" {
		t.Fatalf("anonymous requests must not log a dependency failure, got %s", extra)
	}
}

func countDependencyLogs(t *testing.T, raw string) int {
	t.Helper()
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if payload["msg"] == "api read path dependency unavailable" {
			count++
		}
	}
	return count
}
