package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

type executableTrustPolicyInvalidator interface {
	CaptureExecutableTrustExactWithFallback(string, extensions.GuardPolicyEntry) (extensions.GuardPolicyEntry, bool)
	ReleaseExecutableTrustCaptureExact(string, extensions.GuardPolicyEntry) bool
	InvalidateExecutableTrustExact(string, extensions.GuardPolicyEntry) bool
}

// ExecutableTrustRevocationFence serializes the durable revoke with the local
// runtime full-set. PostgreSQL publication converges other API/worker nodes;
// this fence closes the exact process on the initiating node before returning.
type ExecutableTrustRevocationFence struct {
	runtime  *Manager
	policies executableTrustPolicyInvalidator
}

func NewExecutableTrustRevocationFence(
	runtime *Manager,
	policies executableTrustPolicyInvalidator,
) *ExecutableTrustRevocationFence {
	return &ExecutableTrustRevocationFence{runtime: runtime, policies: policies}
}

func (f *ExecutableTrustRevocationFence) RevokeExecutableTrust(
	ctx context.Context,
	extensionID string,
	reason string,
	durable func(context.Context) error,
) error {
	if f == nil || f.runtime == nil || f.policies == nil || ctx == nil || durable == nil {
		return ErrRuntimeAdmissionInvalid
	}
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return ErrRuntimeAdmissionInvalid
	}
	unlock, err := f.runtime.lockRuntimeSetTransition(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	var exact RuntimeInstanceArtifactIdentity
	drainedByFence := false
	active, runtimeErr := f.runtime.ActiveRuntimeInstance(extensionID)
	if runtimeErr == nil {
		exact = RuntimeInstanceArtifactIdentity{
			RuntimeInstanceIdentity: active.Identity,
			ExtensionVersion:        active.ExtensionVersion,
			ArtifactDigest:          active.ArtifactDigest,
		}
		if !active.Admission.Draining && !active.Admission.Quarantined && !active.Admission.Forced {
			if _, err := f.runtime.beginDrainRuntimeSetLocked(ctx, active.Identity); err != nil {
				return err
			}
			drainedByFence = true
		}
	} else if !errors.Is(runtimeErr, ErrRuntimeInstanceNotFound) {
		return runtimeErr
	}
	policyFallback := extensions.GuardPolicyEntry{ExtensionID: extensionID}
	if exact.InstanceID != "" {
		policyFallback.Version = exact.ExtensionVersion
		policyFallback.PackageDigest = exact.ArtifactDigest
		policyFallback.CurrentTrustRequired = true
		policyFallback.CurrentArtifactTrusted = true
	}
	policy, _ := f.policies.CaptureExecutableTrustExactWithFallback(extensionID, policyFallback)

	durableErr := durable(ctx)
	commitUnknown := errors.Is(durableErr, extensions.ErrTrustRevocationCommitUnknown)
	if durableErr != nil && !commitUnknown {
		policyReleased := f.policies.ReleaseExecutableTrustCaptureExact(extensionID, policy)
		var policyErr error
		if !policyReleased {
			policyErr = fmt.Errorf("%w: release exact guard policy capture", ErrRuntimeAdmissionInvalid)
		}
		if drainedByFence {
			compensationCtx, cancel := lifecycleBoundaryCompensationContext(ctx)
			_, resumeErr := f.runtime.resumeRuntimeInstanceRuntimeSetLocked(compensationCtx, exact.RuntimeInstanceIdentity)
			cancel()
			return errors.Join(durableErr, policyErr, resumeErr)
		}
		return errors.Join(durableErr, policyErr)
	}
	cause := error(ErrRuntimeTrustRevoked)
	if reason = strings.TrimSpace(reason); reason != "" {
		cause = fmt.Errorf("%w: %s", ErrRuntimeTrustRevoked, reason)
	}
	runtimeErr = nil
	if exact.InstanceID != "" {
		_, runtimeErr = f.runtime.QuarantineRuntimeArtifact(exact, cause)
	}
	f.policies.InvalidateExecutableTrustExact(extensionID, policy)
	return errors.Join(durableErr, runtimeErr)
}

var _ extensions.ExecutableTrustRevocationSink = (*ExecutableTrustRevocationFence)(nil)
