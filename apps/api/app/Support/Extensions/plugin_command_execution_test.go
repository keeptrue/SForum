package extensionsruntime

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type pluginCommandRuntimeServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer
	request *pluginwire.CommandInvocationRequest
}

func (s *pluginCommandRuntimeServer) InvokeCommand(
	_ context.Context,
	request *pluginwire.CommandInvocationRequest,
) (*pluginwire.CommandInvocationResponse, error) {
	s.request = request
	result, err := protocolV2Document("demo.commands.command.result", "1", map[string]any{"ok": true})
	if err != nil {
		return nil, err
	}
	return &pluginwire.CommandInvocationResponse{Result: result}, nil
}

func TestProtocolV2PluginCommandCarriesExactTypedContract(t *testing.T) {
	server := &pluginCommandRuntimeServer{}
	client := pluginCommandProtocolClient(t, server)
	contract := pluginCommandRuntimeContract(false)
	output, err := client.invokePluginCommand(context.Background(), contract, map[string]any{"name": "SForum"})
	if err != nil {
		t.Fatal(err)
	}
	if server.request.GetCommandId() != contract.ID || server.request.GetContractVersion() != contract.ContractVersion ||
		server.request.GetHandler() != contract.Handler || server.request.GetInput().GetSchemaId() != "demo.commands.command.input" ||
		server.request.GetInput().GetSchemaVersion() != "1" || server.request.GetInput().GetValue().AsMap()["name"] != "SForum" ||
		output["ok"] != true {
		t.Fatalf("request=%#v output=%#v", server.request, output)
	}
	stale := contract
	stale.ArtifactDigest = "replacement"
	if _, err := client.invokePluginCommand(context.Background(), stale, map[string]any{}); !errors.Is(err, ErrPluginCommandRuntimeStale) {
		t.Fatalf("stale command = %v", err)
	}
	changed := contract
	changed.Handler = "command.changed"
	if _, err := client.invokePluginCommand(context.Background(), changed, map[string]any{}); !errors.Is(err, ErrPluginCommandRuntimeStale) {
		t.Fatalf("frozen command drift = %v", err)
	}
}

type pluginCommandStarterStub struct {
	instanceID string
	calls      int
	input      map[string]any
	contract   PluginCommandContract
}

func (s *pluginCommandStarterStub) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{InstanceID: s.instanceID}, nil
}

func (*pluginCommandStarterStub) Stop(context.Context, extensions.Extension) error { return nil }

func (s *pluginCommandStarterStub) InvokePluginCommand(
	_ context.Context,
	_ RuntimeInstanceIdentity,
	contract PluginCommandContract,
	input map[string]any,
) (map[string]any, error) {
	s.calls++
	s.contract = contract
	s.input = input
	return map[string]any{"ok": true}, nil
}

func TestManagerPluginCommandUsesSafeModeAndRuntimeAdmission(t *testing.T) {
	starter := &pluginCommandStarterStub{instanceID: "runtime-a"}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := pluginCommandExtension("demo.commands", false)
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	result, err := manager.ExecutePluginCommand(
		context.Background(), extension.Manifest.Commands[0].ID, map[string]any{"name": "SForum"}, false,
	)
	if err != nil || result.Output["ok"] != true || starter.calls != 1 ||
		!reflect.DeepEqual(starter.input, map[string]any{"name": "SForum"}) {
		t.Fatalf("result=%#v calls=%d input=%#v err=%v", result, starter.calls, starter.input, err)
	}
	if _, err := manager.ExecutePluginCommand(context.Background(), extension.Manifest.Commands[0].ID, nil, true); !errors.Is(err, ErrPluginCommandSafeMode) {
		t.Fatalf("ordinary safe mode = %v", err)
	}
	identity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: starter.instanceID}
	if _, err := manager.BeginDrain(identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ExecutePluginCommand(context.Background(), extension.Manifest.Commands[0].ID, nil, false); !errors.Is(err, ErrRuntimeAdmissionDraining) {
		t.Fatalf("draining command = %v", err)
	}
}

func TestManagerAllowsExplicitRecoverySafeCommandInSafeMode(t *testing.T) {
	starter := &pluginCommandStarterStub{instanceID: "runtime-recovery"}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := pluginCommandExtension("demo.recovery", true)
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ExecutePluginCommand(
		context.Background(), extension.Manifest.Commands[0].ID, map[string]any{}, true,
	); err != nil {
		t.Fatal(err)
	}
}

func pluginCommandProtocolClient(t *testing.T, server *pluginCommandRuntimeServer) *protocolV2Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient("passthrough:///plugin-command-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.commands", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-a",
		TrustGrantId: "grant-a", RuntimeEpoch: 1, InstanceId: "runtime-a",
	}
	declaration := extensions.ManifestCommand{
		ID: "demo.commands.command.sync", ContractVersion: "demo.commands.command.sync@1",
		Handler: "command.sync", InputSchema: "demo.commands.command.input@1",
		ResultSchema: "demo.commands.command.result@1", TimeoutMS: 3000,
	}
	return newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: identity, commands: []extensions.ManifestCommand{declaration},
		token: []byte("01234567890123456789012345678901"), instance: identity.InstanceId,
	})
}

func pluginCommandRuntimeContract(recoverySafe bool) PluginCommandContract {
	return PluginCommandContract{
		ID: "demo.commands.command.sync", ContractVersion: "demo.commands.command.sync@1",
		ExtensionID: "demo.commands", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-a", InstanceID: "runtime-a",
		Handler: "command.sync", InputSchema: "demo.commands.command.input@1", ResultSchema: "demo.commands.command.result@1",
		RecoverySafe: recoverySafe, Timeout: 3 * time.Second,
	}
}
