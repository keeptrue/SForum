package routes

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestDispatcherCompletesValidResponseAfterCallerCancellation(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		wantCalls int
	}{
		{name: "handler response validation", mode: "handler", wantCalls: 1},
		{name: "response guard", mode: "guard", wantCalls: 1},
		{name: "response request schema", mode: "request_schema", wantCalls: 1},
		{name: "response input schema", mode: "response_schema", wantCalls: 1},
		{name: "response plugin", mode: "plugin", wantCalls: 2},
		{name: "patched response schema", mode: "patched_schema", wantCalls: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.cancel_handler_"+test.mode, extensionmanifest.RouteActionAdd)
			after := dispatchPluginStep(RoutePhaseAfter, "demo.route.cancel_after_"+test.mode, extensionmanifest.RouteActionAfter)
			after.MutableResponseFields = []string{"/status"}
			plan := dispatchPlan(http.MethodPost, "/cancel-response-"+test.mode, nil, []RouteExecutionStep{handler, after}, 0)
			ctx, cancel := context.WithCancel(context.Background())
			lease := &dispatchIdempotencyLease{}
			controller := &dispatchIdempotencyController{lease: lease}
			guard := &responseCancellationGuard{mode: test.mode, cancel: cancel, routeID: after.RouteID}
			schemas := &responseCancellationSchemas{
				mode: test.mode, cancel: cancel, handlerID: handler.RouteID, afterID: after.RouteID,
				parent: ctx, responseCalls: make(map[string]int),
			}
			invoker := &responseCancellationInvoker{
				mode: test.mode, cancel: cancel, handlerID: handler.RouteID, afterID: after.RouteID,
			}
			sink := &recordingRouteFailureSink{}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: plan}, Steps: invoker, Guard: guard, Schemas: schemas,
				Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
					Idempotency: "required.24h@1", IdempotencyRequired: true,
				}},
				Idempotency: controller, Failures: sink, DefaultTimeout: 100 * time.Millisecond,
			})
			request := DispatchRequest{Method: http.MethodPost, Path: "/cancel-response-" + test.mode}

			first, err := dispatcher.Dispatch(ctx, request, nil)
			if err != nil || !first.Handled || first.Response.Status != http.StatusCreated ||
				string(first.Response.Body) != `{"created":true}` || ctx.Err() == nil ||
				invoker.calls != test.wantCalls || lease.completeCalls != 1 || lease.abortCalls != 0 ||
				lease.completed.Response.Status != http.StatusCreated || len(sink.events) != 0 ||
				schemas.detachedValidations == 0 || schemas.invalidFinalizationContext {
				t.Fatalf("first=%#v error=%v ctx=%v calls=%d lease=%#v incidents=%#v", first, err, ctx.Err(), invoker.calls, lease, sink.events)
			}

			controller.replay = &RouteIdempotencyReplay{
				Response:              cloneDispatchResponse(lease.completed.Response),
				ResponseContractKnown: lease.completed.ResponseContractKnown,
				ResponseContract:      cloneRouteReplayResponseContract(lease.completed.ResponseContract),
			}
			second, err := dispatcher.Dispatch(context.Background(), request, nil)
			if err != nil || !second.Handled || second.Response.Status != http.StatusCreated ||
				invoker.calls != test.wantCalls || controller.calls != 2 || lease.completeCalls != 1 ||
				lease.abortCalls != 0 || len(sink.events) != 0 {
				t.Fatalf("second=%#v error=%v calls=%d controller=%#v lease=%#v incidents=%#v", second, err, invoker.calls, controller, lease, sink.events)
			}
		})
	}
}

