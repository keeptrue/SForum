package http

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestRouteDispatchRequestCapturesHostCredentialSource(t *testing.T) {
	tests := []struct {
		name   string
		actor  identity.Actor
		bearer bool
		want   routes.DispatchCredentialSource
	}{
		{name: "anonymous"},
		{name: "cookie", actor: identity.Actor{ID: 7, Status: identity.UserStatusActive}, want: routes.DispatchCredentialCookie},
		{name: "bearer", actor: identity.Actor{ID: 7, Status: identity.UserStatusActive}, bearer: true, want: routes.DispatchCredentialBearer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := fiber.New()
			var captured routes.DispatchRequest
			app.Get("/", func(c fiber.Ctx) error {
				if test.bearer {
					c.SetContext(apitokens.WithAuth(c.Context(), apitokens.Authenticated{UserID: 7, TokenID: 11}))
				}
				captured = routeDispatchRequest(c, test.actor)
				return c.SendStatus(fiber.StatusNoContent)
			})
			response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/", nil))
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if captured.CredentialSource != test.want {
				t.Fatalf("credential source = %q, want %q", captured.CredentialSource, test.want)
			}
		})
	}
}

func TestRouteDispatcherMiddlewareLeavesCoreOnlyStreamUncapturedAndCallsNextOnce(t *testing.T) {
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Core: []routes.CoreRoute{{
		ID: "core.route.test.stream", ContractVersion: "sforum.route.test.stream@1",
		Method: "GET", Path: "/api/v1/core-stream",
	}}}); err != nil {
		t.Fatal(err)
	}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{Plans: routeRegistryPlanResolver{registry: registry}})
	provider := &routeDispatcherStreamProvider{}
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{
		RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{provider},
	})

	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/core-stream", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusOK || string(body) != "chunk-1chunk-2" ||
		provider.handlerCalls.Load() != 1 || provider.writerCalls.Load() != 1 {
		t.Fatalf("status=%d body=%q handler=%d writer=%d", response.StatusCode, body, provider.handlerCalls.Load(), provider.writerCalls.Load())
	}
}

func TestRouteDispatcherMiddlewareRunsBeforeCoreAndLetsUnknownRoutesContinue(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("middleware.demo", 'a')
	before := routeDispatcherManifestRoute("middleware.demo.before", extensionmanifest.RouteActionBefore, "/api/v1/core", "GET")
	before.TargetID = "core.route.test.core"
	before.ResponseSchema = ""
	if _, err := registry.Publish(routes.Publication{
		Core:    []routes.CoreRoute{{ID: "core.route.test.core", ContractVersion: "sforum.route.test.core@1", Method: "GET", Path: "/api/v1/core"}},
		Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{before}}},
	}); err != nil {
		t.Fatal(err)
	}
	runtime := newRouteDispatcherV2RuntimeForArtifact(t, artifact)
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: HostRouteGuardAuthorizer{}, Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	provider := &routeDispatcherTestProvider{}
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{
		RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{provider},
	})

	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/core", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusOK || string(body) != "core" || provider.coreCalls != 1 {
		t.Fatalf("status=%d body=%q coreCalls=%d", response.StatusCode, body, provider.coreCalls)
	}
	if runtime.calls != 1 || runtime.request.RouteID != before.ID ||
		runtime.request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageRequest {
		t.Fatalf("runtime calls=%d request=%#v", runtime.calls, runtime.request)
	}

	response, err = app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/untracked", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusOK || string(body) != "untracked" || provider.unknownCalls != 1 {
		t.Fatalf("status=%d body=%q unknownCalls=%d", response.StatusCode, body, provider.unknownCalls)
	}
}

