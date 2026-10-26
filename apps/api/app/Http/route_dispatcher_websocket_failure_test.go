package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteWebSocketAdapterFailureDispositionMatrix(t *testing.T) {
	tests := []struct {
		name      string
		failure   func() error
		wantClass routes.RouteStreamFailureClass
	}{
		{name: "runtime transport", failure: func() error { return errors.New("runtime crashed") }, wantClass: routes.RouteStreamFailureRuntimeTransport},
		{name: "host budget", failure: func() error {
			return routes.WithRouteStreamIncident(routes.ErrRouteStreamBudgetExceeded, routes.RouteStreamFailureHostBudget)
		}, wantClass: routes.RouteStreamFailureHostBudget},
		{name: "missing terminal", failure: func() error { return routeWebSocketMissingTerminalResult().err }, wantClass: routes.RouteStreamFailureMissingTerminal},
		{name: "typed EOF incident", failure: func() error {
			return routeWebSocketPumpFailure(routes.WithRouteStreamIncident(io.EOF, routes.RouteStreamFailureMissingTerminal))
		}, wantClass: routes.RouteStreamFailureMissingTerminal},
		{name: "caller disconnect", failure: func() error { return routes.WithRouteStreamAbort(io.ErrUnexpectedEOF) }},
		{name: "host writer", failure: func() error { return routes.WithRouteStreamAbort(io.ErrClosedPipe) }},
		{name: "force drain", failure: func() error {
			return routes.WithRouteStreamAbort(extensionsruntime.ErrRuntimeAdmissionForced)
		}},
		{name: "invalid pump result", failure: func() error { return routeWebSocketPumpFailure(nil) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatch, session, sink := prepareWebSocketDispositionTest(t)
			_ = dispatch.StreamFailed(test.failure())
			session.Cancel()
			events := sink.snapshot()
			if test.wantClass == "" {
				if len(events) != 0 {
					t.Fatalf("events=%#v", events)
				}
				return
			}
			if len(events) != 1 || events[0].CauseClass != test.wantClass {
				t.Fatalf("events=%#v want=%q", events, test.wantClass)
			}
		})
	}
}

func TestAwaitRouteWebSocketTerminalClassifiesGraceTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, 5 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			results := make(chan routeWebSocketPumpResult, 1)
			results <- routeWebSocketPumpResult{direction: "request", normal: true}
			terminal, drain := awaitRouteWebSocketTerminal(results, timeout)
			if terminal.direction != "response" || terminal.normal || terminal.err == nil || !drain {
				t.Fatalf("terminal=%#v drain=%t", terminal, drain)
			}
			dispatch, session, sink := prepareWebSocketDispositionTest(t)
			_ = dispatch.StreamFailed(terminal.err)
			session.Cancel()
			events := sink.snapshot()
			if len(events) != 1 || events[0].CauseClass != routes.RouteStreamFailureMissingTerminal {
				t.Fatalf("events=%#v", events)
			}
		})
	}
}

func TestAwaitRouteWebSocketTerminalInvalidDirectionsAreHostAborts(t *testing.T) {
	for _, test := range []struct {
		name    string
		results []routeWebSocketPumpResult
	}{
		{name: "first", results: []routeWebSocketPumpResult{{direction: "invalid", err: errors.New("invalid first result")}}},
		{name: "second invalid", results: []routeWebSocketPumpResult{
			{direction: "request", normal: true},
			{direction: "invalid", err: errors.New("invalid second result")},
		}},
		{name: "duplicate request", results: []routeWebSocketPumpResult{
			{direction: "request", normal: true},
			{direction: "request", normal: true},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			results := make(chan routeWebSocketPumpResult, len(test.results))
			for _, result := range test.results {
				results <- result
			}
			terminal, _ := awaitRouteWebSocketTerminal(results, time.Second)
			dispatch, session, sink := prepareWebSocketDispositionTest(t)
			_ = dispatch.StreamFailed(routeWebSocketPumpFailure(terminal.err))
			session.Cancel()
			if events := sink.snapshot(); len(events) != 0 {
				t.Fatalf("terminal=%#v events=%#v", terminal, events)
			}
		})
	}
}

