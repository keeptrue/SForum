package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	extensionscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Extensions"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

func TestPublicFrontendRuntimeMatchesExactAcquiredInstance(t *testing.T) {
	target := extensionsruntime.RuntimeInstanceSnapshot{
		Identity:         extensionsruntime.RuntimeInstanceIdentity{ExtensionID: "demo.plugin", InstanceID: "runtime-1"},
		ExtensionVersion: "1.0.0",
		ArtifactDigest:   "package-v1",
	}
	if !publicFrontendRuntimeMatches(nil, target) {
		t.Fatal("ordinary extension route unexpectedly required a public frontend identity")
	}
	exact := &extensionscontroller.PublicFrontendBridgeIdentity{
		ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0", PackageDigest: "package-v1",
	}
	if !publicFrontendRuntimeMatches(exact, target) {
		t.Fatal("exact public frontend bridge did not match its acquired runtime")
	}
	stale := *exact
	stale.PackageDigest = "package-v2"
	if publicFrontendRuntimeMatches(&stale, target) {
		t.Fatal("stale public frontend package reached a different runtime")
	}
}

func TestExtensionRouteGatewayStripsPluginLinkOnProductionAdapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Add("Link", `<https://evil.example/>; rel="canonical"`)
		writer.Header().Add("Link", `</asset.js>; rel="preload"`)
		writer.Header().Set("X-Plugin-Metadata", "kept")
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	identity := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: "demo.plugin", InstanceID: "runtime-1"}
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &providerRouteRuntime{
		gate: gate,
		snapshot: extensionsruntime.RuntimeInstanceSnapshot{
			Identity: identity, Active: true,
			Target: extensionsruntime.RouteTarget{BaseURL: server.URL, InstanceID: identity.InstanceID},
		},
	}
	gateway := extensionRouteGateway{runtime: runtime, gateway: extensionsruntime.NewRouteGateway()}
	app := fiber.New()
	app.Get("/proxy", func(c fiber.Ctx) error {
		return gateway.Proxy(c, extensionscontroller.ProxyInput{Matched: extensions.MatchedRoute{
			Extension: extensions.Extension{ID: identity.ExtensionID}, Path: "/links",
		}})
	})

	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/proxy", nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || len(response.Header.Values("Link")) != 0 ||
		response.Header.Get("X-Plugin-Metadata") != "kept" || runtime.calls != 1 ||
		runtime.extensionID != identity.ExtensionID || runtime.class != extensionsruntime.RuntimeCallRoute {
		t.Fatalf("status=%d headers=%v runtime=%#v", response.StatusCode, response.Header, runtime)
	}
}

type providerRouteRuntime struct {
	extensions.RuntimeManager
	snapshot    extensionsruntime.RuntimeInstanceSnapshot
	gate        *extensionsruntime.RuntimeAdmissionGate
	calls       int
	extensionID string
	class       extensionsruntime.RuntimeCallClass
}

func (r *providerRouteRuntime) RouteTarget(extensionID string) (extensionsruntime.RouteTarget, bool) {
	return r.snapshot.Target, extensionID == r.snapshot.Identity.ExtensionID
}

func (r *providerRouteRuntime) AcquireActiveRuntimeCall(
	ctx context.Context,
	extensionID string,
	class extensionsruntime.RuntimeCallClass,
) (extensionsruntime.RuntimeInstanceSnapshot, *extensionsruntime.RuntimeAdmissionLease, error) {
	r.calls++
	r.extensionID = extensionID
	r.class = class
	lease, err := r.gate.Acquire(ctx, class)
	return r.snapshot, lease, err
}

// providerProtocolV2Runtime 在 HTTP 代理之外补上 exact-instance V2 派发，
// 用来覆盖「legacy 前缀 + 协议 v2 插件（无 HTTP BaseURL）」这条回落分支。
type providerProtocolV2Runtime struct {
	*providerRouteRuntime
	requests []extensionsruntime.ProtocolV2RouteRequest
	response extensionsruntime.ProtocolV2RouteResponse
	err      error
}

func (r *providerProtocolV2Runtime) InvokeRouteInstance(
	_ context.Context,
	_ extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	r.requests = append(r.requests, request)
	return r.response, r.err
}

func newProviderRouteRuntime(
	t *testing.T,
	instance extensionsruntime.RuntimeInstanceIdentity,
	target extensionsruntime.RouteTarget,
) *providerRouteRuntime {
	t.Helper()
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(instance)
	if err != nil {
		t.Fatal(err)
	}
	return &providerRouteRuntime{
		gate:     gate,
		snapshot: extensionsruntime.RuntimeInstanceSnapshot{Identity: instance, Active: true, Target: target},
	}
}

