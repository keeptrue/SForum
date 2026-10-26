package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"
	"google.golang.org/protobuf/types/known/structpb"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	routeGuardProductionHelperEnv = "route-guard-production-chain-e2e"
	routeGuardCustomID            = "runtime.guard.custom"
	routeGuardRawID               = "runtime.guard.raw"
	routeGuardHTTPCustomID        = "runtime.guard.http.custom"
	routeGuardHTTPRawID           = "runtime.guard.http.raw"
	routeGuardWebSocketID         = "runtime.guard.websocket"
)

// TestRouteCustomAndRawGuardsAcrossFiberManagerAndRealProtocolV2Process is the
// production-chain evidence for custom/raw guards: Fiber middleware, Registry
// execution plan, ProductionRouteGuardAuthorizer, RuntimePluginRouteGuardEvaluator,
// and a real Protocol V2 go-plugin subprocess.
func TestRouteCustomAndRawGuardsAcrossFiberManagerAndRealProtocolV2Process(t *testing.T) {
	extension := routeGuardProductionExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: routeStreamE2ETrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "guard-grant", ImpactDigest: "guard-impact",
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Identity.InstanceID == "" || runtime.Target.BaseURL != "" {
		t.Fatalf("expected exact Protocol V2 subprocess runtime: %#v", runtime)
	}

	registry := routes.NewRegistry()
	artifact := routes.PluginArtifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	}
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: extension.Manifest.Routes, Guards: extension.Manifest.Guards,
	}}}); err != nil {
		t.Fatal(err)
	}

	policy := &routeGuardProductionPolicy{
		lookup: extensions.GuardPolicyLookup{
			Revision: 1, Found: true,
			Entry: extensions.GuardPolicyEntry{
				ExtensionID: extension.ID, ExtensionType: extensions.TypePlugin,
				Status: extensions.StatusEnabled, Version: extension.Version,
				PackageDigest: extension.PackageDigest, CurrentTrustRequired: true, CurrentArtifactTrusted: true,
			},
		},
		ok: true,
	}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry},
		Steps: NewBufferedRouteStepInvoker(manager),
		Guard: NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{
			PluginGuards: NewRuntimePluginRouteGuardEvaluator(manager, policy),
		}),
		Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	app := fiber.New()
	app.Use(routeDispatcherMiddleware(dispatcher, func(fiber.Ctx) (identity.Actor, error) {
		return identity.Actor{ID: 42, Status: identity.UserStatusActive, Permissions: map[string]bool{"topic.read": true}}, nil
	}))

	// custom allow: guard then handler; credentials stay filtered.
	status, body, calls := routeGuardProductionDo(t, app, starter, extension.ID, routeGuardProductionRequest(
		stdhttp.MethodPost, "/guard/custom", `{"title":"ok"}`, "",
	))
	if status != stdhttp.StatusCreated || body != `{"ok":"custom"}` || calls < 2 {
		t.Fatalf("custom allow status=%d body=%q calls=%d", status, body, calls)
	}
	afterAllow := starter.ProtocolTelemetry(extension.ID).CallCount

	// custom deny: Host maps typed deny to 403 and never invokes the route handler.
	status, _, afterDeny := routeGuardProductionDo(t, app, starter, extension.ID, routeGuardProductionRequest(
		stdhttp.MethodPost, "/guard/custom", `{"title":"ok"}`, "deny=1",
	))
	if status != stdhttp.StatusForbidden || afterDeny <= afterAllow {
		t.Fatalf("custom deny status=%d calls=%d→%d", status, afterAllow, afterDeny)
	}
	denyOnlyCalls := afterDeny - afterAllow
	if denyOnlyCalls < 1 {
		t.Fatalf("custom deny did not invoke guard: delta=%d", denyOnlyCalls)
	}

	// raw_request allow: exact trusted artifact forwards browser credentials.
	status, body, rawCalls := routeGuardProductionDo(t, app, starter, extension.ID, routeGuardProductionRequest(
		stdhttp.MethodPost, "/guard/raw", `{"title":"ok"}`, "",
	))
	if status != stdhttp.StatusCreated || body != `{"ok":"raw"}` || rawCalls <= afterDeny {
		t.Fatalf("raw allow status=%d body=%q calls=%d", status, body, rawCalls)
	}
	afterRaw := starter.ProtocolTelemetry(extension.ID).CallCount

	// trust revoke: fail closed before any further plugin invoke; no credential path.
	policy.SetTrusted(false)
	status, _, revokedCalls := routeGuardProductionDo(t, app, starter, extension.ID, routeGuardProductionRequest(
		stdhttp.MethodPost, "/guard/raw", `{"title":"ok"}`, "",
	))
	if status != stdhttp.StatusBadGateway || revokedCalls != afterRaw {
		t.Fatalf("raw trust revoke status=%d calls=%d want=%d", status, revokedCalls, afterRaw)
	}
	status, _, customRevoked := routeGuardProductionDo(t, app, starter, extension.ID, routeGuardProductionRequest(
		stdhttp.MethodPost, "/guard/custom", `{"title":"ok"}`, "",
	))
	if status != stdhttp.StatusBadGateway || customRevoked != afterRaw {
		t.Fatalf("custom trust revoke status=%d calls=%d want=%d", status, customRevoked, afterRaw)
	}
	if after, err := manager.InspectRuntimeInstance(runtime.Identity); err != nil || after.Admission.ActiveTotal != 0 {
		t.Fatalf("admission after production chain=%#v err=%v", after, err)
	}
}

