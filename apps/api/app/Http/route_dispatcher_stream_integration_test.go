package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const routeStreamE2EHelperEnv = "route-stream-http-e2e"

const (
	routeStreamBackpressureChunkSize     = 1 << 20
	routeStreamBackpressureChunks        = 16
	routeStreamBackpressureStartedEnv    = "SFORUM_ROUTE_STREAM_BACKPRESSURE_STARTED"
	routeStreamBackpressureCompletedEnv  = "SFORUM_ROUTE_STREAM_BACKPRESSURE_COMPLETED"
	routeStreamBackpressureStartedFile   = ".backpressure-started"
	routeStreamBackpressureCompletedFile = ".backpressure-completed"
)

var routeStreamE2ECorrelations sync.Map

func TestRouteStreamAcrossFiberManagerAndRealProtocolV2Process(t *testing.T) {
	extension := routeStreamE2EExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: routeStreamE2ETrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "stream-grant", ImpactDigest: "stream-impact"}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	registry := routes.NewRegistry()
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: routes.PluginArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
			RuntimeInstanceID: runtime.Identity.InstanceID,
		},
		Routes: extension.Manifest.Routes,
	}}}); err != nil {
		t.Fatal(err)
	}
	traces := routes.NewRouteTraceRing(32)
	streamFailures := &routeV2RecordingStreamFailureSink{}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(manager),
		Guard: HostRouteGuardAuthorizer{}, Trace: traces, StreamFailures: streamFailures,
	})
	app := fiber.New(fiber.Config{StreamRequestBody: true, DisablePreParseMultipartForm: true})
	app.Use(routeDispatcherMiddleware(dispatcher, nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener = routeStreamE2ESmallBufferListener{Listener: listener}
	serverDone := make(chan error, 1)
	go func() { serverDone <- app.Listener(listener) }()
	t.Cleanup(func() {
		_ = app.Shutdown()
		_ = listener.Close()
		<-serverDone
	})
	baseHTTP := "http://" + listener.Addr().String()
	minimumTraces := 0

	t.Run("multipart bounded upload", func(t *testing.T) {
		minimumTraces += 2
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("upload", "payload.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(bytes.Repeat([]byte("m"), extensionsruntime.MaxProtocolV2RouteChunkSize+257)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request, err := stdhttp.NewRequest(stdhttp.MethodPost, baseHTTP+"/upload", bytes.NewReader(body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response, err := (&stdhttp.Client{Timeout: 10 * time.Second}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var received, maximum int
		if _, err := fmt.Sscanf(string(payload), "bytes=%d max=%d", &received, &maximum); err != nil {
			t.Fatalf("decode stream evidence %q: %v", payload, err)
		}
		if response.StatusCode != stdhttp.StatusCreated || received != body.Len() || maximum <= 0 ||
			maximum > extensionsruntime.MaxProtocolV2RouteChunkSize {
			t.Fatalf("status=%d bytes=%d/%d max=%d", response.StatusCode, received, body.Len(), maximum)
		}
	})

	t.Run("SSE", func(t *testing.T) {
		minimumTraces += 2
		response, err := (&stdhttp.Client{Timeout: 10 * time.Second}).Get(baseHTTP + "/events")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != stdhttp.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" ||
			string(payload) != "data: one\n\ndata: two\n\n" {
			t.Fatalf("status=%d headers=%v body=%q", response.StatusCode, response.Header, payload)
		}
	})

	t.Run("opaque binary stream", func(t *testing.T) {
		minimumTraces += 2
		response, err := (&stdhttp.Client{Timeout: 10 * time.Second}).Get(baseHTTP + "/binary")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != stdhttp.StatusOK ||
			response.Header.Get("Content-Type") != "application/octet-stream" ||
			!bytes.Equal(payload, routeStreamE2EOpaquePayload()) {
			t.Fatalf("status=%d headers=%v body=%x", response.StatusCode, response.Header, payload)
		}
	})

	t.Run("TCP slow consumer applies bounded backpressure", func(t *testing.T) {
		minimumTraces += 2
		connection, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		tcpConnection, ok := connection.(*net.TCPConn)
		if !ok {
			t.Fatalf("connection type %T is not TCP", connection)
		}
		if err := tcpConnection.SetReadBuffer(4 << 10); err != nil {
			t.Fatal(err)
		}
		if err := connection.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
			t.Fatal(err)
		}
		request, err := stdhttp.NewRequest(stdhttp.MethodGet, baseHTTP+"/backpressure", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Close = true
		if err := request.Write(connection); err != nil {
			t.Fatal(err)
		}
		response, err := stdhttp.ReadResponse(bufio.NewReaderSize(connection, 1<<10), request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != stdhttp.StatusOK || response.Header.Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("status=%d headers=%v", response.StatusCode, response.Header)
		}

		startedPath := filepath.Join(extension.PackagePath, routeStreamBackpressureStartedFile)
		completedPath := filepath.Join(extension.PackagePath, routeStreamBackpressureCompletedFile)
		poll := time.NewTicker(5 * time.Millisecond)
		defer poll.Stop()
		startedDeadline := time.NewTimer(5 * time.Second)
		defer startedDeadline.Stop()
		for {
			_, statErr := os.Stat(startedPath)
			if statErr == nil {
				break
			}
			if !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal(statErr)
			}
			select {
			case <-startedDeadline.C:
				t.Fatal("subprocess did not start the backpressure stream")
			case <-poll.C:
			}
		}

		// The child writes completed only after every synchronous gRPC Send. With
		// both TCP windows restricted, its absence proves the producer is blocked
		// instead of placing the entire response in an unbounded Host queue.
		hold := time.NewTimer(200 * time.Millisecond)
		defer hold.Stop()
		holding := true
		for holding {
			if _, statErr := os.Stat(completedPath); statErr == nil {
				t.Fatal("subprocess completed while the TCP consumer was paused")
			} else if !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal(statErr)
			}
			snapshot, inspectErr := manager.InspectRuntimeInstance(runtime.Identity)
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			if snapshot.Admission.ActiveTotal != 1 {
				t.Fatalf("slow consumer did not retain one active stream: %#v", snapshot.Admission)
			}
			select {
			case <-hold.C:
				holding = false
			case <-poll.C:
			}
		}

		if err := tcpConnection.SetReadBuffer(1 << 20); err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		wantSize := routeStreamBackpressureChunkSize * routeStreamBackpressureChunks
		if len(payload) != wantSize || bytes.Count(payload, []byte{0xa5}) != wantSize {
			t.Fatalf("backpressure payload size=%d want=%d", len(payload), wantSize)
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			_, completedErr := os.Stat(completedPath)
			completed := completedErr == nil
			if completedErr != nil && !errors.Is(completedErr, os.ErrNotExist) {
				t.Fatal(completedErr)
			}
			snapshot, inspectErr := manager.InspectRuntimeInstance(runtime.Identity)
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			if completed && snapshot.Admission.ActiveTotal == 0 {
				break
			}
			select {
			case <-deadline.C:
				t.Fatalf("consumed stream admission did not drain: %#v", snapshot.Admission)
			case <-poll.C:
			}
		}
	})

	t.Run("WebSocket", func(t *testing.T) {
		minimumTraces += 2
		dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: []string{"sforum.stream.v1"}}
		headers := make(stdhttp.Header)
		headers.Set("Cookie", "session=websocket-secret")
		headers.Set("Authorization", "Bearer websocket-secret")
		headers.Set("X-API-Key", "websocket-api-key")
		headers.Set("X-Auth-Token", "websocket-auth-token")
		connection, response, err := dialer.Dial("ws://"+listener.Addr().String()+"/socket", headers)
		if err != nil {
			t.Fatalf("dial response=%v err=%v", response, err)
		}
		defer connection.Close()
		if err := connection.WriteMessage(websocket.TextMessage, []byte("real-process")); err != nil {
			t.Fatal(err)
		}
		messageType, payload, err := connection.ReadMessage()
		if err != nil || messageType != websocket.BinaryMessage || string(payload) != "real-process" {
			t.Fatalf("messageType=%d payload=%q err=%v", messageType, payload, err)
		}
	})

	t.Run("disconnect cancels exact runtime admission", func(t *testing.T) {
		minimumTraces += 2
		response, err := (&stdhttp.Client{}).Get(baseHTTP + "/disconnect")
		if err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 64)
		if _, err := io.ReadFull(response.Body, buffer); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		deadline := time.Now().Add(5 * time.Second)
		for {
			snapshot, inspectErr := manager.InspectRuntimeInstance(runtime.Identity)
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			if snapshot.Admission.ActiveTotal == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("stream admission did not drain: %#v", snapshot.Admission)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("force drain releases bound lifetime without recv", func(t *testing.T) {
		minimumTraces++
		prepared, err := dispatcher.PrepareStream(context.Background(), routes.DispatchRequest{
			Method: stdhttp.MethodGet, Path: "/disconnect",
		})
		if err != nil || prepared.Dispatch == nil {
			t.Fatalf("prepared=%#v err=%v", prepared, err)
		}
		start, err := prepared.Dispatch.Open(context.Background())
		if err != nil || start.Session == nil {
			t.Fatalf("start=%#v err=%v", start, err)
		}
		source, ok := start.Session.(routes.RouteStreamLifetimeSource)
		if !ok {
			t.Fatal("bound production stream has no lifetime source")
		}
		forceCause := fmt.Errorf("trusted route force drain")
		if _, err := manager.ForceDrain(runtime.Identity, forceCause); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			snapshot, inspectErr := manager.InspectRuntimeInstance(runtime.Identity)
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			if snapshot.Admission.ActiveTotal == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("forced stream admission did not drain: %#v", snapshot.Admission)
			}
			time.Sleep(10 * time.Millisecond)
		}
		select {
		case <-source.Done():
			t.Fatal("inner ForceDrain published outer Done before adapter trace")
		default:
		}
		if err := prepared.Dispatch.StreamAborted(forceCause); !errors.Is(err, forceCause) {
			t.Fatalf("forced stream abort=%v want %v", err, forceCause)
		}
		start.Session.Cancel()
		select {
		case <-source.Done():
		case <-time.After(time.Second):
			t.Fatal("adapter Cancel did not publish forced stream Done")
		}
		if !errors.Is(source.Cause(), forceCause) {
			t.Fatalf("bound force cause=%v want %v", source.Cause(), forceCause)
		}
	})

	if records := traces.RouteTraces(32); len(records) < minimumTraces {
		t.Fatalf("real stream traces=%#v", records)
	}
	if events := streamFailures.snapshot(); len(events) != 0 {
		t.Fatalf("normal, disconnect, and ForceDrain paths recorded incidents: %#v", events)
	}
}

