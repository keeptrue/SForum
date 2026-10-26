package extensionsruntime

import (
	"context"
	"errors"
	"slices"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestComposedLifecycleBoundaryReconcilesCommittedJobsBeforeOpeningOrCleanup(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		jobMode   LifecycleBoundaryJobMode
		afterCall string
	}{
		{
			name: "activation opens target after reconciliation", operation: extensions.LifecycleMachineEnable,
			position: 5, jobMode: LifecycleBoundaryJobsEnable, afterCall: "jobs.resume:enable:target",
		},
		{
			name: "deactivation cleans Host state after reconciliation", operation: extensions.LifecycleMachineDisable,
			position: 3, jobMode: LifecycleBoundaryJobsDisable, afterCall: "cleanup:disable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			jobs := &orderedComposedBoundaryJobs{composedBoundaryJobs: fixture.jobs}
			fixture.boundary = NewComposedLifecycleHostBoundary(ComposedLifecycleHostBoundaryDependencies{
				Runtime: fixture.runtime, Preflight: fixture.preflight, Migrations: fixture.migrations,
				Jobs: jobs, Registries: fixture.registries, State: fixture.state,
				Journal: fixture.journal, Cleanup: fixture.cleanup,
			})

			if _, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request); err != nil {
				t.Fatal(err)
			}
			commit := slices.Index(fixture.calls, "journal.commit:"+string(lifecycleOrderingPublicationMode(test.operation)))
			reconcile := slices.Index(fixture.calls, "jobs.reconcile:"+string(test.jobMode))
			after := slices.Index(fixture.calls, test.afterCall)
			if commit < 0 || reconcile <= commit || after <= reconcile {
				t.Fatalf("unsafe committed-job ordering: %#v", fixture.calls)
			}
		})
	}
}

func TestComposedLifecycleBoundaryKeepsTargetClosedWhenCommittedJobReconciliationFails(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineEnable, 5)
	jobs := &orderedComposedBoundaryJobs{composedBoundaryJobs: fixture.jobs}
	jobs.reconcileErr = errComposedDelegate
	fixture.boundary = NewComposedLifecycleHostBoundary(ComposedLifecycleHostBoundaryDependencies{
		Runtime: fixture.runtime, Preflight: fixture.preflight, Migrations: fixture.migrations,
		Jobs: jobs, Registries: fixture.registries, State: fixture.state,
		Journal: fixture.journal, Cleanup: fixture.cleanup,
	})

	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, errComposedDelegate) || !fixture.journal.committed {
		t.Fatalf("error/marker = %v/%v", err, fixture.journal.committed)
	}
	if slices.Contains(fixture.calls, "jobs.resume:enable:target") ||
		slices.Contains(fixture.calls, "runtime.resume:target-instance") {
		t.Fatalf("committed reconciliation failure opened target: %#v", fixture.calls)
	}
}

type orderedComposedBoundaryJobs struct {
	*composedBoundaryJobs
}

func (d *orderedComposedBoundaryJobs) ReconcileCommittedLifecycleJobs(
	_ context.Context,
	_ LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	_ LifecycleBoundaryPublicationMode,
) error {
	d.fixture.record("jobs.reconcile:" + string(mode))
	d.reconcileCalls++
	return d.reconcileErr
}

func lifecycleOrderingPublicationMode(
	operation extensions.LifecycleMachineOperation,
) LifecycleBoundaryPublicationMode {
	if operation == extensions.LifecycleMachineDisable || operation == extensions.LifecycleMachineUninstall {
		return LifecycleBoundaryDeactivate
	}
	return LifecycleBoundaryActivate
}