func TestRouteHostRouteGuardAuthorizerCannotMintRawOnFiber(t *testing.T) {
	// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 已不可发布（拒绝侧见
	// TestRouteRetiredCoreRawRequestGuardIsRejectedAtPublication），因此本测试改用包内
	// 显式声明的 kind=raw_request 守卫作为载体，断言强度不变且更强：即使路由合法声明了
	// raw_request 守卫，legacy HostRouteGuardAuthorizer 也不得在 Fiber 栈上铸造 raw
	// 权限，并且在拒绝前不会调用任何插件代码。
	extension := routeGuardProductionExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: routeStreamE2ETrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "legacy-grant", ImpactDigest: "legacy-impact",
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	registry := routes.NewRegistry()
	rawRoute := extensions.ManifestRoute{
		ID: "runtime.guard.http.legacy_raw", ContractVersion: "runtime.guard.http.legacy_raw@1",
		Action: extensionmanifest.RouteActionAdd, Path: "/guard/legacy-raw",
		Methods: []string{stdhttp.MethodPost}, Guard: routeGuardRawID,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.legacy_raw",
		RequestSchema: "runtime.guard.http.legacy_raw.request@1", ResponseSchema: "runtime.guard.http.legacy_raw.response@1",
	}
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: routes.PluginArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
			RuntimeInstanceID: runtime.Identity.InstanceID,
		},
		Routes: []extensions.ManifestRoute{rawRoute}, Guards: extension.Manifest.Guards,
	}}}); err != nil {
		t.Fatal(err)
	}
	before := starter.ProtocolTelemetry(extension.ID).CallCount
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans:   routeRegistryPlanResolver{registry: registry},
		Steps:   NewBufferedRouteStepInvoker(manager),
		Guard:   HostRouteGuardAuthorizer{},
		Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	app := fiber.New()
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	status, body, after := routeGuardProductionDo(t, app, starter, extension.ID, routeGuardProductionRequest(
		stdhttp.MethodPost, "/guard/legacy-raw", `{"title":"secret"}`, "",
	))
	if status != stdhttp.StatusForbidden || after != before {
		t.Fatalf("legacy raw status=%d body=%q calls=%d→%d", status, body, before, after)
	}
}

