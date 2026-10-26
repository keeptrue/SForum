package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"sync/atomic"
	"time"

	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ProtocolV2HostRegistrar registers host-owned generated services on one
// runtime-scoped broker server. Authentication remains owned by the runtime.
type ProtocolV2HostRegistrar interface {
	RegisterProtocolV2(grpc.ServiceRegistrar)
}

// protocolV2HostSessionDelegationMaxBytes bounds the echoed session token at
// the transport boundary. The signing authority applies the same bound.
const protocolV2HostSessionDelegationMaxBytes = 4096

type protocolV2HostBinding struct {
	identity  *protocolv2.ExtensionIdentity
	authority []*protocolv2.AuthorityGrant
	tokenHash [sha256.Size]byte
}

func newProtocolV2HostBinding(config protocolV2ClientConfig) protocolV2HostBinding {
	return protocolV2HostBinding{
		identity:  cloneV2Identity(config.identity),
		authority: cloneV2Authority(config.authority),
		tokenHash: sha256.Sum256(config.token),
	}
}

func protocolV2HostGRPCServer(
	options []grpc.ServerOption,
	registrar ProtocolV2HostRegistrar,
	binding protocolV2HostBinding,
) *grpc.Server {
	server := pluginv2sdk.NewGRPCServer(
		options,
		ProtocolV2ServerConfig{},
		[]grpc.UnaryServerInterceptor{protocolV2HostUnaryAuthInterceptor(binding)},
		[]grpc.StreamServerInterceptor{protocolV2HostStreamAuthInterceptor(binding)},
	)
	registrar.RegisterProtocolV2(server)
	return server
}

func protocolV2HostUnaryAuthInterceptor(binding protocolV2HostBinding) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := validateProtocolV2HostToken(ctx, binding); err != nil {
			return nil, err
		}
		carrier, ok := request.(interface {
			GetContext() *protocolv2.RequestContext
		})
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "protocol v2 request context is required")
		}
		if err := validateProtocolV2HostContext(carrier.GetContext(), binding); err != nil {
			return nil, err
		}
		trusted := hostapi.ContextWithProtocolV2RuntimeIdentity(ctx, binding.identity)
		return handler(trusted, request)
	}
}

func protocolV2HostStreamAuthInterceptor(binding protocolV2HostBinding) grpc.StreamServerInterceptor {
	return func(server any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := validateProtocolV2HostToken(stream.Context(), binding); err != nil {
			return err
		}
		return handler(server, &protocolV2AuthenticatedHostStream{ServerStream: stream, binding: binding})
	}
}

type protocolV2AuthenticatedHostStream struct {
	grpc.ServerStream
	binding   protocolV2HostBinding
	validated atomic.Bool
}

func (s *protocolV2AuthenticatedHostStream) Context() context.Context {
	if s == nil || s.ServerStream == nil {
		return context.Background()
	}
	// The broker token already authenticates this immutable binding. Publish it
	// immediately because grpc's generic stream wrapper may cache Context before
	// the first Recv; RecvMsg still validates the open-frame identity/authority.
	return hostapi.ContextWithProtocolV2RuntimeIdentity(s.ServerStream.Context(), s.binding.identity)
}

func (s *protocolV2AuthenticatedHostStream) RecvMsg(message any) error {
	if err := s.ServerStream.RecvMsg(message); err != nil {
		return err
	}
	if s.validated.Load() {
		return nil
	}
	requestContext := protocolV2HostStreamOpenContext(message)
	if requestContext == nil {
		return status.Error(codes.InvalidArgument, "protocol v2 stream open context is required")
	}
	if err := validateProtocolV2HostContext(requestContext, s.binding); err != nil {
		return err
	}
	s.validated.Store(true)
	return nil
}

func protocolV2HostStreamOpenContext(message any) *protocolv2.RequestContext {
	if carrier, ok := message.(interface {
		GetContext() *protocolv2.RequestContext
	}); ok {
		return carrier.GetContext()
	}
	switch value := message.(type) {
	case *hostv2.ServiceStreamFrame:
		return value.GetOpen().GetContext()
	case *hostv2.FileWriteFrame:
		return value.GetOpen().GetContext()
	case *hostv2.HttpStreamFrame:
		return value.GetOpen().GetContext()
	default:
		return nil
	}
}

func validateProtocolV2HostToken(ctx context.Context, binding protocolV2HostBinding) error {
	values := metadata.ValueFromIncomingContext(ctx, ProtocolV2RuntimeTokenMetadataKey)
	if len(values) != 1 {
		return status.Error(codes.Unauthenticated, "protocol v2 runtime token is required")
	}
	received := sha256.Sum256([]byte(values[0]))
	if subtle.ConstantTimeCompare(received[:], binding.tokenHash[:]) != 1 {
		return status.Error(codes.Unauthenticated, "protocol v2 runtime token is stale")
	}
	return nil
}

func validateProtocolV2HostContext(ctx *protocolv2.RequestContext, binding protocolV2HostBinding) error {
	if ctx == nil || ctx.GetRequestId() == "" || ctx.GetExtension() == nil {
		return status.Error(codes.InvalidArgument, "protocol v2 request id and exact extension identity are required")
	}
	if !proto.Equal(ctx.GetExtension(), binding.identity) {
		return status.Error(codes.FailedPrecondition, "protocol v2 runtime identity is stale")
	}
	if !equalProtocolV2Authority(ctx.GetGrantedAuthority(), binding.authority) {
		return status.Error(codes.PermissionDenied, "protocol v2 authority disclosure does not match the runtime grant")
	}
	if ctx.GetActor() != nil {
		return status.Error(codes.PermissionDenied, "protocol v2 actor context is not host-attested")
	}
	if len(ctx.GetHostCommandDelegations()) != 0 || len(ctx.GetHostQueryDelegations()) != 0 {
		return status.Error(codes.PermissionDenied, "protocol v2 actor delegations are Host-to-plugin only")
	}
	// S9 reverses the direction for session state: the Host delivers the session
	// delegation on the invocation, and the plugin echoes it back on the Host
	// session-state RPCs. The interceptor therefore only bounds the shape here;
	// the session service validates the signature, binding, and mint record and
	// denies without it.
	if delegation := ctx.GetHostSessionDelegation(); delegation != nil {
		if token := delegation.GetToken(); token == "" || len(token) > protocolV2HostSessionDelegationMaxBytes {
			return status.Error(codes.InvalidArgument, "protocol v2 session delegation is malformed")
		}
	}
	deadline := ctx.GetDeadline()
	if deadline == nil || !deadline.IsValid() {
		return status.Error(codes.InvalidArgument, "protocol v2 request deadline is required")
	}
	if !deadline.AsTime().After(time.Now()) {
		return status.Error(codes.DeadlineExceeded, "protocol v2 request deadline has expired")
	}
	return nil
}

func equalProtocolV2Authority(left, right []*protocolv2.AuthorityGrant) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !proto.Equal(left[index], right[index]) {
			return false
		}
	}
	return true
}
