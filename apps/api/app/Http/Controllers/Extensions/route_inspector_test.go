package extensionscontroller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestRouteInspectorHTTPAllowsViewAndLegacyManageWithoutRawRequestData(t *testing.T) {
	app, _ := newRouteInspectorTestApp(t, true)
	viewer := loginRouteInspectorUser(t, app, 1)
	legacyManager := loginRouteInspectorUser(t, app, 2)

	values := url.Values{}
	values.Set("method", "POST")
	values.Set("path", "/controller/topics?token=secret-value")
	endpoint := "/api/v1/admin/extensions/route-inspector?" + values.Encode()
	for name, cookie := range map[string]*http.Cookie{"viewer": viewer, "legacy_manager": legacyManager} {
		t.Run(name, func(t *testing.T) {
			response := performExtensionRequest(t, app, http.MethodGet, endpoint, cookie)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
			}
			defer response.Body.Close()
			var envelope testEnvelope[routes.RouteInspectorSnapshot]
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data.Method != "POST" || envelope.Data.Resolution != routes.InspectionAmbiguous ||
				envelope.Data.Revision == 0 || len(envelope.Data.Chain) != 0 || len(envelope.Data.Conflicts) != 1 {
				t.Fatalf("inspection snapshot = %#v", envelope.Data)
			}
			encoded, err := json.Marshal(envelope.Data)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "token=") {
				t.Fatalf("inspector leaked raw request data: %s", encoded)
			}
		})
	}
}

func TestRouteInspectorHTTPRequiresAuthenticationAndExtensionView(t *testing.T) {
	app, _ := newRouteInspectorTestApp(t, true)
	endpoint := "/api/v1/admin/extensions/route-inspector?method=POST&path=%2Fcontroller%2Ftopics"
	unauthenticated := performExtensionRequest(t, app, http.MethodGet, endpoint, nil)
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.StatusCode)
	}
	unauthenticated.Body.Close()

	denied := loginRouteInspectorUser(t, app, 3)
	response := performExtensionRequest(t, app, http.MethodGet, endpoint, denied)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("no-view status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()
}

func TestRouteInspectorHTTPValidatesExplicitMethodAndPathAndFailsClosedWhenUnavailable(t *testing.T) {
	app, _ := newRouteInspectorTestApp(t, true)
	viewer := loginRouteInspectorUser(t, app, 1)
	for _, endpoint := range []string{
		"/api/v1/admin/extensions/route-inspector?path=%2Fcontroller%2Ftopics",
		"/api/v1/admin/extensions/route-inspector?method=POST",
		"/api/v1/admin/extensions/route-inspector?method=%2A&path=%2Fcontroller%2Ftopics",
		"/api/v1/admin/extensions/route-inspector?method=POST&path=relative",
	} {
		response := performExtensionRequest(t, app, http.MethodGet, endpoint, viewer)
		if response.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s status=%d body=%s", endpoint, response.StatusCode, responseBody(t, response))
		}
		var envelope testEnvelope[testErrorData]
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if envelope.Data.Reason != routeInspectorInvalidReason {
			t.Fatalf("%s reason=%q", endpoint, envelope.Data.Reason)
		}
	}

	unavailableApp, _ := newRouteInspectorTestApp(t, false)
	unavailableViewer := loginRouteInspectorUser(t, unavailableApp, 1)
	response := performExtensionRequest(
		t, unavailableApp, http.MethodGet,
		"/api/v1/admin/extensions/route-inspector?method=POST&path=%2Fcontroller%2Ftopics",
		unavailableViewer,
	)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()
}

func newRouteInspectorTestApp(t *testing.T, configured bool) (*fiber.App, *authsession.Manager) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
		3: {ID: 3, Status: identity.UserStatusActive},
	}}
	controller := NewController(nil, actors, manager)
	if configured {
		registry, _, _, _ := routeProviderControllerRegistry(t)
		providers := routes.NewProviderSelectionAPI(registry, &routeProviderControllerStore{})
		controller.WithRouteProviderSelection(providers, &routeProviderControllerAuditor{nextID: 70})
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

func loginRouteInspectorUser(t *testing.T, app *fiber.App, userID int64) *http.Cookie {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "/api/v1/route-inspector-login/"+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		t.Fatalf("login %d status=%d cookies=%d", userID, response.StatusCode, len(response.Cookies()))
	}
	return response.Cookies()[0]
}

