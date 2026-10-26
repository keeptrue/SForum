package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func (b *ComposedLifecycleHostBoundary) publishActivation(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	jobMode LifecycleBoundaryJobMode,
) error {
	if b.dependencies.Runtime == nil {
		return lifecycleBoundaryMissing("exact runtime publication", request)
	}
	if err := b.drainSourceAdmissions(ctx, request, jobMode); err != nil {
		return b.failBeforePublication(ctx, request, err)
	}
	if err := b.drainTargetAdmissions(ctx, request, jobMode); err != nil {
		compensationCtx, cancel := lifecycleBoundaryCompensationContext(ctx)
		closeErr := b.closeFailedTargetRuntime(compensationCtx, request)
		cancel()
		if closeErr != nil {
			return lifecycleBoundaryFailure(err, []error{fmt.Errorf("close target runtime: %w", closeErr)})
		}
		// The target jobs/schedules drain returned an aggregate error, so its
		// admission state is not proven. Keep both roles closed instead of
		// reopening source against an ambiguous target trigger snapshot.
		return err
	}
	state, jobs, registries, committed, err := b.preparePublication(ctx, request, jobMode, LifecycleBoundaryActivate)
	if err != nil {
		return err
	}
	transactions := []namedLifecycleCompensation{
		{"registries", registries}, {"jobs and schedules", jobs}, {"state", state},
	}

	target, err := lifecycleBoundaryTargetIdentity(request)
	if err != nil {
		return b.failActivationPhase(ctx, request, jobMode, committed, err, transactions)
	}
	snapshot, publishErr := b.dependencies.Runtime.PublishDrainedRuntimeInstance(ctx, target)
	if publishErr == nil {
		publishErr = validateLifecycleBoundaryRuntimeSnapshot("publish drained target", snapshot, request.TargetExtension, target, true)
	}
	if publishErr == nil {
		publishErr = validateLifecycleBoundaryAdmission("publish drained target", snapshot.Admission, target, true, true)
	}
	if publishErr != nil {
		return b.failActivationPhase(ctx, request, jobMode, committed, publishErr, transactions)
	}

	if err := state.Publish(ctx); err != nil {
		return b.failActivationPhase(ctx, request, jobMode, committed, err, transactions)
	}
	if err := jobs.Publish(ctx); err != nil {
		return b.failActivationPhase(ctx, request, jobMode, committed, err, transactions)
	}
	if err := registries.Publish(ctx); err != nil {
		return b.failActivationPhase(ctx, request, jobMode, committed, err, transactions)
	}
	if err := requireLifecycleTransactionsTarget(ctx, transactions); err != nil {
		return b.failActivationPhase(ctx, request, jobMode, committed, err, transactions)
	}
	commitState, err := b.commitPublication(ctx, request, LifecycleBoundaryActivate)
	if err != nil {
		if commitState == lifecyclePublicationCommitUnknown {
			return err
		}
		return b.failActivationPhase(ctx, request, jobMode, false, err, transactions)
	}
	if err := b.dependencies.Jobs.ReconcileCommittedLifecycleJobs(
		ctx, cloneLifecycleBoundaryRequest(request), jobMode, LifecycleBoundaryActivate,
	); err != nil {
		return b.failCommittedActivation(ctx, request, jobMode, err)
	}
	if err := b.dependencies.Jobs.ResumeLifecycleJobs(
		ctx, cloneLifecycleBoundaryRequest(request), jobMode, extensions.LifecycleRuntimeTarget,
	); err != nil {
		return b.failCommittedActivation(ctx, request, jobMode, err)
	}
	return nil
}

