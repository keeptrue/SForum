package routes

import (
	"context"
	"errors"
	"net/http"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestStableRedirectTargetMaterializesCorePathWithoutSourceQuery(t *testing.T) {
	registry := NewRegistry()
	target := coreRoute("core.route.redirect.target", http.MethodGet, "/topics/:locale/:topicID/*rest")
	artifact := routeArtifact("redirect.target", "1.0.0", 'c')
	redirect := stableRedirectRoute(
		"redirect.target.route.legacy", target.ID, "/legacy/:locale/:legacyID/*tail", http.MethodGet,
	)
	redirect.StatusCode = http.StatusMovedPermanently
	if _, err := registry.Publish(Publication{
		Core:    []CoreRoute{target},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect}}},
	}); err != nil {
		t.Fatal(err)
	}

	path := "/legacy/zh-CN/主题/评论/一"
	plan, err := registry.BuildExecutionPlan(http.MethodGet, path)
	if err != nil {
		t.Fatal(err)
	}
	terminal := plan.Terminal()
	if terminal.TargetID != target.ID || terminal.TargetPath != "/topics/zh-CN/主题/评论/一" || terminal.Destination != "" {
		t.Fatalf("stable redirect terminal = %#v", terminal)
	}

	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: matrixPlanResolver{registry: registry}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
		Method: http.MethodGet, Path: path, Query: "tag=a&tag=b&page=2",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Status != http.StatusMovedPermanently ||
		result.Response.Headers.Get("Location") != "/topics/zh-CN/%E4%B8%BB%E9%A2%98/%E8%AF%84%E8%AE%BA/%E4%B8%80" {
		t.Fatalf("redirect response = %#v", result.Response)
	}
	if result.Response.Headers.Get("Location") == "" || result.Response.Headers.Get("Location") == path ||
		result.Response.Headers.Get("Location") == path+"?tag=a&tag=b&page=2" {
		t.Fatalf("redirect reflected source URL: %#v", result.Response.Headers)
	}

	head, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: http.MethodHead, Path: path}, nil)
	if err != nil || head.Response.Status != http.StatusMovedPermanently {
		t.Fatalf("HEAD redirect = %#v, %v", head, err)
	}
}

func TestStableRedirectTargetFailsClosedForInvalidGraphsAndTargets(t *testing.T) {
	artifact := routeArtifact("redirect.invalid", "1.0.0", 'd')
	t.Run("self", func(t *testing.T) {
		redirect := stableRedirectRoute("redirect.invalid.route.self", "redirect.invalid.route.self", "/self", http.MethodGet)
		_, err := NewRegistry().Publish(Publication{
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect}}},
		})
		if !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("self target error = %v", err)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		first := stableMappingRoute("redirect.invalid.route.first", "redirect.invalid.route.second", "/first", extensionmanifest.RouteActionAlias)
		second := stableMappingRoute("redirect.invalid.route.second", "redirect.invalid.route.first", "/second", extensionmanifest.RouteActionRewrite)
		_, err := NewRegistry().Publish(Publication{
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{first, second}}},
		})
		if !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("cycle error = %v", err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		redirect := stableRedirectRoute("redirect.invalid.route.missing", "core.route.redirect.missing", "/missing", http.MethodGet)
		_, err := NewRegistry().Publish(Publication{
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect}}},
		})
		if !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("missing target error = %v", err)
		}
	})

	t.Run("method mismatch", func(t *testing.T) {
		target := coreRoute("core.route.redirect.method", http.MethodGet, "/target")
		redirect := stableRedirectRoute("redirect.invalid.route.method", target.ID, "/method", http.MethodPost)
		_, err := NewRegistry().Publish(Publication{
			Core:    []CoreRoute{target},
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect}}},
		})
		if !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("method mismatch error = %v", err)
		}
	})

	t.Run("ambiguous declaration", func(t *testing.T) {
		target := coreRoute("core.route.redirect.ambiguous_declaration", http.MethodGet, "/target")
		redirect := stableRedirectRoute("redirect.invalid.route.ambiguous_declaration", target.ID, "/old", http.MethodGet)
		redirect.Destination = "/forged"
		_, err := NewRegistry().Publish(Publication{
			Core:    []CoreRoute{target},
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect}}},
		})
		if !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("ambiguous declaration error = %v", err)
		}
	})

	t.Run("external identity", func(t *testing.T) {
		redirect := stableRedirectRoute("redirect.invalid.route.external", "https://evil.example", "/old", http.MethodGet)
		_, err := NewRegistry().Publish(Publication{
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect}}},
		})
		if !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("external identity error = %v", err)
		}
	})
}

