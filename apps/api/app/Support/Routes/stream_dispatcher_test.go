package routes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestStreamDispatcherPreparesExactTerminalAndPublishesCommitTrace(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("stream.dispatch", "1.0.0", 'a')
	stream := pluginRoute("stream.dispatch.events", "/events", 0, "GET")
	stream.Mode = extensionmanifest.RouteModeSSE
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{stream}}}}); err != nil {
		t.Fatal(err)
	}
	traces := NewRouteTraceRing(8)
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: streamRegistryResolver{registry}, Guard: allowStreamGuard{}, Trace: traces,
	})
	prepared, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{
		Method: "get", Path: "/events", Headers: map[string][]string{"X-Test": {"one"}},
	})
	if err != nil || !prepared.Handled || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	if step := prepared.Dispatch.Step(); step.RouteID != stream.ID || step.Provider.Artifact != artifact {
		t.Fatalf("step=%#v", step)
	}
	request := prepared.Dispatch.Request()
	request.Headers.Set("X-Test", "mutated")
	if prepared.Dispatch.Request().Headers.Get("X-Test") != "one" {
		t.Fatal("stream request was not detached")
	}
	prepared.Dispatch.RequestStarted()
	prepared.Dispatch.ResponseStarted()
	if err := prepared.Dispatch.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Dispatch.Complete(); !errors.Is(err, ErrDispatchAlreadyCommitted) {
		t.Fatalf("second complete error=%v", err)
	}
	records := traces.RouteTraces(8)
	if len(records) != 2 || records[0].Outcome != RouteTraceSucceeded || records[1].Outcome != RouteTraceCommitted ||
		records[1].CommitState != RouteCommitFinal {
		t.Fatalf("traces=%#v", records)
	}
}

func TestRouteStreamDispatchStepIsDetached(t *testing.T) {
	dispatch := &RouteStreamDispatch{step: RouteExecutionStep{
		MutableRequestFields:  []string{"/query"},
		MutableResponseFields: []string{"/status"},
	}}
	step := dispatch.Step()
	step.MutableRequestFields[0] = "/caller-request"
	step.MutableResponseFields[0] = "/caller-response"

	again := dispatch.Step()
	if again.MutableRequestFields[0] != "/query" || again.MutableResponseFields[0] != "/status" {
		t.Fatalf("stream step leaked mutable field slices: %#v", again)
	}
}

func TestStreamDispatcherBypassesCoreAndBufferedPlansAndFailsClosedOnComposition(t *testing.T) {
	registry := NewRegistry()
	core := coreRoute("core.route.stream.bypass", "GET", "/core")
	artifact := routeArtifact("stream.composed", "1.0.0", 'c')
	buffered := pluginRoute("stream.composed.buffered", "/buffered", 0, "GET")
	composed := pluginRoute("stream.composed.handler", "/composed", 0, "GET")
	composed.Mode = extensionmanifest.RouteModeSSE
	global := extensionmanifest.ManifestRoute{
		ID: "stream.composed.global", ContractVersion: "stream.composed.global@1",
		Action: extensionmanifest.RouteActionGlobalMiddleware, Guard: extensionmanifest.GuardCorePublic,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.global",
		RequestSchema: "stream.composed.global.request@1", ResponseSchema: "stream.composed.global.response@1",
	}
	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{core}, Plugins: []PluginRouteSet{{
			Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{buffered, composed, global},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(DispatcherConfig{Plans: streamRegistryResolver{registry}, Guard: allowStreamGuard{}})
	for _, path := range []string{"/core", "/buffered", "/missing"} {
		prepared, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{Method: "GET", Path: path})
		if err != nil || prepared.Handled {
			t.Fatalf("path=%s prepared=%#v err=%v", path, prepared, err)
		}
	}
	if _, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{Method: "GET", Path: "/composed"}); !errors.Is(err, ErrDispatchTransport) {
		t.Fatalf("composed stream error=%v", err)
	}
}