func TestRouteStreamHTTPHelperProcess(t *testing.T) {
	if os.Getenv("SFORUM_PLUGIN_HELPER") != routeStreamE2EHelperEnv {
		return
	}
	server := pluginv2sdk.NewServer().
		WithFeatures(&protocolwire.ProtocolFeature{Name: "stream.routes", Version: "1"}).
		WithRuntimeStreams(pluginv2sdk.RuntimeStreams{Route: routeStreamE2EHandler})
	pluginv2sdk.Serve(&routeStreamE2EServer{Server: server})
	os.Exit(0)
}

type routeStreamE2EServer struct{ *pluginv2sdk.Server }

func (s *routeStreamE2EServer) InvokeRoute(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	traceID := request.GetContext().GetTrace().GetTraceId()
	if traceID == "" {
		return nil, fmt.Errorf("stream preflight trace id is empty")
	}
	routeStreamE2ECorrelations.Store(traceID, request.GetRouteId())
	status := uint32(stdhttp.StatusOK)
	headers := []*protocolwire.Header{}
	switch request.GetRouteId() {
	case "runtime.stream.upload":
		status = stdhttp.StatusCreated
	case "runtime.stream.events", "runtime.stream.disconnect":
		headers = append(headers, &protocolwire.Header{Name: "Content-Type", Values: []string{"text/event-stream"}})
	case "runtime.stream.binary", "runtime.stream.backpressure":
		headers = append(headers, &protocolwire.Header{Name: "Content-Type", Values: []string{"application/octet-stream"}})
	case "runtime.stream.websocket":
		if header := routeStreamE2EForwardedCredential(request.GetHeaders()); header != "" {
			return nil, fmt.Errorf("filtered WebSocket preflight forwarded credential %s", header)
		}
		status = stdhttp.StatusSwitchingProtocols
		headers = append(headers, &protocolwire.Header{Name: "Sec-WebSocket-Protocol", Values: []string{"sforum.stream.v1"}})
	default:
		return &pluginwire.RouteResponse{
			Context: routeStreamE2EResponseContext(request.GetContext()),
			Error:   &protocolwire.ErrorDetail{Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "route.not_found"},
		}, nil
	}
	return &pluginwire.RouteResponse{
		Context: routeStreamE2EResponseContext(request.GetContext()), StatusCode: status,
		Headers: headers, StreamFollows: true,
	}, nil
}

