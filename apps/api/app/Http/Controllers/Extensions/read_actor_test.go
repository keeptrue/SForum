package extensionscontroller

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// 插件代理路由的 access 判定（login / permission）建立在这个结果上：
// 依赖故障必须能上抛成 503，而不是「当成匿名」把受限插件路由放行。
func TestOptionalActorClassifiesStoreFailures(t *testing.T) {
	controller := &Controller{
		users: staticExtensionActorStore{actor: identity.Actor{ID: 7, Status: identity.UserStatusActive}},
		sessions: authsession.NewManager(session.NewStore(session.Config{
			Extractor: extractors.FromCookie("sforum_session"),
			Storage:   failingReadStore{},
		}), authsession.Config{HashSecret: "extensions-read-actor"}),
	}
	app := fiber.New()
	var (
		hasActor bool
		gotErr   error
	)
	app.Get("/probe", func(c fiber.Ctx) error {
		_, hasActor, gotErr = controller.optionalActor(c)
		return c.SendStatus(fiber.StatusNoContent)
	})
	request := httptest.NewRequest(fiber.MethodGet, "/probe", nil)
	request.Header.Set(fiber.HeaderCookie, "sforum_session=stale-session-cookie")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	response.Body.Close()

	if hasActor {
		t.Fatal("a store outage must not be reported as an authenticated actor")
	}
	if !errors.Is(gotErr, authsession.ErrStoreUnavailable) {
		t.Fatalf("expected the dependency sentinel to stay in the chain, got %v", gotErr)
	}
}

type staticExtensionActorStore struct{ actor identity.Actor }

func (s staticExtensionActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

type failingReadStore struct{}

func (failingReadStore) GetWithContext(context.Context, string) ([]byte, error) {
	return nil, errors.New("redis: connection refused")
}

func (failingReadStore) Get(string) ([]byte, error) {
	return nil, errors.New("redis: connection refused")
}

func (failingReadStore) SetWithContext(context.Context, string, []byte, time.Duration) error {
	return nil
}

func (failingReadStore) Set(string, []byte, time.Duration) error { return nil }

func (failingReadStore) DeleteWithContext(context.Context, string) error { return nil }

func (failingReadStore) Delete(string) error { return nil }

func (failingReadStore) ResetWithContext(context.Context) error { return nil }

func (failingReadStore) Reset() error { return nil }

func (failingReadStore) Close() error { return nil }
