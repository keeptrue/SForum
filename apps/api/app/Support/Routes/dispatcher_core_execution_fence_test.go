package routes

import (
	"context"
	"errors"
	"net/http"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestInvokeCoreWithCommitEvidenceDistinguishesDelivery(t *testing.T) {
	t.Run("cancelled before delivery stays pristine", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		observer := NewRouteCommitObserver()
		core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
			t.Fatal("cancelled request reached Core")
			return DispatchResponse{}, nil
		}}

		_, err := invokeCoreWithCommitEvidence(ctx, RouteExecutionStep{}, DispatchRequest{}, core, observer)
		if !errors.Is(err, context.Canceled) || core.calls != 0 || observer.ExecutionObserved() ||
			observer.State() != RouteCommitPristine {
			t.Fatalf("error=%v calls=%d state=%q observed=%t", err, core.calls, observer.State(), observer.ExecutionObserved())
		}
	})

	t.Run("error after delivery preserves side effect evidence", func(t *testing.T) {
		wantErr := errors.New("core write outcome unknown")
		observer := NewRouteCommitObserver()
		core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
			return DispatchResponse{}, wantErr
		}}

		_, err := invokeCoreWithCommitEvidence(context.Background(), RouteExecutionStep{}, DispatchRequest{}, core, observer)
		if !errors.Is(err, wantErr) || core.calls != 1 || !observer.ExecutionObserved() ||
			observer.State() != RouteCommitSideEffectStarted {
			t.Fatalf("error=%v calls=%d state=%q observed=%t", err, core.calls, observer.State(), observer.ExecutionObserved())
		}
	})

	t.Run("successful response advances response evidence", func(t *testing.T) {
		observer := NewRouteCommitObserver()
		core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
			return DispatchResponse{Status: http.StatusCreated}, nil
		}}

		response, err := invokeCoreWithCommitEvidence(
			context.Background(), RouteExecutionStep{}, DispatchRequest{}, core, observer,
		)
		if err != nil || response.Status != http.StatusCreated || core.calls != 1 || !observer.ExecutionObserved() ||
			observer.State() != RouteCommitResponseStarted {
			t.Fatalf("response=%#v error=%v calls=%d state=%q observed=%t", response, err, core.calls, observer.State(), observer.ExecutionObserved())
		}
	})
}

