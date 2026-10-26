package extensionsruntime

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type pluginJobStarterStub struct {
	invocations []supportjobs.PluginJobInvocation
}

func (s *pluginJobStarterStub) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{}, nil
}

func (s *pluginJobStarterStub) Stop(context.Context, extensions.Extension) error { return nil }

func (s *pluginJobStarterStub) ExecutePluginJob(_ context.Context, invocation supportjobs.PluginJobInvocation) error {
	s.invocations = append(s.invocations, invocation)
	return nil
}

func TestManagerExecutesOnlyRunningExactPluginJobContract(t *testing.T) {
	starter := &pluginJobStarterStub{}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := pluginJobRuntimeExtension()
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	contract, err := extensions.PluginJobContractForExtension(extension, "demo.sync")
	if err != nil {
		t.Fatal(err)
	}
	invocation := supportjobs.PluginJobInvocation{Contract: contract, TrustGrantID: "grant-1"}
	if err := manager.ExecutePluginJob(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	if len(starter.invocations) != 1 {
		t.Fatalf("invocations = %#v", starter.invocations)
	}

	stale := invocation
	stale.Contract.ArtifactDigest = "old-digest"
	if err := manager.ExecutePluginJob(context.Background(), stale); !errors.Is(err, supportjobs.ErrPluginJobRuntimeStale) {
		t.Fatalf("stale error = %v", err)
	}
	if len(starter.invocations) != 1 {
		t.Fatalf("stale job reached runtime: %#v", starter.invocations)
	}
	if err := manager.Stop(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	if err := manager.ExecutePluginJob(context.Background(), invocation); !errors.Is(err, extensions.ErrRuntimeUnavailable) {
		t.Fatalf("stopped runtime error = %v", err)
	}
}

func TestProtocolStarterDispatchesPluginJobOnlyToV2Invoker(t *testing.T) {
	starter := NewProtocolStarter(ProtocolStarterConfig{})
	invoker := &pluginJobProtocolStub{}
	starter.protocols["demo.plugin"] = invoker
	invocation := supportjobs.PluginJobInvocation{
		Contract: supportjobs.PluginJobContract{ExtensionID: "demo.plugin"},
	}
	if err := starter.ExecutePluginJob(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	if invoker.calls != 1 {
		t.Fatalf("calls = %d", invoker.calls)
	}
	starter.protocols["demo.plugin"] = ProtocolNoop{}
	if err := starter.ExecutePluginJob(context.Background(), invocation); !errors.Is(err, extensions.ErrRuntimeUnavailable) {
		t.Fatalf("v1 error = %v", err)
	}
}

func TestProtocolV2ClientExecutesTypedPluginJobStream(t *testing.T) {
	var received *pluginwire.JobRequest
	client := pluginJobProtocolV2TestClient(t, func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
		received = request
		if err := stream.Send(pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_RUNNING, 1, 2)); err != nil {
			return err
		}
		return stream.Send(pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, 2, 2))
	})
	contract := pluginJobRuntimeContract()
	err := client.ExecutePluginJob(context.Background(), supportjobs.PluginJobInvocation{
		JobID: 71, Attempt: 3, Contract: contract, TrustGrantID: "grant-1", Payload: map[string]any{"page": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.GetJobId() != "71" || received.GetAttempt() != 3 || received.GetPayloadVersion() != "1" ||
		received.GetPayload().GetSchemaId() != "demo.sync.payload" || received.GetPayload().GetValue().AsMap()["page"] != float64(2) {
		t.Fatalf("request = %#v", received)
	}
}

func TestProtocolV2ClientRejectsJobStreamWithoutTerminalState(t *testing.T) {
	client := pluginJobProtocolV2TestClient(t, func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
		return stream.Send(pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_RUNNING, 1, 2))
	})
	err := client.ExecutePluginJob(context.Background(), supportjobs.PluginJobInvocation{
		JobID: 72, Contract: pluginJobRuntimeContract(), TrustGrantID: "grant-1",
	})
	var protocolErr *ProtocolV2Error
	if !errors.As(err, &protocolErr) || protocolErr.Reason != "runtime.job_terminal_missing" {
		t.Fatalf("terminal error = %v", err)
	}
}

func TestProtocolV2ClientFreezesAndEnforcesJobDeclarations(t *testing.T) {
	extension := pluginJobRuntimeExtension()
	extension.Source = extensions.SourceBuiltin
	starter := NewProtocolStarter(ProtocolStarterConfig{})
	config, err := starter.protocolV2ClientConfig(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	extension.Manifest.Jobs[0].ContractVersion = "forged.job@9"
	if config.jobs[0].ContractVersion != "demo.plugin.job.sync@1" {
		t.Fatalf("start config followed caller mutation: %#v", config.jobs)
	}
	clientSnapshot := newProtocolV2Client(nil, config)
	config.jobs[0].ContractVersion = "mutated.config@9"
	if clientSnapshot.jobs[0].ContractVersion != "demo.plugin.job.sync@1" {
		t.Fatalf("client followed config mutation: %#v", clientSnapshot.jobs)
	}

	calls := 0
	client := pluginJobProtocolV2TestClient(t, func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
		calls++
		return stream.Send(pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, 1, 1))
	})
	client.jobs[0].Handler = "job.sync"
	contract := pluginJobRuntimeContract()
	forged := contract
	forged.JobContract = "forged.job@9"
	if err := client.ExecutePluginJob(context.Background(), supportjobs.PluginJobInvocation{
		JobID: 73, Contract: forged, TrustGrantID: "grant-1",
	}); !errors.Is(err, supportjobs.ErrPluginJobRuntimeStale) {
		t.Fatalf("forged job contract error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("forged job reached runtime %d times", calls)
	}
	if err := client.ExecutePluginJob(context.Background(), supportjobs.PluginJobInvocation{
		JobID: 73, Contract: contract, TrustGrantID: "grant-1",
	}); err != nil || calls != 1 {
		t.Fatalf("frozen exact job err=%v calls=%d", err, calls)
	}
}

func TestProtocolV2ClientRejectsMalformedJobProgress(t *testing.T) {
	tests := map[string]func(*pluginwire.JobRequest, grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error{
		"wrong step": func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
			update := pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, 1, 1)
			update.StepId = "other-job"
			return stream.Send(update)
		},
		"wrong context": func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
			update := pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, 1, 1)
			update.Context.RequestId = "forged-request"
			return stream.Send(update)
		},
		"regressed counters": func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
			if err := stream.Send(pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_RUNNING, 2, 3)); err != nil {
				return err
			}
			return stream.Send(pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_RUNNING, 1, 3))
		},
		"failed without typed error": func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
			return stream.Send(pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_FAILED, 0, 0))
		},
		"cancelled with wrong code": func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
			update := pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_CANCELLED, 0, 0)
			update.Error = &protocolwire.ErrorDetail{Code: protocolwire.ErrorCode_ERROR_CODE_CONFLICT, Reason: "job.conflict"}
			return stream.Send(update)
		},
		"undeclared result": func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
			update := pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, 1, 1)
			update.Result = &protocolwire.TypedDocument{SchemaId: "undeclared.job.result", SchemaVersion: "1"}
			return stream.Send(update)
		},
		"after terminal": func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
			if err := stream.Send(pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, 1, 1)); err != nil {
				return err
			}
			return stream.Send(pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_RUNNING, 1, 1))
		},
	}
	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			client := pluginJobProtocolV2TestClient(t, handler)
			err := client.ExecutePluginJob(context.Background(), supportjobs.PluginJobInvocation{
				JobID: 74, Contract: pluginJobRuntimeContract(), TrustGrantID: "grant-1",
			})
			if !errors.Is(err, ErrInvalidPluginJobStream) {
				t.Fatalf("malformed progress error = %v", err)
			}
		})
	}
}

