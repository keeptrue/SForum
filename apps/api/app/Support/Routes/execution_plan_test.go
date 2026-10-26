package routes

import (
	"errors"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestExecutionPlanBuildsDeterministicCompletePhaseChain(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("plan.chain", "1.2.3", 'a')
	targetID := "core.route.plan.topic"
	globalHigh := executionGlobalRoute("plan.chain.global_high", 90)
	globalLow := executionGlobalRoute("plan.chain.global_low", 10)
	beforeHigh := modifierRoute("plan.chain.before_high", targetID, "/topics/:slug", extensionmanifest.RouteActionBefore, "GET", 80)
	beforeHigh.Guard = extensionmanifest.GuardCorePermission
	beforeHigh.Permission = "forum.topic.view"
	beforeHigh.Fallback = "readonly_core"
	beforeHigh.TimeoutMS = 1250
	beforeLow := modifierRoute("plan.chain.before_low", targetID, "/topics/:slug", extensionmanifest.RouteActionBefore, "GET", 20)
	filter := modifierRoute("plan.chain.filter", targetID, "/topics/:slug", extensionmanifest.RouteActionFilter, "GET", 70)
	wrap := modifierRoute("plan.chain.wrap", targetID, "/topics/:slug", extensionmanifest.RouteActionWrap, "GET", 60)
	afterHigh := modifierRoute("plan.chain.after_high", targetID, "/topics/:slug", extensionmanifest.RouteActionAfter, "GET", 50)
	afterLow := modifierRoute("plan.chain.after_low", targetID, "/topics/:slug", extensionmanifest.RouteActionAfter, "GET", 5)
	_, err := registry.Publish(Publication{
		Core: []CoreRoute{coreRoute(targetID, "GET", "/topics/:slug")},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{
			beforeLow, globalLow, afterLow, wrap, globalHigh, beforeHigh, filter, afterHigh,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := registry.BuildExecutionPlan("GET", "/topics/welcome")
	if err != nil {
		t.Fatal(err)
	}
	chain := plan.Chain()
	wantIDs := []string{
		"plan.chain.global_high", "plan.chain.global_low",
		"plan.chain.before_high", "plan.chain.before_low",
		"plan.chain.filter", "plan.chain.wrap", targetID,
		"plan.chain.after_high", "plan.chain.after_low",
	}
	wantPhases := []RouteExecutionPhase{
		RoutePhaseGlobal, RoutePhaseGlobal, RoutePhaseBefore, RoutePhaseBefore,
		RoutePhaseFilter, RoutePhaseWrap, RoutePhaseHandler, RoutePhaseAfter, RoutePhaseAfter,
	}
	if len(chain) != len(wantIDs) {
		t.Fatalf("chain=%#v", chain)
	}
	handlers := 0
	for index, step := range chain {
		if step.RouteID != wantIDs[index] || step.Phase != wantPhases[index] {
			t.Fatalf("step %d=%#v want id=%s phase=%s", index, step, wantIDs[index], wantPhases[index])
		}
		if step.Phase == RoutePhaseHandler {
			handlers++
		}
	}
	if handlers != 1 || plan.Terminal().RouteID != targetID || plan.Terminal().Provider.Kind != ProviderCore {
		t.Fatalf("terminal=%#v handlers=%d", plan.Terminal(), handlers)
	}
	before := chain[2]
	if before.Provider.Kind != ProviderPlugin || before.Provider.Artifact != artifact ||
		before.Action != extensionmanifest.RouteActionBefore || before.Access != "permission" ||
		before.Guard != extensionmanifest.GuardCorePermission || before.Permission != "forum.topic.view" ||
		before.RequestSchema == "" || before.ResponseSchema == "" || before.TimeoutMS != 1250 ||
		before.Fallback != "readonly_core" || before.Priority != 80 {
		t.Fatalf("step lost execution metadata: %#v", before)
	}
	if plan.Revision() != 1 || plan.Method() != "GET" || plan.Path() != "/topics/welcome" || plan.Params()["slug"] != "welcome" || plan.UnsafeMethod() {
		t.Fatalf("plan identity=%d %s %s %#v unsafe=%v", plan.Revision(), plan.Method(), plan.Path(), plan.Params(), plan.UnsafeMethod())
	}
	if !plan.Valid() || !plan.AllowsFallback(2, RouteCommitPristine) || plan.AllowsFallback(6, RouteCommitPristine) {
		t.Fatalf("step fallback policy is not bound to its declaration")
	}
}

func TestExecutionPlanSupportsEveryTerminalAction(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("plan.actions", "1.0.0", 'b')
	add := pluginRoute("plan.actions.add", "/custom", 0, "GET")
	// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 不再是可登记的路由守卫，
	// raw 权限改由包内显式声明的 kind=raw_request 守卫承载。
	add.Guard = "plan.actions.raw_guard"
	alias := pluginRoute("plan.actions.alias", "/alias", 0, "GET")
	alias.Action, alias.TargetID, alias.Handler, alias.ResponseSchema = extensionmanifest.RouteActionAlias, "core.route.plan.home", "", ""
	alias.Guard = extensionmanifest.GuardCoreInherit
	redirect := pluginRoute("plan.actions.redirect", "/old", 0, "GET")
	redirect.Action, redirect.Handler, redirect.ResponseSchema, redirect.Destination = extensionmanifest.RouteActionRedirect, "", "", "/new"
	rewrite := pluginRoute("plan.actions.rewrite", "/rewrite", 0, "GET")
	rewrite.Action, rewrite.TargetID, rewrite.Handler, rewrite.ResponseSchema = extensionmanifest.RouteActionRewrite, "core.route.plan.home", "", ""
	rewrite.Guard = extensionmanifest.GuardCoreInherit
	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{coreRoute("core.route.plan.home", "GET", "/home")},
		Plugins: []PluginRouteSet{{
			Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{add, alias, redirect, rewrite},
			Guards: []extensionmanifest.ManifestGuard{pluginGuard(add.Guard, "raw_request")},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	// routeExecutionAccess 仍保留 Host raw 守卫的描述性标签（只用于结构相等/旧制品比对），
	// 这里直接锁住该映射，避免它被静默删除。
	if got := routeExecutionAccess(extensionmanifest.GuardCoreRaw, ProviderPlugin); got != "raw_request" {
		t.Fatalf("core raw_request access label = %q", got)
	}
	tests := []struct {
		path        string
		action      string
		target      string
		destination string
		targetPath  string
		access      string
		guardKind   string
	}{
		{"/custom", extensionmanifest.RouteActionAdd, "", "", "", "custom", "raw_request"},
		{"/alias", extensionmanifest.RouteActionAlias, "core.route.plan.home", "", "/home", "inherit", ""},
		{"/old", extensionmanifest.RouteActionRedirect, "", "/new", "", "public", ""},
		{"/rewrite", extensionmanifest.RouteActionRewrite, "core.route.plan.home", "", "/home", "inherit", ""},
	}
	for _, test := range tests {
		plan, err := registry.BuildExecutionPlan("GET", test.path)
		if err != nil {
			t.Fatalf("%s: %v", test.path, err)
		}
		terminal := plan.Terminal()
		if len(plan.Chain()) != 1 || terminal.Action != test.action || terminal.TargetID != test.target ||
			terminal.Destination != test.destination || terminal.TargetPath != test.targetPath ||
			terminal.Provider.Artifact != artifact || terminal.Access != test.access ||
			terminal.PluginGuard.Kind != test.guardKind {
			t.Fatalf("%s terminal=%#v chain=%#v", test.path, terminal, plan.Chain())
		}
	}
}

func TestExecutionPlanMaterializesAliasAndRewriteTargetParameters(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("plan.mapping", "1.0.0", 'b')
	target := coreRoute("core.route.plan.mapping", "GET", "/topics/:topicID/*rest")
	alias := pluginRoute("plan.mapping.alias", "/legacy/:id/*path", 0, "GET")
	alias.Action, alias.TargetID, alias.Handler, alias.ResponseSchema = extensionmanifest.RouteActionAlias, target.ID, "", ""
	alias.Guard = extensionmanifest.GuardCoreInherit
	rewrite := alias
	rewrite.ID, rewrite.ContractVersion, rewrite.Action, rewrite.Path = "plan.mapping.rewrite", "plan.mapping.rewrite@1", extensionmanifest.RouteActionRewrite, "/internal/:id/*path"
	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{alias, rewrite}}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/legacy/42/comments/7", "/internal/42/comments/7"} {
		plan, err := registry.BuildExecutionPlan("GET", path)
		if err != nil || plan.Terminal().TargetPath != "/topics/42/comments/7" {
			t.Fatalf("path %s target = %q, %v", path, plan.Terminal().TargetPath, err)
		}
	}
}

func TestExecutionPlanCopiesExactInheritedCoreGuard(t *testing.T) {
	registry := NewRegistry()
	target := coreRoute("core.route.plan.protected", "PATCH", "/protected/:id")
	target.Guard = CoreGuardDescriptor{
		RouteID: target.ID, ContractVersion: target.ContractVersion, Method: target.Method,
		Kind: CoreGuardContextual, Permissions: []string{"topic.edit_own", "topic.edit_any"},
		EvaluatorID: "core.guard.forum.topic_edit",
	}
	replacement := modifierRoute("plan.guard.replace", target.ID, target.Path, extensionmanifest.RouteActionReplace, target.Method, 100)
	replacement.Guard = extensionmanifest.GuardCoreInherit
	artifact := routeArtifact("plan.guard", "1.0.0", 'a')
	if _, err := registry.Publish(Publication{Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}}}); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	plugin := executionRouteByAction(t, snapshot, extensionmanifest.RouteActionReplace)
	plan, err := buildRouteExecutionPlan(snapshot, Match{Revision: snapshot.Revision, Route: plugin, Params: map[string]string{"id": "7"}}, "PATCH", "/protected/7")
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Terminal().CoreGuard
	if !equalCoreGuardDescriptor(got, target.Guard) || got.RouteID != target.ID || got.ContractVersion != target.ContractVersion || got.Method != target.Method {
		t.Fatalf("inherited guard = %#v", got)
	}
	got.Permissions[0] = "mutated"
	var snapshotGuard CoreGuardDescriptor
	for _, route := range registry.Snapshot().Routes {
		if route.Provider.Kind == ProviderCore && route.ID == target.ID {
			snapshotGuard = route.CoreGuard
		}
	}
	if plan.Terminal().CoreGuard.Permissions[0] != "topic.edit_own" || snapshotGuard.Permissions[0] != "topic.edit_own" {
		t.Fatal("inherited descriptor escaped immutable plan/snapshot copies")
	}
}

func TestExecutionPlanInheritedGuardFailsClosedOnMissingDriftAndPluginTarget(t *testing.T) {
	t.Run("missing descriptor", func(t *testing.T) {
		registry := NewRegistry()
		target := coreRoute("core.route.plan.unreviewed", "GET", "/unreviewed")
		target.Guard = CoreGuardDescriptor{}
		alias := pluginRoute("plan.guard.alias", "/alias-unreviewed", 0, "GET")
		alias.Action, alias.TargetID, alias.Handler, alias.ResponseSchema, alias.Guard = extensionmanifest.RouteActionAlias, target.ID, "", "", extensionmanifest.GuardCoreInherit
		if _, err := registry.Publish(Publication{Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{Artifact: routeArtifact("plan.guard", "1.0.0", 'b'), Routes: []extensionmanifest.ManifestRoute{alias}}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.BuildExecutionPlan("GET", "/alias-unreviewed"); !errors.Is(err, ErrInvalidExecutionPlan) {
			t.Fatalf("missing descriptor error = %v", err)
		}
	})

	t.Run("descriptor identity drift", func(t *testing.T) {
		target := coreRoute("core.route.plan.drift", "GET", "/drift")
		target.Guard.ContractVersion = "sforum.route.plan.drift@2"
		if _, err := NewRegistry().Publish(Publication{Core: []CoreRoute{target}}); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("drift error = %v", err)
		}
	})

	t.Run("plugin target", func(t *testing.T) {
		registry := NewRegistry()
		artifact := routeArtifact("plan.plugin_target", "1.0.0", 'c')
		base := pluginRoute("plan.plugin_target.base", "/plugin-base", 0, "GET")
		alias := pluginRoute("plan.plugin_target.alias", "/plugin-alias", 0, "GET")
		alias.Action, alias.TargetID, alias.Handler, alias.ResponseSchema, alias.Guard = extensionmanifest.RouteActionAlias, base.ID, "", "", extensionmanifest.GuardCoreInherit
		if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{base, alias}}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.BuildExecutionPlan("GET", "/plugin-alias"); !errors.Is(err, ErrInvalidExecutionPlan) {
			t.Fatalf("plugin inheritance error = %v", err)
		}
	})
}

func TestExecutionPlanReplacementRequiresOneExplicitWinner(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("plan.replace", "1.0.0", 'c')
	replacement := modifierRoute(
		"plan.replace.writer", "core.route.plan.create", "/topics",
		extensionmanifest.RouteActionReplace, "POST", 100,
	)
	if _, err := registry.Publish(Publication{
		Core:    []CoreRoute{coreRoute("core.route.plan.create", "POST", "/topics")},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.BuildExecutionPlan("POST", "/topics"); !errors.Is(err, ErrAmbiguousRoute) {
		t.Fatalf("unselected replacement did not fail closed: %v", err)
	}
	snapshot := registry.Snapshot()
	selected := executionRouteByAction(t, snapshot, extensionmanifest.RouteActionReplace)
	plan, err := buildRouteExecutionPlan(snapshot, Match{
		Revision: snapshot.Revision, Route: selected, Params: map[string]string{},
	}, "POST", "/topics")
	if err != nil {
		t.Fatalf("explicit unique replacement: %v", err)
	}
	if !plan.UnsafeMethod() || plan.Terminal().Action != extensionmanifest.RouteActionReplace || plan.Terminal().Provider.Artifact != artifact {
		t.Fatalf("replacement plan=%#v", plan.Terminal())
	}

	secondArtifact := routeArtifact("plan.replace.second", "1.0.0", 'd')
	second := modifierRoute(
		"plan.replace.second.writer", "core.route.plan.create", "/topics",
		extensionmanifest.RouteActionReplace, "POST", 200,
	)
	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{coreRoute("core.route.plan.create", "POST", "/topics")},
		Plugins: []PluginRouteSet{
			{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}},
			{Artifact: secondArtifact, Routes: []extensionmanifest.ManifestRoute{second}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot = registry.Snapshot()
	selected = executionRouteByID(t, snapshot, "plan.replace.writer")
	if _, err := buildRouteExecutionPlan(snapshot, Match{
		Revision: snapshot.Revision, Route: selected, Params: map[string]string{},
	}, "POST", "/topics"); !errors.Is(err, ErrInvalidExecutionPlan) {
		t.Fatalf("multiple replacement providers did not fail closed: %v", err)
	}
}

func TestExecutionPlanFallbackAndSecondWriterFence(t *testing.T) {
	if (RouteExecutionPlan{}).AllowsWriter(RouteCommitPristine) {
		t.Fatal("zero execution plan allowed a writer")
	}
	tests := []struct {
		name     string
		method   string
		fallback string
		allowed  bool
	}{
		{"get readonly core", "GET", "readonly_core", true},
		{"head readonly core", "HEAD", "readonly_core", true},
		{"get not found", "GET", "not_found", true},
		{"get closed", "GET", "closed", false},
		{"unsafe post", "POST", "readonly_core", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			artifact := routeArtifact("plan.fallback", "1.0.0", 'e')
			method := test.method
			declaredMethod := method
			if method == "HEAD" {
				declaredMethod = "GET"
			}
			route := pluginRoute("plan.fallback.route", "/fallback", 0, declaredMethod)
			route.Fallback = test.fallback
			if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}}}}); err != nil {
				t.Fatal(err)
			}
			plan, err := registry.BuildExecutionPlan(method, "/fallback")
			if err != nil {
				t.Fatal(err)
			}
			if got := plan.AllowsFallback(0, RouteCommitPristine); got != test.allowed {
				t.Fatalf("fallback=%v want=%v plan=%#v", got, test.allowed, plan.Terminal())
			}
			for _, state := range []RouteExecutionCommitState{
				RouteCommitUnknown, RouteCommitResponseStarted, RouteCommitSideEffectStarted, RouteCommitFinal,
			} {
				if plan.AllowsFallback(0, state) || plan.AllowsWriter(state) {
					t.Fatalf("state %q allowed fallback/second writer", state)
				}
			}
			if !plan.AllowsWriter(RouteCommitPristine) || plan.UnsafeMethod() != (method == "POST") {
				t.Fatalf("writer/unsafe fence incorrect: unsafe=%v", plan.UnsafeMethod())
			}
		})
	}
}

