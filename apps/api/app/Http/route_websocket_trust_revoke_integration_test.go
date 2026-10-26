package http

import (
	"context"
	"fmt"
	"net"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	routeStreamUnrelatedHelperEnv = "route-stream-unrelated-http-e2e"
	routeStreamUnrelatedRouteID   = "runtime.unrelated.websocket"
)

func TestRouteWebSocketSafeModeSnapshotDoesNotInvokeOrMutateExactRuntime(t *testing.T) {
	extension := routeStreamE2EExtension(t)
	trust := &routeStreamMutableTrust{}
	trust.Set(extensions.RuntimeTrustIdentity{TrustGrantID: "safe-mode-grant", ImpactDigest: "safe-mode-impact"})
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: trust})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}

	registry := routes.NewRegistry()
	snapshot, err := registry.Publish(routes.Publication{
		SafeMode: true,
		Plugins:  []routes.PluginRouteSet{routeStreamPluginRouteSet(extension, runtime)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.SafeMode || len(snapshot.Routes) != 0 {
		t.Fatalf("safe-mode route snapshot=%#v", snapshot)
	}
	webSocketURL := startRouteStreamWebSocketServer(t, registry, manager) + "/socket"
	if status := requireRouteStreamWebSocketHTTPRejected(t, webSocketURL); status != stdhttp.StatusNotFound {
		t.Fatalf("safe-mode WebSocket status=%d", status)
	}
	if telemetry := starter.ProtocolTelemetry(extension.ID); telemetry.CallCount != 0 {
		t.Fatalf("safe-mode WebSocket invoked plugin runtime: %#v", telemetry)
	}
	assertRouteStreamRuntimeUnchanged(t, manager, runtime, 0)

	publishRouteStreamRuntime(t, registry, extension, runtime)
	connection := dialRouteStreamWebSocket(t, webSocketURL)
	t.Cleanup(func() { _ = connection.Close() })
	assertExactRouteLease(t, manager, runtime, false)
	setRouteStreamWebSocketDeadline(t, connection)
	assertRouteStreamWebSocketEchoAndNormalClose(t, connection, "safe-mode-recovered")
	waitExactRouteDrain(t, manager, runtime)
}

func TestRouteWebSocketTrustRevocationDoesNotPolluteUnrelatedRuntime(t *testing.T) {
	target := routeStreamE2EExtension(t)
	unrelated := unrelatedRouteStreamE2EExtension(t)
	trust := &routeStreamMutableTrust{}
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: trust})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	t.Cleanup(func() { manager.Close(context.Background()) })

	trust.Set(extensions.RuntimeTrustIdentity{TrustGrantID: "target-grant", ImpactDigest: "target-impact"})
	if err := manager.Start(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	targetRuntime, err := manager.ActiveRuntimeInstance(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	trust.Set(extensions.RuntimeTrustIdentity{TrustGrantID: "unrelated-grant", ImpactDigest: "unrelated-impact"})
	if err := manager.Start(t.Context(), unrelated); err != nil {
		t.Fatal(err)
	}
	unrelatedRuntime, err := manager.ActiveRuntimeInstance(unrelated.ID)
	if err != nil {
		t.Fatal(err)
	}

	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{
		routeStreamPluginRouteSet(target, targetRuntime),
		routeStreamPluginRouteSet(unrelated, unrelatedRuntime),
	}}); err != nil {
		t.Fatal(err)
	}
	baseURL := startRouteStreamWebSocketServer(t, registry, manager)
	targetURL := baseURL + "/socket"
	unrelatedURL := baseURL + "/unrelated-socket"
	targetSocket := dialRouteStreamWebSocket(t, targetURL)
	t.Cleanup(func() { _ = targetSocket.Close() })
	unrelatedSocket := dialRouteStreamWebSocket(t, unrelatedURL)
	t.Cleanup(func() { _ = unrelatedSocket.Close() })
	assertExactRouteLease(t, manager, targetRuntime, false)
	assertExactRouteLease(t, manager, unrelatedRuntime, false)

	policy := &routeStreamTrustPolicy{}
	fence := extensionsruntime.NewExecutableTrustRevocationFence(manager, policy)
	if err := fence.RevokeExecutableTrust(t.Context(), target.ID, "operator_revoked", func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertRouteStreamRuntimeGracefullyRevoked(t, manager, targetRuntime, 1)
	assertRouteStreamRuntimeUnchanged(t, manager, unrelatedRuntime, 1)
	targetCalls := starter.ProtocolTelemetry(target.ID).CallCount
	if status := requireRouteStreamWebSocketHTTPRejected(t, targetURL); status != stdhttp.StatusBadGateway {
		t.Fatalf("revoked WebSocket status=%d", status)
	}
	if calls := starter.ProtocolTelemetry(target.ID).CallCount; calls != targetCalls {
		t.Fatalf("revoked WebSocket reached target RPC: before=%d after=%d", targetCalls, calls)
	}
	assertRouteStreamRuntimeGracefullyRevoked(t, manager, targetRuntime, 1)

	unrelatedNewSocket := dialRouteStreamWebSocket(t, unrelatedURL)
	t.Cleanup(func() { _ = unrelatedNewSocket.Close() })
	assertRouteStreamRuntimeUnchanged(t, manager, unrelatedRuntime, 2)
	setRouteStreamWebSocketDeadline(t, unrelatedNewSocket)
	assertRouteStreamWebSocketEchoAndNormalClose(t, unrelatedNewSocket, "unrelated-new-admission")
	assertRouteStreamRuntimeUnchanged(t, manager, unrelatedRuntime, 1)
	setRouteStreamWebSocketDeadline(t, unrelatedSocket)
	assertRouteStreamWebSocketEchoAndNormalClose(t, unrelatedSocket, "unrelated-existing-admission")
	waitExactRouteDrain(t, manager, unrelatedRuntime)

	setRouteStreamWebSocketDeadline(t, targetSocket)
	assertRouteStreamWebSocketEchoAndNormalClose(t, targetSocket, "target-graceful-drain")
	waitExactRouteDrain(t, manager, targetRuntime)
	assertRouteStreamRuntimeUnchanged(t, manager, unrelatedRuntime, 0)
}

func routeStreamPluginRouteSet(
	extension extensions.Extension,
	runtime extensionsruntime.RuntimeInstanceSnapshot,
) routes.PluginRouteSet {
	return routes.PluginRouteSet{
		Artifact: routes.PluginArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
			RuntimeInstanceID: runtime.Identity.InstanceID,
		},
		Routes: extension.Manifest.Routes,
	}
}

