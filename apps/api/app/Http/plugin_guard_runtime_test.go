package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"reflect"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRuntimePluginRouteGuardEvaluatorInvokesExactSanitizedGuard(t *testing.T) {
	plan, step := productionDirectGuardPlan(t, "guard.production.custom", "")
	policy := &testExtensionGuardPolicy{lookup: exactPluginGuardLookup(step), ok: true}
	runtime := newTestPluginGuardRuntime(t, step)
	evaluator := NewRuntimePluginRouteGuardEvaluator(runtime, policy)
	request := routes.DispatchRequest{
		Method: plan.Method(), Path: plan.Path(), Query: "preview=1&tag=one&tag=&tag=two", Params: plan.Params(),
		Headers: stdhttp.Header{
			"X-Request-ID": []string{"request-41"}, "Cookie": []string{"session=secret"},
			"Authorization": []string{"Bearer secret"}, "X-API-Key": []string{"api-key-secret"},
			"X-Auth-Token": []string{"auth-token-secret"}, "connection": []string{"X-Guard-Hop"},
			"X-Guard-Hop": []string{"hop-secret"}, "X-SForum-Forged": []string{"forged"},
		},
		ActorID: 42, Authenticated: true, Permissions: map[string]bool{"topic.read": true},
	}
	if err := evaluator.EvaluatePluginGuard(context.Background(), routes.PluginGuardEvaluation{
		PlanRevision: plan.Revision(), RequestMethod: plan.Method(), RequestPath: plan.Path(), Step: step, Request: request,
		Authority: resolvedPluginGuardTestAuthority(step),
	}); err != nil {
		t.Fatal(err)
	}
	if runtime.inspectCalls != 1 || runtime.acquireCalls != 1 || runtime.invokeCalls != 1 ||
		runtime.class != extensionsruntime.RuntimeCallGuard || runtime.request.GuardID != step.PluginGuard.ID ||
		runtime.request.GuardContractVersion != step.PluginGuard.ContractVersion ||
		runtime.request.RouteID != step.RouteID || runtime.request.RouteContractVersion != step.ContractVersion ||
		runtime.request.QueryParameters["preview"] != "1" || runtime.request.QueryParameters["tag"] != "one" ||
		!reflect.DeepEqual(runtime.request.QueryParameterValues["tag"], []string{"one", "", "two"}) ||
		runtime.request.Actor.UserID != 42 ||
		runtime.request.Headers.Get("X-Request-ID") != "request-41" || runtime.request.Headers.Get("Cookie") != "" ||
		runtime.request.Headers.Get("Authorization") != "" || runtime.request.Headers.Get("X-API-Key") != "" ||
		runtime.request.Headers.Get("X-Auth-Token") != "" || runtime.request.Headers.Get("X-Guard-Hop") != "" ||
		runtime.request.Headers.Get("X-SForum-Forged") != "" {
		t.Fatalf("runtime guard = %#v", runtime)
	}
}