func TestProtocolV2ClientPreservesTypedJobFailure(t *testing.T) {
	client := pluginJobProtocolV2TestClient(t, func(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
		update := pluginJobProgressUpdate(request, protocolwire.ProgressState_PROGRESS_STATE_FAILED, 1, 2)
		update.Error = &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE, Reason: "job.provider_unavailable",
			Message: "Provider unavailable.", Retryable: true,
		}
		return stream.Send(update)
	})
	err := client.ExecutePluginJob(context.Background(), supportjobs.PluginJobInvocation{
		JobID: 75, Contract: pluginJobRuntimeContract(), TrustGrantID: "grant-1",
	})
	var protocolErr *ProtocolV2Error
	if !errors.As(err, &protocolErr) || protocolErr.Code != protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE ||
		protocolErr.Reason != "job.provider_unavailable" || !protocolErr.Retryable {
		t.Fatalf("typed job failure = %#v", err)
	}
}

type pluginJobProtocolStub struct {
	ProtocolNoop
	calls int
}

func (s *pluginJobProtocolStub) ExecutePluginJob(context.Context, supportjobs.PluginJobInvocation) error {
	s.calls++
	return nil
}

func pluginJobRuntimeExtension() extensions.Extension {
	return extensions.Extension{
		ID: "demo.plugin", Version: "1.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, PackageDigest: "digest-v1",
		Manifest: extensions.Manifest{Jobs: []extensions.ManifestJob{{
			ID: "demo.plugin.job.sync", ContractVersion: "demo.plugin.job.sync@1",
			Name: "demo.sync", Handler: "job.sync", PayloadSchema: "demo.sync.payload@1", RetryPolicy: "bounded",
		}}},
	}
}