func TestAwaitRouteWebSocketTerminalPrefersQueuedRuntimeAndResponse(t *testing.T) {
	runtimeErr := errors.New("runtime crashed independently")
	abortErr := routes.WithRouteStreamAbort(io.ErrUnexpectedEOF)
	for _, test := range []struct {
		name       string
		results    []routeWebSocketPumpResult
		wantClass  routes.RouteStreamFailureClass
		wantCommit bool
	}{
		{name: "runtime after abort", results: []routeWebSocketPumpResult{
			{direction: "request", err: abortErr},
			{direction: "response", err: runtimeErr},
		}, wantClass: routes.RouteStreamFailureRuntimeTransport},
		{name: "runtime before abort", results: []routeWebSocketPumpResult{
			{direction: "response", err: runtimeErr},
			{direction: "request", err: abortErr},
		}, wantClass: routes.RouteStreamFailureRuntimeTransport},
		{name: "response after abort", results: []routeWebSocketPumpResult{
			{direction: "request", err: abortErr},
			{direction: "response", normal: true},
		}, wantCommit: true},
		{name: "zero grace queued response", results: []routeWebSocketPumpResult{
			{direction: "request", normal: true},
			{direction: "response", normal: true},
		}, wantCommit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			results := make(chan routeWebSocketPumpResult, len(test.results))
			for _, result := range test.results {
				results <- result
			}
			terminal, drain := awaitRouteWebSocketTerminal(results, 0)
			if drain {
				t.Fatalf("queued opposite pump was not consumed: %#v", terminal)
			}
			if test.wantCommit {
				if terminal.direction != "response" || !terminal.normal || terminal.err != nil {
					t.Fatalf("terminal=%#v", terminal)
				}
				return
			}
			dispatch, session, sink := prepareWebSocketDispositionTest(t)
			_ = dispatch.StreamFailed(routeWebSocketPumpFailure(terminal.err))
			session.Cancel()
			events := sink.snapshot()
			if len(events) != 1 || events[0].CauseClass != test.wantClass {
				t.Fatalf("terminal=%#v events=%#v", terminal, events)
			}
		})
	}
}

func TestClassifyRouteWebSocketDetachFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		wantClass routes.RouteStreamFailureClass
	}{
		{name: "Host budget", err: routes.ErrRouteStreamBudgetExceeded, wantClass: routes.RouteStreamFailureHostBudget},
		{name: "caller", err: errors.New("caller disappeared")},
		{name: "typed caller with budget-shaped cause", err: routes.WithRouteStreamAbort(routes.ErrRouteStreamBudgetExceeded)},
		{name: "ForceDrain", err: extensionsruntime.ErrRuntimeAdmissionForced},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatch, session, sink := prepareWebSocketDispositionTest(t)
			_ = dispatch.StreamFailed(classifyRouteWebSocketDetachFailure(test.err))
			session.Cancel()
			events := sink.snapshot()
			if test.wantClass == "" && len(events) != 0 {
				t.Fatalf("events=%#v", events)
			}
			if test.wantClass != "" && (len(events) != 1 || events[0].CauseClass != test.wantClass) {
				t.Fatalf("events=%#v want=%q", events, test.wantClass)
			}
		})
	}
}

func TestValidRouteWebSocketResponseHeadersReserveHandshakeAuthority(t *testing.T) {
	for _, name := range []string{
		"Sec-WebSocket-Accept", "Sec-WebSocket-Extensions", "Sec-WebSocket-Key", "Sec-WebSocket-Version",
	} {
		t.Run(name, func(t *testing.T) {
			if validRouteWebSocketResponseHeaders(stdhttp.Header{name: {"forged"}}) {
				t.Fatalf("Host-owned handshake header %q was accepted", name)
			}
		})
	}
	if !validRouteWebSocketResponseHeaders(stdhttp.Header{
		"Sec-WebSocket-Protocol": {"sforum.v1"},
		"X-Plugin-Metadata":      {"allowed"},
	}) {
		t.Fatal("declared subprotocol and ordinary metadata were rejected")
	}
}