func (b *ComposedLifecycleHostBoundary) publishDeactivation(ctx context.Context, request LifecycleBoundaryRequest) error {
	if b.dependencies.Runtime == nil {
		return lifecycleBoundaryMissing("exact runtime publication", request)
	}
	jobMode := LifecycleBoundaryJobsDisable
	if request.Operation == extensions.LifecycleMachineUninstall {
		jobMode = LifecycleBoundaryJobsUninstall
	}
	if err := b.drainSourceAdmissions(ctx, request, jobMode); err != nil {
		return b.failBeforePublication(ctx, request, fmt.Errorf("deactivation drain source admissions: %w", err))
	}
	state, jobs, registries, committed, err := b.preparePublication(ctx, request, jobMode, LifecycleBoundaryDeactivate)
	if err != nil {
		return fmt.Errorf("prepare deactivation publication: %w", err)
	}
	transactions := []namedLifecycleCompensation{
		{"registries", registries}, {"jobs and schedules", jobs}, {"state", state},
	}

	if err := state.Publish(ctx); err != nil {
		return b.failDeactivationPhase(ctx, request, committed, err, transactions)
	}
	if err := jobs.Publish(ctx); err != nil {
		return b.failDeactivationPhase(ctx, request, committed, err, transactions)
	}
	if err := registries.Publish(ctx); err != nil {
		return b.failDeactivationPhase(ctx, request, committed, err, transactions)
	}
	if err := requireLifecycleTransactionsTarget(ctx, transactions); err != nil {
		return b.failDeactivationPhase(ctx, request, committed, err, transactions)
	}
	commitState, err := b.commitPublication(ctx, request, LifecycleBoundaryDeactivate)
	if err != nil {
		if commitState == lifecyclePublicationCommitUnknown {
			return err
		}
		return b.failDeactivationPhase(ctx, request, false, err, transactions)
	}
	if err := b.dependencies.Jobs.ReconcileCommittedLifecycleJobs(
		ctx, cloneLifecycleBoundaryRequest(request), jobMode, LifecycleBoundaryDeactivate,
	); err != nil {
		// The marker is authoritative. Source runtime, enqueue, and schedule
		// admissions stay closed; a retry may only converge forward.
		return err
	}
	return nil
}

func (b *ComposedLifecycleHostBoundary) preparePublication(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	jobMode LifecycleBoundaryJobMode,
	mode LifecycleBoundaryPublicationMode,
) (LifecycleBoundaryTransaction, LifecycleBoundaryTransaction, LifecycleBoundaryTransaction, bool, error) {
	if b.dependencies.Journal == nil {
		return nil, nil, nil, false, lifecycleBoundaryMissing("publication journal", request)
	}
	if b.dependencies.State == nil {
		return nil, nil, nil, false, lifecycleBoundaryMissing("durable state", request)
	}
	if b.dependencies.Jobs == nil {
		return nil, nil, nil, false, lifecycleBoundaryMissing("jobs and schedules", request)
	}
	if b.dependencies.Registries == nil {
		return nil, nil, nil, false, lifecycleBoundaryMissing("registries", request)
	}
	if err := b.dependencies.Journal.PrepareLifecyclePublication(ctx, cloneLifecycleBoundaryRequest(request), mode); err != nil {
		return nil, nil, nil, false, err
	}
	committed, err := b.dependencies.Journal.LifecyclePublicationCommitted(ctx, cloneLifecycleBoundaryRequest(request), mode)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if err := b.dependencies.Jobs.ValidateLifecycleJobs(ctx, cloneLifecycleBoundaryRequest(request), jobMode); err != nil {
		return nil, nil, nil, committed, err
	}
	if err := b.dependencies.Registries.ValidateLifecycleRegistries(ctx, cloneLifecycleBoundaryRequest(request)); err != nil {
		return nil, nil, nil, committed, err
	}
	state, err := b.dependencies.State.PrepareLifecycleStatePublication(ctx, cloneLifecycleBoundaryRequest(request), mode)
	if err != nil {
		return nil, nil, nil, committed, err
	}
	if state == nil {
		return nil, nil, nil, committed, lifecycleBoundaryMissing("durable state transaction", request)
	}
	jobs, err := b.dependencies.Jobs.PrepareLifecycleJobPublication(ctx, cloneLifecycleBoundaryRequest(request), mode)
	if err != nil {
		return nil, nil, nil, committed, err
	}
	if jobs == nil {
		return nil, nil, nil, committed, lifecycleBoundaryMissing("job transaction", request)
	}
	registries, err := b.dependencies.Registries.PrepareLifecycleRegistryPublication(ctx, cloneLifecycleBoundaryRequest(request), mode)
	if err != nil {
		return nil, nil, nil, committed, err
	}
	if registries == nil {
		return nil, nil, nil, committed, lifecycleBoundaryMissing("registry transaction", request)
	}
	transactions := []namedLifecycleCompensation{
		{"registries", registries}, {"jobs and schedules", jobs}, {"state", state},
	}
	states, err := inspectLifecycleTransactions(ctx, transactions)
	if err != nil {
		return nil, nil, nil, committed, err
	}
	if !committed && lifecycleTransactionsContainTarget(states) {
		if err := b.restoreUncommittedPublication(ctx, request, jobMode, transactions); err != nil {
			return nil, nil, nil, false, err
		}
	}
	return state, jobs, registries, committed, nil
}