func TestRouteDispatcherMiddlewareExecutesSelectedExactReplacementWithoutCoreWriter(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("replace.demo", 'b')
	replacement := routeDispatcherManifestRoute("replace.demo.writer", extensionmanifest.RouteActionReplace, "/api/v1/replace", "GET")
	replacement.TargetID = "core.route.test.replace"
	replacement.Fallback = "readonly_core"
	if _, err := registry.Publish(routes.Publication{
		Core:    []routes.CoreRoute{{ID: "core.route.test.replace", ContractVersion: "sforum.route.test.replace@1", Method: "GET", Path: "/api/v1/replace"}},
		Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	}); err != nil {
		t.Fatal(err)
	}
	store := &routeSelectionMemoryStore{}
	selectionAPI := routes.NewProviderSelectionAPI(registry, store)
	pathSignature := ""
	for _, route := range registry.Snapshot().Routes {
		if route.ID == replacement.ID {
			pathSignature = route.PathSignature
		}
	}
	key := routes.ProviderSelectionKey{
		TargetRouteID: "core.route.test.replace", TargetContractVersion: "sforum.route.test.replace@1",
		Method: "GET", PathSignature: pathSignature,
	}
	if _, err := selectionAPI.Select(context.Background(), routes.SelectProviderRequest{
		Key: key, ProviderRouteID: replacement.ID, ProviderContractVersion: replacement.ContractVersion,
		ProviderArtifact: artifact, ActorUserID: 1, AuditEventID: 2,
	}); err != nil {
		t.Fatal(err)
	}
	runtime, target := newRouteDispatcherRuntime(t, artifact)
	target.Config.Handler = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
		if request.Header.Get("X-SForum-Extension-ID") != artifact.ExtensionID ||
			request.Header.Get("X-SForum-Route-ID") != replacement.ID ||
			request.Header.Get("X-SForum-Route-Handler") != replacement.Handler ||
			request.Header.Get("X-SForum-Actor-ID") != "42" ||
			request.Header.Get("X-SForum-Route-Phase") != string(routes.RoutePhaseHandler) {
			t.Errorf("route headers=%#v", request.Header)
		}
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" ||
			request.Header.Get("X-SForum-Forged") != "" || request.Header.Get("X-Trace-ID") != "trace-1" {
			t.Errorf("authority headers leaked or ordinary header missing: %#v", request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Set-Cookie", "sforum_session=forged")
		writer.Header().Set("X-SForum-Forged", "runtime")
		_, _ = writer.Write([]byte(`{"provider":"plugin"}`))
	})
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: selectionAPI, Steps: NewBufferedRouteStepInvoker(runtime), Guard: HostRouteGuardAuthorizer{},
		Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	provider := &routeReplacementCoreProvider{}
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{
		RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{provider},
		RouteActors: func(fiber.Ctx) (identity.Actor, error) {
			return identity.Actor{ID: 42, Status: identity.UserStatusActive}, nil
		},
	})

	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/replace", nil)
	request.Header.Set("Authorization", "Bearer forged")
	request.Header.Set("Cookie", "session=forged")
	request.Header.Set("X-SForum-Extension-ID", "forged.extension")
	request.Header.Set("X-SForum-Route-ID", "forged.route")
	request.Header.Set("X-SForum-Route-Handler", "forged.handler")
	request.Header.Set("X-SForum-Actor-ID", "999")
	request.Header.Set("X-SForum-Forged", "yes")
	request.Header.Set("X-Trace-ID", "trace-1")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusOK || string(body) != `{"provider":"plugin"}` || provider.calls != 0 ||
		response.Header.Get("Set-Cookie") != "" || response.Header.Get("X-SForum-Forged") != "" {
		t.Fatalf("status=%d body=%q core calls=%d", response.StatusCode, body, provider.calls)
	}
}

