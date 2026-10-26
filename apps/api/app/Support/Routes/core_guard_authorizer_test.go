package routes

import (
	"context"
	"errors"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestCoreGuardEvaluatorRegistryIsVersionedExactAndInspectable(t *testing.T) {
	evaluator := CoreGuardEvaluatorFunc(func(context.Context, CoreGuardEvaluation) error { return nil })
	registry, err := NewCoreGuardEvaluatorRegistry([]CoreGuardEvaluatorRegistration{
		{EvaluatorID: "core.guard.test.second", ContractVersion: CoreGuardEvaluatorContractV1, Evaluator: evaluator},
		{EvaluatorID: "core.guard.test.first", ContractVersion: CoreGuardEvaluatorContractV1, Evaluator: evaluator},
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := registry.Bindings()
	if len(bindings) != 2 || bindings[0].EvaluatorID != "core.guard.test.first" ||
		bindings[1].EvaluatorID != "core.guard.test.second" ||
		bindings[0].ContractVersion != CoreGuardEvaluatorContractV1 {
		t.Fatalf("bindings = %#v", bindings)
	}
	bindings[0].EvaluatorID = "mutated"
	if registry.Bindings()[0].EvaluatorID != "core.guard.test.first" {
		t.Fatal("bindings escaped immutable registry")
	}

	invalid := [][]CoreGuardEvaluatorRegistration{
		{{EvaluatorID: "plugin.guard.test", ContractVersion: CoreGuardEvaluatorContractV1, Evaluator: evaluator}},
		{{EvaluatorID: "core.guard.test", ContractVersion: "sforum.route.guard_evaluator@2", Evaluator: evaluator}},
		{{EvaluatorID: "core.guard.test", ContractVersion: CoreGuardEvaluatorContractV1}},
		{
			{EvaluatorID: "core.guard.test", ContractVersion: CoreGuardEvaluatorContractV1, Evaluator: evaluator},
			{EvaluatorID: "core.guard.test", ContractVersion: CoreGuardEvaluatorContractV1, Evaluator: evaluator},
		},
	}
	for index, registrations := range invalid {
		if _, err := NewCoreGuardEvaluatorRegistry(registrations); !errors.Is(err, ErrCoreGuardRegistryInvalid) {
			t.Fatalf("invalid registration %d error = %v", index, err)
		}
	}
}

func TestCoreGuardAuthorizerExecutesTypedInheritedKinds(t *testing.T) {
	tests := []struct {
		name       string
		descriptor CoreGuardDescriptor
		request    DispatchRequest
		want       error
	}{
		{name: "public", descriptor: CoreGuardDescriptor{Kind: CoreGuardPublic}},
		{name: "login", descriptor: CoreGuardDescriptor{Kind: CoreGuardLogin}, request: authenticatedGuardRequest()},
		{name: "login denied", descriptor: CoreGuardDescriptor{Kind: CoreGuardLogin}, want: ErrCoreGuardLoginRequired},
		{name: "guest", descriptor: CoreGuardDescriptor{Kind: CoreGuardGuest}},
		{name: "guest denied", descriptor: CoreGuardDescriptor{Kind: CoreGuardGuest}, request: authenticatedGuardRequest(), want: ErrCoreGuardGuestRequired},
		{name: "super admin", descriptor: CoreGuardDescriptor{Kind: CoreGuardSuperAdmin}, request: guardRequestWithPermissions("*")},
		{name: "super admin denied", descriptor: CoreGuardDescriptor{Kind: CoreGuardSuperAdmin}, request: authenticatedGuardRequest(), want: ErrCoreGuardPermissionDenied},
		{name: "permission any", descriptor: CoreGuardDescriptor{Kind: CoreGuardPermissionAny, Permissions: []string{"topic.edit_own", "topic.edit_any"}}, request: guardRequestWithPermissions("topic.edit_any")},
		{name: "permission denied", descriptor: CoreGuardDescriptor{Kind: CoreGuardPermissionAny, Permissions: []string{"topic.edit_own", "topic.edit_any"}}, request: authenticatedGuardRequest(), want: ErrCoreGuardPermissionDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, step := inheritedCoreGuardPlan(t, test.descriptor)
			request := test.request
			request.Method, request.Path = plan.Method(), plan.Path()
			err := (CoreGuardAuthorizer{}).Authorize(context.Background(), plan, step, request)
			if !errors.Is(err, test.want) || test.want == nil && err != nil {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCoreGuardAuthorizerRequiresExplicitContextualEvaluator(t *testing.T) {
	descriptor := CoreGuardDescriptor{
		Kind: CoreGuardContextual, Permissions: []string{"topic.edit_any"}, EvaluatorID: "core.guard.test.contextual",
	}
	plan, step := inheritedCoreGuardPlan(t, descriptor)
	request := guardRequestWithPermissions("topic.edit_any")
	request.Method, request.Path = plan.Method(), plan.Path()
	if err := (CoreGuardAuthorizer{}).Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrCoreGuardEvaluatorUnavailable) {
		t.Fatalf("missing evaluator error = %v", err)
	}

	called := 0
	registry := MustNewCoreGuardEvaluatorRegistry([]CoreGuardEvaluatorRegistration{{
		EvaluatorID: descriptor.EvaluatorID, ContractVersion: CoreGuardEvaluatorContractV1,
		Evaluator: CoreGuardEvaluatorFunc(func(_ context.Context, evaluation CoreGuardEvaluation) error {
			called++
			if evaluation.Descriptor.RouteID != step.TargetID || evaluation.PlanRevision != plan.Revision() ||
				evaluation.RequestMethod != "PATCH" || evaluation.RequestPath != "/guard/alias" ||
				evaluation.Request.ActorID != request.ActorID {
				t.Fatalf("evaluation = %#v", evaluation)
			}
			evaluation.Descriptor.Permissions[0] = "mutated"
			evaluation.Step.CoreGuard.Permissions[0] = "mutated"
			evaluation.Request.Permissions["mutated"] = true
			return nil
		}),
	}})
	authorizer := CoreGuardAuthorizer{Evaluators: registry}
	if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
		t.Fatal(err)
	}
	if called != 1 || step.CoreGuard.Permissions[0] != "topic.edit_any" ||
		plan.Terminal().CoreGuard.Permissions[0] != "topic.edit_any" || request.Permissions["mutated"] {
		t.Fatal("contextual evaluator mutated caller-owned authority data")
	}
}

func TestCoreGuardAuthorizerRejectsForgedInheritedAuthority(t *testing.T) {
	plan, step := inheritedCoreGuardPlan(t, CoreGuardDescriptor{Kind: CoreGuardPublic})
	request := DispatchRequest{Method: plan.Method(), Path: plan.Path()}
	for _, mutate := range []func(*RouteExecutionStep, *DispatchRequest){
		func(step *RouteExecutionStep, _ *DispatchRequest) { step.TargetID = "core.route.forged" },
		func(step *RouteExecutionStep, _ *DispatchRequest) {
			step.CoreGuard.ContractVersion = "sforum.route.forged@1"
		},
		func(step *RouteExecutionStep, _ *DispatchRequest) { step.CoreGuard.Kind = CoreGuardSuperAdmin },
		func(_ *RouteExecutionStep, request *DispatchRequest) { request.Method = "GET" },
		func(_ *RouteExecutionStep, request *DispatchRequest) { request.Path = "/forged" },
	} {
		candidateStep := step
		candidateStep.CoreGuard = cloneCoreGuardDescriptor(step.CoreGuard)
		candidateRequest := cloneDispatchRequest(request)
		mutate(&candidateStep, &candidateRequest)
		if err := (CoreGuardAuthorizer{}).Authorize(context.Background(), plan, candidateStep, candidateRequest); !errors.Is(err, ErrCoreGuardEvaluatorUnavailable) {
			t.Fatalf("forged authority error = %v", err)
		}
	}
}

func TestCoreGuardAuthorizerPreservesDirectPluginGuardsAndClosesCustomAuthority(t *testing.T) {
	authorizer := CoreGuardAuthorizer{}
	authenticated := guardRequestWithPermissions("topic.create")
	tests := []struct {
		guard      string
		permission string
		request    DispatchRequest
		want       error
	}{
		{guard: extensionmanifest.GuardCorePublic},
		{guard: extensionmanifest.GuardCoreLogin, request: authenticated},
		{guard: extensionmanifest.GuardCoreLogin, want: ErrCoreGuardLoginRequired},
		{guard: extensionmanifest.GuardCoreGuest},
		{guard: extensionmanifest.GuardCoreGuest, request: authenticated, want: ErrCoreGuardGuestRequired},
		{guard: extensionmanifest.GuardCoreGuest, request: DispatchRequest{ActorID: 42}, want: ErrCoreGuardGuestRequired},
		{guard: extensionmanifest.GuardCorePermission, permission: "topic.create", request: authenticated},
		{guard: extensionmanifest.GuardCorePermission, permission: "topic.create", request: authenticatedGuardRequest(), want: ErrCoreGuardPermissionDenied},
		{guard: "guard.direct.custom", request: authenticated, want: ErrCoreGuardEvaluatorUnavailable},
	}
	for index, test := range tests {
		plan, step := directCoreGuardPlan(t, test.guard, test.permission)
		request := test.request
		request.Method, request.Path = plan.Method(), plan.Path()
		err := authorizer.Authorize(context.Background(), plan, step, request)
		if !errors.Is(err, test.want) || test.want == nil && err != nil {
			t.Fatalf("case %d error = %v, want %v", index, err, test.want)
		}
	}

	// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 是脆弱契约——空插件守卫
	// 绑定即可换取 raw 请求权限。它必须 fail closed 到「登记阶段直接拒绝」，而不是
	// 「登记成功、授权为空」：拒绝后的路由也不得再被解析出执行计划。
	registry := NewRegistry()
	coreRawRoute := pluginRoute("guard.direct.route", "/guard/direct", 0, "GET")
	coreRawRoute.Guard = extensionmanifest.GuardCoreRaw
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
		Artifact: routeArtifact("guard.direct", "1.0.0", 'b'),
		Routes:   []extensionmanifest.ManifestRoute{coreRawRoute},
	}}}); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("core raw_request guard publish error = %v", err)
	}
	if registry.Revision() != 0 {
		t.Fatalf("refused core raw_request publication advanced revision to %d", registry.Revision())
	}
	if _, err := registry.BuildExecutionPlan("GET", coreRawRoute.Path); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("refused core raw_request route is still addressable: %v", err)
	}
}

