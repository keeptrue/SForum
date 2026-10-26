package routes

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestDispatcherExecutesBufferedChainInPlanOrder(t *testing.T) {
	steps := []RouteExecutionStep{
		dispatchPluginStep(RoutePhaseGlobal, "demo.route.global", extensionmanifest.RouteActionGlobalMiddleware),
		dispatchPluginStep(RoutePhaseBefore, "demo.route.before", extensionmanifest.RouteActionBefore),
		dispatchPluginStep(RoutePhaseFilter, "demo.route.filter", extensionmanifest.RouteActionFilter),
		dispatchPluginStep(RoutePhaseWrap, "demo.route.wrap", extensionmanifest.RouteActionWrap),
		dispatchCoreStep("core.route.topic.show"),
		dispatchPluginStep(RoutePhaseAfter, "demo.route.after", extensionmanifest.RouteActionAfter),
	}
	steps[5].MutableResponseFields = []string{"/headers/x-after"}
	order := make([]string, 0, len(steps)+2)
	invoker := &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
		order = append(order, string(input.Step.Phase)+":"+input.Step.RouteID+":"+string(input.Stage))
		if input.Step.Phase == RoutePhaseAfter && input.Stage == InvocationStageResponse {
			if input.Response == nil || string(input.Response.Body) != `{"source":"core"}` {
				t.Fatalf("after response = %#v", input.Response)
			}
			return RouteInvocationResult{ResponsePatch: []RoutePatchOperation{{
				Kind: RoutePatchAdd, Path: "/headers/x-after", Value: []byte(`["yes"]`),
			}}}, nil
		}
		return RouteInvocationResult{}, nil
	}}
	core := &dispatchCoreInvoker{invoke: func(_ context.Context, step RouteExecutionStep, request DispatchRequest) (DispatchResponse, error) {
		order = append(order, string(step.Phase)+":"+step.RouteID+":"+string(InvocationStageHandler))
		if request.Params["id"] != "42" || request.Headers.Get("X-Test") != "present" {
			t.Fatalf("core request = %#v", request)
		}
		return DispatchResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"source":"core"}`)}, nil
	}}
	guard := &dispatchGuard{}
	schemas := &dispatchSchemas{}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan("GET", "/topics/42", map[string]string{"id": "42"}, steps, 4)},
		Steps: invoker, Guard: guard, Schemas: schemas,
	})

	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
		Method: "GET", Path: "/topics/42", Headers: http.Header{"X-Test": []string{"present"}},
	}, core)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{
		"global:demo.route.global:request", "before:demo.route.before:request",
		"filter:demo.route.filter:request", "wrap:demo.route.wrap:request",
		"handler:core.route.topic.show:handler", "wrap:demo.route.wrap:response",
		"filter:demo.route.filter:response", "after:demo.route.after:response",
	}
	if !result.Handled || result.Response.Status != http.StatusOK || result.Response.Headers.Get("X-After") != "yes" || !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("result=%#v order=%#v", result, order)
	}
	// The final payload is validated once more against the last applicable
	// response contract before the Dispatcher finalizes it.
	if guard.calls != 7 || schemas.requestCalls != 7 || schemas.responseCalls != 5 {
		t.Fatalf("guard=%d request schemas=%d response schemas=%d", guard.calls, schemas.requestCalls, schemas.responseCalls)
	}
}

func TestDispatcherLeavesCoreOnlyPlanEntirelyUnhandled(t *testing.T) {
	core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		t.Fatal("core-only plan must not enter buffered CoreInvoker")
		return DispatchResponse{}, nil
	}}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan(
			"GET", "/download", nil, []RouteExecutionStep{dispatchCoreStep("core.route.download")}, 0,
		)},
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/download"}, core)
	if err != nil || result.Handled || core.calls != 0 {
		t.Fatalf("result=%#v core calls=%d err=%v", result, core.calls, err)
	}
}

func TestDispatcherSafeFallbackRequiresPristineGETOrHEAD(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		t.Run(method, func(t *testing.T) {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.replace", extensionmanifest.RouteActionReplace)
			step.Fallback = "readonly_core"
			core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
				return DispatchResponse{Status: http.StatusOK, Body: []byte("core")}, nil
			}}
			dispatcher := dispatchTestDispatcher(dispatchPlan(method, "/resource", nil, []RouteExecutionStep{step}, 0), &dispatchStepInvoker{
				invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
					return RouteInvocationResult{}, errors.New("plugin unavailable")
				},
			})
			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: method, Path: "/resource"}, core)
			if err != nil || !result.Handled || string(result.Response.Body) != "core" || core.calls != 1 {
				t.Fatalf("result=%#v core=%d err=%v", result, core.calls, err)
			}
		})
	}
}

func TestDispatcherSkipsPairedResponseStageWhenRequestStageFallsBack(t *testing.T) {
	for _, action := range []string{extensionmanifest.RouteActionFilter, extensionmanifest.RouteActionWrap} {
		t.Run(action, func(t *testing.T) {
			modifier := dispatchPluginStep(RoutePhaseFilter, "demo.route."+action, action)
			if action == extensionmanifest.RouteActionWrap {
				modifier.Phase = RoutePhaseWrap
			}
			modifier.Fallback = "readonly_core"
			coreStep := dispatchCoreStep("core.route.fallback")
			requestCalls := 0
			responseCalls := 0
			core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
				return DispatchResponse{Status: http.StatusOK, Body: []byte("core")}, nil
			}}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: dispatchPlan("GET", "/fallback", nil, []RouteExecutionStep{modifier, coreStep}, 1)},
				Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
					switch input.Stage {
					case InvocationStageRequest:
						requestCalls++
						return RouteInvocationResult{}, errors.New("modifier unavailable")
					case InvocationStageResponse:
						responseCalls++
						t.Fatal("response half executed after request half fallback")
					}
					return RouteInvocationResult{}, nil
				}},
				Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
			})
			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/fallback"}, core)
			if err != nil || string(result.Response.Body) != "core" || requestCalls != 1 || responseCalls != 0 || core.calls != 1 {
				t.Fatalf("result=%#v request=%d response=%d core=%d err=%v", result, requestCalls, responseCalls, core.calls, err)
			}
		})
	}
}

func TestDispatcherRejectsMutableBodyWithoutMatchingSchemaBeforeInvocation(t *testing.T) {
	before := dispatchPluginStep(RoutePhaseBefore, "demo.route.request_body_without_schema", extensionmanifest.RouteActionBefore)
	before.MutableRequestFields, before.RequestSchema = []string{"/body/title"}, ""
	after := dispatchPluginStep(RoutePhaseAfter, "demo.route.response_body_without_schema", extensionmanifest.RouteActionAfter)
	after.MutableResponseFields, after.ResponseSchema = []string{"/body/title"}, ""
	coreStep := dispatchCoreStep("core.route.body")
	for _, test := range []struct {
		name     string
		steps    []RouteExecutionStep
		terminal int
	}{
		{name: "request", steps: []RouteExecutionStep{before, coreStep}, terminal: 1},
		{name: "response", steps: []RouteExecutionStep{coreStep, after}, terminal: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			core := &dispatchCoreInvoker{}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: dispatchPlan("GET", "/body", nil, test.steps, test.terminal)},
				Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
					t.Fatal("schema-less body mutation invoked plugin")
					return RouteInvocationResult{}, nil
				}}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
			})
			if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/body"}, core); !errors.Is(err, ErrInvalidExecutionPlan) || core.calls != 0 {
				t.Fatalf("core=%d error=%v", core.calls, err)
			}
		})
	}
}

func TestDispatcherRejectsDriftedPhaseActionTopology(t *testing.T) {
	core := dispatchCoreStep("core.route.topology")
	for _, test := range []struct {
		name     string
		steps    []RouteExecutionStep
		terminal int
	}{
		{name: "before phase with wrap action", steps: []RouteExecutionStep{
			dispatchPluginStep(RoutePhaseBefore, "demo.route.drifted_before", extensionmanifest.RouteActionWrap), core,
		}, terminal: 1},
		{name: "after phase with wrap action", steps: []RouteExecutionStep{
			core, dispatchPluginStep(RoutePhaseAfter, "demo.route.drifted_after", extensionmanifest.RouteActionWrap),
		}, terminal: 0},
		{name: "handler phase with before action", steps: []RouteExecutionStep{
			dispatchPluginStep(RoutePhaseHandler, "demo.route.drifted_handler", extensionmanifest.RouteActionBefore),
		}, terminal: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: dispatchPlan("GET", "/topology", nil, test.steps, test.terminal)},
				Steps: &dispatchStepInvoker{}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
			})
			if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/topology"}, &dispatchCoreInvoker{}); !errors.Is(err, ErrInvalidExecutionPlan) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDispatcherBufferedErrorResponseRemainsSafeToDiscard(t *testing.T) {
	step := dispatchPluginStep(RoutePhaseHandler, "demo.route.replace", extensionmanifest.RouteActionReplace)
	step.Fallback = "readonly_core"
	core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		return DispatchResponse{Status: http.StatusOK, Body: []byte("core")}, nil
	}}
	dispatcher := dispatchTestDispatcher(dispatchPlan("GET", "/resource", nil, []RouteExecutionStep{step}, 0), &dispatchStepInvoker{
		invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
			buffered := DispatchResponse{Status: http.StatusBadGateway, Body: []byte("discarded")}
			return RouteInvocationResult{Response: &buffered}, errors.New("plugin unavailable")
		},
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/resource"}, core)
	if err != nil || string(result.Response.Body) != "core" || core.calls != 1 {
		t.Fatalf("result=%#v core=%d err=%v", result, core.calls, err)
	}
}

func TestDispatcherNeverRunsSecondWriterAfterSideEffectOrOnUnsafeMethod(t *testing.T) {
	tests := []struct {
		name            string
		method          string
		sideEffect      bool
		responseStarted bool
	}{
		{name: "get after side effect", method: "GET", sideEffect: true},
		{name: "get after response started", method: "GET", responseStarted: true},
		{name: "unsafe pristine", method: "POST", sideEffect: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.replace", extensionmanifest.RouteActionReplace)
			step.Fallback = "readonly_core"
			core := &dispatchCoreInvoker{}
			dispatcher := dispatchTestDispatcher(dispatchPlan(test.method, "/resource", nil, []RouteExecutionStep{step}, 0), &dispatchStepInvoker{
				invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
					if test.sideEffect {
						input.Commit.SideEffectStarted()
					}
					if test.responseStarted {
						input.Commit.ResponseStarted()
					}
					return RouteInvocationResult{
						SideEffectStarted: test.sideEffect, ResponseStarted: test.responseStarted,
					}, errors.New("writer failed")
				},
			})
			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: test.method, Path: "/resource"}, core)
			if !errors.Is(err, ErrDispatchTransport) || result.Handled || core.calls != 0 {
				t.Fatalf("result=%#v core=%d err=%v", result, core.calls, err)
			}
		})
	}
}

func TestDispatcherFailsClosedForGuardAndSchema(t *testing.T) {
	step := dispatchPluginStep(RoutePhaseHandler, "demo.route.add", extensionmanifest.RouteActionAdd)
	plan := dispatchPlan("POST", "/custom", nil, []RouteExecutionStep{step}, 0)
	t.Run("guard", func(t *testing.T) {
		dispatcher := NewDispatcher(DispatcherConfig{
			Plans: dispatchPlanResolver{plan: plan}, Steps: &dispatchStepInvoker{},
			Guard: &dispatchGuard{err: errors.New("denied")}, Schemas: &dispatchSchemas{},
		})
		_, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil)
		if !errors.Is(err, ErrDispatchDenied) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("missing schema validator", func(t *testing.T) {
		dispatcher := NewDispatcher(DispatcherConfig{
			Plans: dispatchPlanResolver{plan: plan}, Steps: &dispatchStepInvoker{}, Guard: &dispatchGuard{},
		})
		_, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/custom"}, nil)
		if !errors.Is(err, ErrDispatchSchema) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestDispatcherIssuesRawAuthorityOnlyAfterExactStepAuthorization(t *testing.T) {
	tests := []struct {
		name   string
		step   RouteExecutionStep
		raw    bool
		denied bool
	}{
		{name: "ordinary", step: dispatchPluginStep(RoutePhaseHandler, "demo.route.ordinary", extensionmanifest.RouteActionAdd)},
		// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 不再授予 raw 权限，
		// 该步骤必须在授权阶段就被拒绝，绝不带 raw 权限进入传输层。
		{name: "legacy core raw refused", step: func() RouteExecutionStep {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.raw", extensionmanifest.RouteActionAdd)
			step.Guard = extensionmanifest.GuardCoreRaw
			return step
		}(), denied: true},
		{name: "declared raw guard", step: func() RouteExecutionStep {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.declared_raw", extensionmanifest.RouteActionAdd)
			step.Guard = "demo.route.raw_guard"
			step.PluginGuard = PluginGuardBinding{
				ID: step.Guard, ContractVersion: step.Guard + "@1", Kind: "raw_request",
				Entry: "backend/raw_guard", Digest: strings.Repeat("b", 64),
			}
			return step
		}(), raw: true},
		{name: "custom guard", step: func() RouteExecutionStep {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.custom", extensionmanifest.RouteActionAdd)
			step.Guard = "demo.route.custom_guard"
			step.PluginGuard = PluginGuardBinding{
				ID: step.Guard, ContractVersion: step.Guard + "@1", Kind: "custom",
				Entry: "backend/custom_guard", Digest: strings.Repeat("c", 64),
			}
			return step
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invoked := false
			plan := dispatchPlan("POST", "/authority", nil, []RouteExecutionStep{test.step}, 0)
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: plan}, Guard: &dispatchAuthorityGuard{}, Schemas: &dispatchSchemas{},
				Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
					invoked = true
					if input.RawRequestAuthorized() != test.raw {
						t.Fatalf("raw authority = %v, want %v", input.RawRequestAuthorized(), test.raw)
					}
					response := DispatchResponse{Status: http.StatusNoContent}
					return RouteInvocationResult{Response: &response}, nil
				}},
			})
			result, err := dispatcher.Dispatch(
				context.Background(), DispatchRequest{Method: "POST", Path: "/authority"}, nil,
			)
			if test.denied {
				if err == nil || !errors.Is(err, ErrDispatchDenied) || invoked || result.Handled {
					t.Fatalf("result=%#v invoked=%v err=%v", result, invoked, err)
				}
				return
			}
			if err != nil || !result.Handled || !invoked {
				t.Fatalf("result=%#v invoked=%v err=%v", result, invoked, err)
			}
		})
	}
}

func TestDispatcherLegacyGuardCannotAuthorizeRawRequest(t *testing.T) {
	step := dispatchPluginStep(RoutePhaseHandler, "demo.route.legacy_raw", extensionmanifest.RouteActionAdd)
	step.Guard = extensionmanifest.GuardCoreRaw
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/authority", nil, []RouteExecutionStep{step}, 0)},
		Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
			t.Fatal("legacy guard minted raw authority")
			return RouteInvocationResult{}, nil
		}},
		Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
	})
	_, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/authority"}, nil)
	if !errors.Is(err, ErrDispatchDenied) {
		t.Fatalf("error = %v", err)
	}
}

func TestRouteInvocationAuthorityRejectsExactBindingDrift(t *testing.T) {
	step := dispatchPluginStep(RoutePhaseAfter, "demo.route.bound_raw", extensionmanifest.RouteActionAfter)
	// 决定 R1（2026-10-16）：raw 权限只能来自包内显式声明的 kind=raw_request 守卫。
	// 继续用 core.guard.raw_request 会让这个响应阶段步骤在授权时被拒绝，而拒绝又会
	// 被「提交后失败」路径吸收，使整张漂移矩阵静默地不再执行。
	step.Guard = "demo.route.bound_raw_guard"
	step.PluginGuard = PluginGuardBinding{
		ID: step.Guard, ContractVersion: step.Guard + "@1", Kind: "raw_request",
		Entry: "backend/bound_raw_guard", Digest: strings.Repeat("b", 64),
	}
	reached := false
	request := DispatchRequest{
		Method: "POST", Path: "/authority", Headers: http.Header{"Cookie": {"session=secret"}},
		Body: []byte(`{"ok":true}`), ActorID: 42, Authenticated: true,
	}
	plan := dispatchPlan(request.Method, request.Path, nil, []RouteExecutionStep{
		dispatchCoreStep("core.route.authority"), step,
	}, 0)
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan}, Guard: &dispatchAuthorityGuard{}, Schemas: &dispatchSchemas{},
		Failures: &recordingRouteFailureSink{},
		Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
			reached = true
			if !input.RawRequestAuthorized() {
				t.Fatal("exact invocation lost raw authority")
			}
			if input.Response == nil {
				t.Fatal("after invocation response is nil")
			}
			mutations := []func(*RouteInvocation){
				func(value *RouteInvocation) { value.PlanRevision++ },
				func(value *RouteInvocation) { value.StepIndex++ },
				func(value *RouteInvocation) { value.Stage = InvocationStage("forged") },
				func(value *RouteInvocation) { value.Commit = NewRouteCommitObserver() },
				func(value *RouteInvocation) { value.Step.RouteID += ".forged" },
				func(value *RouteInvocation) { value.Step.Provider.Artifact.PackageDigest = strings.Repeat("d", 64) },
				func(value *RouteInvocation) { value.Request.Path += "/forged" },
				func(value *RouteInvocation) { value.Request.Headers.Set("Cookie", "session=forged") },
				func(value *RouteInvocation) { value.Request.hostMutatedParams = true },
				func(value *RouteInvocation) { value.Response = nil },
				func(value *RouteInvocation) { value.Response.Status++ },
				func(value *RouteInvocation) { value.Response.Headers.Set("X-Forged", "yes") },
				func(value *RouteInvocation) { value.Response.Body = []byte("forged") },
			}
			for index, mutate := range mutations {
				forged := input
				forged.Step = cloneRouteExecutionSteps([]RouteExecutionStep{input.Step})[0]
				forged.Request = cloneDispatchRequest(input.Request)
				if input.Response != nil {
					response := cloneDispatchResponse(*input.Response)
					forged.Response = &response
				}
				mutate(&forged)
				if forged.RawRequestAuthorized() {
					t.Fatalf("mutation %d retained raw authority", index)
				}
			}
			return RouteInvocationResult{}, nil
		}},
	})
	core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		return DispatchResponse{
			Status: http.StatusCreated, Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"source":"core"}`),
		}, nil
	}}
	if _, err := dispatcher.Dispatch(context.Background(), request, core); err != nil {
		t.Fatal(err)
	}
	if !reached {
		t.Fatal("raw-authority drift matrix never reached the exact plugin invocation")
	}
}

