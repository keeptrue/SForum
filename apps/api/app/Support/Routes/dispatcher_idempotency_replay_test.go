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

func TestDispatcherRequiredMutableReplayRecordsAndReplaysProgressiveRequest(t *testing.T) {
	fixture := newDispatcherMutableReplayFixture(t)
	lease := &dispatchIdempotencyLease{}
	guard := &dispatcherReplayCaptureGuard{}
	schemas := &dispatcherReplayCaptureSchemas{}
	invoker := &dispatcherReplayInvoker{
		requestPatches: map[string][]RoutePatchOperation{
			fixture.beforeID: fixture.beforePatch,
			fixture.filterID: fixture.filterPatch,
			fixture.wrapID:   nil,
		},
	}
	core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		t.Fatal("first execution invoked core")
		return DispatchResponse{}, nil
	}}
	dispatcher := newDispatcherMutableReplayHarness(
		fixture.plan, invoker, guard, schemas,
		&dispatchIdempotencyController{lease: lease, mutationReplayAvailable: true},
	)

	first, err := dispatcher.Dispatch(context.Background(), fixture.request, core)
	if err != nil || !first.Handled || first.Response.Status != http.StatusCreated {
		t.Fatalf("first result=%#v error=%v", first, err)
	}
	if invoker.calls != 6 || core.calls != 0 || lease.completeCalls != 1 || lease.abortCalls != 0 {
		t.Fatalf("first invoker=%d core=%d lease=%#v", invoker.calls, core.calls, lease)
	}

	expected := fixture.expectedRequests(t)
	assertDispatcherReplayObservations(t, "first guards", guard.observations, fixture.guardObservations(expected))
	assertDispatcherReplayObservations(t, "first schemas", schemas.observations, fixture.schemaObservations(expected))
	assertDispatcherReplayTranscript(t, lease.completed.Authorization, fixture, expected)

	replayGuard := &dispatcherReplayCaptureGuard{}
	replaySchemas := &dispatcherReplayCaptureSchemas{}
	replayInvoker := &dispatcherReplayInvoker{fail: func() { t.Fatal("replay invoked plugin") }}
	replayCore := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		t.Fatal("replay invoked core")
		return DispatchResponse{}, nil
	}}
	replayController := &dispatchIdempotencyController{
		mutationReplayAvailable: true,
		replay: &RouteIdempotencyReplay{
			Response:      cloneDispatchResponse(lease.completed.Response),
			Authorization: cloneRouteReplayAuthorization(lease.completed.Authorization),
		},
	}
	replayDispatcher := newDispatcherMutableReplayHarness(
		fixture.plan, replayInvoker, replayGuard, replaySchemas, replayController,
	)

	replayed, err := replayDispatcher.Dispatch(context.Background(), fixture.request, replayCore)
	if err != nil || !replayed.Handled || !reflect.DeepEqual(replayed.Response, first.Response) {
		t.Fatalf("replay result=%#v first=%#v error=%v", replayed, first, err)
	}
	if replayInvoker.calls != 0 || replayCore.calls != 0 || replayController.calls != 1 {
		t.Fatalf("replay invoker=%d core=%d begin=%d", replayInvoker.calls, replayCore.calls, replayController.calls)
	}
	assertDispatcherReplayObservations(t, "replay guards", replayGuard.observations, fixture.guardObservations(expected))
	assertDispatcherReplayObservations(t, "replay schemas", replaySchemas.observations, fixture.schemaObservations(expected))
}

