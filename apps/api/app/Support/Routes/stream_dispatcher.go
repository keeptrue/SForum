package routes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// RouteStreamDispatch is the Host-owned lifetime fence for one non-buffered
// terminal plugin route. Transport adapters report only observed milestones;
// they cannot reset commit state or manufacture fallback eligibility.
type RouteStreamDispatch struct {
	dispatcher *Dispatcher
	plan       RouteExecutionPlan
	index      int
	step       RouteExecutionStep
	request    DispatchRequest
	commit     *RouteCommitObserver
	started    time.Time

	mu              sync.Mutex
	opened          bool
	requestStarted  bool
	responseStarted bool
	responseStatus  int
	finished        bool
	failureRecorded bool
}

type RouteStreamPreparation struct {
	Handled  bool
	Dispatch *RouteStreamDispatch
}

type RouteStreamChunk struct {
	Sequence uint64
	Data     []byte
	Final    bool
}

type RouteStreamSession interface {
	Send([]byte, bool) error
	CloseRequest() error
	Recv() (RouteStreamChunk, error)
	Response() (DispatchResponse, bool)
	Cancel()
}

type RouteStreamStart struct {
	Response DispatchResponse
	Session  RouteStreamSession
}

type StreamingStepInvoker interface {
	OpenStream(context.Context, RouteInvocation) (RouteStreamStart, error)
}

// PrepareStream selects and authorizes a non-buffered plan without reading the
// request body. Composed stream middleware remains closed until its mutation
// and after-response failure semantics are frozen and implemented.
func (d *Dispatcher) PrepareStream(ctx context.Context, request DispatchRequest) (RouteStreamPreparation, error) {
	if d == nil || d.plans == nil || ctx == nil {
		return RouteStreamPreparation{}, ErrDispatchInvalid
	}
	request.Method = strings.ToUpper(strings.TrimSpace(request.Method))
	if request.Method == "" || strings.TrimSpace(request.Path) == "" {
		return RouteStreamPreparation{}, ErrDispatchInvalid
	}
	plan, err := d.plans.BuildExecutionPlan(ctx, request.Method, request.Path)
	if err != nil {
		if errors.Is(err, ErrRouteNotFound) {
			return RouteStreamPreparation{}, nil
		}
		return RouteStreamPreparation{}, err
	}
	if !plan.Valid() {
		return RouteStreamPreparation{}, ErrInvalidExecutionPlan
	}
	chain := plan.Chain()
	if !dispatchPlanHasPluginStep(chain) {
		return RouteStreamPreparation{}, nil
	}
	terminal := plan.Terminal()
	if terminal.Mode == extensionmanifest.RouteModeHTTP {
		return RouteStreamPreparation{}, nil
	}
	if len(chain) != 1 {
		return RouteStreamPreparation{}, fmt.Errorf("%w: composed stream chains are not available", ErrDispatchTransport)
	}
	if terminal.Provider.Kind != ProviderPlugin || terminal.Phase != RoutePhaseHandler ||
		terminal.Action != extensionmanifest.RouteActionAdd && terminal.Action != extensionmanifest.RouteActionReplace {
		return RouteStreamPreparation{}, fmt.Errorf("%w: stream terminal is not an exact plugin handler", ErrDispatchTransport)
	}
	policy, policyExists, policyErr := resolvePlanRouteExecutionPolicy(plan, terminal, d.policies)
	if policyErr != nil {
		return RouteStreamPreparation{}, fmt.Errorf("%w: %v", ErrDispatchIdempotencyUnavailable, policyErr)
	}
	if policyExists && policy.IdempotencyRequired {
		// Streaming, SSE, WebSocket, and multipart responses cannot be replayed
		// as one bounded response. Static validation rejects this combination;
		// keep the runtime boundary closed if publication ever drifts.
		return RouteStreamPreparation{}, ErrDispatchIdempotencyUnavailable
	}
	request.Params = plan.Params()
	request.Headers = cloneHTTPHeader(request.Headers)
	request.Body = nil
	request.Permissions = cloneDispatchPermissions(request.Permissions)
	return RouteStreamPreparation{Handled: true, Dispatch: &RouteStreamDispatch{
		dispatcher: d, plan: plan, step: terminal, request: request,
		commit: NewRouteCommitObserver(), started: time.Now(),
	}}, nil
}

func (d *RouteStreamDispatch) Step() RouteExecutionStep {
	if d == nil {
		return RouteExecutionStep{}
	}
	return cloneRouteExecutionSteps([]RouteExecutionStep{d.step})[0]
}

func (d *RouteStreamDispatch) Request() DispatchRequest {
	if d == nil {
		return DispatchRequest{}
	}
	return cloneDispatchRequest(d.request)
}

