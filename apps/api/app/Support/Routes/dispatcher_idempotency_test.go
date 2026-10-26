package routes

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestDispatcherCompletesRequiredIdempotencyAroundFirstExecution(t *testing.T) {
	step := dispatchPluginStep(RoutePhaseHandler, "demo.route.create", extensionmanifest.RouteActionAdd)
	lease := &dispatchIdempotencyLease{}
	replay := &dispatchIdempotencyController{lease: lease}
	invoker := &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
		response := DispatchResponse{Status: http.StatusCreated, Body: []byte(`{"id":42}`)}
		return RouteInvocationResult{Response: &response}, nil
	}}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/custom", nil, []RouteExecutionStep{step}, 0)},
		Steps: invoker, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
		Policies:    dispatchPolicyResolver{policy: RouteExecutionPolicy{Idempotency: "required.24h@1", IdempotencyRequired: true}},
		Idempotency: replay,
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil)
	if err != nil || !result.Handled || result.Response.Status != http.StatusCreated ||
		replay.calls != 1 || lease.completeCalls != 1 || lease.abortCalls != 0 {
		t.Fatalf("result=%#v replay=%#v lease=%#v err=%v", result, replay, lease, err)
	}
}

func TestDispatcherReauthorizesReplayWithoutCallingPlugin(t *testing.T) {
	step := dispatchPluginStep(RoutePhaseHandler, "demo.route.create", extensionmanifest.RouteActionAdd)
	guard := &dispatchGuard{}
	replay := &dispatchIdempotencyController{replay: &RouteIdempotencyReplay{Response: DispatchResponse{
		Status: http.StatusCreated, Body: []byte(`{"id":42}`),
	}}}
	invoker := &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
		t.Fatal("replay invoked plugin")
		return RouteInvocationResult{}, nil
	}}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/custom", nil, []RouteExecutionStep{step}, 0)},
		Steps: invoker, Guard: guard, Schemas: &dispatchSchemas{},
		Policies:    dispatchPolicyResolver{policy: RouteExecutionPolicy{IdempotencyRequired: true}},
		Idempotency: replay,
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil)
	if err != nil || !result.Handled || guard.calls != 1 || replay.calls != 1 {
		t.Fatalf("result=%#v guard=%d replay=%d err=%v", result, guard.calls, replay.calls, err)
	}

	guard.err = errors.New("permission revoked")
	if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil); !errors.Is(err, ErrDispatchDenied) {
		t.Fatalf("revoked replay error = %v", err)
	}
}

func TestDispatcherRejectsRequiredIdempotencyWithMutableRequestPlanBeforeBegin(t *testing.T) {
	before := dispatchPluginStep(RoutePhaseBefore, "demo.route.idempotent_before", extensionmanifest.RouteActionBefore)
	before.MutableRequestFields = []string{"/query/tag"}
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.idempotent_handler", extensionmanifest.RouteActionAdd)
	controller := &dispatchIdempotencyController{lease: &dispatchIdempotencyLease{}}
	guard := &dispatchGuard{}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/custom", nil, []RouteExecutionStep{before, handler}, 1)},
		Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
			t.Fatal("mutable idempotent plan invoked plugin")
			return RouteInvocationResult{}, nil
		}},
		Guard: guard, Schemas: &dispatchSchemas{},
		Policies:    dispatchPolicyResolver{policy: RouteExecutionPolicy{IdempotencyRequired: true}},
		Idempotency: controller,
	})
	if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil); !errors.Is(err, ErrDispatchIdempotencyUnavailable) {
		t.Fatalf("error=%v", err)
	}
	if controller.calls != 0 || guard.calls != 0 {
		t.Fatalf("idempotency begin=%d guard=%d", controller.calls, guard.calls)
	}
}

