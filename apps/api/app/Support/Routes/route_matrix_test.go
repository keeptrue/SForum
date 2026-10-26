package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// P6 路由动作矩阵（只读回归）：钉住生产已实现的 Registry/Plan/Dispatcher
// 行为，并冻结可变字段候选逐层传递及高优先级 wrap 最外层语义。完整 after
// fail-closed、redirect SEO/canonical、raw session/header authority 或组合 stream
// middleware 等语义由各自专项矩阵覆盖。

func TestP6RouteActionMatrixTerminals(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("matrix.actions", "1.0.0", 'a')
	home := coreRoute("core.route.matrix.home", "GET", "/matrix/home/:id")
	add := pluginRoute("matrix.actions.add", "/matrix/custom/:id", 0, "GET")
	alias := pluginRoute("matrix.actions.alias", "/matrix/legacy/:id", 0, "GET")
	alias.Action, alias.TargetID, alias.Handler, alias.ResponseSchema = extensionmanifest.RouteActionAlias, home.ID, "", ""
	alias.Guard = extensionmanifest.GuardCoreInherit
	redirect := pluginRoute("matrix.actions.redirect", "/matrix/old", 0, "GET")
	redirect.Action, redirect.Handler, redirect.ResponseSchema, redirect.Destination =
		extensionmanifest.RouteActionRedirect, "", "", "/matrix/new"
	rewrite := pluginRoute("matrix.actions.rewrite", "/matrix/internal/:id", 0, "GET")
	rewrite.Action, rewrite.TargetID, rewrite.Handler, rewrite.ResponseSchema =
		extensionmanifest.RouteActionRewrite, home.ID, "", ""
	rewrite.Guard = extensionmanifest.GuardCoreInherit

	if _, err := registry.Publish(Publication{
		Core:    []CoreRoute{home},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{add, alias, redirect, rewrite}}},
	}); err != nil {
		t.Fatal(err)
	}

	// 地址类动作：Registry 解析 + ExecutionPlan 终端身份。
	terminals := []struct {
		path        string
		action      string
		targetID    string
		destination string
		targetPath  string
	}{
		{"/matrix/custom/7", extensionmanifest.RouteActionAdd, "", "", ""},
		{"/matrix/legacy/7", extensionmanifest.RouteActionAlias, home.ID, "", "/matrix/home/7"},
		{"/matrix/old", extensionmanifest.RouteActionRedirect, "", "/matrix/new", ""},
		{"/matrix/internal/7", extensionmanifest.RouteActionRewrite, home.ID, "", "/matrix/home/7"},
	}
	for _, test := range terminals {
		match, err := registry.Resolve("GET", test.path)
		if err != nil || match.Route.Action != test.action || match.Route.Provider.Artifact != artifact {
			t.Fatalf("resolve %s = %#v, %v", test.path, match, err)
		}
		plan, err := registry.BuildExecutionPlan("GET", test.path)
		if err != nil {
			t.Fatalf("plan %s: %v", test.path, err)
		}
		terminal := plan.Terminal()
		if len(plan.Chain()) != 1 || terminal.Action != test.action || terminal.TargetID != test.targetID ||
			terminal.Destination != test.destination || terminal.TargetPath != test.targetPath {
			t.Fatalf("plan %s terminal=%#v chain=%#v", test.path, terminal, plan.Chain())
		}
	}

	// redirect：生产固定为 308 Permanent Redirect + Location，不声明 SEO/canonical 策略。
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: matrixPlanResolver{registry: registry}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/matrix/old"}, nil)
	if err != nil || !result.Handled || result.Response.Status != http.StatusPermanentRedirect ||
		result.Response.Headers.Get("Location") != "/matrix/new" {
		t.Fatalf("redirect dispatch = %#v err=%v", result, err)
	}

	// alias/rewrite：Dispatcher 走 CoreInvoker，不二次进入插件 runtime。
	coreCalls := 0
	core := &dispatchCoreInvoker{invoke: func(_ context.Context, step RouteExecutionStep, request DispatchRequest) (DispatchResponse, error) {
		coreCalls++
		if step.Action != extensionmanifest.RouteActionAlias && step.Action != extensionmanifest.RouteActionRewrite {
			t.Fatalf("unexpected core step action=%q", step.Action)
		}
		if request.Params["id"] != "7" {
			t.Fatalf("core params=%#v", request.Params)
		}
		return DispatchResponse{Status: http.StatusOK, Body: []byte(step.Action + ":" + step.TargetPath)}, nil
	}}
	for _, path := range []string{"/matrix/legacy/7", "/matrix/internal/7"} {
		result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: path}, core)
		if err != nil || !result.Handled || result.Response.Status != http.StatusOK {
			t.Fatalf("mapping %s = %#v err=%v", path, result, err)
		}
	}
	if coreCalls != 2 {
		t.Fatalf("core calls=%d", coreCalls)
	}

	// add：插件 handler 通过 StepInvoker 执行；无 Core 第二写者。
	addCore := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		t.Fatal("add must not invoke core writer")
		return DispatchResponse{}, nil
	}}
	addDispatcher := NewDispatcher(DispatcherConfig{
		Plans: matrixPlanResolver{registry: registry}, Steps: &dispatchStepInvoker{
			invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
				if input.Step.Action != extensionmanifest.RouteActionAdd || input.Step.RouteID != add.ID {
					t.Fatalf("add step=%#v", input.Step)
				}
				return RouteInvocationResult{Response: &DispatchResponse{Status: http.StatusCreated, Body: []byte("added")}}, nil
			},
		},
		Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
	})
	result, err = addDispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/matrix/custom/7"}, addCore)
	if err != nil || !result.Handled || result.Response.Status != http.StatusCreated || string(result.Response.Body) != "added" {
		t.Fatalf("add dispatch = %#v err=%v", result, err)
	}
}