func TestDispatcherRequiredMutableReplayRejectsTranscriptAndSchemaDrift(t *testing.T) {
	fixture := newDispatcherMutableReplayFixture(t)
	lease := &dispatchIdempotencyLease{}
	first := newDispatcherMutableReplayHarness(
		fixture.plan,
		&dispatcherReplayInvoker{requestPatches: map[string][]RoutePatchOperation{
			fixture.beforeID: fixture.beforePatch,
			fixture.filterID: fixture.filterPatch,
			fixture.wrapID:   nil,
		}},
		&dispatcherReplayCaptureGuard{},
		&dispatcherReplayCaptureSchemas{},
		&dispatchIdempotencyController{lease: lease, mutationReplayAvailable: true},
	)
	if _, err := first.Dispatch(context.Background(), fixture.request, nil); err != nil {
		t.Fatalf("seed replay record: %v", err)
	}
	if lease.completed.Authorization == nil {
		t.Fatal("seed execution did not produce replay authorization")
	}

	tests := []struct {
		name       string
		mutate     func(*RouteReplayAuthorization)
		mutatePlan func(RouteExecutionPlan) RouteExecutionPlan
		schemaErr  error
	}{
		{
			name: "missing stage",
			mutate: func(value *RouteReplayAuthorization) {
				value.RequestMutations = value.RequestMutations[:len(value.RequestMutations)-1]
			},
		},
		{
			name: "wrong step",
			mutate: func(value *RouteReplayAuthorization) {
				value.RequestMutations[1].StepIndex++
			},
		},
		{
			name: "before digest mismatch",
			mutate: func(value *RouteReplayAuthorization) {
				value.RequestMutations[0].BeforeDigest = strings.Repeat("b", 64)
			},
		},
		{
			name: "after digest mismatch",
			mutate: func(value *RouteReplayAuthorization) {
				value.RequestMutations[0].AfterDigest = strings.Repeat("c", 64)
			},
		},
		{
			name: "authorization schema drift",
			mutate: func(value *RouteReplayAuthorization) {
				value.Schema = "sforum.route-replay-authorization@2"
			},
		},
		{
			name: "plan request schema drift",
			mutatePlan: func(plan RouteExecutionPlan) RouteExecutionPlan {
				chain := plan.Chain()
				chain[0].RequestSchema += ".changed"
				return dispatchPlan(plan.Method(), plan.Path(), plan.Params(), chain, plan.terminalIndex)
			},
		},
		{
			name:      "runtime schema validator drift",
			schemaErr: errors.New("schema catalog changed"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorization := cloneRouteReplayAuthorization(lease.completed.Authorization)
			if test.mutate != nil {
				test.mutate(authorization)
			}
			plan := fixture.plan
			if test.mutatePlan != nil {
				plan = test.mutatePlan(plan)
			}
			invoker := &dispatcherReplayInvoker{fail: func() { t.Fatal("rejected replay invoked plugin") }}
			core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
				t.Fatal("rejected replay invoked core")
				return DispatchResponse{}, nil
			}}
			dispatcher := newDispatcherMutableReplayHarness(
				plan,
				invoker,
				&dispatcherReplayCaptureGuard{},
				&dispatcherReplayCaptureSchemas{requestErr: test.schemaErr},
				&dispatchIdempotencyController{
					mutationReplayAvailable: true,
					replay: &RouteIdempotencyReplay{
						Response:      cloneDispatchResponse(lease.completed.Response),
						Authorization: authorization,
					},
				},
			)

			result, err := dispatcher.Dispatch(context.Background(), fixture.request, core)
			if !errors.Is(err, ErrDispatchIdempotencyUnavailable) || result.Handled {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if invoker.calls != 0 || core.calls != 0 {
				t.Fatalf("rejected replay invoker=%d core=%d", invoker.calls, core.calls)
			}
		})
	}
}

func TestDispatcherRequiredMutableReplayReauthorizesRevokedHandler(t *testing.T) {
	fixture := newDispatcherMutableReplayFixture(t)
	lease := &dispatchIdempotencyLease{}
	seed := newDispatcherMutableReplayHarness(
		fixture.plan,
		&dispatcherReplayInvoker{requestPatches: map[string][]RoutePatchOperation{
			fixture.beforeID: fixture.beforePatch,
			fixture.filterID: fixture.filterPatch,
			fixture.wrapID:   nil,
		}},
		&dispatcherReplayCaptureGuard{},
		&dispatcherReplayCaptureSchemas{},
		&dispatchIdempotencyController{lease: lease, mutationReplayAvailable: true},
	)
	if _, err := seed.Dispatch(context.Background(), fixture.request, nil); err != nil {
		t.Fatalf("seed mutable replay: %v", err)
	}

	invoker := &dispatcherReplayInvoker{fail: func() { t.Fatal("revoked replay invoked plugin") }}
	core := &dispatchCoreInvoker{invoke: func(context.Context, RouteExecutionStep, DispatchRequest) (DispatchResponse, error) {
		t.Fatal("revoked replay invoked core")
		return DispatchResponse{}, nil
	}}
	guard := &dispatcherGuardFailureAuthorizer{
		failureRouteID: fixture.handlerID,
		failAt:         1,
		failure:        NewPluginGuardFailure(PluginGuardFailureDenied, true),
	}
	replay := newDispatcherMutableReplayHarness(
		fixture.plan,
		invoker,
		guard,
		&dispatcherReplayCaptureSchemas{},
		&dispatchIdempotencyController{
			mutationReplayAvailable: true,
			replay: &RouteIdempotencyReplay{
				Response:      cloneDispatchResponse(lease.completed.Response),
				Authorization: cloneRouteReplayAuthorization(lease.completed.Authorization),
			},
		},
	)

	result, err := replay.Dispatch(context.Background(), fixture.request, core)
	if result.Handled || !errors.Is(err, ErrDispatchDenied) || invoker.calls != 0 || core.calls != 0 ||
		guard.calls[fixture.handlerID] != 1 {
		t.Fatalf("result=%#v error=%v invoker=%d core=%d guards=%#v", result, err, invoker.calls, core.calls, guard.calls)
	}
}

