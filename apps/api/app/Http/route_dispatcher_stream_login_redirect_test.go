package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const (
	streamLoginRedirectConsentPath = "/api/oauth/consent"
	streamLoginRedirectWantTarget  = "/login?redirect=%2Fapi%2Foauth%2Fconsent%3Frequest%3Dsfoq_test"
)

// TestStreamLoginRedirectSendsBrowserToLogin 覆盖 §7 第 1 条：浏览器文档导航
// 命中登录守卫时跳转，目标只由当前请求路径 + 查询串拼成。
func TestStreamLoginRedirectSendsBrowserToLogin(t *testing.T) {
	app, _ := newStreamLoginRedirectFixture(t, []extensionmanifest.ManifestRoute{
		streamLoginRedirectDeclaration(extensionmanifest.RouteModeStream, extensionmanifest.GuardCoreLogin, "GET"),
	}, &streamHTTPTestInvoker{}, nil)

	response, err := app.Test(streamLoginRedirectRequest(stdhttp.MethodGet, streamLoginRedirectConsentPath+"?request=sfoq_test", "text/html"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusFound {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}
	if location := response.Header.Get(fiber.HeaderLocation); location != streamLoginRedirectWantTarget {
		t.Fatalf("location=%q want=%q", location, streamLoginRedirectWantTarget)
	}
	if cache := response.Header.Get(fiber.HeaderCacheControl); cache != "no-store" {
		t.Fatalf("cache-control=%q", cache)
	}
	// 跳转分支不写任何 body（任务书 §7 的 preflight 回归项并入此处）。
	if len(body) != 0 {
		t.Fatalf("redirect body=%q", body)
	}
}

// TestStreamLoginRedirectKeepsJSONEnvelopeForAPIClients 覆盖 §7 第 2 条。
func TestStreamLoginRedirectKeepsJSONEnvelopeForAPIClients(t *testing.T) {
	app, _ := newStreamLoginRedirectFixture(t, []extensionmanifest.ManifestRoute{
		streamLoginRedirectDeclaration(extensionmanifest.RouteModeStream, extensionmanifest.GuardCoreLogin, "GET"),
	}, &streamHTTPTestInvoker{}, nil)

	response, err := app.Test(streamLoginRedirectRequest(stdhttp.MethodGet, streamLoginRedirectConsentPath, "application/json"))
	if err != nil {
		t.Fatal(err)
	}
	assertStreamLoginJSONError(t, response, fiber.StatusUnauthorized, "auth.required")
}

// TestStreamLoginRedirectRequiresHTMLAccept 覆盖 §7 第 3 条。
func TestStreamLoginRedirectRequiresHTMLAccept(t *testing.T) {
	app, _ := newStreamLoginRedirectFixture(t, []extensionmanifest.ManifestRoute{
		streamLoginRedirectDeclaration(extensionmanifest.RouteModeStream, extensionmanifest.GuardCoreLogin, "GET"),
	}, &streamHTTPTestInvoker{}, nil)

	response, err := app.Test(streamLoginRedirectRequest(stdhttp.MethodGet, streamLoginRedirectConsentPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	assertStreamLoginJSONError(t, response, fiber.StatusUnauthorized, "auth.required")
}

// TestStreamLoginRedirectIgnoresSSEMode 覆盖 §7 第 4 条：SSE 客户端永远拿到 JSON。
func TestStreamLoginRedirectIgnoresSSEMode(t *testing.T) {
	for _, accept := range []string{"text/event-stream", "text/html"} {
		t.Run(accept, func(t *testing.T) {
			app, _ := newStreamLoginRedirectFixture(t, []extensionmanifest.ManifestRoute{
				streamLoginRedirectDeclaration(extensionmanifest.RouteModeSSE, extensionmanifest.GuardCoreLogin, "GET"),
			}, &streamHTTPTestInvoker{}, nil)

			response, err := app.Test(streamLoginRedirectRequest(stdhttp.MethodGet, streamLoginRedirectConsentPath, accept))
			if err != nil {
				t.Fatal(err)
			}
			assertStreamLoginJSONError(t, response, fiber.StatusUnauthorized, "auth.required")
		})
	}
}

// TestStreamLoginRedirectIgnoresUnsafeMethods 覆盖 §7 第 5 条：写请求不能被翻译成
// GET 登录页，否则会静默丢掉请求体语义。
func TestStreamLoginRedirectIgnoresUnsafeMethods(t *testing.T) {
	app, _ := newStreamLoginRedirectFixture(t, []extensionmanifest.ManifestRoute{
		streamLoginRedirectDeclaration(extensionmanifest.RouteModeStream, extensionmanifest.GuardCoreLogin, "GET", "POST"),
	}, &streamHTTPTestInvoker{}, nil)

	response, err := app.Test(streamLoginRedirectRequest(stdhttp.MethodPost, streamLoginRedirectConsentPath, "text/html"))
	if err != nil {
		t.Fatal(err)
	}
	assertStreamLoginJSONError(t, response, fiber.StatusUnauthorized, "auth.required")
}

// TestStreamLoginRedirectSupportsHEAD 覆盖 HEAD 触发条件（stream 路由必须显式声明 HEAD，
// 隐式 HEAD 只对 http 模式生效，见 Support/Routes/prepare.go routeMethodMatches）。
func TestStreamLoginRedirectSupportsHEAD(t *testing.T) {
	app, _ := newStreamLoginRedirectFixture(t, []extensionmanifest.ManifestRoute{
		streamLoginRedirectDeclaration(extensionmanifest.RouteModeStream, extensionmanifest.GuardCoreLogin, "HEAD"),
	}, &streamHTTPTestInvoker{}, nil)

	response, err := app.Test(streamLoginRedirectRequest(stdhttp.MethodHead, streamLoginRedirectConsentPath, "text/html"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusFound {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if location := response.Header.Get(fiber.HeaderLocation); location != "/login?redirect=%2Fapi%2Foauth%2Fconsent" {
		t.Fatalf("location=%q", location)
	}
	if len(body) != 0 {
		t.Fatalf("redirect body=%q", body)
	}
}

// TestStreamLoginRedirectFallsBackToPlainLogin 覆盖 §7 第 6 条：非法回跳目标退化为
// Location: /login（不带 redirect）。真实 HTTP 请求里 // 开头的路径会在
// Support/Routes.normalizeRequestPath 阶段被拒绝，到不了 Open()，所以这里直接验证
// 适配器函数与合成 ctx。
func TestStreamLoginRedirectFallsBackToPlainLogin(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
		want   bool
	}{
		{name: "site absolute", target: "/api/oauth/consent?request=x", want: true},
		{name: "root", target: "/", want: true},
		{name: "protocol relative", target: "//evil.example/x", want: false},
		{name: "backslash", target: `/\\evil.example/x`, want: false},
		{name: "crlf injection", target: "/login\r\nSet-Cookie: a=b", want: false},
		{name: "relative", target: "evil.example/x", want: false},
		{name: "empty", target: "", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := safeLoginReturnPath(test.target); got != test.want {
				t.Fatalf("safeLoginReturnPath(%q)=%t want=%t", test.target, got, test.want)
			}
		})
	}

	_, dispatcher := newStreamLoginRedirectFixture(t, []extensionmanifest.ManifestRoute{
		streamLoginRedirectDeclaration(extensionmanifest.RouteModeStream, extensionmanifest.GuardCoreLogin, "GET"),
	}, &streamHTTPTestInvoker{}, nil)
	prepared, err := dispatcher.PrepareStream(context.Background(), routes.DispatchRequest{
		Method: stdhttp.MethodGet, Path: streamLoginRedirectConsentPath,
	})
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}

	app := fiber.New()
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(fiber.MethodGet)
	fctx.Request.Header.Set(fiber.HeaderAccept, "text/html")
	fctx.Request.URI().SetPath("//evil.example/x")
	c := app.AcquireCtx(fctx)
	defer app.ReleaseCtx(c)
	if got := c.Path(); got != "//evil.example/x" {
		t.Fatalf("synthetic ctx path=%q", got)
	}
	location, ok := streamLoginRedirect(c, prepared.Dispatch, ErrRouteLoginRequired)
	if !ok || location != "/login" {
		t.Fatalf("location=%q ok=%t", location, ok)
	}
}

// TestStreamLoginRedirectPassesAuthenticatedBrowsersThrough 覆盖 §7 第 7 条。
func TestStreamLoginRedirectPassesAuthenticatedBrowsersThrough(t *testing.T) {
	session := &streamHTTPTestSession{
		chunks:   []routes.RouteStreamChunk{{Sequence: 1, Data: []byte("consent-page"), Final: true}},
		response: routes.DispatchResponse{Status: stdhttp.StatusOK},
	}
	app, _ := newStreamLoginRedirectFixture(t, []extensionmanifest.ManifestRoute{
		streamLoginRedirectDeclaration(extensionmanifest.RouteModeStream, extensionmanifest.GuardCoreLogin, "GET"),
	}, &streamHTTPTestInvoker{start: routes.RouteStreamStart{
		Response: routes.DispatchResponse{Status: stdhttp.StatusOK}, Session: session,
	}}, func(fiber.Ctx) (identity.Actor, error) {
		return identity.Actor{ID: 42, Status: identity.UserStatusActive}, nil
	})

	response, err := app.Test(streamLoginRedirectRequest(stdhttp.MethodGet, streamLoginRedirectConsentPath, "text/html"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != stdhttp.StatusOK || string(body) != "consent-page" {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}
	if location := response.Header.Get(fiber.HeaderLocation); location != "" {
		t.Fatalf("authenticated stream was redirected to %q", location)
	}
}

// TestStreamLoginRedirectIgnoresNonGuardFailures 覆盖 §7 第 8 条：非守卫错误沿用
// 原有映射，不产生跳转。
func TestStreamLoginRedirectIgnoresNonGuardFailures(t *testing.T) {
	app, _ := newStreamLoginRedirectFixture(t, []extensionmanifest.ManifestRoute{
		streamLoginRedirectDeclaration(extensionmanifest.RouteModeStream, extensionmanifest.GuardCorePublic, "GET"),
	}, &streamLoginRedirectErrorInvoker{err: errors.New("runtime crashed during open")}, nil)

	response, err := app.Test(streamLoginRedirectRequest(stdhttp.MethodGet, streamLoginRedirectConsentPath, "text/html"))
	if err != nil {
		t.Fatal(err)
	}
	assertStreamLoginJSONError(t, response, fiber.StatusBadGateway, "extensions.route_unavailable")
}

func newStreamLoginRedirectFixture(
	t *testing.T,
	declarations []extensionmanifest.ManifestRoute,
	steps routes.StepInvoker,
	actors RouteActorLoader,
) (*fiber.App, *routes.Dispatcher) {
	t.Helper()
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.login.redirect", 'a')
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: declarations,
	}}}); err != nil {
		t.Fatal(err)
	}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: steps, Guard: HostRouteGuardAuthorizer{},
	})
	app := fiber.New(fiber.Config{StreamRequestBody: true, ErrorHandler: errorHandler(slog.Default())})
	app.Use(routeDispatcherMiddleware(dispatcher, actors))
	return app, dispatcher
}

