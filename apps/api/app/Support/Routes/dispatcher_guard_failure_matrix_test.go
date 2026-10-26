package routes

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type dispatcherGuardFailureCase struct {
	name     string
	kind     PluginGuardFailureKind
	observed bool
	denied   bool
}

var dispatcherGuardFailureCases = []dispatcherGuardFailureCase{
	{name: "denied", kind: PluginGuardFailureDenied, observed: true, denied: true},
	{name: "unavailable before invocation", kind: PluginGuardFailureUnavailable},
	{name: "runtime crash", kind: PluginGuardFailureCrash, observed: true},
	{name: "runtime timeout", kind: PluginGuardFailureTimeout, observed: true},
	{name: "pre RPC timeout", kind: PluginGuardFailureTimeout},
	{name: "protocol failure", kind: PluginGuardFailureProtocol, observed: true},
	{name: "runtime canceled", kind: PluginGuardFailureCanceled, observed: true},
}

var dispatcherGuardFailureVariants = []struct {
	name string
	raw  bool
}{
	{name: "custom"},
	{name: "raw_request", raw: true},
}

func TestDispatcherPluginGuardRequestFailureMatrix(t *testing.T) {
	for _, variant := range dispatcherGuardFailureVariants {
		for _, failure := range dispatcherGuardFailureCases {
			t.Run(variant.name+"/"+failure.name, func(t *testing.T) {
				step := dispatcherGuardFailureStep(
					RoutePhaseBefore, "demo.route.guard_failure.request", extensionmanifest.RouteActionBefore, variant.raw,
				)
				// A guard transport failure is not a route invocation failure and must
				// never enter the otherwise legal safe-method fallback path.
				step.Fallback = "readonly_core"
				coreStep := dispatchCoreStep("core.route.guard_failure.request")
				guard := &dispatcherGuardFailureAuthorizer{
					failureRouteID: step.RouteID, failAt: 1,
					failure: NewPluginGuardFailure(failure.kind, failure.observed),
				}
				invoker := &dispatcherGuardFailureInvoker{t: t, verifyAuthority: true, wantRaw: variant.raw}
				core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
					t.Fatal("request-stage guard failure reached the core handler or fallback")
					return DispatchResponse{}, nil
				}}
				traces := NewRouteTraceRing(8)
				sink := &recordingRouteFailureSink{}
				dispatcher := NewDispatcher(DispatcherConfig{
					Plans: dispatchPlanResolver{plan: dispatchPlan(
						http.MethodGet, "/guard-failure", nil, []RouteExecutionStep{step, coreStep}, 1,
					)},
					Steps: invoker, Guard: guard, Schemas: &dispatchSchemas{}, Trace: traces, Failures: sink,
				})

				result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
					Method: http.MethodGet, Path: "/guard-failure",
				}, core)
				wantOutcome := RouteTraceTransportFailed
				if failure.denied {
					wantOutcome = RouteTraceDenied
					if !errors.Is(err, ErrDispatchDenied) {
						t.Fatalf("denied error=%v", err)
					}
				} else if !errors.Is(err, ErrDispatchTransport) {
					t.Fatalf("transport error=%v", err)
				}
				if result.Handled || core.calls != 0 || len(invoker.calls) != 0 || len(sink.events) != 0 {
					t.Fatalf("result=%#v core=%d invocations=%#v incidents=%#v", result, core.calls, invoker.calls, sink.events)
				}
				records := traces.RouteTraces(0)
				if len(records) != 1 || records[0].Outcome != wantOutcome ||
					records[0].InvocationStage != InvocationStageRequest || records[0].RouteID != step.RouteID ||
					records[0].CommitState != RouteCommitPristine {
					t.Fatalf("request failure traces=%#v", records)
				}
				if guard.calls[step.RouteID] != 1 {
					t.Fatalf("guard calls=%#v", guard.calls)
				}
			})
		}
	}
}