type namedLifecycleCompensation struct {
	name        string
	transaction LifecycleBoundaryTransaction
}

func inspectLifecycleTransactions(
	ctx context.Context,
	transactions []namedLifecycleCompensation,
) ([]LifecycleBoundaryTransactionState, error) {
	states := make([]LifecycleBoundaryTransactionState, 0, len(transactions))
	for _, transaction := range transactions {
		state, err := transaction.transaction.Inspect(ctx)
		if err != nil {
			return nil, fmt.Errorf("inspect %s publication: %w", transaction.name, err)
		}
		if state != LifecycleBoundaryTransactionSource && state != LifecycleBoundaryTransactionTarget {
			return nil, fmt.Errorf("%w: %s publication returned state %q", ErrLifecycleBoundaryInvalid, transaction.name, state)
		}
		states = append(states, state)
	}
	return states, nil
}

func lifecycleTransactionsContainTarget(states []LifecycleBoundaryTransactionState) bool {
	for _, state := range states {
		if state == LifecycleBoundaryTransactionTarget {
			return true
		}
	}
	return false
}

func requireLifecycleTransactionsTarget(ctx context.Context, transactions []namedLifecycleCompensation) error {
	states, err := inspectLifecycleTransactions(ctx, transactions)
	if err != nil {
		return err
	}
	for _, state := range states {
		if state != LifecycleBoundaryTransactionTarget {
			return fmt.Errorf("%w: publication did not converge to the exact target", ErrLifecycleBoundaryInvalid)
		}
	}
	return nil
}

func (b *ComposedLifecycleHostBoundary) restoreUncommittedPublication(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	jobMode LifecycleBoundaryJobMode,
	transactions []namedLifecycleCompensation,
) error {
	errs := restoreLifecycleTransactions(ctx, transactions)
	if len(errs) != 0 {
		return lifecycleBoundaryFailure(
			fmt.Errorf("%w: uncommitted publication did not restore", ErrLifecycleBoundaryInvalid), errs,
		)
	}
	if err := b.restoreSourceRuntimeDrained(ctx, request); err != nil {
		return fmt.Errorf("restore uncommitted runtime: %w", err)
	}
	if request.SourceExtension != nil {
		if err := b.dependencies.Jobs.DrainLifecycleJobs(
			ctx, cloneLifecycleBoundaryRequest(request), jobMode, extensions.LifecycleRuntimeSource,
		); err != nil {
			return fmt.Errorf("restore uncommitted job admission: %w", err)
		}
	}
	states, err := inspectLifecycleTransactions(ctx, transactions)
	if err != nil {
		return err
	}
	for _, state := range states {
		if state != LifecycleBoundaryTransactionSource {
			return fmt.Errorf("%w: uncommitted publication did not converge to source", ErrLifecycleBoundaryInvalid)
		}
	}
	return nil
}

type lifecyclePublicationCommitState uint8

const (
	lifecyclePublicationCommitUnknown lifecyclePublicationCommitState = iota
	lifecyclePublicationCommitSource
	lifecyclePublicationCommitTarget
)