func TestP6RouteModifierAndGlobalChainSelection(t *testing.T) {
	// before/after/filter/wrap 与 global middleware 均按声明进入链，并按不同 priority 降序排列。
	// wrap 高优先级在请求侧先进入、响应侧后退出，因此是最外层；filter/after 响应候选低到高传递。
	registry := NewRegistry()
	artifact := routeArtifact("matrix.chain", "1.0.0", 'b')
	targetID := "core.route.matrix.topic"
	targetPath := "/matrix/topics/:slug"
	globalHigh := executionGlobalRoute("matrix.chain.global_high", 40)
	globalLow := executionGlobalRoute("matrix.chain.global_low", 10)
	beforeHigh := modifierRoute("matrix.chain.before_high", targetID, targetPath, extensionmanifest.RouteActionBefore, "GET", 90)
	beforeLow := modifierRoute("matrix.chain.before_low", targetID, targetPath, extensionmanifest.RouteActionBefore, "GET", 20)
	filterHigh := modifierRoute("matrix.chain.filter_high", targetID, targetPath, extensionmanifest.RouteActionFilter, "GET", 70)
	filterLow := modifierRoute("matrix.chain.filter_low", targetID, targetPath, extensionmanifest.RouteActionFilter, "GET", 30)
	wrapHigh := modifierRoute("matrix.chain.wrap_high", targetID, targetPath, extensionmanifest.RouteActionWrap, "GET", 60)
	wrapLow := modifierRoute("matrix.chain.wrap_low", targetID, targetPath, extensionmanifest.RouteActionWrap, "GET", 15)
	afterHigh := modifierRoute("matrix.chain.after_high", targetID, targetPath, extensionmanifest.RouteActionAfter, "GET", 50)
	afterLow := modifierRoute("matrix.chain.after_low", targetID, targetPath, extensionmanifest.RouteActionAfter, "GET", 5)
	requestContributions := []*extensionmanifest.ManifestRoute{
		&globalHigh, &globalLow, &beforeHigh, &beforeLow, &filterHigh, &filterLow, &wrapHigh, &wrapLow,
	}
	for _, contribution := range requestContributions {
		contribution.MutableRequestFields = []string{"/body/requestMarkers"}
	}
	responseContributions := []*extensionmanifest.ManifestRoute{
		&filterHigh, &filterLow, &wrapHigh, &wrapLow, &afterHigh, &afterLow,
	}
	for _, contribution := range responseContributions {
		contribution.MutableResponseFields = []string{"/body/responseMarkers"}
	}
	// 另一目标的 filter/wrap 不得进入本请求链。
	otherFilter := modifierRoute("matrix.chain.other_filter", "core.route.matrix.other", "/matrix/other", extensionmanifest.RouteActionFilter, "GET", 100)
	otherWrap := modifierRoute("matrix.chain.other_wrap", "core.route.matrix.other", "/matrix/other", extensionmanifest.RouteActionWrap, "GET", 100)

	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{
			coreRoute(targetID, "GET", targetPath),
			coreRoute("core.route.matrix.other", "GET", "/matrix/other"),
		},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{
			globalLow, afterLow, wrapLow, filterLow, beforeLow, otherFilter, otherWrap,
			afterHigh, wrapHigh, filterHigh, beforeHigh, globalHigh,
		}}},
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := registry.BuildExecutionPlan("GET", "/matrix/topics/welcome")
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{
		"matrix.chain.global_high", "matrix.chain.global_low",
		"matrix.chain.before_high", "matrix.chain.before_low",
		"matrix.chain.filter_high", "matrix.chain.filter_low",
		"matrix.chain.wrap_high", "matrix.chain.wrap_low",
		targetID,
		"matrix.chain.after_high", "matrix.chain.after_low",
	}
	wantPhases := []RouteExecutionPhase{
		RoutePhaseGlobal, RoutePhaseGlobal,
		RoutePhaseBefore, RoutePhaseBefore,
		RoutePhaseFilter, RoutePhaseFilter,
		RoutePhaseWrap, RoutePhaseWrap,
		RoutePhaseHandler,
		RoutePhaseAfter, RoutePhaseAfter,
	}
	chain := plan.Chain()
	if len(chain) != len(wantIDs) {
		t.Fatalf("chain=%#v", chain)
	}
	gotIDs := make([]string, 0, len(chain))
	for index, step := range chain {
		gotIDs = append(gotIDs, step.RouteID)
		if step.RouteID != wantIDs[index] || step.Phase != wantPhases[index] {
			t.Fatalf("step %d = %#v want id=%s phase=%s", index, step, wantIDs[index], wantPhases[index])
		}
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("ids=%#v", gotIDs)
	}
	if plan.Terminal().RouteID != targetID || plan.Terminal().Provider.Kind != ProviderCore {
		t.Fatalf("terminal=%#v", plan.Terminal())
	}

	// Dispatcher request 侧高到低进入，wrap/filter/after response 侧低到高展开。
	// 每个贡献都写入不同 marker，并由下一层断言收到上一层已接受的 JSON 候选；
	// 因而这里同时证明数据流方向，而不只是记录调用顺序。
	order := make([]string, 0, len(chain)*2)
	requestMarker := map[string]string{
		globalHigh.ID: "global-high", globalLow.ID: "global-low",
		beforeHigh.ID: "before-high", beforeLow.ID: "before-low",
		filterHigh.ID: "filter-high-request", filterLow.ID: "filter-low-request",
		wrapHigh.ID: "wrap-high-request", wrapLow.ID: "wrap-low-request",
	}
	requestBefore := map[string][]string{
		globalHigh.ID: {"client"},
		globalLow.ID:  {"client", "global-high"},
		beforeHigh.ID: {"client", "global-high", "global-low"},
		beforeLow.ID:  {"client", "global-high", "global-low", "before-high"},
		filterHigh.ID: {"client", "global-high", "global-low", "before-high", "before-low"},
		filterLow.ID: {
			"client", "global-high", "global-low", "before-high", "before-low", "filter-high-request",
		},
		wrapHigh.ID: {
			"client", "global-high", "global-low", "before-high", "before-low",
			"filter-high-request", "filter-low-request",
		},
		wrapLow.ID: {
			"client", "global-high", "global-low", "before-high", "before-low",
			"filter-high-request", "filter-low-request", "wrap-high-request",
		},
	}
	responseMarker := map[string]string{
		wrapLow.ID: "wrap-low", wrapHigh.ID: "wrap-high",
		filterLow.ID: "filter-low", filterHigh.ID: "filter-high",
		afterLow.ID: "after-low", afterHigh.ID: "after-high",
	}
	responseBefore := map[string][]string{
		wrapLow.ID:  {"core"},
		wrapHigh.ID: {"core", "wrap-low"},
		filterLow.ID: {
			"core", "wrap-low", "wrap-high",
		},
		filterHigh.ID: {
			"core", "wrap-low", "wrap-high", "filter-low",
		},
		afterLow.ID: {
			"core", "wrap-low", "wrap-high", "filter-low", "filter-high",
		},
		afterHigh.ID: {
			"core", "wrap-low", "wrap-high", "filter-low", "filter-high", "after-low",
		},
	}
	invoker := &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
		order = append(order, string(input.Step.Phase)+":"+input.Step.RouteID+":"+string(input.Stage))
		switch input.Stage {
		case InvocationStageRequest:
			marker, ok := requestMarker[input.Step.RouteID]
			if !ok {
				t.Fatalf("unexpected request-stage route %q", input.Step.RouteID)
			}
			want := requestBefore[input.Step.RouteID]
			if got := matrixRouteBodyMarkers(t, input.Request.Body, "requestMarkers"); !reflect.DeepEqual(got, want) {
				t.Fatalf("request candidate for %s = %#v want=%#v", input.Step.RouteID, got, want)
			}
			candidate := append(append([]string(nil), want...), marker)
			return RouteInvocationResult{RequestPatch: []RoutePatchOperation{{
				Kind: RoutePatchReplace, Path: "/body/requestMarkers", Value: routePatchValue(t, candidate),
			}}}, nil
		case InvocationStageResponse:
			marker, ok := responseMarker[input.Step.RouteID]
			if !ok || input.Response == nil {
				t.Fatalf("unexpected response-stage input for %q: %#v", input.Step.RouteID, input.Response)
			}
			want := responseBefore[input.Step.RouteID]
			if got := matrixRouteBodyMarkers(t, input.Response.Body, "responseMarkers"); !reflect.DeepEqual(got, want) {
				t.Fatalf("response candidate for %s = %#v want=%#v", input.Step.RouteID, got, want)
			}
			candidate := append(append([]string(nil), want...), marker)
			return RouteInvocationResult{ResponsePatch: []RoutePatchOperation{{
				Kind: RoutePatchReplace, Path: "/body/responseMarkers", Value: routePatchValue(t, candidate),
			}}}, nil
		default:
			t.Fatalf("plugin contribution %q invoked at stage %q", input.Step.RouteID, input.Stage)
		}
		return RouteInvocationResult{}, nil
	}}
	core := &dispatchCoreInvoker{invoke: func(_ context.Context, step RouteExecutionStep, request DispatchRequest) (DispatchResponse, error) {
		order = append(order, string(step.Phase)+":"+step.RouteID+":"+string(InvocationStageHandler))
		want := append(append([]string(nil), requestBefore[wrapLow.ID]...), requestMarker[wrapLow.ID])
		if got := matrixRouteBodyMarkers(t, request.Body, "requestMarkers"); !reflect.DeepEqual(got, want) {
			t.Fatalf("core request candidate = %#v want=%#v", got, want)
		}
		return DispatchResponse{Status: http.StatusOK, Body: []byte(`{"responseMarkers":["core"]}`)}, nil
	}}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: matrixPlanResolver{registry: registry}, Steps: invoker, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
		Method: "GET", Path: "/matrix/topics/welcome", Body: []byte(`{"requestMarkers":["client"]}`),
	}, core)
	if err != nil || !result.Handled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	wantResponseMarkers := append(append([]string(nil), responseBefore[afterHigh.ID]...), responseMarker[afterHigh.ID])
	if got := matrixRouteBodyMarkers(t, result.Response.Body, "responseMarkers"); !reflect.DeepEqual(got, wantResponseMarkers) {
		t.Fatalf("final response markers=%#v want=%#v", got, wantResponseMarkers)
	}
	wantOrder := []string{
		"global:matrix.chain.global_high:request", "global:matrix.chain.global_low:request",
		"before:matrix.chain.before_high:request", "before:matrix.chain.before_low:request",
		"filter:matrix.chain.filter_high:request", "filter:matrix.chain.filter_low:request",
		"wrap:matrix.chain.wrap_high:request", "wrap:matrix.chain.wrap_low:request",
		"handler:" + targetID + ":handler",
		"wrap:matrix.chain.wrap_low:response", "wrap:matrix.chain.wrap_high:response",
		"filter:matrix.chain.filter_low:response", "filter:matrix.chain.filter_high:response",
		"after:matrix.chain.after_low:response", "after:matrix.chain.after_high:response",
	}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order=%#v want=%#v", order, wantOrder)
	}
}