func startRouteStreamWebSocketServer(
	t *testing.T,
	registry *routes.Registry,
	manager *extensionsruntime.Manager,
) string {
	t.Helper()
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(manager),
		Guard: HostRouteGuardAuthorizer{},
	})
	app := fiber.New(fiber.Config{StreamRequestBody: true, DisablePreParseMultipartForm: true})
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- app.Listener(listener) }()
	t.Cleanup(func() {
		_ = app.ShutdownWithTimeout(5 * time.Second)
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("timed out waiting for WebSocket test server shutdown")
		}
	})
	return "ws://" + listener.Addr().String()
}

func unrelatedRouteStreamE2EExtension(t *testing.T) extensions.Extension {
	t.Helper()
	extension := routeStreamE2EExtension(t)
	extension.ID = "runtime.unrelated"
	extension.Name = "Unrelated Runtime Stream"
	extension.PackageDigest = strings.Repeat("b", 64)
	extension.Manifest.ID = extension.ID
	var websocketRoute extensions.ManifestRoute
	for _, route := range extension.Manifest.Routes {
		if route.ID == "runtime.stream.websocket" {
			websocketRoute = route
			break
		}
	}
	if websocketRoute.ID == "" {
		t.Fatal("shared route-stream fixture lost its WebSocket route")
	}
	websocketRoute.ID = routeStreamUnrelatedRouteID
	websocketRoute.ContractVersion = routeStreamUnrelatedRouteID + "@1"
	websocketRoute.Path = "/unrelated-socket"
	websocketRoute.ResponseSchema = routeStreamUnrelatedRouteID + ".response@1"
	extension.Manifest.Routes = []extensions.ManifestRoute{websocketRoute}
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_HELPER=" + routeStreamUnrelatedHelperEnv + " exec " +
		routeStreamShellQuote(os.Args[0]) + " -test.run=TestRouteStreamUnrelatedHTTPHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(extension.PackagePath, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	return extension
}

func TestRouteStreamUnrelatedHTTPHelperProcess(t *testing.T) {
	if os.Getenv("SFORUM_PLUGIN_HELPER") != routeStreamUnrelatedHelperEnv {
		return
	}
	server := pluginv2sdk.NewServer().
		WithFeatures(&protocolwire.ProtocolFeature{Name: "stream.routes", Version: "1"}).
		WithRuntimeStreams(pluginv2sdk.RuntimeStreams{Route: routeStreamUnrelatedE2EHandler})
	pluginv2sdk.Serve(&routeStreamUnrelatedE2EServer{Server: server})
	os.Exit(0)
}

