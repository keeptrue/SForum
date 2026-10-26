package routes

import (
	"context"
	"errors"
	"net/http"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestDispatcherPrefersPlanBoundPolicyOverLegacyResolver(t *testing.T) {
	required := RouteExecutionPolicy{
		RateLimit:   routePolicyRateLimitIPWrite,
		Idempotency: routePolicyIdempotencyRequired24h, IdempotencyRequired: true,
	}
	disabled := RouteExecutionPolicy{RateLimit: routePolicyRateLimitIPWrite, Idempotency: routePolicyDisabled}
	tests := []struct {
		name              string
		bound             *RouteExecutionPolicy
		legacy            *policyResolutionResolver
		controller        bool
		wantBeginCalls    int
		wantResolverCalls int
	}{
		{
			name: "bound required ignores live disabled", bound: &required,
			legacy: &policyResolutionResolver{policy: disabled}, controller: true,
			wantBeginCalls: 1,
		},
		{
			name: "bound disabled ignores live required", bound: &disabled,
			legacy: &policyResolutionResolver{policy: required},
		},
		{
			name: "bound required needs no live resolver", bound: &required,
			controller: true, wantBeginCalls: 1,
		},
		{
			name: "bound disabled ignores live failure", bound: &disabled,
			legacy: &policyResolutionResolver{err: errors.New("live policy revision failed")},
		},
		{
			name:   "legacy publication falls back to live required",
			legacy: &policyResolutionResolver{policy: required}, controller: true,
			wantBeginCalls: 1, wantResolverCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := policyResolutionHTTPPlan(t, test.bound)
			var controller *dispatchIdempotencyController
			if test.controller {
				controller = &dispatchIdempotencyController{lease: &dispatchIdempotencyLease{}}
			}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: plan},
				Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
					response := DispatchResponse{Status: http.StatusOK}
					return RouteInvocationResult{Response: &response}, nil
				}},
				Guard: &dispatchAuthorityGuard{}, Schemas: &dispatchSchemas{},
				Policies: test.legacy, Idempotency: controller,
			})
			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
				Method: http.MethodPost, Path: "/policy-resolution",
			}, nil)
			if err != nil || !result.Handled || result.Response.Status != http.StatusOK {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			beginCalls := 0
			if controller != nil {
				beginCalls = controller.calls
			}
			resolverCalls := 0
			if test.legacy != nil {
				resolverCalls = test.legacy.calls
			}
			if beginCalls != test.wantBeginCalls || resolverCalls != test.wantResolverCalls {
				t.Fatalf("begin=%d resolver=%d", beginCalls, resolverCalls)
			}
		})
	}
}

func TestStreamDispatcherPrefersPlanBoundPolicyOverLegacyResolver(t *testing.T) {
	required := RouteExecutionPolicy{
		RateLimit:   routePolicyRateLimitIPWrite,
		Idempotency: routePolicyIdempotencyRequired24h, IdempotencyRequired: true,
	}
	disabled := RouteExecutionPolicy{RateLimit: routePolicyDisabled, Idempotency: routePolicyDisabled}
	tests := []struct {
		name              string
		bound             bool
		policy            RouteExecutionPolicy
		legacy            *policyResolutionResolver
		wantUnavailable   bool
		wantResolverCalls int
	}{
		{name: "bound required needs no live resolver", bound: true, policy: required, wantUnavailable: true},
		{name: "bound disabled ignores live required", bound: true, policy: disabled, legacy: &policyResolutionResolver{policy: required}},
		{name: "bound disabled ignores live failure", bound: true, policy: disabled, legacy: &policyResolutionResolver{err: errors.New("live policy revision failed")}},
		{name: "legacy publication uses live required", legacy: &policyResolutionResolver{policy: required}, wantUnavailable: true, wantResolverCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step := dispatchPluginStep(RoutePhaseHandler, "policy.resolution.stream", extensionmanifest.RouteActionAdd)
			step.Method = http.MethodGet
			step.Path = "/policy-resolution-stream"
			step.Mode = extensionmanifest.RouteModeSSE
			plan := dispatchPlan(http.MethodGet, step.Path, nil, []RouteExecutionStep{step}, 0)
			plan.policy, plan.policyBound = test.policy, test.bound
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: plan}, Steps: &dispatchStepInvoker{},
				Guard: &dispatchAuthorityGuard{}, Policies: test.legacy,
			})
			prepared, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{
				Method: http.MethodGet, Path: step.Path,
			})
			if test.wantUnavailable {
				if !errors.Is(err, ErrDispatchIdempotencyUnavailable) || prepared.Handled {
					t.Fatalf("prepared=%#v error=%v", prepared, err)
				}
			} else if err != nil || !prepared.Handled || prepared.Dispatch == nil {
				t.Fatalf("prepared=%#v error=%v", prepared, err)
			}
			resolverCalls := 0
			if test.legacy != nil {
				resolverCalls = test.legacy.calls
			}
			if resolverCalls != test.wantResolverCalls {
				t.Fatalf("resolver calls=%d", resolverCalls)
			}
		})
	}
}

func policyResolutionHTTPPlan(t *testing.T, policy *RouteExecutionPolicy) RouteExecutionPlan {
	t.Helper()
	artifact := routeArtifact("policy.resolution", "1.0.0", 'f')
	declaration := pluginRoute("policy.resolution.route", "/policy-resolution", 0, http.MethodPost)
	publication := Publication{Plugins: []PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}
	if policy != nil {
		publication.Policies = []RoutePolicyBinding{{
			Artifact: artifact, RouteID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Method: http.MethodPost, Policy: *policy,
		}}
	}
	registry := NewRegistry()
	if _, err := registry.Publish(publication); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(http.MethodPost, declaration.Path)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

type policyResolutionResolver struct {
	policy RouteExecutionPolicy
	err    error
	calls  int
}

func (r *policyResolutionResolver) ResolveRouteExecutionPolicy(RouteExecutionStep) (RouteExecutionPolicy, error) {
	r.calls++
	return r.policy, r.err
}
