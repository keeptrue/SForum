package extensionsruntime

import (
	"context"
	"fmt"
)

import extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"

type componentHeldAdmission struct {
	request      ComponentRuntimeAdmissionRequest
	target       ComponentTarget
	contribution ComponentContribution
	lease        ComponentRuntimeAdmissionLease
}

func (r *ComponentRegistry) admitComponentRevision(revision uint64) bool {
	return r != nil && r.load().revision == revision
}

// admitComponentContribution proves that the exact artifact is still in the
// same immutable plan. It is used immediately before invocation and again
// before validated output is released.
func (r *ComponentRegistry) admitComponentContribution(
	revision uint64,
	target ComponentTarget,
	contribution ComponentContribution,
) bool {
	if r == nil {
		return false
	}
	state := r.load()
	if state.revision != revision {
		return false
	}
	currentTarget, found := state.targetsByID[target.ID]
	if !found || currentTarget.ContractVersion != target.ContractVersion {
		return false
	}
	registration, found := state.registrations[contribution.Artifact.ExtensionID]
	if !found || registration.instanceID != contribution.Artifact.RuntimeInstanceID ||
		registration.extension.ID != contribution.Artifact.ExtensionID ||
		registration.extension.Version != contribution.Artifact.ExtensionVersion ||
		registration.extension.PackageDigest != contribution.Artifact.PackageDigest {
		return false
	}
	stored, found := state.contributionsByID[contribution.ID]
	if !found || !sameComponentRuntimeContribution(stored, contribution) {
		return false
	}
	if currentTarget.Provider != nil && sameComponentRuntimeContribution(*currentTarget.Provider, contribution) {
		return true
	}
	if contribution.Action == "replace" {
		winner, found := state.replaceWinnerByTarget[currentTarget.ID]
		return found && sameComponentRuntimeContribution(winner, contribution)
	}
	for _, current := range state.contributionsByTarget[currentTarget.ID] {
		if sameComponentRuntimeContribution(current, contribution) {
			return true
		}
	}
	return false
}

func (r *ComponentRegistry) componentContributionOwnedByTheme(
	revision uint64,
	contribution ComponentContribution,
) bool {
	if r == nil {
		return false
	}
	state := r.load()
	registration, found := state.registrations[contribution.Artifact.ExtensionID]
	return found && state.revision == revision && registration.extension.Type == extensions.TypeTheme &&
		registration.instanceID == contribution.Artifact.RuntimeInstanceID &&
		registration.extension.Version == contribution.Artifact.ExtensionVersion &&
		registration.extension.PackageDigest == contribution.Artifact.PackageDigest
}

func (r *componentCompositionRun) acquireComponentAdmission(
	ctx context.Context,
	plan ComponentResolvePlan,
	contribution ComponentContribution,
) (componentHeldAdmission, error) {
	allowed, err := r.authorizeComponentContribution(ctx, contribution)
	if err != nil {
		return componentHeldAdmission{}, err
	}
	if !allowed {
		return componentHeldAdmission{}, ErrComponentCompositionPermissionDenied
	}
	if r == nil || r.executor == nil || r.executor.admission == nil ||
		!r.executor.registry.admitComponentContribution(r.revision, plan.Target, contribution) {
		return componentHeldAdmission{}, ErrComponentCompositionStale
	}
	request := ComponentRuntimeAdmissionRequest{
		Revision: r.revision, TargetID: plan.Target.ID,
		TargetContractVersion: plan.Target.ContractVersion,
		ContributionID:        contribution.ID, Action: contribution.Action,
		Artifact: contribution.Artifact,
	}
	lease, err := r.executor.admission.AcquireComponentRuntime(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return componentHeldAdmission{}, ctx.Err()
		}
		return componentHeldAdmission{}, fmt.Errorf("%w: %v", ErrComponentCompositionUnauthorized, err)
	}
	held := componentHeldAdmission{
		request: request, target: plan.Target, contribution: contribution, lease: lease,
	}
	if err := validateComponentAdmissionLease(ctx, lease); err != nil {
		if lease != nil {
			lease.Release()
		}
		return componentHeldAdmission{}, err
	}
	// Publication can change while Manager acquires the process lease. Both
	// authorities must still name the same immutable contribution.
	if !r.executor.registry.admitComponentContribution(r.revision, plan.Target, contribution) {
		lease.Release()
		return componentHeldAdmission{}, ErrComponentCompositionStale
	}
	return held, nil
}

func validateComponentAdmissionLease(ctx context.Context, lease ComponentRuntimeAdmissionLease) error {
	if lease == nil || lease.Context() == nil {
		return ErrComponentCompositionUnauthorized
	}
	if err := lease.Context().Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrComponentCompositionUnauthorized, err)
	}
	if err := lease.Validate(ctx); err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrComponentCompositionUnauthorized, err)
	}
	if err := lease.Context().Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrComponentCompositionUnauthorized, err)
	}
	return nil
}

func (r *componentCompositionRun) holdComponentAdmission(held componentHeldAdmission) {
	r.admissions = append(r.admissions, held)
}

func (r *componentCompositionRun) releaseComponentAdmissions() {
	for index := len(r.admissions) - 1; index >= 0; index-- {
		if r.admissions[index].lease != nil {
			r.admissions[index].lease.Release()
		}
	}
	r.admissions = nil
}

// validateComponentAdmissions is the final output-release fence. Callers run
// it only after the complete result has been cloned and bounded, while every
// exact lease is still held.
func (r *componentCompositionRun) validateComponentAdmissions(ctx context.Context) error {
	for _, held := range r.admissions {
		if !r.executor.registry.admitComponentContribution(r.revision, held.target, held.contribution) {
			return ErrComponentCompositionStale
		}
		if err := validateComponentAdmissionLease(ctx, held.lease); err != nil {
			return err
		}
		if !r.executor.registry.admitComponentContribution(r.revision, held.target, held.contribution) {
			return ErrComponentCompositionStale
		}
	}
	return nil
}