func TestRouteDispatcherHostFenceControlsCoreFallbackFromObservedTransport(t *testing.T) {
	t.Run("pre-write dial failure may use readonly core", func(t *testing.T) {
		app, provider, ring, server := newRouteFenceApp(t, stdhttp.MethodGet, 0, stdhttp.HandlerFunc(func(stdhttp.ResponseWriter, *stdhttp.Request) {}))
		server.Close()
		response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/fence", nil))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != stdhttp.StatusOK || string(body) != "core" || provider.calls.Load() != 1 {
			t.Fatalf("status=%d body=%q core calls=%d", response.StatusCode, body, provider.calls.Load())
		}
		assertRouteFenceTrace(t, ring, routes.RouteCommitFinal,
			routes.RouteTraceTransportFailed, routes.RouteTraceFallbackUsed, routes.RouteTraceCommitted)
	})

	t.Run("accepted GET crash cannot become a second writer", func(t *testing.T) {
		app, provider, ring, _ := newRouteFenceApp(t, stdhttp.MethodGet, 0, stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
			closeRouteRuntimeConnection(t, writer)
		}))
		response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/fence", nil))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != stdhttp.StatusBadGateway || provider.calls.Load() != 0 {
			t.Fatalf("status=%d core calls=%d", response.StatusCode, provider.calls.Load())
		}
		assertRouteFenceTrace(t, ring, routes.RouteCommitSideEffectStarted, routes.RouteTraceTransportFailed)
	})

	t.Run("partial response cannot become a second writer", func(t *testing.T) {
		app, provider, ring, _ := newRouteFenceApp(t, stdhttp.MethodGet, 0, stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
			writer.Header().Set("Content-Length", "64")
			writer.WriteHeader(stdhttp.StatusOK)
			_, _ = writer.Write([]byte("partial"))
			writer.(stdhttp.Flusher).Flush()
			closeRouteRuntimeConnection(t, writer)
		}))
		response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/fence", nil))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != stdhttp.StatusBadGateway || provider.calls.Load() != 0 {
			t.Fatalf("status=%d core calls=%d", response.StatusCode, provider.calls.Load())
		}
		assertRouteFenceTrace(t, ring, routes.RouteCommitResponseStarted, routes.RouteTraceTransportFailed)
	})

	t.Run("unsafe accepted request always fails closed", func(t *testing.T) {
		app, provider, ring, _ := newRouteFenceApp(t, stdhttp.MethodPost, 0, stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
			_, _ = io.ReadAll(request.Body)
			closeRouteRuntimeConnection(t, writer)
		}))
		request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/fence", strings.NewReader("mutation"))
		request.Header.Set("Content-Type", "application/json")
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != stdhttp.StatusBadGateway || provider.calls.Load() != 0 {
			t.Fatalf("status=%d core calls=%d", response.StatusCode, provider.calls.Load())
		}
		assertRouteFenceTrace(t, ring, routes.RouteCommitSideEffectStarted, routes.RouteTraceTransportFailed)
	})

	t.Run("timeout after acceptance cannot become a second writer", func(t *testing.T) {
		app, provider, ring, _ := newRouteFenceApp(t, stdhttp.MethodGet, 30, stdhttp.HandlerFunc(func(_ stdhttp.ResponseWriter, request *stdhttp.Request) {
			<-request.Context().Done()
		}))
		response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/fence", nil))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != stdhttp.StatusBadGateway || provider.calls.Load() != 0 {
			t.Fatalf("status=%d core calls=%d", response.StatusCode, provider.calls.Load())
		}
		assertRouteFenceTrace(t, ring, routes.RouteCommitSideEffectStarted, routes.RouteTraceTransportFailed)
	})
}

