package pluginv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRuntimeStreamingHelpersRoundTripGeneratedClient(t *testing.T) {
	handshake := validHandshakeRequest()
	server := NewServer().WithRuntimeStreams(RuntimeStreams{
		Lifecycle: func(_ context.Context, request *protocolwire.LifecycleRequest, progress *ProgressStream) error {
			if !request.GetForced() || request.GetDryRun() {
				return fmt.Errorf("lifecycle forced=%t dry_run=%t", request.GetForced(), request.GetDryRun())
			}
			for _, update := range []*protocolwire.ProgressUpdate{
				{StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING, CompletedUnits: 1, TotalUnits: 2, Checkpoint: "half"},
				{StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, CompletedUnits: 2, TotalUnits: 2, Checkpoint: "done"},
			} {
				if err := progress.Send(update); err != nil {
					return err
				}
			}
			return nil
		},
		Job: func(_ context.Context, _ *pluginwire.JobRequest, progress *ProgressStream) error {
			return progress.Send(&protocolwire.ProgressUpdate{})
		},
		Route: func(stream *RouteStream) error {
			chunk, err := stream.Recv()
			if err != nil || string(chunk.GetData()) != "request" {
				return fmt.Errorf("route request chunk=%q err=%w", chunk.GetData(), err)
			}
			if _, err := stream.Recv(); err != io.EOF || stream.PeerClose() == nil {
				return fmt.Errorf("route request did not close: %w", err)
			}
			if err := stream.Send(&protocolwire.DataChunk{Sequence: 1, Data: []byte("response"), Final: true}); err != nil {
				return err
			}
			return stream.Close(&pluginwire.RouteStreamClose{StatusCode: 201})
		},
		File: func(stream *FileStream) error {
			chunk, err := stream.Recv()
			if err != nil || string(chunk.GetData()) != "upload" {
				return fmt.Errorf("file request chunk=%q err=%w", chunk.GetData(), err)
			}
			if _, err := stream.Recv(); err != io.EOF || stream.PeerClose().GetSize() != 6 {
				return fmt.Errorf("file request did not close: %w", err)
			}
			if err := stream.Send(&protocolwire.DataChunk{Sequence: 1, Data: []byte("stored"), Final: true}); err != nil {
				return err
			}
			return stream.Close(&pluginwire.FileClose{Size: 6, Digest: []byte("digest")})
		},
	})
	client := startRuntimeStreamTestServer(t, server, handshake)

	// The first handshake freezes handlers just like features and services.
	server.WithRuntimeStreams(RuntimeStreams{})

	lifecycleContext := runtimeStreamTestContext(handshake, "lifecycle-1")
	progress, err := RunLifecycleStream(context.Background(), client, &protocolwire.LifecycleRequest{
		Context: lifecycleContext, Action: protocolwire.LifecycleAction_LIFECYCLE_ACTION_ENABLE, StepId: "enable", Forced: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, state := range []protocolwire.ProgressState{
		protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
		protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
	} {
		update, err := progress.Recv()
		if err != nil || update.GetState() != state || update.GetContext().GetRequestId() != lifecycleContext.GetRequestId() || update.GetContext().GetExtension().GetInstanceId() != "instance-1" {
			t.Fatalf("progress %d = %#v, err=%v", index, update, err)
		}
	}
	if _, err := progress.Recv(); err != io.EOF {
		t.Fatalf("progress terminal error = %v", err)
	}

	jobProgress, err := ExecuteJobStream(context.Background(), client, &pluginwire.JobRequest{
		Context: runtimeStreamTestContext(handshake, "job-1"), JobId: "job-1", JobKind: "demo.sync", PayloadVersion: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := jobProgress.Recv()
	if err != nil || failed.GetState() != protocolwire.ProgressState_PROGRESS_STATE_FAILED || failed.GetError().GetReason() != "runtime.progress_invalid" {
		t.Fatalf("job failure = %#v, err=%v", failed, err)
	}

	route, err := OpenRouteStream(context.Background(), client, &pluginwire.RouteStreamOpen{
		Context: runtimeStreamTestContext(handshake, "route-1"), RouteId: "demo.route", ContractVersion: "1", Method: "POST", Path: "/demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := route.Send(&protocolwire.DataChunk{Sequence: 1, Data: []byte("request"), Final: true}); err != nil {
		t.Fatal(err)
	}
	if err := route.Close(&pluginwire.RouteStreamClose{}); err != nil {
		t.Fatal(err)
	}
	routeChunk, err := route.Recv()
	if err != nil || string(routeChunk.GetData()) != "response" {
		t.Fatalf("route response=%q err=%v", routeChunk.GetData(), err)
	}
	if _, err := route.Recv(); err != io.EOF || route.PeerClose().GetStatusCode() != 201 {
		t.Fatalf("route close=%#v err=%v", route.PeerClose(), err)
	}

	file, err := OpenFileStream(context.Background(), client, &pluginwire.FileOpen{
		Context: runtimeStreamTestContext(handshake, "file-1"), Operation: "upload", FileId: "file-1", Path: "data.bin", ExpectedSize: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Send(&protocolwire.DataChunk{Sequence: 1, Data: []byte("upload"), Final: true}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(&pluginwire.FileClose{Size: 6}); err != nil {
		t.Fatal(err)
	}
	fileChunk, err := file.Recv()
	if err != nil || string(fileChunk.GetData()) != "stored" {
		t.Fatalf("file response=%q err=%v", fileChunk.GetData(), err)
	}
	if _, err := file.Recv(); err != io.EOF || file.PeerClose().GetSize() != 6 || string(file.PeerClose().GetDigest()) != "digest" {
		t.Fatalf("file close=%#v err=%v", file.PeerClose(), err)
	}
}

func TestRuntimeStreamingHelpersRejectMalformedAndStaleOpens(t *testing.T) {
	handshake := validHandshakeRequest()
	server := NewServer().WithRuntimeStreams(RuntimeStreams{
		Route: func(*RouteStream) error { return nil },
		File:  func(*FileStream) error { return nil },
	})
	client := startRuntimeStreamTestServer(t, server, handshake)

	rawRoute, err := client.StreamRoute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := rawRoute.Send(&pluginwire.RouteStreamFrame{Frame: &pluginwire.RouteStreamFrame_Chunk{
		Chunk: &protocolwire.DataChunk{Sequence: 1, Data: []byte("no open")},
	}}); err != nil {
		t.Fatal(err)
	}
	_ = rawRoute.CloseSend()
	frame, err := rawRoute.Recv()
	if err != nil || frame.GetClose().GetError().GetReason() != "runtime.route_open_required" {
		t.Fatalf("malformed route response=%#v err=%v", frame, err)
	}
	rawFile, err := client.TransferFile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := rawFile.CloseSend(); err != nil {
		t.Fatal(err)
	}
	fileFrame, err := rawFile.Recv()
	if err != nil || fileFrame.GetClose().GetError().GetReason() != "runtime.file_open_required" {
		t.Fatalf("missing file open response=%#v err=%v", fileFrame, err)
	}

	staleContext := runtimeStreamTestContext(handshake, "stale-route")
	staleContext.Extension.InstanceId = "stale-instance"
	stale, err := OpenRouteStream(context.Background(), client, &pluginwire.RouteStreamOpen{
		Context: staleContext, RouteId: "demo.route", ContractVersion: "1", Method: "GET", Path: "/demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = stale.Recv()
	var runtimeErr *RuntimeStreamError
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != protocolwire.ErrorCode_ERROR_CODE_STALE_RUNTIME {
		t.Fatalf("stale route error=%#v", err)
	}

	expired := runtimeStreamTestContext(handshake, "expired-file")
	expired.Deadline = timestamppb.New(time.Now().Add(-time.Second))
	if _, err := OpenFileStream(context.Background(), client, &pluginwire.FileOpen{
		Context: expired, Operation: "read", FileId: "file-1",
	}); err == nil {
		t.Fatal("expired file stream was accepted by client helper")
	}
}

func TestRuntimeProgressCancellationPropagates(t *testing.T) {
	handshake := validHandshakeRequest()
	server := NewServer().WithRuntimeStreams(RuntimeStreams{
		Lifecycle: func(ctx context.Context, _ *protocolwire.LifecycleRequest, _ *ProgressStream) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	client := startRuntimeStreamTestServer(t, server, handshake)
	ctx, cancel := context.WithCancel(context.Background())
	progress, err := RunLifecycleStream(ctx, client, &protocolwire.LifecycleRequest{
		Context: runtimeStreamTestContext(handshake, "cancel-lifecycle"), StepId: "cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := progress.Recv(); status.Code(err) != codes.Canceled {
		t.Fatalf("cancel error=%v code=%s", err, status.Code(err))
	}
}

func startRuntimeStreamTestServer(
	t *testing.T,
	server *Server,
	handshake *protocolwire.HandshakeRequest,
) pluginwire.PluginRuntimeServiceClient {
	t.Helper()
	response, err := server.Handshake(context.Background(), handshake)
	if err != nil || response.GetError() != nil {
		t.Fatalf("handshake response=%#v err=%v", response, err)
	}
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient("passthrough:///runtime-stream-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return pluginwire.NewPluginRuntimeServiceClient(connection)
}

func runtimeStreamTestContext(handshake *protocolwire.HandshakeRequest, requestID string) *protocolwire.RequestContext {
	result := proto.Clone(handshake.GetContext()).(*protocolwire.RequestContext)
	result.RequestId = requestID
	result.Deadline = timestamppb.New(time.Now().Add(time.Minute))
	return result
}
