package systemupdatescontroller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	systemupdates "github.com/keeptrue/sforum/apps/api/app/Models/SystemUpdates"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

type systemUpdatesEnvelope struct {
	Data systemupdates.Status `json:"data"`
}

func TestStatusRequiresAdminAccess(t *testing.T) {
	app, cookie := newSystemUpdatesTestApp(identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/system-updates", nil)
	req.AddCookie(cookie)

	response, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.StatusCode)
	}
}

func TestStatusReturnsReleaseState(t *testing.T) {
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionAdminAccess: true}}
	app, cookie := newSystemUpdatesTestApp(actor)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/system-updates", nil)
	req.AddCookie(cookie)

	response, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	var body systemUpdatesEnvelope
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Data.UpdateAvailable || body.Data.LatestVersion != "1.1.0" {
		t.Fatalf("unexpected status: %#v", body.Data)
	}
}

func TestCheckNowRequiresSiteSettingsPermission(t *testing.T) {
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionAdminAccess: true}}
	app, cookie := newSystemUpdatesTestApp(actor)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system-updates/check", nil)
	req.AddCookie(cookie)

	response, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.StatusCode)
	}
}

func TestCheckNowAllowsSiteSettingsManager(t *testing.T) {
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true}}
	app, cookie := newSystemUpdatesTestApp(actor)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system-updates/check", nil)
	req.AddCookie(cookie)

	response, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
}

func newSystemUpdatesTestApp(actor identity.Actor) (*fiber.App, *http.Cookie) {
	sessions := authsession.NewManager(session.NewStore(), authsession.Config{})
	service := systemupdates.NewService(
		systemUpdatesSource{},
		systemupdates.WithHTTPClient(systemUpdatesHTTPClient{}),
		systemupdates.WithBuildProvider(func() platformversion.BuildInfo { return platformversion.BuildInfo{Version: "1.0.0"} }),
	)
	controller := NewController(service, systemUpdatesActorStore{actor: actor}, sessions)
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"}}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{
			systemUpdatesRouteProvider(func(api fiber.Router) {
				api.Post("/test-login", func(c fiber.Ctx) error {
					_, err := sessions.Start(c, actor.ID)
					return err
				})
			}),
			controller,
		},
	})

	loginResponse, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/test-login", nil))
	if err != nil {
		panic(err)
	}
	defer loginResponse.Body.Close()
	cookies := loginResponse.Cookies()
	if len(cookies) == 0 {
		panic("expected session cookie")
	}
	return app, cookies[0]
}

type systemUpdatesSource struct{}

func (systemUpdatesSource) GitHubMirrorURL(context.Context) (string, error) { return "", nil }

type systemUpdatesHTTPClient struct{}

func (systemUpdatesHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`[{"tag_name":"v1.1.0"}]`)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

type systemUpdatesActorStore struct{ actor identity.Actor }

func (s systemUpdatesActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

type systemUpdatesRouteProvider func(fiber.Router)

func (fn systemUpdatesRouteProvider) RegisterRoutes(api fiber.Router) { fn(api) }