func TestBufferedRouteStepInvokerRejectsStaleArtifactAndNonLoopback(t *testing.T) {
	artifact := routeDispatcherArtifact("runtime.demo", 'c')
	runtime, server := newRouteDispatcherRuntime(t, artifact)
	declaration := routeDispatcherManifestRoute(
		"runtime.demo.route", extensionmanifest.RouteActionAdd, "/api/v1/demo", stdhttp.MethodGet,
	)
	input := captureAuthorizedRouteInvocation(t, artifact, declaration, routes.DispatchRequest{
		Method: stdhttp.MethodGet, Path: "/api/v1/demo",
	})
	invoker := NewBufferedRouteStepInvoker(runtime)

	tampered := input
	tampered.Step.Provider.Artifact.PackageDigest = strings.Repeat("d", 64)
	if _, err := invoker.Invoke(context.Background(), tampered); !errors.Is(err, ErrRouteRuntimeTarget) {
		t.Fatalf("tampered invocation err=%v", err)
	}
	runtime.snapshot.ArtifactDigest = strings.Repeat("d", 64)
	if _, err := invoker.Invoke(context.Background(), input); !errors.Is(err, ErrRouteRuntimeArtifact) {
		t.Fatalf("stale runtime err=%v", err)
	}
	runtime.snapshot.ArtifactDigest = artifact.PackageDigest
	runtime.snapshot.Target.BaseURL = "https://example.com"
	if _, err := invoker.Invoke(context.Background(), input); !errors.Is(err, ErrRouteRuntimeTarget) {
		t.Fatalf("target err=%v", err)
	}
	server.Close()
}

func TestBufferedRouteStepInvokerUsesHostObservedCommitEvidence(t *testing.T) {
	artifact := routeDispatcherArtifact("evidence.demo", 'f')

	t.Run("successful exchange ignores plugin false header", func(t *testing.T) {
		runtime, server := newRouteDispatcherRuntime(t, artifact)
		server.Config.Handler = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
			writer.Header().Set("X-SForum-Side-Effect-Started", "false")
			_, _ = writer.Write([]byte("ok"))
		})
		input := capturePublicHTTPRouteInvocation(t, artifact, "evidence.demo.route", "/evidence")
		observer := input.Commit
		result, err := NewBufferedRouteStepInvoker(runtime).Invoke(context.Background(), input)
		if err != nil || !result.SideEffectStarted || !result.ResponseStarted || observer.State() != routes.RouteCommitResponseStarted {
			t.Fatalf("result=%#v state=%q err=%v", result, observer.State(), err)
		}
	})

	t.Run("dial failure stays pristine", func(t *testing.T) {
		runtime, server := newRouteDispatcherRuntime(t, artifact)
		server.Close()
		input := capturePublicHTTPRouteInvocation(t, artifact, "evidence.demo.route", "/evidence")
		observer := input.Commit
		result, err := NewBufferedRouteStepInvoker(runtime).Invoke(context.Background(), input)
		if err == nil || result.SideEffectStarted || result.ResponseStarted || observer.State() != routes.RouteCommitPristine {
			t.Fatalf("result=%#v state=%q err=%v", result, observer.State(), err)
		}
	})
}

