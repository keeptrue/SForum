package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"testing"

	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteV2StreamCancellationBeforeRemoteCallStaysPristine(t *testing.T) {
	dispatcher, runtime, traces := routeV2PreflightEvidenceFixture(t)
	prepared, err := dispatcher.PrepareStream(
		context.Background(), routes.DispatchRequest{Method: stdhttp.MethodGet, Path: "/stream-v2-evidence"},
	)
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v error=%v", prepared, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = prepared.Dispatch.Open(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, routes.ErrDispatchTransport) ||
		runtime.calls != 0 || runtime.streamOpenCalls != 0 || len(traces.RouteTraces(0)) != 0 {
		t.Fatalf("error=%v routeCalls=%d streamCalls=%d traces=%#v",
			err, runtime.calls, runtime.streamOpenCalls, traces.RouteTraces(0))
	}
}

func TestRouteV2StreamPreflightFailureMarksObservedExecution(t *testing.T) {
	dispatcher, runtime, traces := routeV2PreflightEvidenceFixture(t)
	runtime.err = errors.New("runtime crashed after preflight dispatch")
	prepared, err := dispatcher.PrepareStream(
		context.Background(), routes.DispatchRequest{Method: stdhttp.MethodGet, Path: "/stream-v2-evidence"},
	)
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v error=%v", prepared, err)
	}

	_, err = prepared.Dispatch.Open(context.Background())
	records := traces.RouteTraces(0)
	if !errors.Is(err, routes.ErrDispatchTransport) || runtime.calls != 1 || runtime.streamOpenCalls != 0 ||
		len(records) != 1 || records[0].Outcome != routes.RouteTraceTransportFailed ||
		records[0].CommitState != routes.RouteCommitSideEffectStarted {
		t.Fatalf("error=%v routeCalls=%d streamCalls=%d traces=%#v", err, runtime.calls, runtime.streamOpenCalls, records)
	}
}

func routeV2PreflightEvidenceFixture(
	t *testing.T,
) (*routes.Dispatcher, *routeV2PreflightEvidenceRuntime, *routes.RouteTraceRing) {
	t.Helper()
	artifact := routeDispatcherArtifact("stream-v2-evidence", 'f')
	declaration := routeDispatcherManifestRoute(
		"stream-v2-evidence.route", extensionmanifest.RouteActionAdd, "/stream-v2-evidence", stdhttp.MethodGet,
	)
	declaration.Mode = extensionmanifest.RouteModeWebSocket
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	runtime := &routeV2PreflightEvidenceRuntime{
		routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, artifact),
	}
	runtime.response = extensionsruntime.ProtocolV2RouteResponse{
		StatusCode: fiber.StatusSwitchingProtocols, StreamFollows: true,
	}
	traces := routes.NewRouteTraceRing(8)
	return routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: HostRouteGuardAuthorizer{}, Trace: traces,
	}), runtime, traces
}

type routeV2PreflightEvidenceRuntime struct {
	*routeDispatcherV2Runtime
	streamOpenCalls int
}

func (r *routeV2PreflightEvidenceRuntime) OpenRouteStreamInstance(
	context.Context,
	extensionsruntime.RuntimeInstanceIdentity,
	extensionsruntime.ProtocolV2RouteStreamRequest,
) (*extensionsruntime.ProtocolV2RouteStream, error) {
	r.streamOpenCalls++
	return nil, errors.New("stream open is not expected")
}