func (b *ComposedLifecycleHostBoundary) commitPublication(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) (lifecyclePublicationCommitState, error) {
	err := b.dependencies.Journal.CommitLifecyclePublication(ctx, cloneLifecycleBoundaryRequest(request), mode)
	if err == nil {
		return lifecyclePublicationCommitTarget, nil
	}
	inspectionCtx, cancel := lifecycleBoundaryCompensationContext(ctx)
	defer cancel()
	committed, inspectErr := b.dependencies.Journal.LifecyclePublicationCommitted(
		inspectionCtx, cloneLifecycleBoundaryRequest(request), mode,
	)
	if inspectErr != nil {
		return lifecyclePublicationCommitUnknown, errors.Join(err, fmt.Errorf("inspect publication marker after commit failure: %w", inspectErr))
	}
	if committed {
		return lifecyclePublicationCommitTarget, nil
	}
	return lifecyclePublicationCommitSource, err
}

func (b *ComposedLifecycleHostBoundary) failActivationPhase(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	jobMode LifecycleBoundaryJobMode,
	committed bool,
	cause error,
	transactions []namedLifecycleCompensation,
) error {
	if committed {
		return b.failCommittedActivation(ctx, request, jobMode, cause)
	}
	return b.failActivation(ctx, request, cause, transactions...)
}

func (b *ComposedLifecycleHostBoundary) failDeactivationPhase(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	committed bool,
	cause error,
	transactions []namedLifecycleCompensation,
) error {
	if committed {
		// Source runtime, enqueue, and schedule admissions were closed before
		// inspecting the marker. Postcommit recovery can only converge forward.
		return cause
	}
	return b.failDeactivation(ctx, request, cause, transactions...)
}

func (b *ComposedLifecycleHostBoundary) failCommittedActivation(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	jobMode LifecycleBoundaryJobMode,
	cause error,
) error {
	compensationCtx, cancel := lifecycleBoundaryCompensationContext(ctx)
	defer cancel()
	errs := make([]error, 0, 2)
	if err := b.dependencies.Jobs.DrainLifecycleJobs(
		compensationCtx, cloneLifecycleBoundaryRequest(request), jobMode, extensions.LifecycleRuntimeTarget,
	); err != nil {
		errs = append(errs, fmt.Errorf("close target jobs and schedules: %w", err))
	}
	target, err := lifecycleBoundaryTargetIdentity(request)
	if err == nil {
		err = b.drainRuntime(compensationCtx, target)
	}
	if err != nil {
		errs = append(errs, fmt.Errorf("close target runtime: %w", err))
	}
	return lifecycleBoundaryFailure(cause, errs)
}

func (b *ComposedLifecycleHostBoundary) failActivation(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	cause error,
	transactions ...namedLifecycleCompensation,
) error {
	compensationCtx, cancel := lifecycleBoundaryCompensationContext(ctx)
	defer cancel()
	errs := restoreLifecycleTransactions(compensationCtx, transactions)
	if len(errs) == 0 {
		if err := b.requireLifecycleSourceResumeProof(compensationCtx, request); err != nil {
			closeErr := b.closeFailedTargetRuntime(compensationCtx, request)
			errs = append(errs, fmt.Errorf("migration resume proof: %w", errors.Join(err, closeErr)))
		} else if err := b.restoreSourceRuntime(compensationCtx, request); err != nil {
			closeErr := b.closeSourceAdmissions(compensationCtx, request)
			errs = append(errs, fmt.Errorf("runtime: %w", errors.Join(err, closeErr)))
		} else if request.SourceExtension != nil {
			if err := b.openSourceLifecycleJobs(compensationCtx, request); err != nil {
				errs = append(errs, fmt.Errorf("jobs and schedules: %w", err))
			}
		}
	} else if err := b.closeFailedTargetRuntime(compensationCtx, request); err != nil {
		errs = append(errs, fmt.Errorf("close target runtime: %w", err))
	}
	return lifecycleBoundaryFailure(cause, errs)
}