func (d *RouteStreamDispatch) Open(ctx context.Context) (RouteStreamStart, error) {
	if d == nil || d.dispatcher == nil || ctx == nil {
		return RouteStreamStart{}, ErrDispatchInvalid
	}
	d.mu.Lock()
	if d.opened || d.finished {
		d.mu.Unlock()
		return RouteStreamStart{}, ErrDispatchAlreadyCommitted
	}
	d.opened = true
	d.mu.Unlock()
	lifetime := newRouteStreamOpenLifetime(ctx, d.streamBudgetDuration())
	attached := false
	defer func() {
		if !attached {
			// Prefer the exact open-context cause (budget/caller) over a blank cancel.
			cause := context.Cause(lifetime.Context())
			if cause == nil {
				cause = context.Canceled
			}
			lifetime.close(cause)
		}
	}()
	authority, err := d.dispatcher.authorize(
		lifetime.Context(), d.plan, d.index, d.step, d.request, nil, InvocationStageHandler, d.commit,
	)
	if err != nil {
		return d.finishStreamOpenError(ctx, lifetime, err, true)
	}
	// Propagate an already-canceled caller into the Host open context before the
	// next stage so budget and caller causes stay distinguishable.
	if ctx.Err() != nil {
		lifetime.cancelFromCaller()
	}
	if err := lifetime.Context().Err(); err != nil {
		return d.finishStreamOpenError(ctx, lifetime, err, false)
	}
	invoker, ok := d.dispatcher.steps.(StreamingStepInvoker)
	if !ok {
		return d.finishStreamOpenError(
			ctx, lifetime,
			fmt.Errorf("%w: streaming step invoker is unavailable", ErrDispatchTransport), false,
		)
	}
	result, err := invoker.OpenStream(lifetime.Context(), RouteInvocation{
		PlanRevision: d.plan.Revision(), StepIndex: d.index, Step: d.step,
		Stage: InvocationStageHandler, Request: cloneDispatchRequest(d.request), Commit: d.commit,
		authority: authority,
	})
	if ctx.Err() != nil {
		lifetime.cancelFromCaller()
	}
	if err != nil {
		return d.finishStreamOpenError(ctx, lifetime, err, false)
	}
	d.setResponseStatus(result.Response.Status)
	if err := lifetime.Context().Err(); err != nil {
		if result.Session != nil {
			result.Session.Cancel()
		}
		return d.finishStreamOpenError(ctx, lifetime, err, false)
	}
	if result.Session == nil || !ValidTerminalResponseStatus(d.step.Mode, result.Response.Status) || len(result.Response.Body) != 0 {
		if result.Session != nil {
			result.Session.Cancel()
		}
		return d.finishStreamOpenErrorAs(
			ctx, lifetime, fmt.Errorf("%w: invalid streaming preflight", ErrDispatchTransport), false,
			RouteStreamFailureInvalidPreflight,
		)
	}
	if ctx.Err() != nil {
		lifetime.cancelFromCaller()
	}
	if err := lifetime.Context().Err(); err != nil {
		result.Session.Cancel()
		return d.finishStreamOpenError(ctx, lifetime, err, false)
	}
	result.Response.Headers = cloneHTTPHeader(result.Response.Headers)
	result.Session = bindRouteStreamLifetime(result.Session, lifetime)
	if result.Session == nil {
		return d.finishStreamOpenError(ctx, lifetime, ErrDispatchTransport, false)
	}
	attached = true
	return result, nil
}

func (d *RouteStreamDispatch) streamBudgetDuration() time.Duration {
	if d != nil && d.step.TimeoutMS > 0 {
		return time.Duration(d.step.TimeoutMS) * time.Millisecond
	}
	return routeStreamDefaultBudget
}

func (d *RouteStreamDispatch) finishStreamOpenError(
	caller context.Context,
	lifetime *routeStreamOpenLifetime,
	err error,
	guardFailure bool,
) (RouteStreamStart, error) {
	return d.finishStreamOpenErrorAs(
		caller, lifetime, err, guardFailure, RouteStreamFailureRuntimeTransport,
	)
}

func (d *RouteStreamDispatch) finishStreamOpenErrorAs(
	caller context.Context,
	lifetime *routeStreamOpenLifetime,
	err error,
	guardFailure bool,
	failureClass RouteStreamFailureClass,
) (RouteStreamStart, error) {
	if err == nil {
		err = ErrDispatchTransport
	}
	cause := context.Cause(lifetime.Context())
	callerErr := caller.Err()
	if errors.Is(err, ErrRouteStreamBudgetExceeded) ||
		errors.Is(cause, ErrRouteStreamBudgetExceeded) && routeStreamCancellationLike(err) {
		d.failStream(RouteStreamFailureHostBudget, true)
		return RouteStreamStart{}, fmt.Errorf("%w: %w", ErrDispatchTransport, ErrRouteStreamBudgetExceeded)
	}
	callerCause := context.Cause(caller)
	if callerCause == nil {
		callerCause = callerErr
	}
	callerCaused := callerErr != nil && routeStreamCallerCausedFailure(err, callerErr, callerCause)
	if callerCaused {
		if !d.commit.ExecutionObserved() {
			d.finishWithoutTrace()
			return RouteStreamStart{}, callerErr
		}
		d.failStream("", false)
		if !errors.Is(err, ErrDispatchTransport) {
			err = fmt.Errorf("%w: %w", ErrDispatchTransport, err)
		}
		return RouteStreamStart{}, err
	}
	if class, incident, classified := routeStreamFailureDisposition(err); classified {
		d.failStream(class, incident)
		if !errors.Is(err, ErrDispatchTransport) {
			err = fmt.Errorf("%w: %w", ErrDispatchTransport, err)
		}
		return RouteStreamStart{}, err
	}
	if guardFailure {
		d.finishWithoutTrace()
		outcome, _, _ := classifyRouteGuardFailure(err)
		d.dispatcher.appendTrace(d.plan, d.index, d.step, InvocationStageHandler, outcome, d.started, d.commit.State())
		return RouteStreamStart{}, err
	}
	d.failStream(failureClass, true)
	if !errors.Is(err, ErrDispatchTransport) {
		err = fmt.Errorf("%w: %w", ErrDispatchTransport, err)
	}
	return RouteStreamStart{}, err
}

