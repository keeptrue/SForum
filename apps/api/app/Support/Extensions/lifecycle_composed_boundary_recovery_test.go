package extensionsruntime

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestComposedLifecycleBoundaryPrepareFailuresKeepSourceClosedWithoutAggregateProof(t *testing.T) {
	failures := []struct {
		name      string
		configure func(*composedBoundaryFixture)
	}{
		{"journal prepare", func(f *composedBoundaryFixture) { f.journal.prepareErr = errComposedDelegate }},
		{"jobs validate", func(f *composedBoundaryFixture) { f.jobs.validateErr = errComposedDelegate }},
		{"registries validate", func(f *composedBoundaryFixture) { f.registries.validateErr = errComposedDelegate }},
		{"state prepare", func(f *composedBoundaryFixture) { f.state.prepareErr = errComposedDelegate }},
		{"jobs prepare", func(f *composedBoundaryFixture) { f.jobs.prepareErr = errComposedDelegate }},
		{"registries prepare", func(f *composedBoundaryFixture) { f.registries.prepareErr = errComposedDelegate }},
		{"nil state transaction", func(f *composedBoundaryFixture) { f.state.nilTransaction = true }},
	}
	operations := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
	}{
		{"activation", extensions.LifecycleMachineUpgrade, 8},
		{"deactivation", extensions.LifecycleMachineDisable, 3},
	}

	for _, operation := range operations {
		for _, failure := range failures {
			t.Run(operation.name+"/"+failure.name, func(t *testing.T) {
				fixture := newComposedBoundaryFixture(t, operation.operation, operation.position)
				// A prior crashed attempt may have published one aggregate family even
				// though the shared marker is still false.
				fixture.registries.transaction = &composedBoundaryTransaction{
					fixture: fixture, name: "registries", state: LifecycleBoundaryTransactionTarget,
				}
				failure.configure(fixture)

				if _, err := fixture.boundary.RunLifecycleHostBoundary(t.Context(), fixture.request); err == nil {
					t.Fatal("expected preparation failure")
				}
				if countCallPrefix(fixture.calls, "journal.inspect-operation:") != 0 ||
					countCallPrefix(fixture.calls, "jobs.resume:") != 0 ||
					countCallPrefix(fixture.calls, "runtime.resume:") != 0 ||
					countCallPrefix(fixture.calls, "migrations.resume-proof:") != 0 ||
					countCallSuffix(fixture.calls, ".publish") != 0 {
					t.Fatalf("unsafe preparation recovery: %#v", fixture.calls)
				}
			})
		}
	}
}

func TestComposedLifecycleBoundaryUncertainPreparationNeverReopensSource(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*composedBoundaryFixture)
	}{
		{
			"marker inspection unknown",
			func(f *composedBoundaryFixture) { f.journal.inspectErr = errComposedDelegate },
		},
		{
			"transaction inspection unknown",
			func(f *composedBoundaryFixture) {
				f.registries.transaction = &composedBoundaryTransaction{
					fixture: f, name: "registries", inspectErr: errComposedDelegate,
				}
			},
		},
		{
			"transaction restore unknown",
			func(f *composedBoundaryFixture) {
				f.registries.transaction = &composedBoundaryTransaction{
					fixture: f, name: "registries", state: LifecycleBoundaryTransactionTarget, restoreErr: errComposedDelegate,
				}
			},
		},
		{
			"committed marker",
			func(f *composedBoundaryFixture) {
				f.journal.committed = true
				f.state.prepareErr = errComposedDelegate
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
			test.configure(fixture)
			if _, err := fixture.boundary.RunLifecycleHostBoundary(t.Context(), fixture.request); err == nil {
				t.Fatal("expected preparation failure")
			}
			if countCallPrefix(fixture.calls, "jobs.resume:") != 0 ||
				countCallPrefix(fixture.calls, "runtime.resume:") != 0 ||
				countCallPrefix(fixture.calls, "migrations.resume-proof:") != 0 {
				t.Fatalf("uncertain preparation reopened source: %#v", fixture.calls)
			}
		})
	}
}