func (b *ComposedLifecycleHostBoundary) failDeactivation(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	cause error,
	transactions ...namedLifecycleCompensation,
) error {
	compensationCtx, cancel := lifecycleBoundaryCompensationContext(ctx)
	defer cancel()
	errs := restoreLifecycleTransactions(compensationCtx, transactions)
	if len(errs) == 0 {
		if err := b.resumeSourceAdmissionsDuringCompensation(compensationCtx, request); err != nil {
			errs = append(errs, err)
		}
	}
	return lifecycleBoundaryFailure(cause, errs)
}

func (b *ComposedLifecycleHostBoundary) failBeforePublication(ctx context.Context, request LifecycleBoundaryRequest, cause error) error {
	if request.SourceExtension == nil {
		return cause
	}
	compensationCtx, cancel := lifecycleBoundaryCompensationContext(ctx)
	defer cancel()
	if b.dependencies.Journal == nil {
		return lifecycleBoundaryFailure(cause, []error{lifecycleBoundaryMissing("publication journal", request)})
	}
	publication, mode, err := lifecycleBoundaryCanonicalPublication(request)
	if err != nil {
		return lifecycleBoundaryFailure(cause, []error{err})
	}
	committed, err := b.dependencies.Journal.LifecyclePublicationCommittedForOperation(
		compensationCtx, publication, mode,
	)
	if err != nil {
		return lifecycleBoundaryFailure(cause, []error{fmt.Errorf("inspect publication marker: %w", err)})
	}
	if committed {
		return cause
	}
	if b.dependencies.Runtime == nil {
		return lifecycleBoundaryFailure(cause, []error{lifecycleBoundaryMissing("exact runtime publication", request)})
	}
	if err := b.resumeSourceAdmissionsDuringCompensation(compensationCtx, request); err != nil {
		return lifecycleBoundaryFailure(cause, []error{err})
	}
	return cause
}

func restoreLifecycleTransactions(ctx context.Context, transactions []namedLifecycleCompensation) []error {
	errs := make([]error, 0)
	for _, compensation := range transactions {
		if compensation.transaction == nil {
			errs = append(errs, fmt.Errorf("%s transaction is unavailable", compensation.name))
			continue
		}
		if err := compensation.transaction.Restore(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", compensation.name, err))
		}
	}
	return errs
}

func (b *ComposedLifecycleHostBoundary) restoreSourceRuntime(ctx context.Context, request LifecycleBoundaryRequest) error {
	target, targetErr := lifecycleBoundaryTargetIdentity(request)
	if targetErr != nil {
		return targetErr
	}
	drainErr := b.drainRuntime(ctx, target)
	if request.SourceExtension == nil {
		stopErr := b.dependencies.Runtime.StopRuntimeInstance(ctx, target)
		if errors.Is(stopErr, ErrRuntimeInstanceNotFound) {
			stopErr = nil
		}
		return errors.Join(drainErr, stopErr)
	}

	source, err := lifecycleBoundarySourceIdentity(request)
	if err != nil {
		return errors.Join(drainErr, err)
	}
	snapshot, publishErr := b.dependencies.Runtime.PublishDrainedRuntimeInstance(ctx, source)
	if publishErr == nil {
		publishErr = validateLifecycleBoundaryRuntimeSnapshot("restore drained source", snapshot, *request.SourceExtension, source, true)
	}
	if publishErr == nil {
		publishErr = validateLifecycleBoundaryAdmission("restore drained source", snapshot.Admission, source, true, true)
	}
	return errors.Join(drainErr, publishErr)
}

func (b *ComposedLifecycleHostBoundary) restoreSourceRuntimeDrained(ctx context.Context, request LifecycleBoundaryRequest) error {
	target, err := lifecycleBoundaryTargetIdentity(request)
	if err != nil {
		return err
	}
	drainErr := b.drainRuntime(ctx, target)
	if request.SourceExtension == nil {
		// A retry is about to republish this exact candidate. Keep it retained and
		// drained; final failure compensation owns stopping an uncommitted target.
		return drainErr
	}
	source, sourceErr := lifecycleBoundarySourceIdentity(request)
	if sourceErr != nil {
		return errors.Join(drainErr, sourceErr)
	}
	snapshot, publishErr := b.dependencies.Runtime.PublishDrainedRuntimeInstance(ctx, source)
	if publishErr == nil {
		publishErr = validateLifecycleBoundaryRuntimeSnapshot("restore drained source", snapshot, *request.SourceExtension, source, true)
	}
	if publishErr == nil {
		publishErr = validateLifecycleBoundaryAdmission("restore drained source", snapshot.Admission, source, true, true)
	}
	return errors.Join(drainErr, publishErr)
}