func TestRegistryRejectsNonHTTPModifierMappingAndGlobalModes(t *testing.T) {
	artifact := routeArtifact("stream.contract", "1.0.0", 'f')
	target := coreRoute("core.route.stream.contract", "GET", "/stream-contract")
	modifier := modifierRoute(
		"stream.contract.before", target.ID, target.Path, extensionmanifest.RouteActionBefore, "GET", 1,
	)
	mapping := stableMappingRoute(
		"stream.contract.alias", target.ID, "/stream-contract-alias", extensionmanifest.RouteActionAlias,
	)
	global := executionGlobalRoute("stream.contract.global", 1)
	for _, declaration := range []extensionmanifest.ManifestRoute{modifier, mapping, global} {
		declaration.Mode = extensionmanifest.RouteModeSSE
		registry := NewRegistry()
		if _, err := registry.Publish(Publication{
			Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration}}},
		}); !errors.Is(err, ErrInvalidRoute) || registry.Revision() != 0 {
			t.Fatalf("action=%s revision=%d error=%v", declaration.Action, registry.Revision(), err)
		}
	}
}

func TestStreamDispatcherRejectsDriftedTrailingModifierAndTerminalAction(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "stream.drifted.handler", extensionmanifest.RouteActionAdd)
	handler.Mode = extensionmanifest.RouteModeSSE
	after := dispatchPluginStep(RoutePhaseAfter, "stream.drifted.after", extensionmanifest.RouteActionAfter)
	invalidHandler := handler
	invalidHandler.RouteID = "stream.drifted.invalid_handler"
	invalidHandler.ContractVersion = invalidHandler.RouteID + "@1"
	invalidHandler.Action = extensionmanifest.RouteActionBefore

	for _, test := range []struct {
		name     string
		steps    []RouteExecutionStep
		terminal int
	}{
		{name: "trailing response modifier", steps: []RouteExecutionStep{handler, after}, terminal: 0},
		{name: "invalid terminal action", steps: []RouteExecutionStep{invalidHandler}, terminal: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := NewDispatcher(DispatcherConfig{Plans: dispatchPlanResolver{plan: dispatchPlan(
				"GET", "/drifted-stream", nil, test.steps, test.terminal,
			)}})
			prepared, err := dispatcher.PrepareStream(
				context.Background(), DispatchRequest{Method: "GET", Path: "/drifted-stream"},
			)
			if prepared.Handled || !errors.Is(err, ErrDispatchTransport) {
				t.Fatalf("prepared=%#v error=%v", prepared, err)
			}
		})
	}
}

func TestStreamDispatcherFailsClosedWithoutGuard(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("stream.denied", "1.0.0", 'd')
	stream := pluginRoute("stream.denied.events", "/denied", 0, "GET")
	stream.Mode = extensionmanifest.RouteModeStream
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{stream}}}}); err != nil {
		t.Fatal(err)
	}
	prepared, err := NewDispatcher(DispatcherConfig{Plans: streamRegistryResolver{registry}}).PrepareStream(
		context.Background(), DispatchRequest{Method: "GET", Path: "/denied"},
	)
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v error=%v", prepared, err)
	}
	if _, err := prepared.Dispatch.Open(context.Background()); !errors.Is(err, ErrDispatchDenied) {
		t.Fatalf("open guard error=%v", err)
	}
}

