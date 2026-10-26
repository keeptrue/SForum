package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteDispatcherStreamsSSEThroughFiberAndCommitsTrace(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.http", 'a')
	declaration := routeDispatcherManifestRoute("stream.http.events", extensionmanifest.RouteActionAdd, "/api/v1/events", "GET")
	declaration.Mode = extensionmanifest.RouteModeSSE
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	session := &streamHTTPTestSession{
		chunks: []routes.RouteStreamChunk{
			{Sequence: 1, Data: []byte("data: one\n\n")},
			{Sequence: 2, Data: []byte("data: two\n\n"), Final: true},
		},
		response: routes.DispatchResponse{Status: stdhttp.StatusOK, Headers: stdhttp.Header{"Content-Type": {"text/event-stream"}}},
	}
	traces := routes.NewRouteTraceRing(8)
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: &streamHTTPTestInvoker{
			start: routes.RouteStreamStart{
				Response: routes.DispatchResponse{Status: stdhttp.StatusOK, Headers: stdhttp.Header{"Content-Type": {"text/event-stream"}}},
				Session:  session,
			},
		},
		Guard: HostRouteGuardAuthorizer{}, Trace: traces,
	})
	app := fiber.New(fiber.Config{StreamRequestBody: true, DisablePreParseMultipartForm: true})
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/events", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	// Successful streams still Cancel after Complete so Host budget/lease release.
	if response.StatusCode != stdhttp.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" ||
		string(body) != "data: one\n\ndata: two\n\n" || !session.requestClosed || !session.cancelled {
		t.Fatalf("status=%d headers=%v body=%q session=%#v", response.StatusCode, response.Header, body, session)
	}
	records := traces.RouteTraces(8)
	if len(records) != 2 || records[0].Outcome != routes.RouteTraceSucceeded || records[1].Outcome != routes.RouteTraceCommitted {
		t.Fatalf("traces=%#v", records)
	}
}

func TestRouteDispatcherClassifiesInvalidSSEPreflight(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.sse.invalid", 'e')
	declaration := routeDispatcherManifestRoute(
		"stream.sse.invalid.events", extensionmanifest.RouteActionAdd, "/api/v1/invalid-events", stdhttp.MethodGet,
	)
	declaration.Mode = extensionmanifest.RouteModeSSE
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	session := &streamHTTPTestSession{}
	sink := &routeV2RecordingStreamFailureSink{}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry},
		Steps: &streamHTTPTestInvoker{observe: true, start: routes.RouteStreamStart{
			Response: routes.DispatchResponse{
				Status: stdhttp.StatusOK, Headers: stdhttp.Header{"Content-Type": {"application/json"}},
			},
			Session: session,
		}},
		Guard: HostRouteGuardAuthorizer{}, StreamFailures: sink,
	})
	app := fiber.New(fiber.Config{StreamRequestBody: true})
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/invalid-events", nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != fiber.StatusBadGateway || !session.cancelled {
		t.Fatalf("status=%d cancelled=%t", response.StatusCode, session.cancelled)
	}
	events := sink.snapshot()
	if len(events) != 1 || events[0].CauseClass != routes.RouteStreamFailureInvalidPreflight {
		t.Fatalf("events=%#v", events)
	}
}

func TestPumpRouteStreamRequestUsesBoundedChunks(t *testing.T) {
	body := bytes.Repeat([]byte("x"), extensionsruntime.MaxProtocolV2RouteChunkSize+17)
	session := &streamHTTPTestSession{}
	if err := pumpRouteStreamRequest(bytes.NewReader(body), session); err != nil {
		t.Fatal(err)
	}
	if !session.requestClosed || session.maxRequestChunk != extensionsruntime.MaxProtocolV2RouteChunkSize || session.requestBytes != len(body) || len(session.requestChunks) != 2 {
		t.Fatalf("closed=%t max=%d bytes=%d chunks=%v", session.requestClosed, session.maxRequestChunk, session.requestBytes, session.requestChunks)
	}
}

func TestPumpRouteStreamRequestClassifiesCallerAndRuntimeFailures(t *testing.T) {
	callerErr := errors.New("caller request body failed")
	runtimeErr := errors.New("runtime request stream failed")
	for _, test := range []struct {
		name      string
		reader    io.Reader
		session   *streamHTTPTestSession
		wantCount int
	}{
		{name: "caller reader", reader: routeStreamErrorReader{err: callerErr}, session: &streamHTTPTestSession{}},
		{name: "runtime send", reader: bytes.NewReader([]byte("body")), session: &streamHTTPTestSession{sendErr: runtimeErr}, wantCount: 1},
		{name: "runtime close", reader: bytes.NewReader(nil), session: &streamHTTPTestSession{closeRequestErr: runtimeErr}, wantCount: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatch, session, sink := prepareHTTPStreamAdapterTest(t, test.session)
			err := pumpRouteStreamRequest(test.reader, session)
			if err == nil {
				t.Fatal("pump unexpectedly succeeded")
			}
			_ = dispatch.StreamFailed(err)
			events := sink.snapshot()
			if len(events) != test.wantCount {
				t.Fatalf("events=%#v", events)
			}
			if test.wantCount == 1 && events[0].CauseClass != routes.RouteStreamFailureRuntimeTransport {
				t.Fatalf("class=%q", events[0].CauseClass)
			}
		})
	}
}