func TestDispatcherRejectsMalformedRequiredReplayRequestBeforeBegin(t *testing.T) {
	step := dispatchPluginStep(RoutePhaseHandler, "demo.route.idempotent_invalid", extensionmanifest.RouteActionAdd)
	for _, test := range []struct {
		name    string
		request DispatchRequest
	}{
		{
			name:    "malformed query",
			request: DispatchRequest{Method: "POST", Path: "/invalid-replay", Query: "%"},
		},
		{
			name: "oversized connection metadata",
			request: DispatchRequest{
				Method: "POST", Path: "/invalid-replay",
				Headers: http.Header{"Connection": {strings.Repeat("x", routeMutationMetadataMaximumBytes+1)}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &dispatchIdempotencyController{lease: &dispatchIdempotencyLease{}}
			guard := &dispatchGuard{}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/invalid-replay", nil, []RouteExecutionStep{step}, 0)},
				Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
					t.Fatal("invalid replay request invoked plugin")
					return RouteInvocationResult{}, nil
				}},
				Guard: guard, Schemas: &dispatchSchemas{},
				Policies:    dispatchPolicyResolver{policy: RouteExecutionPolicy{IdempotencyRequired: true}},
				Idempotency: controller,
			})

			if result, err := dispatcher.Dispatch(context.Background(), test.request, nil); result.Handled ||
				!errors.Is(err, ErrDispatchIdempotencyKeyInvalid) || controller.calls != 0 || guard.calls != 0 {
				t.Fatalf("result=%#v error=%v controller=%d guard=%d", result, err, controller.calls, guard.calls)
			}
		})
	}
}

func TestDispatcherAbortsRequiredIdempotencyWhenPluginGuardFailsBeforeHandler(t *testing.T) {
	for _, variant := range dispatcherGuardFailureVariants {
		for _, failure := range []struct {
			name    string
			kind    PluginGuardFailureKind
			wantErr error
		}{
			{name: "denied", kind: PluginGuardFailureDenied, wantErr: ErrDispatchDenied},
			{name: "crash", kind: PluginGuardFailureCrash, wantErr: ErrDispatchTransport},
			{name: "timeout", kind: PluginGuardFailureTimeout, wantErr: ErrDispatchTransport},
		} {
			t.Run(variant.name+"/"+failure.name, func(t *testing.T) {
				step := dispatcherGuardFailureStep(
					RoutePhaseHandler, "demo.route.idempotent_guard_failure", extensionmanifest.RouteActionAdd, variant.raw,
				)
				lease := &dispatchIdempotencyLease{}
				guard := &dispatcherGuardFailureAuthorizer{
					failureRouteID: step.RouteID, failAt: 1,
					failure: NewPluginGuardFailure(failure.kind, true),
				}
				dispatcher := NewDispatcher(DispatcherConfig{
					Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/idempotent-guard", nil, []RouteExecutionStep{step}, 0)},
					Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
						t.Fatal("guard failure reached route handler")
						return RouteInvocationResult{}, nil
					}},
					Guard: guard, Schemas: &dispatchSchemas{},
					Policies:    dispatchPolicyResolver{policy: RouteExecutionPolicy{IdempotencyRequired: true}},
					Idempotency: &dispatchIdempotencyController{lease: lease},
				})

				result, err := dispatcher.Dispatch(
					context.Background(), DispatchRequest{Method: "POST", Path: "/idempotent-guard"}, nil,
				)
				if !errors.Is(err, failure.wantErr) || result.Handled ||
					lease.abortCalls != 1 || lease.completeCalls != 0 {
					t.Fatalf("result=%#v lease=%#v error=%v", result, lease, err)
				}
			})
		}
	}
}