func TestExecutionPlanStrictlyClonesInputsAndOutputs(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("plan.clone", "1.0.0", 'f')
	route := pluginRoute("plan.clone.item", "/items/:id", 5, "GET")
	route.Fallback = "not_found"
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}}}}); err != nil {
		t.Fatal(err)
	}
	match, err := registry.Resolve("GET", "/items/42")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	plan, err := buildRouteExecutionPlan(snapshot, match, "GET", "/items/42")
	if err != nil {
		t.Fatal(err)
	}

	match.Params["id"] = "mutated"
	match.Route.Handler = "mutated"
	snapshot.Routes[0].Handler = "mutated"
	params := plan.Params()
	params["id"] = "caller-mutated"
	chain := plan.Chain()
	chain[0].Handler = "caller-mutated"
	chain[0].Provider.Artifact.ExtensionID = "caller-mutated"
	if plan.Params()["id"] != "42" || plan.Terminal().Handler != "route.handle" ||
		plan.Terminal().Provider.Artifact != artifact {
		t.Fatalf("plan changed through caller-owned data: params=%#v terminal=%#v", plan.Params(), plan.Terminal())
	}

	oldRevision := plan.Revision()
	if _, err := registry.Publish(Publication{Core: []CoreRoute{coreRoute("core.route.plan.new", "GET", "/new")}}); err != nil {
		t.Fatal(err)
	}
	if plan.Revision() != oldRevision || plan.Terminal().RouteID != "plan.clone.item" {
		t.Fatalf("new publication mutated retained plan: revision=%d terminal=%#v", plan.Revision(), plan.Terminal())
	}
}