func pluginJobRuntimeContract() supportjobs.PluginJobContract {
	contract, err := extensions.PluginJobContractForExtension(pluginJobRuntimeExtension(), "demo.sync")
	if err != nil {
		panic(err)
	}
	return contract
}

type pluginJobRuntimeTestServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer
	job func(*pluginwire.JobRequest, grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error
}

func (s *pluginJobRuntimeTestServer) Handshake(context.Context, *protocolwire.HandshakeRequest) (*protocolwire.HandshakeResponse, error) {
	return &protocolwire.HandshakeResponse{SelectedProtocol: &protocolwire.ProtocolRange{
		Protocol: protocolV2Name, Major: 2, MinMinor: 0, MaxMinor: 0,
	}}, nil
}

func (s *pluginJobRuntimeTestServer) ExecuteJob(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
	return s.job(request, stream)
}

func pluginJobProtocolV2TestClient(
	t *testing.T,
	handler func(*pluginwire.JobRequest, grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error,
) *protocolV2Client {
	t.Helper()
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
		TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: "runtime-1",
	}
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(grpcServer, &pluginJobRuntimeTestServer{job: handler})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient("passthrough:///plugin-job-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: identity, jobs: append([]extensions.ManifestJob(nil), pluginJobRuntimeExtension().Manifest.Jobs...),
		token: []byte("01234567890123456789012345678901"), instance: identity.InstanceId,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	return client
}

func pluginJobProgressUpdate(request *pluginwire.JobRequest, state protocolwire.ProgressState, completed, total uint32) *protocolwire.ProgressUpdate {
	requestContext := request.GetContext()
	return &protocolwire.ProgressUpdate{
		Context: &protocolwire.ResponseContext{
			RequestId: requestContext.GetRequestId(), Trace: proto.Clone(requestContext.GetTrace()).(*protocolwire.TraceContext),
			Extension: proto.Clone(requestContext.GetExtension()).(*protocolwire.ExtensionIdentity), ServerTime: timestamppb.Now(),
		},
		StepId: request.GetJobId(), State: state, CompletedUnits: completed, TotalUnits: total,
	}
}