func matrixRouteBodyMarkers(t *testing.T, body []byte, field string) []string {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode %s body %q: %v", field, body, err)
	}
	if len(document) != 1 {
		t.Fatalf("%s body has unexpected fields: %s", field, body)
	}
	raw, ok := document[field]
	if !ok {
		t.Fatalf("%s body is missing marker field: %s", field, body)
	}
	var markers []string
	if err := json.Unmarshal(raw, &markers); err != nil || markers == nil {
		t.Fatalf("decode %s markers %q: %#v err=%v", field, raw, markers, err)
	}
	return markers
}

// TestP6RouteSamePriorityOrderingPermutations 钉住合法 Registry 发布下同优先级、
// 同相位 contribution 的确定性次序：Plugins 与各插件 Routes 输入顺序变化不得改变
// BuildExecutionPlan 产出的有序 contribution ID。
func TestP6RouteSamePriorityOrderingPermutations(t *testing.T) {
	targetID := "core.route.matrix.order"
	targetPath := "/matrix/order/:id"
	const priority = 40

	type pluginRoutes struct {
		artifact PluginArtifact
		ids      []string
	}
	// 多 artifact、同 priority 的 before；route ID 必须落在 artifact.ExtensionID 前缀下。
	// 期望最终仅按 route ID 升序（priority 相同），与 Plugins/Routes 输入次序无关。
	defs := []pluginRoutes{
		{routeArtifact("matrix.order.plugin.b", "1.0.0", 'b'), []string{
			"matrix.order.plugin.b.bravo", "matrix.order.plugin.b.delta",
		}},
		{routeArtifact("matrix.order.plugin.a", "1.0.0", 'a'), []string{"matrix.order.plugin.a.alpha"}},
		{routeArtifact("matrix.order.plugin.c", "1.0.0", 'c'), []string{"matrix.order.plugin.c.charlie"}},
	}
	wantContribIDs := []string{
		"matrix.order.plugin.a.alpha",
		"matrix.order.plugin.b.bravo",
		"matrix.order.plugin.b.delta",
		"matrix.order.plugin.c.charlie",
	}

	// 若干 Plugins 顺序与 per-plugin Routes 顺序置换；均须产出相同有序 ID。
	permutations := [][]pluginRoutes{
		defs,
		{defs[2], defs[0], defs[1]},
		{defs[1], defs[2], defs[0]},
		{
			{defs[0].artifact, []string{defs[0].ids[1], defs[0].ids[0]}},
			defs[2],
			defs[1],
		},
		{
			{defs[2].artifact, append([]string(nil), defs[2].ids...)},
			{defs[0].artifact, []string{defs[0].ids[1], defs[0].ids[0]}},
			{defs[1].artifact, append([]string(nil), defs[1].ids...)},
		},
	}

	var baseline []string
	for index, perm := range permutations {
		plugins := make([]PluginRouteSet, 0, len(perm))
		for _, item := range perm {
			routes := make([]extensionmanifest.ManifestRoute, 0, len(item.ids))
			for _, id := range item.ids {
				routes = append(routes, modifierRoute(
					id, targetID, targetPath, extensionmanifest.RouteActionBefore, "GET", priority,
				))
			}
			plugins = append(plugins, PluginRouteSet{Artifact: item.artifact, Routes: routes})
		}

		registry := NewRegistry()
		if _, err := registry.Publish(Publication{
			Core:    []CoreRoute{coreRoute(targetID, "GET", targetPath)},
			Plugins: plugins,
		}); err != nil {
			t.Fatalf("perm %d publish: %v", index, err)
		}
		plan, err := registry.BuildExecutionPlan("GET", "/matrix/order/7")
		if err != nil {
			t.Fatalf("perm %d plan: %v", index, err)
		}

		got := make([]string, 0, len(wantContribIDs))
		for _, step := range plan.Chain() {
			if step.Phase == RoutePhaseBefore {
				got = append(got, step.RouteID)
			}
		}
		if index == 0 {
			if !reflect.DeepEqual(got, wantContribIDs) {
				t.Fatalf("baseline contrib ids=%#v want=%#v", got, wantContribIDs)
			}
			baseline = got
			continue
		}
		if !reflect.DeepEqual(got, baseline) {
			t.Fatalf("perm %d contrib ids=%#v baseline=%#v", index, got, baseline)
		}
	}
}

