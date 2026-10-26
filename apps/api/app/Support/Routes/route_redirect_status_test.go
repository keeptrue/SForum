package routes

import (
	"context"
	"errors"
	"net/http"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRedirectStatusPropagatesThroughRegistryPlanDispatcherAndInspector(t *testing.T) {
	for _, test := range []struct {
		name     string
		declared int
		want     int
	}{
		{name: "default", want: http.StatusPermanentRedirect},
		{name: "301", declared: http.StatusMovedPermanently, want: http.StatusMovedPermanently},
		{name: "308", declared: http.StatusPermanentRedirect, want: http.StatusPermanentRedirect},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			artifact := routeArtifact("redirect.status."+test.name, "1.0.0", 'a')
			redirect := redirectStatusRoute(artifact.ExtensionID+".route.old", test.declared)
			if _, err := registry.Publish(Publication{
				Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{redirect}}},
			}); err != nil {
				t.Fatal(err)
			}

			plan, err := registry.BuildExecutionPlan(http.MethodGet, "/old")
			if err != nil {
				t.Fatal(err)
			}
			terminal := plan.Terminal()
			if terminal.StatusCode != test.want {
				t.Fatalf("plan status = %d, want %d", terminal.StatusCode, test.want)
			}
			forged := terminal
			forged.StatusCode = http.StatusMovedPermanently
			if test.want == http.StatusMovedPermanently {
				forged.StatusCode = http.StatusPermanentRedirect
			}
			if equalCoreGuardExecutionStep(terminal, forged) {
				t.Fatal("redirect status must be part of the exact authorized step")
			}
			inspected := inspectorExecutionStep(0, terminal, routeStepPathSignature(terminal))
			if inspected.StatusCode != test.want {
				t.Fatalf("inspector status = %d, want %d", inspected.StatusCode, test.want)
			}

			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: matrixPlanResolver{registry: registry}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
			})
			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/old"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Handled || result.Response.Status != test.want || result.Response.Headers.Get("Location") != "/new" {
				t.Fatalf("dispatch = %#v", result)
			}
		})
	}
}

func TestRegistryRejectsInvalidOrNonRedirectStatusCode(t *testing.T) {
	artifact := routeArtifact("redirect.status.invalid", "1.0.0", 'b')
	for _, status := range []int{-1, 200, 302, 307, 309} {
		registry := NewRegistry()
		route := redirectStatusRoute("redirect.status.invalid.route.old", status)
		if _, err := registry.Publish(Publication{
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}}},
		}); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("status %d error = %v", status, err)
		}
	}

	nonRedirect := pluginRoute("redirect.status.invalid.route.add", "/add", 0, http.MethodGet)
	nonRedirect.StatusCode = http.StatusMovedPermanently
	if _, err := NewRegistry().Publish(Publication{
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{nonRedirect}}},
	}); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("non-redirect status error = %v", err)
	}
}

func redirectStatusRoute(id string, status int) extensionmanifest.ManifestRoute {
	route := pluginRoute(id, "/old", 0, http.MethodGet)
	route.Action = extensionmanifest.RouteActionRedirect
	route.Guard = extensionmanifest.GuardCorePublic
	route.Handler = ""
	route.RequestSchema = ""
	route.ResponseSchema = ""
	route.Destination = "/new"
	route.StatusCode = status
	return route
}
