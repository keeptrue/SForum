package extensionsruntime

import (
	"context"
	"errors"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// IdentitySessionEvaluateInvoker adapts IdentityProviderRuntime to the Host
// session-policy evaluation boundary. It only ever invokes the exact provider
// claim supplied by ProviderResolution; callers cannot substitute Core or a
// generic provider slot.
type IdentitySessionEvaluateInvoker struct {
	runtime *IdentityProviderRuntime
}

func NewIdentitySessionEvaluateInvoker(
	runtime *IdentityProviderRuntime,
) (*IdentitySessionEvaluateInvoker, error) {
	if runtime == nil {
		return nil, ErrIdentityProviderInvocationInvalid
	}
	return &IdentitySessionEvaluateInvoker{runtime: runtime}, nil
}

// InvokeExact holds exact runtime admission through Accept and requires the
// Host accept callback to call the commit fence exactly once.
func (i *IdentitySessionEvaluateInvoker) InvokeExact(
	ctx context.Context,
	provider identityregistry.ProviderContribution,
	operation string,
	actorUserID int64,
	input map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	if i == nil || i.runtime == nil || accept == nil {
		return ErrIdentityProviderInvocationInvalid
	}
	if provider.ID == "" || operation == "" {
		return ErrIdentityProviderInvocationInvalid
	}
	_, err := i.runtime.InvokeExact(
		ctx,
		provider,
		IdentityProviderInvocation{
			ProviderID:  provider.ID,
			Operation:   operation,
			ActorUserID: actorUserID,
			Input:       input,
		},
		func(callCtx context.Context, proposal IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
			// Defense in depth: refuse a different exact artifact than the
			// resolution claim even if Registry ids collide across revisions.
			if !sameIdentityProviderContract(proposal.Provider, provider) ||
				proposal.Operation.Name != operation {
				return errors.Join(ErrIdentityProviderStale, ErrIdentityProviderAcceptFailed)
			}
			return accept(callCtx, proposal.Output, func() error {
				if fence == nil {
					return ErrIdentityProviderInvocationInvalid
				}
				return fence()
			})
		},
	)
	return err
}

var _ identity.SessionPolicyEvaluateInvoker = (*IdentitySessionEvaluateInvoker)(nil)