// TestRouteRetiredCoreRawRequestGuardIsRejectedAtPublication 是 R1 的拒绝侧断言。
// 旧契约下任何路由只要引用 Host 拥有的 core.guard.raw_request（即使完全没有插件
// 守卫绑定、没有一行插件代码）就能获得 raw 权限并接收浏览器凭证；现在该守卫必须在
// 发布/清单校验阶段被拒绝，且不能再被解析成执行计划——raw 权限入口就此关闭。
func TestRouteRetiredCoreRawRequestGuardIsRejectedAtPublication(t *testing.T) {
	registry := routes.NewRegistry()
	retired := extensions.ManifestRoute{
		ID: "runtime.guard.http.core_raw", ContractVersion: "runtime.guard.http.core_raw@1",
		Action: extensionmanifest.RouteActionAdd, Path: "/guard/core-raw",
		Methods: []string{stdhttp.MethodPost}, Guard: extensionmanifest.GuardCoreRaw,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.core_raw",
		RequestSchema: "runtime.guard.http.core_raw.request@1", ResponseSchema: "runtime.guard.http.core_raw.response@1",
	}
	_, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: routes.PluginArtifact{
			ExtensionID: "runtime.guard", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), RuntimeInstanceID: "runtime-instance",
		},
		Routes: []extensions.ManifestRoute{retired},
	}}})
	if !errors.Is(err, routes.ErrInvalidRoute) {
		t.Fatalf("retired core raw guard publish error = %v", err)
	}
	if _, planErr := registry.BuildExecutionPlan(stdhttp.MethodPost, retired.Path); planErr == nil {
		t.Fatal("retired core raw guard route was still resolvable into an execution plan")
	}
}

func TestRouteWebSocketCustomGuardRunsOnlyAtOpenPreflight(t *testing.T) {
	extension := routeGuardProductionExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: routeStreamE2ETrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "ws-guard-grant", ImpactDigest: "ws-guard-impact",
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: routes.PluginArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
			RuntimeInstanceID: runtime.Identity.InstanceID,
		},
		Routes: extension.Manifest.Routes, Guards: extension.Manifest.Guards,
	}}}); err != nil {
		t.Fatal(err)
	}
	policy := &routeGuardProductionPolicy{
		lookup: extensions.GuardPolicyLookup{
			Revision: 1, Found: true,
			Entry: extensions.GuardPolicyEntry{
				ExtensionID: extension.ID, ExtensionType: extensions.TypePlugin,
				Status: extensions.StatusEnabled, Version: extension.Version,
				PackageDigest: extension.PackageDigest, CurrentTrustRequired: true, CurrentArtifactTrusted: true,
			},
		},
		ok: true,
	}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry},
		Steps: NewBufferedRouteStepInvoker(manager),
		Guard: NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{
			PluginGuards: NewRuntimePluginRouteGuardEvaluator(manager, policy),
		}),
		Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	app := fiber.New()
	app.Use(routeDispatcherMiddleware(dispatcher, func(fiber.Ctx) (identity.Actor, error) {
		return identity.Actor{ID: 42, Status: identity.UserStatusActive}, nil
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- app.Listener(listener) }()
	t.Cleanup(func() {
		_ = app.Shutdown()
		_ = listener.Close()
		<-serverDone
	})
	webSocketURL := "ws://" + listener.Addr().String() + "/guard/socket"

	// deny at Open preflight: no upgrade, guard observed, no stream lease retained.
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: []string{"sforum.stream.v1"}}
	deniedHeaders := stdhttp.Header{
		"Authorization": {"Bearer browser-secret"},
		"Cookie":        {"session=browser-secret"},
	}
	_, deniedResponse, deniedErr := dialer.Dial(webSocketURL+"?deny=1", deniedHeaders)
	if deniedResponse != nil && deniedResponse.Body != nil {
		_ = deniedResponse.Body.Close()
	}
	if deniedErr == nil {
		t.Fatal("denied WebSocket guard unexpectedly upgraded")
	}
	if deniedResponse == nil || deniedResponse.StatusCode != stdhttp.StatusForbidden {
		t.Fatalf("denied WebSocket status=%v", deniedResponse)
	}
	afterDeny := starter.ProtocolTelemetry(extension.ID).CallCount
	if afterDeny < 1 {
		t.Fatal("denied WebSocket did not invoke custom guard")
	}
	if after, err := manager.InspectRuntimeInstance(runtime.Identity); err != nil || after.Admission.ActiveTotal != 0 {
		t.Fatalf("denied WebSocket retained lease: %#v err=%v", after, err)
	}

	// allow: guard + unary preflight + stream open once; post-upgrade traffic does not re-run guard.
	connection, response, err := dialer.Dial(webSocketURL, deniedHeaders)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial status=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	afterOpen := starter.ProtocolTelemetry(extension.ID).CallCount
	if afterOpen <= afterDeny {
		t.Fatalf("allowed WebSocket open calls=%d deny=%d", afterOpen, afterDeny)
	}
	for _, payload := range []string{"one", "two", "three"} {
		if err := connection.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatal(err)
		}
		_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
		messageType, message, err := connection.ReadMessage()
		if err != nil || string(message) != payload {
			t.Fatalf("messageType=%d payload=%q message=%q err=%v", messageType, payload, message, err)
		}
	}
	afterTraffic := starter.ProtocolTelemetry(extension.ID).CallCount
	if afterTraffic != afterOpen {
		t.Fatalf("post-upgrade traffic re-entered plugin: open=%d traffic=%d", afterOpen, afterTraffic)
	}
	_ = connection.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"))
	waitExactRouteDrain(t, manager, runtime)

	// trust revoke after open boundary still blocks new handshakes without further invokes.
	policy.SetTrusted(false)
	beforeRevoke := starter.ProtocolTelemetry(extension.ID).CallCount
	_, revokedResponse, revokedErr := dialer.Dial(webSocketURL, nil)
	if revokedResponse != nil && revokedResponse.Body != nil {
		_ = revokedResponse.Body.Close()
	}
	if revokedErr == nil {
		t.Fatal("revoked trust WebSocket unexpectedly upgraded")
	}
	if starter.ProtocolTelemetry(extension.ID).CallCount != beforeRevoke {
		t.Fatalf("revoked WebSocket invoked plugin: before=%d after=%d",
			beforeRevoke, starter.ProtocolTelemetry(extension.ID).CallCount)
	}
}

