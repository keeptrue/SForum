package hostapi

import (
	"context"
	"errors"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func (s *HostCacheService) acquireHostCacheRuntime(
	ctx context.Context,
	artifact cacheregistry.Artifact,
) (ServiceProviderAdmissionLease, context.Context, error) {
	if ctx == nil {
		return nil, nil, ErrHostCacheInvalid
	}
	if artifact.Core {
		return nil, ctx, nil
	}
	if s == nil || s.admission == nil {
		return nil, nil, ErrHostCacheStale
	}
	lease, err := s.admission.AcquireServiceProvider(ctx, ServiceProviderIdentity{
		ExtensionID: artifact.ExtensionID,
		InstanceID:  artifact.RuntimeInstanceID,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		return nil, nil, ErrHostCacheStale
	}
	if lease == nil || lease.Context() == nil {
		if lease != nil {
			lease.Release()
		}
		return nil, nil, ErrHostCacheStale
	}
	return lease, lease.Context(), nil
}

func (s *HostCacheService) acquireHostCacheProvider(
	prepared preparedHostCache,
	candidate HostCacheProviderCandidate,
) (ServiceProviderAdmissionLease, context.Context, error) {
	if err := s.validatePreparedPlan(prepared); err != nil {
		return nil, nil, err
	}
	if err := s.validateProviderSelection(prepared, candidate); err != nil {
		return nil, nil, err
	}
	if candidate.Core {
		return nil, prepared.executionCtx, nil
	}
	lease, err := s.admission.AcquireServiceProvider(prepared.executionCtx, ServiceProviderIdentity{
		ExtensionID: candidate.ExtensionID,
		InstanceID:  candidate.RuntimeInstance,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		return nil, nil, ErrHostCacheStale
	}
	if lease == nil || lease.Context() == nil {
		if lease != nil {
			lease.Release()
		}
		return nil, nil, ErrHostCacheStale
	}
	if err := s.validateHostCacheLease(lease, lease.Context()); err != nil {
		lease.Release()
		return nil, nil, err
	}
	// Close the selection/admission TOCTOU before extension code is entered.
	if err := s.validateProviderSelection(prepared, candidate); err != nil {
		lease.Release()
		return nil, nil, err
	}
	return lease, lease.Context(), nil
}

func (s *HostCacheService) validatePreparedPlan(prepared preparedHostCache) error {
	if s == nil || s.registry == nil || prepared.executionCtx == nil {
		return ErrHostCacheStale
	}
	if err := s.validateHostCacheLease(prepared.ownerLease, prepared.executionCtx); err != nil {
		return err
	}
	if err := s.registry.ValidateLeasedPlan(prepared.plan); err != nil {
		return ErrHostCacheStale
	}
	return nil
}

func (s *HostCacheService) validateProviderSelection(prepared preparedHostCache, candidate HostCacheProviderCandidate) error {
	if !prepared.external {
		return nil
	}
	if s == nil || s.resolver == nil {
		return ErrHostCacheProviderUnavailable
	}
	if err := s.resolver.ValidateHostCacheProvider(prepared.executionCtx, prepared.providerReq, prepared.providers, candidate); err != nil {
		return ErrHostCacheStale
	}
	return nil
}

func (s *HostCacheService) validateHostCacheExecution(
	prepared preparedHostCache,
	providerLease ServiceProviderAdmissionLease,
	providerCtx context.Context,
) error {
	if err := s.validateHostCacheLease(providerLease, providerCtx); err != nil {
		return err
	}
	return s.validatePreparedPlan(prepared)
}

func (s *HostCacheService) validateHostCacheLease(lease ServiceProviderAdmissionLease, ctx context.Context) error {
	if ctx == nil {
		return ErrHostCacheStale
	}
	if lease != nil {
		if failure, ok := lease.(ServiceProviderAdmissionLeaseFailure); ok {
			if err := failure.ServiceProviderAdmissionFailure(); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				return ErrHostCacheStale
			}
		}
	}
	if err := ctx.Err(); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return err
	}
	return nil
}

func (p *preparedHostCache) release() {
	if p == nil || p.ownerLease == nil {
		return
	}
	p.ownerLease.Release()
	p.ownerLease = nil
}
