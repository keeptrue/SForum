package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestPluginRouteLinkHeadersDoNotReachFiber(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("canonical.plugin", 'a')
	declaration := routeDispatcherManifestRoute(
		"canonical.plugin.route", extensionmanifest.RouteActionAdd, "/plugin-link", stdhttp.MethodGet,
	)
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	runtime, server := newRouteDispatcherRuntime(t, artifact)
	server.Config.Handler = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Plugin-Metadata", "kept")
		for _, value := range []string{
			"<https://evil.example/>; rel=\"canonical\"",
			"</asset.js>; rel=\"preload canonical\"",
			"</page/2?value=a,b>; REL=Canonical; title=\"quoted, comma\"",
			"</next>; rel=\"next\"",
		} {
			writer.Header().Add("Link", value)
		}
		writer.WriteHeader(stdhttp.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	})
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: HostRouteGuardAuthorizer{}, Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
	})
	app := fiber.New()
	app.Use(routeDispatcherMiddleware(dispatcher, nil))

	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/plugin-link", nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusOK || len(response.Header.Values("Link")) != 0 ||
		response.Header.Get("X-Plugin-Metadata") != "kept" {
		t.Fatalf("status=%d headers=%v", response.StatusCode, response.Header)
	}
}

func TestCoreRouteLinkHeaderRemainsAvailable(t *testing.T) {
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Core: []routes.CoreRoute{{
		ID: "core.route.canonical.link", ContractVersion: "sforum.route.canonical.link@1",
		Method: stdhttp.MethodGet, Path: "/core-link",
	}}}); err != nil {
		t.Fatal(err)
	}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{Plans: routeRegistryPlanResolver{registry: registry}})
	app := fiber.New()
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	app.Get("/core-link", func(c fiber.Ctx) error {
		c.Set("Link", "</page/2>; rel=\"next\"")
		return c.SendStatus(stdhttp.StatusNoContent)
	})

	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/core-link", nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusNoContent || response.Header.Get("Link") != "</page/2>; rel=\"next\"" {
		t.Fatalf("status=%d headers=%v", response.StatusCode, response.Header)
	}
}

func TestFilteredRouteResponseHeadersReserveLinkForHost(t *testing.T) {
	filtered := filteredRouteResponseHeaders(stdhttp.Header{
		"Link": {
			"<https://evil.example/>; rel=\"canonical\"",
			"</asset.js>; rel=\"preload\"",
			"</next?value=a,b>; rel=\"next\"; title=\"quoted, comma\"",
		},
		"Cache-Control": {"private"},
	})
	if len(filtered.Values("Link")) != 0 || filtered.Get("Cache-Control") != "private" {
		t.Fatalf("filtered headers=%v", filtered)
	}
}

func TestWriteRouteDispatchResponseEnforcesHostCanonicalLink(t *testing.T) {
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error {
		writeRouteDispatchResponse(c, routes.DispatchResponse{
			Status: stdhttp.StatusOK,
			Headers: stdhttp.Header{
				fiber.HeaderLink: {"<https://evil.example/>; rel=\"canonical\""},
			},
			CanonicalPath: "/topics/中文",
		}, nil)
		return nil
	})

	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got := response.Header.Values(fiber.HeaderLink); len(got) != 1 || got[0] != "</topics/%E4%B8%AD%E6%96%87>; rel=\"canonical\"" {
		t.Fatalf("Link = %#v", got)
	}
}

func TestRouteCanonicalLinkPathRejectsExternalQueryFragmentAndControls(t *testing.T) {
	for _, path := range []string{"", "relative", "//evil.example/path", "https://evil.example/path", "/path?query=1", "/path#fragment", "/bad\r\nvalue"} {
		if value, ok := routeCanonicalLinkPath(path); ok || value != "" {
			t.Fatalf("canonical path %q = %q, %v", path, value, ok)
		}
	}
	for _, test := range []struct{ path, want string }{
		{path: "/topics/中文", want: "/topics/%E4%B8%AD%E6%96%87"},
		{path: "/topics/%E4%B8%AD%E6%96%87", want: "/topics/%E4%B8%AD%E6%96%87"},
	} {
		if got, ok := routeCanonicalLinkPath(test.path); !ok || got != test.want {
			t.Fatalf("canonical path %q = %q, %v", test.path, got, ok)
		}
	}
}
