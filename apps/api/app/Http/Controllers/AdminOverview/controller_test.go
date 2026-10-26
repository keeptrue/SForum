package adminoverviewcontroller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	adminoverview "github.com/keeptrue/sforum/apps/api/app/Models/AdminOverview"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

type apiEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type apiErrorData struct {
	Reason string `json:"reason"`
}

func TestOverviewRequiresAuthentication(t *testing.T) {
	app, _ := newOverviewTestApp(identity.Actor{}, &overviewHTTPStore{})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/overview", nil))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	var body apiEnvelope[apiErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Reason != "auth.required" {
		t.Fatalf("expected auth.required, got %#v", body)
	}
}

func TestOverviewRequiresAdminAccess(t *testing.T) {
	app, cookie := newOverviewTestApp(identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{}}, &overviewHTTPStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/overview", nil)
	req.AddCookie(cookie)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	var body apiEnvelope[apiErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Reason != "permission.denied" {
		t.Fatalf("expected permission.denied, got %#v", body)
	}
}

func TestOverviewReturnsEnvelope(t *testing.T) {
	app, cookie := newOverviewTestApp(overviewHTTPActor(), &overviewHTTPStore{snapshot: adminoverview.StoreSnapshot{
		Community: adminoverview.CommunityStats{
			UserCount:  3,
			TopicCount: 8,
		},
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/overview", nil)
	req.AddCookie(cookie)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body apiEnvelope[adminoverview.AdminOverview]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Community.UserCount != 3 || body.Data.Community.TopicCount != 8 {
		t.Fatalf("unexpected overview payload: %#v", body.Data)
	}
	if body.Data.Runtime.MemoryBytes == 0 {
		t.Fatalf("expected runtime memory in payload, got %#v", body.Data.Runtime)
	}
	if body.Data.Runtime.Build.Name != "SForum" || body.Data.Runtime.Build.Version == "" {
		t.Fatalf("expected build identity in payload, got %#v", body.Data.Runtime.Build)
	}
}

func TestResourcesReturnsEnvelope(t *testing.T) {
	usage := adminoverview.RuntimeUsage{
		APIMemoryBytes:   32 * 1024 * 1024,
		TotalMemoryBytes: 32 * 1024 * 1024,
		APICPUPercent:    1.25,
		TotalCPUPercent:  1.25,
	}
	disk := adminoverview.DiskRuntimeStats{
		TotalBytes:  2048,
		UsedBytes:   512,
		FreeBytes:   1536,
		UsedPercent: 25,
	}
	loadAverage := adminoverview.SystemLoadAverage{OneMinute: 0.25, FiveMinutes: 0.5, FifteenMinutes: 0.75}
	app, cookie := newOverviewTestApp(overviewHTTPActor(), &overviewHTTPStore{}, adminoverview.RuntimeStats{
		MemoryBytes: 1,
		Resources:   &usage,
		Disk:        &disk,
		LoadAverage: &loadAverage,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/overview/resources", nil)
	req.AddCookie(cookie)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body apiEnvelope[adminoverview.AdminOverviewResources]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Resources == nil || body.Data.Resources.APIMemoryBytes != usage.APIMemoryBytes {
		t.Fatalf("resources=%#v", body.Data.Resources)
	}
	if body.Data.Disk == nil || body.Data.Disk.UsedPercent != 25 {
		t.Fatalf("disk=%#v", body.Data.Disk)
	}
	if body.Data.LoadAverage == nil || body.Data.LoadAverage.FifteenMinutes != 0.75 {
		t.Fatalf("loadAverage=%#v", body.Data.LoadAverage)
	}
}

func TestResourcesRequiresAdminAccess(t *testing.T) {
	app, cookie := newOverviewTestApp(identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{}}, &overviewHTTPStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/overview/resources", nil)
	req.AddCookie(cookie)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func newOverviewTestApp(actor identity.Actor, store *overviewHTTPStore, runtime ...adminoverview.RuntimeStats) (*fiber.App, *http.Cookie) {
	sessions := authsession.NewManager(session.NewStore(), authsession.Config{})
	stats := adminoverview.RuntimeStats{MemoryBytes: 1, HeapAllocBytes: 1, GoroutineCount: 1}
	if len(runtime) > 0 {
		stats = runtime[0]
	}
	controller := NewController(
		adminoverview.NewService(store, adminoverview.StaticRuntimeProvider{Stats: stats}),
		overviewHTTPActorStore{actor: actor},
		sessions,
	)
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"}}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{
			routeProviderFunc(func(api fiber.Router) {
				api.Post("/test-login", func(c fiber.Ctx) error {
					_, err := sessions.Start(c, actor.ID)
					return err
				})
			}),
			controller,
		},
	})

	loginResp, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/test-login", nil))
	if err != nil {
		panic(err)
	}
	defer loginResp.Body.Close()
	cookies := loginResp.Cookies()
	if len(cookies) == 0 {
		panic("expected session cookie")
	}
	return app, cookies[0]
}

func overviewHTTPActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAdminAccess: true},
	}
}

type routeProviderFunc func(api fiber.Router)

func (fn routeProviderFunc) RegisterRoutes(api fiber.Router) {
	fn(api)
}

type overviewHTTPActorStore struct {
	actor identity.Actor
}

func (s overviewHTTPActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

type overviewHTTPStore struct {
	snapshot adminoverview.StoreSnapshot
}

func (s *overviewHTTPStore) Snapshot(context.Context, time.Time) (adminoverview.StoreSnapshot, error) {
	return s.snapshot, nil
}