func TestStreamRouteResponseCancelsOnRuntimeFailure(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.failure", 'b')
	declaration := routeDispatcherManifestRoute("stream.failure.body", extensionmanifest.RouteActionAdd, "/failure", "GET")
	declaration.Mode = extensionmanifest.RouteModeStream
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration}}}}); err != nil {
		t.Fatal(err)
	}
	session := &streamHTTPTestSession{recvErr: errors.New("runtime crashed")}
	traces := routes.NewRouteTraceRing(8)
	sink := &routeV2RecordingStreamFailureSink{}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: &streamHTTPTestInvoker{start: routes.RouteStreamStart{
			Response: routes.DispatchResponse{Status: stdhttp.StatusOK}, Session: session,
		}}, Guard: HostRouteGuardAuthorizer{}, Trace: traces, StreamFailures: sink,
	})
	prepared, err := dispatcher.PrepareStream(context.Background(), routes.DispatchRequest{Method: "GET", Path: "/failure"})
	if err != nil {
		t.Fatal(err)
	}
	start, err := prepared.Dispatch.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepared.Dispatch.ResponseStarted()
	streamRouteResponse(bufio.NewWriter(bytes.NewBuffer(nil)), start.Session, prepared.Dispatch)
	if !session.cancelled {
		t.Fatal("failed stream was not cancelled")
	}
	records := traces.RouteTraces(8)
	if len(records) != 2 || records[1].Outcome != routes.RouteTraceTransportFailed {
		t.Fatalf("traces=%#v", records)
	}
	events := sink.snapshot()
	if len(events) != 1 || events[0].CauseClass != routes.RouteStreamFailureRuntimeTransport {
		t.Fatalf("events=%#v", events)
	}
}

func TestStreamRouteResponseClassifiesMissingTerminal(t *testing.T) {
	session := &streamHTTPTestSession{recvErr: io.EOF}
	dispatch, bound, sink := prepareHTTPStreamAdapterTest(t, session)
	dispatch.ResponseStarted()
	streamRouteResponse(bufio.NewWriter(io.Discard), bound, dispatch)
	events := sink.snapshot()
	if len(events) != 1 || events[0].CauseClass != routes.RouteStreamFailureMissingTerminal {
		t.Fatalf("events=%#v", events)
	}
}

func TestStreamRouteResponseHostWriterFailuresDoNotRecordIncidents(t *testing.T) {
	for _, test := range []struct {
		name       string
		bufferSize int
		payload    []byte
	}{
		{name: "write", bufferSize: 1, payload: []byte("xx")},
		{name: "flush", bufferSize: 4096, payload: []byte("x")},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &streamHTTPTestSession{chunks: []routes.RouteStreamChunk{{Data: test.payload}}}
			dispatch, bound, sink := prepareHTTPStreamAdapterTest(t, session)
			dispatch.ResponseStarted()
			streamRouteResponse(bufio.NewWriterSize(routeStreamErrorWriter{err: io.ErrClosedPipe}, test.bufferSize), bound, dispatch)
			if events := sink.snapshot(); len(events) != 0 {
				t.Fatalf("events=%#v", events)
			}
		})
	}
}

func TestStreamRouteResponsePublishesFailTraceBeforeLifetimeDone(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.fail-order", 'f')
	declaration := routeDispatcherManifestRoute("stream.fail-order.body", extensionmanifest.RouteActionAdd, "/fail-order", "GET")
	declaration.Mode = extensionmanifest.RouteModeStream
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration}}}}); err != nil {
		t.Fatal(err)
	}
	inner := &streamHTTPTestSession{recvErr: errors.New("runtime crashed")}
	traces := routes.NewRouteTraceRing(8)
	probe := &streamLifetimeOrderProbe{inner: traces}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: &streamHTTPTestInvoker{start: routes.RouteStreamStart{
			Response: routes.DispatchResponse{Status: stdhttp.StatusOK}, Session: inner,
		}}, Guard: HostRouteGuardAuthorizer{}, Trace: probe,
	})
	prepared, err := dispatcher.PrepareStream(context.Background(), routes.DispatchRequest{Method: "GET", Path: "/fail-order"})
	if err != nil {
		t.Fatal(err)
	}
	start, err := prepared.Dispatch.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source, ok := start.Session.(routes.RouteStreamLifetimeSource)
	if !ok {
		t.Fatal("Open did not bind a lifetime source")
	}
	probe.done = source.Done()
	prepared.Dispatch.ResponseStarted()
	streamRouteResponse(bufio.NewWriter(bytes.NewBuffer(nil)), start.Session, prepared.Dispatch)
	if !probe.failWhileOpen.Load() {
		t.Fatal("transport-fail trace was not observed while lifetime Done was still open")
	}
	if probe.failAfterDone.Load() {
		t.Fatal("transport-fail trace published after lifetime Done closed")
	}
	select {
	case <-source.Done():
	case <-time.After(time.Second):
		t.Fatal("lifetime Done was not closed after adapter Cancel")
	}
	records := traces.RouteTraces(0)
	if !hasRouteTraceOutcome(records, routes.RouteTraceTransportFailed) {
		t.Fatalf("missing fail trace: %#v", records)
	}
}

