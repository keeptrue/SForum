package http

import (
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// Fiber 的路由匹配不区分大小写，Registry 的 plan 匹配区分大小写。入口判定
// 必须与路由匹配同口径，否则大写路径会跳过整条托管链后仍然命中 handler。
func TestIsAPIRoutePathIgnoresCase(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/api/v1", want: true},
		{path: "/api/v1/health", want: true},
		{path: "/API/V1", want: true},
		{path: "/API/V1/health", want: true},
		{path: "/Api/V1/auth/login", want: true},
		{path: "/apiv1", want: false},
		{path: "/api/v10", want: false},
		{path: "/admin/users", want: false},
		{path: "/", want: false},
	}
	for _, tt := range tests {
		if got := isAPIRoutePath(tt.path); got != tt.want {
			t.Fatalf("isAPIRoutePath(%q)=%v, want %v", tt.path, got, tt.want)
		}
	}
}

// 大写/混合大小写变体必须与小写路径走同一条限流、维护、CSRF、Dispatcher 链，
// 不能直抵 handler。
func TestUppercaseAPIPathRunsRegistryManagedChain(t *testing.T) {
	var hits atomic.Int64
	cfg := routeDispatcherConfig()
	cfg.CSRFEnabled = true
	cfg.CSRFTrustedOrigins = []string{"https://forum.example.com"}
	app := NewApp(cfg, slog.Default(), Dependencies{
		RouteProviders: []RouteProvider{arbitraryRouteProviderFunc(func(api fiber.Router) {
			api.Post("/ingress-probe", func(c fiber.Ctx) error {
				hits.Add(1)
				return c.SendStatus(fiber.StatusNoContent)
			})
		})},
	})

	for _, path := range []string{"/api/v1/ingress-probe", "/API/V1/ingress-probe", "/Api/V1/ingress-probe"} {
		request := httptest.NewRequest(stdhttp.MethodPost, path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != stdhttp.StatusForbidden {
			t.Fatalf("%s without CSRF token status=%d, want 403", path, response.StatusCode)
		}
		if hits.Load() != 0 {
			t.Fatalf("%s reached the handler without traversing CSRF", path)
		}
	}

	// 反向对照：带合法 CSRF 令牌时，大小写两种写法都必须真正落到 handler。
	prime := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/health", nil)
	primeResponse, err := app.Test(prime)
	if err != nil {
		t.Fatal(err)
	}
	token := arbitraryRouteCSRFToken(t, primeResponse)
	primeResponse.Body.Close()

	for _, path := range []string{"/api/v1/ingress-probe", "/API/V1/ingress-probe"} {
		request := httptest.NewRequest(stdhttp.MethodPost, path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://forum.example.com")
		request.Header.Set("X-Csrf-Token", token)
		request.AddCookie(&stdhttp.Cookie{Name: "csrf_", Value: token})
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != stdhttp.StatusNoContent {
			t.Fatalf("%s with CSRF token status=%d, want 204", path, response.StatusCode)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("handler hits=%d, want 2 (lowercase + uppercase)", hits.Load())
	}
}

// RV-29：探针头只在可信内部调用方生效，外部调用方不得据响应差异枚举
// 插件占用的路径。
//
// app.Test 的连接固定上报 0.0.0.0:0（Fiber testConn），无法用它模拟公网对端，
// 所以地址判定单测落在纯函数上；端到端只覆盖「转发链头」这条外部形状。
func TestIsInternalRouteProbeAddress(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1", want: true},
		{addr: "::1", want: true},
		{addr: "172.18.0.4", want: true},
		{addr: "10.1.2.3", want: true},
		{addr: "fd00::1", want: true},
		// 进程内传输（Fiber 测试连接、unix socket）没有具体对端地址。
		{addr: "0.0.0.0", want: true},
		{addr: "::", want: true},
		{addr: "203.0.113.7", want: false},
		{addr: "2001:db8::1", want: false},
		{addr: "", want: false},
		{addr: "not-an-ip", want: false},
	}
	for _, tt := range tests {
		if got := isInternalRouteProbeAddress(tt.addr); got != tt.want {
			t.Fatalf("isInternalRouteProbeAddress(%q)=%v, want %v", tt.addr, got, tt.want)
		}
	}
}

func TestRouteProbeRejectsForwardedCallers(t *testing.T) {
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{})

	probe := func(extraHeaders map[string]string) *stdhttp.Response {
		request := httptest.NewRequest(stdhttp.MethodHead, "/admin/host-owned-page", nil)
		request.Header.Set(internalRouteProbeHeader, internalRouteProbeVersion)
		request.Header.Set(internalRouteProbeMethodHeader, stdhttp.MethodPost)
		for name, value := range extraHeaders {
			request.Header.Set(name, value)
		}
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		response.Body.Close()
		return response
	}

	internal := probe(nil)
	if internal.StatusCode != stdhttp.StatusNotFound ||
		internal.Header.Get(internalRouteProbeResultHeader) != internalRouteProbeMiss {
		t.Fatalf("internal probe status=%d headers=%#v", internal.StatusCode, internal.Header)
	}

	// 任何边缘代理都会注入的客户端来源头：探针不再可用，且不得回传占用证据。
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded", "CF-Connecting-IP", "True-Client-IP"} {
		denied := probe(map[string]string{header: "203.0.113.7"})
		if denied.StatusCode != stdhttp.StatusForbidden ||
			denied.Header.Get(internalRouteProbeResultHeader) != "" {
			t.Fatalf("%s probe status=%d headers=%#v", header, denied.StatusCode, denied.Header)
		}
	}
}
