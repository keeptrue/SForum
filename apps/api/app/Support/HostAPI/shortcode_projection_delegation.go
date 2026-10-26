package hostapi

import (
	"context"
	"errors"

	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// ProtocolV2ShortcodeProjectionDelegationRequest is Host-authored for one
// exact content-runtime call. ActorUserID zero represents an anonymous viewer.
type ProtocolV2ShortcodeProjectionDelegationRequest struct {
	ActorUserID int64
	Runtime     *protocolv2.ExtensionIdentity
	Locale      string
	Scope       string
}

type ProtocolV2ShortcodeProjectionDelegationBundleIssuer interface {
	IssueProtocolV2ShortcodeProjectionDelegations(
		context.Context,
		ProtocolV2ShortcodeProjectionDelegationRequest,
	) ([]ProtocolV2QueryActorDelegationGrant, error)
}

func (g *Gateway) IssueProtocolV2ShortcodeProjectionDelegations(
	ctx context.Context,
	request ProtocolV2ShortcodeProjectionDelegationRequest,
) ([]ProtocolV2QueryActorDelegationGrant, error) {
	if g == nil || request.ActorUserID < 0 || request.Runtime.GetExtensionId() != ShortcodeProjectionExtensionID {
		return nil, ErrProtocolV2QueryDelegationInvalid
	}
	grants := make([]ProtocolV2QueryActorDelegationGrant, 0, len(ShortcodeProjectionQueryIDs()))
	for _, queryID := range ShortcodeProjectionQueryIDs() {
		grant, err := g.IssueProtocolV2QueryActorDelegation(ctx, ProtocolV2QueryActorDelegationRequest{
			ActorUserID: request.ActorUserID, Runtime: request.Runtime, QueryID: queryID,
			Locale: request.Locale, Scope: request.Scope,
		})
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.Join(ErrProtocolV2QueryDelegationInvalid, err)
		}
		grants = append(grants, grant)
	}
	return grants, nil
}

var _ ProtocolV2ShortcodeProjectionDelegationBundleIssuer = (*Gateway)(nil)