func TestStreamRouteResponsePublishesCommitTraceBeforeLifetimeDone(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.commit-order", 'c')
	declaration := routeDispatcherManifestRoute("stream.commit-order.body", extensionmanifest.RouteActionAdd, "/commit-order", "GET")
	declaration.Mode = extensionmanifest.RouteModeStream
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration}}}}); err != nil {
		t.Fatal(err)
	}
	inner := &streamHTTPTestSession{
		chunks:   []routes.RouteStreamChunk{{Data: []byte("ok"), Final: true}},
		response: routes.DispatchResponse{Status: stdhttp.StatusOK},
	}
	traces := routes.NewRouteTraceRing(8)
	probe := &streamLifetimeOrderProbe{inner: traces}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: &streamHTTPTestInvoker{start: routes.RouteStreamStart{
			Response: routes.DispatchResponse{Status: stdhttp.StatusOK}, Session: inner,
		}}, Guard: HostRouteGuardAuthorizer{}, Trace: probe,
	})
	prepared, err := dispatcher.PrepareStream(context.Background(), routes.DispatchRequest{Method: "GET", Path: "/commit-order"})
	if err != nil {
		t.Fatal(err)
	}
	start, err := prepared.Dispatch.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source, ok := start.Session.(routes.RouteStreamLifetimeSource)
	if !ok {
		t.Fatal("Open did not bind a lifetime source")
	}
	probe.done = source.Done()
	prepared.Dispatch.ResponseStarted()
	streamRouteResponse(bufio.NewWriter(bytes.NewBuffer(nil)), start.Session, prepared.Dispatch)
	if !probe.commitWhileOpen.Load() {
		t.Fatal("commit trace was not observed while lifetime Done was still open")
	}
	if probe.commitAfterDone.Load() {
		t.Fatal("commit trace published after lifetime Done closed")
	}
	select {
	case <-source.Done():
	case <-time.After(time.Second):
		t.Fatal("lifetime Done was not closed after adapter Cancel")
	}
	if !hasRouteTraceOutcome(traces.RouteTraces(0), routes.RouteTraceCommitted) {
		t.Fatalf("missing commit trace: %#v", traces.RouteTraces(0))
	}
}

func TestRouteDispatcherBridgesWebSocketMessagesAndDisconnect(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.websocket", 'c')
	declaration := routeDispatcherManifestRoute("stream.websocket.echo", extensionmanifest.RouteActionAdd, "/socket", "GET")
	declaration.Mode = extensionmanifest.RouteModeWebSocket
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	session := newWebSocketEchoSession()
	traces := routes.NewRouteTraceRing(8)
	invoker := &streamHTTPTestInvoker{observe: true, start: routes.RouteStreamStart{
		Response: routes.DispatchResponse{
			Status:  fiber.StatusSwitchingProtocols,
			Headers: stdhttp.Header{"Sec-WebSocket-Protocol": {"sforum.echo.v1"}},
		},
		Session: session,
	}}
	contextKey := routeWebSocketRequestContextKey{}
	guard := &countingRouteGuard{contextKey: contextKey, contextValue: "request-bound"}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: invoker,
		Guard: guard, Trace: traces,
	})
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.SetContext(context.WithValue(c.Context(), contextKey, "request-bound"))
		return c.Next()
	})
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- app.Listener(listener) }()
	t.Cleanup(func() {
		_ = app.Shutdown()
		_ = listener.Close()
		<-serverDone
	})
	dialer := websocket.Dialer{Subprotocols: []string{"sforum.echo.v1"}}
	connection, response, err := dialer.Dial("ws://"+listener.Addr().String()+"/socket", nil)
	if err != nil {
		t.Fatalf("dial status=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if response.StatusCode != fiber.StatusSwitchingProtocols || connection.Subprotocol() != "sforum.echo.v1" {
		t.Fatalf("status=%d subprotocol=%q", response.StatusCode, connection.Subprotocol())
	}
	if err := connection.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	messageType, payload, err := connection.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage || string(payload) != "hello" {
		t.Fatalf("messageType=%d payload=%q err=%v", messageType, payload, err)
	}
	_, _, err = connection.ReadMessage()
	var closeError *websocket.CloseError
	if !errors.As(err, &closeError) || closeError.Code != websocket.CloseNormalClosure {
		t.Fatalf("websocket terminal error=%v", err)
	}
	select {
	case <-session.done:
	case <-time.After(2 * time.Second):
		t.Fatal("websocket disconnect did not cancel the runtime session")
	}
	if invoker.openCalls.Load() != 1 || string(session.received) != "hello" {
		t.Fatalf("openCalls=%d received=%q", invoker.openCalls.Load(), session.received)
	}
	if guard.calls.Load() != 1 {
		t.Fatalf("guard calls=%d", guard.calls.Load())
	}
	records := traces.RouteTraces(8)
	if len(records) != 2 || records[0].Outcome != routes.RouteTraceSucceeded || records[1].Outcome != routes.RouteTraceCommitted {
		t.Fatalf("traces=%#v", records)
	}
}