func TestDispatcherCancellationStillRevalidatesSchemaLessResponseMutation(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.cancel_schema_less_handler", extensionmanifest.RouteActionAdd)
	after := dispatchPluginStep(RoutePhaseAfter, "demo.route.cancel_schema_less_after", extensionmanifest.RouteActionAfter)
	after.ResponseSchema = ""
	after.MutableResponseFields = []string{"/status"}
	plan := dispatchPlan(http.MethodPost, "/cancel-schema-less", nil, []RouteExecutionStep{handler, after}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	lease := &dispatchIdempotencyLease{}
	controller := &dispatchIdempotencyController{lease: lease}
	schemas := &dispatcherReplayResponseSchemas{requiredStatus: http.StatusCreated}
	sink := &recordingRouteFailureSink{}
	calls := 0
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan},
		Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
			calls++
			if input.Stage == InvocationStageHandler {
				response := DispatchResponse{Status: http.StatusCreated, Body: []byte(`{"created":true}`)}
				return RouteInvocationResult{Response: &response, SideEffectStarted: true, ResponseStarted: true}, nil
			}
			cancel()
			return RouteInvocationResult{ResponsePatch: []RoutePatchOperation{{
				Kind: RoutePatchReplace, Path: "/status", Value: []byte(`202`),
			}}}, nil
		}},
		Guard: &dispatchGuard{}, Schemas: schemas, Failures: sink,
		Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
			Idempotency: "required.24h@1", IdempotencyRequired: true,
		}},
		Idempotency: controller, DefaultTimeout: 100 * time.Millisecond,
	})
	request := DispatchRequest{Method: http.MethodPost, Path: "/cancel-schema-less"}

	first, err := dispatcher.Dispatch(ctx, request, nil)
	if err != nil || !first.Handled || first.Response.Status != http.StatusCreated || ctx.Err() == nil ||
		calls != 2 || lease.completeCalls != 1 || lease.completed.Response.Status != http.StatusCreated ||
		len(sink.events) != 1 || sink.events[0].RouteID != after.RouteID ||
		sink.events[0].FailureCode != RouteFailureResponseSchemaRejected {
		t.Fatalf("first=%#v error=%v context=%v calls=%d lease=%#v incidents=%#v", first, err, ctx.Err(), calls, lease, sink.events)
	}

	controller.replay = &RouteIdempotencyReplay{
		Response:              cloneDispatchResponse(lease.completed.Response),
		ResponseContractKnown: lease.completed.ResponseContractKnown,
		ResponseContract:      cloneRouteReplayResponseContract(lease.completed.ResponseContract),
	}
	second, err := dispatcher.Dispatch(context.Background(), request, nil)
	if err != nil || !second.Handled || second.Response.Status != http.StatusCreated || calls != 2 ||
		controller.calls != 2 || lease.completeCalls != 1 || len(sink.events) != 1 {
		t.Fatalf("second=%#v error=%v calls=%d controller=%#v lease=%#v incidents=%#v", second, err, calls, controller, lease, sink.events)
	}
}

func TestDispatcherReplayCompletionOutlivesCallerCancellation(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.cancel_during_complete", extensionmanifest.RouteActionAdd)
	plan := dispatchPlan(http.MethodPost, "/cancel-during-complete", nil, []RouteExecutionStep{handler}, 0)
	lease := &responseCompletionBarrierLease{entered: make(chan struct{}), release: make(chan struct{})}
	controller := &dispatchIdempotencyController{lease: lease}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan},
		Steps: &dispatchStepInvoker{invoke: func(context.Context, RouteInvocation) (RouteInvocationResult, error) {
			response := DispatchResponse{Status: http.StatusCreated, Body: []byte(`{"created":true}`)}
			return RouteInvocationResult{Response: &response, SideEffectStarted: true, ResponseStarted: true}, nil
		}},
		Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
		Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
			Idempotency: "required.24h@1", IdempotencyRequired: true,
		}},
		Idempotency: controller, DefaultTimeout: 250 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	type dispatchOutcome struct {
		result DispatchResult
		err    error
	}
	outcome := make(chan dispatchOutcome, 1)
	go func() {
		result, err := dispatcher.Dispatch(ctx, DispatchRequest{
			Method: http.MethodPost, Path: "/cancel-during-complete",
		}, nil)
		outcome <- dispatchOutcome{result: result, err: err}
	}()

	select {
	case <-lease.entered:
	case <-time.After(time.Second):
		t.Fatal("replay completion did not start")
	}
	cancel()
	close(lease.release)
	select {
	case got := <-outcome:
		if got.err != nil || !got.result.Handled || got.result.Response.Status != http.StatusCreated ||
			lease.completeCalls != 1 || lease.abortCalls != 0 || lease.ctxErrBefore != nil ||
			lease.ctxErrAfter != nil || !lease.hasDeadline {
			t.Fatalf("outcome=%#v lease=%#v", got, lease)
		}
	case <-time.After(time.Second):
		t.Fatal("replay completion did not finish")
	}
}