func TestRouteGuardProductionHelperProcess(t *testing.T) {
	if os.Getenv("SFORUM_PLUGIN_HELPER") != routeGuardProductionHelperEnv {
		return
	}
	server := pluginv2sdk.NewServer().
		WithFeatures(&protocolwire.ProtocolFeature{Name: "stream.routes", Version: "1"}).
		WithRuntimeStreams(pluginv2sdk.RuntimeStreams{Route: routeGuardProductionStreamHandler})
	pluginv2sdk.Serve(&routeGuardProductionServer{Server: server})
	os.Exit(0)
}

type routeGuardProductionServer struct{ *pluginv2sdk.Server }

func (s *routeGuardProductionServer) InvokeRoute(
	_ context.Context,
	request *pluginwire.RouteRequest,
) (*pluginwire.RouteResponse, error) {
	switch request.GetRouteId() {
	case routeGuardCustomID:
		return routeGuardProductionGuardResponse(request, false)
	case routeGuardRawID:
		return routeGuardProductionGuardResponse(request, true)
	case routeGuardHTTPCustomID:
		if header := routeStreamE2EForwardedCredential(request.GetHeaders()); header != "" {
			return routeGuardProductionError(request, "filtered custom route forwarded "+header)
		}
		return &pluginwire.RouteResponse{
			Context: routeStreamE2EResponseContext(request.GetContext()), StatusCode: stdhttp.StatusCreated,
			Headers: []*protocolwire.Header{{Name: "Content-Type", Values: []string{"application/json"}}},
			Body:    protocolV2RouteJSONBody("runtime.guard.http.custom.response", "1", map[string]any{"ok": "custom"}),
		}, nil
	case routeGuardHTTPRawID:
		if header := routeStreamE2EForwardedCredential(request.GetHeaders()); header == "" {
			return routeGuardProductionError(request, "raw route missing credentials")
		}
		return &pluginwire.RouteResponse{
			Context: routeStreamE2EResponseContext(request.GetContext()), StatusCode: stdhttp.StatusCreated,
			Headers: []*protocolwire.Header{{Name: "Content-Type", Values: []string{"application/json"}}},
			Body:    protocolV2RouteJSONBody("runtime.guard.http.raw.response", "1", map[string]any{"ok": "raw"}),
		}, nil
	case routeGuardWebSocketID:
		if header := routeStreamE2EForwardedCredential(request.GetHeaders()); header != "" {
			return routeGuardProductionError(request, "filtered websocket preflight forwarded "+header)
		}
		return &pluginwire.RouteResponse{
			Context:       routeStreamE2EResponseContext(request.GetContext()),
			StatusCode:    stdhttp.StatusSwitchingProtocols,
			Headers:       []*protocolwire.Header{{Name: "Sec-WebSocket-Protocol", Values: []string{"sforum.stream.v1"}}},
			StreamFollows: true,
		}, nil
	default:
		return &pluginwire.RouteResponse{
			Context: routeStreamE2EResponseContext(request.GetContext()),
			Error:   &protocolwire.ErrorDetail{Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "route.not_found"},
		}, nil
	}
}