func TestRouteDispatcherWaitsForWebSocketResponseTerminalAfterClientClose(t *testing.T) {
	session := newWebSocketTerminalBarrierSession()
	t.Cleanup(session.cleanup)
	traces := routes.NewRouteTraceRing(8)
	sink := &routeV2RecordingStreamFailureSink{}
	connection := dialRouteWebSocketTerminalTest(t, session, traces, sink)

	if err := connection.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	waitRouteWebSocketSignal(t, session.requestClosed, "request close")
	assertNoRouteTraceOutcome(t, traces.RouteTraces(0), routes.RouteTraceCommitted)
	select {
	case <-session.done:
		t.Fatal("normal request close cancelled the session before the response terminal")
	default:
	}

	session.release()
	waitRouteWebSocketSignal(t, session.done, "response failure cancellation")
	waitRouteWebSocketSignal(t, session.recvExited, "response pump exit")
	records := traces.RouteTraces(0)
	assertNoRouteTraceOutcome(t, records, routes.RouteTraceCommitted)
	if !hasRouteTraceOutcome(records, routes.RouteTraceTransportFailed) {
		t.Fatalf("response failure was not recorded: %#v", records)
	}
	events := sink.snapshot()
	if len(events) != 1 || events[0].CauseClass != routes.RouteStreamFailureRuntimeTransport {
		t.Fatalf("events=%#v", events)
	}
}

func TestRouteDispatcherFailsWebSocketWhenClosingPluginRequestFails(t *testing.T) {
	session := newWebSocketTerminalBarrierSession()
	t.Cleanup(session.cleanup)
	session.closeRequestErr = errors.New("plugin request close failed")
	traces := routes.NewRouteTraceRing(8)
	sink := &routeV2RecordingStreamFailureSink{}
	connection := dialRouteWebSocketTerminalTest(t, session, traces, sink)

	if err := connection.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	waitRouteWebSocketSignal(t, session.requestClosed, "request close")
	waitRouteWebSocketSignal(t, session.done, "request close failure cancellation")
	waitRouteWebSocketSignal(t, session.recvExited, "response pump exit")
	records := traces.RouteTraces(0)
	assertNoRouteTraceOutcome(t, records, routes.RouteTraceCommitted)
	if !hasRouteTraceOutcome(records, routes.RouteTraceTransportFailed) {
		t.Fatalf("request close failure was not recorded: %#v", records)
	}
	events := sink.snapshot()
	if len(events) != 1 || events[0].CauseClass != routes.RouteStreamFailureRuntimeTransport {
		t.Fatalf("events=%#v", events)
	}
}

func TestRouteDispatcherCommitsWebSocketAfterRequestCloseAndResponseTerminal(t *testing.T) {
	session := newWebSocketTerminalBarrierSession()
	t.Cleanup(session.cleanup)
	session.recvErr = io.EOF
	session.response = routes.DispatchResponse{Status: fiber.StatusSwitchingProtocols}
	traces := routes.NewRouteTraceRing(8)
	sink := &routeV2RecordingStreamFailureSink{}
	connection := dialRouteWebSocketTerminalTest(t, session, traces, sink)

	if err := connection.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	waitRouteWebSocketSignal(t, session.requestClosed, "request close")
	assertNoRouteTraceOutcome(t, traces.RouteTraces(0), routes.RouteTraceCommitted)
	session.release()
	_, _, err := connection.ReadMessage()
	var closeError *websocket.CloseError
	if !errors.As(err, &closeError) || closeError.Code != websocket.CloseNormalClosure {
		t.Fatalf("websocket terminal error=%v", err)
	}
	waitRouteWebSocketSignal(t, session.done, "successful terminal cancellation")
	waitRouteWebSocketSignal(t, session.recvExited, "response pump exit")
	records := traces.RouteTraces(0)
	if !hasRouteTraceOutcome(records, routes.RouteTraceCommitted) ||
		hasRouteTraceOutcome(records, routes.RouteTraceTransportFailed) {
		t.Fatalf("terminal traces=%#v", records)
	}
	if events := sink.snapshot(); len(events) != 0 {
		t.Fatalf("events=%#v", events)
	}
}