func TestDispatcherRequiredMutableReplayPreservesPendingAfterUnknownCompletion(t *testing.T) {
	fixture := newDispatcherMutableReplayFixture(t)
	completeErr := errors.New("replay persistence result is unknown")
	lease := &dispatchIdempotencyLease{completeErr: completeErr}
	invoker := &dispatcherReplayInvoker{requestPatches: map[string][]RoutePatchOperation{
		fixture.beforeID: fixture.beforePatch,
		fixture.filterID: fixture.filterPatch,
		fixture.wrapID:   nil,
	}}
	dispatcher := newDispatcherMutableReplayHarness(
		fixture.plan,
		invoker,
		&dispatcherReplayCaptureGuard{},
		&dispatcherReplayCaptureSchemas{},
		&dispatchIdempotencyController{lease: lease, mutationReplayAvailable: true},
	)

	result, err := dispatcher.Dispatch(context.Background(), fixture.request, nil)
	if result.Handled || !errors.Is(err, ErrDispatchIdempotencyUnavailable) ||
		lease.completeCalls != 1 || lease.abortCalls != 0 || lease.completed.Authorization == nil || invoker.calls != 6 {
		t.Fatalf("result=%#v error=%v lease=%#v invocations=%d", result, err, lease, invoker.calls)
	}
	beforeRetry := invoker.calls
	retry := newDispatcherMutableReplayHarness(
		fixture.plan,
		invoker,
		&dispatcherReplayCaptureGuard{},
		&dispatcherReplayCaptureSchemas{},
		&dispatchIdempotencyController{err: ErrDispatchIdempotencyUnavailable, mutationReplayAvailable: true},
	)
	if second, retryErr := retry.Dispatch(context.Background(), fixture.request, nil); second.Handled ||
		!errors.Is(retryErr, ErrDispatchIdempotencyUnavailable) || invoker.calls != beforeRetry {
		t.Fatalf("retry=%#v error=%v invocations=%d want=%d", second, retryErr, invoker.calls, beforeRetry)
	}
}

func TestDispatcherRequiredMutableReplayRejectsAggregateTranscriptBeforeHandler(t *testing.T) {
	large := strings.Repeat("x", routeReplayMutationMaximumBytes/2+1024)
	firstPatch := []RoutePatchOperation{{
		Kind: RoutePatchAdd, Path: "/body/first", Value: routePatchValue(t, large),
	}}
	secondPatch := []RoutePatchOperation{{
		Kind: RoutePatchAdd, Path: "/body/second", Value: routePatchValue(t, large),
	}}
	first := dispatchPluginStep(RoutePhaseBefore, "demo.route.replay_budget_first", extensionmanifest.RouteActionBefore)
	first.MutableRequestFields = routePatchPaths(firstPatch)
	second := dispatchPluginStep(RoutePhaseFilter, "demo.route.replay_budget_second", extensionmanifest.RouteActionFilter)
	second.MutableRequestFields = routePatchPaths(secondPatch)
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.replay_budget_handler", extensionmanifest.RouteActionAdd)
	plan := dispatchPlan("POST", "/replay-budget", nil, []RouteExecutionStep{first, second, handler}, 2)
	lease := &dispatchIdempotencyLease{}
	invoker := &dispatcherReplayInvoker{requestPatches: map[string][]RoutePatchOperation{
		first.RouteID: firstPatch, second.RouteID: secondPatch,
	}, observeExecution: true}
	dispatcher := newDispatcherMutableReplayHarness(
		plan,
		invoker,
		&dispatcherReplayCaptureGuard{},
		&dispatcherReplayCaptureSchemas{},
		&dispatchIdempotencyController{lease: lease, mutationReplayAvailable: true},
	)

	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{
		Method: "POST", Path: "/replay-budget", Body: []byte(`{}`),
	}, nil)
	if result.Handled || !errors.Is(err, ErrDispatchIdempotencyUnavailable) ||
		invoker.calls != 2 || lease.completeCalls != 0 || lease.abortCalls != 0 {
		t.Fatalf("result=%#v error=%v invocations=%d lease=%#v", result, err, invoker.calls, lease)
	}
}

