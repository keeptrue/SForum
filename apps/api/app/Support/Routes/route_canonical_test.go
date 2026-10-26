package routes

import (
	"context"
	"net/http"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestAliasAndRewriteCanonicalPathsRemainHostOwned(t *testing.T) {
	registry := NewRegistry()
	target := coreRoute("core.route.canonical.target", http.MethodGet, "/topics/:topicID")
	artifact := routeArtifact("canonical.mapping", "1.0.0", 'f')
	alias := stableMappingRoute("canonical.mapping.route.alias", target.ID, "/alias/:id", extensionmanifest.RouteActionAlias)
	rewrite := stableMappingRoute("canonical.mapping.route.rewrite", target.ID, "/rewrite/:id", extensionmanifest.RouteActionRewrite)
	if _, err := registry.Publish(Publication{
		Core:    []CoreRoute{target},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{alias, rewrite}}},
	}); err != nil {
		t.Fatal(err)
	}

	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: matrixPlanResolver{registry: registry}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
	})
	core := &dispatchCoreInvoker{invoke: func(_ context.Context, _ RouteExecutionStep, _ DispatchRequest) (DispatchResponse, error) {
		return DispatchResponse{
			Status: http.StatusOK, Headers: http.Header{"Link": {"<https://evil.example/>; rel=\"canonical\""}},
		}, nil
	}}

	aliasResult, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
		Method: http.MethodGet, Path: "/alias/41", Query: "page=2&tag=a&tag=b",
	}, core)
	if err != nil {
		t.Fatal(err)
	}
	if aliasResult.Response.CanonicalPath != "/topics/41" {
		t.Fatalf("alias canonical = %q", aliasResult.Response.CanonicalPath)
	}

	rewriteResult, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
		Method: http.MethodGet, Path: "/rewrite/42", Query: "page=2",
	}, core)
	if err != nil {
		t.Fatal(err)
	}
	if rewriteResult.Response.CanonicalPath != "/rewrite/42" {
		t.Fatalf("rewrite canonical = %q", rewriteResult.Response.CanonicalPath)
	}
}
