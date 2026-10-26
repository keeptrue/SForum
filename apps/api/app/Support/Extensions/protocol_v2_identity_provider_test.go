package extensionsruntime

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
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

func TestProtocolV2IdentityProviderUsesFrozenSchemasAndReducedContext(t *testing.T) {
	var received *pluginwire.ProviderCallRequest
	client := newProtocolV2IdentityProviderTestClient(t, func(
		_ context.Context,
		request *pluginwire.ProviderCallRequest,
	) (*pluginwire.ProviderCallResponse, error) {
		received = request
		return protocolV2IdentityProviderTestResponse(
			request, "plugin.identity.runtime.risk.output@1", map[string]any{"disposition": "allow"},
		), nil
	})

	result, err := client.InvokeIdentityProvider(t.Context(), protocolV2IdentityProviderTestRequest())
	if err != nil || result.Output["disposition"] != "allow" {
		t.Fatalf("identity result = %#v, error = %v", result, err)
	}
	ctx := received.GetContext()
	actor := ctx.GetActor()
	if received.GetSlotId() != ProtocolV2IdentityProviderSlot ||
		received.GetDeclarationId() != "plugin.identity.runtime.risk" ||
		received.GetContractVersion() != "plugin.identity.runtime.risk@1" ||
		received.GetOperation() != "risk.evaluate" ||
		received.GetInput().GetSchemaId() != "plugin.identity.runtime.risk.input" ||
		received.GetInput().GetSchemaVersion() != "1" ||
		actor.GetUserId() != 42 || actor.GetSessionId() != "" || actor.GetClientIp() != "" ||
		actor.GetUserAgent() != "" || len(actor.GetRoleIds()) != 0 || len(actor.GetPermissionKeys()) != 0 ||
		ctx.GetTrace() != nil || len(ctx.GetGrantedAuthority()) != 0 || ctx.GetIdempotencyKey() != "" ||
		len(ctx.GetHostCommandDelegations()) != 0 || len(ctx.GetHostQueryDelegations()) != 0 {
		t.Fatalf("identity ProviderCall leaked or drifted context: %#v", received)
	}
	withoutActor := protocolV2IdentityProviderTestRequest()
	withoutActor.ActorUserID = 0
	if _, err := client.InvokeIdentityProvider(t.Context(), withoutActor); err != nil || received.GetContext().GetActor() != nil {
		t.Fatalf("actorless identity ProviderCall = %#v, error = %v", received, err)
	}
}

