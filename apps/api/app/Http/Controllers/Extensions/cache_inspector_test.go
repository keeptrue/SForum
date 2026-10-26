package extensionscontroller

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestCacheInspectorHTTPAllowsViewAndLegacyManageAndRedactsSensitiveFields(t *testing.T) {
	app, _, registry, inspector := newCacheInspectorTestApp(t, true)
	viewer := loginCacheInspectorUser(t, app, 1)
	manager := loginCacheInspectorUser(t, app, 2)

	for index, operation := range []string{"get", "set", "invalidate_declared"} {
		inspector.RecordHostCacheTrace(hostapi.HostCacheTrace{
			Operation: operation, Outcome: hostapi.HostCacheTraceAllowed,
			RegistryRevision: registry.Revision(), Duration: time.Duration(index+1) * time.Microsecond,
			TagDigest: strings.Repeat("e", 64), TagCount: 1, InvalidatorID: "forum.topic.updated",
		})
	}

	for name, cookie := range map[string]*http.Cookie{"viewer": viewer, "legacy_manager": manager} {
		t.Run(name, func(t *testing.T) {
			response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/cache-inspector", cookie)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
			}
			defer response.Body.Close()
			var envelope testEnvelope[hostapi.HostCacheInspectionSnapshot]
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data.Registry.Revision != registry.Revision() || len(envelope.Data.Traces) != 3 ||
				len(envelope.Data.Invalidations) != 1 || envelope.Data.Metrics.Samples != 3 {
				t.Fatalf("inspection snapshot = %#v", envelope.Data)
			}
			encoded, err := json.Marshal(envelope.Data)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{`"key"`, `"value"`, `"tags"`, `"tagNames"`, `"token"`, `"lockToken"`, "core.inspect.secret_tag"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("cache inspector disclosed %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestCacheInspectorHTTPValidatesLimitAndEnforcesPermissions(t *testing.T) {
	app, _, registry, inspector := newCacheInspectorTestApp(t, true)
	viewer := loginCacheInspectorUser(t, app, 1)
	denied := loginCacheInspectorUser(t, app, 3)
	for index := 0; index < 3; index++ {
		inspector.RecordHostCacheTrace(hostapi.HostCacheTrace{
			Operation: "get", Outcome: hostapi.HostCacheTraceHit,
			RegistryRevision: registry.Revision(), Duration: time.Microsecond,
		})
	}

	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/cache-inspector?limit=1", viewer)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("limited status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var limited testEnvelope[hostapi.HostCacheInspectionSnapshot]
	if err := json.NewDecoder(response.Body).Decode(&limited); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(limited.Data.Traces) != 1 {
		t.Fatalf("limited traces = %d", len(limited.Data.Traces))
	}

	for _, value := range []string{"0", "-1", "201", "invalid"} {
		response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/cache-inspector?limit="+value, viewer)
		assertCacheInspectorError(t, response, http.StatusUnprocessableEntity, cacheInspectorInvalidReason)
	}
	response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/cache-inspector", nil)
	assertCacheInspectorError(t, response, http.StatusUnauthorized, "auth.required")
	response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/cache-inspector", denied)
	assertCacheInspectorError(t, response, http.StatusForbidden, "permission.denied")
}

func TestCacheInspectorHTTPMapsConflictAndUnavailable(t *testing.T) {
	app, controller, _, _ := newCacheInspectorTestApp(t, true)
	viewer := loginCacheInspectorUser(t, app, 1)
	controller.cacheInspect = func(*cacheregistry.Registry, int) (hostapi.HostCacheInspectionSnapshot, error) {
		return hostapi.HostCacheInspectionSnapshot{}, hostapi.ErrHostCacheInspectorConflict
	}
	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/cache-inspector", viewer)
	assertCacheInspectorError(t, response, http.StatusConflict, cacheInspectorConflictReason)

	unavailable, _, _, _ := newCacheInspectorTestApp(t, false)
	unavailableViewer := loginCacheInspectorUser(t, unavailable, 1)
	response = performExtensionRequest(t, unavailable, http.MethodGet, "/api/v1/admin/extensions/cache-inspector", unavailableViewer)
	assertCacheInspectorError(t, response, http.StatusServiceUnavailable, cacheInspectorUnavailableReason)

	if mapped, ok := mapCacheInspectorError(errors.New("backend failed")).(*fiber.Error); !ok ||
		mapped.Code != http.StatusServiceUnavailable || mapped.Message != cacheInspectorUnavailableReason {
		t.Fatalf("unknown error mapping = %#v", mapped)
	}
}

func TestParseCacheInspectorLimitUsesReviewedDefaultAndBounds(t *testing.T) {
	for raw, want := range map[string]int{"": 100, "1": 1, "200": 200} {
		got, err := parseCacheInspectorLimit(raw)
		if err != nil || got != want {
			t.Fatalf("limit %q = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"0", "-1", "201", "1.5", "invalid"} {
		if _, err := parseCacheInspectorLimit(raw); !errors.Is(err, hostapi.ErrHostCacheInspectorInvalid) {
			t.Fatalf("invalid limit %q error = %v", raw, err)
		}
	}
}

func newCacheInspectorTestApp(
	t *testing.T,
	configured bool,
) (*fiber.App, *Controller, *cacheregistry.Registry, *hostapi.HostCacheInspector) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
		3: {ID: 3, Status: identity.UserStatusActive},
	}}
	controller := NewController(nil, actors, manager)
	registry := cacheregistry.New()
	inspector := hostapi.NewHostCacheInspector(16)
	if configured {
		artifact, err := cacheregistry.NewCoreArtifact("core.inspect", "1.0.0", strings.Repeat("d", 64))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Publish(cacheregistry.Publication{
			Artifact: artifact,
			Caches: []cacheregistry.Declaration{{
				ID: "core.inspect.items", ContractVersion: "core.inspect.items@1",
				Namespace: "core.inspect.items", Policy: cacheregistry.PolicyPublic,
				Tags: []string{"core.inspect.secret_tag"}, Invalidators: []string{"core.inspect.topic_updated"},
			}},
		}); err != nil {
			t.Fatal(err)
		}
		controller.WithCacheInspector(registry, inspector)
	}
	login := extensionRouteProviderFunc(func(router fiber.Router) {
		router.Post("/cache-inspector-login/:id", func(c fiber.Ctx) error {
			id, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			_, err := manager.Start(c, id)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, login},
	})
	return app, controller, registry, inspector
}

func loginCacheInspectorUser(t *testing.T, app *fiber.App, userID int64) *http.Cookie {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "/api/v1/cache-inspector-login/"+strconv.FormatInt(userID, 10), nil)
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

func assertCacheInspectorError(t *testing.T, response *http.Response, status int, reason string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("status=%d want=%d body=%s", response.StatusCode, status, responseBody(t, response))
	}
	var envelope testEnvelope[testErrorData]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Reason != reason {
		t.Fatalf("reason=%q want=%q", envelope.Data.Reason, reason)
	}
}