func TestExecutionPlanRejectsForgedOrAmbiguousMatchData(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("plan.invalid", "1.0.0", 'a')
	route := pluginRoute("plan.invalid.route", "/items/:id", 0, "GET")
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}}}}); err != nil {
		t.Fatal(err)
	}
	match, err := registry.Resolve("GET", "/items/1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	tests := []struct {
		name   string
		change func(*Snapshot, *Match)
	}{
		{"revision", func(_ *Snapshot, match *Match) { match.Revision++ }},
		{"candidates", func(_ *Snapshot, match *Match) { match.Candidates = []Route{match.Route} }},
		{"params", func(_ *Snapshot, match *Match) { match.Params["id"] = "2" }},
		{"terminal not snapshot", func(_ *Snapshot, match *Match) { match.Route.Handler = "forged" }},
		{"contribution not snapshot", func(_ *Snapshot, match *Match) {
			forged := match.Route
			forged.ID = "plan.invalid.forged"
			forged.Action = extensionmanifest.RouteActionBefore
			forged.TargetID = match.Route.ID
			match.Contributions = []Route{forged}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateSnapshot := snapshotViewForExecutionTest(snapshot)
			candidateMatch := cloneExecutionMatch(match)
			test.change(&candidateSnapshot, &candidateMatch)
			if _, err := buildRouteExecutionPlan(candidateSnapshot, candidateMatch, "GET", "/items/1"); !errors.Is(err, ErrInvalidExecutionPlan) {
				t.Fatalf("forged match accepted: %v", err)
			}
		})
	}
}

