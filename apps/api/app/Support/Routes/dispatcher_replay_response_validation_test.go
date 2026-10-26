package routes

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestDispatcherReplayRevalidatesCurrentResponseContracts(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.replay_response_handler", extensionmanifest.RouteActionAdd)
	handler.ResponseSchema = "demo.route.replay_response_handler.response@1"
	after := dispatchPluginStep(RoutePhaseAfter, "demo.route.replay_response_after", extensionmanifest.RouteActionAfter)
	after.ResponseSchema = "demo.route.replay_response_after.response@1"
	plan := dispatchPlan("POST", "/replay-response", nil, []RouteExecutionStep{handler, after}, 0)
	stored := DispatchResponse{
		Status: http.StatusCreated,
		Headers: http.Header{
			"Content-Type":         {"application/json"},
			"Idempotency-Replayed": {"true"},
		},
		Body: []byte(`{"created":true}`),
	}

	for _, test := range []struct {
		name          string
		schemas       *dispatcherReplayResponseSchemas
		wantErr       bool
		wantValidated []string
	}{
		{name: "current final contract accepts", schemas: &dispatcherReplayResponseSchemas{}, wantValidated: []string{after.RouteID}},
		{name: "intermediate contract does not own final payload", schemas: &dispatcherReplayResponseSchemas{rejectRouteID: handler.RouteID}, wantValidated: []string{after.RouteID}},
		{name: "current final response contract drift", schemas: &dispatcherReplayResponseSchemas{rejectRouteID: after.RouteID}, wantErr: true, wantValidated: []string{after.RouteID}},
		{name: "response validator unavailable", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			invoker := &dispatcherReplayInvoker{fail: func() { t.Fatal("replay invoked plugin") }}
			controller := &dispatchIdempotencyController{replay: &RouteIdempotencyReplay{Response: stored}}
			var schemas SchemaValidator
			if test.schemas != nil {
				schemas = test.schemas
			}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: plan}, Steps: invoker,
				Guard: &dispatcherReplayCaptureGuard{}, Schemas: schemas,
				Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
					Idempotency: "required.24h@1", IdempotencyRequired: true,
				}},
				Idempotency: controller, Failures: dispatcherReplayFailureSink{},
			})

			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
				Method: "POST", Path: "/replay-response",
			}, nil)
			if test.wantErr {
				if result.Handled || !errors.Is(err, ErrDispatchIdempotencyUnavailable) {
					t.Fatalf("result=%#v error=%v", result, err)
				}
			} else if err != nil || !result.Handled || !reflect.DeepEqual(result.Response, stored) {
				t.Fatalf("result=%#v error=%v want=%#v", result, err, stored)
			}
			if controller.calls != 1 || invoker.calls != 0 {
				t.Fatalf("controller calls=%d plugin calls=%d", controller.calls, invoker.calls)
			}
			if test.schemas != nil && !reflect.DeepEqual(test.schemas.validated, test.wantValidated) {
				t.Fatalf("validated=%v want=%v", test.schemas.validated, test.wantValidated)
			}
		})
	}
}

