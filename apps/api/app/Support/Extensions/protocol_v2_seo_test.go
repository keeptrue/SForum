package extensionsruntime

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2SEOReusesActorlessExactProviderCall(t *testing.T) {
	declaration := extensions.ManifestSEO{
		ID: "plugin.seo.reference.title", ContractVersion: "plugin.seo.reference.title@1",
		Scope: "core.page.topic", Kind: "title", Action: "filter",
		Handler: "plugin.seo.reference.title", FailurePolicy: "fallback", TimeoutMS: 500,
	}
	var received *pluginwire.ProviderCallRequest
	client := newProtocolV2SEOTestClient(t, declaration, func(_ context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
		received = request
		output, err := structpb.NewStruct(map[string]any{"document": map[string]any{"title": "Plugin title"}})
		if err != nil {
			return nil, err
		}
		return &pluginwire.ProviderCallResponse{
			Output: &protocolwire.TypedDocument{
				SchemaId: "sforum.seo.apply.response", SchemaVersion: "1", Value: output,
			},
		}, nil
	})
	result, err := client.InvokeVersionedSEO(context.Background(), VersionedSEORequest{
		DeclarationID: declaration.ID, ContractVersion: declaration.ContractVersion,
		Handler: declaration.Handler, Timeout: 500 * time.Millisecond,
		Input: map[string]any{"scope": declaration.Scope},
	})
	if err != nil || result.Output["document"] == nil {
		t.Fatalf("SEO result=%#v err=%v", result, err)
	}
	if received.GetSlotId() != ProtocolV2SEOProviderSlot || received.GetOperation() != ProtocolV2SEOProviderOperation ||
		received.GetDeclarationId() != declaration.ID || received.GetContractVersion() != declaration.ContractVersion ||
		received.GetInput().GetSchemaId() != "sforum.seo.apply.request" || received.GetInput().GetSchemaVersion() != "1" ||
		received.GetContext().GetActor() != nil {
		t.Fatalf("SEO ProviderCall=%#v", received)
	}
	if _, err := client.InvokeVersionedSEO(context.Background(), VersionedSEORequest{
		DeclarationID: declaration.ID, ContractVersion: declaration.ContractVersion,
		Handler: "plugin.seo.reference.drift", Timeout: 500 * time.Millisecond,
	}); err == nil {
		t.Fatal("drifted SEO handler reached transport")
	}
}

func TestProtocolV2SEOPropagatesProviderDeadline(t *testing.T) {
	declaration := extensions.ManifestSEO{
		ID: "plugin.seo.reference.title", ContractVersion: "plugin.seo.reference.title@1",
		Scope: "core.page.topic", Kind: "title", Action: "filter",
		Handler: "plugin.seo.reference.title", FailurePolicy: "fallback", TimeoutMS: 10,
	}
	client := newProtocolV2SEOTestClient(t, declaration, func(ctx context.Context, _ *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	_, err := client.InvokeVersionedSEO(context.Background(), VersionedSEORequest{
		DeclarationID: declaration.ID, ContractVersion: declaration.ContractVersion,
		Handler: declaration.Handler, Timeout: 10 * time.Millisecond, Input: map[string]any{"scope": declaration.Scope},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SEO timeout error=%v", err)
	}
}

func TestProtocolV2SEOMapsProviderCancellationStatus(t *testing.T) {
	declaration := extensions.ManifestSEO{
		ID: "plugin.seo.reference.title", ContractVersion: "plugin.seo.reference.title@1",
		Scope: "core.page.topic", Kind: "title", Action: "filter",
		Handler: "plugin.seo.reference.title", FailurePolicy: "fallback", TimeoutMS: 500,
	}
	for _, test := range []struct {
		name string
		code codes.Code
		want error
	}{
		{name: "deadline", code: codes.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "canceled", code: codes.Canceled, want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newProtocolV2SEOTestClient(t, declaration, func(context.Context, *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
				return nil, status.Error(test.code, "provider stopped")
			})
			_, err := client.InvokeVersionedSEO(context.Background(), VersionedSEORequest{
				DeclarationID: declaration.ID, ContractVersion: declaration.ContractVersion,
				Handler: declaration.Handler, Timeout: 500 * time.Millisecond,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("SEO %s error=%v", test.name, err)
			}
		})
	}
}

type protocolV2SEOTestServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer
	invoke func(context.Context, *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error)
}

func (s *protocolV2SEOTestServer) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	return s.invoke(ctx, request)
}

func newProtocolV2SEOTestClient(
	t *testing.T,
	declaration extensions.ManifestSEO,
	invoke func(context.Context, *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error),
) *protocolV2Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(server, &protocolV2SEOTestServer{invoke: invoke})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///seo-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "plugin.seo.reference", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
		TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: "seo-runtime",
	}
	return newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: identity, instance: identity.InstanceId, seo: []extensions.ManifestSEO{declaration},
	})
}
