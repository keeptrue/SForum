package routes

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestDispatcherPreservesUnsafeResponseAndRecordsCommittedAfterFailure(t *testing.T) {
	tests := []struct {
		name         string
		code         RouteFailureCode
		outcome      RouteTraceOutcome
		guardErr     error
		requestErr   error
		transportErr error
		responseErr  error
		observed     bool
	}{
		{name: "guard denied", code: RouteFailureGuardDenied, outcome: RouteTraceDenied, guardErr: errors.New("denied")},
		{name: "request schema rejected", code: RouteFailureRequestSchemaRejected, outcome: RouteTraceSchemaRejected, requestErr: errors.New("request rejected")},
		{name: "transport failed", code: RouteFailureTransportFailed, outcome: RouteTraceTransportFailed, transportErr: errors.New("runtime crashed"), observed: true},
		{name: "response schema rejected", code: RouteFailureResponseSchemaRejected, outcome: RouteTraceSchemaRejected, responseErr: errors.New("response rejected"), observed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coreStep := dispatchCoreStep("core.route.committed")
			after := dispatchPluginStep(RoutePhaseAfter, "demo.route.after_failure", extensionmanifest.RouteActionAfter)
			next := dispatchPluginStep(RoutePhaseAfter, "demo.route.after_never", extensionmanifest.RouteActionAfter)
			after.MutableResponseFields = []string{"/status"}
			if test.responseErr != nil {
				after.RequestSchema = ""
			}
			guard := &committedAfterGuard{err: test.guardErr}
			schemas := &committedAfterSchemas{requestErr: test.requestErr, responseErr: test.responseErr}
			calls := 0
			invoker := &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
				calls++
				if input.Step.RouteID == next.RouteID {
					t.Fatal("later after contribution executed after committed failure")
				}
				if test.transportErr != nil {
					return RouteInvocationResult{SideEffectStarted: true}, test.transportErr
				}
				return RouteInvocationResult{ResponsePatch: []RoutePatchOperation{{
					Kind: RoutePatchReplace, Path: "/status", Value: []byte(`202`),
				}}}, nil
			}}
			sink := &recordingRouteFailureSink{}
			traces := NewRouteTraceRing(8)
			dispatcher := NewDispatcher(DispatcherConfig{
				// after chains are stored high-to-low and unwind low-to-high.
				Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/committed", nil, []RouteExecutionStep{coreStep, next, after}, 0)},
				Steps: invoker, Guard: guard, Schemas: schemas, Trace: traces, Failures: sink,
			})
			original := DispatchResponse{
				Status: http.StatusCreated, Headers: http.Header{"X-Original": {"yes"}}, Body: []byte(`{"source":"core"}`),
			}
			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
				Method: "POST", Path: "/committed", ActorID: 42, Authenticated: true,
			}, &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
				return original, nil
			}})
			if err != nil || !result.Handled || !reflect.DeepEqual(result.Response, original) {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			wantCalls := 1
			if test.guardErr != nil || test.requestErr != nil {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("runtime calls=%d want=%d", calls, wantCalls)
			}
			if len(sink.events) != 1 {
				t.Fatalf("failure events=%#v", sink.events)
			}
			event := sink.events[0]
			if event.FailureCode != test.code || event.StepIndex != 2 || event.Phase != RoutePhaseAfter ||
				event.Action != extensionmanifest.RouteActionAfter || event.RouteID != after.RouteID ||
				event.ContractVersion != after.ContractVersion || event.Method != "POST" ||
				event.RuntimeExecutionObserved != test.observed ||
				event.ActorID != 42 || event.ResponseStatus != http.StatusCreated ||
				event.CommitState != RouteCommitFinal || event.Artifact != after.Provider.Artifact {
				t.Fatalf("failure event=%#v", event)
			}
			records := traces.RouteTraces(0)
			if len(records) != 2 || records[0].Outcome != test.outcome || records[1].Outcome != RouteTraceCommitted {
				t.Fatalf("traces=%#v", records)
			}
		})
	}
}