func TestAwaitRouteWebSocketTerminalRequiresPluginResponseAfterNormalRequestClose(t *testing.T) {
	type terminalResult struct {
		pump  routeWebSocketPumpResult
		drain bool
	}
	results := make(chan routeWebSocketPumpResult)
	resolved := make(chan terminalResult, 1)
	go func() {
		pump, drain := awaitRouteWebSocketTerminal(results, time.Second)
		resolved <- terminalResult{pump: pump, drain: drain}
	}()

	results <- routeWebSocketPumpResult{direction: "request", normal: true}
	select {
	case terminal := <-resolved:
		t.Fatalf("request close became authoritative terminal: %#v", terminal)
	default:
	}
	responseErr := errors.New("plugin response failed")
	select {
	case results <- routeWebSocketPumpResult{direction: "response", err: responseErr}:
	case <-time.After(time.Second):
		t.Fatal("terminal resolver did not wait for the plugin response")
	}
	select {
	case terminal := <-resolved:
		if terminal.pump.direction != "response" || terminal.pump.normal ||
			!errors.Is(terminal.pump.err, responseErr) || terminal.drain {
			t.Fatalf("terminal=%#v", terminal)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal resolver did not return the plugin response failure")
	}
}

func TestAwaitRouteWebSocketTerminalBoundsMissingPluginResponse(t *testing.T) {
	type terminalResult struct {
		pump  routeWebSocketPumpResult
		drain bool
	}
	results := make(chan routeWebSocketPumpResult)
	resolved := make(chan terminalResult, 1)
	go func() {
		pump, drain := awaitRouteWebSocketTerminal(results, 0)
		resolved <- terminalResult{pump: pump, drain: drain}
	}()
	results <- routeWebSocketPumpResult{direction: "request", normal: true}
	select {
	case terminal := <-resolved:
		if terminal.pump.direction != "response" || terminal.pump.normal ||
			terminal.pump.err == nil || !terminal.drain {
			t.Fatalf("terminal=%#v", terminal)
		}
	case <-time.After(time.Second):
		t.Fatal("missing plugin response was not bounded")
	}
}

func TestRouteWebSocketResponseTerminalGraceIsHostBounded(t *testing.T) {
	if routeWebSocketResponseTerminalGrace != 2*time.Second ||
		routeWebSocketResponseTerminalGrace >= extensionsruntime.DefaultProtocolV2RouteStreamTimeout {
		t.Fatalf("terminal grace=%s", routeWebSocketResponseTerminalGrace)
	}
}

func TestAwaitRouteWebSocketTerminalPreservesResponseBeforeCloseCleanupError(t *testing.T) {
	results := make(chan routeWebSocketPumpResult, 2)
	closeErr := errors.New("plugin request close failed")
	results <- routeWebSocketPumpResult{direction: "response", normal: true}
	results <- routeWebSocketPumpResult{direction: "request", err: closeErr}

	terminal, drain := awaitRouteWebSocketTerminal(results, time.Second)
	if terminal.direction != "response" || !terminal.normal || terminal.err != nil || !drain {
		t.Fatalf("terminal=%#v drain=%t", terminal, drain)
	}
	drained := <-results
	if drained.direction != "request" || !errors.Is(drained.err, closeErr) {
		t.Fatalf("drained=%#v", drained)
	}
}

func TestRouteWebSocketPumpFailureNeverLeavesEOFFenceOpen(t *testing.T) {
	for _, err := range []error{nil, io.EOF} {
		if failure := routeWebSocketPumpFailure(err); !errors.Is(failure, routes.ErrDispatchTransport) {
			t.Fatalf("input=%v failure=%v", err, failure)
		}
	}
	sentinel := errors.New("request failed")
	if failure := routeWebSocketPumpFailure(sentinel); !errors.Is(failure, sentinel) {
		t.Fatalf("failure=%v", failure)
	}
}

func dialRouteWebSocketTerminalTest(
	t *testing.T,
	session routes.RouteStreamSession,
	traces *routes.RouteTraceRing,
	streamFailures ...routes.RouteStreamFailureSink,
) *websocket.Conn {
	t.Helper()
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.websocket.terminal", 'e')
	declaration := routeDispatcherManifestRoute(
		"stream.websocket.terminal.handler", extensionmanifest.RouteActionAdd, "/socket-terminal", stdhttp.MethodGet,
	)
	declaration.Mode = extensionmanifest.RouteModeWebSocket
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	config := routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry},
		Steps: &streamHTTPTestInvoker{start: routes.RouteStreamStart{
			Response: routes.DispatchResponse{Status: fiber.StatusSwitchingProtocols},
			Session:  session,
		}},
		Guard: HostRouteGuardAuthorizer{},
		Trace: traces,
	}
	if len(streamFailures) > 0 {
		config.StreamFailures = streamFailures[0]
	}
	dispatcher := routes.NewDispatcher(config)
	app := fiber.New()
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- app.Listener(listener) }()
	t.Cleanup(func() {
		_ = app.Shutdown()
		_ = listener.Close()
		<-serverDone
	})
	connection, response, err := websocket.DefaultDialer.Dial(
		"ws://"+listener.Addr().String()+"/socket-terminal", nil,
	)
	if err != nil {
		t.Fatalf("dial status=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func waitRouteWebSocketSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func assertNoRouteTraceOutcome(t *testing.T, records []routes.RouteTraceRecord, outcome routes.RouteTraceOutcome) {
	t.Helper()
	if hasRouteTraceOutcome(records, outcome) {
		t.Fatalf("unexpected %s trace: %#v", outcome, records)
	}
}

func hasRouteTraceOutcome(records []routes.RouteTraceRecord, outcome routes.RouteTraceOutcome) bool {
	for _, record := range records {
		if record.Outcome == outcome {
			return true
		}
	}
	return false
}

func TestRouteDispatcherInvalidWebSocketPreflightFailsBeforeLifetimeDone(t *testing.T) {
	// Drive the real Fiber serveRouteWebSocket path: Open returns 101 with an
	// unsolicited subprotocol the client never offered. The adapter must Fail
	// (transport-fail trace) before Cancel so the fail evidence is visible while
	// the session is still open.
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.websocket.preflight-order", 'a')
	declaration := routeDispatcherManifestRoute(
		"stream.websocket.preflight-order.socket", extensionmanifest.RouteActionAdd, "/socket-preflight-order", "GET",
	)
	declaration.Mode = extensionmanifest.RouteModeWebSocket
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	session := &webSocketPreflightOrderSession{}
	traces := routes.NewRouteTraceRing(8)
	sink := &routeV2RecordingStreamFailureSink{}
	probe := &webSocketPreflightOrderProbe{inner: traces, session: session}
	// Use Header.Set so keys are canonical; map literals break Header.Get and would
	// skip subprotocol validation (selected == "" is treated as no selection).
	preflightHeaders := make(stdhttp.Header)
	preflightHeaders.Set("Sec-WebSocket-Protocol", "unsolicited.v1")
	invoker := &streamHTTPTestInvoker{observe: true, start: routes.RouteStreamStart{
		Response: routes.DispatchResponse{
			Status:  fiber.StatusSwitchingProtocols,
			Headers: preflightHeaders,
		},
		Session: session,
	}}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: invoker,
		Guard: HostRouteGuardAuthorizer{}, Trace: probe, StreamFailures: sink,
	})
	app := fiber.New()
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- app.Listener(listener) }()
	t.Cleanup(func() {
		_ = app.Shutdown()
		_ = listener.Close()
		<-serverDone
	})

	// Client offers no subprotocol; server selects unsolicited.v1 → invalid preflight.
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	connection, response, dialErr := dialer.Dial("ws://"+listener.Addr().String()+"/socket-preflight-order", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if connection != nil {
		_ = connection.Close()
	}
	if dialErr == nil {
		t.Fatal("invalid WebSocket preflight unexpectedly upgraded")
	}
	if invoker.openCalls.Load() != 1 {
		t.Fatalf("openCalls=%d", invoker.openCalls.Load())
	}
	if !probe.failBeforeCancel.Load() {
		t.Fatal("transport-fail trace was not published before Session.Cancel on serveRouteWebSocket")
	}
	if probe.failAfterCancel.Load() {
		t.Fatal("transport-fail trace published after Session.Cancel closed the lifetime")
	}
	if !session.cancelled.Load() {
		t.Fatal("invalid preflight did not Cancel the stream session")
	}
	if !hasRouteTraceOutcome(traces.RouteTraces(0), routes.RouteTraceTransportFailed) {
		t.Fatalf("missing fail trace: %#v", traces.RouteTraces(0))
	}
	events := sink.snapshot()
	if len(events) != 1 || events[0].CauseClass != routes.RouteStreamFailureInvalidPreflight {
		t.Fatalf("events=%#v", events)
	}
}

