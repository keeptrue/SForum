package entitymetacontroller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// 用户名下没凭据、有凭据解析不出来，是两件不同的事：
//   - 没有凭据 → 零值 Actor + 200 匿名视角；
//   - 有凭据但会话/令牌解析失败 → 401。
//
// 把后者吞掉（历史实现是 `actor, _ := h.optionalActor(c)`）会让一个带了失效会话的
// 调用方按匿名可见集合读到元数据，即把「受限字段」读成「公开字段」。
func TestReadActorKeepsAnonymousAndRejectsBrokenCredential(t *testing.T) {
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

	broken := &Controller{
		sessions: newSessionManager(brokenSessionStore()),
		users:    staticActorStore{actor: viewer},
	}
	if result := probeReadActor(t, broken, "sforum_session=stale-session-cookie"); !isUnauthorized(result.err) {
		t.Fatalf("an unresolvable credential must fail closed with 401, got %v", result.err)
	}
}

// handler 层证据：会话解析失败时必须在调用 service 之前就返回 401。
// service 为 nil —— 只要 handler 继续往 service 走就会崩，所以 401 本身就证明
// 「没有降级成匿名读」。
func TestListValuesShortCircuitsBeforeServiceWhenCredentialIsBroken(t *testing.T) {
	app := fiber.New()
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

	if response.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("expected 401 for a broken credential, got %d", response.StatusCode)
	}
	// 裸 fiber app 没有宿主 errorHandler，401 的 message 就是传给 fiber.NewError 的
	// reason 键；装配后的完整 envelope 由 http 层测试覆盖。
	if body := readBody(t, response.Body); !strings.Contains(body, "auth.required") {
		t.Fatalf("expected the auth.required reason, got %q", body)
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