func TestStreamDispatcherClassifiesPluginGuardFailures(t *testing.T) {
	failures := []struct {
		name     string
		kind     PluginGuardFailureKind
		observed bool
		wantErr  error
		outcome  RouteTraceOutcome
	}{
		{name: "denied", kind: PluginGuardFailureDenied, observed: true, wantErr: ErrDispatchDenied, outcome: RouteTraceDenied},
		{name: "unavailable", kind: PluginGuardFailureUnavailable, wantErr: ErrDispatchTransport, outcome: RouteTraceTransportFailed},
		{name: "crash", kind: PluginGuardFailureCrash, observed: true, wantErr: ErrDispatchTransport, outcome: RouteTraceTransportFailed},
		{name: "timeout", kind: PluginGuardFailureTimeout, observed: true, wantErr: ErrDispatchTransport, outcome: RouteTraceTransportFailed},
		{name: "protocol", kind: PluginGuardFailureProtocol, observed: true, wantErr: ErrDispatchTransport, outcome: RouteTraceTransportFailed},
		{name: "runtime canceled", kind: PluginGuardFailureCanceled, observed: true, wantErr: ErrDispatchTransport, outcome: RouteTraceTransportFailed},
	}
	for _, action := range []string{extensionmanifest.RouteActionAdd, extensionmanifest.RouteActionReplace} {
		for _, raw := range []bool{false, true} {
			guardKind := "custom"
			if raw {
				guardKind = "raw_request"
			}
			for _, failure := range failures {
				t.Run(action+"/"+guardKind+"/"+failure.name, func(t *testing.T) {
					step := streamPluginGuardStep("stream.guard_failure."+action+"."+guardKind, action, raw)
					guard := &streamFailureGuard{failure: NewPluginGuardFailure(failure.kind, failure.observed)}
					invoker := &authorityStreamInvoker{}
					traces := NewRouteTraceRing(4)
					sink := &recordingRouteFailureSink{}
					dispatcher := NewDispatcher(DispatcherConfig{
						Plans: dispatchPlanResolver{plan: dispatchPlan(http.MethodGet, "/guard-stream", nil, []RouteExecutionStep{step}, 0)},
						Guard: guard, Steps: invoker, Trace: traces, Failures: sink,
					})
					prepared, err := dispatcher.PrepareStream(
						context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/guard-stream"},
					)
					if err != nil || prepared.Dispatch == nil {
						t.Fatalf("prepared=%#v error=%v", prepared, err)
					}

					_, err = prepared.Dispatch.Open(context.Background())
					if !errors.Is(err, failure.wantErr) || invoker.calls != 0 || len(sink.events) != 0 ||
						prepared.Dispatch.commit.ExecutionObserved() {
						t.Fatalf("error=%v invocations=%d observed=%t", err, invoker.calls, prepared.Dispatch.commit.ExecutionObserved())
					}
					records := traces.RouteTraces(0)
					if len(records) != 1 || records[0].Outcome != failure.outcome ||
						records[0].InvocationStage != InvocationStageHandler ||
						records[0].CommitState != RouteCommitPristine || records[0].RouteID != step.RouteID {
						t.Fatalf("traces=%#v", records)
					}
					if _, secondErr := prepared.Dispatch.Open(context.Background()); !errors.Is(secondErr, ErrDispatchAlreadyCommitted) || len(traces.RouteTraces(0)) != 1 {
						t.Fatalf("second open error=%v traces=%#v", secondErr, traces.RouteTraces(0))
					}
				})
			}
		}
	}
}

