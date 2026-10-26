package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const p6RouteMatrixPermission = "topic.create"

// p6RouteMatrixRawGuard 是包内显式声明的 kind=raw_request 守卫（entry + digest +
// contractVersion 齐备）。R1 之后 raw 权限只能来自这种声明，不能再来自 Host 拥有的
// core.guard.raw_request。
const p6RouteMatrixRawGuard = "p6.matrix.raw_request"

func TestP6RoutePermissionCSRFLocaleQueryAndBodyMatrix(t *testing.T) {
	harness := newP6RouteRequestHarness(t, extensionmanifest.GuardCorePermission, "", true)

	// CSRF 位于 Route Dispatcher 之前；缺 token 时不得解析 actor、schema 或调用插件。
	harness.actor = p6RouteMatrixActor(true)
	status, _ := p6RouteMatrixDo(t, harness.app, p6RouteMatrixRequest(`{"title":"hello"}`, "view=full", ""))
	if status != stdhttp.StatusForbidden || harness.runtime.routeCalls != 0 || harness.schemas.requestCalls != 0 {
		t.Fatalf("missing CSRF status=%d runtime=%d schemas=%d", status, harness.runtime.routeCalls, harness.schemas.requestCalls)
	}

	// 有效 CSRF 不能代替 Host permission；拒绝发生在 schema/runtime 之前。
	harness.actor = p6RouteMatrixActor(false)
	token := harness.csrfToken(t)
	status, _ = p6RouteMatrixDo(t, harness.app, p6RouteMatrixRequest(`{"title":"hello"}`, "view=full", token))
	if status != stdhttp.StatusForbidden || harness.runtime.routeCalls != 0 || harness.schemas.requestCalls != 0 {
		t.Fatalf("permission denial status=%d runtime=%d schemas=%d", status, harness.runtime.routeCalls, harness.schemas.requestCalls)
	}

	// Host schema validation owns the body contract and rejects before Protocol V2 execution。
	harness.actor = p6RouteMatrixActor(true)
	token = harness.csrfToken(t)
	status, _ = p6RouteMatrixDo(t, harness.app, p6RouteMatrixRequest(`{"title":42}`, "view=full", token))
	if status != stdhttp.StatusUnprocessableEntity || harness.runtime.routeCalls != 0 ||
		harness.schemas.requestCalls != 1 || harness.schemas.responseCalls != 0 {
		t.Fatalf("schema denial status=%d runtime=%d schemas=%d/%d", status, harness.runtime.routeCalls,
			harness.schemas.requestCalls, harness.schemas.responseCalls)
	}

	// 完整允许路径穿过 Fiber、Registry、Dispatcher 和 Protocol V2，并保留 locale path/query/body。
	token = harness.csrfToken(t)
	status, body := p6RouteMatrixDo(t, harness.app, p6RouteMatrixRequest(`{"title":"hello"}`, "view=full", token))
	if status != stdhttp.StatusCreated || body != `{"ok":true}` || harness.runtime.routeCalls != 1 ||
		harness.schemas.requestCalls != 2 || harness.schemas.responseCalls != 2 {
		t.Fatalf("allowed status=%d body=%q runtime=%d schemas=%d/%d", status, body, harness.runtime.routeCalls,
			harness.schemas.requestCalls, harness.schemas.responseCalls)
	}
	assertP6RouteMatrixRuntimeRequest(t, harness.runtime.routeRequest, extensionsruntime.ProtocolV2RequestAuthority{
		Mode: extensionsruntime.ProtocolV2RequestAuthorityFiltered, GuardKind: extensionsruntime.ProtocolV2RequestGuardHost,
	})
}

