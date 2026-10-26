package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionRouteGuardAcceptsOnlyHostAppliedPluginParamMutation(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("params.demo", 'c')
	handler := routeDispatcherManifestRoute(
		"params.demo.handler", extensionmanifest.RouteActionAdd, "/params/:id", stdhttp.MethodGet,
	)
	before := routeDispatcherManifestRoute(
		"params.demo.before", extensionmanifest.RouteActionBefore, handler.Path, stdhttp.MethodGet,
	)
	before.TargetID = handler.ID
	before.MutableRequestFields = []string{"/params/id"}
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{handler, before},
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(stdhttp.MethodGet, "/params/41")
	if err != nil {
		t.Fatal(err)
	}
	terminal := plan.Terminal()
	authorizer := NewProductionRouteGuardAuthorizer()
	forged := routes.DispatchRequest{
		Method: stdhttp.MethodGet, Path: "/params/41", Params: map[string]string{"id": "99"},
	}
	if _, err := authorizer.AuthorizeRoute(context.Background(), plan, 1, terminal, forged); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("forged params error=%v", err)
	}
	forgedPath := routes.DispatchRequest{
		Method: stdhttp.MethodGet, Path: "/params/99", Params: map[string]string{"id": "41"},
	}
	if _, err := authorizer.AuthorizeRoute(context.Background(), plan, 1, terminal, forgedPath); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("forged path error=%v", err)
	}

	invoker := &paramsMutationStepInvoker{t: t}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: invoker,
		Guard: authorizer, Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	result, err := dispatcher.Dispatch(context.Background(), routes.DispatchRequest{
		Method: stdhttp.MethodGet, Path: "/params/41",
	}, nil)
	if err != nil || result.Response.Status != stdhttp.StatusOK ||
		!reflect.DeepEqual(invoker.stages, []routes.InvocationStage{routes.InvocationStageRequest, routes.InvocationStageHandler}) {
		t.Fatalf("result=%#v stages=%#v err=%v", result, invoker.stages, err)
	}
}

func TestRouteParamsMutationReachesProtocolV2HandlerWithOriginalPath(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("params.protocol", 'd')
	handler := routeDispatcherManifestRoute(
		"params.protocol.handler", extensionmanifest.RouteActionAdd, "/params-v2/:id", stdhttp.MethodGet,
	)
	before := routeDispatcherManifestRoute(
		"params.protocol.before", extensionmanifest.RouteActionBefore, handler.Path, stdhttp.MethodGet,
	)
	before.TargetID = handler.ID
	before.MutableRequestFields = []string{"/params/id"}
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{handler, before},
	}}}); err != nil {
		t.Fatal(err)
	}
	runtime := &paramsMutationProtocolV2Runtime{
		routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, artifact), t: t,
	}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: NewProductionRouteGuardAuthorizer(), Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})

	result, err := dispatcher.Dispatch(context.Background(), routes.DispatchRequest{
		Method: stdhttp.MethodGet, Path: "/params-v2/41",
	}, nil)
	if err != nil || result.Response.Status != stdhttp.StatusOK || string(result.Response.Body) != `{"ok":true}` ||
		!reflect.DeepEqual(runtime.stages, []extensionsruntime.ProtocolV2RouteInvocationStage{
			extensionsruntime.ProtocolV2RouteInvocationStageRequest,
			extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		}) {
		t.Fatalf("result=%#v stages=%#v error=%v", result, runtime.stages, err)
	}
}

func TestRouteParamsMutationCannotRetargetCoreHandler(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("params.core", 'e')
	core := routes.CoreRoute{
		ID: "core.route.params.target", ContractVersion: "sforum.route.params.target@1",
		Method: stdhttp.MethodGet, Path: "/api/v1/params-core/:id",
	}
	before := routeDispatcherManifestRoute(
		"params.core.before", extensionmanifest.RouteActionBefore, core.Path, stdhttp.MethodGet,
	)
	before.TargetID = core.ID
	before.MutableRequestFields = []string{"/params/id"}
	if _, err := registry.Publish(routes.Publication{
		Core:    []routes.CoreRoute{core},
		Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{before}}},
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &coreParamsMutationV2Runtime{routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, artifact)}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: NewProductionRouteGuardAuthorizer(), Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	provider := &paramsMutationCoreProvider{}
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{
		RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{provider},
	})

	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/params-core/41", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.ReadAll(response.Body)
	if response.StatusCode != fiber.StatusUnprocessableEntity || provider.calls != 0 || runtime.calls != 1 {
		t.Fatalf("status=%d core=%d runtime=%d", response.StatusCode, provider.calls, runtime.calls)
	}
}

