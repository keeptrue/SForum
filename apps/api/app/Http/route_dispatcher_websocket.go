package http

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const (
	routeWebSocketControlTimeout = 2 * time.Second
	// A peer Close ends the WebSocket, so it gets a short Host-owned terminal
	// grace instead of retaining the ordinary long-lived route deadline.
	routeWebSocketResponseTerminalGrace = 2 * time.Second
)

func serveRouteWebSocket(c fiber.Ctx, dispatch *routes.RouteStreamDispatch, hostHeaders stdhttp.Header) error {
	if c == nil || dispatch == nil || !validRouteWebSocketUpgrade(c) {
		return fiber.NewError(fiber.StatusUpgradeRequired, "extensions.websocket_upgrade_required")
	}
	// Guard evaluation and runtime preflight happen before hijacking, so they must
	// still observe request cancellation. The bridge below owns the longer-lived
	// connection only after the upgrade succeeds.
	start, err := dispatch.Open(c.Context())
	if err != nil {
		return mapRouteDispatchError(err)
	}
	if !validRouteWebSocketPreflight(c, start.Response) {
		// Fail before Cancel so the transport trace lands before lifetime Done.
		failure := dispatch.StreamFailedAs(
			routes.RouteStreamFailureInvalidPreflight,
			fmt.Errorf("%w: invalid websocket preflight", routes.ErrDispatchTransport),
		)
		start.Session.Cancel()
		return mapRouteDispatchError(failure)
	}
	c.Response().Reset()
	for name, values := range start.Response.Headers {
		for _, value := range values {
			c.Response().Header.Add(name, value)
		}
	}
	restoreHostRouteResponseHeaders(c, hostHeaders)
	upgrader := websocket.FastHTTPUpgrader{}
	if err := upgrader.Upgrade(c.RequestCtx(), func(connection *websocket.Conn) {
		// ResponseStarted publishes the success trace before any later cancel path
		// can race session completion visibility.
		dispatch.ResponseStarted()
		// Detach only after a successful Upgrade so request cancel no longer kills
		// the stream; Host budget and ForceCancel remain authoritative.
		if detacher, ok := start.Session.(routes.RouteStreamCallerDetacher); ok {
			if detachErr := detacher.DetachCaller(); detachErr != nil {
				// Fail before Cancel so the transport trace lands before Done.
				_ = dispatch.StreamFailed(classifyRouteWebSocketDetachFailure(detachErr))
				start.Session.Cancel()
				_ = connection.Close()
				return
			}
		}
		bridgeRouteWebSocket(connection, start.Session, dispatch)
	}); err != nil {
		// Fail before Cancel: trace must be visible before session completion.
		_ = dispatch.StreamAborted(err)
		start.Session.Cancel()
		// FastHTTPUpgrader has already authored the exact handshake error.
		return nil
	}
	return nil
}

func classifyRouteWebSocketDetachFailure(err error) error {
	if _, _, classified := routes.InspectRouteStreamFailureDisposition(err); classified {
		return err
	}
	if errors.Is(err, routes.ErrRouteStreamBudgetExceeded) {
		return routes.WithRouteStreamIncident(err, routes.RouteStreamFailureHostBudget)
	}
	return routes.WithRouteStreamAbort(err)
}

func classifyRouteWebSocketRuntimeFailure(err error) error {
	if err == nil {
		return nil
	}
	if _, _, classified := routes.InspectRouteStreamFailureDisposition(err); classified {
		return err
	}
	switch {
	case errors.Is(err, routes.ErrRouteStreamBudgetExceeded):
		return routes.WithRouteStreamIncident(err, routes.RouteStreamFailureHostBudget)
	case errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return routes.WithRouteStreamAbort(err)
	default:
		return err
	}
}

func validRouteWebSocketUpgrade(c fiber.Ctx) bool {
	if c == nil || c.Method() != fiber.MethodGet || !websocket.FastHTTPIsWebSocketUpgrade(c.RequestCtx()) {
		return false
	}
	versions := c.Request().Header.PeekAll("Sec-WebSocket-Version")
	if len(versions) != 1 || strings.TrimSpace(string(versions[0])) != "13" {
		return false
	}
	keys := c.Request().Header.PeekAll("Sec-WebSocket-Key")
	if len(keys) != 1 {
		return false
	}
	nonce, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(keys[0])))
	if err != nil || len(nonce) != 16 {
		return false
	}
	origin := strings.TrimSpace(c.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) &&
		parsed.User == nil && (parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == "" && parsed.Fragment == "" &&
		strings.EqualFold(parsed.Host, c.Get("Host"))
}