func TestRouteDispatcherRejectsMalformedWebSocketBeforeRuntime(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.websocket.invalid", 'd')
	declaration := routeDispatcherManifestRoute("stream.websocket.invalid.socket", extensionmanifest.RouteActionAdd, "/socket-invalid", "GET")
	declaration.Mode = extensionmanifest.RouteModeWebSocket
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration}}}}); err != nil {
		t.Fatal(err)
	}
	invoker := &streamHTTPTestInvoker{}
	guard := &countingRouteGuard{}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: invoker, Guard: guard,
	})
	app := fiber.New()
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	validUpgrade := func() *stdhttp.Request {
		request := httptest.NewRequest(stdhttp.MethodGet, "/socket-invalid", nil)
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		request.Header.Set("Sec-WebSocket-Version", "13")
		return request
	}
	tests := []struct {
		name    string
		request func() *stdhttp.Request
	}{
		{name: "plain request", request: func() *stdhttp.Request {
			return httptest.NewRequest(stdhttp.MethodGet, "/socket-invalid", nil)
		}},
		{name: "cross origin", request: func() *stdhttp.Request {
			request := validUpgrade()
			request.Header.Set("Origin", "https://evil.example")
			return request
		}},
		{name: "version substring", request: func() *stdhttp.Request {
			request := validUpgrade()
			request.Header.Set("Sec-WebSocket-Version", "113")
			return request
		}},
		{name: "version list", request: func() *stdhttp.Request {
			request := validUpgrade()
			request.Header.Set("Sec-WebSocket-Version", "13, 12")
			return request
		}},
		{name: "duplicate version", request: func() *stdhttp.Request {
			request := validUpgrade()
			request.Header.Add("Sec-WebSocket-Version", "13")
			return request
		}},
		{name: "malformed key", request: func() *stdhttp.Request {
			request := validUpgrade()
			request.Header.Set("Sec-WebSocket-Key", "not-base64")
			return request
		}},
		{name: "key list", request: func() *stdhttp.Request {
			request := validUpgrade()
			request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==, dGhlIHNhbXBsZSBub25jZQ==")
			return request
		}},
		{name: "wrong nonce length", request: func() *stdhttp.Request {
			request := validUpgrade()
			request.Header.Set("Sec-WebSocket-Key", "YWJj")
			return request
		}},
		{name: "duplicate key", request: func() *stdhttp.Request {
			request := validUpgrade()
			request.Header.Add("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			return request
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := app.Test(test.request())
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != fiber.StatusUpgradeRequired || invoker.openCalls.Load() != 0 || guard.calls.Load() != 0 {
				t.Fatalf("status=%d openCalls=%d guardCalls=%d", response.StatusCode, invoker.openCalls.Load(), guard.calls.Load())
			}
		})
	}
}

type countingRouteGuard struct {
	calls        atomic.Int32
	inner        routes.CoreGuardAuthorizer
	contextKey   any
	contextValue any
}

type routeWebSocketRequestContextKey struct{}

func (g *countingRouteGuard) validateContext(ctx context.Context) error {
	if g.contextKey != nil && ctx.Value(g.contextKey) != g.contextValue {
		return errors.New("route guard lost the request context")
	}
	return nil
}

func (g *countingRouteGuard) Authorize(
	ctx context.Context,
	plan routes.RouteExecutionPlan,
	step routes.RouteExecutionStep,
	request routes.DispatchRequest,
) error {
	g.calls.Add(1)
	if err := g.validateContext(ctx); err != nil {
		return err
	}
	return g.inner.Authorize(ctx, plan, step, request)
}