type dispatcherMutableReplayFixture struct {
	plan        RouteExecutionPlan
	request     DispatchRequest
	beforeID    string
	filterID    string
	wrapID      string
	handlerID   string
	beforePatch []RoutePatchOperation
	filterPatch []RoutePatchOperation
}

type dispatcherReplayExpectedRequests struct {
	initial     DispatchRequest
	afterBefore DispatchRequest
	afterFilter DispatchRequest
}

func newDispatcherMutableReplayFixture(t *testing.T) dispatcherMutableReplayFixture {
	t.Helper()
	beforePatch := []RoutePatchOperation{
		{Kind: RoutePatchAdd, Path: "/query/added", Value: routePatchValue(t, []string{"q1", "q2"})},
		{Kind: RoutePatchReplace, Path: "/query/keep/0", Value: routePatchValue(t, "two")},
		{Kind: RoutePatchRemove, Path: "/query/drop"},
		{Kind: RoutePatchAdd, Path: "/params/added", Value: routePatchValue(t, "p1")},
		{Kind: RoutePatchReplace, Path: "/params/slug", Value: routePatchValue(t, "new")},
		{Kind: RoutePatchRemove, Path: "/params/remove"},
		{Kind: RoutePatchAdd, Path: "/body/added", Value: routePatchValue(t, "b1")},
		{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "new")},
		{Kind: RoutePatchRemove, Path: "/body/remove"},
		{Kind: RoutePatchAdd, Path: "/headers/x-added", Value: routePatchValue(t, []string{"h1"})},
		{Kind: RoutePatchReplace, Path: "/headers/x-keep/0", Value: routePatchValue(t, "new")},
		{Kind: RoutePatchRemove, Path: "/headers/x-remove"},
	}
	filterPatch := []RoutePatchOperation{
		{Kind: RoutePatchReplace, Path: "/query/added/1", Value: routePatchValue(t, "q3")},
		{Kind: RoutePatchReplace, Path: "/params/added", Value: routePatchValue(t, "p2")},
		{Kind: RoutePatchReplace, Path: "/body/added", Value: routePatchValue(t, "b2")},
		{Kind: RoutePatchReplace, Path: "/headers/x-added/0", Value: routePatchValue(t, "h2")},
	}

	const (
		beforeID  = "demo.route.replay_before"
		filterID  = "demo.route.replay_filter"
		wrapID    = "demo.route.replay_wrap"
		handlerID = "demo.route.replay_handler"
	)
	before := dispatchPluginStep(RoutePhaseBefore, beforeID, extensionmanifest.RouteActionBefore)
	before.MutableRequestFields = routePatchPaths(beforePatch)
	filter := dispatchPluginStep(RoutePhaseFilter, filterID, extensionmanifest.RouteActionFilter)
	filter.MutableRequestFields = routePatchPaths(filterPatch)
	wrap := dispatchPluginStep(RoutePhaseWrap, wrapID, extensionmanifest.RouteActionWrap)
	// 即使插件本次返回空 patch，声明过 mutation 的 request stage 也必须进入 transcript。
	wrap.MutableRequestFields = []string{"/query/keep/0"}
	handler := dispatchPluginStep(RoutePhaseHandler, handlerID, extensionmanifest.RouteActionAdd)
	plan := dispatchPlan(
		"POST", "/mutable/old", map[string]string{"slug": "old", "remove": "gone"},
		[]RouteExecutionStep{before, filter, wrap, handler}, 3,
	)
	request := DispatchRequest{
		Method: "POST", Path: "/mutable/old", Query: "keep=one&drop=gone",
		Headers: http.Header{
			"Content-Type":    {"application/json"},
			"Idempotency-Key": {"mutable-replay-42"},
			"X-Keep":          {"old"},
			"X-Remove":        {"gone"},
		},
		Body:     []byte(`{"title":"old","remove":"gone"}`),
		ClientIP: "127.0.0.1",
	}
	return dispatcherMutableReplayFixture{
		plan: plan, request: request, beforeID: beforeID, filterID: filterID, wrapID: wrapID,
		handlerID: handlerID, beforePatch: beforePatch, filterPatch: filterPatch,
	}
}

