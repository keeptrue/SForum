package hostapi

import (
	"context"
	"sort"
	"strings"

	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// ProtocolV2ActorDelegationBundleRequest is authored only by a Host route or
// admin adapter. Permission keys are an issuance-time filter; Execute still
// reloads the actor and permissions inside the command transaction.
type ProtocolV2ActorDelegationBundleRequest struct {
	ActorUserID    int64
	PermissionKeys []string
	Runtime        *protocolv2.ExtensionIdentity
	IdempotencyKey string
}

// ProtocolV2ActorDelegationGrant carries one command-bound token to the exact
// plugin invocation that may use it.
type ProtocolV2ActorDelegationGrant struct {
	CommandID      string
	CommandVersion string
	IdempotencyKey string
	Token          string
}

// ProtocolV2ActorDelegationBundleIssuer is the narrow production capability
// consumed by Host-owned route and admin transports.
type ProtocolV2ActorDelegationBundleIssuer interface {
	IssueProtocolV2ActorDelegations(context.Context, ProtocolV2ActorDelegationBundleRequest) ([]ProtocolV2ActorDelegationGrant, error)
}

func (e *protocolV2CommandEngine) issueProtocolV2ActorDelegations(
	ctx context.Context,
	request ProtocolV2ActorDelegationBundleRequest,
) ([]ProtocolV2ActorDelegationGrant, error) {
	if e == nil || e.delegations == nil || ctx == nil {
		return nil, ErrProtocolV2ActorDelegationInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validProtocolV2ActorDelegationBinding(request.ActorUserID, request.Runtime, request.IdempotencyKey) {
		return nil, ErrProtocolV2ActorDelegationInvalid
	}
	request.Runtime = cloneProtocolV2ExtensionIdentity(request.Runtime)
	permissions := normalizeProtocolV2DelegationPermissions(request.PermissionKeys)
	keys := make([]protocolV2CommandKey, 0, len(e.definitions))
	for key, definition := range e.definitions {
		if definition.ActorMode == protocolV2CommandActorDelegated &&
			protocolV2DelegationPermissionsAllow(permissions, definition.RequiredPermissions) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].id == keys[right].id {
			return keys[left].version < keys[right].version
		}
		return keys[left].id < keys[right].id
	})

	grants := make([]ProtocolV2ActorDelegationGrant, 0, len(keys))
	for _, key := range keys {
		token, err := e.delegations.IssueActorDelegation(ctx, ProtocolV2ActorDelegationRequest{
			ActorUserID: request.ActorUserID, Runtime: request.Runtime,
			CommandID: key.id, CommandVersion: key.version, IdempotencyKey: request.IdempotencyKey,
		})
		if err != nil {
			return nil, err
		}
		grants = append(grants, ProtocolV2ActorDelegationGrant{
			CommandID: key.id, CommandVersion: key.version,
			IdempotencyKey: request.IdempotencyKey, Token: token,
		})
	}
	return grants, nil
}

func normalizeProtocolV2DelegationPermissions(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = true
		}
	}
	return result
}

func protocolV2DelegationPermissionsAllow(granted map[string]bool, required []string) bool {
	if granted["*"] {
		return true
	}
	for _, permission := range required {
		if !granted[permission] {
			return false
		}
	}
	return true
}