func TestP6RouteCustomGuardOwnsPolicyAndFailsClosed(t *testing.T) {
	harness := newP6RouteRequestHarness(t, "p6.matrix.owner", "custom", false)
	harness.actor = p6RouteMatrixActor(true)

	status, _ := p6RouteMatrixDo(t, harness.app, p6RouteMatrixRequest(`{"title":"hello"}`, "view=full", "opaque-csrf"))
	if status != stdhttp.StatusCreated || harness.runtime.guardCalls != 1 || harness.runtime.routeCalls != 1 ||
		!reflect.DeepEqual(harness.runtime.events, []string{"guard", "route"}) {
		t.Fatalf("custom allow status=%d guard=%d route=%d events=%#v", status,
			harness.runtime.guardCalls, harness.runtime.routeCalls, harness.runtime.events)
	}
	assertP6RouteMatrixGuardRequest(t, harness.runtime.guardRequest)
	assertP6RouteMatrixRuntimeRequest(t, harness.runtime.routeRequest, extensionsruntime.ProtocolV2RequestAuthority{
		Mode: extensionsruntime.ProtocolV2RequestAuthorityFiltered, GuardKind: extensionsruntime.ProtocolV2RequestGuardCustom,
	})

	status, _ = p6RouteMatrixDo(t, harness.app, p6RouteMatrixRequest(`{"title":"hello"}`, "deny=1", "opaque-csrf"))
	if status != stdhttp.StatusForbidden || harness.runtime.guardCalls != 2 || harness.runtime.routeCalls != 1 {
		t.Fatalf("custom deny status=%d guard=%d route=%d", status, harness.runtime.guardCalls, harness.runtime.routeCalls)
	}

	harness.runtime.guardErr = errors.New("guard runtime crashed")
	status, _ = p6RouteMatrixDo(t, harness.app, p6RouteMatrixRequest(`{"title":"hello"}`, "view=full", "opaque-csrf"))
	if status != stdhttp.StatusBadGateway || harness.runtime.guardCalls != 3 || harness.runtime.routeCalls != 1 {
		t.Fatalf("custom crash status=%d guard=%d route=%d", status, harness.runtime.guardCalls, harness.runtime.routeCalls)
	}
}

func TestP6RouteRawRequestDeclarationRequiresExactTrustAndForwardsCredentials(t *testing.T) {
	// 决定 R1（2026-10-16）：raw 权限只能来自包内显式声明的 kind=raw_request 守卫。
	// 本测试原先用 Host 拥有的 core.guard.raw_request 作为载体；该守卫已被拒绝，
	// 因此改用声明的守卫，保持「声明的 raw_request 守卫获得 raw 权限并转发浏览器
	// 凭证」这一侧覆盖（拒绝侧见 TestRouteRetiredCoreRawRequestGuardIsRejectedAtPublication）。
	harness := newP6RouteRequestHarness(t, p6RouteMatrixRawGuard, "raw_request", false)
	harness.actor = p6RouteMatrixActor(true)

	status, _ := p6RouteMatrixDo(t, harness.app, p6RouteMatrixRequest(`{"title":"hello"}`, "view=full", "opaque-csrf"))
	if status != stdhttp.StatusCreated || harness.runtime.guardCalls != 1 || harness.runtime.routeCalls != 1 ||
		!reflect.DeepEqual(harness.runtime.events, []string{"guard", "route"}) {
		t.Fatalf("raw allow status=%d guard=%d route=%d events=%#v", status,
			harness.runtime.guardCalls, harness.runtime.routeCalls, harness.runtime.events)
	}
	// 声明的 raw_request 守卫自身以 raw 权限被调用，并且同样收到浏览器凭证。
	if harness.runtime.guardRequest.GuardID != p6RouteMatrixRawGuard ||
		harness.runtime.guardRequest.Authority != (extensionsruntime.ProtocolV2RequestAuthority{
			Mode: extensionsruntime.ProtocolV2RequestAuthorityRaw, GuardKind: extensionsruntime.ProtocolV2RequestGuardRawRequest,
		}) {
		t.Fatalf("raw guard request = %#v", harness.runtime.guardRequest)
	}
	assertP6RouteMatrixHeaders(t, harness.runtime.guardRequest.Headers, true)
	assertP6RouteMatrixRuntimeRequest(t, harness.runtime.routeRequest, extensionsruntime.ProtocolV2RequestAuthority{
		Mode: extensionsruntime.ProtocolV2RequestAuthorityRaw, GuardKind: extensionsruntime.ProtocolV2RequestGuardRawRequest,
	})

	// raw_request 是单独声明的高风险权限，只对 exact trusted artifact 转发浏览器凭据。
	harness.policy.lookup.Entry.CurrentArtifactTrusted = false
	status, _ = p6RouteMatrixDo(t, harness.app, p6RouteMatrixRequest(`{"title":"hello"}`, "view=full", "opaque-csrf"))
	if status != stdhttp.StatusBadGateway || harness.runtime.guardCalls != 1 || harness.runtime.routeCalls != 1 {
		t.Fatalf("raw trust drift status=%d guard=%d route=%d", status,
			harness.runtime.guardCalls, harness.runtime.routeCalls)
	}
}

