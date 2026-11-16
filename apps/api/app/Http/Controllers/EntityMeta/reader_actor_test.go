package entitymetacontroller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

// 「拿不到主体」是三类不同的事，答案必须分开：
//   - 没有凭据 / 凭据不可解析 → 零值 Actor + 200 匿名视角（B 在会话层已折成 A）；
//   - 主体不存在或不可用     → 401 auth.required；
//   - 会话或身份存储不可用   → 503 service.not_ready（不是 401，也不是 500）。
//
// 历史实现把这三类都写成 401：于是 Redis 抖动会被答成「你的登录无效」，运维去查权限
// 而不是查依赖。反过来，把它吞成匿名（`actor, _ := h.optionalActor(c)`）会让带了失效
// 会话的调用方按匿名可见集合读到元数据，把「受限字段」读成「公开字段」。
func TestReadActorKeepsAnonymousAndClassifiesCredentialFailures(t *testing.T) {
	anonymous := probeReadActor(t, &Controller{}, "")
	if anonymous.err != nil {
		t.Fatalf("requests without credentials must stay anonymous: %v", anonymous.err)
	}
	if anonymous.actor.ID != 0 {
		t.Fatalf("expected zero actor for anonymous request, got id %d", anonymous.actor.ID)
	}

	viewer := identity.Actor{ID: 7, Status: identity.UserStatusActive}
	healthy := &Controller{sessions: newSessionManager(nil), users: staticActorStore{actor: viewer}}
	signedIn := probeReadActor(t, healthy, loginCookie(t, healthy.sessions))
	if signedIn.err != nil {
		t.Fatalf("expected a usable session to resolve: %v", signedIn.err)
	}
	if signedIn.actor.ID != viewer.ID {
		t.Fatalf("expected resolved actor id %d, got %d", viewer.ID, signedIn.actor.ID)
	}

	// D：会话存储不可用。这是依赖故障，不是身份结论。
	downStore := &Controller{
		sessions: newSessionManager(brokenSessionStore()),
		users:    staticActorStore{actor: viewer},
	}
	if result := probeReadActor(t, downStore, "sforum_session=stale-session-cookie"); !isDependencyFailure(result.err) {
		t.Fatalf("a session store outage must answer 503, got %v", result.err)
	}

	// C：凭据指向的主体已经不存在。
	missingSubject := &Controller{
		sessions: newSessionManager(nil),
		users:    erroringActorStore{err: identity.ErrUserNotFound},
	}
	if result := probeReadActor(t, missingSubject, loginCookie(t, missingSubject.sessions)); !isUnauthorized(result.err) {
		t.Fatalf("a missing subject must answer 401, got %v", result.err)
	}

	// C：主体已解析但账号不可用（禁用）。
	disabled := &Controller{
		sessions: newSessionManager(nil),
		users:    staticActorStore{actor: identity.Actor{ID: 7, Status: identity.UserStatusDisabled}},
	}
	if result := probeReadActor(t, disabled, loginCookie(t, disabled.sessions)); !isUnauthorized(result.err) {
		t.Fatalf("a disabled subject must answer 401, got %v", result.err)
	}
}