func TestValidRouteWebSocketSubprotocolReadsEveryRequestHeaderLine(t *testing.T) {
	app := fiber.New()
	app.Get("/subprotocol", func(c fiber.Ctx) error {
		if !validRouteWebSocketSubprotocol(c, []string{"second.v1"}) {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		if validRouteWebSocketSubprotocol(c, []string{"first.v1", "second.v1"}) {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	request := httptest.NewRequest(stdhttp.MethodGet, "/subprotocol", nil)
	request.Header.Add("Sec-WebSocket-Protocol", "first.v1")
	request.Header.Add("Sec-WebSocket-Protocol", "second.v1")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

func TestRouteWebSocketPluginHandshakeHeaderIsInvalidPreflight(t *testing.T) {
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.websocket.handshake", 'b')
	declaration := routeDispatcherManifestRoute(
		"stream.websocket.handshake.handler", extensionmanifest.RouteActionAdd, "/socket-handshake", stdhttp.MethodGet,
	)
	declaration.Mode = extensionmanifest.RouteModeWebSocket
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	session := &webSocketPreflightOrderSession{}
	sink := &routeV2RecordingStreamFailureSink{}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry},
		Steps: &streamHTTPTestInvoker{observe: true, start: routes.RouteStreamStart{
			Response: routes.DispatchResponse{
				Status: fiber.StatusSwitchingProtocols,
				Headers: stdhttp.Header{
					"Sec-WebSocket-Extensions": {"permessage-deflate"},
				},
			},
			Session: session,
		}},
		Guard: HostRouteGuardAuthorizer{}, StreamFailures: sink,
	})
	app := fiber.New()
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	request := httptest.NewRequest(stdhttp.MethodGet, "/socket-handshake", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Sec-WebSocket-Version", "13")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != fiber.StatusBadGateway || !session.cancelled.Load() {
		t.Fatalf("status=%d cancelled=%t", response.StatusCode, session.cancelled.Load())
	}
	events := sink.snapshot()
	if len(events) != 1 || events[0].CauseClass != routes.RouteStreamFailureInvalidPreflight {
		t.Fatalf("events=%#v", events)
	}
}

func TestRouteWebSocketClientFailuresDoNotRecordIncidents(t *testing.T) {
	tests := []struct {
		name string
		act  func(*websocket.Conn) error
	}{
		{name: "abnormal disconnect", act: func(connection *websocket.Conn) error {
			return connection.UnderlyingConn().Close()
		}},
		{name: "oversized message", act: func(connection *websocket.Conn) error {
			return connection.WriteMessage(
				websocket.BinaryMessage,
				bytes.Repeat([]byte("x"), extensionsruntime.MaxProtocolV2RouteChunkSize+1),
			)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newWebSocketTerminalBarrierSession()
			t.Cleanup(session.cleanup)
			sink := &routeV2RecordingStreamFailureSink{}
			traces := routes.NewRouteTraceRing(8)
			connection := dialRouteWebSocketTerminalTest(t, session, traces, sink)
			if err := test.act(connection); err != nil {
				t.Fatal(err)
			}
			waitRouteWebSocketSignal(t, session.done, "client failure cancellation")
			waitRouteWebSocketSignal(t, session.recvExited, "response pump exit")
			if events := sink.snapshot(); len(events) != 0 {
				t.Fatalf("events=%#v", events)
			}
			records := traces.RouteTraces(0)
			if !hasRouteTraceOutcome(records, routes.RouteTraceTransportFailed) ||
				hasRouteTraceOutcome(records, routes.RouteTraceCommitted) || session.sendCalls.Load() != 0 {
				t.Fatalf("traces=%#v sendCalls=%d", records, session.sendCalls.Load())
			}
		})
	}
}

func TestRouteWebSocketRuntimeFailureDispositionMatrix(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*webSocketTerminalBarrierSession)
		closeClient bool
		sendMessage bool
		wantClass   routes.RouteStreamFailureClass
	}{
		{name: "recv runtime crash", wantClass: routes.RouteStreamFailureRuntimeTransport},
		{name: "send runtime crash", sendMessage: true, configure: func(session *webSocketTerminalBarrierSession) {
			session.sendErr = errors.New("runtime send failed")
		}, wantClass: routes.RouteStreamFailureRuntimeTransport},
		{name: "close request runtime crash", closeClient: true, configure: func(session *webSocketTerminalBarrierSession) {
			session.closeRequestErr = errors.New("runtime close failed")
		}, wantClass: routes.RouteStreamFailureRuntimeTransport},
		{name: "missing terminal", configure: func(session *webSocketTerminalBarrierSession) {
			session.recvErr = io.EOF
			session.missingResponse = true
		}, wantClass: routes.RouteStreamFailureMissingTerminal},
		{name: "terminal status drift", configure: func(session *webSocketTerminalBarrierSession) {
			session.recvErr = io.EOF
			session.response = routes.DispatchResponse{Status: fiber.StatusOK}
		}, wantClass: routes.RouteStreamFailureRuntimeTransport},
		{name: "typed EOF incident", configure: func(session *webSocketTerminalBarrierSession) {
			session.recvErr = routes.WithRouteStreamIncident(io.EOF, routes.RouteStreamFailureMissingTerminal)
			session.response = routes.DispatchResponse{Status: fiber.StatusSwitchingProtocols}
		}, wantClass: routes.RouteStreamFailureMissingTerminal},
		{name: "host budget", configure: func(session *webSocketTerminalBarrierSession) {
			session.recvErr = routes.WithRouteStreamIncident(
				routes.ErrRouteStreamBudgetExceeded,
				routes.RouteStreamFailureHostBudget,
			)
		}, wantClass: routes.RouteStreamFailureHostBudget},
		{name: "force drain", configure: func(session *webSocketTerminalBarrierSession) {
			session.recvErr = routes.WithRouteStreamAbort(extensionsruntime.ErrRuntimeAdmissionForced)
		}},
		{name: "normal terminal", configure: func(session *webSocketTerminalBarrierSession) {
			session.recvErr = io.EOF
			session.response = routes.DispatchResponse{Status: fiber.StatusSwitchingProtocols}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newWebSocketTerminalBarrierSession()
			t.Cleanup(session.cleanup)
			if test.configure != nil {
				test.configure(session)
			}
			sink := &routeV2RecordingStreamFailureSink{}
			connection := dialRouteWebSocketTerminalTest(t, session, routes.NewRouteTraceRing(8), sink)
			if test.sendMessage {
				if err := connection.WriteMessage(websocket.BinaryMessage, []byte("request")); err != nil {
					t.Fatal(err)
				}
			} else if test.closeClient {
				if err := connection.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(time.Second),
				); err != nil {
					t.Fatal(err)
				}
				waitRouteWebSocketSignal(t, session.requestClosed, "request close")
			} else {
				session.release()
			}
			waitRouteWebSocketSignal(t, session.done, "runtime terminal cancellation")
			waitRouteWebSocketSignal(t, session.recvExited, "response pump exit")
			events := sink.snapshot()
			if test.wantClass == "" {
				if len(events) != 0 {
					t.Fatalf("events=%#v", events)
				}
				return
			}
			if len(events) != 1 || events[0].CauseClass != test.wantClass {
				t.Fatalf("events=%#v want=%q", events, test.wantClass)
			}
		})
	}
}

