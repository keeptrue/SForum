package bootstrap

import (
	"context"
	"errors"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

type pluginJobRuntimeAdmissionSource interface {
	AcquireActiveRuntimeCall(context.Context, string, extensionsruntime.RuntimeCallClass) (extensionsruntime.RuntimeInstanceSnapshot, *extensionsruntime.RuntimeAdmissionLease, error)
}

type pluginJobEnqueueAdmission struct {
	runtime pluginJobRuntimeAdmissionSource
}

type pluginJobEnqueueLease struct {
	lease  *extensionsruntime.RuntimeAdmissionLease
	caller context.Context
}

func newPluginJobEnqueueAdmission(runtime pluginJobRuntimeAdmissionSource) hostapi.PluginJobEnqueueAdmission {
	return &pluginJobEnqueueAdmission{runtime: runtime}
}

func (a *pluginJobEnqueueAdmission) AcquirePluginJobEnqueue(
	ctx context.Context,
	identity hostapi.PluginJobEnqueueIdentity,
) (hostapi.PluginJobEnqueueLease, error) {
	if a == nil || a.runtime == nil {
		return nil, hostapi.ErrPluginJobEnqueueUnavailable
	}
	snapshot, lease, err := a.runtime.AcquireActiveRuntimeCall(ctx, identity.ExtensionID, extensionsruntime.RuntimeCallJob)
	if err != nil {
		return nil, mapPluginJobAdmissionError(err)
	}
	if lease == nil {
		return nil, hostapi.ErrPluginJobEnqueueUnavailable
	}
	if snapshot.Identity.ExtensionID != identity.ExtensionID ||
		snapshot.Identity.InstanceID != identity.InstanceID ||
		snapshot.ExtensionVersion != identity.ExtensionVersion ||
		snapshot.ArtifactDigest != identity.ArtifactDigest {
		lease.Release()
		return nil, hostapi.ErrPluginJobEnqueueStale
	}
	return &pluginJobEnqueueLease{lease: lease, caller: ctx}, nil
}

func mapPluginJobAdmissionError(err error) error {
	switch {
	case errors.Is(err, extensionsruntime.ErrRuntimeAdmissionDraining),
		errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced):
		return errors.Join(hostapi.ErrPluginJobEnqueueDraining, err)
	case errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound),
		errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotActive):
		return errors.Join(hostapi.ErrPluginJobEnqueueStale, err)
	case errors.Is(err, extensions.ErrRuntimeUnavailable):
		return errors.Join(hostapi.ErrPluginJobEnqueueUnavailable, err)
	default:
		return errors.Join(hostapi.ErrPluginJobEnqueueUnavailable, err)
	}
}

func (l *pluginJobEnqueueLease) Context() context.Context {
	if l == nil || l.lease == nil {
		return nil
	}
	return l.lease.Context
}

func (l *pluginJobEnqueueLease) PluginJobEnqueueFailure() error {
	if l == nil || l.lease == nil || l.lease.Context == nil {
		return hostapi.ErrPluginJobEnqueueUnavailable
	}
	if l.caller != nil && l.caller.Err() != nil {
		return context.Cause(l.caller)
	}
	cause := context.Cause(l.lease.Context)
	if l.lease.Context.Err() != nil {
		return errors.Join(hostapi.ErrPluginJobEnqueueDraining, cause)
	}
	return cause
}

func (l *pluginJobEnqueueLease) Release() {
	if l != nil && l.lease != nil {
		l.lease.Release()
	}
}