func (d *RouteStreamDispatch) setResponseStatus(status int) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.responseStatus = status
	d.mu.Unlock()
}

func routeStreamCallerCausedFailure(err, callerErr, callerCause error) bool {
	return callerErr != nil && (errors.Is(err, callerErr) || callerCause != nil && errors.Is(err, callerCause))
}

func routeStreamCancellationLike(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (d *RouteStreamDispatch) finishWithoutTrace() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.finished = true
	d.mu.Unlock()
}

func (d *RouteStreamDispatch) RequestStarted() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished || d.requestStarted {
		return
	}
	d.requestStarted = true
	d.commit.SideEffectStarted()
}

func (d *RouteStreamDispatch) ResponseStarted() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished || d.responseStarted {
		return
	}
	d.responseStarted = true
	d.commit.ResponseStarted()
	d.dispatcher.appendTrace(d.plan, d.index, d.step, InvocationStageHandler, RouteTraceSucceeded, d.started, d.commit.State())
}

func (d *RouteStreamDispatch) StreamFailed(err error) error {
	if err == nil {
		return nil
	}
	if class, incident, classified := routeStreamFailureDisposition(err); classified {
		d.failStream(class, incident)
		return fmt.Errorf("%w: %w", ErrDispatchTransport, err)
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) &&
		!errors.Is(err, ErrRouteStreamBudgetExceeded) {
		d.failStream("", false)
		return fmt.Errorf("%w: %w", ErrDispatchTransport, err)
	}
	class := RouteStreamFailureRuntimeTransport
	if errors.Is(err, ErrRouteStreamBudgetExceeded) {
		class = RouteStreamFailureHostBudget
	}
	d.failStream(class, true)
	return fmt.Errorf("%w: %w", ErrDispatchTransport, err)
}

func (d *RouteStreamDispatch) StreamFailedAs(class RouteStreamFailureClass, err error) error {
	if err == nil {
		return nil
	}
	return d.StreamFailed(WithRouteStreamIncident(err, class))
}

// StreamAborted publishes the transport trace without attributing a plugin
// incident. Caller disconnects, Host writer failures, and lifecycle ForceDrain
// use this path.
func (d *RouteStreamDispatch) StreamAborted(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return d.StreamFailed(WithRouteStreamAbort(err))
}

func (d *RouteStreamDispatch) Fail() {
	d.failStream("", false)
}

func (d *RouteStreamDispatch) failStream(class RouteStreamFailureClass, record bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.finished {
		d.mu.Unlock()
		return
	}
	d.finished = true
	commitState := d.commit.State()
	responseStatus := d.responseStatus
	shouldRecord := record && !d.failureRecorded && d.dispatcher.streamFailures != nil &&
		d.commit.ExecutionObserved() && ValidRouteStreamFailureClass(class)
	if shouldRecord {
		d.failureRecorded = true
	}
	d.mu.Unlock()

	d.dispatcher.appendTrace(
		d.plan, d.index, d.step, InvocationStageHandler,
		RouteTraceTransportFailed, d.started, commitState,
	)
	if !shouldRecord {
		return
	}
	event := RouteStreamFailure{
		Revision: d.plan.Revision(), StepIndex: d.index, Phase: d.step.Phase,
		InvocationStage: InvocationStageHandler, Action: d.step.Action, Mode: d.step.Mode,
		RouteID: d.step.RouteID, ContractVersion: d.step.ContractVersion,
		Method: d.plan.Method(), PathSignature: routeStepPathSignature(d.step),
		FailureCode: RouteFailureTransportFailed, CauseClass: class,
		RuntimeExecutionObserved: true, ActorID: d.request.ActorID,
		ResponseStatus: responseStatus, CommitState: commitState, Artifact: d.step.Provider.Artifact,
	}
	if ValidRouteStreamFailure(event) {
		d.dispatcher.streamFailures.RecordStreamFailure(context.Background(), event)
	}
}

func (d *RouteStreamDispatch) Complete() error {
	if d == nil {
		return ErrDispatchInvalid
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished || !d.responseStarted || !d.commit.Finalize() {
		return ErrDispatchAlreadyCommitted
	}
	d.finished = true
	d.dispatcher.appendTrace(d.plan, d.index, d.step, InvocationStageHandler, RouteTraceCommitted, d.started, d.commit.State())
	return nil
}