// closeFailedTargetRuntime keeps admission fail-closed when a durable or
// registry snapshot could not be proven restored. Reopening source code against
// an unknown database/registry state would be an unsafe fallback.
func (b *ComposedLifecycleHostBoundary) closeFailedTargetRuntime(ctx context.Context, request LifecycleBoundaryRequest) error {
	target, err := lifecycleBoundaryTargetIdentity(request)
	if err != nil {
		return err
	}
	drainErr := b.drainRuntime(ctx, target)
	if request.SourceExtension != nil {
		return drainErr
	}
	stopErr := b.dependencies.Runtime.StopRuntimeInstance(ctx, target)
	if errors.Is(stopErr, ErrRuntimeInstanceNotFound) {
		stopErr = nil
	}
	return errors.Join(drainErr, stopErr)
}

func (b *ComposedLifecycleHostBoundary) drainSourceAdmissions(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	jobMode LifecycleBoundaryJobMode,
) error {
	if request.SourceExtension == nil {
		return nil
	}
	if b.dependencies.Runtime == nil {
		return lifecycleBoundaryMissing("exact runtime publication", request)
	}
	if b.dependencies.Jobs == nil {
		return lifecycleBoundaryMissing("jobs and schedules", request)
	}
	jobErr := b.dependencies.Jobs.DrainLifecycleJobs(
		ctx, cloneLifecycleBoundaryRequest(request), jobMode, extensions.LifecycleRuntimeSource,
	)
	source, err := lifecycleBoundarySourceIdentity(request)
	if err != nil {
		return errors.Join(jobErr, err)
	}
	return errors.Join(jobErr, b.drainRuntime(ctx, source))
}

func (b *ComposedLifecycleHostBoundary) drainTargetAdmissions(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	jobMode LifecycleBoundaryJobMode,
) error {
	if b.dependencies.Jobs == nil {
		return lifecycleBoundaryMissing("jobs and schedules", request)
	}
	jobErr := b.dependencies.Jobs.DrainLifecycleJobs(
		ctx, cloneLifecycleBoundaryRequest(request), jobMode, extensions.LifecycleRuntimeTarget,
	)
	target, err := lifecycleBoundaryTargetIdentity(request)
	if err != nil {
		return errors.Join(jobErr, err)
	}
	return errors.Join(jobErr, b.drainRuntime(ctx, target))
}

func (b *ComposedLifecycleHostBoundary) resumeSourceAdmissions(ctx context.Context, request LifecycleBoundaryRequest) error {
	if request.SourceExtension == nil {
		return nil
	}
	if err := b.requireLifecycleSourceResumeProof(ctx, request); err != nil {
		closeErr := b.closeSourceAdmissions(ctx, request)
		return fmt.Errorf("migration resume proof: %w", errors.Join(err, closeErr))
	}
	if err := b.openSourceLifecycleJobs(ctx, request); err != nil {
		return fmt.Errorf("jobs and schedules: %w", err)
	}
	return nil
}

// resumeSourceAdmissionsDuringCompensation reopens the source surface while a
// failed lifecycle step unwinds. A retry can run after the exact source
// instance was already retired (draining/starting rotated it); there is then no
// source surface left to reopen, so that state counts as converged. Treating it
// as a compensation failure would keep the operation half-restored and hide the
// real cause behind a secondary error.
func (b *ComposedLifecycleHostBoundary) resumeSourceAdmissionsDuringCompensation(
	ctx context.Context,
	request LifecycleBoundaryRequest,
) error {
	err := b.resumeSourceAdmissions(ctx, request)
	if err == nil || !lifecycleRuntimeInstanceGone(err) {
		return err
	}
	slog.WarnContext(ctx, "lifecycle compensation: source runtime already retired",
		"extensionId", request.TargetExtension.ID,
		"operation", request.Operation, "position", request.Position, "error", err)
	return nil
}

