package routes

import (
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// M10 行 3：中间件动作词表的完整顺序与**方法过滤**对比。
//
// 既有 TestExecutionPlanBuildsDeterministicCompletePhaseChain 覆盖了安全方法（GET）
// 下的 global → before → filter → wrap → handler → after 顺序；本测试补上它没有的
// 一半：
//  1. 不安全方法（POST）下同一词表仍按同一顺序排列，且计划被标记为 unsafe；
//  2. 全局中间件对**所有**方法生效，而 before/filter/wrap/after 只在声明的那个方法上生效
//     ——这是 execution_plan.go:216 把 global 排除在方法匹配之外的结果，作者最容易踩错。
func TestExecutionPlanOrdersModifierPhasesForSafeAndUnsafeMethods(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("plan.methods", "2.0.0", 'b')
	targetID := "core.route.plan.methods"
	getTargetID := "core.route.plan.methods.get"
	path := "/plan-methods/:slug"

	// 同一份声明里，POST 专用修饰器与 GET 专用修饰器并存，全局中间件不声明方法。
	global := executionGlobalRoute("plan.methods.global", 50)
	postBefore := modifierRoute("plan.methods.before_post", targetID, path, extensionmanifest.RouteActionBefore, "POST", 40)
	postFilter := modifierRoute("plan.methods.filter_post", targetID, path, extensionmanifest.RouteActionFilter, "POST", 30)
	postWrap := modifierRoute("plan.methods.wrap_post", targetID, path, extensionmanifest.RouteActionWrap, "POST", 20)
	postAfter := modifierRoute("plan.methods.after_post", targetID, path, extensionmanifest.RouteActionAfter, "POST", 10)
	getBefore := modifierRoute("plan.methods.before_get", getTargetID, path, extensionmanifest.RouteActionBefore, "GET", 40)
	getAfter := modifierRoute("plan.methods.after_get", getTargetID, path, extensionmanifest.RouteActionAfter, "GET", 10)

	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{
			coreRoute(targetID, "POST", path),
			coreRoute(getTargetID, "GET", path),
		},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{
			postAfter, getAfter, global, postWrap, getBefore, postFilter, postBefore,
		}}},
	}); err != nil {
		t.Fatal(err)
	}

	postPlan, err := registry.BuildExecutionPlan("POST", "/plan-methods/welcome")
	if err != nil {
		t.Fatalf("POST plan: %v", err)
	}
	if !postPlan.UnsafeMethod() {
		t.Fatal("POST must be marked as an unsafe method plan")
	}
	assertPhaseOrder(t, postPlan, []struct {
		id    string
		phase RouteExecutionPhase
	}{
		{"plan.methods.global", RoutePhaseGlobal},
		{"plan.methods.before_post", RoutePhaseBefore},
		{"plan.methods.filter_post", RoutePhaseFilter},
		{"plan.methods.wrap_post", RoutePhaseWrap},
		{targetID, RoutePhaseHandler},
		{"plan.methods.after_post", RoutePhaseAfter},
	})
	assertChainLacks(t, postPlan, "plan.methods.before_get", "plan.methods.after_get")

	getPlan, err := registry.BuildExecutionPlan("GET", "/plan-methods/welcome")
	if err != nil {
		t.Fatalf("GET plan: %v", err)
	}
	if getPlan.UnsafeMethod() {
		t.Fatal("GET must not be marked as an unsafe method plan")
	}
	// 全局中间件在 GET 计划里同样存在（不参与方法匹配），而 POST 专用修饰器不出现。
	assertPhaseOrder(t, getPlan, []struct {
		id    string
		phase RouteExecutionPhase
	}{
		{"plan.methods.global", RoutePhaseGlobal},
		{"plan.methods.before_get", RoutePhaseBefore},
		{getTargetID, RoutePhaseHandler},
		{"plan.methods.after_get", RoutePhaseAfter},
	})
	assertChainLacks(t, getPlan, "plan.methods.before_post", "plan.methods.filter_post",
		"plan.methods.wrap_post", "plan.methods.after_post")
}

func assertPhaseOrder(t *testing.T, plan RouteExecutionPlan, want []struct {
	id    string
	phase RouteExecutionPhase
}) {
	t.Helper()
	chain := plan.Chain()
	if len(chain) != len(want) {
		got := make([]string, 0, len(chain))
		for _, step := range chain {
			got = append(got, step.RouteID+"("+string(step.Phase)+")")
		}
		t.Fatalf("chain = %v; want %d steps", got, len(want))
	}
	handlers := 0
	for index, step := range chain {
		if step.RouteID != want[index].id || step.Phase != want[index].phase {
			t.Fatalf("step %d = %s(%s); want %s(%s)", index, step.RouteID, step.Phase, want[index].id, want[index].phase)
		}
		if step.Phase == RoutePhaseHandler {
			handlers++
		}
	}
	if handlers != 1 {
		t.Fatalf("handler steps = %d; want exactly 1", handlers)
	}
}

func assertChainLacks(t *testing.T, plan RouteExecutionPlan, ids ...string) {
	t.Helper()
	present := map[string]bool{}
	for _, step := range plan.Chain() {
		present[step.RouteID] = true
	}
	for _, id := range ids {
		if present[id] {
			t.Fatalf("%s must not appear in a %s plan", id, plan.Method())
		}
	}
}