type p6RouteRequestHarness struct {
	app     *fiber.App
	actor   identity.Actor
	runtime *p6RouteMatrixRuntime
	policy  *testExtensionGuardPolicy
	schemas *p6RouteMatrixSchemaCatalog
}

func newP6RouteRequestHarness(t *testing.T, guard, guardKind string, csrfEnabled bool) *p6RouteRequestHarness {
	t.Helper()
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("p6.matrix", 'a')
	route := routeDispatcherManifestRoute(
		"p6.matrix.route", extensionmanifest.RouteActionAdd, "/api/v1/:locale/p6/:id", stdhttp.MethodPost,
	)
	route.Guard = guard
	route.RequestSchema = "p6.matrix.request@1"
	if guard == extensionmanifest.GuardCorePermission {
		route.Permission = p6RouteMatrixPermission
	}
	var guards []extensionmanifest.ManifestGuard
	if guardKind != "" {
		guards = []extensionmanifest.ManifestGuard{{
			ID: guard, ContractVersion: guard + "@1", Kind: guardKind,
			Entry: "backend/guard", Digest: strings.Repeat("b", 64), Permissions: []string{p6RouteMatrixPermission},
		}}
	}
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}, Guards: guards,
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(stdhttp.MethodPost, "/api/v1/zh-CN/p6/41")
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Terminal()
	runtime := newP6RouteMatrixRuntime(t, step)
	schemas := &p6RouteMatrixSchemaCatalog{}
	harness := &p6RouteRequestHarness{runtime: runtime, schemas: schemas}

	policies := ProductionRouteGuardPolicies{}
	// R1 之后 core.guard.raw_request 既不能发布也不再有守卫绑定；raw 载体只剩
	// 包内显式声明的 kind=raw_request 守卫，因此只按声明装配插件守卫策略。
	if guardKind != "" {
		harness.policy = &testExtensionGuardPolicy{lookup: exactPluginGuardLookup(step), ok: true}
		policies.PluginGuards = NewRuntimePluginRouteGuardEvaluator(runtime, harness.policy)
	}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard:   NewProductionRouteGuardAuthorizerWithPolicies(policies),
		Schemas: CatalogRouteSchemaValidator{Catalog: schemas},
	})
	cfg := routeDispatcherConfig()
	cfg.CSRFEnabled = csrfEnabled
	cfg.CSRFTrustedOrigins = []string{"https://forum.example.com"}
	harness.app = NewApp(cfg, slog.Default(), Dependencies{
		RouteDispatcher: dispatcher,
		RouteActors: func(fiber.Ctx) (identity.Actor, error) {
			return harness.actor, nil
		},
		RouteProviders: []RouteProvider{p6RouteMatrixTokenProvider{}},
	})
	return harness
}

func (h *p6RouteRequestHarness) csrfToken(t *testing.T) string {
	t.Helper()
	response, err := h.app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/p6-token", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	for _, cookie := range response.Cookies() {
		if cookie.Name == "csrf_" && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatal("CSRF middleware did not mint a token")
	return ""
}