func TestCoreGuardAuthorizerRejectsDirectGuardOutsideExactPlan(t *testing.T) {
	plan, step := directCoreGuardPlan(t, extensionmanifest.GuardCorePublic, "")
	request := DispatchRequest{Method: plan.Method(), Path: plan.Path()}
	for _, mutate := range []func(*RouteExecutionStep, *DispatchRequest){
		func(step *RouteExecutionStep, _ *DispatchRequest) { step.RouteID = "guard.plugin.forged" },
		func(step *RouteExecutionStep, _ *DispatchRequest) { step.Provider = Provider{} },
		func(_ *RouteExecutionStep, request *DispatchRequest) { request.Method = "POST" },
		func(_ *RouteExecutionStep, request *DispatchRequest) { request.Path = "/forged" },
	} {
		candidateStep := step
		candidateRequest := cloneDispatchRequest(request)
		mutate(&candidateStep, &candidateRequest)
		if err := (CoreGuardAuthorizer{}).Authorize(context.Background(), plan, candidateStep, candidateRequest); !errors.Is(err, ErrCoreGuardEvaluatorUnavailable) {
			t.Fatalf("forged direct guard error = %v", err)
		}
	}
}

func TestCoreGuardAuthorizerExecutesExactPluginGuardEvaluator(t *testing.T) {
	// raw 请求权限仍然存在，但只能来自包内显式声明的 kind=raw_request 守卫。
	for _, test := range []struct {
		guard    string
		wantKind RequestGuardKind
	}{
		{guard: "guard.direct.raw", wantKind: RequestGuardRawRequest},
		{guard: "guard.direct.custom", wantKind: RequestGuardCustom},
	} {
		guard := test.guard
		plan, step := directCoreGuardPlan(t, guard, "")
		request := DispatchRequest{
			Method: plan.Method(), Path: plan.Path(), ActorID: 42, Authenticated: true,
			Permissions: map[string]bool{"topic.read": true}, Params: plan.Params(),
		}
		called := 0
		authorizer := CoreGuardAuthorizer{PluginGuards: PluginGuardEvaluatorFunc(func(_ context.Context, evaluation PluginGuardEvaluation) error {
			called++
			if evaluation.PlanRevision != plan.Revision() || evaluation.RequestMethod != plan.Method() ||
				evaluation.RequestPath != plan.Path() || !equalCoreGuardExecutionStep(evaluation.Step, step) ||
				evaluation.Request.ActorID != request.ActorID ||
				evaluation.Authority.GuardKind != test.wantKind ||
				evaluation.Authority.Mode != RequestAuthorityRaw && test.wantKind == RequestGuardRawRequest {
				t.Fatalf("plugin guard evaluation = %#v", evaluation)
			}
			evaluation.Request.Permissions["forged"] = true
			evaluation.Step.PluginGuard.Permissions = append(evaluation.Step.PluginGuard.Permissions, "forged")
			return nil
		})}
		if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil || called != 1 {
			t.Fatalf("guard %q error = %v, called = %d", guard, err, called)
		}
		if request.Permissions["forged"] || len(plan.Terminal().PluginGuard.Permissions) != len(step.PluginGuard.Permissions) {
			t.Fatal("plugin guard evaluator mutated immutable request or plan")
		}
	}
}