// handler 层证据：依赖故障时必须在调用 service 之前就返回 503，且响应体是
// `service.not_ready` + `Retry-After` —— 不是 200 匿名视图（那是静默降级），
// 也不是 401（那会把依赖故障说成身份问题）。
// service 为 nil —— 只要 handler 继续往 service 走就会崩，所以 503 本身就证明
// 「没有降级成匿名读」。
func TestListValuesShortCircuitsBeforeServiceWhenTheSessionStoreIsDown(t *testing.T) {
	// 用真实装配（而不是裸 fiber.New）：503 的 envelope 与 Retry-After 是宿主
	// errorHandler 的职责，裸 app 只会把 typed error 变成 500。
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"},
	}, slog.Default(), apphttp.Dependencies{})
	controller := &Controller{
		sessions: newSessionManager(brokenSessionStore()),
		users:    staticActorStore{actor: identity.Actor{ID: 7, Status: identity.UserStatusActive}},
	}
	app.Get("/entity-meta/:entityType/:entityID", controller.listValues)

	request := httptest.NewRequest(http.MethodGet, "/entity-meta/user/1", nil)
	request.Header.Set(fiber.HeaderCookie, "sforum_session=stale-session-cookie")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("expected 503 for a session store outage, got %d", response.StatusCode)
	}
	if retryAfter := response.Header.Get(fiber.HeaderRetryAfter); retryAfter != "1" {
		t.Fatalf("expected Retry-After: 1 on a dependency failure, got %q", retryAfter)
	}
	body := readBody(t, response.Body)
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode envelope %q: %v", body, err)
	}
	if envelope.Code != fiber.StatusServiceUnavailable || envelope.Data.Reason != "service.not_ready" {
		t.Fatalf("a dependency failure must be answered as service.not_ready, got %q", body)
	}
	if strings.Contains(body, "auth.required") {
		t.Fatalf("a dependency failure must not be reported as an identity problem, got %q", body)
	}
}

// 同一 handler 在没有凭据时必须继续往下走（而不是被 401 拦下）：用一个非法
// entityID 把链路停在「解析参数」这一步，语义即「actor 门已通过、进入匿名视角」。
func TestListValuesLetsAnonymousRequestsThroughTheActorGate(t *testing.T) {
	app := fiber.New()
	controller := &Controller{sessions: newSessionManager(nil), users: staticActorStore{}}
	app.Get("/entity-meta/:entityType/:entityID", controller.listValues)

	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/entity-meta/user/not-a-number", nil))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected the anonymous request to pass the actor gate and fail on the id, got %d", response.StatusCode)
	}
}

type readActorResult struct {
	actor identity.Actor
	err   error
}