func TestDispatcherPluginGuardRequestCallerCancellationDoesNotRecordIncident(t *testing.T) {
	for _, variant := range dispatcherGuardFailureVariants {
		t.Run(variant.name, func(t *testing.T) {
			step := dispatcherGuardFailureStep(
				RoutePhaseBefore, "demo.route.guard_failure.request_canceled", extensionmanifest.RouteActionBefore, variant.raw,
			)
			guard := &dispatcherGuardFailureAuthorizer{
				failureRouteID: step.RouteID, failAt: 1,
				failure: NewPluginGuardFailure(PluginGuardFailureCanceled, true),
			}
			invoker := &dispatcherGuardFailureInvoker{t: t, verifyAuthority: true, wantRaw: variant.raw}
			core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
				t.Fatal("canceled request reached the core handler")
				return DispatchResponse{}, nil
			}}
			sink := &recordingRouteFailureSink{}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: dispatchPlan(http.MethodGet, "/guard-canceled", nil, []RouteExecutionStep{
					step, dispatchCoreStep("core.route.guard_failure.request_canceled"),
				}, 1)},
				Steps: invoker, Guard: guard, Schemas: &dispatchSchemas{}, Failures: sink,
			})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			result, err := dispatcher.Dispatch(ctx, DispatchRequest{Method: http.MethodGet, Path: "/guard-canceled"}, core)
			if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDispatchDenied) || errors.Is(err, ErrDispatchTransport) {
				t.Fatalf("caller cancellation error=%v", err)
			}
			if result.Handled || core.calls != 0 || len(invoker.calls) != 0 || len(sink.events) != 0 {
				t.Fatalf("result=%#v core=%d invocations=%#v incidents=%#v", result, core.calls, invoker.calls, sink.events)
			}
		})
	}
}

func TestDispatcherPluginGuardUnsafeResponseFailureMatrix(t *testing.T) {
	for _, variant := range dispatcherGuardFailureVariants {
		for _, action := range []string{
			extensionmanifest.RouteActionFilter,
			extensionmanifest.RouteActionWrap,
			extensionmanifest.RouteActionAfter,
		} {
			for _, failure := range dispatcherGuardFailureCases {
				t.Run(variant.name+"/"+action+"/"+failure.name, func(t *testing.T) {
					plan, failing, next := dispatcherGuardFailureResponsePlan(action, variant.raw)
					failAt := 1
					wantNextCalls := 0
					if action != extensionmanifest.RouteActionAfter {
						failAt = 2
						wantNextCalls = 1
					}
					guard := &dispatcherGuardFailureAuthorizer{
						failureRouteID: failing.RouteID, failAt: failAt,
						failure: NewPluginGuardFailure(failure.kind, failure.observed),
					}
					invoker := &dispatcherGuardFailureInvoker{t: t, verifyAuthority: true, wantRaw: variant.raw}
					traces := NewRouteTraceRing(16)
					sink := &recordingRouteFailureSink{}
					original := dispatcherGuardFailurePriorResponse()
					core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
						return original, nil
					}}
					dispatcher := NewDispatcher(DispatcherConfig{
						Plans: dispatchPlanResolver{plan: plan}, Steps: invoker, Guard: guard,
						Schemas: &dispatchSchemas{}, Trace: traces, Failures: sink,
					})

					result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
						Method: http.MethodPost, Path: "/guard-response", ActorID: 42,
					}, core)
					if err != nil || !result.Handled || !reflect.DeepEqual(result.Response, original) || core.calls != 1 {
						t.Fatalf("result=%#v original=%#v core=%d err=%v", result, original, core.calls, err)
					}
					if guard.calls[failing.RouteID] != failAt || guard.calls[next.RouteID] != wantNextCalls {
						t.Fatalf("guard calls=%#v", guard.calls)
					}
					if len(sink.events) != 1 {
						t.Fatalf("incidents=%#v", sink.events)
					}
					event := sink.events[0]
					wantCode, wantOutcome := RouteFailureTransportFailed, RouteTraceTransportFailed
					if failure.denied {
						wantCode, wantOutcome = RouteFailureGuardDenied, RouteTraceDenied
					}
					if event.FailureCode != wantCode || event.RuntimeExecutionObserved != failure.observed ||
						event.InvocationStage != InvocationStageResponse || event.Phase != failing.Phase ||
						event.Action != failing.Action || event.RouteID != failing.RouteID ||
						event.ResponseStatus != original.Status || event.CommitState != RouteCommitFinal ||
						event.Artifact != failing.Provider.Artifact {
						t.Fatalf("incident=%#v", event)
					}
					responseTraces := dispatcherGuardFailureResponseTraces(traces.RouteTraces(0))
					if len(responseTraces) != 2 || responseTraces[0].Outcome != wantOutcome ||
						responseTraces[1].Outcome != RouteTraceCommitted ||
						responseTraces[0].RouteID != failing.RouteID || responseTraces[1].RouteID != failing.RouteID {
						t.Fatalf("response traces=%#v all=%#v", responseTraces, traces.RouteTraces(0))
					}
					for _, call := range invoker.calls {
						if call.stage == InvocationStageResponse {
							t.Fatalf("guard failure reached response transport: %#v", invoker.calls)
						}
					}
				})
			}
		}
	}
}