func TestDispatcherFirstExecutionAndReplayUseSameFinalResponseContract(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.final_contract_handler", extensionmanifest.RouteActionAdd)
	handler.ResponseSchema = "demo.route.final_contract_handler.response@1"
	after := dispatchPluginStep(RoutePhaseAfter, "demo.route.final_contract_after", extensionmanifest.RouteActionAfter)
	after.ResponseSchema = ""
	after.MutableResponseFields = []string{"/status"}
	plan := dispatchPlan("POST", "/final-contract", nil, []RouteExecutionStep{handler, after}, 0)
	lease := &dispatchIdempotencyLease{}
	controller := &dispatchIdempotencyController{lease: lease}
	schemas := &dispatcherReplayResponseSchemas{requiredStatus: http.StatusCreated}
	sink := &recordingRouteFailureSink{}
	pluginCalls := 0
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan},
		Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
			pluginCalls++
			if input.Stage == InvocationStageHandler {
				response := DispatchResponse{
					Status:  http.StatusCreated,
					Headers: http.Header{"Content-Type": {"application/json"}},
					Body:    []byte(`{"created":true}`),
				}
				return RouteInvocationResult{Response: &response, SideEffectStarted: true, ResponseStarted: true}, nil
			}
			return RouteInvocationResult{ResponsePatch: []RoutePatchOperation{{
				Kind: RoutePatchReplace, Path: "/status", Value: []byte(`202`),
			}}}, nil
		}},
		Guard: &dispatchGuard{}, Schemas: schemas, Failures: sink,
		Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
			Idempotency: "required.24h@1", IdempotencyRequired: true,
		}},
		Idempotency: controller,
	})
	request := DispatchRequest{Method: "POST", Path: "/final-contract"}

	first, err := dispatcher.Dispatch(context.Background(), request, nil)
	if err != nil || !first.Handled || first.Response.Status != http.StatusCreated ||
		lease.completeCalls != 1 || lease.completed.Response.Status != http.StatusCreated ||
		pluginCalls != 2 || len(sink.events) != 1 {
		t.Fatalf("first=%#v error=%v lease=%#v plugin calls=%d events=%#v", first, err, lease, pluginCalls, sink.events)
	}
	if event := sink.events[0]; event.RouteID != after.RouteID ||
		event.FailureCode != RouteFailureResponseSchemaRejected || event.ResponseStatus != http.StatusCreated {
		t.Fatalf("failure event=%#v", event)
	}

	controller.replay = &RouteIdempotencyReplay{
		Response:              cloneDispatchResponse(lease.completed.Response),
		ResponseContractKnown: lease.completed.ResponseContractKnown,
		ResponseContract:      cloneRouteReplayResponseContract(lease.completed.ResponseContract),
	}
	second, err := dispatcher.Dispatch(context.Background(), request, nil)
	if err != nil || !second.Handled || !reflect.DeepEqual(second.Response, first.Response) ||
		pluginCalls != 2 || controller.calls != 2 || len(sink.events) != 1 {
		t.Fatalf("second=%#v first=%#v error=%v plugin calls=%d controller=%#v events=%#v", second, first, err, pluginCalls, controller, sink.events)
	}
}

func TestDispatcherReplayUsesPersistedEffectiveContractAfterLaterPreflightRejection(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.effective_contract_handler", extensionmanifest.RouteActionAdd)
	after := dispatchPluginStep(RoutePhaseAfter, "demo.route.effective_contract_after", extensionmanifest.RouteActionAfter)
	plan := dispatchPlan("POST", "/effective-contract", nil, []RouteExecutionStep{handler, after}, 0)
	lease := &dispatchIdempotencyLease{}
	controller := &dispatchIdempotencyController{lease: lease}
	schemas := &dispatcherReplayResponseSchemas{rejectRouteID: after.RouteID}
	sink := &recordingRouteFailureSink{}
	pluginCalls := 0
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan},
		Steps: &dispatchStepInvoker{invoke: func(_ context.Context, input RouteInvocation) (RouteInvocationResult, error) {
			pluginCalls++
			if input.Stage != InvocationStageHandler {
				t.Fatal("response modifier ran after its input contract was rejected")
			}
			response := DispatchResponse{
				Status: http.StatusCreated, Headers: http.Header{"Content-Type": {"application/json"}},
				Body: []byte(`{"created":true}`),
			}
			return RouteInvocationResult{Response: &response, SideEffectStarted: true, ResponseStarted: true}, nil
		}},
		Guard: &dispatchGuard{}, Schemas: schemas, Failures: sink,
		Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
			Idempotency: "required.24h@1", IdempotencyRequired: true,
		}},
		Idempotency: controller,
	})
	request := DispatchRequest{Method: "POST", Path: "/effective-contract"}

	first, err := dispatcher.Dispatch(context.Background(), request, nil)
	if err != nil || !first.Handled || first.Response.Status != http.StatusCreated ||
		pluginCalls != 1 || lease.completeCalls != 1 || len(sink.events) != 1 {
		t.Fatalf("first=%#v error=%v plugin calls=%d lease=%#v events=%#v", first, err, pluginCalls, lease, sink.events)
	}
	contract := lease.completed.ResponseContract
	if !lease.completed.ResponseContractKnown || contract == nil ||
		contract.StepIndex != 0 || contract.InvocationStage != InvocationStageHandler ||
		contract.RouteID != handler.RouteID || contract.ResponseSchema != handler.ResponseSchema {
		t.Fatalf("effective response contract=%#v known=%t", contract, lease.completed.ResponseContractKnown)
	}

	controller.replay = &RouteIdempotencyReplay{
		Response:              cloneDispatchResponse(lease.completed.Response),
		ResponseContractKnown: true,
		ResponseContract:      cloneRouteReplayResponseContract(contract),
	}
	second, err := dispatcher.Dispatch(context.Background(), request, nil)
	if err != nil || !second.Handled || !reflect.DeepEqual(second.Response, first.Response) ||
		pluginCalls != 1 || controller.calls != 2 || len(sink.events) != 1 {
		t.Fatalf("second=%#v first=%#v error=%v calls=%d controller=%#v events=%#v", second, first, err, pluginCalls, controller, sink.events)
	}

	controller.replay.ResponseContract = newRouteReplayResponseContract(
		routeInvocationExecution{index: 1, stage: InvocationStageResponse}, after,
	)
	if result, replayErr := dispatcher.Dispatch(context.Background(), request, nil); result.Handled ||
		!errors.Is(replayErr, ErrDispatchIdempotencyUnavailable) || pluginCalls != 1 {
		t.Fatalf("forged provenance result=%#v error=%v plugin calls=%d", result, replayErr, pluginCalls)
	}
}