func TestStreamDispatcherCallerCancellationHasNoFailureEvidence(t *testing.T) {
	for _, raw := range []bool{false, true} {
		guardKind := "custom"
		if raw {
			guardKind = "raw_request"
		}
		t.Run(guardKind, func(t *testing.T) {
			step := streamPluginGuardStep(
				"stream.guard_canceled."+guardKind, extensionmanifest.RouteActionAdd, raw,
			)
			guard := &streamFailureGuard{failure: NewPluginGuardFailure(PluginGuardFailureCanceled, true)}
			invoker := &authorityStreamInvoker{}
			traces := NewRouteTraceRing(4)
			sink := &recordingRouteFailureSink{}
			dispatcher := NewDispatcher(DispatcherConfig{
				Plans: dispatchPlanResolver{plan: dispatchPlan(http.MethodGet, "/guard-canceled", nil, []RouteExecutionStep{step}, 0)},
				Guard: guard, Steps: invoker, Trace: traces, Failures: sink,
			})
			prepared, err := dispatcher.PrepareStream(
				context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/guard-canceled"},
			)
			if err != nil || prepared.Dispatch == nil {
				t.Fatalf("prepared=%#v error=%v", prepared, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			_, err = prepared.Dispatch.Open(ctx)
			if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDispatchDenied) ||
				errors.Is(err, ErrDispatchTransport) || invoker.calls != 0 ||
				prepared.Dispatch.commit.ExecutionObserved() || len(traces.RouteTraces(0)) != 0 || len(sink.events) != 0 {
				t.Fatalf("error=%v invocations=%d observed=%t traces=%#v",
					err, invoker.calls, prepared.Dispatch.commit.ExecutionObserved(), traces.RouteTraces(0))
			}
		})
	}
}

func TestStreamDispatcherCallerCancellationBeforeRuntimeAdmissionHasNoFailureEvidence(t *testing.T) {
	for _, action := range []string{extensionmanifest.RouteActionAdd, extensionmanifest.RouteActionReplace} {
		for _, raw := range []bool{false, true} {
			guardKind := "custom"
			if raw {
				guardKind = "raw_request"
			}
			t.Run(action+"/"+guardKind, func(t *testing.T) {
				step := streamPluginGuardStep("stream.pre_admission_canceled."+action+"."+guardKind, action, raw)
				ctx, cancel := context.WithCancel(context.Background())
				guard := &cancelingStreamGuard{cancel: cancel}
				invoker := &preAdmissionCanceledStreamInvoker{t: t}
				traces := NewRouteTraceRing(4)
				sink := &recordingRouteFailureSink{}
				dispatcher := NewDispatcher(DispatcherConfig{
					Plans: dispatchPlanResolver{plan: dispatchPlan(http.MethodGet, "/pre-admission-canceled", nil, []RouteExecutionStep{step}, 0)},
					Guard: guard, Steps: invoker, Trace: traces, Failures: sink,
				})
				prepared, err := dispatcher.PrepareStream(
					context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/pre-admission-canceled"},
				)
				if err != nil || prepared.Dispatch == nil {
					t.Fatalf("prepared=%#v error=%v", prepared, err)
				}

				_, err = prepared.Dispatch.Open(ctx)
				// Host open context absorbs caller cancel after guard and before the
				// streaming invoker, so runtime admission never starts.
				if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDispatchDenied) ||
					errors.Is(err, ErrDispatchTransport) || invoker.calls != 0 ||
					prepared.Dispatch.commit.ExecutionObserved() || len(traces.RouteTraces(0)) != 0 || len(sink.events) != 0 {
					t.Fatalf("error=%v invocations=%d observed=%t traces=%#v incidents=%#v",
						err, invoker.calls, prepared.Dispatch.commit.ExecutionObserved(), traces.RouteTraces(0), sink.events)
				}
			})
		}
	}
}

func TestStreamDispatcherCallerCancellationAfterObservedExecutionFailsClosed(t *testing.T) {
	for _, action := range []string{extensionmanifest.RouteActionAdd, extensionmanifest.RouteActionReplace} {
		for _, raw := range []bool{false, true} {
			guardKind := "custom"
			if raw {
				guardKind = "raw_request"
			}
			t.Run(action+"/"+guardKind, func(t *testing.T) {
				step := streamPluginGuardStep("stream.observed_canceled."+action+"."+guardKind, action, raw)
				ctx, cancel := context.WithCancel(context.Background())
				invoker := &observedCanceledStreamInvoker{cancel: cancel}
				streamFailures := &recordingRouteStreamFailureSink{}
				traces := NewRouteTraceRing(4)
				dispatcher := NewDispatcher(DispatcherConfig{
					Plans: dispatchPlanResolver{plan: dispatchPlan(http.MethodGet, "/observed-canceled", nil, []RouteExecutionStep{step}, 0)},
					Guard: allowStreamGuard{}, Steps: invoker, Trace: traces, StreamFailures: streamFailures,
				})
				prepared, err := dispatcher.PrepareStream(
					context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/observed-canceled"},
				)
				if err != nil || prepared.Dispatch == nil {
					t.Fatalf("prepared=%#v error=%v", prepared, err)
				}

				_, err = prepared.Dispatch.Open(ctx)
				records := traces.RouteTraces(0)
				if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrDispatchTransport) || invoker.calls != 1 ||
					len(records) != 1 || records[0].Outcome != RouteTraceTransportFailed ||
					records[0].CommitState != RouteCommitSideEffectStarted || len(streamFailures.snapshot()) != 0 {
					t.Fatalf("error=%v invocations=%d traces=%#v", err, invoker.calls, records)
				}
			})
		}
	}
}

