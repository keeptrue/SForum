package http

import (
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestNewAppDispatchesArbitraryPublicAdminAndAPIPaths(t *testing.T) {
	harness := newArbitraryRouteHarness(t, false, false)
	harness.permission = true

	tests := []struct {
		method     string
		path       string
		body       string
		want       string
		pluginCall bool
	}{
		{stdhttp.MethodGet, "/plugin/docs/intro", "", "arbitrary.demo.public", true},
		{stdhttp.MethodGet, "/plugin/legacy/intro", "", "alias:intro", false},
		{stdhttp.MethodPost, "/admin/plugin/rebuild", `{}`, "arbitrary.demo.admin", true},
		{stdhttp.MethodGet, "/api/v1/plugin/report", "", "arbitrary.demo.api", true},
	}
	wantPluginCalls := int64(0)
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.Header.Set("Accept-Language", "en-US,en;q=0.9")
		if test.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := harness.app.Test(request)
		if err != nil {
			t.Fatalf("%s %s: %v", test.method, test.path, err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != stdhttp.StatusOK || string(body) != test.want {
			t.Fatalf("%s %s status=%d body=%q", test.method, test.path, response.StatusCode, body)
		}
		if test.pluginCall {
			wantPluginCalls++
		}
	}
	if harness.routeCalls.Load() != wantPluginCalls {
		t.Fatalf("runtime calls=%d", harness.routeCalls.Load())
	}
	if got := harness.lastLocale(); got != "en-US" {
		t.Fatalf("negotiated locale=%q", got)
	}
}

func TestNewAppArbitraryAdminRoutePreservesCSRFAndPermissionAuthority(t *testing.T) {
	harness := newArbitraryRouteHarness(t, false, true)

	prime := httptest.NewRequest(stdhttp.MethodGet, "/plugin/docs/intro", nil)
	prime.Header.Set("Origin", "https://forum.example.com")
	primeResponse, err := harness.app.Test(prime)
	if err != nil {
		t.Fatal(err)
	}
	token := arbitraryRouteCSRFToken(t, primeResponse)
	primeResponse.Body.Close()
	baselineRuntime := harness.routeCalls.Load()
	baselineActors := harness.actorCalls.Load()

	missing := httptest.NewRequest(stdhttp.MethodPost, "/admin/plugin/rebuild", strings.NewReader(`{}`))
	missing.Header.Set("Content-Type", "application/json")
	missingResponse, err := harness.app.Test(missing)
	if err != nil {
		t.Fatal(err)
	}
	missingResponse.Body.Close()
	if missingResponse.StatusCode != stdhttp.StatusForbidden || harness.routeCalls.Load() != baselineRuntime ||
		harness.actorCalls.Load() != baselineActors {
		t.Fatalf("missing CSRF status=%d runtime=%d actors=%d", missingResponse.StatusCode, harness.routeCalls.Load(), harness.actorCalls.Load())
	}

	denied := arbitraryRouteUnsafeRequest(token)
	deniedResponse, err := harness.app.Test(denied)
	if err != nil {
		t.Fatal(err)
	}
	deniedResponse.Body.Close()
	if deniedResponse.StatusCode != stdhttp.StatusForbidden || harness.routeCalls.Load() != baselineRuntime {
		t.Fatalf("denied status=%d runtime=%d", deniedResponse.StatusCode, harness.routeCalls.Load())
	}

	harness.permission = true
	allowedResponse, err := harness.app.Test(arbitraryRouteUnsafeRequest(token))
	if err != nil {
		t.Fatal(err)
	}
	allowedResponse.Body.Close()
	if allowedResponse.StatusCode != stdhttp.StatusOK || harness.routeCalls.Load() != baselineRuntime+1 {
		t.Fatalf("allowed status=%d runtime=%d", allowedResponse.StatusCode, harness.routeCalls.Load())
	}
}

func TestNewAppUnknownNuxtPathBypassesRegistryAuthority(t *testing.T) {
	harness := newArbitraryRouteHarness(t, false, true)
	auth := &arbitraryRouteRejectingBearer{}
	harness.app = newArbitraryRouteApp(harness, auth)

	request := httptest.NewRequest(stdhttp.MethodPost, "/admin/host-owned-page", strings.NewReader(`{"value":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer sft_untrusted")
	response, err := harness.app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusNotFound || auth.calls.Load() != 0 ||
		harness.actorCalls.Load() != 0 || harness.routeCalls.Load() != 0 {
		t.Fatalf("status=%d bearer=%d actors=%d runtime=%d", response.StatusCode, auth.calls.Load(), harness.actorCalls.Load(), harness.routeCalls.Load())
	}
}

func TestNewAppUnknownWebSocketPathFailsClosedBeforeHostAuthority(t *testing.T) {
	harness := newArbitraryRouteHarness(t, false, true)
	auth := &arbitraryRouteRejectingBearer{}
	harness.app = newArbitraryRouteApp(harness, auth)

	request := httptest.NewRequest(stdhttp.MethodGet, "/socket/not-declared", nil)
	request.Header.Set("Connection", "keep-alive, Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Origin", "http://example.com")
	response, err := harness.app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusNotFound || auth.calls.Load() != 0 ||
		harness.actorCalls.Load() != 0 || harness.routeCalls.Load() != 0 {
		t.Fatalf("status=%d bearer=%d actors=%d runtime=%d", response.StatusCode,
			auth.calls.Load(), harness.actorCalls.Load(), harness.routeCalls.Load())
	}
}

func TestNewAppArbitraryRouteAuthenticatesBearerBeforeLoadingActor(t *testing.T) {
	harness := newArbitraryRouteHarness(t, false, true)
	auth := &arbitraryRouteAcceptingBearer{}
	harness.actorOverride = func(c fiber.Ctx) (identity.Actor, error) {
		userID, ok := apitokens.UserIDFromContext(c.Context())
		if !ok || userID != 42 || apitokens.TokenIDFromContext(c.Context()) != 91 {
			return identity.Actor{}, errors.New("bearer authority was not available to actor loader")
		}
		return identity.Actor{
			ID: 42, Status: identity.UserStatusActive,
			Permissions: map[string]bool{"arbitrary.demo.manage": true},
		}, nil
	}
	harness.app = newArbitraryRouteApp(harness, auth)

	request := httptest.NewRequest(stdhttp.MethodPost, "/admin/plugin/rebuild", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer sft_valid")
	response, err := harness.app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusOK || auth.calls.Load() != 1 || harness.routeCalls.Load() != 1 {
		t.Fatalf("status=%d bearer=%d runtime=%d", response.StatusCode, auth.calls.Load(), harness.routeCalls.Load())
	}
}

func TestNewAppInternalRouteProbeNeverExecutesPluginOrLoadsActor(t *testing.T) {
	harness := newArbitraryRouteHarness(t, false, true)

	matched := httptest.NewRequest(stdhttp.MethodHead, "/plugin/docs/intro?preview=1", nil)
	matched.Header.Set(internalRouteProbeHeader, internalRouteProbeVersion)
	matched.Header.Set(internalRouteProbeMethodHeader, stdhttp.MethodGet)
	matchedResponse, err := harness.app.Test(matched)
	if err != nil {
		t.Fatal(err)
	}
	matchedResponse.Body.Close()
	if matchedResponse.StatusCode != stdhttp.StatusNoContent ||
		matchedResponse.Header.Get(internalRouteProbeResultHeader) != internalRouteProbeMatch {
		t.Fatalf("matched probe status=%d headers=%#v", matchedResponse.StatusCode, matchedResponse.Header)
	}

	miss := httptest.NewRequest(stdhttp.MethodHead, "/admin/host-owned-page", nil)
	miss.Header.Set(internalRouteProbeHeader, internalRouteProbeVersion)
	miss.Header.Set(internalRouteProbeMethodHeader, stdhttp.MethodPost)
	missResponse, err := harness.app.Test(miss)
	if err != nil {
		t.Fatal(err)
	}
	missResponse.Body.Close()
	if missResponse.StatusCode != stdhttp.StatusNotFound ||
		missResponse.Header.Get(internalRouteProbeResultHeader) != internalRouteProbeMiss ||
		harness.actorCalls.Load() != 0 || harness.routeCalls.Load() != 0 {
		t.Fatalf("miss probe status=%d actors=%d runtime=%d", missResponse.StatusCode, harness.actorCalls.Load(), harness.routeCalls.Load())
	}
}

func TestNewAppSafeModeBypassesArbitraryPluginRoutes(t *testing.T) {
	harness := newArbitraryRouteHarness(t, true, true)

	request := httptest.NewRequest(stdhttp.MethodGet, "/plugin/docs/intro", nil)
	response, err := harness.app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusNotFound || harness.actorCalls.Load() != 0 || harness.routeCalls.Load() != 0 {
		t.Fatalf("status=%d actors=%d runtime=%d", response.StatusCode, harness.actorCalls.Load(), harness.routeCalls.Load())
	}

	probe := httptest.NewRequest(stdhttp.MethodHead, "/plugin/docs/intro", nil)
	probe.Header.Set(internalRouteProbeHeader, internalRouteProbeVersion)
	probe.Header.Set(internalRouteProbeMethodHeader, stdhttp.MethodGet)
	probeResponse, err := harness.app.Test(probe)
	if err != nil {
		t.Fatal(err)
	}
	probeResponse.Body.Close()
	if probeResponse.StatusCode != stdhttp.StatusNotFound ||
		probeResponse.Header.Get(internalRouteProbeResultHeader) != internalRouteProbeMiss {
		t.Fatalf("safe-mode probe status=%d headers=%#v", probeResponse.StatusCode, probeResponse.Header)
	}
}

type arbitraryRouteHarness struct {
	app           *fiber.App
	resolver      routes.PlanResolver
	dispatcher    *routes.Dispatcher
	permission    bool
	csrf          bool
	routeCalls    atomic.Int64
	actorCalls    atomic.Int64
	localeMu      sync.Mutex
	localeValue   string
	actorOverride RouteActorLoader
}

func newArbitraryRouteHarness(t *testing.T, safeMode, csrfEnabled bool) *arbitraryRouteHarness {
	t.Helper()
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("arbitrary.demo", 'c')
	publicRoute := routeDispatcherManifestRoute("arbitrary.demo.public", extensionmanifest.RouteActionAdd, "/plugin/docs/:slug", stdhttp.MethodGet)
	adminRoute := routeDispatcherManifestRoute("arbitrary.demo.admin", extensionmanifest.RouteActionAdd, "/admin/plugin/rebuild", stdhttp.MethodPost)
	adminRoute.Guard = extensionmanifest.GuardCorePermission
	adminRoute.Permission = "arbitrary.demo.manage"
	adminRoute.RequestSchema = "arbitrary.demo.admin.request@1"
	apiRoute := routeDispatcherManifestRoute("arbitrary.demo.api", extensionmanifest.RouteActionAdd, "/api/v1/plugin/report", stdhttp.MethodGet)
	aliasTarget := routeMappingCoreRoute("core.route.arbitrary.alias_target", stdhttp.MethodGet, "/api/v1/plugin/core-target/:slug")
	aliasRoute := routeMappingPluginRoute("arbitrary.demo.alias", extensionmanifest.RouteActionAlias, aliasTarget.ID, "/plugin/legacy/:slug", stdhttp.MethodGet)
	if _, err := registry.Publish(routes.Publication{
		SafeMode: safeMode,
		Core:     []routes.CoreRoute{aliasTarget},
		Plugins: []routes.PluginRouteSet{{
			Artifact: artifact,
			Routes:   []extensionmanifest.ManifestRoute{publicRoute, adminRoute, apiRoute, aliasRoute},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	runtime, target := newRouteDispatcherRuntime(t, artifact)
	harness := &arbitraryRouteHarness{csrf: csrfEnabled}
	target.Config.Handler = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
		harness.routeCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(request.Header.Get("X-SForum-Route-ID")))
	})
	harness.resolver = routeRegistryPlanResolver{registry: registry}
	harness.dispatcher = routes.NewDispatcher(routes.DispatcherConfig{
		Plans: harness.resolver, Steps: NewBufferedRouteStepInvoker(runtime), Guard: NewProductionRouteGuardAuthorizer(),
		Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	harness.app = newArbitraryRouteApp(harness, nil)
	return harness
}

func newArbitraryRouteApp(harness *arbitraryRouteHarness, bearer BearerAuthenticator) *fiber.App {
	cfg := routeDispatcherConfig()
	cfg.SupportedLocales = []string{"zh-CN", "en-US"}
	cfg.CSRFEnabled = harness.csrf
	cfg.CSRFTrustedOrigins = []string{"https://forum.example.com"}
	return NewApp(cfg, slog.Default(), Dependencies{
		RoutePlans: harness.resolver, RouteDispatcher: harness.dispatcher, BearerTokens: bearer,
		RouteProviders: []RouteProvider{arbitraryRouteProviderFunc(func(api fiber.Router) {
			api.Get("/plugin/core-target/:slug", func(c fiber.Ctx) error {
				return c.SendString("alias:" + c.Params("slug"))
			})
		})},
		RouteActors: func(c fiber.Ctx) (identity.Actor, error) {
			harness.actorCalls.Add(1)
			harness.localeMu.Lock()
			harness.localeValue = Locale(c)
			harness.localeMu.Unlock()
			if harness.actorOverride != nil {
				return harness.actorOverride(c)
			}
			actor := identity.Actor{ID: 42, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
			if harness.permission {
				actor.Permissions["arbitrary.demo.manage"] = true
			}
			return actor, nil
		},
	})
}

func (h *arbitraryRouteHarness) lastLocale() string {
	h.localeMu.Lock()
	defer h.localeMu.Unlock()
	return h.localeValue
}

func arbitraryRouteCSRFToken(t *testing.T, response *stdhttp.Response) string {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == "csrf_" && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatal("csrf_ cookie was not minted")
	return ""
}

func arbitraryRouteUnsafeRequest(token string) *stdhttp.Request {
	request := httptest.NewRequest(stdhttp.MethodPost, "/admin/plugin/rebuild", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://forum.example.com")
	request.Header.Set("X-Csrf-Token", token)
	request.AddCookie(&stdhttp.Cookie{Name: "csrf_", Value: token})
	return request
}

type arbitraryRouteRejectingBearer struct{ calls atomic.Int64 }

func (a *arbitraryRouteRejectingBearer) AuthenticatePlaintext(fiber.Ctx, string) (apitokens.Authenticated, error) {
	a.calls.Add(1)
	return apitokens.Authenticated{}, errors.New("unexpected bearer authentication")
}

type arbitraryRouteAcceptingBearer struct{ calls atomic.Int64 }

func (a *arbitraryRouteAcceptingBearer) AuthenticatePlaintext(_ fiber.Ctx, plaintext string) (apitokens.Authenticated, error) {
	a.calls.Add(1)
	if plaintext != "sft_valid" {
		return apitokens.Authenticated{}, errors.New("unexpected bearer token")
	}
	return apitokens.Authenticated{UserID: 42, TokenID: 91, Scopes: []string{"arbitrary.demo.manage"}}, nil
}

type arbitraryRouteProviderFunc func(fiber.Router)

func (f arbitraryRouteProviderFunc) RegisterRoutes(api fiber.Router) { f(api) }

var _ routes.PlanResolver = routeRegistryPlanResolver{}
