package routes

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestDispatcherTraceOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		guard      GuardAuthorizer
		schemas    SchemaValidator
		invoke     func(context.Context, RouteInvocation) (RouteInvocationResult, error)
		want       RouteTraceOutcome
		wantCommit RouteExecutionCommitState
	}{
		{
			name: "denied", guard: &dispatchGuard{err: errors.New("secret denial detail")}, schemas: &traceSchemas{},
			want: RouteTraceDenied, wantCommit: RouteCommitPristine,
		},
		{
			name: "request schema rejected", guard: &dispatchGuard{},
			schemas: &traceSchemas{requestErr: errors.New("secret request body detail")},
			want:    RouteTraceSchemaRejected, wantCommit: RouteCommitPristine,
		},
		{
			name: "response schema rejected", guard: &dispatchGuard{},
			schemas: &traceSchemas{responseErr: errors.New("secret response body detail")},
			invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
				response := DispatchResponse{Status: http.StatusOK, Body: []byte("private response")}
				return RouteInvocationResult{Response: &response}, nil
			},
			want: RouteTraceSchemaRejected, wantCommit: RouteCommitPristine,
		},
		{
			name: "transport failed", guard: &dispatchGuard{}, schemas: &traceSchemas{},
			invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
				return RouteInvocationResult{SideEffectStarted: true}, errors.New("secret transport detail")
			},
			want: RouteTraceTransportFailed, wantCommit: RouteCommitSideEffectStarted,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step := dispatchTraceStep(RoutePhaseHandler, "trace.route.failure")
			ring := NewRouteTraceRing(8)
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: dispatchPlan("POST", "/trace", nil, []RouteExecutionStep{step}, 0)},
				Steps: &dispatchStepInvoker{invoke: test.invoke}, Guard: test.guard, Schemas: test.schemas, Trace: ring,
			})
			_, _ = dispatcher.Dispatch(context.Background(), DispatchRequest{
				Method: "POST", Path: "/trace", Query: "token=private", Body: []byte("private request"),
			}, nil)

			records := ring.RouteTraces(0)
			if len(records) != 1 || records[0].Outcome != test.want || records[0].CommitState != test.wantCommit {
				t.Fatalf("records=%#v", records)
			}
			assertExactTraceMetadata(t, records[0], step, "POST")
		})
	}
}

func TestDispatcherTracesFallbackAndFinalCommit(t *testing.T) {
	step := dispatchTraceStep(RoutePhaseHandler, "trace.route.fallback")
	step.Fallback = "readonly_core"
	ring := NewRouteTraceRing(8)
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan("GET", "/trace", nil, []RouteExecutionStep{step}, 0)},
		Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
			return RouteInvocationResult{}, errors.New("private upstream failure")
		}},
		Guard: &dispatchGuard{}, Schemas: &traceSchemas{}, Trace: ring,
	})
	core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		return DispatchResponse{Status: http.StatusOK, Body: []byte("core")}, nil
	}}
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/trace"}, core)
	if err != nil || !result.Handled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	records := ring.RouteTraces(0)
	want := []RouteTraceOutcome{RouteTraceTransportFailed, RouteTraceFallbackUsed, RouteTraceCommitted}
	if len(records) != len(want) {
		t.Fatalf("records=%#v", records)
	}
	for index, outcome := range want {
		if records[index].Outcome != outcome {
			t.Fatalf("record[%d]=%#v", index, records[index])
		}
		assertExactTraceMetadata(t, records[index], step, "GET")
	}
	if records[2].CommitState != RouteCommitFinal {
		t.Fatalf("commit trace=%#v", records[2])
	}
}

func TestDispatcherTracesNonHandlerReadonlyFallbackAndFinalCommit(t *testing.T) {
	plugin := dispatchTraceStep(RoutePhaseBefore, "trace.route.before-fallback")
	plugin.Fallback = "readonly_core"
	coreStep := dispatchCoreStep("core.route.trace")
	coreStep.Path = "/trace"
	coreStep.Method = "GET"
	ring := NewRouteTraceRing(8)
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan("GET", "/trace", nil, []RouteExecutionStep{plugin, coreStep}, 1)},
		Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
			return RouteInvocationResult{}, errors.New("private upstream failure")
		}},
		Guard: &dispatchGuard{}, Schemas: &traceSchemas{}, Trace: ring,
	})
	core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		return DispatchResponse{Status: http.StatusOK, Body: []byte("core")}, nil
	}}
	if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/trace"}, core); err != nil {
		t.Fatal(err)
	}
	records := ring.RouteTraces(0)
	want := []RouteTraceOutcome{RouteTraceTransportFailed, RouteTraceFallbackUsed, RouteTraceCommitted}
	if len(records) != len(want) {
		t.Fatalf("records=%#v", records)
	}
	for index, outcome := range want {
		if records[index].Outcome != outcome || records[index].StepIndex != 0 {
			t.Fatalf("record[%d]=%#v", index, records[index])
		}
	}
	if records[2].CommitState != RouteCommitFinal {
		t.Fatalf("commit trace=%#v", records[2])
	}
}