func p6RouteMatrixActor(allowed bool) identity.Actor {
	permissions := map[string]bool{}
	if allowed {
		permissions[p6RouteMatrixPermission] = true
	}
	return identity.Actor{ID: 42, Status: identity.UserStatusActive, Permissions: permissions}
}

func p6RouteMatrixRequest(body, query, csrfToken string) *stdhttp.Request {
	target := "/api/v1/zh-CN/p6/41"
	if query != "" {
		target += "?" + query
	}
	request := httptest.NewRequest(stdhttp.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept-Language", "zh-CN")
	request.Header.Set("Authorization", "Bearer browser-secret")
	request.Header.Set("X-API-Key", "api-key-secret")
	request.Header.Set("X-Auth-Token", "auth-token-secret")
	request.Header.Set("X-Trace-ID", "trace-p6")
	request.Header.Set("X-SForum-Forged", "forged")
	request.Header.Set("Origin", "https://forum.example.com")
	request.AddCookie(&stdhttp.Cookie{Name: "session", Value: "browser-secret"})
	if csrfToken != "" {
		request.Header.Set("X-Csrf-Token", csrfToken)
		request.AddCookie(&stdhttp.Cookie{Name: "csrf_", Value: csrfToken})
	}
	return request
}

func p6RouteMatrixDo(t *testing.T, app *fiber.App, request *stdhttp.Request) (int, string) {
	t.Helper()
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}

func assertP6RouteMatrixRuntimeRequest(
	t *testing.T,
	request extensionsruntime.ProtocolV2RouteRequest,
	authority extensionsruntime.ProtocolV2RequestAuthority,
) {
	t.Helper()
	if request.RouteID != "p6.matrix.route" || request.ContractVersion != "p6.matrix.route@1" ||
		request.Method != stdhttp.MethodPost || request.Path != "/api/v1/zh-CN/p6/41" ||
		request.PathParameters["locale"] != "zh-CN" || request.PathParameters["id"] != "41" ||
		request.QueryParameters["view"] != "full" || request.Body["title"] != "hello" ||
		request.Authority != authority || request.Actor == nil || request.Actor.UserID != 42 ||
		!reflect.DeepEqual(request.Actor.PermissionKeys, []string{p6RouteMatrixPermission}) {
		t.Fatalf("runtime request=%#v", request)
	}
	assertP6RouteMatrixHeaders(t, request.Headers, authority.Mode == extensionsruntime.ProtocolV2RequestAuthorityRaw)
}

func assertP6RouteMatrixGuardRequest(t *testing.T, request extensionsruntime.ProtocolV2GuardRequest) {
	t.Helper()
	if request.GuardID != "p6.matrix.owner" || request.RouteID != "p6.matrix.route" ||
		request.PathParameters["locale"] != "zh-CN" || request.PathParameters["id"] != "41" ||
		request.QueryParameters["view"] != "full" || request.Body["title"] != "hello" ||
		request.Authority != (extensionsruntime.ProtocolV2RequestAuthority{
			Mode: extensionsruntime.ProtocolV2RequestAuthorityFiltered, GuardKind: extensionsruntime.ProtocolV2RequestGuardCustom,
		}) ||
		request.Actor == nil || request.Actor.UserID != 42 {
		t.Fatalf("guard request=%#v", request)
	}
	assertP6RouteMatrixHeaders(t, request.Headers, false)
}

func assertP6RouteMatrixHeaders(t *testing.T, headers stdhttp.Header, raw bool) {
	t.Helper()
	if headers.Get("X-Trace-ID") != "trace-p6" || headers.Get("Accept-Language") != "zh-CN" ||
		headers.Get("X-Csrf-Token") != "" || headers.Get("X-SForum-Forged") != "" {
		t.Fatalf("projected headers=%#v", headers)
	}
	if raw {
		if headers.Get("Authorization") != "Bearer browser-secret" ||
			!strings.Contains(headers.Get("Cookie"), "session=browser-secret") ||
			headers.Get("X-API-Key") != "api-key-secret" || headers.Get("X-Auth-Token") != "auth-token-secret" {
			t.Fatalf("raw credentials were not forwarded: %#v", headers)
		}
	} else {
		for _, name := range []string{"Cookie", "Authorization", "X-API-Key", "X-Auth-Token"} {
			if values := headers.Values(name); len(values) != 0 {
				t.Fatalf("filtered credential %s survived: %#v", name, values)
			}
		}
	}
}

type p6RouteMatrixRuntime struct {
	*testPluginGuardRuntime
	guardRequest extensionsruntime.ProtocolV2GuardRequest
	routeRequest extensionsruntime.ProtocolV2RouteRequest
	guardErr     error
	guardCalls   int
	routeCalls   int
	events       []string
}

func newP6RouteMatrixRuntime(t *testing.T, step routes.RouteExecutionStep) *p6RouteMatrixRuntime {
	t.Helper()
	return &p6RouteMatrixRuntime{testPluginGuardRuntime: newTestPluginGuardRuntime(t, step)}
}

func (r *p6RouteMatrixRuntime) InvokeGuardInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2GuardRequest,
) error {
	if identity != r.snapshot.Identity {
		return extensionsruntime.ErrRuntimeInstanceNotFound
	}
	r.guardCalls++
	r.guardRequest = request
	r.events = append(r.events, "guard")
	if r.guardErr != nil {
		return r.guardErr
	}
	if request.QueryParameters["deny"] == "1" {
		return extensionsruntime.ErrProtocolV2GuardDenied
	}
	return nil
}