func routeGuardProductionGuardResponse(request *pluginwire.RouteRequest, raw bool) (*pluginwire.RouteResponse, error) {
	if request.GetContractVersion() != request.GetRouteId()+"@1" ||
		request.GetMethod() == "" || request.GetPath() == "" ||
		request.GetContext().GetActor().GetUserId() != 42 {
		return &pluginwire.RouteResponse{
			Context:    routeStreamE2EResponseContext(request.GetContext()),
			StatusCode: stdhttp.StatusForbidden,
		}, nil
	}
	if raw {
		if header := routeStreamE2EForwardedCredential(request.GetHeaders()); header == "" {
			return routeGuardProductionError(request, "raw guard missing credentials")
		}
	} else if header := routeStreamE2EForwardedCredential(request.GetHeaders()); header != "" {
		return routeGuardProductionError(request, "custom guard forwarded "+header)
	}
	if request.GetQueryParameters()["deny"] == "1" {
		return &pluginwire.RouteResponse{
			Context:    routeStreamE2EResponseContext(request.GetContext()),
			StatusCode: stdhttp.StatusForbidden,
		}, nil
	}
	return &pluginwire.RouteResponse{
		Context: routeStreamE2EResponseContext(request.GetContext()), StatusCode: stdhttp.StatusNoContent,
	}, nil
}

func routeGuardProductionError(request *pluginwire.RouteRequest, reason string) (*pluginwire.RouteResponse, error) {
	return &pluginwire.RouteResponse{
		Context: routeStreamE2EResponseContext(request.GetContext()),
		Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: reason,
		},
	}, nil
}

func routeGuardProductionStreamHandler(stream *pluginv2sdk.RouteStream) error {
	if stream.Open().GetRouteId() != routeGuardWebSocketID {
		return fmt.Errorf("unknown stream route")
	}
	if header := routeStreamE2EForwardedCredential(stream.Open().GetHeaders()); header != "" {
		return fmt.Errorf("filtered websocket open forwarded %s", header)
	}
	chunk, err := stream.Recv()
	if err != nil {
		return err
	}
	if err := stream.Send(&protocolwire.DataChunk{Sequence: 1, Data: chunk.GetData()}); err != nil {
		return err
	}
	// Keep the stream open for multi-message echo without re-entering Host guard.
	for sequence := uint64(2); ; sequence++ {
		next, err := stream.Recv()
		if err == io.EOF {
			return stream.Close(&pluginwire.RouteStreamClose{StatusCode: stdhttp.StatusSwitchingProtocols})
		}
		if err != nil {
			return err
		}
		if err := stream.Send(&protocolwire.DataChunk{Sequence: sequence, Data: next.GetData()}); err != nil {
			return err
		}
	}
}

