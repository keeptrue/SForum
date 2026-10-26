package extensionscontroller

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

type routeCatalogEnvelope struct {
	Data routeCatalogResponse `json:"data"`
}

func TestRouteCatalogHTTPReturnsRegistrySnapshotForViewers(t *testing.T) {
	app, _ := newRouteInspectorTestApp(t, true)
	for name, cookie := range map[string]*http.Cookie{
		"viewer":         loginRouteInspectorUser(t, app, 1),
		"legacy_manager": loginRouteInspectorUser(t, app, 2),
	} {
		t.Run(name, func(t *testing.T) {
			values := url.Values{}
			endpoint := "/api/v1/admin/extensions/route-catalog?" + values.Encode()
			response := performExtensionRequest(t, app, http.MethodGet, endpoint, cookie)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
			}
			defer response.Body.Close()
			var envelope routeCatalogEnvelope
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			data := envelope.Data
			if data.Revision == 0 || data.Total != len(data.Routes) || data.Total != 2 {
				t.Fatalf("catalog = rev %d total %d routes %d", data.Revision, data.Total, len(data.Routes))
			}
			var coreEntry, pluginEntry *routeCatalogEntryView
			for index := range data.Routes {
				switch data.Routes[index].ProviderKind {
				case routes.ProviderCore:
					coreEntry = &data.Routes[index]
				case routes.ProviderPlugin:
					pluginEntry = &data.Routes[index]
				}
			}
			if coreEntry == nil || pluginEntry == nil {
				t.Fatalf("catalog entries = %#v", data.Routes)
			}
			if !coreEntry.Admitted || coreEntry.Artifact != nil {
				t.Fatalf("core entry = %#v", *coreEntry)
			}
			if !pluginEntry.Admitted || pluginEntry.Artifact == nil || pluginEntry.Artifact.ExtensionID != "controller.route" {
				t.Fatalf("plugin entry = %#v", *pluginEntry)
			}
			if pluginEntry.Action != "replace" || pluginEntry.TargetRouteID != coreEntry.RouteID {
				t.Fatalf("plugin entry action = %s target = %s", pluginEntry.Action, pluginEntry.TargetRouteID)
			}
			foundSelectionConflict := false
			for _, entry := range data.Routes {
				for _, kind := range entry.ConflictKinds {
					if kind == string(routes.ConflictProviderSelection) {
						foundSelectionConflict = true
					}
				}
			}
			if !foundSelectionConflict {
				t.Fatal("replace conflict must surface in conflictKinds")
			}
			if pluginEntry.GuardRisk == "" {
				t.Fatal("guardRisk must be classified for every entry")
			}
		})
	}
}

func TestRouteCatalogHTTPRequiresAuthenticationAndExtensionView(t *testing.T) {
	app, _ := newRouteInspectorTestApp(t, true)
	endpoint := "/api/v1/admin/extensions/route-catalog"
	unauthenticated := performExtensionRequest(t, app, http.MethodGet, endpoint, nil)
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.StatusCode)
	}
	unauthenticated.Body.Close()

	denied := loginRouteInspectorUser(t, app, 3)
	response := performExtensionRequest(t, app, http.MethodGet, endpoint, denied)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("no-view status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()
}

func TestRouteCatalogHTTPFailsClosedWhenUnavailable(t *testing.T) {
	app, _ := newRouteInspectorTestApp(t, false)
	viewer := loginRouteInspectorUser(t, app, 1)
	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/route-catalog", viewer)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	defer response.Body.Close()
	var payload struct {
		Data struct {
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Reason != "extensions.route_catalog_unavailable" {
		t.Fatalf("reason = %q", payload.Data.Reason)
	}
}
