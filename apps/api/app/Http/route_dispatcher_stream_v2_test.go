package http

import (
	"context"
	"errors"
	"io"
	stdhttp "net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteV2StreamPreAdmissionCancellationStaysPristine(t *testing.T) {
	dispatcher, runtime, traces := routeV2StreamTestDispatcher(t)
	prepared, err := dispatcher.PrepareStream(
		context.Background(), routes.DispatchRequest{Method: stdhttp.MethodGet, Path: "/stream-v2"},
	)
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v error=%v", prepared, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = prepared.Dispatch.Open(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, routes.ErrDispatchTransport) ||
		runtime.calls != 0 || runtime.streamOpenCalls != 0 || len(traces.RouteTraces(0)) != 0 {
		t.Fatalf("error=%v routeCalls=%d streamCalls=%d traces=%#v",
			err, runtime.calls, runtime.streamOpenCalls, traces.RouteTraces(0))
	}
}

func TestRouteV2StreamPreflightFailureRecordsObservedExecution(t *testing.T) {
	sink := &routeV2RecordingStreamFailureSink{}
	dispatcher, runtime, traces := routeV2StreamTestDispatcherWithSink(t, sink)
	runtime.err = errors.New("runtime crashed after preflight dispatch")
	prepared, err := dispatcher.PrepareStream(
		context.Background(), routes.DispatchRequest{Method: stdhttp.MethodGet, Path: "/stream-v2"},
	)
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v error=%v", prepared, err)
	}

	_, err = prepared.Dispatch.Open(context.Background())
	records := traces.RouteTraces(0)
	if !errors.Is(err, routes.ErrDispatchTransport) || runtime.calls != 1 || runtime.streamOpenCalls != 0 ||
		len(records) != 1 || records[0].Outcome != routes.RouteTraceTransportFailed ||
		records[0].CommitState != routes.RouteCommitSideEffectStarted ||
		len(sink.snapshot()) != 1 || sink.snapshot()[0].CauseClass != routes.RouteStreamFailureRuntimeTransport {
		t.Fatalf("error=%v routeCalls=%d streamCalls=%d traces=%#v", err, runtime.calls, runtime.streamOpenCalls, records)
	}
}

func TestRouteV2StreamSharesCorrelationAndPreservesExactQuery(t *testing.T) {
	dispatcher, runtime, _ := routeV2StreamTestDispatcher(t)
	const rawQuery = "tag=one&tag=&tag=a%2Bb&page=2"
	open := func() string {
		prepared, err := dispatcher.PrepareStream(context.Background(), routes.DispatchRequest{
			Method: stdhttp.MethodGet, Path: "/stream-v2", Query: rawQuery,
		})
		if err != nil || prepared.Dispatch == nil {
			t.Fatalf("prepared=%#v error=%v", prepared, err)
		}
		if _, err := prepared.Dispatch.Open(context.Background()); !errors.Is(err, routes.ErrDispatchTransport) {
			t.Fatalf("open error=%v", err)
		}
		if runtime.request.CorrelationID == "" || runtime.request.CorrelationID != runtime.streamRequest.CorrelationID {
			t.Fatalf("correlation preflight=%q stream=%q", runtime.request.CorrelationID, runtime.streamRequest.CorrelationID)
		}
		if !strings.HasPrefix(runtime.request.CorrelationID, "route_") ||
			runtime.request.QueryParameters["tag"] != "one" ||
			!reflect.DeepEqual(runtime.request.QueryParameterValues["tag"], []string{"one", "", "a+b"}) ||
			runtime.streamRequest.Path != "/stream-v2?"+rawQuery {
			t.Fatalf("preflight=%#v stream=%#v", runtime.request, runtime.streamRequest)
		}
		return runtime.request.CorrelationID
	}
	first := open()
	if second := open(); second == first {
		t.Fatalf("separate stream admissions reused correlation %q", first)
	}
}