func TestBufferedRouteStepInvokerFencesCrashPartialResponseAndCancellation(t *testing.T) {
	artifact := routeDispatcherArtifact("transport.fence", 'd')

	t.Run("runtime accepts request then crashes", func(t *testing.T) {
		runtime, server := newRouteDispatcherRuntime(t, artifact)
		server.Config.Handler = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
			closeRouteRuntimeConnection(t, writer)
		})
		input := capturePublicHTTPRouteInvocation(t, artifact, "transport.fence.route", "/crash")
		observer := input.Commit
		result, err := NewBufferedRouteStepInvoker(runtime).Invoke(context.Background(), input)
		if err == nil || !result.SideEffectStarted || result.ResponseStarted || observer.State() != routes.RouteCommitSideEffectStarted {
			t.Fatalf("result=%#v state=%q err=%v", result, observer.State(), err)
		}
	})

	t.Run("response headers and body begin before disconnect", func(t *testing.T) {
		runtime, server := newRouteDispatcherRuntime(t, artifact)
		server.Config.Handler = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
			writer.Header().Set("Content-Length", "64")
			writer.WriteHeader(stdhttp.StatusOK)
			_, _ = writer.Write([]byte("partial"))
			writer.(stdhttp.Flusher).Flush()
			closeRouteRuntimeConnection(t, writer)
		})
		input := capturePublicHTTPRouteInvocation(t, artifact, "transport.fence.route", "/partial")
		observer := input.Commit
		result, err := NewBufferedRouteStepInvoker(runtime).Invoke(context.Background(), input)
		if err == nil || !result.SideEffectStarted || !result.ResponseStarted || observer.State() != routes.RouteCommitResponseStarted {
			t.Fatalf("result=%#v state=%q err=%v", result, observer.State(), err)
		}
	})

	t.Run("caller cancellation after request acceptance", func(t *testing.T) {
		runtime, server := newRouteDispatcherRuntime(t, artifact)
		accepted := make(chan struct{})
		server.Config.Handler = stdhttp.HandlerFunc(func(_ stdhttp.ResponseWriter, request *stdhttp.Request) {
			close(accepted)
			<-request.Context().Done()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		input := capturePublicHTTPRouteInvocation(t, artifact, "transport.fence.route", "/cancel")
		observer := input.Commit
		result, err := NewBufferedRouteStepInvoker(runtime).Invoke(ctx, input)
		if err == nil || !result.SideEffectStarted || result.ResponseStarted || observer.State() != routes.RouteCommitSideEffectStarted {
			t.Fatalf("result=%#v state=%q err=%v", result, observer.State(), err)
		}
		select {
		case <-accepted:
		default:
			t.Fatal("runtime never accepted the cancelled request")
		}
	})
}

func TestRouteDispatcherDeclaredNonHTTPModeFailsClosedBeforeRuntime(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.demo", 'e')
	declaration := routeDispatcherManifestRoute("stream.demo.events", extensionmanifest.RouteActionAdd, "/api/v1/plugin-events", "GET")
	declaration.Mode = extensionmanifest.RouteModeSSE
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	runtime, server := newRouteDispatcherRuntime(t, artifact)
	var calls atomic.Int64
	server.Config.Handler = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
		calls.Add(1)
		writer.WriteHeader(stdhttp.StatusOK)
	})
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: HostRouteGuardAuthorizer{}, Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{RouteDispatcher: dispatcher})

	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/plugin-events", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != stdhttp.StatusBadGateway || calls.Load() != 0 {
		t.Fatalf("status=%d runtime calls=%d", response.StatusCode, calls.Load())
	}
}