func TestDispatcherAbortsFailedExecutionButPreservesUnknownCompletion(t *testing.T) {
	step := dispatchPluginStep(RoutePhaseHandler, "demo.route.create", extensionmanifest.RouteActionAdd)
	policy := dispatchPolicyResolver{policy: RouteExecutionPolicy{IdempotencyRequired: true}}
	t.Run("handler failure releases lease", func(t *testing.T) {
		lease := &dispatchIdempotencyLease{}
		dispatcher := NewDispatcher(DispatcherConfig{
			Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/custom", nil, []RouteExecutionStep{step}, 0)},
			Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
				return RouteInvocationResult{}, errors.New("plugin failed")
			}},
			Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{}, Policies: policy,
			Idempotency: &dispatchIdempotencyController{lease: lease},
		})
		if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil); !errors.Is(err, ErrDispatchTransport) {
			t.Fatalf("error = %v", err)
		}
		if lease.abortCalls != 1 || lease.completeCalls != 0 {
			t.Fatalf("lease = %#v", lease)
		}
	})

	t.Run("observed transport failure keeps pending", func(t *testing.T) {
		lease := &dispatchIdempotencyLease{}
		dispatcher := NewDispatcher(DispatcherConfig{
			Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/custom", nil, []RouteExecutionStep{step}, 0)},
			Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
				return RouteInvocationResult{SideEffectStarted: true}, errors.New("plugin failed after dispatch")
			}},
			Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{}, Policies: policy,
			Idempotency: &dispatchIdempotencyController{lease: lease},
		})
		if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil); !errors.Is(err, ErrDispatchTransport) {
			t.Fatalf("error = %v", err)
		}
		if lease.abortCalls != 0 || lease.completeCalls != 0 {
			t.Fatalf("lease = %#v", lease)
		}
	})

	t.Run("response schema rejection keeps pending", func(t *testing.T) {
		lease := &dispatchIdempotencyLease{}
		responseStep := step
		responseStep.RequestSchema = ""
		response := DispatchResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}
		dispatcher := NewDispatcher(DispatcherConfig{
			Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/custom", nil, []RouteExecutionStep{responseStep}, 0)},
			Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
				return RouteInvocationResult{Response: &response, ResponseStarted: true}, nil
			}},
			Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{err: errors.New("response rejected")}, Policies: policy,
			Idempotency: &dispatchIdempotencyController{lease: lease},
		})
		if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil); !errors.Is(err, ErrDispatchSchema) {
			t.Fatalf("error = %v", err)
		}
		if lease.abortCalls != 0 || lease.completeCalls != 0 {
			t.Fatalf("lease = %#v", lease)
		}
	})

	t.Run("completion failure keeps pending", func(t *testing.T) {
		lease := &dispatchIdempotencyLease{completeErr: errors.New("redis unavailable")}
		response := DispatchResponse{Status: http.StatusOK}
		dispatcher := NewDispatcher(DispatcherConfig{
			Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/custom", nil, []RouteExecutionStep{step}, 0)},
			Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
				return RouteInvocationResult{Response: &response}, nil
			}},
			Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{}, Policies: policy,
			Idempotency: &dispatchIdempotencyController{lease: lease},
		})
		if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil); !errors.Is(err, ErrDispatchIdempotencyUnavailable) {
			t.Fatalf("error = %v", err)
		}
		if lease.completeCalls != 1 || lease.abortCalls != 0 {
			t.Fatalf("lease = %#v", lease)
		}
	})
}

type dispatchPolicyResolver struct {
	policy RouteExecutionPolicy
	err    error
}

func (r dispatchPolicyResolver) ResolveRouteExecutionPolicy(RouteExecutionStep) (RouteExecutionPolicy, error) {
	return r.policy, r.err
}

type dispatchIdempotencyController struct {
	lease                   RouteIdempotencyLease
	replay                  *RouteIdempotencyReplay
	err                     error
	calls                   int
	mutationReplayAvailable bool
}

func (c *dispatchIdempotencyController) Begin(
	context.Context,
	RouteExecutionPlan,
	RouteExecutionStep,
	RouteExecutionPolicy,
	DispatchRequest,
) (RouteIdempotencyLease, *RouteIdempotencyReplay, error) {
	c.calls++
	return c.lease, c.replay, c.err
}

func (c *dispatchIdempotencyController) MutationReplayAvailable() bool {
	return c != nil && c.mutationReplayAvailable
}

type dispatchIdempotencyLease struct {
	completeCalls int
	abortCalls    int
	completeErr   error
	abortErr      error
	completed     RouteIdempotencyCompletion
}

func (l *dispatchIdempotencyLease) Complete(_ context.Context, completion RouteIdempotencyCompletion) error {
	l.completeCalls++
	l.completed = RouteIdempotencyCompletion{
		Response:              cloneDispatchResponse(completion.Response),
		Authorization:         cloneRouteReplayAuthorization(completion.Authorization),
		ResponseContractKnown: completion.ResponseContractKnown,
		ResponseContract:      cloneRouteReplayResponseContract(completion.ResponseContract),
	}
	return l.completeErr
}

func (l *dispatchIdempotencyLease) Abort(context.Context) error {
	l.abortCalls++
	return l.abortErr
}
