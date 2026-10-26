package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRouteStreamFailureSinkRecordsExactPayloadFreeIncidentOnce(t *testing.T) {
	for _, class := range []RouteStreamFailureClass{
		RouteStreamFailureRuntimeTransport,
		RouteStreamFailureHostBudget,
		RouteStreamFailureInvalidPreflight,
		RouteStreamFailureMissingTerminal,
	} {
		t.Run(string(class), func(t *testing.T) {
			sink := &recordingRouteStreamFailureSink{}
			step := streamIncidentStep("stream.incident." + string(class))
			dispatcher := streamIncidentDispatcher(step, &streamIncidentInvoker{}, sink)
			prepared, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{
				Method: http.MethodGet, Path: "/incident", ActorID: 42,
			})
			if err != nil || prepared.Dispatch == nil {
				t.Fatalf("prepared=%#v err=%v", prepared, err)
			}
			start, err := prepared.Dispatch.Open(context.Background())
			if err != nil || start.Session == nil {
				t.Fatalf("start=%#v err=%v", start, err)
			}
			prepared.Dispatch.ResponseStarted()
			failure := errors.New("must not enter evidence")
			if class == RouteStreamFailureRuntimeTransport {
				err = prepared.Dispatch.StreamFailed(failure)
			} else {
				err = prepared.Dispatch.StreamFailedAs(class, failure)
			}
			if !errors.Is(err, ErrDispatchTransport) {
				t.Fatalf("failure error=%v", err)
			}
			_ = prepared.Dispatch.StreamFailedAs(class, errors.New("duplicate"))
			start.Session.Cancel()

			events := sink.snapshot()
			if len(events) != 1 {
				t.Fatalf("events=%#v", events)
			}
			event := events[0]
			if !ValidRouteStreamFailure(event) || event.CauseClass != class || event.Revision != 1 ||
				event.StepIndex != 0 || event.Phase != RoutePhaseHandler ||
				event.InvocationStage != InvocationStageHandler || event.Action != extensionmanifest.RouteActionAdd ||
				event.Mode != extensionmanifest.RouteModeStream || event.RouteID != step.RouteID ||
				event.ContractVersion != step.ContractVersion || event.Method != http.MethodGet ||
				event.PathSignature != routeStepPathSignature(step) || event.FailureCode != RouteFailureTransportFailed ||
				!event.RuntimeExecutionObserved || event.ActorID != 42 || event.ResponseStatus != http.StatusOK ||
				event.CommitState != RouteCommitResponseStarted || event.Artifact != step.Provider.Artifact {
				t.Fatalf("event=%#v", event)
			}
			body, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			lower := strings.ToLower(string(body))
			for _, forbidden := range []string{"must not enter evidence", "requestbody", "responsebody", "rawerror", "headers", "query"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("payload-free incident contains %q: %s", forbidden, body)
				}
			}
		})
	}
}

func TestDispatcherRequiresExplicitStreamFailureSink(t *testing.T) {
	sink := &combinedRouteFailureSink{}
	legacyOnly := NewDispatcher(DispatcherConfig{Failures: sink})
	if legacyOnly.streamFailures != nil {
		t.Fatal("legacy failure sink was implicitly granted stream incident authority")
	}
	explicit := NewDispatcher(DispatcherConfig{Failures: sink, StreamFailures: sink})
	if explicit.streamFailures != sink {
		t.Fatal("explicit stream failure sink was not installed")
	}
}

func TestRouteStreamDispositionPreservesCauseAndFailsClosed(t *testing.T) {
	cause := errors.New("exact cause")
	incident := WithRouteStreamIncident(cause, RouteStreamFailureMissingTerminal)
	class, record, classified := routeStreamFailureDisposition(incident)
	if !classified || !record || class != RouteStreamFailureMissingTerminal || !errors.Is(incident, cause) {
		t.Fatalf("incident class=%q record=%t classified=%t error=%v", class, record, classified, incident)
	}
	abort := WithRouteStreamAbort(cause)
	class, record, classified = routeStreamFailureDisposition(abort)
	if !classified || record || class != "" || !errors.Is(abort, cause) {
		t.Fatalf("abort class=%q record=%t classified=%t error=%v", class, record, classified, abort)
	}
	class, record, classified = routeStreamFailureDisposition(WithRouteStreamIncident(cause, "forged"))
	if !classified || !record || class != RouteStreamFailureRuntimeTransport {
		t.Fatalf("invalid class suppressed incident: class=%q record=%t classified=%t", class, record, classified)
	}
}