func TestHostRouteGuardAuthorizerPublicLoginGuestPermissionAndClosedAuthorities(t *testing.T) {
	authorizer := HostRouteGuardAuthorizer{}
	request := routes.DispatchRequest{Authenticated: true, Permissions: map[string]bool{"topic.create": true}}
	tests := []struct {
		name    string
		guard   string
		request routes.DispatchRequest
		want    error
	}{
		{name: "public", guard: extensionmanifest.GuardCorePublic, want: nil},
		{name: "login", guard: extensionmanifest.GuardCoreLogin, request: request, want: nil},
		{name: "login denied", guard: extensionmanifest.GuardCoreLogin, want: ErrRouteLoginRequired},
		{name: "guest", guard: extensionmanifest.GuardCoreGuest, want: nil},
		{name: "guest denied", guard: extensionmanifest.GuardCoreGuest, request: request, want: ErrRouteGuestRequired},
		{name: "permission", guard: extensionmanifest.GuardCorePermission, request: request, want: nil},
		{name: "permission denied", guard: extensionmanifest.GuardCorePermission, request: routes.DispatchRequest{Authenticated: true}, want: ErrRoutePermissionDenied},
		{name: "inherit closed", guard: extensionmanifest.GuardCoreInherit, request: request, want: ErrRouteGuardUnavailable},
		{name: "raw closed", guard: extensionmanifest.GuardCoreRaw, request: request, want: ErrRouteGuardUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step := routes.RouteExecutionStep{Guard: test.guard, Permission: "topic.create"}
			err := authorizer.Authorize(context.Background(), routes.RouteExecutionPlan{}, step, test.request)
			if !errors.Is(err, test.want) || test.want == nil && err != nil {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
}

type routeRegistryPlanResolver struct{ registry *routes.Registry }

func (r routeRegistryPlanResolver) BuildExecutionPlan(_ context.Context, method, path string) (routes.RouteExecutionPlan, error) {
	return r.registry.BuildExecutionPlan(method, path)
}

var errRouteInvocationCaptured = errors.New("route invocation captured")

type routeInvocationCapture struct {
	invocation routes.RouteInvocation
	calls      int
}

func (c *routeInvocationCapture) SupportsMode(string) bool { return true }

func (c *routeInvocationCapture) Invoke(
	_ context.Context,
	invocation routes.RouteInvocation,
) (routes.RouteInvocationResult, error) {
	c.calls++
	c.invocation = invocation
	return routes.RouteInvocationResult{}, errRouteInvocationCaptured
}

func captureAuthorizedRouteInvocation(
	t *testing.T,
	artifact routes.PluginArtifact,
	declaration extensionmanifest.ManifestRoute,
	request routes.DispatchRequest,
) routes.RouteInvocation {
	t.Helper()
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	capture := &routeInvocationCapture{}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: capture,
		Guard: NewProductionRouteGuardAuthorizer(), Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	if _, err := dispatcher.Dispatch(context.Background(), request, nil); !errors.Is(err, errRouteInvocationCaptured) {
		t.Fatalf("capture route invocation: %v", err)
	}
	if capture.calls != 1 || capture.invocation.Commit == nil {
		t.Fatalf("captured route invocation calls=%d invocation=%#v", capture.calls, capture.invocation)
	}
	if _, ok := capture.invocation.RequestAuthority(); !ok {
		t.Fatal("dispatcher did not bind request authority to the captured invocation")
	}
	return capture.invocation
}

func capturePublicHTTPRouteInvocation(
	t *testing.T,
	artifact routes.PluginArtifact,
	routeID string,
	path string,
) routes.RouteInvocation {
	t.Helper()
	declaration := routeDispatcherManifestRoute(routeID, extensionmanifest.RouteActionAdd, path, stdhttp.MethodGet)
	return captureAuthorizedRouteInvocation(t, artifact, declaration, routes.DispatchRequest{
		Method: stdhttp.MethodGet, Path: path,
	})
}

type acceptRouteSchemaCatalog struct{}

func (acceptRouteSchemaCatalog) ValidateRouteSchema(context.Context, routes.PluginArtifact, string, string, string, string, string, string, string, string, int, []byte) error {
	return nil
}

type routeDispatcherRuntime struct {
	snapshot extensionsruntime.RuntimeInstanceSnapshot
	gate     *extensionsruntime.RuntimeAdmissionGate
}

func (r *routeDispatcherRuntime) InspectRuntimeInstance(identity extensionsruntime.RuntimeInstanceIdentity) (extensionsruntime.RuntimeInstanceSnapshot, error) {
	if identity != r.snapshot.Identity {
		return extensionsruntime.RuntimeInstanceSnapshot{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	return r.snapshot, nil
}

func (r *routeDispatcherRuntime) AcquireRuntimeCall(ctx context.Context, identity extensionsruntime.RuntimeInstanceIdentity, class extensionsruntime.RuntimeCallClass) (*extensionsruntime.RuntimeAdmissionLease, error) {
	if identity != r.snapshot.Identity || !r.snapshot.Active || class != extensionsruntime.RuntimeCallRoute {
		return nil, extensionsruntime.ErrRuntimeInstanceNotActive
	}
	return r.gate.Acquire(ctx, class)
}

func newRouteDispatcherRuntime(t *testing.T, artifact routes.PluginArtifact) (*routeDispatcherRuntime, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writer.WriteHeader(stdhttp.StatusNoContent)
	}))
	identity := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID}
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	runtime := &routeDispatcherRuntime{
		gate: gate,
		snapshot: extensionsruntime.RuntimeInstanceSnapshot{
			Identity: identity, ExtensionVersion: artifact.ExtensionVersion, ArtifactDigest: artifact.PackageDigest,
			Target: extensionsruntime.RouteTarget{BaseURL: server.URL, InstanceID: artifact.RuntimeInstanceID}, Active: true,
		},
	}
	t.Cleanup(server.Close)
	return runtime, server
}

func closeRouteRuntimeConnection(t *testing.T, writer stdhttp.ResponseWriter) {
	t.Helper()
	hijacker, ok := writer.(stdhttp.Hijacker)
	if !ok {
		t.Error("route runtime response writer cannot hijack connection")
		return
	}
	connection, _, err := hijacker.Hijack()
	if err != nil {
		t.Errorf("hijack route runtime connection: %v", err)
		return
	}
	_ = connection.Close()
}

func newRouteFenceApp(
	t *testing.T,
	method string,
	timeoutMS int,
	handler stdhttp.Handler,
) (*fiber.App, *routeFenceCoreProvider, *routes.RouteTraceRing, *httptest.Server) {
	t.Helper()
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("fence.demo", 'e')
	replacement := routeDispatcherManifestRoute("fence.demo.writer", extensionmanifest.RouteActionReplace, "/api/v1/fence", method)
	replacement.TargetID = "core.route.test.fence"
	replacement.Fallback = "readonly_core"
	replacement.TimeoutMS = timeoutMS
	if method != stdhttp.MethodGet && method != stdhttp.MethodHead {
		replacement.RequestSchema = replacement.ID + ".request@1"
	}
	if _, err := registry.Publish(routes.Publication{
		Core: []routes.CoreRoute{{
			ID: "core.route.test.fence", ContractVersion: "sforum.route.test.fence@1", Method: method, Path: "/api/v1/fence",
		}},
		Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	}); err != nil {
		t.Fatal(err)
	}
	pathSignature := ""
	for _, route := range registry.Snapshot().Routes {
		if route.ID == replacement.ID {
			pathSignature = route.PathSignature
			break
		}
	}
	selectionAPI := routes.NewProviderSelectionAPI(registry, &routeSelectionMemoryStore{})
	if _, err := selectionAPI.Select(context.Background(), routes.SelectProviderRequest{
		Key: routes.ProviderSelectionKey{
			TargetRouteID: "core.route.test.fence", TargetContractVersion: "sforum.route.test.fence@1",
			Method: method, PathSignature: pathSignature,
		},
		ProviderRouteID: replacement.ID, ProviderContractVersion: replacement.ContractVersion,
		ProviderArtifact: artifact, ActorUserID: 1, AuditEventID: 1,
	}); err != nil {
		t.Fatal(err)
	}
	runtime, server := newRouteDispatcherRuntime(t, artifact)
	server.Config.Handler = handler
	ring := routes.NewRouteTraceRing(16)
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: selectionAPI, Steps: NewBufferedRouteStepInvoker(runtime), Guard: HostRouteGuardAuthorizer{},
		Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}}, Trace: ring,
	})
	provider := &routeFenceCoreProvider{}
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{
		RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{provider},
	})
	return app, provider, ring, server
}