// probeReadActor 通过真实请求执行 readActor：会话解析要读 cookie 与 storage，
// 只有真链路才能给出「有凭据 / 无凭据」的差别。
func probeReadActor(t *testing.T, controller *Controller, cookie string) readActorResult {
	t.Helper()
	app := fiber.New()
	var got readActorResult
	app.Get("/probe", func(c fiber.Ctx) error {
		got.actor, got.err = controller.readActor(c)
		return c.SendStatus(fiber.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if cookie != "" {
		request.Header.Set(fiber.HeaderCookie, cookie)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("probe request failed: %v", err)
	}
	response.Body.Close()
	return got
}

// loginCookie 签发一个真实 Host 会话，返回可回放的 cookie 串。
func loginCookie(t *testing.T, sessions *authsession.Manager) string {
	t.Helper()
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		if _, err := sessions.Start(c, 7); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	response, err := app.Test(httptest.NewRequest(http.MethodPost, "/login", nil))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	response.Body.Close()
	cookies := response.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected a session cookie")
	}
	return cookies[0].Name + "=" + cookies[0].Value
}

var errSessionStoreDown = errors.New("session storage unavailable")

// sessionStore 复刻装配层的会话 cookie 名：默认值 session_id 与生产不一致，
// 用它写测试会得到「cookie 根本没被读」的假绿。
func sessionStore(cfg session.Config) *session.Store {
	if cfg.Extractor.Chain == nil {
		cfg.Extractor = extractors.FromCookie("sforum_session")
	}
	return session.NewStore(cfg)
}

func brokenSessionStore() *session.Store {
	return sessionStore(session.Config{Storage: failingStorage{err: errSessionStoreDown}})
}

func newSessionManager(store *session.Store) *authsession.Manager {
	if store == nil {
		store = sessionStore(session.Config{})
	}
	return authsession.NewManager(store, authsession.Config{HashSecret: "entity-meta-reader-test"})
}

func isUnauthorized(err error) bool {
	var fiberErr *fiber.Error
	return errors.As(err, &fiberErr) && fiberErr.Code == fiber.StatusUnauthorized
}

// isDependencyFailure 判定「依赖故障」类错误：底层哨兵必须仍在错误链上
// （HTTP 层只包装、不改写原因）。用它而不是断言某个具体错误类型，是为了让
// 判据落在语义上：只要原始依赖错误可见，就是依赖故障。
func isDependencyFailure(err error) bool {
	return err != nil && (errors.Is(err, authsession.ErrStoreUnavailable) ||
		errors.Is(err, identity.ErrStoreUnavailable) ||
		errors.Is(err, errSessionStoreDown))
}

func readBody(t *testing.T, body io.Reader) string {
	t.Helper()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

type staticActorStore struct{ actor identity.Actor }

func (s staticActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

type erroringActorStore struct{ err error }

func (s erroringActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return identity.Actor{}, s.err
}

// failingStorage 模拟 Redis 整体不可用：Get 直接报错，正是「会话解析失败」的成因。
type failingStorage struct{ err error }

func (s failingStorage) GetWithContext(context.Context, string) ([]byte, error) {
	return nil, s.err
}

func (s failingStorage) Get(string) ([]byte, error) { return nil, s.err }

func (s failingStorage) SetWithContext(context.Context, string, []byte, time.Duration) error {
	return s.err
}

func (s failingStorage) Set(string, []byte, time.Duration) error { return s.err }

func (s failingStorage) DeleteWithContext(context.Context, string) error { return s.err }

func (s failingStorage) Delete(string) error { return s.err }

func (s failingStorage) ResetWithContext(context.Context) error { return s.err }

func (s failingStorage) Reset() error { return s.err }

func (s failingStorage) Close() error { return nil }

// 写路径与私密读路径共用同一个 actor 门（UpsertValues 等）：无凭据必须 401，
// 依赖故障必须 503 —— 两条答案不能互换，也不能都变成 500。
func TestRequireActorGateOnWritePaths(t *testing.T) {
	t.Run("anonymous is rejected as 401 not 503", func(t *testing.T) {
		controller := &Controller{sessions: newSessionManager(nil), users: staticActorStore{}}
		response := probeActorGate(t, controller, "")
		defer response.Body.Close()
		if response.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("expected 401 for an anonymous write, got %d", response.StatusCode)
		}
		body := readBody(t, response.Body)
		if !strings.Contains(body, "auth.required") {
			t.Fatalf("expected auth.required, got %q", body)
		}
		if strings.Contains(body, "service.not_ready") {
			t.Fatalf("an anonymous request must not be reported as a dependency failure: %q", body)
		}
	})

	t.Run("dependency failure is 503 not 401", func(t *testing.T) {
		controller := &Controller{
			sessions: newSessionManager(brokenSessionStore()),
			users:    staticActorStore{actor: identity.Actor{ID: 7, Status: identity.UserStatusActive}},
		}
		response := probeActorGate(t, controller, "sforum_session=stale-session-cookie")
		defer response.Body.Close()
		if response.StatusCode != fiber.StatusServiceUnavailable {
			t.Fatalf("expected 503 for a session store outage, got %d", response.StatusCode)
		}
		if got := response.Header.Get(fiber.HeaderRetryAfter); got != "1" {
			t.Fatalf("expected Retry-After: 1, got %q", got)
		}
		body := readBody(t, response.Body)
		if !strings.Contains(body, "service.not_ready") || strings.Contains(body, "auth.required") {
			t.Fatalf("expected service.not_ready and no identity claim, got %q", body)
		}
	})

	t.Run("missing subject is 401", func(t *testing.T) {
		controller := &Controller{
			sessions: newSessionManager(nil),
			users:    erroringActorStore{err: identity.ErrUserNotFound},
		}
		response := probeActorGate(t, controller, loginCookie(t, controller.sessions))
		defer response.Body.Close()
		if response.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("expected 401 for a missing subject, got %d", response.StatusCode)
		}
	})
}

// probeActorGate 用真实装配打一个只走 actor 门的探针端点。
func probeActorGate(t *testing.T, controller *Controller, cookie string) *http.Response {
	t.Helper()
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"},
	}, slog.Default(), apphttp.Dependencies{})
	app.Get("/probe", func(c fiber.Ctx) error {
		_, err := controller.actor(c)
		if err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if cookie != "" {
		request.Header.Set(fiber.HeaderCookie, cookie)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("probe request: %v", err)
	}
	return response
}