func TestDispatcherRequiredReplayFencesCoreAliasAndRewrite(t *testing.T) {
	for _, action := range []string{extensionmanifest.RouteActionAlias, extensionmanifest.RouteActionRewrite} {
		t.Run(action+" success replays without Core", func(t *testing.T) {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.core_"+action, action)
			step.TargetPath = "/core-target"
			plan := dispatchPlan(http.MethodPost, "/core-"+action, nil, []RouteExecutionStep{step}, 0)
			lease := &dispatchIdempotencyLease{}
			controller := &dispatchIdempotencyController{lease: lease}
			dispatcher := coreReplayTestDispatcher(plan, controller)
			core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
				return DispatchResponse{
					Status: http.StatusCreated, Headers: http.Header{"Content-Type": {"application/json"}},
					Body: []byte(`{"source":"core"}`),
				}, nil
			}}
			request := DispatchRequest{
				Method: http.MethodPost, Path: "/core-" + action,
				Headers: http.Header{"Idempotency-Key": {"core-" + action}},
			}

			first, err := dispatcher.Dispatch(context.Background(), request, core)
			if err != nil || !first.Handled || first.Response.Status != http.StatusCreated || core.calls != 1 ||
				lease.completeCalls != 1 || lease.abortCalls != 0 {
				t.Fatalf("first=%#v error=%v core=%d lease=%#v", first, err, core.calls, lease)
			}
			controller.replay = &RouteIdempotencyReplay{
				Response:              cloneDispatchResponse(lease.completed.Response),
				ResponseContractKnown: lease.completed.ResponseContractKnown,
				ResponseContract:      cloneRouteReplayResponseContract(lease.completed.ResponseContract),
			}
			second, err := dispatcher.Dispatch(context.Background(), request, core)
			if err != nil || !second.Handled || string(second.Response.Body) != string(first.Response.Body) ||
				core.calls != 1 || controller.calls != 2 || lease.completeCalls != 1 || lease.abortCalls != 0 {
				t.Fatalf("second=%#v error=%v core=%d controller=%#v lease=%#v", second, err, core.calls, controller, lease)
			}
		})

		t.Run(action+" unknown outcome remains pending", func(t *testing.T) {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.core_unknown_"+action, action)
			step.TargetPath = "/core-target"
			plan := dispatchPlan(http.MethodPost, "/core-unknown-"+action, nil, []RouteExecutionStep{step}, 0)
			lease := &dispatchIdempotencyLease{}
			controller := &dispatchIdempotencyController{lease: lease}
			dispatcher := coreReplayTestDispatcher(plan, controller)
			wantErr := errors.New("core outcome unknown")
			core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
				return DispatchResponse{}, wantErr
			}}
			request := DispatchRequest{Method: http.MethodPost, Path: "/core-unknown-" + action}

			if result, err := dispatcher.Dispatch(context.Background(), request, core); result.Handled || !errors.Is(err, wantErr) ||
				core.calls != 1 || lease.completeCalls != 0 || lease.abortCalls != 0 {
				t.Fatalf("result=%#v error=%v core=%d lease=%#v", result, err, core.calls, lease)
			}
			controller.err = ErrDispatchIdempotencyInProgress
			if result, err := dispatcher.Dispatch(context.Background(), request, core); result.Handled ||
				!errors.Is(err, ErrDispatchIdempotencyInProgress) || core.calls != 1 {
				t.Fatalf("retry result=%#v error=%v core=%d", result, err, core.calls)
			}
		})

		t.Run(action+" non-success response remains pending", func(t *testing.T) {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.core_non_success_"+action, action)
			step.TargetPath = "/core-target"
			plan := dispatchPlan(http.MethodPost, "/core-non-success-"+action, nil, []RouteExecutionStep{step}, 0)
			lease := &dispatchIdempotencyLease{}
			controller := &dispatchIdempotencyController{lease: lease}
			dispatcher := coreReplayTestDispatcher(plan, controller)
			core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
				return DispatchResponse{Status: http.StatusInternalServerError}, nil
			}}
			request := DispatchRequest{Method: http.MethodPost, Path: "/core-non-success-" + action}

			first, err := dispatcher.Dispatch(context.Background(), request, core)
			if err != nil || !first.Handled || first.Response.Status != http.StatusInternalServerError ||
				core.calls != 1 || lease.completeCalls != 0 || lease.abortCalls != 0 {
				t.Fatalf("first=%#v error=%v core=%d lease=%#v", first, err, core.calls, lease)
			}
			controller.err = ErrDispatchIdempotencyInProgress
			if result, retryErr := dispatcher.Dispatch(context.Background(), request, core); result.Handled ||
				!errors.Is(retryErr, ErrDispatchIdempotencyInProgress) || core.calls != 1 {
				t.Fatalf("retry result=%#v error=%v core=%d", result, retryErr, core.calls)
			}
		})

		t.Run(action+" pre-delivery cancellation aborts", func(t *testing.T) {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.core_cancel_"+action, action)
			step.TargetPath = "/core-target"
			plan := dispatchPlan(http.MethodPost, "/core-cancel-"+action, nil, []RouteExecutionStep{step}, 0)
			lease := &dispatchIdempotencyLease{}
			controller := &dispatchIdempotencyController{lease: lease}
			dispatcher := coreReplayTestDispatcher(plan, controller)
			core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
				t.Fatal("cancelled request reached Core")
				return DispatchResponse{}, nil
			}}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			if result, err := dispatcher.Dispatch(ctx, DispatchRequest{
				Method: http.MethodPost, Path: "/core-cancel-" + action,
			}, core); result.Handled || !errors.Is(err, context.Canceled) || core.calls != 0 ||
				lease.completeCalls != 0 || lease.abortCalls != 1 {
				t.Fatalf("result=%#v error=%v core=%d lease=%#v", result, err, core.calls, lease)
			}
		})

		t.Run(action+" cancellation after delivery remains pending", func(t *testing.T) {
			step := dispatchPluginStep(RoutePhaseHandler, "demo.route.core_delivered_cancel_"+action, action)
			step.TargetPath = "/core-target"
			plan := dispatchPlan(http.MethodPost, "/core-delivered-cancel-"+action, nil, []RouteExecutionStep{step}, 0)
			lease := &dispatchIdempotencyLease{}
			controller := &dispatchIdempotencyController{lease: lease}
			dispatcher := coreReplayTestDispatcher(plan, controller)
			ctx, cancel := context.WithCancel(context.Background())
			core := &dispatchCoreInvoker{invoke: func(ctx context.Context, _ RouteExecutionStep, _ DispatchRequest) (DispatchResponse, error) {
				cancel()
				return DispatchResponse{}, ctx.Err()
			}}

			if result, err := dispatcher.Dispatch(ctx, DispatchRequest{
				Method: http.MethodPost, Path: "/core-delivered-cancel-" + action,
			}, core); result.Handled || !errors.Is(err, context.Canceled) || core.calls != 1 ||
				lease.completeCalls != 0 || lease.abortCalls != 0 {
				t.Fatalf("result=%#v error=%v core=%d lease=%#v", result, err, core.calls, lease)
			}
			controller.err = ErrDispatchIdempotencyInProgress
			if result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
				Method: http.MethodPost, Path: "/core-delivered-cancel-" + action,
			}, core); result.Handled || !errors.Is(err, ErrDispatchIdempotencyInProgress) || core.calls != 1 {
				t.Fatalf("retry result=%#v error=%v core=%d", result, err, core.calls)
			}
		})
	}
}

func coreReplayTestDispatcher(plan RouteExecutionPlan, controller RouteIdempotencyController) *Dispatcher {
	return NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan}, Guard: &dispatchGuard{}, Schemas: &dispatchSchemas{},
		Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
			Idempotency: "required.24h@1", IdempotencyRequired: true,
		}},
		Idempotency: controller,
	})
}