// M5 任务 7/8：崩溃循环隔离的持久状态必须有管理员可读端点，并且
// view/manage 都可读、匿名拒绝、未配置时 fail-closed、空账本是正常状态。
func TestRouteIncidentLedgerHTTPAllowsViewDeniesAnonymousAndFailsClosed(t *testing.T) {
	reader := &routeIncidentReaderStub{records: []apphttp.RouteRuntimeIncidentRecord{{
		ID: 41, LocalResult: apphttp.RouteRuntimeIncidentQuarantined, AuditEventID: 9001,
		Evidence: apphttp.RouteRuntimeIncidentEvidence{
			IncidentKey: "incident-key-41", RouteID: "acme.demo.route.read",
			ContractVersion: "acme.demo.route.read@1", Method: "GET", PathSignature: "/api/v1/acme-demo",
			Phase: routes.RoutePhaseHandler, Action: "add", FailureCode: routes.RouteFailureTransportFailed,
			CauseClass: "runtime_transport", CommitState: routes.RouteCommitFinal,
			Artifact: routes.PluginArtifact{
				ExtensionID: "acme.demo", ExtensionVersion: "1.0.0",
				PackageDigest: strings.Repeat("a", 64), RuntimeInstanceID: "instance-41",
			},
		},
	}}}
	app, _ := newRouteIncidentTestApp(t, reader)
	endpoint := "/api/v1/admin/extensions/route-incidents"
	for name, userID := range map[string]int64{"viewer": 1, "legacy_manager": 2} {
		t.Run(name, func(t *testing.T) {
			cookie := loginRouteInspectorUser(t, app, userID)
			response := performExtensionRequest(t, app, http.MethodGet, endpoint, cookie)
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
			}
			var envelope struct {
				Data struct {
					Limit     int                                 `json:"limit"`
					Summary   apphttp.RouteRuntimeIncidentSummary `json:"summary"`
					Incidents []apphttp.RouteRuntimeIncidentView  `json:"incidents"`
				} `json:"data"`
			}
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data.Limit != apphttp.RouteRuntimeIncidentReadMaximum || len(envelope.Data.Incidents) != 1 {
				t.Fatalf("ledger = %#v", envelope.Data)
			}
			incident := envelope.Data.Incidents[0]
			if !incident.Quarantined || incident.LocalResult != string(apphttp.RouteRuntimeIncidentQuarantined) ||
				incident.ExtensionID != "acme.demo" || incident.RuntimeInstanceID != "instance-41" ||
				incident.FailureCode != string(routes.RouteFailureTransportFailed) {
				t.Fatalf("incident view = %#v", incident)
			}
			if envelope.Data.Summary.Quarantined != 1 {
				t.Fatalf("summary = %#v", envelope.Data.Summary)
			}
		})
	}

	anonymous := performExtensionRequest(t, app, http.MethodGet, endpoint, nil)
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", anonymous.StatusCode)
	}

	denied := performExtensionRequest(t, app, http.MethodGet, endpoint, loginRouteInspectorUser(t, app, 3))
	denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("no-permission status=%d", denied.StatusCode)
	}

	unconfigured, _ := newRouteIncidentTestApp(t, nil)
	unavailable := performExtensionRequest(
		t, unconfigured, http.MethodGet, endpoint, loginRouteInspectorUser(t, unconfigured, 1),
	)
	unavailable.Body.Close()
	if unavailable.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status=%d", unavailable.StatusCode)
	}
}

// 空账本是正常状态：端点必须返回 200 + 空数组，而不是 404/503，
// 否则管理端无法区分「没有事故」与「读取失败」。
func TestRouteIncidentLedgerHTTPReportsEmptyLedgerAsSuccess(t *testing.T) {
	app, _ := newRouteIncidentTestApp(t, &routeIncidentReaderStub{})
	response := performExtensionRequest(
		t, app, http.MethodGet, "/api/v1/admin/extensions/route-incidents",
		loginRouteInspectorUser(t, app, 1),
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	body := responseBody(t, response)
	if !strings.Contains(body, "\"incidents\":[]") {
		t.Fatalf("empty ledger body=%s", body)
	}
}

type routeIncidentReaderStub struct {
	records []apphttp.RouteRuntimeIncidentRecord
	err     error
}

func (s *routeIncidentReaderStub) ListRecentRouteRuntimeIncidents(
	context.Context,
	int,
) ([]apphttp.RouteRuntimeIncidentRecord, error) {
	if s == nil {
		return nil, errors.New("reader is not configured")
	}
	return s.records, s.err
}

func newRouteIncidentTestApp(
	t *testing.T,
	reader apphttp.RouteRuntimeIncidentReader,
) (*fiber.App, *authsession.Manager) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
		3: {ID: 3, Status: identity.UserStatusActive},
	}}
	controller := NewController(nil, actors, manager).WithRouteIncidentReader(reader)
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