func lifecycleRuntimeInstanceGone(err error) bool {
	return errors.Is(err, ErrRuntimeInstanceNotActive) || errors.Is(err, ErrRuntimeInstanceNotFound)
}

func (b *ComposedLifecycleHostBoundary) requireLifecycleSourceResumeProof(
	ctx context.Context,
	request LifecycleBoundaryRequest,
) error {
	var mode LifecycleBoundaryMigrationMode
	switch request.Operation {
	case extensions.LifecycleMachineUpgrade:
		mode = LifecycleBoundaryMigrationUpgrade
	case extensions.LifecycleMachineRollback:
		mode = LifecycleBoundaryMigrationRollback
	default:
		return nil
	}
	if b.dependencies.Migrations == nil {
		return lifecycleBoundaryMissing("migration source-resume proof", request)
	}
	allowed, err := b.dependencies.Migrations.CanResumeLifecycleSource(
		ctx, cloneLifecycleBoundaryRequest(request), mode,
	)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrLifecycleBoundarySourceResumeUnsafe
	}
	return nil
}

func (b *ComposedLifecycleHostBoundary) resumeSourceLifecycleJobs(ctx context.Context, request LifecycleBoundaryRequest) error {
	if b.dependencies.Jobs == nil {
		return lifecycleBoundaryMissing("jobs and schedules", request)
	}
	mode, err := lifecycleBoundaryJobModeForOperation(request.Operation)
	if err != nil {
		return err
	}
	return b.dependencies.Jobs.ResumeLifecycleJobs(
		ctx, cloneLifecycleBoundaryRequest(request), mode, extensions.LifecycleRuntimeSource,
	)
}

func (b *ComposedLifecycleHostBoundary) openSourceLifecycleJobs(ctx context.Context, request LifecycleBoundaryRequest) error {
	err := b.resumeSourceLifecycleJobs(ctx, request)
	if err == nil {
		return nil
	}
	closeErr := b.closeSourceAdmissions(ctx, request)
	return errors.Join(err, closeErr)
}

func (b *ComposedLifecycleHostBoundary) closeSourceAdmissions(ctx context.Context, request LifecycleBoundaryRequest) error {
	mode, err := lifecycleBoundaryJobModeForOperation(request.Operation)
	if err != nil {
		return err
	}
	var errs []error
	if b.dependencies.Jobs != nil {
		if drainErr := b.dependencies.Jobs.DrainLifecycleJobs(
			ctx, cloneLifecycleBoundaryRequest(request), mode, extensions.LifecycleRuntimeSource,
		); drainErr != nil {
			errs = append(errs, drainErr)
		}
	}
	source, sourceErr := lifecycleBoundarySourceIdentity(request)
	if sourceErr != nil {
		errs = append(errs, sourceErr)
	} else if drainErr := b.drainRuntime(ctx, source); drainErr != nil {
		errs = append(errs, drainErr)
	}
	return errors.Join(errs...)
}