func TestDispatcherDoesNotHideRuntimeOwnedResponseCancellation(t *testing.T) {
	for _, runtimeErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(runtimeErr.Error(), func(t *testing.T) {
			handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.runtime_cancel_handler", extensionmanifest.RouteActionAdd)
			after := dispatchPluginStep(RoutePhaseAfter, "demo.route.runtime_cancel_after", extensionmanifest.RouteActionAfter)
			plan := dispatchPlan(http.MethodPost, "/runtime-response-cancel", nil, []RouteExecutionStep{handler, after}, 0)
			lease := &dispatchIdempotencyLease{}
			sink := &recordingRouteFailureSink{}
			calls := 0
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: plan},
				Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
					calls++
					if input.Stage == InvocationStageHandler {
						response := DispatchResponse{Status: http.StatusCreated, Body: []byte(`{"created":true}`)}
						return RouteInvocationResult{Response: &response, SideEffectStarted: true, ResponseStarted: true}, nil
					}
					return RouteInvocationResult{SideEffectStarted: true}, runtimeErr
				}},
				Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{}, Failures: sink,
				Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
					Idempotency: "required.24h@1", IdempotencyRequired: true,
				}},
				Idempotency: &dispatchIdempotencyController{lease: lease},
			})

			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
				Method: http.MethodPost, Path: "/runtime-response-cancel",
			}, nil)
			if err != nil || !result.Handled || result.Response.Status != http.StatusCreated || calls != 2 ||
				lease.completeCalls != 1 || lease.abortCalls != 0 || len(sink.events) != 1 ||
				sink.events[0].FailureCode != RouteFailureTransportFailed {
				t.Fatalf("result=%#v error=%v calls=%d lease=%#v incidents=%#v", result, err, calls, lease, sink.events)
			}
		})
	}
}

func TestDispatcherRecordsRuntimeFailureDespiteConcurrentCallerCancellation(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.concurrent_cancel_handler", extensionmanifest.RouteActionAdd)
	after := dispatchPluginStep(RoutePhaseAfter, "demo.route.concurrent_cancel_after", extensionmanifest.RouteActionAfter)
	plan := dispatchPlan(http.MethodPost, "/concurrent-runtime-cancel", nil, []RouteExecutionStep{handler, after}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	sink := &responseCancellationFailureSink{}
	calls := 0
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan},
		Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
			calls++
			if input.Stage == InvocationStageHandler {
				response := DispatchResponse{Status: http.StatusCreated, Body: []byte(`{"created":true}`)}
				return RouteInvocationResult{Response: &response, SideEffectStarted: true, ResponseStarted: true}, nil
			}
			cancel()
			return RouteInvocationResult{SideEffectStarted: true}, errors.New("runtime crashed while caller disconnected")
		}},
		Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{}, Failures: sink,
		DefaultTimeout: 100 * time.Millisecond,
	})

	result, err := dispatcher.Dispatch(ctx, DispatchRequest{
		Method: http.MethodPost, Path: "/concurrent-runtime-cancel",
	}, nil)
	if err != nil || !result.Handled || result.Response.Status != http.StatusCreated || calls != 2 ||
		ctx.Err() == nil || len(sink.events) != 1 || sink.events[0].FailureCode != RouteFailureTransportFailed ||
		sink.ctxErr != nil || !sink.hasDeadline {
		t.Fatalf("result=%#v error=%v context=%v calls=%d sink=%#v", result, err, ctx.Err(), calls, sink)
	}
}

type responseCancellationGuard struct {
	mode    string
	cancel  context.CancelFunc
	routeID string
	done    bool
}

func (g *responseCancellationGuard) Authorize(
	ctx context.Context,
	_ RouteExecutionPlan,
	step RouteExecutionStep,
	_ DispatchRequest,
) error {
	if g != nil && g.mode == "guard" && step.RouteID == g.routeID && !g.done {
		g.done = true
		g.cancel()
		return ctx.Err()
	}
	return nil
}