func TestDispatcherDoesNotPreserveAfterFailureOutsideExplicitUnsafeBoundary(t *testing.T) {
	for _, test := range []struct {
		name          string
		method        string
		sink          RouteFailureSink
		wantCoreCalls int
		wantStepCalls int
	}{
		{name: "safe method", method: "GET", sink: &recordingRouteFailureSink{}, wantCoreCalls: 1, wantStepCalls: 1},
		{name: "missing sink", method: "POST"},
	} {
		t.Run(test.name, func(t *testing.T) {
			coreStep := dispatchCoreStep("core.route.closed")
			after := dispatchPluginStep(RoutePhaseAfter, "demo.route.closed_after", extensionmanifest.RouteActionAfter)
			stepCalls := 0
			coreCalls := 0
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: dispatchPlan(test.method, "/closed", nil, []RouteExecutionStep{coreStep, after}, 0)},
				Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
					stepCalls++
					return RouteInvocationResult{SideEffectStarted: true}, errors.New("after failed")
				}},
				Guard: &committedAfterGuard{}, Schemas: &committedAfterSchemas{}, Failures: test.sink,
			})
			_, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: test.method, Path: "/closed"},
				&dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
					coreCalls++
					return DispatchResponse{Status: http.StatusOK}, nil
				}})
			if !errors.Is(err, ErrDispatchTransport) {
				t.Fatalf("error=%v", err)
			}
			if coreCalls != test.wantCoreCalls || stepCalls != test.wantStepCalls {
				t.Fatalf("core calls=%d step calls=%d", coreCalls, stepCalls)
			}
		})
	}
}

func TestDispatcherCompletesAndReplaysUnsafeResponseAfterCommittedAfterFailure(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.idempotent_handler", extensionmanifest.RouteActionAdd)
	after := dispatchPluginStep(RoutePhaseAfter, "demo.route.idempotent_after", extensionmanifest.RouteActionAfter)
	plan := dispatchPlan("POST", "/idempotent", nil, []RouteExecutionStep{handler, after}, 0)
	lease := &dispatchIdempotencyLease{}
	controller := &dispatchIdempotencyController{lease: lease}
	sink := &recordingRouteFailureSink{}
	calls := 0
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan},
		Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
			calls++
			if input.Step.Phase == RoutePhaseAfter {
				return RouteInvocationResult{SideEffectStarted: true}, errors.New("after crashed")
			}
			response := DispatchResponse{Status: http.StatusCreated, Body: []byte(`{"id":42}`)}
			return RouteInvocationResult{Response: &response, SideEffectStarted: true, ResponseStarted: true}, nil
		}},
		Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{}, Failures: sink,
		Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{IdempotencyRequired: true}}, Idempotency: controller,
	})
	request := DispatchRequest{Method: "POST", Path: "/idempotent"}
	first, err := dispatcher.Dispatch(context.Background(), request, nil)
	if err != nil || first.Response.Status != http.StatusCreated || string(first.Response.Body) != `{"id":42}` ||
		lease.completeCalls != 1 || lease.abortCalls != 0 || calls != 2 || len(sink.events) != 1 {
		t.Fatalf("first=%#v lease=%#v calls=%d events=%#v err=%v", first, lease, calls, sink.events, err)
	}
	controller.replay = &RouteIdempotencyReplay{
		Response:      cloneDispatchResponse(lease.completed.Response),
		Authorization: cloneRouteReplayAuthorization(lease.completed.Authorization),
	}
	second, err := dispatcher.Dispatch(context.Background(), request, nil)
	if err != nil || !reflect.DeepEqual(second, first) || calls != 2 || len(sink.events) != 1 || controller.calls != 2 {
		t.Fatalf("second=%#v first=%#v calls=%d events=%#v controller=%#v err=%v", second, first, calls, sink.events, controller, err)
	}
}

type committedAfterGuard struct{ err error }

func (g *committedAfterGuard) Authorize(_ context.Context, _ RouteExecutionPlan, step RouteExecutionStep, _ DispatchRequest) error {
	if step.Phase == RoutePhaseAfter {
		return g.err
	}
	return nil
}

type committedAfterSchemas struct {
	requestErr    error
	responseErr   error
	responseCalls int
}

func (s *committedAfterSchemas) ValidateRequest(_ context.Context, step RouteExecutionStep, _ DispatchRequest) error {
	if step.Phase == RoutePhaseAfter {
		return s.requestErr
	}
	return nil
}

func (s *committedAfterSchemas) ValidateResponse(_ context.Context, step RouteExecutionStep, _ DispatchRequest, _ DispatchResponse) error {
	if step.Phase == RoutePhaseAfter {
		s.responseCalls++
		if s.responseCalls > 1 {
			return s.responseErr
		}
	}
	return nil
}

type recordingRouteFailureSink struct{ events []RouteCommittedAfterFailure }

func (s *recordingRouteFailureSink) RecordCommittedAfterFailure(_ context.Context, event RouteCommittedAfterFailure) {
	s.events = append(s.events, event)
}