func TestDispatcherDoesNotIssueAuthorityWhenGuardDenies(t *testing.T) {
	step := dispatchPluginStep(RoutePhaseHandler, "demo.route.raw_denied", extensionmanifest.RouteActionAdd)
	step.Guard = extensionmanifest.GuardCoreRaw
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/authority", nil, []RouteExecutionStep{step}, 0)},
		Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
			t.Fatal("denied raw step reached the transport")
			return RouteInvocationResult{}, nil
		}},
		Guard: &dispatchGuard{err: errors.New("denied")}, Schemas: &dispatchSchemas{},
	})
	_, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "POST", Path: "/authority"}, nil)
	if !errors.Is(err, ErrDispatchDenied) {
		t.Fatalf("error = %v", err)
	}
}

func TestDispatcherPropagatesTimeoutAndCallerCancellation(t *testing.T) {
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
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.slow", extensionmanifest.RouteActionAdd)
			step.TimeoutMS = 5
			dispatcher := dispatchTestDispatcher(dispatchPlan("GET", "/slow", nil, []RouteExecutionStep{step}, 0), &dispatchStepInvoker{
				invoke: func(ctx context.Context, _ RouteInvocation) (RouteInvocationResult, error) {
					<-ctx.Done()
					return RouteInvocationResult{}, ctx.Err()
				},
			})
			ctx, cancel := test.parent()
			defer cancel()
			_, err := dispatcher.Dispatch(ctx, DispatchRequest{Method: "GET", Path: "/slow"}, nil)
			if !errors.Is(err, ErrDispatchTransport) || !errors.Is(err, test.wantErr) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRouteCommitObserverIsSingleFinalizerAndNeverReturnsToPristine(t *testing.T) {
	observer := NewRouteCommitObserver()
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); observer.ResponseStarted() }()
	go func() { defer wait.Done(); observer.SideEffectStarted() }()
	wait.Wait()
	if observer.State() != RouteCommitResponseStarted || !observer.ExecutionObserved() {
		t.Fatalf("state=%q", observer.State())
	}
	if !observer.Finalize() || observer.Finalize() || observer.State() != RouteCommitFinal {
		t.Fatalf("final state=%q", observer.State())
	}

	finalizedFirst := NewRouteCommitObserver()
	if !finalizedFirst.Finalize() || finalizedFirst.ExecutionObserved() {
		t.Fatalf("unexpected pristine finalization: state=%q observed=%t", finalizedFirst.State(), finalizedFirst.ExecutionObserved())
	}
	if finalizedFirst.SideEffectStarted() || !finalizedFirst.ExecutionObserved() || finalizedFirst.State() != RouteCommitFinal {
		t.Fatalf("late evidence was lost: state=%q observed=%t", finalizedFirst.State(), finalizedFirst.ExecutionObserved())
	}
}