func TestRuntimePluginRouteGuardEvaluatorMapsTypedFailureEvidence(t *testing.T) {
	plan, step := productionDirectGuardPlan(t, "guard.production.custom", "")
	request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()}
	policy := &testExtensionGuardPolicy{lookup: exactPluginGuardLookup(step), ok: true}
	const secret = "plugin-reason-secret"
	for _, test := range []struct {
		name     string
		err      error
		kind     routes.PluginGuardFailureKind
		observed bool
		compat   error
	}{
		{
			name: "legacy denied", err: extensionsruntime.ErrProtocolV2GuardDenied,
			kind: routes.PluginGuardFailureDenied, observed: true, compat: routes.ErrCoreGuardPermissionDenied,
		},
		{
			name: "typed denied", err: extensionsruntime.NewProtocolV2GuardCallFailure(extensionsruntime.ProtocolV2GuardFailureDenied, nil),
			kind: routes.PluginGuardFailureDenied, observed: true, compat: routes.ErrCoreGuardPermissionDenied,
		},
		{
			name: "crash", err: extensionsruntime.NewProtocolV2GuardCallFailure(
				extensionsruntime.ProtocolV2GuardFailureCrash, errors.New(secret),
			),
			kind: routes.PluginGuardFailureCrash, observed: true, compat: routes.ErrCoreGuardEvaluatorUnavailable,
		},
		{
			name: "timeout", err: extensionsruntime.NewProtocolV2GuardCallFailure(extensionsruntime.ProtocolV2GuardFailureTimeout, nil),
			kind: routes.PluginGuardFailureTimeout, observed: true, compat: context.DeadlineExceeded,
		},
		{
			name: "protocol", err: extensionsruntime.NewProtocolV2GuardCallFailure(extensionsruntime.ProtocolV2GuardFailureProtocol, nil),
			kind: routes.PluginGuardFailureProtocol, observed: true, compat: routes.ErrCoreGuardEvaluatorUnavailable,
		},
		{
			name: "canceled", err: extensionsruntime.NewProtocolV2GuardCallFailure(extensionsruntime.ProtocolV2GuardFailureCanceled, nil),
			kind: routes.PluginGuardFailureCanceled, observed: true, compat: context.Canceled,
		},
		{
			name: "pre RPC unavailable", err: errors.New(secret),
			kind: routes.PluginGuardFailureUnavailable, observed: false, compat: routes.ErrCoreGuardEvaluatorUnavailable,
		},
		{
			name: "pre RPC timeout", err: context.DeadlineExceeded,
			kind: routes.PluginGuardFailureTimeout, observed: false, compat: context.DeadlineExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTestPluginGuardRuntime(t, step)
			runtime.invokeErr = test.err
			err := NewRuntimePluginRouteGuardEvaluator(runtime, policy).EvaluatePluginGuard(context.Background(), routes.PluginGuardEvaluation{
				PlanRevision: plan.Revision(), RequestMethod: plan.Method(), RequestPath: plan.Path(), Step: step, Request: request,
				Authority: resolvedPluginGuardTestAuthority(step),
			})
			var evidence *routes.PluginGuardFailure
			if !errors.As(err, &evidence) || evidence.Kind() != test.kind ||
				evidence.RuntimeExecutionObserved() != test.observed || !errors.Is(err, test.compat) ||
				strings.Contains(err.Error(), secret) || runtime.invokeCalls != 1 {
				t.Fatalf("error = %v, calls = %d", err, runtime.invokeCalls)
			}
		})
	}
}

func TestRuntimePluginRouteGuardEvaluatorFailsClosedOnTrustDrift(t *testing.T) {
	plan, step := productionDirectGuardPlan(t, "guard.production.custom", "")
	request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()}
	base := exactPluginGuardLookup(step)
	tests := []struct {
		name   string
		mutate func(*extensions.GuardPolicyLookup)
	}{
		{name: "missing snapshot", mutate: func(value *extensions.GuardPolicyLookup) { value.Revision = 0 }},
		{name: "not found", mutate: func(value *extensions.GuardPolicyLookup) { value.Found = false }},
		{name: "safe mode", mutate: func(value *extensions.GuardPolicyLookup) { value.SafeMode = true }},
		{name: "disabled", mutate: func(value *extensions.GuardPolicyLookup) { value.Entry.Status = extensions.StatusDisabled }},
		{name: "version", mutate: func(value *extensions.GuardPolicyLookup) { value.Entry.Version = "2.0.0" }},
		{name: "digest", mutate: func(value *extensions.GuardPolicyLookup) { value.Entry.PackageDigest = strings.Repeat("d", 64) }},
		{name: "revoked", mutate: func(value *extensions.GuardPolicyLookup) { value.Entry.CurrentArtifactTrusted = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := base
			test.mutate(&lookup)
			policy := &testExtensionGuardPolicy{lookup: lookup, ok: true}
			if lookup.Revision == 0 {
				policy.ok = false
			}
			runtime := newTestPluginGuardRuntime(t, step)
			err := NewRuntimePluginRouteGuardEvaluator(runtime, policy).EvaluatePluginGuard(context.Background(), routes.PluginGuardEvaluation{
				PlanRevision: plan.Revision(), RequestMethod: plan.Method(), RequestPath: plan.Path(), Step: step, Request: request,
				Authority: resolvedPluginGuardTestAuthority(step),
			})
			var evidence *routes.PluginGuardFailure
			if !errors.Is(err, routes.ErrCoreGuardEvaluatorUnavailable) || !errors.As(err, &evidence) ||
				evidence.Kind() != routes.PluginGuardFailureUnavailable || evidence.RuntimeExecutionObserved() || runtime.invokeCalls != 0 {
				t.Fatalf("error = %v, calls = %d", err, runtime.invokeCalls)
			}
		})
	}
}