func TestProtocolV2IdentityProviderRejectsFrozenDeclarationDriftBeforeCall(t *testing.T) {
	var calls atomic.Int32
	client := newProtocolV2IdentityProviderTestClient(t, func(
		context.Context,
		*pluginwire.ProviderCallRequest,
	) (*pluginwire.ProviderCallResponse, error) {
		calls.Add(1)
		return nil, errors.New("unexpected call")
	})
	tests := []struct {
		name   string
		change func(*VersionedIdentityProviderRequest)
	}{
		{name: "provider", change: func(input *VersionedIdentityProviderRequest) { input.ProviderID += ".other" }},
		{name: "contract", change: func(input *VersionedIdentityProviderRequest) { input.ContractVersion += ".other" }},
		{name: "kind", change: func(input *VersionedIdentityProviderRequest) { input.Kind = "session" }},
		{name: "handler", change: func(input *VersionedIdentityProviderRequest) { input.Handler += ".other" }},
		{name: "priority", change: func(input *VersionedIdentityProviderRequest) { input.Priority++ }},
		{name: "operation", change: func(input *VersionedIdentityProviderRequest) { input.Operation = "session.evaluate" }},
		{name: "input schema", change: func(input *VersionedIdentityProviderRequest) { input.InputSchema += ".other" }},
		{name: "output schema", change: func(input *VersionedIdentityProviderRequest) { input.OutputSchema += ".other" }},
		{name: "failure policy", change: func(input *VersionedIdentityProviderRequest) { input.FailurePolicy = "omit" }},
		{name: "timeout", change: func(input *VersionedIdentityProviderRequest) { input.Timeout++ }},
		{name: "input wire schema", change: func(input *VersionedIdentityProviderRequest) { input.InputSchemaWireReference = "invalid" }},
		{name: "output wire schema", change: func(input *VersionedIdentityProviderRequest) { input.OutputSchemaWireReference = "invalid" }},
		{name: "actor", change: func(input *VersionedIdentityProviderRequest) { input.ActorUserID = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := protocolV2IdentityProviderTestRequest()
			test.change(&input)
			_, err := client.InvokeIdentityProvider(t.Context(), input)
			if !errors.Is(err, ErrProtocolV2IdentityProviderInvalid) {
				t.Fatalf("identity drift error = %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid identity declarations reached transport %d times", calls.Load())
	}
}

func TestProtocolV2IdentityProviderValidatesResponseBeforeAcceptingOutput(t *testing.T) {
	tests := []struct {
		name       string
		response   func(*pluginwire.ProviderCallRequest) *pluginwire.ProviderCallResponse
		wantLocal  bool
		wantReason string
	}{
		{
			name: "context before typed error",
			response: func(request *pluginwire.ProviderCallRequest) *pluginwire.ProviderCallResponse {
				response := protocolV2IdentityProviderTestResponse(request, "plugin.identity.runtime.risk.output@1", nil)
				response.Context.RequestId = "drifted"
				response.Error = &protocolwire.ErrorDetail{
					Code:   protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
					Reason: "identity_provider.denied", Message: "Denied.",
				}
				return response
			},
			wantLocal: true,
		},
		{
			name: "extension identity",
			response: func(request *pluginwire.ProviderCallRequest) *pluginwire.ProviderCallResponse {
				response := protocolV2IdentityProviderTestResponse(request, "plugin.identity.runtime.risk.output@1", nil)
				response.Context.Extension.ArtifactDigest = "drifted"
				return response
			},
			wantLocal: true,
		},
		{
			name: "future server time",
			response: func(request *pluginwire.ProviderCallRequest) *pluginwire.ProviderCallResponse {
				response := protocolV2IdentityProviderTestResponse(request, "plugin.identity.runtime.risk.output@1", nil)
				response.Context.ServerTime = timestamppb.New(time.Now().Add(time.Minute))
				return response
			},
			wantLocal: true,
		},
		{
			name: "bounded server time",
			response: func(request *pluginwire.ProviderCallRequest) *pluginwire.ProviderCallResponse {
				response := protocolV2IdentityProviderTestResponse(request, "plugin.identity.runtime.risk.output@1", nil)
				response.Context.ServerTime = timestamppb.New(time.Now().Add(-time.Minute))
				return response
			},
			wantLocal: true,
		},
		{
			name: "typed error before schema",
			response: func(request *pluginwire.ProviderCallRequest) *pluginwire.ProviderCallResponse {
				response := protocolV2IdentityProviderTestResponse(request, "plugin.identity.runtime.wrong@1", nil)
				response.Error = &protocolwire.ErrorDetail{
					Code:   protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
					Reason: "identity_provider.denied", Message: "Denied.",
				}
				return response
			},
			wantReason: "identity_provider.denied",
		},
		{
			name: "output schema",
			response: func(request *pluginwire.ProviderCallRequest) *pluginwire.ProviderCallResponse {
				return protocolV2IdentityProviderTestResponse(request, "plugin.identity.runtime.wrong@1", nil)
			},
			wantLocal: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newProtocolV2IdentityProviderTestClient(t, func(
				_ context.Context,
				request *pluginwire.ProviderCallRequest,
			) (*pluginwire.ProviderCallResponse, error) {
				return test.response(request), nil
			})
			_, err := client.InvokeIdentityProvider(t.Context(), protocolV2IdentityProviderTestRequest())
			if test.wantLocal && !errors.Is(err, ErrProtocolV2IdentityProviderInvalid) {
				t.Fatalf("identity response error = %v", err)
			}
			if test.wantReason != "" {
				var protocolErr *ProtocolV2Error
				if !errors.As(err, &protocolErr) || protocolErr.Reason != test.wantReason {
					t.Fatalf("identity typed error = %v", err)
				}
			}
		})
	}
}

func TestProtocolV2IdentityProviderMapsDeadlineAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		code codes.Code
		want error
	}{
		{name: "deadline", code: codes.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "cancel", code: codes.Canceled, want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newProtocolV2IdentityProviderTestClient(t, func(
				context.Context,
				*pluginwire.ProviderCallRequest,
			) (*pluginwire.ProviderCallResponse, error) {
				return nil, status.Error(test.code, "provider stopped")
			})
			_, err := client.InvokeIdentityProvider(t.Context(), protocolV2IdentityProviderTestRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("identity %s error = %v", test.name, err)
			}
		})
	}
}

