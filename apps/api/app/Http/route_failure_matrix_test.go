package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestP6RouteProtocolV2CrashAndTimeoutMatrix(t *testing.T) {
	tests := []struct {
		name      string
		timeoutMS int
		invoke    func(context.Context) error
	}{
		{
			name: "runtime crash after receiving request",
			invoke: func(context.Context) error {
				return errors.New("plugin runtime crashed")
			},
		},
		{
			name:      "runtime exceeds exact route deadline",
			timeoutMS: 10,
			invoke: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, core, runtime, traces := newP6RouteV2FailureApp(t, test.timeoutMS, test.invoke)
			response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/p6-v2-failure", nil))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()

			if response.StatusCode != stdhttp.StatusBadGateway || core.calls.Load() != 0 || runtime.calls.Load() != 1 {
				t.Fatalf("status=%d core=%d runtime=%d", response.StatusCode, core.calls.Load(), runtime.calls.Load())
			}
			if runtime.activeAtInvoke.Load() != 1 || runtime.gate.Snapshot().ActiveTotal != 0 {
				t.Fatalf("active at invoke=%d final admission=%#v", runtime.activeAtInvoke.Load(), runtime.gate.Snapshot())
			}
			if test.timeoutMS > 0 && !runtime.deadlineObserved.Load() {
				t.Fatal("Protocol V2 runtime did not observe the route deadline")
			}
			assertRouteFenceTrace(t, traces, routes.RouteCommitSideEffectStarted, routes.RouteTraceTransportFailed)
		})
	}
}

type p6RouteV2FailureRuntime struct {
	snapshot         extensionsruntime.RuntimeInstanceSnapshot
	gate             *extensionsruntime.RuntimeAdmissionGate
	invoke           func(context.Context) error
	calls            atomic.Int64
	activeAtInvoke   atomic.Int64
	deadlineObserved atomic.Bool
}

func (r *p6RouteV2FailureRuntime) InspectRuntimeInstance(
	identity extensionsruntime.RuntimeInstanceIdentity,
) (extensionsruntime.RuntimeInstanceSnapshot, error) {
	if identity != r.snapshot.Identity {
		return extensionsruntime.RuntimeInstanceSnapshot{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	return r.snapshot, nil
}

func (r *p6RouteV2FailureRuntime) AcquireRuntimeCall(
	ctx context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	class extensionsruntime.RuntimeCallClass,
) (*extensionsruntime.RuntimeAdmissionLease, error) {
	if identity != r.snapshot.Identity || class != extensionsruntime.RuntimeCallRoute {
		return nil, extensionsruntime.ErrRuntimeInstanceNotActive
	}
	return r.gate.Acquire(ctx, class)
}

func (r *p6RouteV2FailureRuntime) InvokeRouteInstance(
	ctx context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	_ extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if identity != r.snapshot.Identity {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	r.calls.Add(1)
	r.activeAtInvoke.Store(int64(r.gate.Snapshot().ActiveTotal))
	err := r.invoke(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		r.deadlineObserved.Store(true)
	}
	return extensionsruntime.ProtocolV2RouteResponse{}, err
}

type p6RouteV2FailureCoreProvider struct {
	calls atomic.Int64
}

func (p *p6RouteV2FailureCoreProvider) RegisterRoutes(api fiber.Router) {
	api.Get("/p6-v2-failure", func(c fiber.Ctx) error {
		p.calls.Add(1)
		return c.SendString("core")
	})
}

func newP6RouteV2FailureApp(
	t *testing.T,
	timeoutMS int,
	invoke func(context.Context) error,
) (*fiber.App, *p6RouteV2FailureCoreProvider, *p6RouteV2FailureRuntime, *routes.RouteTraceRing) {
	t.Helper()
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("p6.v2.failure", 'f')
	replacement := routeDispatcherManifestRoute(
		"p6.v2.failure.replace", extensionmanifest.RouteActionReplace, "/api/v1/p6-v2-failure", stdhttp.MethodGet,
	)
	replacement.TargetID = "core.route.test.p6.v2.failure"
	replacement.Fallback = "readonly_core"
	replacement.TimeoutMS = timeoutMS
	if _, err := registry.Publish(routes.Publication{
		Core: []routes.CoreRoute{{
			ID: replacement.TargetID, ContractVersion: "sforum.route.test.p6.v2.failure@1",
			Method: stdhttp.MethodGet, Path: replacement.Path,
		}},
		Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	}); err != nil {
		t.Fatal(err)
	}

	pathSignature := ""
	for _, declaration := range registry.Snapshot().Routes {
		if declaration.ID == replacement.ID {
			pathSignature = declaration.PathSignature
			break
		}
	}
	if pathSignature == "" {
		t.Fatal("replacement path signature was not published")
	}
	selection := routes.NewProviderSelectionAPI(registry, &routeSelectionMemoryStore{})
	if _, err := selection.Select(t.Context(), routes.SelectProviderRequest{
		Key: routes.ProviderSelectionKey{
			TargetRouteID: replacement.TargetID, TargetContractVersion: "sforum.route.test.p6.v2.failure@1",
			Method: stdhttp.MethodGet, PathSignature: pathSignature,
		},
		ProviderRouteID: replacement.ID, ProviderContractVersion: replacement.ContractVersion,
		ProviderArtifact: artifact, ActorUserID: 1, AuditEventID: 1,
	}); err != nil {
		t.Fatal(err)
	}

	identity := extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID,
	}
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &p6RouteV2FailureRuntime{
		gate: gate, invoke: invoke,
		snapshot: extensionsruntime.RuntimeInstanceSnapshot{
			Identity: identity, ExtensionVersion: artifact.ExtensionVersion, ArtifactDigest: artifact.PackageDigest,
			Target: extensionsruntime.RouteTarget{InstanceID: identity.InstanceID}, Active: true,
		},
	}
	traces := routes.NewRouteTraceRing(4)
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: selection, Steps: NewBufferedRouteStepInvoker(runtime), Guard: HostRouteGuardAuthorizer{},
		Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}}, Trace: traces,
	})
	core := &p6RouteV2FailureCoreProvider{}
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{
		RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{core},
	})
	return app, core, runtime, traces
}

var _ ExactRouteRuntime = (*p6RouteV2FailureRuntime)(nil)
var _ exactRouteV2Runtime = (*p6RouteV2FailureRuntime)(nil)