func routeStreamE2EHandler(stream *pluginv2sdk.RouteStream) error {
	routeID := stream.Open().GetRouteId()
	streamTrace := stream.Open().GetContext().GetTrace().GetTraceId()
	preflightRoute, ok := routeStreamE2ECorrelations.LoadAndDelete(streamTrace)
	if !ok || streamTrace == "" || preflightRoute != routeID {
		return fmt.Errorf("stream correlation mismatch: preflight=%v stream=%q route=%q", preflightRoute, streamTrace, routeID)
	}
	switch routeID {
	case "runtime.stream.upload":
		total, maximum := 0, 0
		for {
			chunk, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			total += len(chunk.GetData())
			if len(chunk.GetData()) > maximum {
				maximum = len(chunk.GetData())
			}
		}
		payload := []byte("bytes=" + strconv.Itoa(total) + " max=" + strconv.Itoa(maximum))
		if err := stream.Send(&protocolwire.DataChunk{Sequence: 1, Data: payload, Final: true}); err != nil {
			return err
		}
		return stream.Close(&pluginwire.RouteStreamClose{StatusCode: stdhttp.StatusCreated})
	case "runtime.stream.events":
		if _, err := stream.Recv(); err != io.EOF {
			return err
		}
		for index, payload := range [][]byte{[]byte("data: one\n\n"), []byte("data: two\n\n")} {
			if err := stream.Send(&protocolwire.DataChunk{Sequence: uint64(index + 1), Data: payload, Final: index == 1}); err != nil {
				return err
			}
		}
		return stream.Close(&pluginwire.RouteStreamClose{StatusCode: stdhttp.StatusOK})
	case "runtime.stream.binary":
		if _, err := stream.Recv(); err != io.EOF {
			return err
		}
		payload := routeStreamE2EOpaquePayload()
		if err := stream.Send(&protocolwire.DataChunk{Sequence: 1, Data: payload[:3]}); err != nil {
			return err
		}
		if err := stream.Send(&protocolwire.DataChunk{Sequence: 2, Data: payload[3:], Final: true}); err != nil {
			return err
		}
		return stream.Close(&pluginwire.RouteStreamClose{StatusCode: stdhttp.StatusOK})
	case "runtime.stream.backpressure":
		if _, err := stream.Recv(); err != io.EOF {
			return err
		}
		startedPath := strings.TrimSpace(os.Getenv(routeStreamBackpressureStartedEnv))
		completedPath := strings.TrimSpace(os.Getenv(routeStreamBackpressureCompletedEnv))
		if startedPath == "" || completedPath == "" {
			return fmt.Errorf("backpressure marker paths are empty")
		}
		if err := os.WriteFile(startedPath, []byte("started\n"), 0o600); err != nil {
			return err
		}
		payload := bytes.Repeat([]byte{0xa5}, routeStreamBackpressureChunkSize)
		for index := 0; index < routeStreamBackpressureChunks; index++ {
			if err := stream.Send(&protocolwire.DataChunk{
				Sequence: uint64(index + 1), Data: payload, Final: index == routeStreamBackpressureChunks-1,
			}); err != nil {
				return err
			}
		}
		if err := os.WriteFile(completedPath, []byte("completed\n"), 0o600); err != nil {
			return err
		}
		return stream.Close(&pluginwire.RouteStreamClose{StatusCode: stdhttp.StatusOK})
	case "runtime.stream.websocket":
		if header := routeStreamE2EForwardedCredential(stream.Open().GetHeaders()); header != "" {
			return fmt.Errorf("filtered WebSocket open forwarded credential %s", header)
		}
		chunk, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := stream.Send(&protocolwire.DataChunk{Sequence: 1, Data: chunk.GetData()}); err != nil {
			return err
		}
		return stream.Close(&pluginwire.RouteStreamClose{StatusCode: stdhttp.StatusSwitchingProtocols})
	case "runtime.stream.disconnect":
		if _, err := stream.Recv(); err != io.EOF {
			return err
		}
		payload := bytes.Repeat([]byte("d"), 32<<10)
		for sequence := uint64(1); ; sequence++ {
			if err := stream.Send(&protocolwire.DataChunk{Sequence: sequence, Data: payload}); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown stream route")
	}
}

func routeStreamE2EForwardedCredential(headers []*protocolwire.Header) string {
	for _, header := range headers {
		switch strings.ToLower(strings.TrimSpace(header.GetName())) {
		case "cookie", "authorization", "x-api-key", "x-auth-token":
			return header.GetName()
		}
	}
	return ""
}

func routeStreamE2EResponseContext(request *protocolwire.RequestContext) *protocolwire.ResponseContext {
	return &protocolwire.ResponseContext{
		RequestId: request.GetRequestId(), Trace: proto.Clone(request.GetTrace()).(*protocolwire.TraceContext),
		Extension: proto.Clone(request.GetExtension()).(*protocolwire.ExtensionIdentity), ServerTime: timestamppb.Now(),
	}
}

func routeStreamE2EExtension(t *testing.T) extensions.Extension {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "runtime.stream", "1.0.0")
	if err := os.MkdirAll(filepath.Join(packageRoot, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_HELPER=" + routeStreamShellQuote(routeStreamE2EHelperEnv) + " " +
		routeStreamBackpressureStartedEnv + "=" + routeStreamShellQuote(filepath.Join(packageRoot, routeStreamBackpressureStartedFile)) + " " +
		routeStreamBackpressureCompletedEnv + "=" + routeStreamShellQuote(filepath.Join(packageRoot, routeStreamBackpressureCompletedFile)) + " " +
		"exec " + routeStreamShellQuote(os.Args[0]) + " -test.run=TestRouteStreamHTTPHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(packageRoot, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	route := func(id, path, method, mode string) extensions.ManifestRoute {
		value := extensions.ManifestRoute{
			ID: id, ContractVersion: id + "@1", Action: extensionmanifest.RouteActionAdd,
			Path: path, Methods: []string{method}, Guard: extensionmanifest.GuardCorePublic,
			Fallback: "closed", Mode: mode, Handler: "route.stream", ResponseSchema: id + ".response@1",
		}
		if method != stdhttp.MethodGet {
			value.RequestSchema = id + ".request@1"
		}
		return value
	}
	return extensions.Extension{
		ID: "runtime.stream", Name: "Runtime Stream", Version: "1.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: strings.Repeat("a", 64), PackagePath: packageRoot,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "runtime.stream", Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2",
			},
			Routes: []extensions.ManifestRoute{
				route("runtime.stream.upload", "/upload", stdhttp.MethodPost, extensionmanifest.RouteModeMultipart),
				route("runtime.stream.events", "/events", stdhttp.MethodGet, extensionmanifest.RouteModeSSE),
				route("runtime.stream.binary", "/binary", stdhttp.MethodGet, extensionmanifest.RouteModeStream),
				route("runtime.stream.backpressure", "/backpressure", stdhttp.MethodGet, extensionmanifest.RouteModeStream),
				route("runtime.stream.websocket", "/socket", stdhttp.MethodGet, extensionmanifest.RouteModeWebSocket),
				route("runtime.stream.disconnect", "/disconnect", stdhttp.MethodGet, extensionmanifest.RouteModeSSE),
			},
		},
	}
}

func routeStreamE2EOpaquePayload() []byte {
	return []byte{0x00, 0xff, 0x53, 0x46, 0x80, 0x0a, 0x00, 0xfe}
}

type routeStreamE2ESmallBufferListener struct {
	net.Listener
}

func (l routeStreamE2ESmallBufferListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tcpConnection, ok := connection.(*net.TCPConn)
	if !ok {
		_ = connection.Close()
		return nil, fmt.Errorf("route stream listener accepted %T, want TCP", connection)
	}
	if err := tcpConnection.SetWriteBuffer(4 << 10); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func routeStreamShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type routeStreamE2ETrust struct {
	identity extensions.RuntimeTrustIdentity
}

func (s routeStreamE2ETrust) RuntimeIdentity(context.Context, extensions.Extension) (extensions.RuntimeTrustIdentity, error) {
	return s.identity, nil
}

var _ pluginwire.PluginRuntimeServiceServer = (*routeStreamE2EServer)(nil)