func TestProtocolV2IdentityProviderEnforcesFrozenDeadline(t *testing.T) {
	client := newProtocolV2IdentityProviderTestClient(t, func(
		ctx context.Context,
		_ *pluginwire.ProviderCallRequest,
	) (*pluginwire.ProviderCallResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	client.manifestIdentity.Providers[0].Operations[0].TimeoutMS = 10
	input := protocolV2IdentityProviderTestRequest()
	input.Timeout = 10 * time.Millisecond
	_, err := client.InvokeIdentityProvider(t.Context(), input)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("identity frozen deadline error = %v", err)
	}
}

type protocolV2IdentityProviderTestServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer
	invoke func(context.Context, *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error)
}

func (s *protocolV2IdentityProviderTestServer) ProviderCall(
	ctx context.Context,
	request *pluginwire.ProviderCallRequest,
) (*pluginwire.ProviderCallResponse, error) {
	return s.invoke(ctx, request)
}

func newProtocolV2IdentityProviderTestClient(
	t *testing.T,
	invoke func(context.Context, *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error),
) *protocolV2Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(server, &protocolV2IdentityProviderTestServer{invoke: invoke})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient(
		"passthrough:///identity-provider-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "plugin.identity.runtime", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
		TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: "identity-runtime",
	}
	return newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: identity, instance: identity.InstanceId,
		manifestIdentity: protocolV2IdentityProviderTestManifest(),
		authority:        []*protocolwire.AuthorityGrant{{Key: "users.read"}},
	})
}

func protocolV2IdentityProviderTestManifest() *extensions.ManifestIdentity {
	return &extensions.ManifestIdentity{Providers: []extensions.ManifestIdentityProvider{{
		ID: "plugin.identity.runtime.risk", ContractVersion: "plugin.identity.runtime.risk@1",
		Kind: "risk", Handler: "identity.risk", Priority: 10,
		Operations: []extensions.ManifestIdentityProviderOperation{{
			Name: "risk.evaluate", InputSchema: "schemas/risk-input.json",
			OutputSchema: "schemas/risk-output.json", TimeoutMS: 1000,
			FailurePolicy: extensionmanifest.IdentityProviderFailureFailClosed,
		}},
	}}}
}

func protocolV2IdentityProviderTestRequest() VersionedIdentityProviderRequest {
	return VersionedIdentityProviderRequest{
		ProviderID: "plugin.identity.runtime.risk", ContractVersion: "plugin.identity.runtime.risk@1",
		Kind: "risk", Handler: "identity.risk", Priority: 10, Operation: "risk.evaluate",
		InputSchema: "schemas/risk-input.json", InputSchemaWireReference: "plugin.identity.runtime.risk.input@1",
		OutputSchema: "schemas/risk-output.json", OutputSchemaWireReference: "plugin.identity.runtime.risk.output@1",
		Timeout: time.Second, FailurePolicy: extensionmanifest.IdentityProviderFailureFailClosed,
		ActorUserID: 42, Input: map[string]any{"signal": "login"},
	}
}

func protocolV2IdentityProviderTestResponse(
	request *pluginwire.ProviderCallRequest,
	schemaReference string,
	output map[string]any,
) *pluginwire.ProviderCallResponse {
	schemaID, version, err := protocolV2SchemaRef(schemaReference)
	if err != nil {
		panic(err)
	}
	document, err := protocolV2Document(schemaID, version, output)
	if err != nil {
		panic(err)
	}
	return &pluginwire.ProviderCallResponse{
		Context: &protocolwire.ResponseContext{
			RequestId: request.GetContext().GetRequestId(), ServerTime: timestamppb.Now(),
			Trace:     cloneProtocolV2IdentityTestTrace(request.GetContext().GetTrace()),
			Extension: proto.Clone(request.GetContext().GetExtension()).(*protocolwire.ExtensionIdentity),
		},
		Output: document,
	}
}

func cloneProtocolV2IdentityTestTrace(value *protocolwire.TraceContext) *protocolwire.TraceContext {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolwire.TraceContext)
}