func (f dispatcherMutableReplayFixture) expectedRequests(t *testing.T) dispatcherReplayExpectedRequests {
	t.Helper()
	initial := cloneDispatchRequest(f.request)
	initial.Params = f.plan.Params()
	initial.hostMutatedParams = false
	afterBefore, err := applyRouteRequestPatch(initial, f.beforePatch, routePatchPaths(f.beforePatch), false)
	if err != nil {
		t.Fatalf("build expected before patch: %v", err)
	}
	afterBefore.hostMutatedParams = true
	afterFilter, err := applyRouteRequestPatch(afterBefore, f.filterPatch, routePatchPaths(f.filterPatch), false)
	if err != nil {
		t.Fatalf("build expected filter patch: %v", err)
	}
	return dispatcherReplayExpectedRequests{initial: initial, afterBefore: afterBefore, afterFilter: afterFilter}
}

func (f dispatcherMutableReplayFixture) guardObservations(
	expected dispatcherReplayExpectedRequests,
) []dispatcherReplayObservation {
	return []dispatcherReplayObservation{
		{routeID: f.beforeID, request: expected.initial},
		{routeID: f.filterID, request: expected.afterBefore},
		{routeID: f.wrapID, request: expected.afterFilter},
		{routeID: f.handlerID, request: expected.afterFilter},
		{routeID: f.wrapID, request: expected.afterFilter},
		{routeID: f.filterID, request: expected.afterFilter},
	}
}

func (f dispatcherMutableReplayFixture) schemaObservations(
	expected dispatcherReplayExpectedRequests,
) []dispatcherReplayObservation {
	return []dispatcherReplayObservation{
		{routeID: f.beforeID, request: expected.initial},
		{routeID: f.beforeID, request: expected.afterBefore},
		{routeID: f.filterID, request: expected.afterBefore},
		{routeID: f.filterID, request: expected.afterFilter},
		{routeID: f.wrapID, request: expected.afterFilter},
		{routeID: f.wrapID, request: expected.afterFilter},
		{routeID: f.handlerID, request: expected.afterFilter},
		{routeID: f.wrapID, request: expected.afterFilter},
		{routeID: f.filterID, request: expected.afterFilter},
	}
}

func newDispatcherMutableReplayHarness(
	plan RouteExecutionPlan,
	invoker StepInvoker,
	guard GuardAuthorizer,
	schemas SchemaValidator,
	idempotency RouteIdempotencyController,
) *Dispatcher {
	return NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: plan}, Steps: invoker, Guard: guard, Schemas: schemas,
		Policies:    dispatchPolicyResolver{policy: RouteExecutionPolicy{Idempotency: "required.24h@1", IdempotencyRequired: true}},
		Idempotency: idempotency,
		Failures:    dispatcherReplayFailureSink{},
	})
}

type dispatcherReplayFailureSink struct{}

func (dispatcherReplayFailureSink) RecordCommittedAfterFailure(context.Context, RouteCommittedAfterFailure) {
}

type dispatcherReplayObservation struct {
	routeID string
	request DispatchRequest
}

type dispatcherReplayCaptureGuard struct {
	observations []dispatcherReplayObservation
}

func (g *dispatcherReplayCaptureGuard) AuthorizeRoute(
	_ context.Context,
	plan RouteExecutionPlan,
	stepIndex int,
	step RouteExecutionStep,
	request DispatchRequest,
) (RouteGuardAuthorization, error) {
	g.observations = append(g.observations, dispatcherReplayObservation{
		routeID: step.RouteID, request: cloneDispatchRequest(request),
	})
	authorization, ok := authorizedRouteGuardAuthorization(plan, stepIndex, step, request)
	if !ok {
		return RouteGuardAuthorization{}, ErrCoreGuardEvaluatorUnavailable
	}
	return authorization, nil
}

