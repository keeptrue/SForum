package routes

import (
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// EX2-05：插件 global middleware 的作用域由 TargetID 显式声明。
//   - 留空 → 只覆盖插件自己的 terminal；
//   - 写明 → 精确覆盖那一条 terminal。
// Core 的 /api/v1 terminal 默认落在作用域之外，这是本文件的核心断言。

func TestGlobalMiddlewareSkipsCoreTerminalsByDefault(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("scope.default", "1.0.0", 'd')
	global := executionGlobalRoute("scope.default.global", 50)
	pluginAdd := pluginRoute("scope.default.add", "/scope-plugin", 0, "GET")
	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{
			coreRoute("core.route.scope.core", "GET", "/api/v1/scope-core"),
		},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{global, pluginAdd}}},
	}); err != nil {
		t.Fatal(err)
	}

	// denied：Core terminal 的链里没有插件的 global middleware。
	corePlan, err := registry.BuildExecutionPlan("GET", "/api/v1/scope-core")
	if err != nil {
		t.Fatal(err)
	}
	coreChain := corePlan.Chain()
	if len(coreChain) != 1 || coreChain[0].RouteID != "core.route.scope.core" || coreChain[0].Phase != RoutePhaseHandler {
		t.Fatalf("core chain picked up a plugin global middleware: %#v", coreChain)
	}

	// allowed：插件自己的 terminal 仍然吃到 global middleware，且它排在链头。
	pluginPlan, err := registry.BuildExecutionPlan("GET", "/scope-plugin")
	if err != nil {
		t.Fatal(err)
	}
	pluginChain := pluginPlan.Chain()
	if len(pluginChain) != 2 {
		t.Fatalf("plugin chain=%#v", pluginChain)
	}
	if pluginChain[0].RouteID != "scope.default.global" || pluginChain[0].Phase != RoutePhaseGlobal {
		t.Fatalf("global middleware is not the first step: %#v", pluginChain[0])
	}
	if pluginChain[1].RouteID != "scope.default.add" || pluginChain[1].Phase != RoutePhaseHandler {
		t.Fatalf("plugin terminal is not last: %#v", pluginChain[1])
	}
}

func TestGlobalMiddlewareCoversOnlyItsDeclaredCoreRoute(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("scope.targeted", "1.0.0", 'e')
	global := executionGlobalRoute("scope.targeted.global", 50)
	global.TargetID = "core.route.scope.declared"
	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{
			coreRoute("core.route.scope.declared", "GET", "/api/v1/scope-declared"),
			coreRoute("core.route.scope.other", "GET", "/api/v1/scope-other"),
		},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{global}}},
	}); err != nil {
		t.Fatal(err)
	}

	declared, err := registry.BuildExecutionPlan("GET", "/api/v1/scope-declared")
	if err != nil {
		t.Fatal(err)
	}
	declaredChain := declared.Chain()
	if len(declaredChain) != 2 || declaredChain[0].RouteID != "scope.targeted.global" ||
		declaredChain[0].Phase != RoutePhaseGlobal || declaredChain[1].RouteID != "core.route.scope.declared" {
		t.Fatalf("declared core terminal chain=%#v", declaredChain)
	}

	other, err := registry.BuildExecutionPlan("GET", "/api/v1/scope-other")
	if err != nil {
		t.Fatal(err)
	}
	otherChain := other.Chain()
	if len(otherChain) != 1 || otherChain[0].RouteID != "core.route.scope.other" {
		t.Fatalf("global middleware leaked to an undeclared core terminal: %#v", otherChain)
	}
}

// policy 快照（幂等/凭据变更策略）必须用同一份作用域判定，否则会出现「执行链没有
// 这一步，但策略快照仍然把它当成这一步在改凭据」的分裂。
func TestPolicySnapshotUsesGlobalMiddlewareScope(t *testing.T) {
	coreTerminal := Route{
		ID: "core.route.scope.policy", Method: "GET", Path: "/api/v1/scope-policy",
		Provider: Provider{Kind: ProviderCore},
	}
	pluginTerminal := Route{
		ID: "scope.policy.add", Method: "GET", Path: "/scope-policy",
		Provider: Provider{Kind: ProviderPlugin},
	}
	global := Route{
		ID: "scope.policy.global", Action: extensionmanifest.RouteActionGlobalMiddleware,
		Provider: Provider{Kind: ProviderPlugin},
	}
	if routePolicyContributionApplies(coreTerminal, global) {
		t.Fatal("policy snapshot let a plugin global middleware apply to a core terminal")
	}
	if !routePolicyContributionApplies(pluginTerminal, global) {
		t.Fatal("policy snapshot dropped a plugin global middleware from a plugin terminal")
	}
	declared := global
	declared.TargetID = coreTerminal.ID
	if !routePolicyContributionApplies(coreTerminal, declared) {
		t.Fatal("explicitly declared global middleware was not applied to its core terminal")
	}
	if routePolicyContributionApplies(
		Route{ID: "core.route.scope.other", Method: "GET", Provider: Provider{Kind: ProviderCore}},
		declared,
	) {
		t.Fatal("declared global middleware leaked to another core terminal")
	}
}