type paramsMutationStepInvoker struct {
	t      *testing.T
	stages []routes.InvocationStage
}

func (*paramsMutationStepInvoker) SupportsMode(mode string) bool {
	return mode == extensionmanifest.RouteModeHTTP
}

func (i *paramsMutationStepInvoker) Invoke(
	_ context.Context,
	input routes.RouteInvocation,
) (routes.RouteInvocationResult, error) {
	i.stages = append(i.stages, input.Stage)
	if _, ok := input.RequestAuthority(); !ok {
		i.t.Fatal("exact request authority is missing")
	}
	switch input.Stage {
	case routes.InvocationStageRequest:
		if input.Request.Path != "/params/41" || input.Request.Params["id"] != "41" || input.Request.HostMutatedParams() {
			i.t.Fatalf("initial request=%#v", input.Request)
		}
		return routes.RouteInvocationResult{RequestPatch: []routes.RoutePatchOperation{{
			Kind: routes.RoutePatchReplace, Path: "/params/id", Value: []byte(`"99"`),
		}}}, nil
	case routes.InvocationStageHandler:
		if input.Request.Path != "/params/41" || input.Request.Params["id"] != "99" || !input.Request.HostMutatedParams() {
			i.t.Fatalf("mutated request=%#v", input.Request)
		}
		response := routes.DispatchResponse{
			Status:  stdhttp.StatusOK,
			Headers: stdhttp.Header{"Content-Type": []string{"application/json"}},
			Body:    []byte(`{"ok":true}`),
		}
		return routes.RouteInvocationResult{Response: &response}, nil
	default:
		i.t.Fatalf("unexpected stage %q", input.Stage)
		return routes.RouteInvocationResult{}, nil
	}
}

type paramsMutationProtocolV2Runtime struct {
	*routeDispatcherV2Runtime
	t      *testing.T
	stages []extensionsruntime.ProtocolV2RouteInvocationStage
}

func (r *paramsMutationProtocolV2Runtime) InvokeRouteInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if identity != r.snapshot.Identity || request.Path != "/params-v2/41" {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
	}
	r.stages = append(r.stages, request.InvocationStage)
	switch request.InvocationStage {
	case extensionsruntime.ProtocolV2RouteInvocationStageRequest:
		if request.RouteAction != extensionmanifest.RouteActionBefore || request.PathParameters["id"] != "41" {
			r.t.Fatalf("request-stage input=%#v", request)
		}
		return extensionsruntime.ProtocolV2RouteResponse{RequestPatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
			Kind: extensionsruntime.ProtocolV2RoutePatchReplace, Path: "/params/id", Value: []byte(`"99"`),
		}}}, nil
	case extensionsruntime.ProtocolV2RouteInvocationStageHandler:
		if request.RouteAction != extensionmanifest.RouteActionAdd || request.PathParameters["id"] != "99" {
			r.t.Fatalf("handler-stage input=%#v", request)
		}
		return extensionsruntime.ProtocolV2RouteResponse{
			StatusCode: stdhttp.StatusOK, Headers: stdhttp.Header{"Content-Type": {"application/json"}},
			Body: map[string]any{"ok": true}, BodyPresent: true,
		}, nil
	default:
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
	}
}

type coreParamsMutationV2Runtime struct {
	*routeDispatcherV2Runtime
	calls int
}

func (r *coreParamsMutationV2Runtime) InvokeRouteInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if identity != r.snapshot.Identity || request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageRequest ||
		request.RouteAction != extensionmanifest.RouteActionBefore || request.Path != "/api/v1/params-core/41" ||
		request.PathParameters["id"] != "41" {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
	}
	r.calls++
	return extensionsruntime.ProtocolV2RouteResponse{RequestPatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
		Kind: extensionsruntime.ProtocolV2RoutePatchReplace, Path: "/params/id", Value: []byte(`"99"`),
	}}}, nil
}

type paramsMutationCoreProvider struct{ calls int }

func (p *paramsMutationCoreProvider) RegisterRoutes(api fiber.Router) {
	api.Get("/params-core/:id", func(c fiber.Ctx) error {
		p.calls++
		return c.SendString(c.Params("id"))
	})
}
