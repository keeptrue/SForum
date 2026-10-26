package hostapi

import (
	"context"

	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

type protocolV2RuntimeIdentityContextKey struct{}

// ContextWithProtocolV2RuntimeIdentity is called only after the runtime-scoped
// broker has authenticated its token and exact immutable identity. Host API
// implementations must use this value instead of trusting request protobufs.
func ContextWithProtocolV2RuntimeIdentity(ctx context.Context, identity *protocolv2.ExtensionIdentity) context.Context {
	if ctx == nil || identity == nil {
		return ctx
	}
	return context.WithValue(
		ctx,
		protocolV2RuntimeIdentityContextKey{},
		proto.Clone(identity).(*protocolv2.ExtensionIdentity),
	)
}

// ProtocolV2RuntimeIdentityFromContext returns a clone of the broker-attested
// identity. Absence means the call did not pass through a runtime binding.
func ProtocolV2RuntimeIdentityFromContext(ctx context.Context) *protocolv2.ExtensionIdentity {
	if ctx == nil {
		return nil
	}
	identity, _ := ctx.Value(protocolV2RuntimeIdentityContextKey{}).(*protocolv2.ExtensionIdentity)
	if identity == nil {
		return nil
	}
	return proto.Clone(identity).(*protocolv2.ExtensionIdentity)
}

func protocolV2RuntimeIdentityFromContext(ctx context.Context) *protocolv2.ExtensionIdentity {
	return ProtocolV2RuntimeIdentityFromContext(ctx)
}