func TestInspectRouteStreamFailureDispositionPreservesHostDecision(t *testing.T) {
	cause := errors.New("stream ended")
	for _, test := range []struct {
		name       string
		err        error
		wantClass  RouteStreamFailureClass
		wantRecord bool
		wantKnown  bool
	}{
		{name: "plain", err: cause},
		{name: "abort", err: WithRouteStreamAbort(cause), wantKnown: true},
		{name: "incident", err: WithRouteStreamIncident(cause, RouteStreamFailureMissingTerminal), wantClass: RouteStreamFailureMissingTerminal, wantRecord: true, wantKnown: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			class, record, known := InspectRouteStreamFailureDisposition(test.err)
			if class != test.wantClass || record != test.wantRecord || known != test.wantKnown || !errors.Is(test.err, cause) {
				t.Fatalf("class=%q record=%t known=%t error=%v", class, record, known, test.err)
			}
		})
	}
}

func TestRouteStreamMissingTerminalRecordsWrappedEOF(t *testing.T) {
	sink := &recordingRouteStreamFailureSink{}
	step := streamIncidentStep("stream.missing_terminal.eof")
	dispatcher := streamIncidentDispatcher(step, &streamIncidentInvoker{}, sink)
	prepared, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{
		Method: http.MethodGet, Path: "/incident",
	})
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	start, err := prepared.Dispatch.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepared.Dispatch.ResponseStarted()
	if err := prepared.Dispatch.StreamFailedAs(RouteStreamFailureMissingTerminal, io.EOF); !errors.Is(err, io.EOF) {
		t.Fatalf("missing terminal error=%v", err)
	}
	start.Session.Cancel()
	events := sink.snapshot()
	if len(events) != 1 || events[0].CauseClass != RouteStreamFailureMissingTerminal {
		t.Fatalf("events=%#v", events)
	}
}

func TestRouteStreamCompleteAndFailureRacePublishesAtMostOneIncident(t *testing.T) {
	for iteration := range 100 {
		sink := &recordingRouteStreamFailureSink{}
		step := streamIncidentStep("stream.terminal.race")
		dispatcher := streamIncidentDispatcher(step, &streamIncidentInvoker{}, sink)
		prepared, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{
			Method: http.MethodGet, Path: "/incident",
		})
		if err != nil || prepared.Dispatch == nil {
			t.Fatalf("iteration=%d prepared=%#v err=%v", iteration, prepared, err)
		}
		start, err := prepared.Dispatch.Open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		prepared.Dispatch.ResponseStarted()
		ready := make(chan struct{})
		var wait sync.WaitGroup
		for worker := range 64 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-ready
				if worker%2 == 0 {
					_ = prepared.Dispatch.Complete()
					return
				}
				_ = prepared.Dispatch.StreamFailed(errors.New("runtime failed"))
			}()
		}
		close(ready)
		wait.Wait()
		start.Session.Cancel()
		events := sink.snapshot()
		if len(events) > 1 {
			t.Fatalf("iteration=%d events=%#v", iteration, events)
		}
		if len(events) == 1 && events[0].CauseClass != RouteStreamFailureRuntimeTransport {
			t.Fatalf("iteration=%d event=%#v", iteration, events[0])
		}
	}
}