func (b *ComposedLifecycleHostBoundary) drainRuntime(ctx context.Context, identity RuntimeInstanceIdentity) error {
	snapshot, err := b.dependencies.Runtime.BeginDrain(identity)
	if errors.Is(err, ErrRuntimeInstanceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateLifecycleBoundaryAdmission("drain runtime", snapshot, identity, true, false); err != nil {
		return err
	}
	if err := b.dependencies.Runtime.WaitDrain(ctx, identity); errors.Is(err, ErrRuntimeInstanceNotFound) {
		return nil
	} else {
		return err
	}
}

func (b *ComposedLifecycleHostBoundary) stopSourceRuntime(ctx context.Context, request LifecycleBoundaryRequest) error {
	if b.dependencies.Runtime == nil {
		return lifecycleBoundaryMissing("exact runtime publication", request)
	}
	source, err := lifecycleBoundarySourceIdentity(request)
	if err != nil {
		return err
	}
	if err := b.drainRuntime(ctx, source); err != nil {
		return err
	}
	err = b.dependencies.Runtime.StopRuntimeInstance(ctx, source)
	if errors.Is(err, ErrRuntimeInstanceNotFound) {
		return nil
	}
	return err
}

func lifecycleBoundaryTargetIdentity(request LifecycleBoundaryRequest) (RuntimeInstanceIdentity, error) {
	return lifecycleBoundaryIdentity("target", request.TargetBinding, request.TargetExtension)
}

func lifecycleBoundarySourceIdentity(request LifecycleBoundaryRequest) (RuntimeInstanceIdentity, error) {
	if request.SourceExtension == nil {
		return RuntimeInstanceIdentity{}, fmt.Errorf("%w: source artifact is required", ErrLifecycleBoundaryInvalid)
	}
	return lifecycleBoundaryIdentity("source", request.SourceBinding, *request.SourceExtension)
}

func lifecycleBoundaryIdentity(
	label string,
	binding extensions.LifecycleRuntimeBinding,
	extension extensions.Extension,
) (RuntimeInstanceIdentity, error) {
	if binding.ExtensionID != extension.ID || binding.ExtensionVersion != extension.Version ||
		binding.PackageDigest != extension.PackageDigest || binding.VersionID != extension.ActiveVersionID ||
		binding.RuntimeInstanceID == "" {
		return RuntimeInstanceIdentity{}, fmt.Errorf("%w: %s exact runtime binding changed", ErrLifecycleBoundaryInvalid, label)
	}
	return RuntimeInstanceIdentity{ExtensionID: binding.ExtensionID, InstanceID: binding.RuntimeInstanceID}, nil
}

func validateLifecycleBoundaryRuntimeSnapshot(
	label string,
	snapshot RuntimeInstanceSnapshot,
	extension extensions.Extension,
	identity RuntimeInstanceIdentity,
	active bool,
) error {
	if snapshot.Identity != identity || snapshot.ExtensionVersion != extension.Version ||
		snapshot.ArtifactDigest != extension.PackageDigest || snapshot.Active != active {
		return fmt.Errorf("%w: %s returned another exact runtime", ErrLifecycleBoundaryInvalid, label)
	}
	return nil
}

func validateLifecycleBoundaryAdmission(
	label string,
	snapshot RuntimeAdmissionSnapshot,
	identity RuntimeInstanceIdentity,
	draining bool,
	requireIdle bool,
) error {
	if snapshot.Identity != identity || snapshot.Draining != draining || (requireIdle && snapshot.ActiveTotal != 0) {
		return fmt.Errorf("%w: %s returned incompatible admission state", ErrLifecycleBoundaryInvalid, label)
	}
	return nil
}

func lifecycleBoundaryFailure(cause error, compensation []error) error {
	if len(compensation) == 0 {
		return cause
	}
	joined := errors.Join(compensation...)
	return errors.Join(cause, fmt.Errorf("%w: %v", ErrLifecycleBoundaryCompensationFailed, joined))
}

func lifecycleBoundaryCompensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, 5*time.Second)
}

func lifecycleBoundaryJobModeForOperation(operation extensions.LifecycleMachineOperation) (LifecycleBoundaryJobMode, error) {
	switch operation {
	case extensions.LifecycleMachineInstall:
		return LifecycleBoundaryJobsInstall, nil
	case extensions.LifecycleMachineEnable:
		return LifecycleBoundaryJobsEnable, nil
	case extensions.LifecycleMachineDisable:
		return LifecycleBoundaryJobsDisable, nil
	case extensions.LifecycleMachineUpgrade:
		return LifecycleBoundaryJobsUpgrade, nil
	case extensions.LifecycleMachineRollback:
		return LifecycleBoundaryJobsRollback, nil
	case extensions.LifecycleMachineUninstall:
		return LifecycleBoundaryJobsUninstall, nil
	default:
		return "", fmt.Errorf("%w: unknown job lifecycle operation %q", ErrLifecycleBoundaryInvalid, operation)
	}
}