func validRouteWebSocketPreflight(c fiber.Ctx, response routes.DispatchResponse) bool {
	if response.Status != fiber.StatusSwitchingProtocols || !validRouteWebSocketResponseHeaders(response.Headers) {
		return false
	}
	return validRouteWebSocketSubprotocol(c, response.Headers.Values("Sec-WebSocket-Protocol"))
}

func validRouteWebSocketResponseHeaders(headers stdhttp.Header) bool {
	for name := range headers {
		canonical := strings.ToLower(strings.TrimSpace(name))
		if strings.HasPrefix(canonical, "sec-websocket-") && canonical != "sec-websocket-protocol" {
			return false
		}
	}
	return true
}

func validRouteWebSocketSubprotocol(c fiber.Ctx, selectedValues []string) bool {
	if len(selectedValues) == 0 {
		return true
	}
	if len(selectedValues) != 1 {
		return false
	}
	selected := strings.TrimSpace(selectedValues[0])
	if selected == "" || strings.Contains(selected, ",") {
		return false
	}
	for _, line := range c.Request().Header.PeekAll("Sec-WebSocket-Protocol") {
		for _, candidate := range strings.Split(string(line), ",") {
			if strings.TrimSpace(candidate) == selected {
				return true
			}
		}
	}
	return false
}

type routeWebSocketPumpResult struct {
	direction string
	normal    bool
	err       error
}

func bridgeRouteWebSocket(connection *websocket.Conn, session routes.RouteStreamSession, dispatch *routes.RouteStreamDispatch) {
	if connection == nil || session == nil || dispatch == nil {
		if session != nil {
			session.Cancel()
		}
		return
	}
	connection.SetReadLimit(extensionsruntime.MaxProtocolV2RouteChunkSize)
	results := make(chan routeWebSocketPumpResult, 2)
	go func() { results <- pumpWebSocketRequests(connection, session) }()
	go func() { results <- pumpWebSocketResponses(connection, session) }()

	terminal, drainOppositePump := awaitRouteWebSocketTerminal(results, routeWebSocketResponseTerminalGrace)
	if terminal.direction == "response" && terminal.normal {
		_ = dispatch.Complete()
	} else {
		_ = dispatch.StreamFailed(routeWebSocketPumpFailure(terminal.err))
	}
	session.Cancel()
	_ = connection.Close()
	// Closing both transports unblocks the opposite pump before this hijacked
	// handler releases the exact runtime admission lease.
	if drainOppositePump {
		<-results
	}
}

func awaitRouteWebSocketTerminal(
	results <-chan routeWebSocketPumpResult,
	timeout time.Duration,
) (routeWebSocketPumpResult, bool) {
	first := normalizeRouteWebSocketPumpResult(<-results, false)
	if !first.normal {
		if second, ok := pollRouteWebSocketPumpResult(results); ok {
			return preferQueuedRouteWebSocketPumpResult(first, second), false
		}
		return first, true
	}
	if first.direction == "response" {
		// A validated response terminal is authoritative. Cancel/drain may make a
		// later request-side CloseRequest fail, but cleanup cannot rewrite commit.
		return first, true
	}
	// A normal client Close only half-closes plugin input. It cannot retain the
	// ordinary 24-hour stream lease while waiting for a missing plugin terminal.
	if second, ok := pollRouteWebSocketPumpResult(results); ok {
		return normalizeRouteWebSocketPumpResult(second, true), false
	}
	if timeout <= 0 {
		return routeWebSocketMissingTerminalResult(), true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case second := <-results:
		return normalizeRouteWebSocketPumpResult(second, true), false
	case <-timer.C:
		if second, ok := pollRouteWebSocketPumpResult(results); ok {
			return normalizeRouteWebSocketPumpResult(second, true), false
		}
		return routeWebSocketMissingTerminalResult(), true
	}
}

func pollRouteWebSocketPumpResult(results <-chan routeWebSocketPumpResult) (routeWebSocketPumpResult, bool) {
	select {
	case result := <-results:
		return result, true
	default:
		return routeWebSocketPumpResult{}, false
	}
}

func normalizeRouteWebSocketPumpResult(result routeWebSocketPumpResult, requireResponse bool) routeWebSocketPumpResult {
	if !validRouteWebSocketPumpResult(result) || requireResponse && result.normal && result.direction != "response" {
		result.normal = false
		result.err = routes.WithRouteStreamAbort(routeWebSocketPumpFailure(result.err))
	}
	if !result.normal {
		result.err = routeWebSocketPumpFailure(result.err)
	}
	return result
}

func preferQueuedRouteWebSocketPumpResult(first, second routeWebSocketPumpResult) routeWebSocketPumpResult {
	second = normalizeRouteWebSocketPumpResult(second, first.direction == "request")
	if second.direction == "response" && second.normal {
		return second
	}
	if routeWebSocketFailureIsAbort(first.err) && !second.normal && !routeWebSocketFailureIsAbort(second.err) {
		return second
	}
	return first
}