func TestStreamDispatcherRejectsPublishedRequiredIdempotencyPolicy(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("stream.idempotency", "1.0.0", '1')
	stream := pluginRoute("stream.idempotency.events", "/idempotent-stream", 0, "POST")
	stream.Mode = extensionmanifest.RouteModeMultipart
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{stream}}}}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: streamRegistryResolver{registry}, Guard: allowStreamGuard{},
		Policies: dispatchPolicyResolver{policy: RouteExecutionPolicy{
			Idempotency: "required.24h@1", IdempotencyRequired: true,
		}},
	})
	prepared, err := dispatcher.PrepareStream(
		context.Background(), DispatchRequest{Method: "POST", Path: "/idempotent-stream"},
	)
	if prepared.Handled || !errors.Is(err, ErrDispatchIdempotencyUnavailable) {
		t.Fatalf("prepared=%#v error=%v", prepared, err)
	}
}

func TestStreamDispatcherPreservesAuthorizedRawRequestStamp(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("stream.raw", "1.0.0", 'a')
	stream := pluginRoute("stream.raw.events", "/raw-events", 0, "GET")
	stream.Mode = extensionmanifest.RouteModeSSE
	// 决定 R1（2026-10-16）：raw 请求权限只能来自包内显式声明的 kind=raw_request
	// 守卫（entry + digest + contractVersion 必须与包内制品一致）。
	stream.Guard = "stream.raw.raw_guard"
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{stream},
		Guards: []extensionmanifest.ManifestGuard{pluginGuard(stream.Guard, "raw_request")},
	}}}); err != nil {
		t.Fatal(err)
	}
	invoker := &authorityStreamInvoker{}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: streamRegistryResolver{registry}, Guard: allowStreamGuard{}, Steps: invoker,
	})
	prepared, err := dispatcher.PrepareStream(
		context.Background(), DispatchRequest{Method: "GET", Path: "/raw-events"},
	)
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v error=%v", prepared, err)
	}
	start, err := prepared.Dispatch.Open(context.Background())
	if err != nil || start.Session == nil || !invoker.raw {
		t.Fatalf("start=%#v raw=%v error=%v", start, invoker.raw, err)
	}
	if _, err := prepared.Dispatch.Open(context.Background()); !errors.Is(err, ErrDispatchAlreadyCommitted) || invoker.calls != 1 {
		t.Fatalf("second open error=%v calls=%d", err, invoker.calls)
	}
	start.Session.Cancel()
}

type authorityStreamInvoker struct {
	raw   bool
	calls int
}

type streamFailureGuard struct {
	failure error
}

type cancelingStreamGuard struct {
	allowStreamGuard
	cancel context.CancelFunc
}

func (g *cancelingStreamGuard) AuthorizeRoute(
	_ context.Context,
	plan RouteExecutionPlan,
	stepIndex int,
	step RouteExecutionStep,
	request DispatchRequest,
) (RouteGuardAuthorization, error) {
	authorization, ok := authorizedRouteGuardAuthorization(plan, stepIndex, step, request)
	if !ok {
		return RouteGuardAuthorization{}, ErrCoreGuardEvaluatorUnavailable
	}
	g.cancel()
	return authorization, nil
}

type preAdmissionCanceledStreamInvoker struct {
	t     *testing.T
	calls int
}

type observedCanceledStreamInvoker struct {
	cancel context.CancelFunc
	calls  int
}

func (*preAdmissionCanceledStreamInvoker) SupportsMode(string) bool { return false }

func (*preAdmissionCanceledStreamInvoker) Invoke(context.Context, RouteInvocation) (RouteInvocationResult, error) {
	return RouteInvocationResult{}, ErrDispatchTransport
}