func TestPumpWebSocketResponsesFailureDisposition(t *testing.T) {
	for _, test := range []struct {
		name         string
		session      routes.RouteStreamSession
		writer       *routeWebSocketFailureWriter
		wantClass    routes.RouteStreamFailureClass
		wantWrites   int
		wantControls int
	}{
		{name: "message write", session: &streamHTTPTestSession{chunks: []routes.RouteStreamChunk{{Sequence: 1, Data: []byte("payload")}}}, writer: &routeWebSocketFailureWriter{messageErr: io.ErrClosedPipe}, wantWrites: 1},
		{name: "close control", session: &streamHTTPTestSession{response: routes.DispatchResponse{Status: fiber.StatusSwitchingProtocols}}, writer: &routeWebSocketFailureWriter{controlErr: io.ErrClosedPipe}, wantControls: 1},
		{name: "oversized runtime output", session: &streamHTTPTestSession{chunks: []routes.RouteStreamChunk{{Sequence: 1, Data: bytes.Repeat([]byte("x"), extensionsruntime.MaxProtocolV2RouteChunkSize+1)}}}, writer: &routeWebSocketFailureWriter{}, wantClass: routes.RouteStreamFailureRuntimeTransport},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := pumpWebSocketResponses(test.writer, test.session)
			if result.normal || result.err == nil {
				t.Fatalf("result=%#v", result)
			}
			if test.writer.messageCalls != test.wantWrites {
				t.Fatalf("message writes=%d want=%d", test.writer.messageCalls, test.wantWrites)
			}
			if test.writer.controlCalls != test.wantControls {
				t.Fatalf("control writes=%d want=%d", test.writer.controlCalls, test.wantControls)
			}
			dispatch, bound, sink := prepareWebSocketDispositionTest(t)
			_ = dispatch.StreamFailed(result.err)
			bound.Cancel()
			events := sink.snapshot()
			if test.wantClass == "" && len(events) != 0 {
				t.Fatalf("events=%#v", events)
			}
			if test.wantClass != "" && (len(events) != 1 || events[0].CauseClass != test.wantClass) {
				t.Fatalf("events=%#v want=%q", events, test.wantClass)
			}
		})
	}
}

