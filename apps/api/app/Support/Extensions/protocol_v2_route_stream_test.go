package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestProtocolV2RouteStreamCarriesExactContextAndBoundedChunks(t *testing.T) {
	var receivedOpen *pluginwire.RouteStreamOpen
	client := newProtocolV2RouteStreamTestClient(t, func(stream grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error {
		first, err := stream.Recv()
		if err != nil {
			return err
		}
		receivedOpen = first.GetOpen()
		request, err := stream.Recv()
		if err != nil || request.GetChunk().GetSequence() != 1 || string(request.GetChunk().GetData()) != "request" || !request.GetChunk().GetFinal() {
			return ErrProtocolV2RouteStreamInvalid
		}
		if _, err := stream.Recv(); err != nil {
			return err
		}
		digest := sha256.Sum256([]byte("response"))
		if err := stream.Send(&pluginwire.RouteStreamFrame{Frame: &pluginwire.RouteStreamFrame_Chunk{Chunk: &protocolwire.DataChunk{
			Sequence: 1, Data: []byte("response"), Checksum: digest[:], Final: true,
		}}}); err != nil {
			return err
		}
		return stream.Send(&pluginwire.RouteStreamFrame{Frame: &pluginwire.RouteStreamFrame_Close{Close: &pluginwire.RouteStreamClose{
			StatusCode: http.StatusCreated, Headers: []*protocolwire.Header{{Name: "X-Stream", Values: []string{"done"}}},
		}}})
	})
	issuer := &recordingProtocolV2ActorDelegationIssuer{grants: []hostapi.ProtocolV2ActorDelegationGrant{{
		CommandID: "sforum.stream.write", CommandVersion: "1", IdempotencyKey: "stream-request-42", Token: "stream-token",
	}}}
	client.delegations = issuer
	client.hostCommands = true
	stream, err := client.OpenRouteStreamContext(context.Background(), ProtocolV2RouteStreamRequest{
		RouteID: "demo.stream", ContractVersion: "demo.stream@1", Method: http.MethodPost,
		Path: "/stream?part=1", Mode: extensionmanifest.RouteModeStream,
		Authority:      protocolV2FilteredHostRequestAuthority(),
		Headers:        http.Header{"X-Test": {"one", "two"}},
		Actor:          NewProtocolV2RouteActor(42, true, map[string]bool{"stream.write": true}),
		IdempotencyKey: "stream-request-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Cancel()
	if err := stream.Send([]byte("request"), true); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseRequest(); err != nil {
		t.Fatal(err)
	}
	chunk, err := stream.Recv()
	if err != nil || string(chunk.Data) != "response" || !chunk.Final {
		t.Fatalf("chunk=%#v err=%v", chunk, err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal err=%v", err)
	}
	response, ok := stream.Response()
	if !ok || response.StatusCode != http.StatusCreated || response.Headers.Get("X-Stream") != "done" {
		t.Fatalf("response=%#v ok=%t", response, ok)
	}
	if receivedOpen.GetRouteId() != "demo.stream" || receivedOpen.GetContractVersion() != "demo.stream@1" ||
		receivedOpen.GetPath() != "/stream?part=1" || receivedOpen.GetContext().GetActor().GetUserId() != 42 ||
		receivedOpen.GetRequestAuthorityMode() != pluginwire.RouteRequestAuthorityMode_ROUTE_REQUEST_AUTHORITY_MODE_FILTERED ||
		receivedOpen.GetGuardKind() != pluginwire.RouteGuardKind_ROUTE_GUARD_KIND_HOST ||
		receivedOpen.GetContext().GetIdempotencyKey() != "stream-request-42" ||
		len(receivedOpen.GetContext().GetHostCommandDelegations()) != 1 ||
		receivedOpen.GetContext().GetHostCommandDelegations()[0].GetToken() != "stream-token" || issuer.calls != 1 ||
		!reflect.DeepEqual(receivedOpen.GetContext().GetActor().GetPermissionKeys(), []string{"stream.write"}) ||
		len(receivedOpen.GetHeaders()) != 1 {
		t.Fatalf("open=%#v", receivedOpen)
	}
	remaining := time.Until(receivedOpen.GetContext().GetDeadline().AsTime())
	if remaining < 23*time.Hour || remaining > DefaultProtocolV2RouteStreamTimeout {
		t.Fatalf("default stream deadline remaining=%s", remaining)
	}
}

func TestProtocolV2RouteStreamRejectsDriftAndMalformedPeerFrames(t *testing.T) {
	client := newProtocolV2RouteStreamTestClient(t, func(stream grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		return stream.Send(&pluginwire.RouteStreamFrame{Frame: &pluginwire.RouteStreamFrame_Chunk{Chunk: &protocolwire.DataChunk{
			Sequence: 2, Data: []byte("out-of-order"), Final: true,
		}}})
	})
	base := ProtocolV2RouteStreamRequest{
		RouteID: "demo.stream", ContractVersion: "demo.stream@1", Method: http.MethodPost,
		Path: "/stream", Mode: extensionmanifest.RouteModeStream,
		Authority: protocolV2FilteredHostRequestAuthority(), Timeout: time.Second,
	}
	for name, mutate := range map[string]func(*ProtocolV2RouteStreamRequest){
		"route":    func(value *ProtocolV2RouteStreamRequest) { value.RouteID = "other" },
		"contract": func(value *ProtocolV2RouteStreamRequest) { value.ContractVersion = "demo.stream@2" },
		"mode":     func(value *ProtocolV2RouteStreamRequest) { value.Mode = extensionmanifest.RouteModeSSE },
		"method":   func(value *ProtocolV2RouteStreamRequest) { value.Method = http.MethodGet },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := client.OpenRouteStreamContext(context.Background(), request); !errors.Is(err, ErrProtocolV2RouteStreamInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	stream, err := client.OpenRouteStreamContext(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Cancel()
	if _, err := stream.Recv(); !errors.Is(err, ErrProtocolV2RouteStreamInvalid) {
		t.Fatalf("malformed chunk error=%v", err)
	}
}

func TestProtocolV2RouteStreamCloseRejectsInformationalStatusesExceptUpgrade(t *testing.T) {
	for _, test := range []struct {
		status int
		valid  bool
	}{
		{status: http.StatusContinue},
		{status: http.StatusEarlyHints},
		{status: http.StatusSwitchingProtocols, valid: true},
		{status: http.StatusOK, valid: true},
	} {
		stream := &ProtocolV2RouteStream{}
		err := stream.captureResponseClose(&pluginwire.RouteStreamClose{StatusCode: uint32(test.status)})
		if test.valid && err != nil {
			t.Fatalf("status=%d error=%v", test.status, err)
		}
		if !test.valid && !errors.Is(err, ErrProtocolV2RouteStreamInvalid) {
			t.Fatalf("status=%d error=%v", test.status, err)
		}
	}
}

func TestProtocolV2RouteStreamPropagatesCancellationAndBoundsRequests(t *testing.T) {
	accepted := make(chan struct{})
	client := newProtocolV2RouteStreamTestClient(t, func(stream grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		close(accepted)
		<-stream.Context().Done()
		return stream.Context().Err()
	})
	ctx, cancel := context.WithCancelCause(context.Background())
	stream, err := client.OpenRouteStreamContext(ctx, ProtocolV2RouteStreamRequest{
		RouteID: "demo.stream", ContractVersion: "demo.stream@1", Method: http.MethodPost,
		Path: "/stream", Mode: extensionmanifest.RouteModeStream,
		Authority: protocolV2FilteredHostRequestAuthority(), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(make([]byte, MaxProtocolV2RouteChunkSize+1), false); !errors.Is(err, ErrProtocolV2RouteStreamInvalid) {
		t.Fatalf("oversized request error=%v", err)
	}
	<-accepted
	forceCause := errors.New("planned route stream drain")
	cancel(errors.Join(ErrRuntimeAdmissionForced, forceCause))
	if _, err := stream.Recv(); !errors.Is(err, ErrRuntimeAdmissionForced) || !errors.Is(err, forceCause) {
		t.Fatalf("cancel error=%v", err)
	}
	if cause := context.Cause(stream.Context()); !errors.Is(cause, ErrRuntimeAdmissionForced) || !errors.Is(cause, forceCause) {
		t.Fatalf("stream context cause=%v", cause)
	}
}

func TestProtocolV2OperationCausePreservesHostCauseAndRuntimeFailures(t *testing.T) {
	forceCause := errors.New("planned drain")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.Join(ErrRuntimeAdmissionForced, forceCause))
	for _, wireErr := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		status.Error(codes.Canceled, "redacted"),
		status.Error(codes.DeadlineExceeded, "redacted"),
	} {
		normalized := protocolV2OperationCause(ctx, wireErr)
		if !errors.Is(normalized, ErrRuntimeAdmissionForced) || !errors.Is(normalized, forceCause) {
			t.Fatalf("wire=%v normalized=%v", wireErr, normalized)
		}
	}
	runtimeErr := status.Error(codes.Unavailable, "runtime stopped")
	if normalized := protocolV2OperationCause(ctx, runtimeErr); normalized != runtimeErr {
		t.Fatalf("distinguishable runtime error changed: %v", normalized)
	}
	if normalized := protocolV2OperationCause(context.Background(), status.Error(codes.Canceled, "peer canceled")); status.Code(normalized) != codes.Canceled {
		t.Fatalf("live context manufactured a Host cancellation: %v", normalized)
	}
}

func TestProtocolV2DeadlineKeepsEarlierParentCause(t *testing.T) {
	deadlineParent, cancelDeadline := context.WithTimeout(context.Background(), time.Hour)
	defer cancelDeadline()
	parent, cancelParent := context.WithCancelCause(deadlineParent)
	child, cancelChild := protocolV2Deadline(parent, 2*time.Hour)
	defer cancelChild()
	want := errors.New("exact parent cause")
	cancelParent(want)
	<-child.Done()
	if !errors.Is(context.Cause(child), want) {
		t.Fatalf("child cause=%v", context.Cause(child))
	}
}

func TestProtocolV2RouteStreamClassifiesMissingTerminal(t *testing.T) {
	client := newProtocolV2RouteStreamTestClient(t, func(stream grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		return nil
	})
	stream, err := client.OpenRouteStreamContext(context.Background(), ProtocolV2RouteStreamRequest{
		RouteID: "demo.stream", ContractVersion: "demo.stream@1", Method: http.MethodPost,
		Path: "/stream", Mode: extensionmanifest.RouteModeStream,
		Authority: protocolV2FilteredHostRequestAuthority(), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Cancel()
	if _, err := stream.Recv(); !errors.Is(err, ErrProtocolV2RouteStreamMissingTerminal) {
		t.Fatalf("missing terminal error=%v", err)
	}
}

func TestProtocolV2RouteStreamDoesNotReclassifyRemoteCancellation(t *testing.T) {
	client := newProtocolV2RouteStreamTestClient(t, func(stream grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		return status.Error(codes.Canceled, "plugin canceled its stream")
	})
	stream, err := client.OpenRouteStreamContext(context.Background(), ProtocolV2RouteStreamRequest{
		RouteID: "demo.stream", ContractVersion: "demo.stream@1", Method: http.MethodPost,
		Path: "/stream", Mode: extensionmanifest.RouteModeStream,
		Authority: protocolV2FilteredHostRequestAuthority(), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Cancel()
	if _, err := stream.Recv(); status.Code(err) != codes.Canceled {
		t.Fatalf("remote cancellation was reclassified: %v", err)
	}
}

type protocolV2RouteStreamTestServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer
	stream func(grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error
}

func (s *protocolV2RouteStreamTestServer) StreamRoute(stream grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error {
	return s.stream(stream)
}

func newProtocolV2RouteStreamTestClient(
	t *testing.T,
	stream func(grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error,
) *protocolV2Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(server, &protocolV2RouteStreamTestServer{stream: stream})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///route-stream-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: &protocolwire.ExtensionIdentity{
			ExtensionId: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
			TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: "runtime-1",
		},
		instance: "runtime-1",
		routes: []extensions.ManifestRoute{{
			ID: "demo.stream", ContractVersion: "demo.stream@1", Methods: []string{http.MethodPost},
			Mode: extensionmanifest.RouteModeStream, Guard: extensionmanifest.GuardCorePublic,
		}},
	})
}