func (i *preAdmissionCanceledStreamInvoker) OpenStream(ctx context.Context, input RouteInvocation) (RouteStreamStart, error) {
	i.calls++
	if !errors.Is(ctx.Err(), context.Canceled) || input.Commit == nil || input.Commit.ExecutionObserved() {
		i.t.Fatalf("context=%v commit=%#v", ctx.Err(), input.Commit)
	}
	return RouteStreamStart{}, ctx.Err()
}

func (*observedCanceledStreamInvoker) SupportsMode(string) bool { return false }

func (*observedCanceledStreamInvoker) Invoke(context.Context, RouteInvocation) (RouteInvocationResult, error) {
	return RouteInvocationResult{}, ErrDispatchTransport
}

func (i *observedCanceledStreamInvoker) OpenStream(ctx context.Context, input RouteInvocation) (RouteStreamStart, error) {
	i.calls++
	input.Commit.SideEffectStarted()
	i.cancel()
	return RouteStreamStart{}, ctx.Err()
}

func (g *streamFailureGuard) AuthorizeRoute(
	_ context.Context,
	plan RouteExecutionPlan,
	stepIndex int,
	step RouteExecutionStep,
	request DispatchRequest,
) (RouteGuardAuthorization, error) {
	authorization, ok := authorizedRouteGuardAuthorization(plan, stepIndex, step, request)
	if !ok {
		return RouteGuardAuthorization{}, ErrCoreGuardEvaluatorUnavailable
	}
	return authorization, g.failure
}

func (g *streamFailureGuard) Authorize(
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

func streamPluginGuardStep(id, action string, raw bool) RouteExecutionStep {
	step := dispatchPluginStep(RoutePhaseHandler, id, action)
	step.Mode = extensionmanifest.RouteModeWebSocket
	kind, suffix := "custom", "custom"
	if raw {
		kind, suffix = "raw_request", "raw"
	}
	step.Guard = id + "." + suffix
	step.PluginGuard = PluginGuardBinding{
		ID: step.Guard, ContractVersion: step.Guard + "@1", Kind: kind,
		Entry: "backend/" + suffix, Digest: strings.Repeat("c", 64),
	}
	return step
}

func (*authorityStreamInvoker) SupportsMode(string) bool { return false }

func (*authorityStreamInvoker) Invoke(context.Context, RouteInvocation) (RouteInvocationResult, error) {
	return RouteInvocationResult{}, ErrDispatchTransport
}

func (i *authorityStreamInvoker) OpenStream(_ context.Context, input RouteInvocation) (RouteStreamStart, error) {
	i.calls++
	i.raw = input.RawRequestAuthorized()
	return RouteStreamStart{
		Response: DispatchResponse{Status: http.StatusOK}, Session: authorityStreamSession{},
	}, nil
}

type authorityStreamSession struct{}

func (authorityStreamSession) Send([]byte, bool) error            { return nil }
func (authorityStreamSession) CloseRequest() error                { return nil }
func (authorityStreamSession) Recv() (RouteStreamChunk, error)    { return RouteStreamChunk{}, io.EOF }
func (authorityStreamSession) Response() (DispatchResponse, bool) { return DispatchResponse{}, false }
func (authorityStreamSession) Cancel()                            {}

type allowStreamGuard struct{}

func (allowStreamGuard) Authorize(context.Context, RouteExecutionPlan, RouteExecutionStep, DispatchRequest) error {
	return nil
}

func (allowStreamGuard) AuthorizeRoute(
	_ context.Context,
	plan RouteExecutionPlan,
	stepIndex int,
	step RouteExecutionStep,
	request DispatchRequest,
) (RouteGuardAuthorization, error) {
	authorization, ok := authorizedRouteGuardAuthorization(plan, stepIndex, step, request)
	if !ok {
		return RouteGuardAuthorization{}, ErrCoreGuardEvaluatorUnavailable
	}
	return authorization, nil
}

type streamRegistryResolver struct{ registry *Registry }

func (r streamRegistryResolver) BuildExecutionPlan(_ context.Context, method, path string) (RouteExecutionPlan, error) {
	return r.registry.BuildExecutionPlan(method, path)
}
