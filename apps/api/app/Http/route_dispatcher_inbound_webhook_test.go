package http_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	webhookscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Webhooks"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/config"
)

// RV-06 的回归必须在**真实中间件链**上验证：生产链里的
// routeDispatcherMiddleware 会对每个 Registry 托管请求先读一次请求体，而
// StreamRequestBody 下 c.Body() 读完就关闭请求体流。测试若手搓一个不挂
// dispatcher 的 Fiber app，就会把「端点已坏」测成「绿」。
func TestInboundWebhookRunsThroughTheProductionDispatcherChain(t *testing.T) {
	app, probe := newInboundWebhookApp(t)

	// 1) 小 body：必须 200 且 bytes 反映真实长度。修复前 dispatcher 先读了体，
	//    handler 的 BodyStream 为 nil → 这里拿到 422 inbound_empty。
	small := []byte(`{"ping":true}`)
	resp, err := app.Test(httptest.NewRequest(stdhttp.MethodPost, "/api/v1/webhooks/inbound/github", bytes.NewReader(small)))
	if err != nil {
		t.Fatalf("small inbound: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("small inbound status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"bytes":13`)) {
		t.Fatalf("small inbound receipt = %s, want bytes 13", body)
	}

	// 2) 超过 1 MiB：立即 413（Content-Length 预检），而不是等 dispatcher 缓冲完。
	oversized := bytes.Repeat([]byte("a"), (1<<20)+1)
	resp, err = app.Test(httptest.NewRequest(stdhttp.MethodPost, "/api/v1/webhooks/inbound/github", bytes.NewReader(oversized)))
	if err != nil {
		t.Fatalf("oversized inbound: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusRequestEntityTooLarge {
		t.Fatalf("oversized inbound status = %d body=%s, want 413", resp.StatusCode, body)
	}
	// 未消费完的请求体不能再被当成下一条请求解析：413 必须关闭连接
	// （net/http 会把 Connection: close 折叠成 resp.Close）。
	if !resp.Close {
		t.Fatal("413 must close the connection so the unread body is not parsed as a pipelined request")
	}

	// 3) core-only 目标下 dispatcher 不得读体：同一条链上的探针 handler 必须还能
	//    拿到流式请求体。
	resp, err = app.Test(httptest.NewRequest(stdhttp.MethodPost, "/api/v1/inbound-probe", bytes.NewReader(small)))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("probe status = %d, want 200", resp.StatusCode)
	}
	if !probe.streamSeen.Load() {
		t.Fatal("core-only handler saw no request body stream: the dispatcher consumed it before Fiber routing")
	}
	if got := probe.bodyLen.Load(); got != int64(len(small)) {
		t.Fatalf("probe body length = %d, want %d", got, len(small))
	}
}

type inboundProbe struct {
	streamSeen atomic.Bool
	bodyLen    atomic.Int64
}

type inboundTestProvider struct {
	handler *webhookscontroller.Controller
	probe   *inboundProbe
}

func (p inboundTestProvider) RegisterRoutes(api fiber.Router) {
	p.handler.RegisterRoutes(api)
	api.Post("/inbound-probe", func(c fiber.Ctx) error {
		p.probe.streamSeen.Store(c.Request().IsBodyStream())
		p.probe.bodyLen.Store(int64(len(c.Body())))
		return apphttp.OK(c, map[string]any{"ok": true})
	})
}

type registryPlanResolver struct{ registry *routes.Registry }

func (r registryPlanResolver) BuildExecutionPlan(_ context.Context, method, path string) (routes.RouteExecutionPlan, error) {
	return r.registry.BuildExecutionPlan(method, path)
}

func newInboundWebhookApp(t *testing.T) (*fiber.App, *inboundProbe) {
	t.Helper()
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Core: []routes.CoreRoute{
		{ID: "core.route.webhooks.inbound", ContractVersion: "sforum.route.webhooks.inbound@1", Method: "POST", Path: "/api/v1/webhooks/inbound/:source"},
		{ID: "core.route.test.inbound_probe", ContractVersion: "sforum.route.test.inbound_probe@1", Method: "POST", Path: "/api/v1/inbound-probe"},
	}}); err != nil {
		t.Fatalf("publish core routes: %v", err)
	}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{Plans: registryPlanResolver{registry: registry}})
	probe := &inboundProbe{}
	cfg := config.Config{
		AppName: "SForum", AppEnv: "test", AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"},
		// 与生产默认一致：必须由入站端点自己收口，而不是靠全局上限。
		HTTPBodyLimit: 64 * 1024 * 1024,
	}
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{
		RouteDispatcher: dispatcher,
		RouteProviders: []apphttp.RouteProvider{inboundTestProvider{
			handler: webhookscontroller.NewController(nil, nil, nil),
			probe:   probe,
		}},
	})
	return app, probe
}