func TestRouteV2StreamRejectsNonUpgradeInformationalPreflight(t *testing.T) {
	for _, status := range []int{stdhttp.StatusContinue, stdhttp.StatusEarlyHints, stdhttp.StatusOK} {
		t.Run(stdhttp.StatusText(status), func(t *testing.T) {
			sink := &routeV2RecordingStreamFailureSink{}
			dispatcher, runtime, _ := routeV2StreamTestDispatcherWithSink(t, sink)
			runtime.response = extensionsruntime.ProtocolV2RouteResponse{StatusCode: status, StreamFollows: true}
			prepared, err := dispatcher.PrepareStream(context.Background(), routes.DispatchRequest{
				Method: stdhttp.MethodGet, Path: "/stream-v2",
			})
			if err != nil || prepared.Dispatch == nil {
				t.Fatalf("prepared=%#v error=%v", prepared, err)
			}
			if _, err := prepared.Dispatch.Open(context.Background()); !errors.Is(err, routes.ErrDispatchTransport) {
				t.Fatalf("status=%d error=%v", status, err)
			}
			if runtime.calls != 1 || runtime.streamOpenCalls != 0 {
				t.Fatalf("status=%d routeCalls=%d streamCalls=%d", status, runtime.calls, runtime.streamOpenCalls)
			}
			events := sink.snapshot()
			if len(events) != 1 || events[0].CauseClass != routes.RouteStreamFailureInvalidPreflight {
				t.Fatalf("status=%d events=%#v", status, events)
			}
		})
	}
}

func routeV2StreamTestDispatcher(
	t *testing.T,
) (*routes.Dispatcher, *routeDispatcherV2StreamRuntime, *routes.RouteTraceRing) {
	return routeV2StreamTestDispatcherWithSink(t, nil)
}

func routeV2StreamTestDispatcherWithSink(
	t *testing.T,
	sink routes.RouteStreamFailureSink,
) (*routes.Dispatcher, *routeDispatcherV2StreamRuntime, *routes.RouteTraceRing) {
	t.Helper()
	artifact := routeDispatcherArtifact("stream-v2", 'a')
	declaration := routeDispatcherManifestRoute(
		"stream-v2.route", extensionmanifest.RouteActionAdd, "/stream-v2", stdhttp.MethodGet,
	)
	declaration.Mode = extensionmanifest.RouteModeWebSocket
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	runtime := &routeDispatcherV2StreamRuntime{routeDispatcherV2Runtime: newRouteDispatcherV2RuntimeForArtifact(t, artifact)}
	runtime.response = extensionsruntime.ProtocolV2RouteResponse{
		StatusCode: fiber.StatusSwitchingProtocols, StreamFollows: true,
	}
	traces := routes.NewRouteTraceRing(8)
	return routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
		Guard: HostRouteGuardAuthorizer{}, Trace: traces, StreamFailures: sink,
	}), runtime, traces
}

type routeDispatcherV2StreamRuntime struct {
	*routeDispatcherV2Runtime
	streamOpenCalls int
	streamRequest   extensionsruntime.ProtocolV2RouteStreamRequest
}

func (r *routeDispatcherV2StreamRuntime) OpenRouteStreamInstance(
	_ context.Context,
	_ extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteStreamRequest,
) (*extensionsruntime.ProtocolV2RouteStream, error) {
	r.streamOpenCalls++
	r.streamRequest = request
	return nil, errors.New("stream open is not expected")
}

func TestRouteV2StreamSessionCopiesChunksAndAcceptsBoundTerminal(t *testing.T) {
	wire := &fakeRouteV2WireStream{
		chunks: []extensionsruntime.ProtocolV2RouteStreamChunk{{Sequence: 1, Data: []byte("first"), Final: true}},
		response: extensionsruntime.ProtocolV2RouteStreamResponse{
			StatusCode: stdhttp.StatusCreated,
			Headers:    stdhttp.Header{"X-Result": {"done"}, "Set-Cookie": {"private=1"}},
		},
	}
	session := &routeV2StreamSession{stream: wire, expectedStatus: stdhttp.StatusCreated}
	if err := session.Send([]byte("request"), true); err != nil || err == nil && string(wire.sent) != "request" {
		t.Fatalf("send=%q err=%v", wire.sent, err)
	}
	if err := session.CloseRequest(); err != nil || !wire.requestClosed {
		t.Fatalf("request closed=%t err=%v", wire.requestClosed, err)
	}
	chunk, err := session.Recv()
	if err != nil || chunk.Sequence != 1 || string(chunk.Data) != "first" || !chunk.Final {
		t.Fatalf("chunk=%#v err=%v", chunk, err)
	}
	chunk.Data[0] = 'x'
	if _, err := session.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal err=%v", err)
	}
	response, ok := session.Response()
	if !ok || response.Status != stdhttp.StatusCreated || response.Headers.Get("X-Result") != "done" || response.Headers.Get("Set-Cookie") != "" {
		t.Fatalf("response=%#v ok=%t", response, ok)
	}
	if wire.cancelled {
		t.Fatal("successful terminal cancelled the runtime stream")
	}
}