func TestRuntimePluginRouteGuardEvaluatorMapsCanceledContextBeforeRPC(t *testing.T) {
	plan, step := productionDirectGuardPlan(t, "guard.production.custom", "")
	policy := &testExtensionGuardPolicy{lookup: exactPluginGuardLookup(step), ok: true}
	runtime := newTestPluginGuardRuntime(t, step)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewRuntimePluginRouteGuardEvaluator(runtime, policy).EvaluatePluginGuard(ctx, routes.PluginGuardEvaluation{
		PlanRevision: plan.Revision(), RequestMethod: plan.Method(), RequestPath: plan.Path(), Step: step,
		Authority: resolvedPluginGuardTestAuthority(step),
		Request:   routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()},
	})
	var evidence *routes.PluginGuardFailure
	if !errors.Is(err, context.Canceled) || !errors.As(err, &evidence) ||
		evidence.Kind() != routes.PluginGuardFailureCanceled || evidence.RuntimeExecutionObserved() ||
		runtime.acquireCalls != 1 || runtime.invokeCalls != 0 {
		t.Fatalf("pre-RPC cancellation error=%v runtime=%#v", err, runtime)
	}
}

func TestRuntimePluginRouteGuardEvaluatorMapsRawRequestGuardFailure(t *testing.T) {
	plan, step := productionRawPluginGuardPlan(t)
	policy := &testExtensionGuardPolicy{lookup: exactPluginGuardLookup(step), ok: true}
	runtime := newTestPluginGuardRuntime(t, step)
	runtime.invokeErr = extensionsruntime.NewProtocolV2GuardCallFailure(
		extensionsruntime.ProtocolV2GuardFailureCrash, errors.New("raw-plugin-secret"),
	)
	err := NewRuntimePluginRouteGuardEvaluator(runtime, policy).EvaluatePluginGuard(context.Background(), routes.PluginGuardEvaluation{
		PlanRevision: plan.Revision(), RequestMethod: plan.Method(), RequestPath: plan.Path(), Step: step,
		Authority: resolvedPluginGuardTestAuthority(step),
		Request:   routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()},
	})
	var evidence *routes.PluginGuardFailure
	if !errors.As(err, &evidence) || evidence.Kind() != routes.PluginGuardFailureCrash ||
		!evidence.RuntimeExecutionObserved() || runtime.invokeCalls != 1 ||
		runtime.request.Authority.Mode != extensionsruntime.ProtocolV2RequestAuthorityRaw ||
		strings.Contains(err.Error(), "raw-plugin-secret") {
		t.Fatalf("raw guard error=%v runtime=%#v", err, runtime)
	}
}

// R1（2026-10-16）翻转断言。旧版 TestRuntimePluginRouteGuardEvaluatorConfirmsRawAuthorityWithoutCredentials
// 确认的是：路由引用 Host 拥有的 core.guard.raw_request 且没有任何插件守卫绑定时，
// 不需要一行插件代码就能拿到 raw 权限——运维方判定这是漏洞。现在断言反向：
// 生产守卫链路必须在触碰运行实例（inspect / acquire / invoke）之前就拒绝该执行步骤。
func TestProductionRouteGuardChainRejectsRetiredCoreRawRequestWithoutPluginGuard(t *testing.T) {
	plan, step := productionDirectGuardPlan(t, extensionmanifest.GuardCoreRaw, "")
	if step.Guard != extensionmanifest.GuardCoreRaw || step.PluginGuard.ID != "" || step.PluginGuard.Kind != "" {
		t.Fatalf("retired core raw step = %#v", step)
	}
	policy := &testExtensionGuardPolicy{lookup: exactPluginGuardLookup(step), ok: true}
	runtime := newTestPluginGuardRuntime(t, step)
	err := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{
		PluginGuards: NewRuntimePluginRouteGuardEvaluator(runtime, policy),
	}).Authorize(context.Background(), plan, step, routes.DispatchRequest{
		Method: plan.Method(), Path: plan.Path(), Params: plan.Params(),
	})
	if !errors.Is(err, ErrRouteGuardUnavailable) || runtime.inspectCalls != 0 ||
		runtime.acquireCalls != 0 || runtime.invokeCalls != 0 {
		t.Fatalf("retired core raw guard error = %v, runtime = %#v", err, runtime)
	}
}