func TestRouteStreamOpenDispositionDoesNotHideConcurrentRuntimeCrash(t *testing.T) {
	tests := []struct {
		name      string
		open      func(context.Context, RouteInvocation) error
		wantClass RouteStreamFailureClass
		wantCount int
		wantCause error
	}{
		{name: "caller cancellation", open: func(ctx context.Context, input RouteInvocation) error {
			input.Commit.SideEffectStarted()
			<-ctx.Done()
			return ctx.Err()
		}, wantCount: 0},
		{name: "runtime crash wins over caller race", open: func(_ context.Context, input RouteInvocation) error {
			input.Commit.SideEffectStarted()
			return errors.New("runtime crashed")
		}, wantClass: RouteStreamFailureRuntimeTransport, wantCount: 1},
		{name: "Host abort", open: func(_ context.Context, input RouteInvocation) error {
			input.Commit.SideEffectStarted()
			return WithRouteStreamAbort(errStreamTestHostAbort)
		}, wantCount: 0, wantCause: errStreamTestHostAbort},
		{name: "Host budget", open: func(_ context.Context, input RouteInvocation) error {
			input.Commit.SideEffectStarted()
			return WithRouteStreamIncident(ErrRouteStreamBudgetExceeded, RouteStreamFailureHostBudget)
		}, wantClass: RouteStreamFailureHostBudget, wantCount: 1, wantCause: ErrRouteStreamBudgetExceeded},
		{name: "invalid preflight", open: func(_ context.Context, input RouteInvocation) error {
			input.Commit.SideEffectStarted()
			return WithRouteStreamIncident(errors.New("invalid preflight"), RouteStreamFailureInvalidPreflight)
		}, wantClass: RouteStreamFailureInvalidPreflight, wantCount: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := &recordingRouteStreamFailureSink{}
			step := streamIncidentStep("stream.open." + strings.ReplaceAll(test.name, " ", "_"))
			ctx, cancel := context.WithCancel(context.Background())
			invoker := &streamOpenFailureInvoker{open: test.open}
			if test.name == "caller cancellation" || test.name == "runtime crash wins over caller race" {
				invoker.before = cancel
			} else {
				defer cancel()
			}
			dispatcher := streamIncidentDispatcher(step, invoker, sink)
			prepared, err := dispatcher.PrepareStream(ctx, DispatchRequest{Method: http.MethodGet, Path: "/incident"})
			if err != nil || prepared.Dispatch == nil {
				t.Fatalf("prepared=%#v err=%v", prepared, err)
			}
			_, err = prepared.Dispatch.Open(ctx)
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("open error=%v", err)
			}
			events := sink.snapshot()
			if len(events) != test.wantCount {
				t.Fatalf("events=%#v error=%v", events, err)
			}
			if test.wantCount == 1 && events[0].CauseClass != test.wantClass {
				t.Fatalf("event=%#v", events[0])
			}
		})
	}
}

func TestRouteStreamOpenCustomCallerCauseDoesNotBecomeIncident(t *testing.T) {
	for _, test := range []struct {
		name      string
		open      func(context.Context, RouteInvocation, error) error
		wantCount int
		wantClass RouteStreamFailureClass
		wantCause error
	}{
		{name: "caller cancellation", open: func(ctx context.Context, input RouteInvocation, _ error) error {
			input.Commit.SideEffectStarted()
			<-ctx.Done()
			return context.Cause(ctx)
		}},
		{name: "independent runtime crash", open: func(_ context.Context, input RouteInvocation, runtimeErr error) error {
			input.Commit.SideEffectStarted()
			return runtimeErr
		}, wantCount: 1, wantClass: RouteStreamFailureRuntimeTransport},
		{name: "Host budget", open: func(_ context.Context, input RouteInvocation, _ error) error {
			input.Commit.SideEffectStarted()
			return WithRouteStreamIncident(ErrRouteStreamBudgetExceeded, RouteStreamFailureHostBudget)
		}, wantCount: 1, wantClass: RouteStreamFailureHostBudget, wantCause: ErrRouteStreamBudgetExceeded},
		{name: "Host abort", open: func(_ context.Context, input RouteInvocation, _ error) error {
			input.Commit.SideEffectStarted()
			return WithRouteStreamAbort(errStreamTestHostAbort)
		}, wantCause: errStreamTestHostAbort},
	} {
		t.Run(test.name, func(t *testing.T) {
			caller, cancelCaller := context.WithCancelCause(context.Background())
			callerCause := errors.New("caller disconnected")
			runtimeErr := errors.New("runtime crashed independently")
			invoker := &streamOpenFailureInvoker{
				before: func() { cancelCaller(callerCause) },
				open: func(ctx context.Context, input RouteInvocation) error {
					return test.open(ctx, input, runtimeErr)
				},
			}
			sink := &recordingRouteStreamFailureSink{}
			dispatcher := streamIncidentDispatcher(streamIncidentStep("stream.custom_caller."+strings.ReplaceAll(test.name, " ", "_")), invoker, sink)
			prepared, err := dispatcher.PrepareStream(caller, DispatchRequest{Method: http.MethodGet, Path: "/incident"})
			if err != nil || prepared.Dispatch == nil {
				t.Fatalf("prepared=%#v err=%v", prepared, err)
			}
			_, err = prepared.Dispatch.Open(caller)
			wantCause := test.wantCause
			if wantCause == nil && test.wantCount == 0 {
				wantCause = callerCause
			}
			if test.name == "independent runtime crash" {
				wantCause = runtimeErr
			}
			if wantCause != nil && (!errors.Is(err, wantCause) || !errors.Is(err, ErrDispatchTransport)) {
				t.Fatalf("error=%v want cause=%v", err, wantCause)
			}
			events := sink.snapshot()
			if len(events) != test.wantCount {
				t.Fatalf("events=%#v error=%v", events, err)
			}
			if test.wantCount == 1 && events[0].CauseClass != test.wantClass {
				t.Fatalf("event=%#v", events[0])
			}
		})
	}
}