type dispatchPlanResolver struct {
	plan RouteExecutionPlan
	err  error
}

func (r dispatchPlanResolver) BuildExecutionPlan(context.Context, string, string) (RouteExecutionPlan, error) {
	return r.plan, r.err
}

type dispatchStepInvoker struct {
	invoke func(context.Context, RouteInvocation) (RouteInvocationResult, error)
}

func (*dispatchStepInvoker) SupportsMode(mode string) bool {
	return mode == extensionmanifest.RouteModeHTTP
}

func (i *dispatchStepInvoker) Invoke(ctx context.Context, input RouteInvocation) (RouteInvocationResult, error) {
	if i.invoke == nil {
		return RouteInvocationResult{}, nil
	}
	return i.invoke(ctx, input)
}

type dispatchGuard struct {
	calls int
	err   error
}

type dispatchAuthorityGuard struct {
	calls int
	err   error
}

func (g *dispatchAuthorityGuard) AuthorizeRoute(
	_ context.Context,
	plan RouteExecutionPlan,
	stepIndex int,
	step RouteExecutionStep,
	request DispatchRequest,
) (RouteGuardAuthorization, error) {
	g.calls++
	if g.err != nil {
		return RouteGuardAuthorization{}, g.err
	}
	authorization, ok := authorizedRouteGuardAuthorization(plan, stepIndex, step, request)
	if !ok {
		return RouteGuardAuthorization{}, ErrCoreGuardEvaluatorUnavailable
	}
	return authorization, nil
}

