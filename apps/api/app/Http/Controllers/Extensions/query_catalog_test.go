package extensionscontroller

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	"github.com/keeptrue/sforum/apps/api/config"
)

type queryCatalogEnvelope struct {
	Data queryCatalogResponse `json:"data"`
}

func newQueryCatalogTestApp(t *testing.T, configured bool) (*fiber.App, *authsession.Manager) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
		3: {ID: 3, Status: identity.UserStatusActive},
	}}
	controller := NewController(nil, actors, manager)
	if configured {
		catalog, err := hostapi.NewQueryRegistryCoreCatalog()
		if err != nil {
			t.Fatal(err)
		}
		controller.WithQueryCatalog(catalog)
	}
	login := extensionRouteProviderFunc(func(router fiber.Router) {
		router.Post("/route-inspector-login/:id", func(c fiber.Ctx) error {
			id, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			_, err := manager.Start(c, id)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, login},
	})
	return app, manager
}

func TestQueryCatalogHTTPReturnsSealedCatalogForViewers(t *testing.T) {
	app, _ := newQueryCatalogTestApp(t, true)
	for name, userID := range map[string]int64{"viewer": 1, "legacy_manager": 2} {
		t.Run(name, func(t *testing.T) {
			cookie := loginRouteInspectorUser(t, app, userID)
			response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/query-catalog", cookie)
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
			}
			var envelope queryCatalogEnvelope
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			data := envelope.Data
			// 10 条 stable（含 1 条 consent-gated）+ 7 条 shortcode 投影 = 密封目录全量；
			// 摘要必须与目录一致。
			if data.ExtensionID != hostapi.QueryRegistryCoreExtensionID ||
				data.Total != 10+len(hostapi.ShortcodeProjectionQueryIDs()) || data.Total != len(data.Queries) {
				t.Fatalf("catalog = %#v", data)
			}
			if data.CostMaximum != 500 || data.ArtifactDigest == "" {
				t.Fatalf("catalog budget/digest = %#v", data)
			}
			var stats *queryCatalogEntryView
			for index := range data.Queries {
				if data.Queries[index].HostQueryID == hostapi.QuerySiteStats {
					stats = &data.Queries[index]
				}
				if data.Queries[index].ResultSchemaDigest == "" || len(data.Queries[index].Fields) == 0 {
					t.Fatalf("entry incomplete: %#v", data.Queries[index])
				}
			}
			if stats == nil || stats.Pagination != "none" || len(stats.Fields) != 4 ||
				stats.QueryID != "core.query.site_stats.current" || stats.HostPlanVersion != hostapi.QueryStableCorePlanVersion {
				t.Fatalf("site stats entry = %#v", stats)
			}
		})
	}
}

func TestQueryCatalogHTTPRequiresAuthenticationAndExtensionView(t *testing.T) {
	app, _ := newQueryCatalogTestApp(t, true)
	endpoint := "/api/v1/admin/extensions/query-catalog"
	unauthenticated := performExtensionRequest(t, app, http.MethodGet, endpoint, nil)
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.StatusCode)
	}
	unauthenticated.Body.Close()

	denied := performExtensionRequest(t, app, http.MethodGet, endpoint, loginRouteInspectorUser(t, app, 3))
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("no-view status=%d body=%s", denied.StatusCode, responseBody(t, denied))
	}
	denied.Body.Close()
}

func TestQueryCatalogHTTPFailsClosedWhenUnavailable(t *testing.T) {
	app, _ := newQueryCatalogTestApp(t, false)
	viewer := loginRouteInspectorUser(t, app, 1)
	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/query-catalog", viewer)
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var payload struct {
		Data struct {
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Reason != queryCatalogUnavailableReason {
		t.Fatalf("reason = %q", payload.Data.Reason)
	}
}