type routeWebSocketFailureWriter struct {
	messageErr   error
	controlErr   error
	messageCalls int
	controlCalls int
}

func (w *routeWebSocketFailureWriter) WriteMessage(int, []byte) error {
	w.messageCalls++
	return w.messageErr
}

func (w *routeWebSocketFailureWriter) WriteControl(int, []byte, time.Time) error {
	w.controlCalls++
	return w.controlErr
}

func prepareWebSocketDispositionTest(
	t *testing.T,
) (*routes.RouteStreamDispatch, routes.RouteStreamSession, *routeV2RecordingStreamFailureSink) {
	t.Helper()
	registry := routes.NewRegistry()
	artifact := routeDispatcherArtifact("stream.websocket.disposition", 'd')
	declaration := routeDispatcherManifestRoute(
		"stream.websocket.disposition.handler",
		extensionmanifest.RouteActionAdd,
		"/socket-disposition",
		stdhttp.MethodGet,
	)
	declaration.Mode = extensionmanifest.RouteModeWebSocket
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{declaration},
	}}}); err != nil {
		t.Fatal(err)
	}
	sink := &routeV2RecordingStreamFailureSink{}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry},
		Steps: &streamHTTPTestInvoker{observe: true, start: routes.RouteStreamStart{
			Response: routes.DispatchResponse{Status: fiber.StatusSwitchingProtocols},
			Session:  &streamHTTPTestSession{},
		}},
		Guard: HostRouteGuardAuthorizer{}, StreamFailures: sink,
	})
	prepared, err := dispatcher.PrepareStream(
		context.Background(), routes.DispatchRequest{Method: stdhttp.MethodGet, Path: "/socket-disposition"},
	)
	if err != nil {
		t.Fatal(err)
	}
	start, err := prepared.Dispatch.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepared.Dispatch.ResponseStarted()
	return prepared.Dispatch, start.Session, sink
}