type responseCancellationSchemas struct {
	mode                       string
	cancel                     context.CancelFunc
	parent                     context.Context
	handlerID                  string
	afterID                    string
	done                       bool
	responseCalls              map[string]int
	detachedValidations        int
	invalidFinalizationContext bool
}

func (s *responseCancellationSchemas) ValidateRequest(ctx context.Context, step RouteExecutionStep, _ DispatchRequest) error {
	if s.mode == "request_schema" && step.RouteID == s.afterID && !s.done {
		s.done = true
		s.cancel()
		return ctx.Err()
	}
	return nil
}

func (s *responseCancellationSchemas) ValidateResponse(
	ctx context.Context,
	step RouteExecutionStep,
	_ DispatchRequest,
	_ DispatchResponse,
) error {
	if s.parent != nil && s.parent.Err() != nil {
		s.detachedValidations++
		_, hasDeadline := ctx.Deadline()
		if ctx.Err() != nil || !hasDeadline {
			s.invalidFinalizationContext = true
		}
	}
	s.responseCalls[step.RouteID]++
	call := s.responseCalls[step.RouteID]
	shouldCancel := s.mode == "handler" && step.RouteID == s.handlerID && call == 1 ||
		s.mode == "response_schema" && step.RouteID == s.afterID && call == 1 ||
		s.mode == "patched_schema" && step.RouteID == s.afterID && call == 2
	if shouldCancel && !s.done {
		s.done = true
		s.cancel()
		return ctx.Err()
	}
	return nil
}

type responseCancellationFailureSink struct {
	events      []RouteCommittedAfterFailure
	ctxErr      error
	hasDeadline bool
}

func (s *responseCancellationFailureSink) RecordCommittedAfterFailure(
	ctx context.Context,
	event RouteCommittedAfterFailure,
) {
	s.ctxErr = ctx.Err()
	_, s.hasDeadline = ctx.Deadline()
	s.events = append(s.events, event)
}

type responseCancellationInvoker struct {
	mode      string
	cancel    context.CancelFunc
	handlerID string
	afterID   string
	done      bool
	calls     int
}

func (*responseCancellationInvoker) SupportsMode(string) bool { return true }

func (i *responseCancellationInvoker) Invoke(ctx context.Context, input RouteInvocation) (RouteInvocationResult, error) {
	i.calls++
	if input.Step.RouteID == i.handlerID && input.Stage == InvocationStageHandler {
		response := DispatchResponse{Status: http.StatusCreated, Body: []byte(`{"created":true}`)}
		return RouteInvocationResult{Response: &response, SideEffectStarted: true, ResponseStarted: true}, nil
	}
	if input.Step.RouteID != i.afterID || input.Stage != InvocationStageResponse {
		return RouteInvocationResult{}, errors.New("unexpected route invocation")
	}
	if i.mode == "plugin" && !i.done {
		i.done = true
		i.cancel()
		return RouteInvocationResult{SideEffectStarted: true}, ctx.Err()
	}
	if i.mode == "patched_schema" {
		return RouteInvocationResult{ResponsePatch: []RoutePatchOperation{{
			Kind: RoutePatchReplace, Path: "/status", Value: []byte(`202`),
		}}}, nil
	}
	return RouteInvocationResult{}, nil
}

type responseCompletionBarrierLease struct {
	entered       chan struct{}
	release       chan struct{}
	completeCalls int
	abortCalls    int
	ctxErrBefore  error
	ctxErrAfter   error
	hasDeadline   bool
}

func (l *responseCompletionBarrierLease) Complete(ctx context.Context, _ RouteIdempotencyCompletion) error {
	l.completeCalls++
	l.ctxErrBefore = ctx.Err()
	_, l.hasDeadline = ctx.Deadline()
	close(l.entered)
	select {
	case <-l.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	l.ctxErrAfter = ctx.Err()
	return l.ctxErrAfter
}

func (l *responseCompletionBarrierLease) Abort(context.Context) error {
	l.abortCalls++
	return nil
}