func (g *dispatcherReplayCaptureGuard) Authorize(
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

type dispatcherReplayCaptureSchemas struct {
	observations []dispatcherReplayObservation
	requestErr   error
}

func (s *dispatcherReplayCaptureSchemas) ValidateRequest(
	_ context.Context,
	step RouteExecutionStep,
	request DispatchRequest,
) error {
	s.observations = append(s.observations, dispatcherReplayObservation{
		routeID: step.RouteID, request: cloneDispatchRequest(request),
	})
	return s.requestErr
}

func (*dispatcherReplayCaptureSchemas) ValidateResponse(
	context.Context,
	RouteExecutionStep,
	DispatchRequest,
	DispatchResponse,
) error {
	return nil
}

type dispatcherReplayInvoker struct {
	calls            int
	requestPatches   map[string][]RoutePatchOperation
	fail             func()
	observeExecution bool
}

func (*dispatcherReplayInvoker) SupportsMode(mode string) bool {
	return mode == extensionmanifest.RouteModeHTTP
}

func (i *dispatcherReplayInvoker) Invoke(
	_ context.Context,
	input RouteInvocation,
) (RouteInvocationResult, error) {
	i.calls++
	if i.observeExecution && input.Commit != nil {
		input.Commit.SideEffectStarted()
	}
	if i.fail != nil {
		i.fail()
	}
	switch input.Stage {
	case InvocationStageRequest:
		return RouteInvocationResult{RequestPatch: cloneRoutePatchOperations(i.requestPatches[input.Step.RouteID])}, nil
	case InvocationStageHandler:
		response := DispatchResponse{
			Status: http.StatusCreated, Headers: http.Header{"Content-Type": {"application/json"}},
			Body: []byte(`{"ok":true}`),
		}
		return RouteInvocationResult{Response: &response, SideEffectStarted: true, ResponseStarted: true}, nil
	case InvocationStageResponse:
		return RouteInvocationResult{}, nil
	default:
		return RouteInvocationResult{}, ErrDispatchTransport
	}
}

func assertDispatcherReplayTranscript(
	t *testing.T,
	authorization *RouteReplayAuthorization,
	fixture dispatcherMutableReplayFixture,
	expected dispatcherReplayExpectedRequests,
) {
	t.Helper()
	if authorization == nil || len(authorization.RequestMutations) != 3 {
		t.Fatalf("authorization = %#v", authorization)
	}
	binding, err := BuildRouteReplayBinding(fixture.plan, expected.initial)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Schema != routeReplayAuthorizationSchema || authorization.PlanDigest != binding.PlanDigest ||
		authorization.BaseDigest != binding.BaseDigest {
		t.Fatalf("authorization binding = %#v want=%#v", authorization, binding)
	}
	expectedRequests := []struct {
		stepIndex int
		before    DispatchRequest
		after     DispatchRequest
		patch     []RoutePatchOperation
	}{
		{stepIndex: 0, before: expected.initial, after: expected.afterBefore, patch: fixture.beforePatch},
		{stepIndex: 1, before: expected.afterBefore, after: expected.afterFilter, patch: fixture.filterPatch},
		{stepIndex: 2, before: expected.afterFilter, after: expected.afterFilter, patch: nil},
	}
	for index, want := range expectedRequests {
		got := authorization.RequestMutations[index]
		beforeDigest, beforeErr := routeReplayRequestDigest(want.before)
		afterDigest, afterErr := routeReplayRequestDigest(want.after)
		if beforeErr != nil || afterErr != nil {
			t.Fatalf("digest[%d] before=%v after=%v", index, beforeErr, afterErr)
		}
		if got.StepIndex != want.stepIndex || got.BeforeDigest != beforeDigest || got.AfterDigest != afterDigest ||
			!reflect.DeepEqual(got.Operations, cloneRoutePatchOperations(want.patch)) {
			t.Fatalf("mutation[%d] = %#v", index, got)
		}
	}
}

func assertDispatcherReplayObservations(
	t *testing.T,
	label string,
	got []dispatcherReplayObservation,
	want []dispatcherReplayObservation,
) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s count=%d want=%d observations=%#v", label, len(got), len(want), got)
	}
	for index := range want {
		if got[index].routeID != want[index].routeID || !reflect.DeepEqual(got[index].request, want[index].request) {
			t.Fatalf("%s[%d] = %#v want=%#v", label, index, got[index], want[index])
		}
	}
}