func TestRedirectLocationPreservesEscapesAndRejectsNonPathReferences(t *testing.T) {
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/topics/中文", want: "/topics/%E4%B8%AD%E6%96%87"},
		{path: "/topics/%E4%B8%AD%E6%96%87", want: "/topics/%E4%B8%AD%E6%96%87"},
	} {
		got, err := routeRedirectLocation(RouteExecutionStep{TargetID: "core.route.target", TargetPath: test.path})
		if err != nil || got != test.want {
			t.Fatalf("location %q = %q, %v", test.path, got, err)
		}
	}
	for _, path := range []string{
		"", "relative", "//evil.example/path", "/\\evil.example/path", "https://evil.example/path",
		"/safe\\segment", "/path?query=1", "/path#fragment", "/bad\r\nvalue",
	} {
		if _, err := routeRedirectLocation(RouteExecutionStep{TargetID: "core.route.target", TargetPath: path}); !errors.Is(err, ErrInvalidExecutionPlan) {
			t.Fatalf("invalid location %q error = %v", path, err)
		}
	}
}

func TestRoutePathBoundariesRejectNetworkPathReferences(t *testing.T) {
	for _, value := range []string{"//evil.example/path", "/\\evil.example/path"} {
		if _, err := compileRoutePath(value); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("route pattern %q error = %v", value, err)
		}
		if _, err := normalizeRequestPath(value); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("request path %q error = %v", value, err)
		}
	}
	if _, err := compileRoutePath("/safe/path"); err != nil {
		t.Fatalf("safe route pattern rejected: %v", err)
	}
	if got, err := normalizeRequestPath("/safe/path?query=1"); err != nil || got != "/safe/path" {
		t.Fatalf("safe request path = %q, %v", got, err)
	}
}

func TestRedirectOutputRemainsHostOwnedAfterModifier(t *testing.T) {
	registry := NewRegistry()
	target := coreRoute("core.route.redirect.authority", http.MethodGet, "/canonical")
	artifact := routeArtifact("redirect.authority", "1.0.0", 'a')
	redirect := stableRedirectRoute("redirect.authority.route.old", target.ID, "/old", http.MethodGet)
	after := modifierRoute("redirect.authority.route.after", redirect.ID, redirect.Path, extensionmanifest.RouteActionAfter, http.MethodGet, 10)
	after.Guard = extensionmanifest.GuardCorePublic
	after.MutableResponseFields = []string{"/status"}
	if _, err := registry.Publish(Publication{
		Core:    []CoreRoute{target},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect, after}}},
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: matrixPlanResolver{registry: registry}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
		Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
			if input.Step.Action != extensionmanifest.RouteActionAfter {
				t.Fatalf("unexpected plugin step = %#v", input.Step)
			}
			return RouteInvocationResult{ResponsePatch: []RoutePatchOperation{
				{Kind: RoutePatchReplace, Path: "/status", Value: []byte(`302`)},
			}}, nil
		}},
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/old"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Status != http.StatusPermanentRedirect || result.Response.Headers.Get("Location") != "/canonical" ||
		result.Response.Headers.Get("Link") != "" || result.Response.CanonicalPath != "/canonical" {
		t.Fatalf("redirect authority = %#v", result.Response)
	}
}