func TestDispatcherPluginGuardUnsafeResponseCallerCancellationDoesNotRecordIncident(t *testing.T) {
	for _, variant := range dispatcherGuardFailureVariants {
		for _, action := range []string{
			extensionmanifest.RouteActionFilter,
			extensionmanifest.RouteActionWrap,
			extensionmanifest.RouteActionAfter,
		} {
			t.Run(variant.name+"/"+action, func(t *testing.T) {
				plan, failing, next := dispatcherGuardFailureResponsePlan(action, variant.raw)
				failAt := 1
				wantNextCalls := 0
				if action != extensionmanifest.RouteActionAfter {
					failAt = 2
					wantNextCalls = 1
				}
				ctx, cancel := context.WithCancel(context.Background())
				guard := &dispatcherGuardFailureAuthorizer{
					failureRouteID: failing.RouteID, failAt: failAt,
					failure: NewPluginGuardFailure(PluginGuardFailureCanceled, true),
					cancel:  cancel,
				}
				invoker := &dispatcherGuardFailureInvoker{t: t, verifyAuthority: true, wantRaw: variant.raw}
				sink := &recordingRouteFailureSink{}
				core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
					return dispatcherGuardFailurePriorResponse(), nil
				}}
				dispatcher := NewDispatcher(DispatcherConfig{
					Plans: dispatchPlanResolver{plan: plan}, Steps: invoker, Guard: guard,
					Schemas: &dispatchSchemas{}, Failures: sink,
				})

				result, err := dispatcher.Dispatch(ctx, DispatchRequest{
					Method: http.MethodPost, Path: "/guard-response-canceled",
				}, core)
				if err != nil || !result.Handled || !reflect.DeepEqual(result.Response, dispatcherGuardFailurePriorResponse()) ||
					core.calls != 1 || len(sink.events) != 0 {
					t.Fatalf("result=%#v error=%v core=%d incidents=%#v", result, err, core.calls, sink.events)
				}
				if ctx.Err() == nil || guard.calls[failing.RouteID] != failAt || guard.calls[next.RouteID] != wantNextCalls {
					t.Fatalf("context=%v guard calls=%#v", ctx.Err(), guard.calls)
				}
				for _, call := range invoker.calls {
					if call.stage == InvocationStageResponse {
						t.Fatalf("caller-canceled guard reached response transport: %#v", invoker.calls)
					}
				}
			})
		}
	}
}

