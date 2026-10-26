package http

import (
	"fmt"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func protocolV2RequestAuthority(
	authority routes.ResolvedRequestAuthority,
) (extensionsruntime.ProtocolV2RequestAuthority, error) {
	result := extensionsruntime.ProtocolV2RequestAuthority{}
	switch authority.Mode {
	case routes.RequestAuthorityFiltered:
		result.Mode = extensionsruntime.ProtocolV2RequestAuthorityFiltered
	case routes.RequestAuthorityRaw:
		result.Mode = extensionsruntime.ProtocolV2RequestAuthorityRaw
	default:
		return extensionsruntime.ProtocolV2RequestAuthority{}, fmt.Errorf("%w: request authority mode", ErrRouteRuntimeTarget)
	}
	switch authority.GuardKind {
	case routes.RequestGuardHost:
		result.GuardKind = extensionsruntime.ProtocolV2RequestGuardHost
	case routes.RequestGuardCustom:
		result.GuardKind = extensionsruntime.ProtocolV2RequestGuardCustom
	case routes.RequestGuardRawRequest:
		result.GuardKind = extensionsruntime.ProtocolV2RequestGuardRawRequest
	default:
		return extensionsruntime.ProtocolV2RequestAuthority{}, fmt.Errorf("%w: request guard kind", ErrRouteRuntimeTarget)
	}
	if result.Mode == extensionsruntime.ProtocolV2RequestAuthorityRaw &&
		result.GuardKind != extensionsruntime.ProtocolV2RequestGuardRawRequest ||
		result.Mode != extensionsruntime.ProtocolV2RequestAuthorityRaw &&
			result.GuardKind == extensionsruntime.ProtocolV2RequestGuardRawRequest {
		return extensionsruntime.ProtocolV2RequestAuthority{}, fmt.Errorf("%w: request authority mismatch", ErrRouteRuntimeTarget)
	}
	return result, nil
}

func rawRouteRequestAuthority(authority routes.ResolvedRequestAuthority) bool {
	return authority.Mode == routes.RequestAuthorityRaw && authority.GuardKind == routes.RequestGuardRawRequest
}