func TestRouteStreamFailureSinkExcludesHostAndCallerAbortions(t *testing.T) {
	for _, test := range []struct {
		name string
		fail func(*RouteStreamDispatch) error
	}{
		{name: "caller cancellation", fail: func(dispatch *RouteStreamDispatch) error {
			return dispatch.StreamFailed(context.Canceled)
		}},
		{name: "Host writer", fail: func(dispatch *RouteStreamDispatch) error {
			return dispatch.StreamAborted(io.ErrClosedPipe)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &recordingRouteStreamFailureSink{}
			step := streamIncidentStep("stream.abort." + strings.ReplaceAll(test.name, " ", "_"))
			dispatcher := streamIncidentDispatcher(step, &streamIncidentInvoker{}, sink)
			prepared, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{
				Method: http.MethodGet, Path: "/incident",
			})
			if err != nil || prepared.Dispatch == nil {
				t.Fatalf("prepared=%#v err=%v", prepared, err)
			}
			start, err := prepared.Dispatch.Open(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			prepared.Dispatch.ResponseStarted()
			if err := test.fail(prepared.Dispatch); !errors.Is(err, ErrDispatchTransport) {
				t.Fatalf("abort error=%v", err)
			}
			start.Session.Cancel()
			if events := sink.snapshot(); len(events) != 0 {
				t.Fatalf("abort recorded incidents=%#v", events)
			}
		})
	}
}

func TestRouteStreamOpenRecordsInvalidPreflightAndBudgetAfterExecution(t *testing.T) {
	t.Run("invalid preflight", func(t *testing.T) {
		sink := &recordingRouteStreamFailureSink{}
		step := streamIncidentStep("stream.invalid_preflight")
		invoker := &streamIncidentInvoker{body: []byte("invalid")}
		dispatcher := streamIncidentDispatcher(step, invoker, sink)
		prepared, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{
			Method: http.MethodGet, Path: "/incident",
		})
		if err != nil || prepared.Dispatch == nil {
			t.Fatalf("prepared=%#v err=%v", prepared, err)
		}
		if _, err := prepared.Dispatch.Open(context.Background()); !errors.Is(err, ErrDispatchTransport) {
			t.Fatalf("open error=%v", err)
		}
		events := sink.snapshot()
		if len(events) != 1 || events[0].CauseClass != RouteStreamFailureInvalidPreflight ||
			events[0].CommitState != RouteCommitSideEffectStarted {
			t.Fatalf("events=%#v", events)
		}
	})

	t.Run("Host budget", func(t *testing.T) {
		sink := &recordingRouteStreamFailureSink{}
		step := streamIncidentStep("stream.host_budget")
		step.TimeoutMS = 5
		dispatcher := streamIncidentDispatcher(step, &streamBudgetIncidentInvoker{}, sink)
		prepared, err := dispatcher.PrepareStream(context.Background(), DispatchRequest{
			Method: http.MethodGet, Path: "/incident",
		})
		if err != nil || prepared.Dispatch == nil {
			t.Fatalf("prepared=%#v err=%v", prepared, err)
		}
		if _, err := prepared.Dispatch.Open(context.Background()); !errors.Is(err, ErrDispatchTransport) || !errors.Is(err, ErrRouteStreamBudgetExceeded) {
			t.Fatalf("open error=%v", err)
		}
		events := sink.snapshot()
		if len(events) != 1 || events[0].CauseClass != RouteStreamFailureHostBudget ||
			events[0].CommitState != RouteCommitSideEffectStarted {
			t.Fatalf("events=%#v", events)
		}
	})
}