func (g *countingRouteGuard) AuthorizeRoute(
	ctx context.Context,
	plan routes.RouteExecutionPlan,
	stepIndex int,
	step routes.RouteExecutionStep,
	request routes.DispatchRequest,
) (routes.RouteGuardAuthorization, error) {
	g.calls.Add(1)
	if err := g.validateContext(ctx); err != nil {
		return routes.RouteGuardAuthorization{}, err
	}
	return g.inner.AuthorizeRoute(ctx, plan, stepIndex, step, request)
}

type streamHTTPTestInvoker struct {
	start     routes.RouteStreamStart
	observe   bool
	openCalls atomic.Int64
}

func (*streamHTTPTestInvoker) SupportsMode(string) bool { return false }

func (*streamHTTPTestInvoker) Invoke(context.Context, routes.RouteInvocation) (routes.RouteInvocationResult, error) {
	return routes.RouteInvocationResult{}, errors.New("buffered invocation is not expected")
}

func (i *streamHTTPTestInvoker) OpenStream(_ context.Context, invocation routes.RouteInvocation) (routes.RouteStreamStart, error) {
	i.openCalls.Add(1)
	if i.observe && invocation.Commit != nil {
		invocation.Commit.SideEffectStarted()
	}
	return i.start, nil
}

type streamHTTPTestSession struct {
	chunks          []routes.RouteStreamChunk
	response        routes.DispatchResponse
	recvErr         error
	sendErr         error
	closeRequestErr error
	requestChunks   []int
	requestBytes    int
	maxRequestChunk int
	requestClosed   bool
	cancelled       bool
	terminal        bool
}

func (s *streamHTTPTestSession) Send(data []byte, _ bool) error {
	s.requestChunks = append(s.requestChunks, len(data))
	s.requestBytes += len(data)
	if len(data) > s.maxRequestChunk {
		s.maxRequestChunk = len(data)
	}
	return s.sendErr
}

func (s *streamHTTPTestSession) CloseRequest() error {
	s.requestClosed = true
	return s.closeRequestErr
}

func (s *streamHTTPTestSession) Recv() (routes.RouteStreamChunk, error) {
	if s.recvErr != nil {
		return routes.RouteStreamChunk{}, s.recvErr
	}
	if len(s.chunks) == 0 {
		s.terminal = true
		return routes.RouteStreamChunk{}, io.EOF
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	return chunk, nil
}

func (s *streamHTTPTestSession) Response() (routes.DispatchResponse, bool) {
	return s.response, s.terminal
}

func (s *streamHTTPTestSession) Cancel() { s.cancelled = true }

type routeStreamErrorReader struct{ err error }

func (r routeStreamErrorReader) Read([]byte) (int, error) { return 0, r.err }

type routeStreamErrorWriter struct{ err error }

func (w routeStreamErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func prepareHTTPStreamAdapterTest(
	t *testing.T,
	session routes.RouteStreamSession,
) (*routes.RouteStreamDispatch, routes.RouteStreamSession, *routeV2RecordingStreamFailureSink) {
	t.Helper()
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.http.adapter", 'a')
	declaration := routeDispatcherManifestRoute(
		"stream.http.adapter.handler", extensionmanifest.RouteActionAdd, "/stream-http-adapter", stdhttp.MethodGet,
	)
	declaration.Mode = extensionmanifest.RouteModeStream
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	sink := &routeV2RecordingStreamFailureSink{}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry},
		Steps: &streamHTTPTestInvoker{start: routes.RouteStreamStart{
			Response: routes.DispatchResponse{Status: stdhttp.StatusOK}, Session: session,
		}},
		Guard: HostRouteGuardAuthorizer{}, StreamFailures: sink,
	})
	prepared, err := dispatcher.PrepareStream(
		context.Background(), routes.DispatchRequest{Method: stdhttp.MethodGet, Path: "/stream-http-adapter"},
	)
	if err != nil {
		t.Fatal(err)
	}
	start, err := prepared.Dispatch.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(start.Session.Cancel)
	// The fake invoker has no runtime call; mark the production-equivalent point
	// before exercising adapter-side provenance.
	prepared.Dispatch.RequestStarted()
	return prepared.Dispatch, start.Session, sink
}

// webSocketPreflightOrderSession records Cancel so the real serveRouteWebSocket
// path can prove Fail publishes the transport-fail trace first.
type webSocketPreflightOrderSession struct {
	streamHTTPTestSession
	cancelled atomic.Bool
}

func (s *webSocketPreflightOrderSession) Cancel() {
	if s == nil {
		return
	}
	s.cancelled.Store(true)
	s.streamHTTPTestSession.Cancel()
}

// webSocketPreflightOrderProbe observes whether the fail trace lands before Cancel.
type webSocketPreflightOrderProbe struct {
	inner            *routes.RouteTraceRing
	session          *webSocketPreflightOrderSession
	failBeforeCancel atomic.Bool
	failAfterCancel  atomic.Bool
}

func (p *webSocketPreflightOrderProbe) AppendRouteTrace(event routes.RouteTraceEvent) {
	if p == nil {
		return
	}
	if p.inner != nil {
		p.inner.AppendRouteTrace(event)
	}
	if event.Outcome != routes.RouteTraceTransportFailed || p.session == nil {
		return
	}
	if p.session.cancelled.Load() {
		p.failAfterCancel.Store(true)
		return
	}
	p.failBeforeCancel.Store(true)
}