func TestDispatcherAttributesCommitToLastPluginContributor(t *testing.T) {
	plugin := dispatchTraceStep(RoutePhaseBefore, "trace.route.contributor")
	coreStep := dispatchCoreStep("core.route.trace")
	coreStep.Path = "/trace"
	coreStep.Method = "GET"
	ring := NewRouteTraceRing(8)
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan("GET", "/trace", nil, []RouteExecutionStep{plugin, coreStep}, 1)},
		Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
			time.Sleep(time.Millisecond)
			return RouteInvocationResult{}, nil
		}},
		Guard: &dispatchGuard{}, Schemas: &traceSchemas{}, Trace: ring,
	})
	core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		return DispatchResponse{Status: http.StatusOK}, nil
	}}
	if _, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/trace"}, core); err != nil {
		t.Fatal(err)
	}
	records := ring.RouteTraces(0)
	if len(records) != 2 || records[0].Outcome != RouteTraceSucceeded || records[1].Outcome != RouteTraceCommitted {
		t.Fatalf("records=%#v", records)
	}
	for _, record := range records {
		if record.StepIndex != 0 || record.RouteID != plugin.RouteID || record.Provider.Kind != ProviderPlugin {
			t.Fatalf("commit attribution=%#v", record)
		}
	}
	if records[0].DurationMicros <= 0 || records[1].DurationMicros < records[0].DurationMicros || records[1].CommitState != RouteCommitFinal {
		t.Fatalf("durations/commit=%#v", records)
	}
}

func TestDispatcherCoreOnlyPlanProducesNoTrace(t *testing.T) {
	ring := NewRouteTraceRing(8)
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan(
			"GET", "/trace", nil, []RouteExecutionStep{dispatchCoreStep("core.route.trace")}, 0,
		)},
		Trace: ring,
	})
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Method: "GET", Path: "/trace"}, nil)
	if err != nil || result.Handled || len(ring.RouteTraces(0)) != 0 {
		t.Fatalf("result=%#v traces=%#v err=%v", result, ring.RouteTraces(0), err)
	}
}

type traceSchemas struct {
	requestErr  error
	responseErr error
}

func (s *traceSchemas) ValidateRequest(context.Context, RouteExecutionStep, DispatchRequest) error {
	return s.requestErr
}

func (s *traceSchemas) ValidateResponse(context.Context, RouteExecutionStep, DispatchRequest, DispatchResponse) error {
	return s.responseErr
}

func dispatchTraceStep(phase RouteExecutionPhase, id string) RouteExecutionStep {
	action := extensionmanifest.RouteActionAdd
	switch phase {
	case RoutePhaseGlobal:
		action = extensionmanifest.RouteActionGlobalMiddleware
	case RoutePhaseBefore:
		action = extensionmanifest.RouteActionBefore
	case RoutePhaseFilter:
		action = extensionmanifest.RouteActionFilter
	case RoutePhaseWrap:
		action = extensionmanifest.RouteActionWrap
	case RoutePhaseAfter:
		action = extensionmanifest.RouteActionAfter
	}
	step := dispatchPluginStep(phase, id, action)
	step.Path = "/trace"
	step.Method = "GET"
	return step
}

func assertExactTraceMetadata(t *testing.T, record RouteTraceRecord, step RouteExecutionStep, method string) {
	t.Helper()
	if record.Revision != 1 || record.RouteID != step.RouteID || record.ContractVersion != step.ContractVersion ||
		record.Method != method || record.PathSignature != routeStepPathSignature(step) || record.Mode != step.Mode ||
		record.Fallback != step.Fallback || record.DurationMicros < 0 || record.Provider.Kind != ProviderPlugin ||
		record.Provider.Artifact == nil || *record.Provider.Artifact != step.Provider.Artifact {
		t.Fatalf("trace metadata=%#v", record)
	}
}