func assertRouteFenceTrace(t *testing.T, ring *routes.RouteTraceRing, state routes.RouteExecutionCommitState, outcomes ...routes.RouteTraceOutcome) {
	t.Helper()
	records := ring.RouteTraces(0)
	if len(records) != len(outcomes) {
		t.Fatalf("traces=%#v", records)
	}
	for index, outcome := range outcomes {
		if records[index].Outcome != outcome {
			t.Fatalf("trace[%d]=%#v", index, records[index])
		}
	}
	if records[len(records)-1].CommitState != state {
		t.Fatalf("last trace=%#v", records[len(records)-1])
	}
}

type routeDispatcherTestProvider struct {
	coreCalls    int
	unknownCalls int
}

type routeDispatcherStreamProvider struct {
	handlerCalls atomic.Int64
	writerCalls  atomic.Int64
}

func (p *routeDispatcherStreamProvider) RegisterRoutes(api fiber.Router) {
	api.Get("/core-stream", func(c fiber.Ctx) error {
		p.handlerCalls.Add(1)
		return c.SendStreamWriter(func(writer *bufio.Writer) {
			p.writerCalls.Add(1)
			_, _ = writer.WriteString("chunk-1")
			_ = writer.Flush()
			_, _ = writer.WriteString("chunk-2")
		})
	})
}

