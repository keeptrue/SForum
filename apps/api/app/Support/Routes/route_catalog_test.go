package routes

import (
	"errors"
	"slices"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRouteCatalogSnapshotMarksCoreAdmittedAndConflictKinds(t *testing.T) {
	registry := NewRegistry()
	core := CoreRoute{ID: "core.route.topics", ContractVersion: "core.topics@1", Method: "GET", Path: "/api/v1/topics"}
	_, err := registry.Publish(Publication{
		Core: []CoreRoute{core, {ID: "core.route.settle", ContractVersion: "core.settle@1", Method: "POST", Path: "/api/v1/payments/:id/settle"}},
		Plugins: []PluginRouteSet{{Artifact: routeArtifact("demo.pay", "2.0.1", 'a'), Routes: []extensionmanifest.ManifestRoute{
			modifierRoute("demo.pay.settle", "core.route.settle", "/api/v1/payments/:id/settle", extensionmanifest.RouteActionReplace, "POST", 0),
			pluginRoute("demo.pay.webhook", "/api/v1/webhooks/demo", 0, "POST"),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewProviderSelectionCatalog(NewProviderSelectionAPI(registry, nil))
	if catalog == nil {
		t.Fatal("catalog should derive from a configured selection API")
	}
	snapshot, err := catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || snapshot.SafeMode || len(snapshot.Routes) != 4 {
		t.Fatalf("snapshot = rev %d safeMode %v routes %d", snapshot.Revision, snapshot.SafeMode, len(snapshot.Routes))
	}
	byID := make(map[string]RouteCatalogRoute)
	for _, route := range snapshot.Routes {
		if route.Provider.Kind == ProviderCore && !route.Admitted {
			t.Fatalf("core route %s must always be admitted", route.ID)
		}
		byID[route.ID+"\x00"+route.Method] = route
	}
	settleRoute, ok := byID["demo.pay.settle\x00POST"]
	if !ok {
		t.Fatal("replace route missing from catalog")
	}
	baseRoute := byID["core.route.settle\x00POST"]
	if !slices.Contains(settleRoute.ConflictKinds, ConflictProviderSelection) {
		t.Fatalf("replace route conflict kinds = %v", settleRoute.ConflictKinds)
	}
	if !slices.Contains(baseRoute.ConflictKinds, ConflictProviderSelection) {
		t.Fatalf("conflict candidates must share the conflict kind, got %v", baseRoute.ConflictKinds)
	}
	webhookRoute := byID["demo.pay.webhook\x00POST"]
	if len(webhookRoute.ConflictKinds) != 0 {
		t.Fatalf("webhook route should have no conflicts, got %v", webhookRoute.ConflictKinds)
	}
}

func TestRouteCatalogSnapshotRespectsPluginAdmission(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Publish(Publication{
		Core: []CoreRoute{{ID: "core.route.ping", ContractVersion: "core.ping@1", Method: "GET", Path: "/api/v1/ping"}},
		Plugins: []PluginRouteSet{
			{Artifact: routeArtifact("demo.in", "1.0.0", 'a'), Routes: []extensionmanifest.ManifestRoute{
				pluginRoute("demo.in.route", "/api/v1/in", 0, "GET"),
			}},
			{Artifact: routeArtifact("demo.out", "0.9.0", 'b'), Routes: []extensionmanifest.ManifestRoute{
				pluginRoute("demo.out.route", "/api/v1/out", 0, "GET"),
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.WithPluginAdmission(func(artifact PluginArtifact) bool {
		return artifact.ExtensionID != "demo.out"
	})
	catalog := NewProviderSelectionCatalog(NewProviderSelectionAPI(registry, nil))
	snapshot, err := catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	admittedSeen, pendingSeen := false, false
	for _, route := range snapshot.Routes {
		switch route.Provider.Artifact.ExtensionID {
		case "demo.in":
			if !route.Admitted {
				t.Fatal("admitted plugin route must report admitted=true")
			}
			admittedSeen = true
		case "demo.out":
			if route.Admitted {
				t.Fatal("non-admitted plugin route must report admitted=false")
			}
			pendingSeen = true
		}
	}
	if !admittedSeen || !pendingSeen {
		t.Fatalf("catalog routes incomplete: admitted=%v pending=%v", admittedSeen, pendingSeen)
	}
}

func TestRouteCatalogSnapshotRejectsUnconfiguredOwner(t *testing.T) {
	if _, err := (*Catalog)(nil).Snapshot(); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("nil catalog err = %v", err)
	}
	if _, err := NewProviderSelectionCatalog(nil).Snapshot(); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("nil providers err = %v", err)
	}
}

func TestRouteCatalogSnapshotKeepsRouteSortStable(t *testing.T) {
	registry := NewRegistry()
	high := pluginRoute("demo.sort.high", "/api/v1/high", 10, "GET")
	_, err := registry.Publish(Publication{
		Core:     []CoreRoute{{ID: "core.route.zebra", ContractVersion: "core.zebra@1", Method: "GET", Path: "/api/v1/zebra"}},
		Plugins:  []PluginRouteSet{{Artifact: routeArtifact("demo.sort", "1.0.0", 'a'), Routes: []extensionmanifest.ManifestRoute{high, pluginRoute("demo.sort.low", "/api/v1/low", 0, "GET")}}},
		SafeMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewProviderSelectionCatalog(NewProviderSelectionAPI(registry, nil))
	snapshot, err := catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.SafeMode {
		t.Fatal("catalog must carry the safe mode flag")
	}
	for index := 1; index < len(snapshot.Routes); index++ {
		left, right := snapshot.Routes[index-1], snapshot.Routes[index]
		if routeSortLess(right.Route, left.Route) {
			t.Fatalf("routes not sorted at %d: %s before %s", index, right.ID, left.ID)
		}
	}
}