type routeStreamUnrelatedE2EServer struct{ *pluginv2sdk.Server }

func (s *routeStreamUnrelatedE2EServer) InvokeRoute(
	_ context.Context,
	request *pluginwire.RouteRequest,
) (*pluginwire.RouteResponse, error) {
	if request.GetRouteId() != routeStreamUnrelatedRouteID {
		return &pluginwire.RouteResponse{
			Context: routeStreamE2EResponseContext(request.GetContext()),
			Error:   &protocolwire.ErrorDetail{Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "route.not_found"},
		}, nil
	}
	if header := routeStreamE2EForwardedCredential(request.GetHeaders()); header != "" {
		return nil, fmt.Errorf("filtered unrelated WebSocket preflight forwarded credential %s", header)
	}
	return &pluginwire.RouteResponse{
		Context: routeStreamE2EResponseContext(request.GetContext()), StatusCode: stdhttp.StatusSwitchingProtocols,
		Headers:       []*protocolwire.Header{{Name: "Sec-WebSocket-Protocol", Values: []string{"sforum.stream.v1"}}},
		StreamFollows: true,
	}, nil
}

func routeStreamUnrelatedE2EHandler(stream *pluginv2sdk.RouteStream) error {
	if stream.Open().GetRouteId() != routeStreamUnrelatedRouteID {
		return fmt.Errorf("unknown unrelated stream route")
	}
	if header := routeStreamE2EForwardedCredential(stream.Open().GetHeaders()); header != "" {
		return fmt.Errorf("filtered unrelated WebSocket open forwarded credential %s", header)
	}
	chunk, err := stream.Recv()
	if err != nil {
		return err
	}
	if err := stream.Send(&protocolwire.DataChunk{Sequence: 1, Data: chunk.GetData()}); err != nil {
		return err
	}
	return stream.Close(&pluginwire.RouteStreamClose{StatusCode: stdhttp.StatusSwitchingProtocols})
}

func requireRouteStreamWebSocketHTTPRejected(t *testing.T, target string) int {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: []string{"sforum.stream.v1"}}
	connection, response, err := dialer.Dial(target, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if connection != nil {
		_ = connection.Close()
	}
	if err == nil {
		t.Fatal("new WebSocket admission unexpectedly succeeded")
	}
	if response == nil || response.StatusCode < stdhttp.StatusBadRequest {
		t.Fatalf("WebSocket rejection did not reach HTTP ingress: response=%v err=%v", response, err)
	}
	return response.StatusCode
}

func setRouteStreamWebSocketDeadline(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	if err := connection.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := connection.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
}

func assertRouteStreamRuntimeUnchanged(
	t *testing.T,
	manager *extensionsruntime.Manager,
	runtime extensionsruntime.RuntimeInstanceSnapshot,
	active int,
) {
	t.Helper()
	snapshot, err := manager.InspectRuntimeInstance(runtime.Identity)
	if err != nil || !snapshot.Active || snapshot.Identity != runtime.Identity ||
		snapshot.ExtensionVersion != runtime.ExtensionVersion || snapshot.ArtifactDigest != runtime.ArtifactDigest ||
		snapshot.Admission.Identity != runtime.Identity ||
		snapshot.Admission.Draining || snapshot.Admission.Quarantined || snapshot.Admission.Forced ||
		snapshot.Admission.ActiveTotal != active ||
		snapshot.Admission.ActiveByClass[extensionsruntime.RuntimeCallRoute] != active {
		t.Fatalf("runtime changed=%#v err=%v", snapshot, err)
	}
}

func assertRouteStreamRuntimeGracefullyRevoked(
	t *testing.T,
	manager *extensionsruntime.Manager,
	runtime extensionsruntime.RuntimeInstanceSnapshot,
	active int,
) {
	t.Helper()
	snapshot, err := manager.InspectRuntimeInstance(runtime.Identity)
	if err != nil || !snapshot.Active || snapshot.Identity != runtime.Identity ||
		snapshot.ExtensionVersion != runtime.ExtensionVersion || snapshot.ArtifactDigest != runtime.ArtifactDigest ||
		snapshot.Admission.Identity != runtime.Identity || !snapshot.Admission.Draining ||
		!snapshot.Admission.Quarantined || snapshot.Admission.Forced || snapshot.Admission.ForceCause != nil ||
		snapshot.Admission.ActiveTotal != active ||
		snapshot.Admission.ActiveByClass[extensionsruntime.RuntimeCallRoute] != active {
		t.Fatalf("runtime was not gracefully revoked=%#v err=%v", snapshot, err)
	}
}