func (r *p6RouteMatrixRuntime) InvokeRouteInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if identity != r.snapshot.Identity {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	r.routeCalls++
	r.routeRequest = request
	r.events = append(r.events, "route")
	return extensionsruntime.ProtocolV2RouteResponse{
		StatusCode: stdhttp.StatusCreated, Headers: stdhttp.Header{"Content-Type": {"application/json"}},
		Body: map[string]any{"ok": true}, BodyPresent: true,
	}, nil
}

type p6RouteMatrixSchemaCatalog struct {
	requestCalls  int
	responseCalls int
}

func (c *p6RouteMatrixSchemaCatalog) ValidateRouteSchema(
	_ context.Context,
	artifact routes.PluginArtifact,
	direction string,
	routeID string,
	method string,
	actualMethod string,
	contractVersion string,
	action string,
	reference string,
	mediaType string,
	responseStatus int,
	payload []byte,
) error {
	if artifact.ExtensionID != "p6.matrix" || routeID != "p6.matrix.route" || method != stdhttp.MethodPost ||
		actualMethod != stdhttp.MethodPost || contractVersion != "p6.matrix.route@1" ||
		action != extensionmanifest.RouteActionAdd || mediaType != "application/json" {
		return fmt.Errorf("matrix schema identity drifted")
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return err
	}
	switch direction {
	case "request":
		c.requestCalls++
		if reference != "p6.matrix.request@1" || responseStatus != 0 || value["title"] != "hello" {
			return fmt.Errorf("matrix request schema rejected payload")
		}
	case "response":
		c.responseCalls++
		if reference != "p6.matrix.route.response@1" || responseStatus != stdhttp.StatusCreated || value["ok"] != true {
			return fmt.Errorf("matrix response schema rejected payload")
		}
	default:
		return fmt.Errorf("matrix schema direction %q", direction)
	}
	return nil
}

type p6RouteMatrixTokenProvider struct{}

func (p6RouteMatrixTokenProvider) RegisterRoutes(api fiber.Router) {
	api.Get("/p6-token", func(c fiber.Ctx) error { return c.SendStatus(stdhttp.StatusNoContent) })
}

var (
	_ ExactPluginGuardRuntime = (*p6RouteMatrixRuntime)(nil)
	_ ExactRouteRuntime       = (*p6RouteMatrixRuntime)(nil)
	_ exactRouteV2Runtime     = (*p6RouteMatrixRuntime)(nil)
)
