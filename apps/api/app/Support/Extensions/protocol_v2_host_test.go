package extensionsruntime

import (
	"context"
	"io"
	"testing"
	"time"

	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProtocolV2HostUnaryAuthBindsExactRuntime(t *testing.T) {
	token := []byte("01234567890123456789012345678901")
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: "demo.v2", ExtensionVersion: "1.0.0", ArtifactDigest: "sha256:artifact",
		TrustGrantId: "grant-1", RuntimeEpoch: 7, InstanceId: "instance-1",
	}
	authority := []*protocolv2.AuthorityGrant{{Key: "settings.own", ContractVersion: hostAPIV2Version}}
	binding := newProtocolV2HostBinding(protocolV2ClientConfig{identity: identity, authority: authority, token: token})
	valid := func() *protocolv2.HealthRequest {
		return &protocolv2.HealthRequest{Context: &protocolv2.RequestContext{
			RequestId: "request-1", Deadline: timestamppb.New(time.Now().Add(time.Minute)),
			Extension: cloneV2Identity(identity), GrantedAuthority: cloneV2Authority(authority),
		}}
	}
	call := func(rawToken []byte, request *protocolv2.HealthRequest) error {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(ProtocolV2RuntimeTokenMetadataKey, string(rawToken)))
		_, err := protocolV2HostUnaryAuthInterceptor(binding)(ctx, request, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
			if trusted := hostapi.ProtocolV2RuntimeIdentityFromContext(ctx); !proto.Equal(trusted, identity) {
				t.Fatalf("unary handler identity = %#v", trusted)
			}
			return &protocolv2.HealthResponse{Healthy: true}, nil
		})
		return err
	}

	if err := call(token, valid()); err != nil {
		t.Fatalf("valid runtime rejected: %v", err)
	}
	tests := []struct {
		name   string
		token  []byte
		mutate func(*protocolv2.HealthRequest)
		code   codes.Code
	}{
		{name: "stale token", token: []byte("11234567890123456789012345678901"), code: codes.Unauthenticated},
		{name: "stale identity", token: token, mutate: func(request *protocolv2.HealthRequest) {
			request.Context.Extension.RuntimeEpoch++
		}, code: codes.FailedPrecondition},
		{name: "forged authority", token: token, mutate: func(request *protocolv2.HealthRequest) {
			request.Context.GrantedAuthority[0].Key = "raw.database"
		}, code: codes.PermissionDenied},
		{name: "forged actor", token: token, mutate: func(request *protocolv2.HealthRequest) {
			request.Context.Actor = &protocolv2.Actor{UserId: 42, PermissionKeys: []string{"admin"}}
		}, code: codes.PermissionDenied},
		{name: "reflected actor delegation", token: token, mutate: func(request *protocolv2.HealthRequest) {
			request.Context.HostCommandDelegations = []*protocolv2.HostCommandDelegation{{
				CommandId: "sforum.demo", CommandVersion: "1", IdempotencyKey: "request-42", Token: "reflected",
			}}
		}, code: codes.PermissionDenied},
		{name: "reflected query delegation", token: token, mutate: func(request *protocolv2.HealthRequest) {
			request.Context.HostQueryDelegations = []*protocolv2.HostQueryDelegation{{
				QueryId: "sforum.demo", ContractVersion: "sforum.demo@1",
				PlanVersion: "sforum.demo.plan@1", Token: "reflected",
			}}
		}, code: codes.PermissionDenied},
		{name: "expired deadline", token: token, mutate: func(request *protocolv2.HealthRequest) {
			request.Context.Deadline = timestamppb.New(time.Now().Add(-time.Second))
		}, code: codes.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid()
			if test.mutate != nil {
				test.mutate(request)
			}
			if code := status.Code(call(test.token, request)); code != test.code {
				t.Fatalf("code = %s, want %s", code, test.code)
			}
		})
	}
}