func TestRouteV2StreamSessionCancelsOnTransportFailureOrStatusDrift(t *testing.T) {
	for name, wire := range map[string]*fakeRouteV2WireStream{
		"transport": {recvErr: errors.New("closed")},
		"status": {
			response: extensionsruntime.ProtocolV2RouteStreamResponse{StatusCode: stdhttp.StatusNoContent},
		},
	} {
		t.Run(name, func(t *testing.T) {
			session := &routeV2StreamSession{stream: wire, expectedStatus: stdhttp.StatusOK}
			_, err := session.Recv()
			if err == nil || !wire.cancelled {
				t.Fatalf("error=%v cancelled=%t", err, wire.cancelled)
			}
			if _, ok := session.Response(); ok {
				t.Fatal("failed stream exposed a terminal response")
			}
		})
	}
}

func TestRouteV2StreamSessionCapturesForceCancelCauseBeforeLeaseRelease(t *testing.T) {
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: "stream.force", InstanceID: "instance-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := gate.Acquire(context.Background(), extensionsruntime.RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	wire := &fakeRouteV2WireStream{blockRecv: make(chan struct{})}
	session := newRouteV2StreamSession(wire, lease, stdhttp.StatusOK)
	session.arm(lease.Context)
	forceCause := errors.New("trust revoked")
	gate.ForceCancel(forceCause)
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("ForceCancel did not finish the stream session")
	}
	if !errors.Is(session.Cause(), extensionsruntime.ErrRuntimeAdmissionForced) || !errors.Is(session.Cause(), forceCause) {
		t.Fatalf("cause=%v want force cause", session.Cause())
	}
	if !wire.cancelled {
		t.Fatal("ForceCancel did not cancel the wire stream")
	}
	if _, ok := session.Response(); ok {
		t.Fatal("ForceCancel published a terminal Response")
	}
	// Lease release is single-flight; a second ForceCancel path must not panic.
	session.Cancel()
	if !errors.Is(session.Cause(), extensionsruntime.ErrRuntimeAdmissionForced) || !errors.Is(session.Cause(), forceCause) {
		t.Fatalf("second cancel overwrote cause=%v", session.Cause())
	}
	for operation, err := range map[string]error{
		"send":  session.Send([]byte("late"), false),
		"close": session.CloseRequest(),
		"recv":  func() error { _, err := session.Recv(); return err }(),
	} {
		if !errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced) || !errors.Is(err, forceCause) {
			t.Fatalf("%s error=%v", operation, err)
		}
	}
}

func TestRouteV2StreamSessionKeepsRuntimeCrashAcrossCancelRace(t *testing.T) {
	for _, operation := range []string{"send", "close", "recv"} {
		t.Run(operation, func(t *testing.T) {
			for iteration := range 100 {
				runtimeErr := errors.New("runtime crashed")
				started := make(chan struct{})
				release := make(chan struct{})
				wire := routeV2BlockingFailureWire(operation, runtimeErr, started, release)
				session := newRouteV2StreamSession(wire, nil, stdhttp.StatusOK)
				ctx, cancel := context.WithCancelCause(context.Background())
				session.arm(ctx)
				result := make(chan error, 1)
				go func() { result <- invokeRouteV2StreamOperation(session, operation) }()
				<-started
				cancel(errors.New("caller left"))
				close(release)
				if err := <-result; !errors.Is(err, runtimeErr) {
					t.Fatalf("iteration=%d error=%v", iteration, err)
				}
			}
		})
	}
}