var _ routes.RouteTraceSink = (*webSocketPreflightOrderProbe)(nil)

// streamLifetimeOrderProbe records whether commit/fail traces land while Done is open.
type streamLifetimeOrderProbe struct {
	inner           *routes.RouteTraceRing
	done            <-chan struct{}
	failWhileOpen   atomic.Bool
	failAfterDone   atomic.Bool
	commitWhileOpen atomic.Bool
	commitAfterDone atomic.Bool
}

func (p *streamLifetimeOrderProbe) AppendRouteTrace(event routes.RouteTraceEvent) {
	if p == nil {
		return
	}
	if p.inner != nil {
		p.inner.AppendRouteTrace(event)
	}
	if p.done == nil {
		return
	}
	select {
	case <-p.done:
		switch event.Outcome {
		case routes.RouteTraceTransportFailed:
			p.failAfterDone.Store(true)
		case routes.RouteTraceCommitted:
			p.commitAfterDone.Store(true)
		}
	default:
		switch event.Outcome {
		case routes.RouteTraceTransportFailed:
			p.failWhileOpen.Store(true)
		case routes.RouteTraceCommitted:
			p.commitWhileOpen.Store(true)
		}
	}
}

var _ routes.StepInvoker = (*streamHTTPTestInvoker)(nil)
var _ routes.StreamingStepInvoker = (*streamHTTPTestInvoker)(nil)
var _ routes.RouteStreamSession = (*streamHTTPTestSession)(nil)
var _ routes.RouteTraceSink = (*streamLifetimeOrderProbe)(nil)

type webSocketEchoSession struct {
	messages  chan []byte
	done      chan struct{}
	doneOnce  sync.Once
	received  []byte
	terminal  atomic.Bool
	responded atomic.Bool
}

func newWebSocketEchoSession() *webSocketEchoSession {
	return &webSocketEchoSession{messages: make(chan []byte, 1), done: make(chan struct{})}
}

func (s *webSocketEchoSession) Send(data []byte, _ bool) error {
	s.received = append([]byte(nil), data...)
	s.messages <- append([]byte(nil), data...)
	return nil
}

func (*webSocketEchoSession) CloseRequest() error { return nil }

func (s *webSocketEchoSession) Recv() (routes.RouteStreamChunk, error) {
	if s.responded.CompareAndSwap(false, true) {
		message := <-s.messages
		return routes.RouteStreamChunk{Sequence: 1, Data: message}, nil
	}
	s.terminal.Store(true)
	return routes.RouteStreamChunk{}, io.EOF
}

func (s *webSocketEchoSession) Response() (routes.DispatchResponse, bool) {
	return routes.DispatchResponse{Status: fiber.StatusSwitchingProtocols}, s.terminal.Load()
}

func (s *webSocketEchoSession) Cancel() {
	s.doneOnce.Do(func() { close(s.done) })
}

var _ routes.RouteStreamSession = (*webSocketEchoSession)(nil)

type webSocketTerminalBarrierSession struct {
	requestClosed   chan struct{}
	releaseResponse chan struct{}
	recvExited      chan struct{}
	done            chan struct{}
	requestOnce     sync.Once
	releaseOnce     sync.Once
	recvOnce        sync.Once
	doneOnce        sync.Once
	closeRequestErr error
	sendErr         error
	recvErr         error
	response        routes.DispatchResponse
	missingResponse bool
	terminal        atomic.Bool
	sendCalls       atomic.Int64
}

func newWebSocketTerminalBarrierSession() *webSocketTerminalBarrierSession {
	return &webSocketTerminalBarrierSession{
		requestClosed:   make(chan struct{}),
		releaseResponse: make(chan struct{}),
		recvExited:      make(chan struct{}),
		done:            make(chan struct{}),
		recvErr:         errors.New("plugin response failed"),
	}
}

func (s *webSocketTerminalBarrierSession) Send([]byte, bool) error {
	s.sendCalls.Add(1)
	return s.sendErr
}

func (s *webSocketTerminalBarrierSession) CloseRequest() error {
	s.requestOnce.Do(func() { close(s.requestClosed) })
	return s.closeRequestErr
}

func (s *webSocketTerminalBarrierSession) Recv() (routes.RouteStreamChunk, error) {
	defer s.recvOnce.Do(func() { close(s.recvExited) })
	select {
	case <-s.releaseResponse:
		if errors.Is(s.recvErr, io.EOF) {
			s.terminal.Store(true)
		}
		return routes.RouteStreamChunk{}, s.recvErr
	case <-s.done:
		return routes.RouteStreamChunk{}, context.Canceled
	}
}

func (s *webSocketTerminalBarrierSession) Response() (routes.DispatchResponse, bool) {
	if s.missingResponse {
		return routes.DispatchResponse{}, false
	}
	return s.response, s.terminal.Load()
}

func (s *webSocketTerminalBarrierSession) Cancel() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *webSocketTerminalBarrierSession) release() {
	s.releaseOnce.Do(func() { close(s.releaseResponse) })
}

func (s *webSocketTerminalBarrierSession) cleanup() {
	s.release()
	s.Cancel()
}

var _ routes.RouteStreamSession = (*webSocketTerminalBarrierSession)(nil)