func TestDispatcherPluginGuardReplayReauthorizationFailureClassification(t *testing.T) {
	tests := []struct {
		name        string
		failure     *PluginGuardFailure
		cancel      bool
		wantError   error
		wantOutcome RouteTraceOutcome
	}{
		{
			name: "unavailable", failure: NewPluginGuardFailure(PluginGuardFailureUnavailable, false),
			wantError: ErrDispatchTransport, wantOutcome: RouteTraceTransportFailed,
		},
		{
			name: "denied", failure: NewPluginGuardFailure(PluginGuardFailureDenied, true),
			wantError: ErrDispatchDenied, wantOutcome: RouteTraceDenied,
		},
		{
			name: "pre RPC timeout", failure: NewPluginGuardFailure(PluginGuardFailureTimeout, false),
			wantError: ErrDispatchTransport, wantOutcome: RouteTraceTransportFailed,
		},
		{
			name: "runtime canceled", failure: NewPluginGuardFailure(PluginGuardFailureCanceled, true),
			wantError: ErrDispatchTransport, wantOutcome: RouteTraceTransportFailed,
		},
		{
			name: "caller canceled", failure: NewPluginGuardFailure(PluginGuardFailureCanceled, true),
			cancel: true, wantError: context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step := dispatcherGuardFailureStep(
				RoutePhaseHandler, "demo.route.guard_failure.replay", extensionmanifest.RouteActionAdd, false,
			)
			guard := &dispatcherGuardFailureAuthorizer{
				failureRouteID: step.RouteID, failAt: 1, failure: test.failure,
			}
			invoker := &dispatcherGuardFailureInvoker{t: t}
			traces := NewRouteTraceRing(8)
			sink := &recordingRouteFailureSink{}
			replay := &dispatchIdempotencyController{replay: &RouteIdempotencyReplay{Response: DispatchResponse{
				Status: http.StatusCreated, Headers: http.Header{"X-Replay": {"exact"}}, Body: []byte(`{"id":42}`),
			}}}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: dispatchPlan(
					http.MethodPost, "/guard-replay", nil, []RouteExecutionStep{step}, 0,
				)},
				Steps: invoker, Guard: guard, Schemas: &dispatchSchemas{}, Trace: traces, Failures: sink,
				Policies:    dispatchPolicyResolver{policy: RouteExecutionPolicy{IdempotencyRequired: true}},
				Idempotency: replay,
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				cancel()
			}
			result, err := dispatcher.Dispatch(ctx, DispatchRequest{
				Method: http.MethodPost, Path: "/guard-replay",
			}, nil)
			if !errors.Is(err, test.wantError) || result.Handled || replay.calls != 1 ||
				guard.calls[step.RouteID] != 1 || len(invoker.calls) != 0 || len(sink.events) != 0 {
				t.Fatalf("result=%#v replay=%d guard=%#v invocations=%#v incidents=%#v err=%v",
					result, replay.calls, guard.calls, invoker.calls, sink.events, err)
			}
			if test.wantError == ErrDispatchTransport && errors.Is(err, ErrDispatchDenied) ||
				test.wantError == ErrDispatchDenied && errors.Is(err, ErrDispatchTransport) ||
				test.cancel && (errors.Is(err, ErrDispatchDenied) || errors.Is(err, ErrDispatchTransport)) {
				t.Fatalf("replay failure was misclassified: %v", err)
			}
			records := traces.RouteTraces(0)
			if test.cancel {
				if len(records) != 0 {
					t.Fatalf("caller cancellation gained a failure trace: %#v", records)
				}
				return
			}
			if len(records) != 1 || records[0].Outcome != test.wantOutcome ||
				records[0].InvocationStage != InvocationStageHandler || records[0].RouteID != step.RouteID ||
				records[0].CommitState != RouteCommitPristine {
				t.Fatalf("replay traces=%#v", records)
			}
		})
	}
}

type dispatcherGuardFailureAuthorizer struct {
	failureRouteID string
	failAt         int
	failure        error
	cancel         context.CancelFunc
	calls          map[string]int
}

func (g *dispatcherGuardFailureAuthorizer) AuthorizeRoute(
	_ context.Context,
	plan RouteExecutionPlan,
	stepIndex int,
	step RouteExecutionStep,
	request DispatchRequest,
) (RouteGuardAuthorization, error) {
	if g.calls == nil {
		g.calls = make(map[string]int)
	}
	g.calls[step.RouteID]++
	authorization, ok := authorizedRouteGuardAuthorization(plan, stepIndex, step, request)
	if !ok {
		return RouteGuardAuthorization{}, ErrCoreGuardEvaluatorUnavailable
	}
	if step.RouteID == g.failureRouteID && g.calls[step.RouteID] == g.failAt {
		if g.cancel != nil {
			g.cancel()
		}
		return RouteGuardAuthorization{}, g.failure
	}
	return authorization, nil
}