func executionGlobalRoute(id string, priority int) extensionmanifest.ManifestRoute {
	return extensionmanifest.ManifestRoute{
		ID: id, ContractVersion: id + "@1", Action: extensionmanifest.RouteActionGlobalMiddleware,
		Guard: extensionmanifest.GuardCoreLogin, Priority: priority, Fallback: "closed",
		Mode: extensionmanifest.RouteModeHTTP, Handler: "route.global",
		RequestSchema: id + ".request@1", ResponseSchema: id + ".response@1",
	}
}

func executionRouteByAction(t *testing.T, snapshot Snapshot, action string) Route {
	t.Helper()
	for _, route := range snapshot.Routes {
		if route.Action == action {
			return route
		}
	}
	t.Fatalf("route action %s not found", action)
	return Route{}
}

func executionRouteByID(t *testing.T, snapshot Snapshot, id string) Route {
	t.Helper()
	for _, route := range snapshot.Routes {
		if route.ID == id {
			return route
		}
	}
	t.Fatalf("route %s not found", id)
	return Route{}
}

func snapshotViewForExecutionTest(source Snapshot) Snapshot {
	result := source
	result.Routes = cloneRoutes(source.Routes)
	result.Conflicts = cloneConflicts(source.Conflicts)
	return result
}

func cloneExecutionMatch(source Match) Match {
	result := source
	result.Params = cloneRouteExecutionParams(source.Params)
	result.Contributions = cloneRoutes(source.Contributions)
	result.Candidates = cloneRoutes(source.Candidates)
	return result
}