func (g *dispatchAuthorityGuard) Authorize(
	ctx context.Context,
	plan RouteExecutionPlan,
	step RouteExecutionStep,
	request DispatchRequest,
) error {
	stepIndex, ok := uniqueRouteExecutionStepIndex(plan, step)
	if !ok {
		return ErrCoreGuardEvaluatorUnavailable
	}
	_, err := g.AuthorizeRoute(ctx, plan, stepIndex, step, request)
	return err
}

func (g *dispatchGuard) Authorize(context.Context, RouteExecutionPlan, RouteExecutionStep, DispatchRequest) error {
	g.calls++
	return g.err
}

type dispatchSchemas struct {
	requestCalls  int
	responseCalls int
	err           error
}

func (s *dispatchSchemas) ValidateRequest(context.Context, RouteExecutionStep, DispatchRequest) error {
	s.requestCalls++
	return s.err
}

func (s *dispatchSchemas) ValidateResponse(context.Context, RouteExecutionStep, DispatchRequest, DispatchResponse) error {
	s.responseCalls++
	return s.err
}

type dispatchCoreInvoker struct {
	calls  int
	invoke func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error)
}

func (i *dispatchCoreInvoker) InvokeCore(ctx context.Context, step RouteExecutionStep, request DispatchRequest) (DispatchResponse, error) {
	i.calls++
	if i.invoke == nil {
		return DispatchResponse{}, errors.New("unexpected core invocation")
	}
	return i.invoke(ctx, step, request)
}