func (g *dispatcherGuardFailureAuthorizer) Authorize(
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

type dispatcherGuardFailureInvocation struct {
	routeID string
	stage   InvocationStage
}

type dispatcherGuardFailureInvoker struct {
	t               *testing.T
	verifyAuthority bool
	wantRaw         bool
	calls           []dispatcherGuardFailureInvocation
}

func (*dispatcherGuardFailureInvoker) SupportsMode(mode string) bool {
	return mode == extensionmanifest.RouteModeHTTP
}

func (i *dispatcherGuardFailureInvoker) Invoke(
	_ context.Context,
	input RouteInvocation,
) (RouteInvocationResult, error) {
	i.calls = append(i.calls, dispatcherGuardFailureInvocation{routeID: input.Step.RouteID, stage: input.Stage})
	if i.verifyAuthority && input.RawRequestAuthorized() != i.wantRaw {
		i.t.Fatalf("raw authority=%t want=%t for %#v", input.RawRequestAuthorized(), i.wantRaw, input.Step)
	}
	if input.Stage != InvocationStageRequest {
		i.t.Fatalf("guard failure reached plugin transport: %#v", input)
	}
	return RouteInvocationResult{}, nil
}

func dispatcherGuardFailureStep(
	phase RouteExecutionPhase,
	id string,
	action string,
	raw bool,
) RouteExecutionStep {
	step := dispatchPluginStep(phase, id, action)
	guardKind, guardSuffix := "custom", "custom"
	if raw {
		guardKind, guardSuffix = "raw_request", "raw"
	}
	step.Guard = "demo.route.guard_failure." + guardSuffix
	step.PluginGuard = PluginGuardBinding{
		ID: step.Guard, ContractVersion: step.Guard + "@1", Kind: guardKind,
		Entry: "backend/" + guardSuffix, Digest: strings.Repeat("b", 64),
	}
	return step
}

func dispatcherGuardFailureResponsePlan(
	action string,
	raw bool,
) (RouteExecutionPlan, RouteExecutionStep, RouteExecutionStep) {
	phase := RoutePhaseAfter
	if action == extensionmanifest.RouteActionFilter {
		phase = RoutePhaseFilter
	} else if action == extensionmanifest.RouteActionWrap {
		phase = RoutePhaseWrap
	}
	failing := dispatcherGuardFailureStep(phase, "demo.route.guard_failure."+action+".low", action, raw)
	next := dispatcherGuardFailureStep(phase, "demo.route.guard_failure."+action+".high", action, raw)
	core := dispatchCoreStep("core.route.guard_failure.response")
	if action == extensionmanifest.RouteActionAfter {
		return dispatchPlan(http.MethodPost, "/guard-response", nil, []RouteExecutionStep{core, next, failing}, 0), failing, next
	}
	return dispatchPlan(http.MethodPost, "/guard-response", nil, []RouteExecutionStep{next, failing, core}, 2), failing, next
}

func dispatcherGuardFailurePriorResponse() DispatchResponse {
	return DispatchResponse{
		Status: http.StatusCreated,
		Headers: http.Header{
			"Content-Type": {"application/json"},
			"X-Original":   {"one", "two"},
		},
		Body: []byte(`{"source":"core","private":"unchanged"}`), CanonicalPath: "/guard-canonical",
	}
}

func dispatcherGuardFailureResponseTraces(records []RouteTraceRecord) []RouteTraceRecord {
	result := make([]RouteTraceRecord, 0, 2)
	for _, record := range records {
		if record.InvocationStage == InvocationStageResponse {
			result = append(result, record)
		}
	}
	return result
}