func TestProtocolV2HostStreamAuthenticatesOnlyOpenFrame(t *testing.T) {
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: "demo.v2", ExtensionVersion: "1.0.0", ArtifactDigest: "artifact",
		TrustGrantId: "grant", RuntimeEpoch: 1, InstanceId: "instance",
	}
	authority := []*protocolv2.AuthorityGrant{{Key: "host.api", ContractVersion: hostAPIV2Version}}
	binding := newProtocolV2HostBinding(protocolV2ClientConfig{identity: identity, authority: authority, token: []byte("01234567890123456789012345678901")})
	requestContext := &protocolv2.RequestContext{
		RequestId: "stream-1", Deadline: timestamppb.New(time.Now().Add(time.Minute)),
		Extension: cloneV2Identity(identity), GrantedAuthority: cloneV2Authority(authority),
	}
	backend := &fakeProtocolV2ServerStream{messages: []proto.Message{
		&hostv2.ServiceStreamFrame{Frame: &hostv2.ServiceStreamFrame_Open{Open: &hostv2.ServiceStreamOpen{Context: requestContext}}},
		&hostv2.ServiceStreamFrame{Frame: &hostv2.ServiceStreamFrame_Message{Message: &protocolv2.TypedDocument{SchemaId: "demo.message", SchemaVersion: "1"}}},
	}}
	stream := &protocolV2AuthenticatedHostStream{ServerStream: backend, binding: binding}
	if trusted := hostapi.ProtocolV2RuntimeIdentityFromContext(stream.Context()); !proto.Equal(trusted, identity) {
		t.Fatalf("stream broker identity = %#v", trusted)
	}
	if err := stream.RecvMsg(new(hostv2.ServiceStreamFrame)); err != nil {
		t.Fatalf("open frame: %v", err)
	}
	if trusted := hostapi.ProtocolV2RuntimeIdentityFromContext(stream.Context()); !proto.Equal(trusted, identity) {
		t.Fatalf("stream handler identity = %#v", trusted)
	}
	if err := stream.RecvMsg(new(hostv2.ServiceStreamFrame)); err != nil {
		t.Fatalf("data frame inherited authentication: %v", err)
	}
}

type fakeProtocolV2ServerStream struct {
	messages []proto.Message
}

func (s *fakeProtocolV2ServerStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeProtocolV2ServerStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeProtocolV2ServerStream) SetTrailer(metadata.MD)       {}
func (s *fakeProtocolV2ServerStream) Context() context.Context     { return context.Background() }
func (s *fakeProtocolV2ServerStream) SendMsg(any) error            { return nil }
func (s *fakeProtocolV2ServerStream) RecvMsg(message any) error {
	if len(s.messages) == 0 {
		return io.EOF
	}
	target, ok := message.(proto.Message)
	if !ok {
		return io.ErrUnexpectedEOF
	}
	proto.Reset(target)
	proto.Merge(target, s.messages[0])
	s.messages = s.messages[1:]
	return nil
}

func TestProtocolV2HostBindingOwnsClonedIdentity(t *testing.T) {
	identity := &protocolv2.ExtensionIdentity{ExtensionId: "demo", RuntimeEpoch: 1}
	binding := newProtocolV2HostBinding(protocolV2ClientConfig{identity: identity, token: []byte("01234567890123456789012345678901")})
	identity.RuntimeEpoch = 2
	if proto.Equal(binding.identity, identity) || binding.identity.GetRuntimeEpoch() != 1 {
		t.Fatalf("binding identity changed with caller: %#v", binding.identity)
	}
}

func TestProtocolV2HostAPIContractCompatibility(t *testing.T) {
	for _, value := range []string{hostAPIV2Contract, hostAPIV2Legacy, hostAPIV2Version} {
		if !supportsProtocolV2HostAPI(value) {
			t.Fatalf("expected %q accepted", value)
		}
	}
	for _, value := range []string{"", "sforum.host@1", "sforum.host/v3", "sforum.host-api@3"} {
		if supportsProtocolV2HostAPI(value) {
			t.Fatalf("expected %q rejected", value)
		}
	}
}