func routeWebSocketFailureIsAbort(err error) bool {
	_, incident, classified := routes.InspectRouteStreamFailureDisposition(err)
	return classified && !incident
}

func routeWebSocketMissingTerminalResult() routeWebSocketPumpResult {
	return routeWebSocketPumpResult{
		direction: "response",
		err: routes.WithRouteStreamIncident(
			fmt.Errorf("websocket close terminal timed out"),
			routes.RouteStreamFailureMissingTerminal,
		),
	}
}

func validRouteWebSocketPumpResult(result routeWebSocketPumpResult) bool {
	return result.direction == "request" || result.direction == "response"
}

func routeWebSocketPumpFailure(err error) error {
	if _, _, classified := routes.InspectRouteStreamFailureDisposition(err); classified {
		return err
	}
	if err == nil || errors.Is(err, io.EOF) {
		return routes.WithRouteStreamAbort(routes.ErrDispatchTransport)
	}
	return classifyRouteWebSocketRuntimeFailure(err)
}

func pumpWebSocketRequests(connection *websocket.Conn, session routes.RouteStreamSession) routeWebSocketPumpResult {
	for {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			var closeError *websocket.CloseError
			normal := errors.As(err, &closeError) &&
				(closeError.Code == websocket.CloseNormalClosure || closeError.Code == websocket.CloseGoingAway)
			if normal {
				if closeErr := session.CloseRequest(); closeErr != nil {
					return routeWebSocketPumpResult{direction: "request", err: classifyRouteWebSocketRuntimeFailure(closeErr)}
				}
				return routeWebSocketPumpResult{direction: "request", normal: true, err: err}
			}
			return routeWebSocketPumpResult{direction: "request", err: routes.WithRouteStreamAbort(err)}
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		if len(payload) > extensionsruntime.MaxProtocolV2RouteChunkSize {
			return routeWebSocketPumpResult{
				direction: "request",
				err:       routes.WithRouteStreamAbort(fmt.Errorf("websocket message exceeds route chunk limit")),
			}
		}
		// RouteStreamFrame has one bounded binary DataChunk. One WebSocket
		// message maps to one chunk, preserving message boundaries without a
		// second framing protocol.
		if err := session.Send(payload, false); err != nil {
			return routeWebSocketPumpResult{direction: "request", err: classifyRouteWebSocketRuntimeFailure(err)}
		}
	}
}

type routeWebSocketResponseWriter interface {
	WriteMessage(int, []byte) error
	WriteControl(int, []byte, time.Time) error
}

func pumpWebSocketResponses(connection routeWebSocketResponseWriter, session routes.RouteStreamSession) routeWebSocketPumpResult {
	for {
		chunk, err := session.Recv()
		if err != nil {
			if _, _, classified := routes.InspectRouteStreamFailureDisposition(err); classified {
				return routeWebSocketPumpResult{direction: "response", err: err}
			}
		}
		if errors.Is(err, io.EOF) {
			response, ok := session.Response()
			if !ok {
				return routeWebSocketPumpResult{
					direction: "response",
					err: routes.WithRouteStreamIncident(
						routes.ErrDispatchTransport,
						routes.RouteStreamFailureMissingTerminal,
					),
				}
			}
			if response.Status != fiber.StatusSwitchingProtocols {
				return routeWebSocketPumpResult{
					direction: "response",
					err:       fmt.Errorf("%w: websocket terminal status drift", routes.ErrDispatchTransport),
				}
			}
			deadline := time.Now().Add(routeWebSocketControlTimeout)
			if err := connection.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				deadline,
			); err != nil && !errors.Is(err, websocket.ErrCloseSent) {
				return routeWebSocketPumpResult{direction: "response", err: routes.WithRouteStreamAbort(err)}
			}
			return routeWebSocketPumpResult{direction: "response", normal: true}
		}
		if err != nil {
			return routeWebSocketPumpResult{direction: "response", err: classifyRouteWebSocketRuntimeFailure(err)}
		}
		if len(chunk.Data) > extensionsruntime.MaxProtocolV2RouteChunkSize {
			return routeWebSocketPumpResult{
				direction: "response",
				err: routes.WithRouteStreamIncident(
					routes.ErrDispatchTransport,
					routes.RouteStreamFailureRuntimeTransport,
				),
			}
		}
		if err := connection.WriteMessage(websocket.BinaryMessage, chunk.Data); err != nil {
			return routeWebSocketPumpResult{direction: "response", err: routes.WithRouteStreamAbort(err)}
		}
	}
}
