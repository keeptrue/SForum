package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	httpserver "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestHostLocalSessionAuthorityPath(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
		want   bool
	}{
		{method: "POST", path: "/api/v1/auth/logout", want: true},
		{method: "post", path: "/api/v1/Auth/Logout/", want: true},
		{method: "DELETE", path: "/api/v1/auth/sessions/session-1", want: true},
		{method: "delete", path: "/API/V1/AUTH/SESSIONS/session-1", want: true},
		{method: "POST", path: "/api/v1/auth/sessions/revoke-others", want: true},
		{method: "POST", path: "/api/v1/users/7/sessions/revoke", want: true},
		{method: "POST", path: "/api/v1/auth/other/../logout", want: false},
		{method: "POST", path: "/api//v1/auth/logout", want: false},
		{method: "POST", path: "/plugin/revoke-alias", want: false},
		{method: "POST", path: "/api/v1/users/7/sessions", want: false},
		{method: "GET", path: "/api/v1/auth/sessions", want: false},
		{method: "POST", path: "/api/v1/auth/login", want: false},
	} {
		if got := hostLocalSessionAuthorityPath(test.method, test.path); got != test.want {
			t.Fatalf("%s %s host-local=%t want=%t", test.method, test.path, got, test.want)
		}
	}
}

func TestHostLocalSessionRoutesBypassDueRenewalThroughDispatcher(t *testing.T) {
	const renewalInterval = 100 * time.Millisecond
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Core: routes.CoreRouteCatalog()}); err != nil {
		t.Fatal(err)
	}
	var gateCalls atomic.Int32
	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{
			RenewalInterval: renewalInterval,
			HashSecret:      "route-session-policy-test",
			RenewalEffectGate: func(ctx context.Context, _ int64, _ int64, effect authsession.RenewalEffect) error {
				gateCalls.Add(1)
				return effect(ctx)
			},
		},
	)
	plans := sessionPolicyRoutePlanResolver{registry: registry}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{Plans: plans})
	app := httpserver.NewApp(config.Config{}, slog.Default(), httpserver.Dependencies{
		RoutePlans:      plans,
		RouteDispatcher: dispatcher,
		RouteActors: func(c fiber.Ctx) (identity.Actor, error) {
			return loadSessionPolicyAwareRouteActor(c, manager, sessionPolicyActorStore{})
		},
		RouteProviders: []httpserver.RouteProvider{sessionPolicyRouteProvider{sessions: manager}},
	})

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/v1/auth/logout"},
		{method: http.MethodDelete, path: "/api/v1/auth/sessions/session-1"},
		{method: http.MethodPost, path: "/api/v1/auth/sessions/revoke-others"},
		{method: http.MethodPost, path: "/api/v1/users/7/sessions/revoke"},
		{method: http.MethodPost, path: "/api/v1/Auth/Logout/"},
	} {
		cookie := issueSessionPolicyRouteCookie(t, manager)
		time.Sleep(renewalInterval + 25*time.Millisecond)
		before := gateCalls.Load()
		request := httptest.NewRequest(test.method, test.path, nil)
		request.AddCookie(cookie)
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("%s %s: %v", test.method, test.path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent || gateCalls.Load() != before {
			t.Fatalf("%s %s status=%d gate before=%d after=%d", test.method, test.path, response.StatusCode, before, gateCalls.Load())
		}
	}

	oldCookie := issueSessionPolicyRouteCookie(t, manager)
	time.Sleep(renewalInterval + 25*time.Millisecond)
	before := gateCalls.Load()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.AddCookie(oldCookie)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent || gateCalls.Load() != before+1 {
		t.Fatalf("ordinary session status=%d gate before=%d after=%d", response.StatusCode, before, gateCalls.Load())
	}
	if len(response.Cookies()) != 1 || response.Cookies()[0].Value == oldCookie.Value {
		t.Fatalf("ordinary session did not rotate cookie: %#v", response.Cookies())
	}
}

type sessionPolicyRoutePlanResolver struct {
	registry *routes.Registry
}

func (r sessionPolicyRoutePlanResolver) BuildExecutionPlan(
	_ context.Context,
	method string,
	path string,
) (routes.RouteExecutionPlan, error) {
	return r.registry.BuildExecutionPlan(method, path)
}

type sessionPolicyActorStore struct{}

func (sessionPolicyActorStore) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	return identity.Actor{
		ID: userID, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionUserManage: true},
	}, nil
}

type sessionPolicyRouteProvider struct {
	sessions *authsession.Manager
}

func (p sessionPolicyRouteProvider) RegisterRoutes(api fiber.Router) {
	auth := api.Group("/auth")
	auth.Post("/logout", func(c fiber.Ctx) error {
		if err := p.sessions.Destroy(c); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	auth.Get("/session", func(c fiber.Ctx) error {
		return requireSessionPolicyRouteUser(c, p.sessions, true)
	})
	auth.Post("/sessions/revoke-others", func(c fiber.Ctx) error {
		return requireSessionPolicyRouteUser(c, p.sessions, false)
	})
	auth.Delete("/sessions/:sessionId", func(c fiber.Ctx) error {
		return requireSessionPolicyRouteUser(c, p.sessions, false)
	})
	api.Post("/users/:userID/sessions/revoke", func(c fiber.Ctx) error {
		return requireSessionPolicyRouteUser(c, p.sessions, false)
	})
}

func requireSessionPolicyRouteUser(c fiber.Ctx, sessions *authsession.Manager, renew bool) error {
	var (
		userID int64
		ok     bool
		err    error
	)
	if renew {
		userID, ok, err = sessions.CurrentUserID(c)
	} else {
		userID, ok, err = sessions.CurrentUserIDWithoutRenewal(c)
	}
	if err != nil {
		return err
	}
	if !ok || userID != 42 {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func issueSessionPolicyRouteCookie(t *testing.T, manager *authsession.Manager) *http.Cookie {
	t.Helper()
	app := fiber.New()
	app.Post("/issue", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	response, err := app.Test(httptest.NewRequest(http.MethodPost, "/issue", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) != 1 {
		t.Fatalf("issue session status=%d cookies=%#v", response.StatusCode, response.Cookies())
	}
	return response.Cookies()[0]
}