func dispatchTestDispatcher(plan RouteExecutionPlan, invoker StepInvoker) *Dispatcher {
	return NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan}, Steps: invoker,
		Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{}, DefaultTimeout: 50 * time.Millisecond,
	})
}

func dispatchPlan(method, path string, params map[string]string, steps []RouteExecutionStep, terminal int) RouteExecutionPlan {
	return RouteExecutionPlan{
		revision: 1, method: method, path: path, params: cloneRouteExecutionParams(params),
		unsafeMethod: method != "GET" && method != "HEAD", terminalIndex: terminal,
		chain: append([]RouteExecutionStep(nil), steps...),
	}
}

func dispatchPluginStep(phase RouteExecutionPhase, id, action string) RouteExecutionStep {
	return RouteExecutionStep{
		Phase: phase, Action: action, RouteID: id, ContractVersion: id + "@1",
		Provider: Provider{Kind: ProviderPlugin, Artifact: PluginArtifact{
			ExtensionID: "demo.route", ExtensionVersion: "1.0.0",
			PackageDigest:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RuntimeInstanceID: "runtime-1",
		}},
		Guard: extensionmanifest.GuardCorePublic, Access: "public", Mode: extensionmanifest.RouteModeHTTP,
		Handler: "route.handle", RequestSchema: id + ".request@1", ResponseSchema: id + ".response@1",
		Fallback: "closed",
	}
}

func dispatchCoreStep(id string) RouteExecutionStep {
	return RouteExecutionStep{
		Phase: RoutePhaseHandler, Action: extensionmanifest.RouteActionAdd, RouteID: id,
		ContractVersion: id + "@1", Provider: Provider{Kind: ProviderCore}, Mode: extensionmanifest.RouteModeHTTP,
	}
}
