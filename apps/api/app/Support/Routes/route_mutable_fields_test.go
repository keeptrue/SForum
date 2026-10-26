package routes

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRouteMutableFieldsPropagateThroughImmutableSnapshotsAndPlans(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("mutable.contract", "1.0.0", 'a')
	target := coreRoute("core.route.mutable.contract", "GET", "/mutable/:id")
	global := executionGlobalRoute("mutable.contract.global", 90)
	global.MutableRequestFields = []string{"/query"}
	before := modifierRoute("mutable.contract.before", target.ID, target.Path, extensionmanifest.RouteActionBefore, "GET", 80)
	before.MutableRequestFields = []string{"/body/title"}
	filter := modifierRoute("mutable.contract.filter", target.ID, target.Path, extensionmanifest.RouteActionFilter, "GET", 70)
	filter.MutableRequestFields = []string{"/headers/x-trace"}
	filter.MutableResponseFields = []string{"/headers/cache-control"}
	wrap := modifierRoute("mutable.contract.wrap", target.ID, target.Path, extensionmanifest.RouteActionWrap, "GET", 60)
	wrap.MutableRequestFields = []string{"/params/id"}
	wrap.MutableResponseFields = []string{"/body"}
	after := modifierRoute("mutable.contract.after", target.ID, target.Path, extensionmanifest.RouteActionAfter, "GET", 50)
	after.MutableResponseFields = []string{"/status"}
	publication := Publication{
		Core: []CoreRoute{target},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{
			global, before, filter, wrap, after,
		}}},
	}
	if _, err := registry.Publish(publication); err != nil {
		t.Fatal(err)
	}

	publication.Plugins[0].Routes[0].MutableRequestFields[0] = "/caller-mutated"
	assertMutableRouteFields(t, registry.Snapshot(), "mutable.contract.global", []string{"/query"}, nil)

	snapshot := registry.Snapshot()
	filterRoute := executionRouteByID(t, snapshot, "mutable.contract.filter")
	filterRoute.MutableRequestFields[0] = "/snapshot-mutated"
	assertMutableRouteFields(t, registry.Snapshot(), "mutable.contract.filter", []string{"/headers/x-trace"}, []string{"/headers/cache-control"})

	changed := cloneRoute(executionRouteByID(t, registry.Snapshot(), "mutable.contract.filter"))
	changed.MutableResponseFields[0] = "/changed"
	if equalRoute(changed, executionRouteByID(t, registry.Snapshot(), "mutable.contract.filter")) {
		t.Fatal("route equality ignored mutable field allowlists")
	}

	stored := registry.PublicationSnapshot()
	stored.Publication.Plugins[0].Routes[1].MutableRequestFields[0] = "/publication-mutated"
	again := registry.PublicationSnapshot()
	if got := again.Publication.Plugins[0].Routes[1].MutableRequestFields; !slices.Equal(got, []string{"/body/title"}) {
		t.Fatalf("publication snapshot mutable fields = %#v", got)
	}

	match, err := registry.Resolve("GET", "/mutable/42")
	if err != nil {
		t.Fatal(err)
	}
	match.Contributions[0].MutableRequestFields[0] = "/match-mutated"
	resolvedAgain, err := registry.Resolve("GET", "/mutable/42")
	if err != nil {
		t.Fatal(err)
	}
	if got := routeByID(t, resolvedAgain.Contributions, "mutable.contract.global").MutableRequestFields; !slices.Equal(got, []string{"/query"}) {
		t.Fatalf("resolved match mutable fields = %#v", got)
	}

	plan, err := registry.BuildExecutionPlan("GET", "/mutable/42")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		request  []string
		response []string
	}{
		"mutable.contract.global": {[]string{"/query"}, nil},
		"mutable.contract.before": {[]string{"/body/title"}, nil},
		"mutable.contract.filter": {[]string{"/headers/x-trace"}, []string{"/headers/cache-control"}},
		"mutable.contract.wrap":   {[]string{"/params/id"}, []string{"/body"}},
		"mutable.contract.after":  {nil, []string{"/status"}},
	}
	assertPlanMutableFields(t, plan, want)
	chain := plan.Chain()
	chain[0].MutableRequestFields[0] = "/plan-mutated"
	assertPlanMutableFields(t, plan, want)
	forged := planStepByID(t, plan, "mutable.contract.filter")
	forged.MutableResponseFields[0] = "/forged"
	if _, ok := uniqueRouteExecutionStepIndex(plan, forged); ok {
		t.Fatal("step authority equality ignored mutable field allowlists")
	}

	inspector := NewInspector(registry, nil, nil)
	inspection, err := inspector.Inspect(t.Context(), "GET", "/mutable/42")
	if err != nil {
		t.Fatal(err)
	}
	assertInspectorMutableFields(t, inspection, want)
	inspection.Chain[0].MutableRequestFields[0] = "/inspector-mutated"
	againInspection, err := inspector.Inspect(t.Context(), "GET", "/mutable/42")
	if err != nil {
		t.Fatal(err)
	}
	assertInspectorMutableFields(t, againInspection, want)
}

func TestRegistryRejectsInvalidRouteMutableFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*extensionmanifest.ManifestRoute)
	}{
		{"empty pointer", func(route *extensionmanifest.ManifestRoute) { route.MutableRequestFields = []string{""} }},
		{"missing slash", func(route *extensionmanifest.ManifestRoute) { route.MutableRequestFields = []string{"body"} }},
		{"invalid escape", func(route *extensionmanifest.ManifestRoute) { route.MutableRequestFields = []string{"/body/~2"} }},
		{"invalid request root", func(route *extensionmanifest.ManifestRoute) { route.MutableRequestFields = []string{"/status"} }},
		{"invalid query shape", func(route *extensionmanifest.ManifestRoute) {
			route.MutableRequestFields = []string{"/query/tag/0/child"}
		}},
		{"invalid params shape", func(route *extensionmanifest.ManifestRoute) {
			route.MutableRequestFields = []string{"/params/id/child"}
		}},
		{"non canonical header", func(route *extensionmanifest.ManifestRoute) { route.MutableRequestFields = []string{"/headers/X-Test"} }},
		{"host request header", func(route *extensionmanifest.ManifestRoute) {
			route.MutableRequestFields = []string{"/headers/idempotency-key"}
		}},
		{"filtered credential header", func(route *extensionmanifest.ManifestRoute) {
			route.MutableRequestFields = []string{"/headers/authorization"}
		}},
		{"invalid response status child", func(route *extensionmanifest.ManifestRoute) { route.MutableResponseFields = []string{"/status/code"} }},
		{"host response header", func(route *extensionmanifest.ManifestRoute) {
			route.MutableResponseFields = []string{"/headers/location"}
		}},
		{"duplicate", func(route *extensionmanifest.ManifestRoute) { route.MutableRequestFields = []string{"/body", "/body"} }},
		{"non canonical whitespace", func(route *extensionmanifest.ManifestRoute) { route.MutableRequestFields = []string{" /body "} }},
		{"pointer count", func(route *extensionmanifest.ManifestRoute) {
			route.MutableRequestFields = registryMutableFieldPointers("over", extensionmanifest.RouteMutableFieldsMaximumCount+1)
		}},
		{"pointer bytes", func(route *extensionmanifest.ManifestRoute) {
			route.MutableRequestFields = []string{"/body/" + strings.Repeat("a", extensionmanifest.RouteMutableFieldMaximumBytes-len("/body/")+1)}
		}},
		{"reference tokens", func(route *extensionmanifest.ManifestRoute) {
			route.MutableRequestFields = []string{"/body" + strings.Repeat("/token", extensionmanifest.RouteMutableFieldMaximumTokens)}
		}},
		{"request action", func(route *extensionmanifest.ManifestRoute) {
			route.Action = extensionmanifest.RouteActionAfter
			route.MutableRequestFields = []string{"/body"}
		}},
		{"response action", func(route *extensionmanifest.ManifestRoute) {
			route.Action = extensionmanifest.RouteActionBefore
			route.MutableResponseFields = []string{"/status"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			artifact := routeArtifact("mutable.invalid", "1.0.0", 'b')
			target := coreRoute("core.route.mutable.invalid", "GET", "/mutable-invalid/:id")
			route := modifierRoute("mutable.invalid.filter", target.ID, target.Path, extensionmanifest.RouteActionFilter, "GET", 10)
			test.mutate(&route)
			_, err := registry.Publish(Publication{
				Core:    []CoreRoute{target},
				Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}}},
			})
			if !errors.Is(err, ErrInvalidRoute) {
				t.Fatalf("invalid mutable fields error = %v", err)
			}
			if registry.Revision() != 0 {
				t.Fatalf("invalid publication advanced revision to %d", registry.Revision())
			}
		})
	}
}

func TestRegistryAcceptsExactRouteMutableFieldBudgets(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("mutable.budget", "1.0.0", 'c')
	target := coreRoute("core.route.mutable.budget", "GET", "/mutable-budget")
	route := modifierRoute("mutable.budget.filter", target.ID, target.Path, extensionmanifest.RouteActionFilter, "GET", 10)
	route.MutableRequestFields = registryMutableFieldPointers("request", extensionmanifest.RouteMutableFieldsMaximumCount)
	route.MutableResponseFields = registryMutableFieldPointers("response", extensionmanifest.RouteMutableFieldsMaximumCount)
	route.MutableRequestFields[0] = "/body/" + strings.Repeat("a", extensionmanifest.RouteMutableFieldMaximumBytes-len("/body/"))
	route.MutableResponseFields[0] = "/body" + strings.Repeat("/token", extensionmanifest.RouteMutableFieldMaximumTokens-1)
	if _, err := registry.Publish(Publication{
		Core:    []CoreRoute{target},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}}},
	}); err != nil {
		t.Fatalf("exact mutable field budgets rejected: %v", err)
	}
}