// resolvedPluginGuardTestAuthority 只从包内声明的 kind=raw_request 守卫推导 raw 权限。
// R1 之后已退役的 core.guard.raw_request 不能再在测试夹具里伪造 raw 权限。
func resolvedPluginGuardTestAuthority(step routes.RouteExecutionStep) routes.ResolvedRequestAuthority {
	if step.PluginGuard.Kind == "raw_request" {
		return routes.ResolvedRequestAuthority{Mode: routes.RequestAuthorityRaw, GuardKind: routes.RequestGuardRawRequest}
	}
	return routes.ResolvedRequestAuthority{Mode: routes.RequestAuthorityFiltered, GuardKind: routes.RequestGuardCustom}
}

func exactPluginGuardLookup(step routes.RouteExecutionStep) extensions.GuardPolicyLookup {
	return extensions.GuardPolicyLookup{
		Revision: 1, Found: true,
		Entry: extensions.GuardPolicyEntry{
			ExtensionID: step.Provider.Artifact.ExtensionID, ExtensionType: extensions.TypePlugin,
			Status: extensions.StatusEnabled, Version: step.Provider.Artifact.ExtensionVersion,
			PackageDigest:        step.Provider.Artifact.PackageDigest,
			CurrentTrustRequired: true, CurrentArtifactTrusted: true,
		},
	}
}

func productionRawPluginGuardPlan(t *testing.T) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	registry := routes.NewRegistry()
	guardID := "guard.production.raw"
	route := extensionmanifest.ManifestRoute{
		ID: "guard.production.raw_route", ContractVersion: "guard.production.raw_route@1",
		Action: extensionmanifest.RouteActionAdd, Path: "/guard/raw", Methods: []string{stdhttp.MethodGet},
		Guard: guardID, Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
		Handler: "route.handle", ResponseSchema: "guard.production.raw.response@1",
	}
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: routes.PluginArtifact{
			ExtensionID: "guard.production", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("b", 64), RuntimeInstanceID: "runtime-b",
		},
		Routes: []extensionmanifest.ManifestRoute{route},
		Guards: []extensionmanifest.ManifestGuard{{
			ID: guardID, ContractVersion: guardID + "@1", Kind: "raw_request",
			Entry: "backend/raw_guard", Digest: strings.Repeat("c", 64),
		}},
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(stdhttp.MethodGet, route.Path)
	if err != nil {
		t.Fatal(err)
	}
	return plan, plan.Terminal()
}

type testPluginGuardRuntime struct {
	snapshot     extensionsruntime.RuntimeInstanceSnapshot
	gate         *extensionsruntime.RuntimeAdmissionGate
	request      extensionsruntime.ProtocolV2GuardRequest
	class        extensionsruntime.RuntimeCallClass
	invokeErr    error
	inspectCalls int
	acquireCalls int
	invokeCalls  int
}

func newTestPluginGuardRuntime(t *testing.T, step routes.RouteExecutionStep) *testPluginGuardRuntime {
	t.Helper()
	identity := extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: step.Provider.Artifact.ExtensionID, InstanceID: step.Provider.Artifact.RuntimeInstanceID,
	}
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
	if err != nil {
		t.Fatal(err)
	}
	return &testPluginGuardRuntime{
		gate: gate,
		snapshot: extensionsruntime.RuntimeInstanceSnapshot{
			Identity: identity, ExtensionVersion: step.Provider.Artifact.ExtensionVersion,
			ArtifactDigest: step.Provider.Artifact.PackageDigest, Active: true,
		},
	}
}

func (r *testPluginGuardRuntime) InspectRuntimeInstance(identity extensionsruntime.RuntimeInstanceIdentity) (extensionsruntime.RuntimeInstanceSnapshot, error) {
	r.inspectCalls++
	if identity != r.snapshot.Identity {
		return extensionsruntime.RuntimeInstanceSnapshot{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	return r.snapshot, nil
}

func (r *testPluginGuardRuntime) AcquireRuntimeCall(
	ctx context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	class extensionsruntime.RuntimeCallClass,
) (*extensionsruntime.RuntimeAdmissionLease, error) {
	r.acquireCalls++
	r.class = class
	if identity != r.snapshot.Identity {
		return nil, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	return r.gate.Acquire(ctx, class)
}

func (r *testPluginGuardRuntime) InvokeGuardInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2GuardRequest,
) error {
	r.invokeCalls++
	if identity != r.snapshot.Identity {
		return extensionsruntime.ErrRuntimeInstanceNotFound
	}
	r.request = request
	return r.invokeErr
}

var _ ExactPluginGuardRuntime = (*testPluginGuardRuntime)(nil)
