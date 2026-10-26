package routes

import (
	"errors"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestLifecycleRoutePublicationRetainsInputAndHonorsAdmission(t *testing.T) {
	registry := NewRegistry()
	visible := false
	registry.WithPluginAdmission(func(artifact PluginArtifact) bool {
		return visible && artifact.RuntimeInstanceID == "runtime-2"
	})
	publication := Publication{Plugins: []PluginRouteSet{{
		Artifact: PluginArtifact{
			ExtensionID: "demo.routes", ExtensionVersion: "2.0.0",
			PackageDigest: strings.Repeat("b", 64), RuntimeInstanceID: "runtime-2",
		},
		Routes: []extensionmanifest.ManifestRoute{{
			ID: "demo.routes.read", ContractVersion: "demo.routes.read@1",
			Action: extensionmanifest.RouteActionAdd, Path: "/lifecycle-route",
			Methods: []string{"GET"}, Guard: extensionmanifest.GuardCorePublic,
			Mode: extensionmanifest.RouteModeHTTP, Fallback: "closed", Handler: "route.read",
			ResponseSchema: "demo.routes.read.response@1",
		}},
	}}}
	if _, err := registry.PublishIfRevision(publication, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("GET", "/lifecycle-route"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("drained route resolution = %v", err)
	}
	visible = true
	match, err := registry.Resolve("GET", "/lifecycle-route")
	if err != nil || match.Route.Provider.Artifact.RuntimeInstanceID != "runtime-2" {
		t.Fatalf("admitted route = %#v, %v", match, err)
	}
	snapshot := registry.PublicationSnapshot()
	snapshot.Publication.Plugins[0].Routes[0].Methods[0] = "POST"
	again := registry.PublicationSnapshot()
	if again.Publication.Plugins[0].Routes[0].Methods[0] != "GET" {
		t.Fatal("publication snapshot was not caller-owned")
	}
	if _, err := registry.PublishIfRevision(Publication{}, 0); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale route publication = %v", err)
	}
}
