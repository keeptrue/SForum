package bootstrap

import (
	"context"
	"errors"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

type pluginServiceRuntimeAdmissionSource interface {
	AcquireRuntimeCall(context.Context, extensionsruntime.RuntimeInstanceIdentity, extensionsruntime.RuntimeCallClass) (*extensionsruntime.RuntimeAdmissionLease, error)
}

type pluginServiceProviderAdmission struct {
	runtime pluginServiceRuntimeAdmissionSource
}

type pluginServiceProviderLease struct {
	lease  *extensionsruntime.RuntimeAdmissionLease
	caller context.Context
}

func newPluginServiceProviderAdmission(runtime pluginServiceRuntimeAdmissionSource) hostapi.ServiceProviderAdmission {
	return &pluginServiceProviderAdmission{runtime: runtime}
}

func (a *pluginServiceProviderAdmission) AcquireServiceProvider(
	ctx context.Context,
	identity hostapi.ServiceProviderIdentity,
) (hostapi.ServiceProviderAdmissionLease, error) {
	if a == nil || a.runtime == nil {
		return nil, hostapi.ErrServiceProviderAdmissionUnavailable
	}
	lease, err := a.runtime.AcquireRuntimeCall(ctx, extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: identity.ExtensionID, InstanceID: identity.InstanceID,
	}, extensionsruntime.RuntimeCallService)
	if err != nil {
		return nil, mapPluginServiceAdmissionError(err)
	}
	if lease == nil {
		return nil, hostapi.ErrServiceProviderAdmissionUnavailable
	}
	return &pluginServiceProviderLease{lease: lease, caller: ctx}, nil
}

func mapPluginServiceAdmissionError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, extensionsruntime.ErrRuntimeAdmissionDraining),
		errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced):
		return errors.Join(hostapi.ErrServiceProviderDraining, err)
	case errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound),
		errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotActive):
		return errors.Join(hostapi.ErrServiceProviderStale, err)
	case errors.Is(err, extensions.ErrRuntimeUnavailable):
		return errors.Join(hostapi.ErrServiceProviderAdmissionUnavailable, err)
	default:
		return errors.Join(hostapi.ErrServiceProviderAdmissionUnavailable, err)
	}
}

func (l *pluginServiceProviderLease) Context() context.Context {
	if l == nil || l.lease == nil {
		return nil
	}
	return l.lease.Context
}

func (l *pluginServiceProviderLease) ServiceProviderAdmissionFailure() error {
	if l == nil || l.lease == nil || l.lease.Context == nil {
		return hostapi.ErrServiceProviderAdmissionUnavailable
	}
	if l.caller != nil && l.caller.Err() != nil {
		return context.Cause(l.caller)
	}
	if l.lease.Context.Err() != nil {
		return errors.Join(hostapi.ErrServiceProviderDraining, context.Cause(l.lease.Context))
	}
	return context.Cause(l.lease.Context)
}

func (l *pluginServiceProviderLease) Release() {
	if l != nil && l.lease != nil {
		l.lease.Release()
	}
}
