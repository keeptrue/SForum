package http

import (
	"context"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteDispatcherWritesRepeatedQueryMutationBackToFiberCore(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("query.demo", 'd')
	core := routes.CoreRoute{
		ID: "core.route.query.demo", ContractVersion: "sforum.route.query.demo@1",
		Method: stdhttp.MethodGet, Path: "/api/v1/query-core",
	}
	before := routeDispatcherManifestRoute(
		"query.demo.before", extensionmanifest.RouteActionBefore, core.Path, stdhttp.MethodGet,
	)
	before.TargetID = core.ID
	before.MutableRequestFields = []string{"/query/tag"}
	if _, err := registry.Publish(routes.Publication{
		Core:    []routes.CoreRoute{core},
		Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{before}}},
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &queryMutationV2Runtime{routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, artifact)}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: NewProductionRouteGuardAuthorizer(), Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	provider := &queryMutationCoreProvider{}
	app := NewApp(routeDispatcherConfig(), slog.Default(), Dependencies{
		RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{provider},
	})
	response, err := app.Test(httptest.NewRequest(
		stdhttp.MethodGet, "/api/v1/query-core?tag=old&keep=1", nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != stdhttp.StatusOK || string(body) != "keep=1&tag=one&tag=&tag=two" ||
		provider.calls != 1 || runtime.calls != 1 {
		t.Fatalf("status=%d body=%q core=%d runtime=%d", response.StatusCode, body, provider.calls, runtime.calls)
	}
}

type queryMutationV2Runtime struct {
	*routeDispatcherV2Runtime
	calls int
}

func (r *queryMutationV2Runtime) InvokeRouteInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if identity != r.snapshot.Identity || request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageRequest ||
		request.RouteAction != extensionmanifest.RouteActionBefore {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrProtocolV2RouteInvalid
	}
	r.calls++
	return extensionsruntime.ProtocolV2RouteResponse{RequestPatch: []extensionsruntime.ProtocolV2RoutePatchOperation{{
		Kind: extensionsruntime.ProtocolV2RoutePatchReplace, Path: "/query/tag", Value: []byte(`["one","","two"]`),
	}}}, nil
}

type queryMutationCoreProvider struct{ calls int }

func (p *queryMutationCoreProvider) RegisterRoutes(api fiber.Router) {
	api.Get("/query-core", func(c fiber.Ctx) error {
		p.calls++
		return c.SendString(string(c.Request().URI().QueryString()))
	})
}