func TestDispatcherReplayUsesHandlerContractWithoutResponseStageOwner(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.replay_handler_contract", extensionmanifest.RouteActionAdd)
	after := dispatchPluginStep(RoutePhaseAfter, "demo.route.replay_without_contract", extensionmanifest.RouteActionAfter)
	after.ResponseSchema = ""
	plan := dispatchPlan("POST", "/replay-handler-contract", nil, []RouteExecutionStep{handler, after}, 0)
	schemas := &dispatcherReplayResponseSchemas{}
	invoker := &dispatcherReplayInvoker{fail: func() { t.Fatal("replay invoked plugin") }}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan}, Steps: invoker,
		Guard: &dispatcherReplayCaptureGuard{}, Schemas: schemas,
		Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
			Idempotency: "required.24h@1", IdempotencyRequired: true,
		}},
		Idempotency: &dispatchIdempotencyController{replay: &RouteIdempotencyReplay{Response: DispatchResponse{
			Status: http.StatusCreated, Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"created":true}`),
		}}},
		Failures: dispatcherReplayFailureSink{},
	})

	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
		Method: "POST", Path: "/replay-handler-contract",
	}, nil)
	if err != nil || !result.Handled || invoker.calls != 0 || !reflect.DeepEqual(schemas.validated, []string{handler.RouteID}) {
		t.Fatalf("result=%#v error=%v plugin calls=%d validated=%v", result, err, invoker.calls, schemas.validated)
	}
}

func TestDispatcherReplayRequiresStoredSuccessTerminalBeforeAuthority(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.replay_status", extensionmanifest.RouteActionAdd)
	plan := dispatchPlan("POST", "/replay-status", nil, []RouteExecutionStep{handler}, 0)
	for _, status := range []int{0, 100, 101, 199, 200, 204, 299, 300, 404, 500, 600} {
		t.Run("status_"+strconv.Itoa(status), func(t *testing.T) {
			guard := &dispatcherReplayCaptureGuard{}
			schemas := &dispatcherReplayResponseSchemas{}
			invoker := &dispatcherReplayInvoker{fail: func() { t.Fatal("status replay invoked plugin") }}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: plan}, Steps: invoker, Guard: guard, Schemas: schemas,
				Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
					Idempotency: "required.24h@1", IdempotencyRequired: true,
				}},
				Idempotency: &dispatchIdempotencyController{replay: &RouteIdempotencyReplay{Response: DispatchResponse{
					Status: status,
				}}},
			})

			result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
				Method: "POST", Path: "/replay-status",
			}, nil)
			valid := status >= http.StatusOK && status < http.StatusMultipleChoices
			if valid {
				if err != nil || !result.Handled || result.Response.Status != status || len(guard.observations) != 1 {
					t.Fatalf("result=%#v error=%v guards=%d", result, err, len(guard.observations))
				}
			} else if result.Handled || !errors.Is(err, ErrDispatchIdempotencyUnavailable) || len(guard.observations) != 0 {
				t.Fatalf("result=%#v error=%v guards=%d", result, err, len(guard.observations))
			}
			if invoker.calls != 0 {
				t.Fatalf("plugin calls=%d", invoker.calls)
			}
		})
	}
}

type dispatcherReplayResponseSchemas struct {
	rejectRouteID  string
	requiredStatus int
	validated      []string
}

func (*dispatcherReplayResponseSchemas) ValidateRequest(context.Context, RouteExecutionStep, DispatchRequest) error {
	return nil
}

func (s *dispatcherReplayResponseSchemas) ValidateResponse(
	_ context.Context,
	step RouteExecutionStep,
	_ DispatchRequest,
	response DispatchResponse,
) error {
	s.validated = append(s.validated, step.RouteID)
	if response.Headers.Get("Idempotency-Replayed") != "" {
		return errors.New("Host replay evidence reached response validation")
	}
	if step.RouteID == s.rejectRouteID {
		return errors.New("current response contract rejected stored replay")
	}
	if s.requiredStatus != 0 && response.Status != s.requiredStatus {
		return errors.New("current response contract rejected status")
	}
	return nil
}
