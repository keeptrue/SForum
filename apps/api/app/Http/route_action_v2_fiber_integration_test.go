package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

// Production-path evidence: Fiber → managed Route Registry middleware →
// Dispatcher → Protocol V2 fake runtime for every P6 modifier family.
// Invalid loopback target fencing lives in route_dispatcher_invalid_target_modifier_fence_test.go;
// re-asserting it here would only duplicate that Host fence.
func TestRouteActionV2FiberModifierChainOrderAndHostPatches(t *testing.T) {
	const (
		path   = "/api/v1/v2-action-chain"
		coreID = "core.route.v2.action.chain"
	)
	artifact := routeDispatcherArtifact("v2.action.chain", 'a')
	global := extensionmanifest.ManifestRoute{
		ID: "v2.action.chain.global", ContractVersion: "v2.action.chain.global@1",
		Action: extensionmanifest.RouteActionGlobalMiddleware, Priority: 50,
		Guard: extensionmanifest.GuardCorePublic, Fallback: "closed",
		Mode: extensionmanifest.RouteModeHTTP, Handler: "route.global",
		RequestSchema: "v2.action.chain.global.request@1", ResponseSchema: "v2.action.chain.global.response@1",
		MutableRequestFields: []string{"/query/view"},
	}
	before := routeDispatcherManifestRoute(
		"v2.action.chain.before", extensionmanifest.RouteActionBefore, path, stdhttp.MethodPost,
	)
	before.TargetID = coreID
	before.Priority = 40
	before.RequestSchema = "v2.action.chain.before.request@1"
	before.MutableRequestFields = []string{"/body/title"}
	filter := routeDispatcherManifestRoute(
		"v2.action.chain.filter", extensionmanifest.RouteActionFilter, path, stdhttp.MethodPost,
	)
	filter.TargetID = coreID
	filter.Priority = 30
	filter.RequestSchema = "v2.action.chain.filter.request@1"
	filter.MutableRequestFields = []string{"/headers/x-trace"}
	filter.MutableResponseFields = []string{"/headers/x-filter"}
	wrap := routeDispatcherManifestRoute(
		"v2.action.chain.wrap", extensionmanifest.RouteActionWrap, path, stdhttp.MethodPost,
	)
	wrap.TargetID = coreID
	wrap.Priority = 20
	wrap.RequestSchema = "v2.action.chain.wrap.request@1"
	wrap.MutableResponseFields = []string{"/headers/x-wrap"}
	after := routeDispatcherManifestRoute(
		"v2.action.chain.after", extensionmanifest.RouteActionAfter, path, stdhttp.MethodPost,
	)
	after.TargetID = coreID
	after.Priority = 10
	after.ResponseSchema = "v2.action.chain.after.response@1"
	after.MutableResponseFields = []string{"/headers/x-after", "/body/source"}

	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{
		Core: []routes.CoreRoute{{
			ID: coreID, ContractVersion: "sforum.route.v2.action.chain@1",
			Method: stdhttp.MethodPost, Path: path,
		}},
		Plugins: []routes.PluginRouteSet{{
			Artifact: artifact,
			Routes:   []extensionmanifest.ManifestRoute{global, before, filter, wrap, after},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	runtime := &routeActionV2FiberRuntime{
		routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, artifact),
	}
	resolver := routeRegistryPlanResolver{registry: registry}
	// POST + response modifiers require a Host failure sink before any writer runs.
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: resolver, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: HostRouteGuardAuthorizer{}, Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
		Failures: routeActionV2FiberFailureSink{},
	})
	core := &routeActionV2FiberCoreProvider{}
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{
		// RoutePlans 与 Dispatcher 共用同一 resolver，对齐生产入口 managed 分类。
		RoutePlans: resolver, RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{core},
	})

	request := httptest.NewRequest(
		stdhttp.MethodPost, path+"?view=client&keep=1",
		strings.NewReader(`{"title":"original","note":"stay"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Trace", "client")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	// runtime 只记录 Protocol V2 插件调用；Host core terminal 插在 request 与 response 之间。
	wantOrder := []string{
		"global_middleware:request",
		"before:request",
		"filter:request",
		"wrap:request",
		"handler:core",
		"wrap:response",
		"filter:response",
		"after:response",
	}
	if core.calls != 1 || len(runtime.order) != 7 {
		t.Fatalf("core=%d v2 order=%#v status=%d body=%q", core.calls, runtime.order, response.StatusCode, body)
	}
	gotOrder := append(append([]string(nil), runtime.order[:4]...), "handler:core")
	gotOrder = append(gotOrder, runtime.order[4:]...)
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("order=%#v want=%#v", gotOrder, wantOrder)
	}
	if core.query != "keep=1&view=plugin" || core.trace != "filter" || core.title != "patched" {
		t.Fatalf("core observed query=%q trace=%q title=%q", core.query, core.trace, core.title)
	}
	if response.StatusCode != stdhttp.StatusOK ||
		response.Header.Get("X-Wrap") != "wrap-out" ||
		response.Header.Get("X-Filter") != "filter-out" ||
		response.Header.Get("X-After") != "after-out" {
		t.Fatalf("status=%d headers wrap=%q filter=%q after=%q body=%q", response.StatusCode,
			response.Header.Get("X-Wrap"), response.Header.Get("X-Filter"), response.Header.Get("X-After"), body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["source"] != "after" || payload["title"] != "patched" || payload["note"] != "stay" {
		t.Fatalf("fiber body=%s", body)
	}
	if runtime.calls != 7 {
		t.Fatalf("protocol v2 calls=%d want=7", runtime.calls)
	}
}

type routeActionV2FiberFailureSink struct{}

func (routeActionV2FiberFailureSink) RecordCommittedAfterFailure(context.Context, routes.RouteCommittedAfterFailure) {
}

type routeActionV2FiberRuntime struct {
	*routeDispatcherV2Runtime
	order []string
}

func (r *routeActionV2FiberRuntime) InvokeRouteInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if identity != r.snapshot.Identity {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	r.calls++
	r.request = request
	r.order = append(r.order, request.RouteAction+":"+string(request.InvocationStage))

	switch request.RouteAction {
	case extensionmanifest.RouteActionGlobalMiddleware:
		if request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageRequest ||
			request.QueryParameters["view"] != "client" {
			return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
		}
		return extensionsruntime.ProtocolV2RouteResponse{RequestPatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
			Kind: extensionsruntime.ProtocolV2RoutePatchReplace, Path: "/query/view", Value: []byte(`["plugin"]`),
		}}}, nil
	case extensionmanifest.RouteActionBefore:
		if request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageRequest ||
			request.Body["title"] != "original" || request.QueryParameters["view"] != "plugin" {
			return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
		}
		return extensionsruntime.ProtocolV2RouteResponse{RequestPatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
			Kind: extensionsruntime.ProtocolV2RoutePatchReplace, Path: "/body/title", Value: []byte(`"patched"`),
		}}}, nil
	case extensionmanifest.RouteActionFilter:
		switch request.InvocationStage {
		case extensionsruntime.ProtocolV2RouteInvocationStageRequest:
			if request.Headers.Get("X-Trace") != "client" || request.Body["title"] != "patched" {
				return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
			}
			return extensionsruntime.ProtocolV2RouteResponse{RequestPatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
				Kind: extensionsruntime.ProtocolV2RoutePatchReplace, Path: "/headers/x-trace", Value: []byte(`["filter"]`),
			}}}, nil
		case extensionsruntime.ProtocolV2RouteInvocationStageResponse:
			if request.PriorResponse == nil || request.PriorResponse.Headers.Get("X-Wrap") != "wrap-out" {
				return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
			}
			return extensionsruntime.ProtocolV2RouteResponse{ResponsePatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
				Kind: extensionsruntime.ProtocolV2RoutePatchAdd, Path: "/headers/x-filter", Value: []byte(`["filter-out"]`),
			}}}, nil
		}
	case extensionmanifest.RouteActionWrap:
		switch request.InvocationStage {
		case extensionsruntime.ProtocolV2RouteInvocationStageRequest:
			if request.Headers.Get("X-Trace") != "filter" {
				return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
			}
			return extensionsruntime.ProtocolV2RouteResponse{}, nil
		case extensionsruntime.ProtocolV2RouteInvocationStageResponse:
			if request.PriorResponse == nil || request.PriorResponse.Body["source"] != "core" {
				return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
			}
			return extensionsruntime.ProtocolV2RouteResponse{ResponsePatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
				Kind: extensionsruntime.ProtocolV2RoutePatchAdd, Path: "/headers/x-wrap", Value: []byte(`["wrap-out"]`),
			}}}, nil
		}
	case extensionmanifest.RouteActionAfter:
		if request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageResponse ||
			request.PriorResponse == nil || request.PriorResponse.Headers.Get("X-Filter") != "filter-out" {
			return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
		}
		return extensionsruntime.ProtocolV2RouteResponse{ResponsePatch: []extensionsruntime.ProtocolV2RoutePatchOperation{
			{Kind: extensionsruntime.ProtocolV2RoutePatchAdd, Path: "/headers/x-after", Value: []byte(`["after-out"]`)},
			{Kind: extensionsruntime.ProtocolV2RoutePatchReplace, Path: "/body/source", Value: []byte(`"after"`)},
		}}, nil
	}
	return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
}

type routeActionV2FiberCoreProvider struct {
	calls int
	query string
	trace string
	title string
}

func (p *routeActionV2FiberCoreProvider) RegisterRoutes(api fiber.Router) {
	api.Post("/v2-action-chain", func(c fiber.Ctx) error {
		p.calls++
		p.query = string(c.Request().URI().QueryString())
		p.trace = string(c.Request().Header.Peek("X-Trace"))
		var payload struct {
			Title string `json:"title"`
			Note  string `json:"note"`
		}
		if err := json.Unmarshal(c.Body(), &payload); err != nil {
			return err
		}
		p.title = payload.Title
		c.Set(fiber.HeaderContentType, "application/json")
		return c.Status(stdhttp.StatusOK).SendString(`{"source":"core","title":"` + payload.Title + `","note":"` + payload.Note + `"}`)
	})
}

var _ routes.RouteFailureSink = routeActionV2FiberFailureSink{}