// TestP6RoutePriorityTieBreakOrder 用合成 Route 直接证明 sortExecutionRoutes
// 的完整 tie-break：priority 降序 → route ID 升序 → contract version 升序 →
// artifact extension ID 升序 → runtime instance ID 升序。
// 更深键位不通过非法 Registry 发布制造（合法 publication 下不可达或不独立）。
func TestP6RoutePriorityTieBreakOrder(t *testing.T) {
	mk := func(priority int, id, contractVersion, extensionID, runtimeInstanceID string) Route {
		return Route{
			ID: id, ContractVersion: contractVersion, Priority: priority,
			Provider: Provider{Kind: ProviderPlugin, Artifact: PluginArtifact{
				ExtensionID: extensionID, RuntimeInstanceID: runtimeInstanceID,
			}},
		}
	}

	// 故意打乱，覆盖每一层比较键。
	routes := []Route{
		mk(10, "b", "v1", "ext.a", "rt-1"),
		mk(30, "z", "v9", "ext.z", "rt-9"),
		mk(10, "a", "v2", "ext.b", "rt-2"),
		mk(10, "a", "v1", "ext.c", "rt-3"),
		mk(10, "a", "v1", "ext.b", "rt-5"),
		mk(10, "a", "v1", "ext.b", "rt-4"),
		mk(20, "m", "v1", "ext.m", "rt-1"),
	}
	sortExecutionRoutes(routes)

	want := []Route{
		mk(30, "z", "v9", "ext.z", "rt-9"),
		mk(20, "m", "v1", "ext.m", "rt-1"),
		mk(10, "a", "v1", "ext.b", "rt-4"),
		mk(10, "a", "v1", "ext.b", "rt-5"),
		mk(10, "a", "v1", "ext.c", "rt-3"),
		mk(10, "a", "v2", "ext.b", "rt-2"),
		mk(10, "b", "v1", "ext.a", "rt-1"),
	}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("sorted=%#v want=%#v", routes, want)
	}
}