func streamIncidentStep(id string) RouteExecutionStep {
	step := dispatchPluginStep(RoutePhaseHandler, id, extensionmanifest.RouteActionAdd)
	step.Mode = extensionmanifest.RouteModeStream
	step.Path = "/incident"
	return step
}

func streamIncidentDispatcher(
	step RouteExecutionStep,
	invoker StreamingStepInvoker,
	sink RouteStreamFailureSink,
) *Dispatcher {
	return NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan(
			http.MethodGet, "/incident", nil, []RouteExecutionStep{step}, 0,
		)},
		Guard: allowStreamGuard{}, Steps: streamIncidentStepInvoker{StreamingStepInvoker: invoker},
		StreamFailures: sink,
	})
}

type streamIncidentStepInvoker struct{ StreamingStepInvoker }

func (streamIncidentStepInvoker) SupportsMode(string) bool { return false }
func (streamIncidentStepInvoker) Invoke(context.Context, RouteInvocation) (RouteInvocationResult, error) {
	return RouteInvocationResult{}, ErrDispatchTransport
}

type streamIncidentInvoker struct{ body []byte }

func (i *streamIncidentInvoker) OpenStream(_ context.Context, input RouteInvocation) (RouteStreamStart, error) {
	input.Commit.SideEffectStarted()
	return RouteStreamStart{
		Response: DispatchResponse{Status: http.StatusOK, Body: append([]byte(nil), i.body...)},
		Session:  authorityStreamSession{},
	}, nil
}

type streamBudgetIncidentInvoker struct{}

func (*streamBudgetIncidentInvoker) OpenStream(ctx context.Context, input RouteInvocation) (RouteStreamStart, error) {
	input.Commit.SideEffectStarted()
	<-ctx.Done()
	return RouteStreamStart{}, context.Cause(ctx)
}

var errStreamTestHostAbort = errors.New("Host aborted stream")

type streamOpenFailureInvoker struct {
	before func()
	open   func(context.Context, RouteInvocation) error
}

func (i *streamOpenFailureInvoker) OpenStream(ctx context.Context, input RouteInvocation) (RouteStreamStart, error) {
	if i.before != nil {
		i.before()
	}
	return RouteStreamStart{}, i.open(ctx, input)
}

type recordingRouteStreamFailureSink struct {
	mu     sync.Mutex
	events []RouteStreamFailure
}

func (s *recordingRouteStreamFailureSink) RecordStreamFailure(_ context.Context, event RouteStreamFailure) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *recordingRouteStreamFailureSink) snapshot() []RouteStreamFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RouteStreamFailure(nil), s.events...)
}

var _ RouteStreamFailureSink = (*recordingRouteStreamFailureSink)(nil)

type combinedRouteFailureSink struct{}

func (*combinedRouteFailureSink) RecordCommittedAfterFailure(context.Context, RouteCommittedAfterFailure) {
}
func (*combinedRouteFailureSink) RecordStreamFailure(context.Context, RouteStreamFailure) {}

var _ RouteFailureSink = (*combinedRouteFailureSink)(nil)
var _ RouteStreamFailureSink = (*combinedRouteFailureSink)(nil)
