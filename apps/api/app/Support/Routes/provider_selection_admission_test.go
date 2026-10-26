package routes

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestProviderSelectionPlanFiltersContributionsByExactRuntimeAdmission(t *testing.T) {
	registry := NewRegistry()
	writerArtifact := routeArtifact("selection.writer", "1.0.0", 'a')
	middlewareArtifact := routeArtifact("selection.middleware", "1.0.0", 'b')
	shadowArtifact := routeArtifact("selection.shadow", "1.0.0", 'c')
	target := coreRoute("core.route.selection.admission", "GET", "/selection/admission")
	replacement := modifierRoute(
		"selection.writer.replace", target.ID, target.Path,
		extensionmanifest.RouteActionReplace, target.Method, 100,
	)
	shadow := modifierRoute(
		"selection.shadow.replace", target.ID, target.Path,
		extensionmanifest.RouteActionReplace, target.Method, 90,
	)
	global := executionGlobalRoute("selection.middleware.global", 40)
	before := modifierRoute(
		"selection.middleware.before", target.ID, target.Path,
		extensionmanifest.RouteActionBefore, target.Method, 30,
	)

	var writerAdmitted atomic.Bool
	var middlewareAdmitted atomic.Bool
	writerAdmitted.Store(true)
	registry.WithPluginAdmission(func(artifact PluginArtifact) bool {
		switch artifact.ExtensionID {
		case writerArtifact.ExtensionID:
			return writerAdmitted.Load()
		case middlewareArtifact.ExtensionID:
			return middlewareAdmitted.Load()
		case shadowArtifact.ExtensionID:
			return false
		default:
			return false
		}
	})
	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{target},
		Plugins: []PluginRouteSet{
			{Artifact: writerArtifact, Routes: []extensionmanifest.ManifestRoute{replacement}},
			{Artifact: middlewareArtifact, Routes: []extensionmanifest.ManifestRoute{global, before}},
			{Artifact: shadowArtifact, Routes: []extensionmanifest.ManifestRoute{shadow}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	providers := NewProviderSelectionAPI(registry, newMemoryProviderSelectionStore())
	conflicts, err := providers.Conflicts(t.Context())
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts=%#v err=%v", conflicts, err)
	}
	selectionRequest := SelectProviderRequest{
		Key:                     conflicts[0].Key,
		ProviderRouteID:         replacement.ID,
		ProviderContractVersion: replacement.ContractVersion,
		ProviderArtifact:        writerArtifact,
		ActorUserID:             7,
		AuditEventID:            11,
	}
	selected, err := providers.Select(t.Context(), selectionRequest)
	if err != nil {
		t.Fatal(err)
	}

	assertChain := func(want ...string) {
		t.Helper()
		plan, err := providers.BuildExecutionPlan(t.Context(), target.Method, target.Path)
		if err != nil {
			t.Fatal(err)
		}
		chain := plan.Chain()
		got := make([]string, len(chain))
		for index := range chain {
			got[index] = chain[index].RouteID
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chain=%#v want=%#v", got, want)
		}
	}

	// The selected writer remains executable without reviving a drained
	// contribution from the same immutable Registry snapshot.
	assertChain(replacement.ID)

	middlewareAdmitted.Store(true)
	assertChain(global.ID, before.ID, replacement.ID)

	// Draining the selected writer returns resolution to Core while admitted
	// modifiers continue to compose around that Core target. A stale runtime
	// also cannot be written back as a fresh administrator selection.
	writerAdmitted.Store(false)
	selectionRequest.ExpectedRevision = selected.Revision
	selectionRequest.AuditEventID++
	if _, err := providers.Select(t.Context(), selectionRequest); !errors.Is(err, ErrProviderSelectionStale) {
		t.Fatalf("drained provider selection error=%v", err)
	}
	conflicts, err = providers.Conflicts(t.Context())
	if err != nil || len(conflicts) != 1 || conflicts[0].SelectionStatus != "stale" {
		t.Fatalf("drained provider conflicts=%#v err=%v", conflicts, err)
	}
	plan, err := providers.BuildExecutionPlan(t.Context(), target.Method, target.Path)
	if err != nil {
		t.Fatal(err)
	}
	chain := plan.Chain()
	if len(chain) != 3 || chain[0].RouteID != global.ID || chain[1].RouteID != before.ID ||
		chain[2].RouteID != target.ID || plan.Terminal().Provider.Kind != ProviderCore {
		t.Fatalf("core fallback chain=%#v", chain)
	}

	writerAdmitted.Store(true)
	middlewareAdmitted.Store(false)
	match, err := providers.Resolve(t.Context(), target.Method, target.Path)
	if err != nil || match.Route.ID != replacement.ID || len(match.Contributions) != 0 {
		t.Fatalf("selected match=%#v err=%v", match, err)
	}

	writerAdmitted.Store(false)
	if _, err := providers.Resolve(t.Context(), "POST", target.Path); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("unknown method error=%v", err)
	}
}