func TestP6RoutePriorityAndConflictSelection(t *testing.T) {
	t.Run("path method conflict fails closed", func(t *testing.T) {
		registry := NewRegistry()
		first := routeArtifact("matrix.conflict.a", "1.0.0", 'a')
		second := routeArtifact("matrix.conflict.b", "1.0.0", 'b')
		snapshot, err := registry.Publish(Publication{Plugins: []PluginRouteSet{
			{Artifact: first, Routes: []extensionmanifest.ManifestRoute{pluginRoute("matrix.conflict.a.route", "/matrix/shared/:id", 10, "GET")}},
			{Artifact: second, Routes: []extensionmanifest.ManifestRoute{pluginRoute("matrix.conflict.b.route", "/matrix/shared/:slug", 20, "GET")}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].Kind != ConflictPathMethod {
			t.Fatalf("conflicts=%#v", snapshot.Conflicts)
		}
		match, err := registry.Resolve("GET", "/matrix/shared/value")
		if !errors.Is(err, ErrAmbiguousRoute) || len(match.Candidates) != 2 {
			t.Fatalf("resolve=%#v err=%v", match, err)
		}
		if _, err := registry.BuildExecutionPlan("GET", "/matrix/shared/value"); !errors.Is(err, ErrAmbiguousRoute) {
			t.Fatalf("plan err=%v", err)
		}
	})

	t.Run("higher path specificity wins without conflict", func(t *testing.T) {
		registry := NewRegistry()
		artifact := routeArtifact("matrix.specificity", "1.0.0", 'c')
		// 更具体的静态段应压过参数段；两者 path signature 不同故不构成 path_method 冲突。
		snapshot, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
			Artifact: artifact,
			Routes: []extensionmanifest.ManifestRoute{
				pluginRoute("matrix.specificity.param", "/matrix/items/:id", 0, "GET"),
				pluginRoute("matrix.specificity.static", "/matrix/items/featured", 0, "GET"),
			},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Conflicts) != 0 {
			t.Fatalf("specific routes must not conflict: %#v", snapshot.Conflicts)
		}
		match, err := registry.Resolve("GET", "/matrix/items/featured")
		if err != nil || match.Route.ID != "matrix.specificity.static" {
			t.Fatalf("resolve=%#v err=%v", match, err)
		}
		plan, err := registry.BuildExecutionPlan("GET", "/matrix/items/featured")
		if err != nil || plan.Terminal().RouteID != "matrix.specificity.static" {
			t.Fatalf("plan=%#v err=%v", plan.Terminal(), err)
		}
	})

	t.Run("replace requires explicit unique provider", func(t *testing.T) {
		registry := NewRegistry()
		artifact := routeArtifact("matrix.replace", "1.0.0", 'd')
		target := coreRoute("core.route.matrix.create", "POST", "/matrix/create")
		replacement := modifierRoute(
			"matrix.replace.writer", target.ID, target.Path,
			extensionmanifest.RouteActionReplace, "POST", 100,
		)
		snapshot, err := registry.Publish(Publication{
			Core:    []CoreRoute{target},
			Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].Kind != ConflictProviderSelection {
			t.Fatalf("conflicts=%#v", snapshot.Conflicts)
		}
		if _, err := registry.BuildExecutionPlan("POST", target.Path); !errors.Is(err, ErrAmbiguousRoute) {
			t.Fatalf("unselected replace plan err=%v", err)
		}

		providers := NewProviderSelectionAPI(registry, newMemoryProviderSelectionStore())
		conflicts, err := providers.Conflicts(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(conflicts) != 1 || conflicts[0].SelectionStatus != "unselected" {
			t.Fatalf("provider conflicts=%#v", conflicts)
		}
		key := conflicts[0].Key
		if _, err := providers.Select(t.Context(), SelectProviderRequest{
			Key: key, ProviderRouteID: replacement.ID, ProviderContractVersion: replacement.ContractVersion,
			ProviderArtifact: artifact, ActorUserID: 7, AuditEventID: 17,
		}); err != nil {
			t.Fatal(err)
		}
		// 显式选择后必须经过生产 ProviderSelectionAPI 解析，不允许测试手工制造 winner。
		plan, err := providers.BuildExecutionPlan(t.Context(), "POST", target.Path)
		if err != nil || plan.Terminal().Action != extensionmanifest.RouteActionReplace ||
			plan.Terminal().Provider.Artifact != artifact || !plan.UnsafeMethod() {
			t.Fatalf("selected replace plan=%#v err=%v", plan.Terminal(), err)
		}

		core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
			t.Fatal("selected replacement must be the only writer")
			return DispatchResponse{}, nil
		}}
		pluginCalls := 0
		dispatcher := NewDispatcher(DispatcherConfig{
			Plans: providers,
			Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
				pluginCalls++
				if input.Step.Action != extensionmanifest.RouteActionReplace || input.Step.Provider.Artifact != artifact {
					t.Fatalf("replace step=%#v", input.Step)
				}
				return RouteInvocationResult{Response: &DispatchResponse{Status: http.StatusCreated, Body: []byte("replaced")}}, nil
			}},
			Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
		})
		result, err := dispatcher.Dispatch(t.Context(), DispatchRequest{Method: "POST", Path: target.Path}, core)
		if err != nil || !result.Handled || result.Response.Status != http.StatusCreated ||
			string(result.Response.Body) != "replaced" || pluginCalls != 1 || core.calls != 0 {
			t.Fatalf("replace dispatch=%#v plugin=%d core=%d err=%v", result, pluginCalls, core.calls, err)
		}
	})
}