func protocolV2CatalogRoute() extensions.ManifestRoute {
	return extensions.ManifestRoute{
		ID:              "demo.route.catalog",
		ContractVersion: "demo.route.catalog@1",
		Action:          "add",
		Path:            "/api/store/catalog",
		Methods:         []string{http.MethodGet},
		Access:          extensions.RouteAccessLogin,
		Guard:           "core.guard.login",
		ResponseSchema:  "demo.catalog.response@1",
		TimeoutMS:       1200,
	}
}

// TestExtensionRouteGatewayDispatchesProtocolV2RouteWithoutBaseURL 覆盖 allowed 路径：
// 目标没有 HTTP BaseURL 时，legacy 前缀必须改写成一次 V2 路由调用，而不是 503。
func TestExtensionRouteGatewayDispatchesProtocolV2RouteWithoutBaseURL(t *testing.T) {
	instance := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: "demo.plugin", InstanceID: "runtime-1"}
	route := protocolV2CatalogRoute()
	runtime := &providerProtocolV2Runtime{
		providerRouteRuntime: newProviderRouteRuntime(t, instance, extensionsruntime.RouteTarget{}),
		response: extensionsruntime.ProtocolV2RouteResponse{
			StatusCode:  http.StatusOK,
			Headers:     http.Header{"Content-Type": {"application/json"}},
			Body:        map[string]any{"products": []any{map[string]any{"id": "demo"}}},
			BodyPresent: true,
		},
	}
	gateway := extensionRouteGateway{runtime: runtime, gateway: extensionsruntime.NewRouteGateway()}
	app := fiber.New()
	app.Get("/proxy", func(c fiber.Ctx) error {
		return gateway.Proxy(c, extensionscontroller.ProxyInput{
			Matched: extensions.MatchedRoute{
				Extension: extensions.Extension{
					ID: instance.ExtensionID, Manifest: extensions.Manifest{Routes: []extensions.ManifestRoute{route}},
				},
				Route: route, Path: route.Path,
			},
			Actor:    identity.Actor{ID: 7, Permissions: map[string]bool{}},
			HasActor: true,
		})
	})

	request := httptest.NewRequest(http.MethodGet, "/proxy?compat=all&tag=a&tag=b", nil)
	request.Header.Set("X-Trace", "trace-1")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode dispatched body: %v", err)
	}
	if response.StatusCode != http.StatusOK || payload["products"] == nil {
		t.Fatalf("status=%d payload=%v", response.StatusCode, payload)
	}
	if response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("expected plugin content-type preserved, got %q", response.Header.Get("Content-Type"))
	}
	if len(runtime.requests) != 1 || runtime.calls != 1 {
		t.Fatalf("expected exactly one V2 dispatch and one admission, got %d/%d", len(runtime.requests), runtime.calls)
	}
	call := runtime.requests[0]
	if call.RouteID != route.ID || call.ContractVersion != route.ContractVersion ||
		call.RouteAction != route.Action || call.Method != http.MethodGet ||
		call.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageHandler {
		t.Fatalf("unexpected dispatch identity: %#v", call)
	}
	// 声明路径，不是 legacy 前缀路径。
	if call.Path != route.Path {
		t.Fatalf("expected declared path %q, got %q", route.Path, call.Path)
	}
	if call.QueryParameters["compat"] != "all" || len(call.QueryParameterValues["tag"]) != 2 {
		t.Fatalf("query not preserved: %v / %v", call.QueryParameters, call.QueryParameterValues)
	}
	if call.Headers.Get("X-Trace") != "trace-1" {
		t.Fatalf("request headers not forwarded: %v", call.Headers)
	}
	expectedAuthority := extensionsruntime.ProtocolV2RequestAuthority{
		Mode: extensionsruntime.ProtocolV2RequestAuthorityFiltered, GuardKind: extensionsruntime.ProtocolV2RequestGuardHost,
	}
	if call.Authority != expectedAuthority {
		t.Fatalf("expected host-filtered authority, got %#v", call.Authority)
	}
	if call.Actor == nil || call.Actor.UserID != 7 {
		t.Fatalf("expected host actor on the wire, got %#v", call.Actor)
	}
	if call.Timeout != 1200*time.Millisecond || call.BodyPresent {
		t.Fatalf("timeout=%s bodyPresent=%v", call.Timeout, call.BodyPresent)
	}
}