// streamLoginRedirectDeclaration 复用 routeDispatcherManifestRoute 的形状，只替换
// 本需求关心的模式、守卫与方法集合。
func streamLoginRedirectDeclaration(mode, guard string, methods ...string) extensionmanifest.ManifestRoute {
	declaration := routeDispatcherManifestRoute(
		"stream.login.redirect.consent", extensionmanifest.RouteActionAdd, streamLoginRedirectConsentPath, methods[0],
	)
	declaration.Methods = methods
	declaration.Mode = mode
	declaration.Guard = guard
	// 含非安全方法的声明必须同时给出 request schema，否则注册表按设计拒绝发布。
	for _, method := range methods {
		if method != stdhttp.MethodGet && method != stdhttp.MethodHead {
			declaration.RequestSchema = declaration.ID + ".request@1"
			break
		}
	}
	return declaration
}

func streamLoginRedirectRequest(method, target, accept string) *stdhttp.Request {
	request := httptest.NewRequest(method, target, nil)
	if accept != "" {
		request.Header.Set(fiber.HeaderAccept, accept)
	}
	return request
}

func assertStreamLoginJSONError(t *testing.T, response *stdhttp.Response, status int, reason string) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if location := response.Header.Get(fiber.HeaderLocation); location != "" {
		t.Fatalf("unexpected redirect location %q", location)
	}
	if response.StatusCode != status {
		t.Fatalf("status=%d want=%d body=%q", response.StatusCode, status, body)
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope %q: %v", body, err)
	}
	if envelope.Code != status || envelope.Data.Reason != reason {
		t.Fatalf("envelope=%#v want status=%d reason=%q", envelope, status, reason)
	}
}

// streamLoginRedirectErrorInvoker 让 OpenStream 直接失败，用于证明非守卫错误不跳转。
type streamLoginRedirectErrorInvoker struct {
	err error
}

func (*streamLoginRedirectErrorInvoker) SupportsMode(string) bool { return false }

func (*streamLoginRedirectErrorInvoker) Invoke(
	context.Context, routes.RouteInvocation,
) (routes.RouteInvocationResult, error) {
	return routes.RouteInvocationResult{}, errors.New("buffered invocation is not expected")
}

func (i *streamLoginRedirectErrorInvoker) OpenStream(
	context.Context, routes.RouteInvocation,
) (routes.RouteStreamStart, error) {
	return routes.RouteStreamStart{}, i.err
}

var _ routes.StepInvoker = (*streamLoginRedirectErrorInvoker)(nil)
var _ routes.StreamingStepInvoker = (*streamLoginRedirectErrorInvoker)(nil)