func (p *routeDispatcherTestProvider) RegisterRoutes(api fiber.Router) {
	api.Get("/core", func(c fiber.Ctx) error {
		p.coreCalls++
		return c.SendString("core")
	})
	api.Get("/untracked", func(c fiber.Ctx) error {
		p.unknownCalls++
		return c.SendString("untracked")
	})
}

type routeReplacementCoreProvider struct{ calls int }

func (p *routeReplacementCoreProvider) RegisterRoutes(api fiber.Router) {
	api.Get("/replace", func(c fiber.Ctx) error {
		p.calls++
		return c.SendString("core")
	})
}

type routeFenceCoreProvider struct{ calls atomic.Int64 }

func (p *routeFenceCoreProvider) RegisterRoutes(api fiber.Router) {
	handler := func(c fiber.Ctx) error {
		p.calls.Add(1)
		return c.SendString("core")
	}
	api.Get("/fence", handler)
	api.Post("/fence", handler)
}

type routeSelectionMemoryStore struct{ selected routes.ProviderSelection }

func (s *routeSelectionMemoryStore) Desired(ctx context.Context, key routes.ProviderSelectionKey) (routes.ProviderSelection, error) {
	return s.Selected(ctx, key)
}

func (s *routeSelectionMemoryStore) Selected(context.Context, routes.ProviderSelectionKey) (routes.ProviderSelection, error) {
	if s.selected.Revision == 0 {
		return routes.ProviderSelection{}, routes.ErrProviderSelectionNotFound
	}
	return s.selected, nil
}

func (s *routeSelectionMemoryStore) Select(_ context.Context, request routes.SelectProviderRequest) (routes.ProviderSelection, error) {
	s.selected = routes.ProviderSelection{
		Key: request.Key, ProviderRouteID: request.ProviderRouteID, ProviderContractVersion: request.ProviderContractVersion,
		ProviderExtensionID: request.ProviderArtifact.ExtensionID, ProviderExtensionVersion: request.ProviderArtifact.ExtensionVersion,
		ProviderPackageDigest: request.ProviderArtifact.PackageDigest, SelectedByUserID: request.ActorUserID,
		SelectionAuditEventID: request.AuditEventID, Revision: 1, SelectedAt: time.Now(), UpdatedAt: time.Now(),
	}
	return s.selected, nil
}

func (*routeSelectionMemoryStore) Reset(context.Context, routes.ResetProviderRequest) error {
	return nil
}
func (*routeSelectionMemoryStore) InvalidateExtension(context.Context, routes.InvalidateProviderRequest) (int64, error) {
	return 0, nil
}
func (*routeSelectionMemoryStore) ListEvents(context.Context, routes.ProviderSelectionKey, int) ([]routes.ProviderSelectionEvent, error) {
	return nil, nil
}

func routeDispatcherArtifact(id string, digest byte) routes.PluginArtifact {
	return routes.PluginArtifact{
		ExtensionID: id, ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat(string(digest), 64), RuntimeInstanceID: "runtime-1",
	}
}

func routeDispatcherManifestRoute(id, action, path, method string) extensionmanifest.ManifestRoute {
	return extensionmanifest.ManifestRoute{
		ID: id, ContractVersion: id + "@1", Action: action, Path: path, Methods: []string{method},
		Guard: extensionmanifest.GuardCorePublic, Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
		Handler: "route.handle", ResponseSchema: id + ".response@1",
	}
}

func routeDispatcherConfig() config.Config {
	return config.Config{AppName: "SForum", AppEnv: "test", AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"}}
}

var _ = identity.Actor{}