func TestRouteV2StreamSessionHostWinnerOverridesWireTeardownFailure(t *testing.T) {
	forceCause := errors.New("trust revoked")
	for _, winner := range []struct {
		name  string
		cause error
		want  error
	}{
		{name: "ForceDrain", cause: errors.Join(extensionsruntime.ErrRuntimeAdmissionForced, forceCause), want: extensionsruntime.ErrRuntimeAdmissionForced},
		{name: "Host budget", cause: routes.ErrRouteStreamBudgetExceeded, want: routes.ErrRouteStreamBudgetExceeded},
	} {
		for _, operation := range []string{"send", "close", "recv"} {
			t.Run(winner.name+"/"+operation, func(t *testing.T) {
				wireErr := errors.New("wire teardown returned unavailable")
				started := make(chan struct{})
				release := make(chan struct{})
				wire := routeV2BlockingFailureWire(operation, wireErr, started, release)
				session := newRouteV2StreamSession(wire, nil, stdhttp.StatusOK)
				result := make(chan error, 1)
				go func() { result <- invokeRouteV2StreamOperation(session, operation) }()
				<-started
				session.CancelWithCause(winner.cause)
				close(release)
				err := <-result
				if !errors.Is(err, winner.want) || errors.Is(err, wireErr) {
					t.Fatalf("error=%v winner=%v", err, winner.cause)
				}
			})
		}
	}
}

func routeV2BlockingFailureWire(operation string, failure error, started, release chan struct{}) *fakeRouteV2WireStream {
	wire := &fakeRouteV2WireStream{}
	switch operation {
	case "send":
		wire.sendErr, wire.sendStarted, wire.sendRelease = failure, started, release
	case "close":
		wire.closeErr, wire.closeStarted, wire.closeRelease = failure, started, release
	case "recv":
		wire.recvErr, wire.recvStarted, wire.recvRelease = failure, started, release
	}
	return wire
}

func invokeRouteV2StreamOperation(session *routeV2StreamSession, operation string) error {
	switch operation {
	case "send":
		return session.Send([]byte("request"), false)
	case "close":
		return session.CloseRequest()
	case "recv":
		_, err := session.Recv()
		return err
	default:
		return errors.New("unknown test operation")
	}
}

func TestClassifyRouteV2StreamErrorPreservesStableCauses(t *testing.T) {
	forceCause := errors.New("trust revoked")
	force := errors.Join(extensionsruntime.ErrRuntimeAdmissionForced, forceCause)
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "ForceDrain", err: force, want: forceCause},
		{name: "budget", err: routes.ErrRouteStreamBudgetExceeded, want: routes.ErrRouteStreamBudgetExceeded},
		{name: "invalid preflight", err: extensionsruntime.ErrProtocolV2RouteResponseInvalid, want: extensionsruntime.ErrProtocolV2RouteResponseInvalid},
		{name: "missing terminal", err: extensionsruntime.ErrProtocolV2RouteStreamMissingTerminal, want: extensionsruntime.ErrProtocolV2RouteStreamMissingTerminal},
		{name: "caller", err: context.Canceled, want: context.Canceled},
		{name: "runtime", err: ErrRouteRuntimeTarget, want: ErrRouteRuntimeTarget},
	} {
		t.Run(test.name, func(t *testing.T) {
			classified := classifyRouteV2StreamError(test.err)
			if !errors.Is(classified, test.want) {
				t.Fatalf("classified=%v", classified)
			}
		})
	}
	if classified := classifyRouteV2StreamError(force); !errors.Is(classified, extensionsruntime.ErrRuntimeAdmissionForced) {
		t.Fatalf("ForceDrain marker lost: %v", classified)
	}
}

func TestRouteV2StreamSessionTerminalAndCancelRaceIsAtomic(t *testing.T) {
	for i := 0; i < 64; i++ {
		wire := &fakeRouteV2WireStream{
			response: extensionsruntime.ProtocolV2RouteStreamResponse{StatusCode: stdhttp.StatusOK},
		}
		session := newRouteV2StreamSession(wire, nil, stdhttp.StatusOK)
		var wg sync.WaitGroup
		var sawEOF, sawCancel atomic.Bool
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := session.Recv()
			if errors.Is(err, io.EOF) {
				sawEOF.Store(true)
			} else if err != nil {
				sawCancel.Store(true)
			}
		}()
		go func() {
			defer wg.Done()
			session.Cancel()
		}()
		wg.Wait()
		response, ok := session.Response()
		if sawEOF.Load() {
			if !ok || response.Status != stdhttp.StatusOK || wire.cancelled {
				t.Fatalf("terminal winner invalid: ok=%t response=%#v cancelled=%t", ok, response, wire.cancelled)
			}
		} else {
			if ok {
				t.Fatal("cancel winner published Response")
			}
			if !wire.cancelled {
				t.Fatal("cancel winner did not cancel wire")
			}
		}
		select {
		case <-session.Done():
		default:
			t.Fatal("Done not closed after terminal/cancel race")
		}
	}
}