func TestComposedLifecycleBoundaryMigrationProofDenialKeepsArtifactsClosed(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		proof     string
		configure func(*composedBoundaryFixture)
	}{
		{
			"upgrade migration partial", extensions.LifecycleMachineUpgrade, 4, "migrations.resume-proof:upgrade",
			func(f *composedBoundaryFixture) { f.migrations.err = errComposedDelegate },
		},
		{
			"upgrade later validation", extensions.LifecycleMachineUpgrade, 7, "migrations.resume-proof:upgrade",
			func(f *composedBoundaryFixture) { f.registries.validateErr = errComposedDelegate },
		},
		{
			"rollback migration partial", extensions.LifecycleMachineRollback, 5, "migrations.resume-proof:rollback",
			func(f *composedBoundaryFixture) { f.migrations.err = errComposedDelegate },
		},
		{
			"publication rollback", extensions.LifecycleMachineUpgrade, 8, "migrations.resume-proof:upgrade",
			func(f *composedBoundaryFixture) {
				f.registries.transaction = &composedBoundaryTransaction{
					fixture: f, name: "registries", publishErr: errComposedDelegate,
				}
			},
		},
		{
			"target drain unknown", extensions.LifecycleMachineUpgrade, 8, "",
			func(f *composedBoundaryFixture) {
				f.jobs.drainErrors = map[extensions.LifecycleCoordinatorRuntimeRole]error{
					extensions.LifecycleRuntimeTarget: errComposedDelegate,
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			fixture.migrations.resumeDenied = true
			test.configure(fixture)

			_, err := fixture.boundary.RunLifecycleHostBoundary(t.Context(), fixture.request)
			if !errors.Is(err, errComposedDelegate) {
				t.Fatalf("error = %v, calls=%#v", err, fixture.calls)
			}
			if countCallPrefix(fixture.calls, "jobs.resume:") != 0 ||
				countCallPrefix(fixture.calls, "runtime.resume:") != 0 {
				t.Fatalf("migration denial reopened an artifact: %#v", fixture.calls)
			}
			if test.proof == "" {
				if countCallPrefix(fixture.calls, "migrations.resume-proof:") != 0 {
					t.Fatalf("target drain uncertainty consulted rollback proof: %#v", fixture.calls)
				}
			} else if countCall(fixture.calls, test.proof) != 1 ||
				!strings.Contains(err.Error(), ErrLifecycleBoundarySourceResumeUnsafe.Error()) {
				t.Fatalf("missing migration denial evidence: error=%v calls=%#v", err, fixture.calls)
			}
		})
	}
}

func TestComposedLifecycleBoundaryCompensationToleratesRetiredSourceRuntime(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.jobs.resumeErr = fmt.Errorf("%w: fixture.source", ErrRuntimeInstanceNotActive)
	cause := errors.New("registries validate failed")

	request, err := newLifecycleBoundaryRequest(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.boundary.failBeforePublication(t.Context(), request, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("compensation must keep the real cause: %v", err)
	}
	if strings.Contains(err.Error(), "jobs and schedules") {
		t.Fatalf("a retired source runtime counts as converged: %v", err)
	}
	if countCallPrefix(fixture.calls, "jobs.resume:") == 0 {
		t.Fatal("expected a source resume attempt")
	}
}

func TestComposedLifecycleBoundaryCompensationKeepsRealResumeFailures(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.jobs.resumeErr = errComposedDelegate
	cause := errors.New("registries validate failed")

	request, err := newLifecycleBoundaryRequest(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.boundary.failBeforePublication(t.Context(), request, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("compensation must keep the real cause: %v", err)
	}
	if !errors.Is(err, ErrLifecycleBoundaryCompensationFailed) ||
		!strings.Contains(err.Error(), "jobs and schedules: delegate failed") {
		t.Fatalf("real resume failure must surface as a compensation failure: %v", err)
	}
}
