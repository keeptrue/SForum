package http

import (
	"context"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const routeQuerySubprocessHelperEnv = "route-query-subprocess-e2e"

func TestRouteQueryMutationAcrossFiberManagerAndRealProtocolV2Process(t *testing.T) {
	extension := routeQuerySubprocessExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: routeStreamE2ETrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "query-grant", ImpactDigest: "query-impact",
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
		t.Fatalf("expected an exact Protocol V2 subprocess runtime: %#v", runtime)
	}

	core := routes.CoreRoute{
		ID: routeQueryCoreID, ContractVersion: "sforum.route.query.subprocess@1",
		Method: stdhttp.MethodGet, Path: routeQueryCorePath,
	}
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{
		Core: []routes.CoreRoute{core},
		Plugins: []routes.PluginRouteSet{{
			Artifact: routes.PluginArtifact{
				ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
				RuntimeInstanceID: runtime.Identity.InstanceID,
			},
			Routes: extension.Manifest.Routes,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(manager),
		Guard: NewProductionRouteGuardAuthorizer(), Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	provider := &queryMutationCoreProvider{}
	app := NewApp(routeDispatcherConfig(), nil, Dependencies{
		RouteDispatcher: dispatcher, RouteProviders: []RouteProvider{provider},
	})

	requestTarget := routeQueryCorePath +
		"?tag=first&tag=&tag=a%2Bb&tag=slash%2Fvalue&keep=legacy&keep=two"
	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, requestTarget, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := "keep=legacy&keep=two&tag=patched-first&tag=&tag=a%2Bb&tag=slash%2Fvalue&tag=tail"
	if response.StatusCode != stdhttp.StatusOK || string(body) != wantQuery || provider.calls != 1 {
		t.Fatalf("status=%d body=%q core calls=%d", response.StatusCode, body, provider.calls)
	}
	after, err := manager.InspectRuntimeInstance(runtime.Identity)
	if err != nil || after.Admission.ActiveTotal != 0 {
		t.Fatalf("runtime admission after query dispatch=%#v err=%v", after.Admission, err)
	}
}

func TestRouteQueryMutationProtocolV2HelperProcess(t *testing.T) {
	if os.Getenv("SFORUM_PLUGIN_HELPER") != routeQuerySubprocessHelperEnv {
		return
	}
	pluginv2sdk.Serve(&routeQuerySubprocessServer{Server: pluginv2sdk.NewServer()})
	os.Exit(0)
}

type routeQuerySubprocessServer struct{ *pluginv2sdk.Server }

func (s *routeQuerySubprocessServer) InvokeRoute(
	_ context.Context,
	request *pluginwire.RouteRequest,
) (*pluginwire.RouteResponse, error) {
	if reason := invalidRouteQuerySubprocessRequest(request); reason != "" {
		return &pluginwire.RouteResponse{
			Context: routeStreamE2EResponseContext(request.GetContext()),
			Error: &protocolwire.ErrorDetail{
				Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: reason,
			},
		}, nil
	}
	return &pluginwire.RouteResponse{
		Context: routeStreamE2EResponseContext(request.GetContext()),
		RequestPatch: []*pluginwire.RoutePatchOperation{{
			Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REPLACE,
			Path: "/query/tag", ValueJson: []byte(`["patched-first","","a+b","slash/value","tail"]`),
		}},
	}, nil
}

func invalidRouteQuerySubprocessRequest(request *pluginwire.RouteRequest) string {
	if request.GetRouteId() != routeQueryRouteID || request.GetContractVersion() != routeQueryRouteID+"@1" ||
		request.GetRouteAction() != extensionmanifest.RouteActionBefore ||
		request.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_REQUEST ||
		request.GetMethod() != stdhttp.MethodGet || request.GetPath() != routeQueryCorePath ||
		request.GetPriorResponse() != nil || !slices.Equal(request.GetMutableRequestFields(), []string{"/query/tag"}) ||
		len(request.GetMutableResponseFields()) != 0 {
		return "route.query.metadata"
	}
	legacy := request.GetQueryParameters()
	if len(legacy) != 2 || legacy["keep"] != "legacy" || legacy["tag"] != "first" {
		return "route.query.legacy_first_values"
	}
	values := request.GetQueryParameterValues()
	if len(values) != 2 || values[0].GetKey() != "keep" ||
		!slices.Equal(values[0].GetValues(), []string{"legacy", "two"}) ||
		values[1].GetKey() != "tag" ||
		!slices.Equal(values[1].GetValues(), []string{"first", "", "a+b", "slash/value"}) {
		return "route.query.lossless_ordered_values"
	}
	return ""
}

const (
	routeQueryCoreID   = "core.route.query.subprocess"
	routeQueryCorePath = "/api/v1/query-core"
	routeQueryRouteID  = "runtime.query.before"
)

func routeQuerySubprocessExtension(t *testing.T) extensions.Extension {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "runtime.query", "1.0.0")
	if err := os.MkdirAll(filepath.Join(packageRoot, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_HELPER=" + routeQuerySubprocessHelperEnv + " exec " +
		routeStreamShellQuote(os.Args[0]) + " -test.run='^TestRouteQueryMutationProtocolV2HelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(packageRoot, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: "runtime.query", Name: "Runtime Query", Version: "1.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: strings.Repeat("e", 64), PackagePath: packageRoot,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "runtime.query", Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2",
			},
			Routes: []extensions.ManifestRoute{{
				ID: routeQueryRouteID, ContractVersion: routeQueryRouteID + "@1",
				Action: extensionmanifest.RouteActionBefore, TargetID: routeQueryCoreID,
				Path: routeQueryCorePath, Methods: []string{stdhttp.MethodGet}, Guard: extensionmanifest.GuardCorePublic,
				Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.query",
				MutableRequestFields: []string{"/query/tag"},
			}},
		},
	}
}

var _ pluginwire.PluginRuntimeServiceServer = (*routeQuerySubprocessServer)(nil)