func TestRouteV2StreamSessionAbsorbsCloseErrorOnlyAfterExpectedTerminal(t *testing.T) {
	closeErr := errors.New("request stream already closed")
	for _, test := range []struct {
		name     string
		response extensionsruntime.ProtocolV2RouteStreamResponse
		wantErr  bool
	}{
		{name: "expected terminal", response: extensionsruntime.ProtocolV2RouteStreamResponse{StatusCode: stdhttp.StatusSwitchingProtocols}},
		{name: "status drift", response: extensionsruntime.ProtocolV2RouteStreamResponse{StatusCode: stdhttp.StatusOK}, wantErr: true},
		{name: "missing terminal", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := &fakeRouteV2WireStream{response: test.response, closeErr: closeErr}
			session := &routeV2StreamSession{stream: wire, expectedStatus: stdhttp.StatusSwitchingProtocols}
			err := session.CloseRequest()
			if !wire.requestClosed || test.wantErr != errors.Is(err, closeErr) {
				t.Fatalf("requestClosed=%t error=%v", wire.requestClosed, err)
			}
		})
	}
}

type fakeRouteV2WireStream struct {
	chunks        []extensionsruntime.ProtocolV2RouteStreamChunk
	response      extensionsruntime.ProtocolV2RouteStreamResponse
	recvErr       error
	sent          []byte
	requestClosed bool
	cancelled     bool
	closeErr      error
	closeStarted  chan struct{}
	closeRelease  chan struct{}
	blockRecv     chan struct{}
	recvStarted   chan struct{}
	recvRelease   chan struct{}
	sendErr       error
	sendStarted   chan struct{}
	sendRelease   chan struct{}
}

func (s *fakeRouteV2WireStream) Send(data []byte, _ bool) error {
	s.sent = append([]byte(nil), data...)
	if s.sendStarted != nil {
		close(s.sendStarted)
	}
	if s.sendRelease != nil {
		<-s.sendRelease
	}
	return s.sendErr
}

func (s *fakeRouteV2WireStream) CloseRequest() error {
	s.requestClosed = true
	if s.closeStarted != nil {
		close(s.closeStarted)
	}
	if s.closeRelease != nil {
		<-s.closeRelease
	}
	return s.closeErr
}

func (s *fakeRouteV2WireStream) Recv() (extensionsruntime.ProtocolV2RouteStreamChunk, error) {
	if s.recvStarted != nil {
		close(s.recvStarted)
	}
	if s.recvRelease != nil {
		<-s.recvRelease
	}
	if s.blockRecv != nil {
		<-s.blockRecv
	}
	if s.recvErr != nil {
		return extensionsruntime.ProtocolV2RouteStreamChunk{}, s.recvErr
	}
	if len(s.chunks) == 0 {
		return extensionsruntime.ProtocolV2RouteStreamChunk{}, io.EOF
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	return chunk, nil
}

func (s *fakeRouteV2WireStream) Response() (extensionsruntime.ProtocolV2RouteStreamResponse, bool) {
	return s.response, s.response.StatusCode != 0
}

func (s *fakeRouteV2WireStream) Cancel() { s.cancelled = true }

var _ routeV2WireStream = (*fakeRouteV2WireStream)(nil)

type routeV2RecordingStreamFailureSink struct {
	mu     sync.Mutex
	events []routes.RouteStreamFailure
}

func (s *routeV2RecordingStreamFailureSink) RecordStreamFailure(_ context.Context, event routes.RouteStreamFailure) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *routeV2RecordingStreamFailureSink) snapshot() []routes.RouteStreamFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]routes.RouteStreamFailure(nil), s.events...)
}

var _ routes.RouteStreamFailureSink = (*routeV2RecordingStreamFailureSink)(nil)