func TestRegistryMutableCredentialsRequireExactRawGuardBinding(t *testing.T) {
	artifact := routeArtifact("mutable.credentials", "1.0.0", 'd')
	target := coreRoute("core.route.mutable.credentials", "POST", "/mutable-credentials")
	base := modifierRoute(
		"mutable.credentials.filter", target.ID, target.Path,
		extensionmanifest.RouteActionFilter, "POST", 10,
	)
	base.MutableRequestFields = []string{"/headers/authorization"}

	// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 曾被任何路由用空插件
	// 守卫绑定借用，从而拿到凭证字段白名单。它现在必须在登记阶段就被拒绝。
	coreRaw := base
	coreRaw.Guard = extensionmanifest.GuardCoreRaw
	coreRawRegistry := NewRegistry()
	if _, err := coreRawRegistry.Publish(Publication{
		Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{coreRaw}}},
	}); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("core raw_request guard error = %v", err)
	}
	if coreRawRegistry.Revision() != 0 {
		t.Fatalf("refused core raw_request publication advanced revision to %d", coreRawRegistry.Revision())
	}

	customRaw := base
	customRaw.Guard = "mutable.credentials.guard.raw"
	rawGuard := pluginGuard(customRaw.Guard, "raw_request")
	snapshot, err := NewRegistry().Publish(Publication{
		Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{
			Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{customRaw}, Guards: []extensionmanifest.ManifestGuard{rawGuard},
		}},
	})
	if err != nil {
		t.Fatalf("exact custom raw_request guard rejected credential allowlist: %v", err)
	}
	if got := executionRouteByID(t, snapshot, customRaw.ID).PluginGuard.Kind; got != "raw_request" {
		t.Fatalf("frozen custom guard kind = %q", got)
	}

	filtered := base
	filtered.Guard = "mutable.credentials.guard.filtered"
	_, err = NewRegistry().Publish(Publication{
		Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{
			Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{filtered},
			Guards: []extensionmanifest.ManifestGuard{pluginGuard(filtered.Guard, "custom")},
		}},
	})
	if !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("custom filtered guard credential allowlist error = %v", err)
	}
}

func registryMutableFieldPointers(prefix string, count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = fmt.Sprintf("/body/%s-%d", prefix, index)
	}
	return values
}

func assertMutableRouteFields(t *testing.T, snapshot Snapshot, id string, request, response []string) {
	t.Helper()
	route := executionRouteByID(t, snapshot, id)
	if !slices.Equal(route.MutableRequestFields, request) || !slices.Equal(route.MutableResponseFields, response) {
		t.Fatalf("route %s mutable fields = %#v / %#v", id, route.MutableRequestFields, route.MutableResponseFields)
	}
}

func routeByID(t *testing.T, values []Route, id string) Route {
	t.Helper()
	for _, route := range values {
		if route.ID == id {
			return route
		}
	}
	t.Fatalf("route %s not found", id)
	return Route{}
}

func planStepByID(t *testing.T, plan RouteExecutionPlan, id string) RouteExecutionStep {
	t.Helper()
	for _, step := range plan.Chain() {
		if step.RouteID == id {
			return step
		}
	}
	t.Fatalf("plan step %s not found", id)
	return RouteExecutionStep{}
}

func assertPlanMutableFields(t *testing.T, plan RouteExecutionPlan, want map[string]struct {
	request  []string
	response []string
}) {
	t.Helper()
	seen := 0
	for _, step := range plan.Chain() {
		expected, ok := want[step.RouteID]
		if !ok {
			continue
		}
		seen++
		if !slices.Equal(step.MutableRequestFields, expected.request) || !slices.Equal(step.MutableResponseFields, expected.response) {
			t.Fatalf("plan step %s mutable fields = %#v / %#v", step.RouteID, step.MutableRequestFields, step.MutableResponseFields)
		}
	}
	if seen != len(want) {
		t.Fatalf("plan exposed %d mutable declarations, want %d", seen, len(want))
	}
}

func assertInspectorMutableFields(t *testing.T, snapshot RouteInspectorSnapshot, want map[string]struct {
	request  []string
	response []string
}) {
	t.Helper()
	seen := 0
	for _, step := range snapshot.Chain {
		expected, ok := want[step.RouteID]
		if !ok {
			continue
		}
		seen++
		if !slices.Equal(step.MutableRequestFields, expected.request) || !slices.Equal(step.MutableResponseFields, expected.response) {
			t.Fatalf("inspector step %s mutable fields = %#v / %#v", step.RouteID, step.MutableRequestFields, step.MutableResponseFields)
		}
	}
	if seen != len(want) {
		t.Fatalf("inspector exposed %d mutable declarations, want %d", seen, len(want))
	}
}