func TestP6RouteSafeModeBypassesPluginContributions(t *testing.T) {
	registry := NewRegistry()
	core := coreRoute("core.route.matrix.health", "GET", "/matrix/health")
	plugin := pluginRoute("matrix.safe.plugin", "/matrix/plugin", 0, "GET")
	before := modifierRoute(
		"matrix.safe.before", core.ID, core.Path, extensionmanifest.RouteActionBefore, "GET", 50,
	)
	snapshot, err := registry.Publish(Publication{
		SafeMode: true,
		Core:     []CoreRoute{core},
		Plugins: []PluginRouteSet{{
			Artifact: routeArtifact("matrix.safe", "1.0.0", 'e'),
			Routes:   []extensionmanifest.ManifestRoute{plugin, before},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.SafeMode || len(snapshot.Routes) != 1 || snapshot.Routes[0].Provider.Kind != ProviderCore {
		t.Fatalf("safe snapshot=%#v", snapshot)
	}
	if _, err := registry.Resolve("GET", "/matrix/plugin"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("plugin resolve err=%v", err)
	}
	plan, err := registry.BuildExecutionPlan("GET", "/matrix/health")
	if err != nil {
		t.Fatal(err)
	}
	// Safe Mode 下 core-only 计划无插件链；Dispatcher 应完全放行给 Fiber。
	if len(plan.Chain()) != 1 || plan.Terminal().Provider.Kind != ProviderCore {
		t.Fatalf("safe plan=%#v", plan.Chain())
	}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: matrixPlanResolver{registry: registry}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/matrix/health"}, &dispatchCoreInvoker{
		invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
			t.Fatal("core-only safe plan must not enter buffered core invoker")
			return DispatchResponse{}, nil
		},
	})
	if err != nil || result.Handled {
		t.Fatalf("safe dispatch=%#v err=%v", result, err)
	}
}

func TestP6RouteUnsafeNoSecondWriterMatrix(t *testing.T) {
	// 与生产 fence 一致：非 GET/HEAD、已开始副作用/响应后，绝不允许 core 成为第二写者。
	tests := []struct {
		name            string
		method          string
		sideEffect      bool
		responseStarted bool
	}{
		{name: "unsafe pristine post", method: "POST"},
		{name: "get after side effect", method: "GET", sideEffect: true},
		{name: "get after response started", method: "GET", responseStarted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providers := matrixSelectedReplacement(t, test.method, "/matrix/fence")
			core := &dispatchCoreInvoker{}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: providers,
				Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
					if input.Step.Action != extensionmanifest.RouteActionReplace || input.Step.Fallback != "readonly_core" {
						t.Fatalf("replace step=%#v", input.Step)
					}
					if test.sideEffect {
						input.Commit.SideEffectStarted()
					}
					if test.responseStarted {
						input.Commit.ResponseStarted()
					}
					return RouteInvocationResult{
						SideEffectStarted: test.sideEffect, ResponseStarted: test.responseStarted,
					}, errors.New("writer failed")
				}},
				Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
			})
			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: test.method, Path: "/matrix/fence"}, core)
			if !errors.Is(err, ErrDispatchTransport) || result.Handled || core.calls != 0 {
				t.Fatalf("result=%#v core=%d err=%v", result, core.calls, err)
			}
		})
	}

	// 对照：GET/HEAD + pristine + readonly_core 允许一次只读 core 回退（已接受契约）。
	for _, method := range []string{"GET", "HEAD"} {
		t.Run("pristine "+method, func(t *testing.T) {
			providers := matrixSelectedReplacement(t, method, "/matrix/fence")
			core := &dispatchCoreInvoker{invoke: func(_ context.Context, step RouteExecutionStep, _ DispatchRequest) (DispatchResponse, error) {
				if step.Action != extensionmanifest.RouteActionReplace || step.TargetID != "core.route.matrix.fence" {
					t.Fatalf("fallback step=%#v", step)
				}
				return DispatchResponse{Status: http.StatusOK, Body: []byte("core")}, nil
			}}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: providers,
				Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
					return RouteInvocationResult{}, errors.New("plugin unavailable")
				}},
				Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
			})
			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: method, Path: "/matrix/fence"}, core)
			if err != nil || !result.Handled || string(result.Response.Body) != "core" || core.calls != 1 {
				t.Fatalf("readonly fallback=%#v core=%d err=%v", result, core.calls, err)
			}
		})
	}
}