func TestCoreGuardAuthorizerRejectsForgedPluginGuardBinding(t *testing.T) {
	plan, step := directCoreGuardPlan(t, "guard.direct.custom", "")
	request := DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()}
	authorizer := CoreGuardAuthorizer{PluginGuards: PluginGuardEvaluatorFunc(func(context.Context, PluginGuardEvaluation) error {
		t.Fatal("forged plugin guard reached evaluator")
		return nil
	})}
	for _, mutate := range []func(*RouteExecutionStep){
		func(value *RouteExecutionStep) { value.PluginGuard.ID = "guard.direct.other" },
		func(value *RouteExecutionStep) { value.PluginGuard.ContractVersion = "" },
		func(value *RouteExecutionStep) { value.PluginGuard.Kind = "builtin" },
		func(value *RouteExecutionStep) { value.PluginGuard.Entry = "" },
		func(value *RouteExecutionStep) { value.PluginGuard.Digest = "forged" },
	} {
		candidate := step
		mutate(&candidate)
		if err := authorizer.Authorize(context.Background(), plan, candidate, request); !errors.Is(err, ErrCoreGuardEvaluatorUnavailable) {
			t.Fatalf("forged binding error = %v", err)
		}
	}
}

func inheritedCoreGuardPlan(t *testing.T, descriptor CoreGuardDescriptor) (RouteExecutionPlan, RouteExecutionStep) {
	t.Helper()
	registry := NewRegistry()
	target := coreRoute("core.route.guard.target", "PATCH", "/guard/target")
	descriptor.RouteID = target.ID
	descriptor.ContractVersion = target.ContractVersion
	descriptor.Method = target.Method
	target.Guard = descriptor
	alias := pluginRoute("guard.plugin.alias", "/guard/alias", 0, "PATCH")
	alias.Action, alias.TargetID, alias.Handler, alias.ResponseSchema = extensionmanifest.RouteActionAlias, target.ID, "", ""
	alias.Guard = extensionmanifest.GuardCoreInherit
	if _, err := registry.Publish(Publication{
		Core:    []CoreRoute{target},
		Plugins: []PluginRouteSet{{Artifact: routeArtifact("guard.plugin", "1.0.0", 'a'), Routes: []extensionmanifest.ManifestRoute{alias}}},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan("PATCH", "/guard/alias")
	if err != nil {
		t.Fatal(err)
	}
	return plan, plan.Terminal()
}

func directCoreGuardPlan(t *testing.T, guard, permission string) (RouteExecutionPlan, RouteExecutionStep) {
	t.Helper()
	registry := NewRegistry()
	route := pluginRoute("guard.direct.route", "/guard/direct", 0, "GET")
	route.Guard, route.Permission = guard, permission
	// 决定 R1（2026-10-16）：raw 权限只能来自包内声明的守卫，因此测试里的
	// guard.direct.raw 必须像真实扩展一样在同一次发布中声明 entry + digest + contract。
	var guards []extensionmanifest.ManifestGuard
	switch guard {
	case "guard.direct.custom":
		guards = []extensionmanifest.ManifestGuard{pluginGuard(guard, "custom")}
	case "guard.direct.raw":
		guards = []extensionmanifest.ManifestGuard{pluginGuard(guard, "raw_request")}
	}
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
		Artifact: routeArtifact("guard.direct", "1.0.0", 'b'), Routes: []extensionmanifest.ManifestRoute{route}, Guards: guards,
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan("GET", route.Path)
	if err != nil {
		t.Fatal(err)
	}
	return plan, plan.Terminal()
}

func authenticatedGuardRequest() DispatchRequest {
	return DispatchRequest{ActorID: 42, Authenticated: true, Permissions: map[string]bool{}}
}

func guardRequestWithPermissions(permissions ...string) DispatchRequest {
	request := authenticatedGuardRequest()
	for _, permission := range permissions {
		request.Permissions[permission] = true
	}
	return request
}