func TestRedirectCanonicalDisappearsWithPluginSnapshot(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("redirect.snapshot", "1.0.0", 'b')
	redirect := redirectStatusRoute("redirect.snapshot.route.old", http.StatusPermanentRedirect)
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect},
	}}}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: matrixPlanResolver{registry: registry}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/old"}, nil)
	if err != nil || !result.Handled || result.Response.CanonicalPath != "/new" {
		t.Fatalf("active redirect = %#v, %v", result, err)
	}

	if _, err := registry.Publish(Publication{SafeMode: true}); err != nil {
		t.Fatal(err)
	}
	result, err = dispatcher.Dispatch(context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/old"}, nil)
	if err != nil || result.Handled || result.Response.CanonicalPath != "" || result.Response.Headers.Get("Location") != "" {
		t.Fatalf("removed redirect = %#v, %v", result, err)
	}
}

func TestStableRedirectTargetFailsClosedForIncompatibleAmbiguousAndPluginTargets(t *testing.T) {
	artifact := routeArtifact("redirect.plan", "1.0.0", 'e')
	t.Run("incompatible parameters", func(t *testing.T) {
		registry := NewRegistry()
		target := coreRoute("core.route.redirect.incompatible", http.MethodGet, "/target")
		redirect := stableRedirectRoute("redirect.plan.route.incompatible", target.ID, "/old/:id", http.MethodGet)
		if _, err := registry.Publish(Publication{
			Core:    []CoreRoute{target},
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect}}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.BuildExecutionPlan(http.MethodGet, "/old/41"); !errors.Is(err, ErrInvalidExecutionPlan) {
			t.Fatalf("incompatible plan error = %v", err)
		}
	})

	t.Run("ambiguous core", func(t *testing.T) {
		registry := NewRegistry()
		first := coreRoute("core.route.redirect.ambiguous", http.MethodGet, "/target")
		second := first
		second.ContractVersion = "sforum.route.redirect.ambiguous@2"
		second.Guard.ContractVersion = second.ContractVersion
		redirect := stableRedirectRoute("redirect.plan.route.ambiguous", first.ID, "/old", http.MethodGet)
		if _, err := registry.Publish(Publication{
			Core:    []CoreRoute{first, second},
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect}}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.BuildExecutionPlan(http.MethodGet, "/old"); !errors.Is(err, ErrInvalidExecutionPlan) {
			t.Fatalf("ambiguous plan error = %v", err)
		}
	})

	t.Run("plugin target", func(t *testing.T) {
		registry := NewRegistry()
		pluginTarget := pluginRoute("redirect.plan.route.target", "/target", 0, http.MethodGet)
		redirect := stableRedirectRoute("redirect.plan.route.plugin", pluginTarget.ID, "/old", http.MethodGet)
		if _, err := registry.Publish(Publication{
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{pluginTarget, redirect}}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.BuildExecutionPlan(http.MethodGet, "/old"); !errors.Is(err, ErrInvalidExecutionPlan) {
			t.Fatalf("plugin target plan error = %v", err)
		}
	})
}

func stableRedirectRoute(id, targetID, path, method string) extensionmanifest.ManifestRoute {
	route := stableMappingRoute(id, targetID, path, extensionmanifest.RouteActionRedirect)
	route.Methods = []string{method}
	route.StatusCode = extensionmanifest.RouteRedirectStatusDefault
	return route
}

func stableMappingRoute(id, targetID, path, action string) extensionmanifest.ManifestRoute {
	route := pluginRoute(id, path, 0, http.MethodGet)
	route.Action = action
	route.TargetID = targetID
	route.Guard = extensionmanifest.GuardCoreInherit
	route.Handler = ""
	route.RequestSchema = ""
	route.ResponseSchema = ""
	route.Destination = ""
	return route
}