func TestP6RouteTimeoutWhereHarnessSupports(t *testing.T) {
	// 现有 Dispatcher 超时 harness：步骤 TimeoutMS 使 Invoke 上下文截止；
	// fallback=closed 时 fail-closed，不进入 core 第二写者。
	for _, test := range []struct {
		name    string
		parent  func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "step timeout",
			parent: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantErr: context.DeadlineExceeded,
		},
		{
			name: "caller cancellation",
			parent: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantErr: context.Canceled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			route := pluginRoute("matrix.timeout.route", "/matrix/timeout", 0, "GET")
			route.TimeoutMS = 5
			if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
				Artifact: routeArtifact("matrix.timeout", "1.0.0", 'f'),
				Routes:   []extensionmanifest.ManifestRoute{route},
			}}}); err != nil {
				t.Fatal(err)
			}
			core := &dispatchCoreInvoker{}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: matrixPlanResolver{registry: registry},
				Steps: &dispatchStepInvoker{invoke: func(ctx context.Context, input RouteInvocation) (RouteInvocationResult, error) {
					if input.Step.TimeoutMS != 5 || input.Step.RouteID != route.ID {
						t.Fatalf("timeout step=%#v", input.Step)
					}
					<-ctx.Done()
					return RouteInvocationResult{}, ctx.Err()
				}},
				Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
			})
			ctx, cancel := test.parent()
			defer cancel()
			result, err := dispatcher.Dispatch(ctx, DispatchRequest{Method: "GET", Path: "/matrix/timeout"}, core)
			if !errors.Is(err, ErrDispatchTransport) || !errors.Is(err, test.wantErr) || result.Handled || core.calls != 0 {
				t.Fatalf("result=%#v core=%d err=%v", result, core.calls, err)
			}
		})
	}
}