// TestExtensionRouteGatewayDispatchesProtocolV2BodyWithDeclaredSchema 覆盖 POST 类型化 body。
func TestExtensionRouteGatewayDispatchesProtocolV2BodyWithDeclaredSchema(t *testing.T) {
	instance := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: "demo.plugin", InstanceID: "runtime-1"}
	route := extensions.ManifestRoute{
		ID: "demo.route.install", ContractVersion: "demo.route.install@1", Action: "add",
		Path: "/api/store/install-url", Methods: []string{http.MethodPost},
		Access: extensions.RouteAccessLogin, Guard: "core.guard.login",
		RequestSchema: "demo.install.request@1", ResponseSchema: "demo.install.response@1",
	}
	runtime := &providerProtocolV2Runtime{
		providerRouteRuntime: newProviderRouteRuntime(t, instance, extensionsruntime.RouteTarget{}),
		response: extensionsruntime.ProtocolV2RouteResponse{
			StatusCode: http.StatusAccepted, Body: map[string]any{"staged": true}, BodyPresent: true,
		},
	}
	gateway := extensionRouteGateway{runtime: runtime, gateway: extensionsruntime.NewRouteGateway()}
	app := fiber.New()
	app.Post("/proxy", func(c fiber.Ctx) error {
		return gateway.Proxy(c, extensionscontroller.ProxyInput{
			Matched: extensions.MatchedRoute{
				Extension: extensions.Extension{ID: instance.ExtensionID},
				Route:     route, Path: route.Path,
			},
			Actor: identity.Actor{ID: 7}, HasActor: true,
		})
	})
	request := httptest.NewRequest(http.MethodPost, "/proxy", strings.NewReader(`{"url":"https://example.com/pkg.zip"}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || len(runtime.requests) != 1 {
		t.Fatalf("status=%d dispatches=%d", response.StatusCode, len(runtime.requests))
	}
	call := runtime.requests[0]
	if !call.BodyPresent || call.Body["url"] != "https://example.com/pkg.zip" || call.Method != http.MethodPost {
		t.Fatalf("typed body not dispatched: %#v", call)
	}
}

// TestExtensionRouteGatewayFailsClosedWhenProtocolV2DispatchFails 覆盖 denied 侧：
// 派发失败必须以 ErrRuntimeFailed 收口（controller 映射为 503），不得伪成功。
func TestExtensionRouteGatewayFailsClosedWhenProtocolV2DispatchFails(t *testing.T) {
	instance := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: "demo.plugin", InstanceID: "runtime-1"}
	route := protocolV2CatalogRoute()
	runtime := &providerProtocolV2Runtime{
		providerRouteRuntime: newProviderRouteRuntime(t, instance, extensionsruntime.RouteTarget{}),
		err:                  errors.New("plugin rpc failed"),
	}
	gateway := extensionRouteGateway{runtime: runtime, gateway: extensionsruntime.NewRouteGateway()}
	var gotErr error
	app := fiber.New()
	app.Get("/proxy", func(c fiber.Ctx) error {
		gotErr = gateway.Proxy(c, extensionscontroller.ProxyInput{
			Matched: extensions.MatchedRoute{
				Extension: extensions.Extension{ID: instance.ExtensionID}, Route: route, Path: route.Path,
			},
		})
		if gotErr != nil {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		return nil
	})
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/proxy", nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !errors.Is(gotErr, extensions.ErrRuntimeFailed) {
		t.Fatalf("expected ErrRuntimeFailed, got %v", gotErr)
	}
	if len(runtime.requests) != 1 {
		t.Fatalf("expected the dispatch to be attempted once, got %d", len(runtime.requests))
	}
}

// TestExtensionRouteGatewayKeepsHTTPProxyForBaseURLTargets 是 v1 回归：
// 目标有 BaseURL 时仍然走原来的 HTTP 代理，且绝不触碰 V2 派发。
func TestExtensionRouteGatewayKeepsHTTPProxyForBaseURLTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/store/catalog" || request.URL.Query().Get("compat") != "all" {
			t.Errorf("unexpected v1 proxy target: %s", request.URL.String())
		}
		writer.Header().Set("X-V1", "kept")
		_, _ = writer.Write([]byte("v1-ok"))
	}))
	defer server.Close()

	instance := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: "demo.plugin", InstanceID: "runtime-1"}
	route := protocolV2CatalogRoute()
	runtime := &providerProtocolV2Runtime{
		providerRouteRuntime: newProviderRouteRuntime(t, instance, extensionsruntime.RouteTarget{
			BaseURL: server.URL, InstanceID: instance.InstanceID,
		}),
	}
	gateway := extensionRouteGateway{runtime: runtime, gateway: extensionsruntime.NewRouteGateway()}
	app := fiber.New()
	app.Get("/proxy", func(c fiber.Ctx) error {
		return gateway.Proxy(c, extensionscontroller.ProxyInput{
			Matched: extensions.MatchedRoute{
				Extension: extensions.Extension{ID: instance.ExtensionID}, Route: route, Path: route.Path,
			},
		})
	})
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/proxy?compat=all", nil))
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 5)
	_, _ = response.Body.Read(body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "v1-ok" ||
		response.Header.Get("X-V1") != "kept" || len(runtime.requests) != 0 {
		t.Fatalf("v1 proxy regression: status=%d body=%q header=%q v2calls=%d",
			response.StatusCode, string(body), response.Header.Get("X-V1"), len(runtime.requests))
	}
}