func routeGuardProductionExtension(t *testing.T) extensions.Extension {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "runtime.guard", "1.0.0")
	if err := os.MkdirAll(filepath.Join(packageRoot, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_HELPER=" + routeGuardProductionHelperEnv + " exec " +
		routeStreamShellQuote(os.Args[0]) + " -test.run='^TestRouteGuardProductionHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(packageRoot, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: "runtime.guard", Name: "Runtime Guard", Version: "1.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: strings.Repeat("a", 64), PackagePath: packageRoot,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "runtime.guard", Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2",
			},
			Guards: []extensions.ManifestGuard{
				{
					ID: routeGuardCustomID, ContractVersion: routeGuardCustomID + "@1", Kind: "custom",
					Entry: "backend/custom_guard", Digest: strings.Repeat("c", 64),
				},
				{
					ID: routeGuardRawID, ContractVersion: routeGuardRawID + "@1", Kind: "raw_request",
					Entry: "backend/raw_guard", Digest: strings.Repeat("b", 64),
				},
			},
			Routes: []extensions.ManifestRoute{
				{
					ID: routeGuardHTTPCustomID, ContractVersion: routeGuardHTTPCustomID + "@1",
					Action: extensionmanifest.RouteActionAdd, Path: "/guard/custom",
					Methods: []string{stdhttp.MethodPost}, Guard: routeGuardCustomID,
					Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.custom",
					RequestSchema: routeGuardHTTPCustomID + ".request@1", ResponseSchema: routeGuardHTTPCustomID + ".response@1",
				},
				{
					ID: routeGuardHTTPRawID, ContractVersion: routeGuardHTTPRawID + "@1",
					Action: extensionmanifest.RouteActionAdd, Path: "/guard/raw",
					Methods: []string{stdhttp.MethodPost}, Guard: routeGuardRawID,
					Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.raw",
					RequestSchema: routeGuardHTTPRawID + ".request@1", ResponseSchema: routeGuardHTTPRawID + ".response@1",
				},
				{
					ID: routeGuardWebSocketID, ContractVersion: routeGuardWebSocketID + "@1",
					Action: extensionmanifest.RouteActionAdd, Path: "/guard/socket",
					Methods: []string{stdhttp.MethodGet}, Guard: routeGuardCustomID,
					Fallback: "closed", Mode: extensionmanifest.RouteModeWebSocket, Handler: "route.socket",
					ResponseSchema: routeGuardWebSocketID + ".response@1",
				},
			},
		},
	}
}

func routeGuardProductionRequest(method, path, body, query string) *stdhttp.Request {
	target := path
	if query != "" {
		target += "?" + query
	}
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer browser-secret")
	request.Header.Set("X-API-Key", "api-key-secret")
	request.Header.Set("X-Auth-Token", "auth-token-secret")
	request.Header.Set("X-Trace-ID", "trace-guard-production")
	request.AddCookie(&stdhttp.Cookie{Name: "session", Value: "browser-secret"})
	return request
}

func routeGuardProductionDo(
	t *testing.T,
	app *fiber.App,
	starter *extensionsruntime.ProtocolStarter,
	extensionID string,
	request *stdhttp.Request,
) (int, string, uint64) {
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
	return response.StatusCode, string(body), starter.ProtocolTelemetry(extensionID).CallCount
}

type routeGuardProductionPolicy struct {
	lookup extensions.GuardPolicyLookup
	ok     bool
	mu     atomic.Bool
}

func (p *routeGuardProductionPolicy) Lookup(extensionID string) (extensions.GuardPolicyLookup, bool) {
	if p == nil || !p.ok || extensionID != p.lookup.Entry.ExtensionID {
		return extensions.GuardPolicyLookup{}, false
	}
	lookup := p.lookup
	if p.mu.Load() {
		lookup.Entry.CurrentArtifactTrusted = false
	}
	return lookup, true
}

func (p *routeGuardProductionPolicy) SetTrusted(trusted bool) {
	if p == nil {
		return
	}
	p.mu.Store(!trusted)
}

func protocolV2RouteJSONBody(schemaID, schemaVersion string, body map[string]any) *protocolwire.TypedDocument {
	value, err := structpb.NewStruct(body)
	if err != nil {
		panic(err)
	}
	return &protocolwire.TypedDocument{
		SchemaId: schemaID, SchemaVersion: schemaVersion, Value: value,
	}
}

var _ pluginwire.PluginRuntimeServiceServer = (*routeGuardProductionServer)(nil)