func matrixSelectedReplacement(t *testing.T, method, path string) *ProviderSelectionAPI {
	t.Helper()
	registry := NewRegistry()
	artifact := routeArtifact("matrix.fence", "1.0.0", 'e')
	target := coreRoute("core.route.matrix.fence", method, path)
	replacement := modifierRoute(
		"matrix.fence.replace", target.ID, target.Path,
		extensionmanifest.RouteActionReplace, method, 100,
	)
	replacement.Fallback = "readonly_core"
	snapshot, err := registry.Publish(Publication{
		Core:    []CoreRoute{target},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].Kind != ConflictProviderSelection {
		t.Fatalf("replace conflicts=%#v", snapshot.Conflicts)
	}
	providers := NewProviderSelectionAPI(registry, newMemoryProviderSelectionStore())
	conflicts, err := providers.Conflicts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].SelectionStatus != "unselected" {
		t.Fatalf("provider conflicts=%#v", conflicts)
	}
	key := conflicts[0].Key
	if _, err := providers.Select(t.Context(), SelectProviderRequest{
		Key: key, ProviderRouteID: replacement.ID, ProviderContractVersion: replacement.ContractVersion,
		ProviderArtifact: artifact, ActorUserID: 7, AuditEventID: 17,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := providers.BuildExecutionPlan(t.Context(), method, path)
	if err != nil || plan.Terminal().Action != extensionmanifest.RouteActionReplace ||
		plan.Terminal().Fallback != "readonly_core" || plan.Terminal().Provider.Artifact != artifact {
		t.Fatalf("selected fallback plan=%#v err=%v", plan.Terminal(), err)
	}
	return providers
}

type matrixPlanResolver struct {
	registry *Registry
}

func (r matrixPlanResolver) BuildExecutionPlan(_ context.Context, method, path string) (RouteExecutionPlan, error) {
	return r.registry.BuildExecutionPlan(method, path)
}
