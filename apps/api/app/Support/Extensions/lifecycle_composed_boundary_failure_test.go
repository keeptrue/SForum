package extensionsruntime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var (
	errComposedPublish      = errors.New("publish failed")
	errComposedRestore      = errors.New("restore failed")
	errComposedRuntime      = errors.New("runtime switch failed")
	errComposedDelegate     = errors.New("delegate failed")
	errComposedStop         = errors.New("runtime stop failed")
	errComposedCompensation = errors.New("runtime compensation failed")
)

func TestComposedLifecycleBoundaryActivationCompensatesEveryPublicationFailure(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*composedBoundaryFixture)
		wantSuffix []string
	}{
		{
			name: "target runtime publication",
			configure: func(f *composedBoundaryFixture) {
				f.runtime.fail = map[string]error{"publish:target-instance": errComposedRuntime}
			},
			wantSuffix: precommitActivationCompensationCalls(),
		},
		{
			name: "target runtime snapshot mismatch",
			configure: func(f *composedBoundaryFixture) {
				f.runtime.wrong = map[string]bool{"publish:target-instance": true}
			},
			wantSuffix: precommitActivationCompensationCalls(),
		},
		{
			name: "durable state",
			configure: func(f *composedBoundaryFixture) {
				f.state.transaction = &composedBoundaryTransaction{fixture: f, name: "state", publishErr: errComposedPublish}
			},
			wantSuffix: precommitActivationCompensationCalls(),
		},
		{
			name: "jobs and schedules",
			configure: func(f *composedBoundaryFixture) {
				f.jobs.transaction = &composedBoundaryTransaction{fixture: f, name: "jobs", publishErr: errComposedPublish}
			},
			wantSuffix: precommitActivationCompensationCalls(),
		},
		{
			name: "registries",
			configure: func(f *composedBoundaryFixture) {
				f.registries.transaction = &composedBoundaryTransaction{fixture: f, name: "registries", publishErr: errComposedPublish}
			},
			wantSuffix: precommitActivationCompensationCalls(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
			test.configure(fixture)
			_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
			if err == nil {
				t.Fatal("expected publication failure")
			}
			if !slices.Equal(fixture.calls[len(fixture.calls)-len(test.wantSuffix):], test.wantSuffix) {
				t.Fatalf("calls = %#v, want suffix %#v", fixture.calls, test.wantSuffix)
			}
			if countCall(fixture.calls, "registries.publish") > 1 || countCall(fixture.calls, "state.publish") > 1 {
				t.Fatalf("publication retried unsafely: %#v", fixture.calls)
			}
		})
	}
}

func TestComposedLifecycleBoundaryRestoresSourceWhenProtocolSwitchedBeforeManagerCASFailure(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	// PublishRuntimeInstance may report a Manager CAS conflict after its
	// ProtocolStarter has already selected the target. Republish + Resume are
	// both mandatory even when Manager still reports source as active.
	fixture.runtime.fail = map[string]error{"publish:target-instance": ErrRuntimeInstanceConflict}
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, ErrRuntimeInstanceConflict) {
		t.Fatalf("error = %v", err)
	}
	want := precommitActivationCompensationCalls()
	if !slices.Equal(fixture.calls[len(fixture.calls)-len(want):], want) {
		t.Fatalf("calls = %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryActivationWithoutSourceStopsFailedCandidate(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineInstall, 8)
	fixture.runtime.fail = map[string]error{"publish:target-instance": errComposedRuntime}
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, errComposedRuntime) {
		t.Fatalf("error = %v", err)
	}
	want := []string{
		"registries.restore", "jobs.restore", "state.restore",
		"runtime.drain:target-instance", "runtime.wait:target-instance", "runtime.stop:target-instance",
	}
	if !slices.Equal(fixture.calls[len(fixture.calls)-len(want):], want) || countCallPrefix(fixture.calls, "runtime.publish:source") != 0 {
		t.Fatalf("calls = %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryCompensationWaitsForCallsPresentAtBeginDrain(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.runtime.fail = map[string]error{"publish:target-instance": errComposedRuntime}
	fixture.runtime.drainActive = 1
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, errComposedRuntime) {
		t.Fatalf("error = %v", err)
	}
	want := precommitActivationCompensationCalls()
	if !slices.Equal(fixture.calls[len(fixture.calls)-len(want):], want) {
		t.Fatalf("calls = %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryReportsAllCompensationFailures(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.registries.transaction = &composedBoundaryTransaction{
		fixture: fixture, name: "registries", publishErr: errComposedPublish, restoreErr: errComposedRestore,
	}
	fixture.jobs.transaction = &composedBoundaryTransaction{fixture: fixture, name: "jobs", restoreErr: errComposedRestore}
	fixture.state.transaction = &composedBoundaryTransaction{fixture: fixture, name: "state", restoreErr: errComposedRestore}
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, errComposedPublish) || !errors.Is(err, ErrLifecycleBoundaryCompensationFailed) {
		t.Fatalf("error = %v", err)
	}
	want := []string{
		"registries.restore", "jobs.restore", "state.restore",
		"runtime.drain:target-instance", "runtime.wait:target-instance",
	}
	if !slices.Equal(fixture.calls[len(fixture.calls)-len(want):], want) {
		t.Fatalf("calls = %#v", fixture.calls)
	}
	if countCallPrefix(fixture.calls, "runtime.publish:source") != 0 || countCallPrefix(fixture.calls, "runtime.resume:source") != 0 {
		t.Fatalf("unsafe source runtime fallback = %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryKeepsSourceDrainedWhenDeactivationRestoreFails(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineDisable, 3)
	fixture.registries.transaction = &composedBoundaryTransaction{
		fixture: fixture, name: "registries", publishErr: errComposedPublish, restoreErr: errComposedRestore,
	}
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, ErrLifecycleBoundaryCompensationFailed) || countCallPrefix(fixture.calls, "runtime.resume") != 0 {
		t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
	}
}

func TestComposedLifecycleBoundaryDeactivationCompensatesBeforeCommit(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*composedBoundaryFixture)
		wantSuffix []string
	}{
		{
			name:       "preflight",
			configure:  func(f *composedBoundaryFixture) { f.preflight.err = errComposedDelegate },
			wantSuffix: []string{"preflight", "journal.inspect-operation:deactivate", "jobs.resume:disable:source"},
		},
		{
			name:       "state prepare",
			configure:  func(f *composedBoundaryFixture) { f.state.prepareErr = errComposedDelegate },
			wantSuffix: []string{"state.prepare:deactivate"},
		},
		{
			name: "state publish",
			configure: func(f *composedBoundaryFixture) {
				f.state.transaction = &composedBoundaryTransaction{fixture: f, name: "state", publishErr: errComposedPublish}
			},
			wantSuffix: precommitDeactivationCompensationCalls(),
		},
		{
			name: "jobs publish",
			configure: func(f *composedBoundaryFixture) {
				f.jobs.transaction = &composedBoundaryTransaction{fixture: f, name: "jobs", publishErr: errComposedPublish}
			},
			wantSuffix: precommitDeactivationCompensationCalls(),
		},
		{
			name: "registry publish",
			configure: func(f *composedBoundaryFixture) {
				f.registries.transaction = &composedBoundaryTransaction{fixture: f, name: "registries", publishErr: errComposedPublish}
			},
			wantSuffix: precommitDeactivationCompensationCalls(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineDisable, 3)
			test.configure(fixture)
			_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
			if err == nil {
				t.Fatal("expected deactivation failure")
			}
			if !slices.Equal(fixture.calls[len(fixture.calls)-len(test.wantSuffix):], test.wantSuffix) {
				t.Fatalf("calls = %#v, want suffix %#v", fixture.calls, test.wantSuffix)
			}
		})
	}
}

func TestComposedLifecycleBoundaryDoesNotReopenAfterCommittedDeactivation(t *testing.T) {
	t.Run("cleanup failure", func(t *testing.T) {
		fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineDisable, 3)
		fixture.cleanup.err = errComposedDelegate
		_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
		if !errors.Is(err, errComposedDelegate) || countCallPrefix(fixture.calls, "runtime.resume") != 0 || countCallSuffix(fixture.calls, ".restore") != 0 {
			t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
		}
	})
	t.Run("stop failure", func(t *testing.T) {
		fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineDisable, 3)
		fixture.runtime.fail = map[string]error{"stop:target-instance": errComposedStop}
		_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
		if !errors.Is(err, errComposedStop) || countCallPrefix(fixture.calls, "runtime.resume") != 0 || countCallSuffix(fixture.calls, ".restore") != 0 {
			t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
		}
	})
}

func TestComposedLifecycleBoundaryDoesNotRollBackCommittedRollbackForRetirementFailure(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineRollback, 6)
	fixture.cleanup.err = errComposedDelegate
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, errComposedDelegate) || countCallSuffix(fixture.calls, ".restore") != 0 ||
		countCall(fixture.calls, "runtime.publish-drained:target-instance") != 1 || countCall(fixture.calls, "runtime.publish:source-instance") != 0 {
		t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
	}
}

func TestComposedLifecycleBoundaryRestoresDrainedSourceForPrepublicationFailures(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		configure func(*composedBoundaryFixture)
		wantProof string
	}{
		{
			"upgrade migration", extensions.LifecycleMachineUpgrade, 4,
			func(f *composedBoundaryFixture) { f.migrations.err = errComposedDelegate },
			"migrations.resume-proof:upgrade",
		},
		{
			"upgrade registry", extensions.LifecycleMachineUpgrade, 7,
			func(f *composedBoundaryFixture) { f.registries.validateErr = errComposedDelegate },
			"migrations.resume-proof:upgrade",
		},
		{
			"rollback migration", extensions.LifecycleMachineRollback, 5,
			func(f *composedBoundaryFixture) { f.migrations.err = errComposedDelegate },
			"migrations.resume-proof:rollback",
		},
		{
			"uninstall preflight", extensions.LifecycleMachineUninstall, 3,
			func(f *composedBoundaryFixture) { f.preflight.err = errComposedDelegate }, "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			test.configure(fixture)
			_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
			wantResume := "jobs.resume:" + string(lifecycleJobModeForTest(test.operation)) + ":source"
			if !errors.Is(err, errComposedDelegate) || fixture.calls[len(fixture.calls)-1] != wantResume {
				t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
			}
			if test.wantProof != "" && countCall(fixture.calls, test.wantProof) != 1 {
				t.Fatalf("source resume proof calls = %#v", fixture.calls)
			}
		})
	}
}

func TestComposedLifecycleBoundaryTargetDrainFailureKeepsBothArtifactsClosed(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.jobs.drainErrors = map[extensions.LifecycleCoordinatorRuntimeRole]error{
		extensions.LifecycleRuntimeTarget: errComposedDelegate,
	}

	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, errComposedDelegate) {
		t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
	}
	if countCall(fixture.calls, "runtime.drain:source-instance") != 1 ||
		countCall(fixture.calls, "runtime.drain:target-instance") < 1 ||
		countCallPrefix(fixture.calls, "runtime.resume") != 0 ||
		countCallPrefix(fixture.calls, "jobs.resume") != 0 {
		t.Fatalf("target drain failure reopened an artifact: %#v", fixture.calls)
	}
}

func lifecycleJobModeForTest(operation extensions.LifecycleMachineOperation) LifecycleBoundaryJobMode {
	mode, _ := lifecycleBoundaryJobModeForOperation(operation)
	return mode
}

func TestComposedLifecycleBoundaryCompensatesWithCancelledCallerContext(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.registries.transaction = &composedBoundaryTransaction{
		fixture: fixture, name: "registries", publishErr: context.Canceled,
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancellation occurs after entry in production; the injected publish error
	// represents that terminal point while compensation receives WithoutCancel.
	cancel()
	// Direct entry correctly rejects an already-cancelled context, so invoke the
	// publication helper to exercise its recovery context in isolation.
	request, err := newLifecycleBoundaryRequest(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.boundary.publishActivation(ctx, request, LifecycleBoundaryJobsUpgrade)
	if !errors.Is(err, context.Canceled) || countCall(fixture.calls, "registries.restore") != 1 ||
		countCall(fixture.calls, "runtime.publish-drained:source-instance") != 1 ||
		countCall(fixture.calls, "jobs.resume:upgrade:source") != 1 || countCallPrefix(fixture.calls, "runtime.resume") != 0 {
		t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
	}
}

func TestComposedLifecycleBoundaryTerminalUninstallIsIdempotentAfterRuntimeStop(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUninstall, 6)
	fixture.runtime.fail = map[string]error{"stop:target-instance": ErrRuntimeInstanceNotFound}
	result, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	assertComposedBoundaryResult(t, result, fixture.request, "removal_staged")
	if !slices.Equal(fixture.calls, []string{
		"runtime.drain:target-instance", "runtime.wait:target-instance", "runtime.stop:target-instance", "cleanup:uninstall_preserve",
	}) {
		t.Fatalf("calls = %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryRetirementAcceptsAlreadyStoppedExactRuntime(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		stopID    string
	}{
		{"disable", extensions.LifecycleMachineDisable, 3, "target-instance"},
		{"upgrade retire", extensions.LifecycleMachineUpgrade, 10, "source-instance"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			fixture.runtime.fail = map[string]error{"stop:" + test.stopID: ErrRuntimeInstanceNotFound}
			if _, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request); err != nil {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestComposedLifecycleBoundaryExplicitRetryRedrainsSourceAndConverges(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.registries.transaction = &composedBoundaryTransaction{
		fixture: fixture, name: "registries", publishErr: errComposedPublish,
	}
	if _, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request); !errors.Is(err, errComposedPublish) {
		t.Fatalf("first attempt = %v", err)
	}
	if countCall(fixture.calls, "runtime.publish-drained:source-instance") != 1 ||
		countCall(fixture.calls, "jobs.resume:upgrade:source") != 1 || countCallPrefix(fixture.calls, "runtime.resume") != 0 {
		t.Fatalf("first compensation = %#v", fixture.calls)
	}
	fixture.registries.transaction.publishErr = nil
	fixture.calls = nil
	if _, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request); err != nil {
		t.Fatalf("retry = %v, calls=%#v", err, fixture.calls)
	}
	wantPrefix := sourceDrainCalls("upgrade", "source-instance")
	if !slices.Equal(fixture.calls[:len(wantPrefix)], wantPrefix) || !fixture.journal.committed {
		t.Fatalf("retry did not redrain/commit = %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryCommittedReplayNeverRestoresSource(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.journal.committed = true
	fixture.state.transaction = &composedBoundaryTransaction{fixture: fixture, name: "state", state: LifecycleBoundaryTransactionTarget}
	fixture.jobs.transaction = &composedBoundaryTransaction{fixture: fixture, name: "jobs", state: LifecycleBoundaryTransactionTarget}
	fixture.registries.transaction = &composedBoundaryTransaction{fixture: fixture, name: "registries", state: LifecycleBoundaryTransactionTarget}
	fixture.runtime.fail = map[string]error{"publish:target-instance": errComposedRuntime}
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, errComposedRuntime) || countCallSuffix(fixture.calls, ".restore") != 0 ||
		countCall(fixture.calls, "runtime.publish:source-instance") != 0 || countCall(fixture.calls, "jobs.resume:upgrade:source") != 0 {
		t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
	}
	if countCall(fixture.calls, "jobs.drain:upgrade:target") < 2 || countCall(fixture.calls, "runtime.drain:target-instance") < 2 {
		t.Fatalf("committed target was not kept closed: %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryCommitWriteThenErrorUsesFreshMarker(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.journal.commitErr = context.Canceled
	fixture.journal.commitWrites = true
	fixture.journal.onCommit = cancel
	result, err := fixture.boundary.RunLifecycleHostBoundary(ctx, fixture.request)
	if err != nil {
		t.Fatalf("result = %#v, %v, calls=%#v", result, err, fixture.calls)
	}
	lastInspection := len(fixture.journal.inspectContexts) - 1
	if !fixture.journal.committed || lastInspection < 1 || fixture.journal.inspectContexts[lastInspection] == ctx ||
		fixture.journal.inspectErrors[lastInspection] != nil ||
		countCallSuffix(fixture.calls, ".restore") != 0 {
		t.Fatalf("fresh marker inspection = %#v, calls=%#v", fixture.journal.inspectContexts, fixture.calls)
	}
}

func TestComposedLifecycleBoundaryRejectsNoopPublicationBeforeMarker(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.registries.transaction = &composedBoundaryTransaction{
		fixture: fixture, name: "registries", publishKeepsState: true,
	}
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, ErrLifecycleBoundaryInvalid) || fixture.journal.committed || countCallSuffix(fixture.calls, ".restore") != 3 {
		t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
	}
}

func TestComposedLifecycleBoundaryRestoresResidualPreMarkerPublication(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.state.transaction = &composedBoundaryTransaction{fixture: fixture, name: "state", state: LifecycleBoundaryTransactionTarget}
	fixture.jobs.transaction = &composedBoundaryTransaction{fixture: fixture, name: "jobs", state: LifecycleBoundaryTransactionSource}
	fixture.registries.transaction = &composedBoundaryTransaction{fixture: fixture, name: "registries", state: LifecycleBoundaryTransactionTarget}
	if _, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request); err != nil {
		t.Fatalf("recovery = %v, calls=%#v", err, fixture.calls)
	}
	firstPublish := slices.Index(fixture.calls, "runtime.publish-drained:target-instance")
	lastRestore := slices.Index(fixture.calls, "state.restore")
	if lastRestore < 0 || firstPublish < 0 || lastRestore > firstPublish || countCall(fixture.calls, "runtime.publish-drained:source-instance") != 1 {
		t.Fatalf("residual publication ordering = %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryInstallRetryRetainsDrainedCandidate(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineInstall, 8)
	fixture.runtime.stopRemoves = true
	fixture.state.transaction = &composedBoundaryTransaction{fixture: fixture, name: "state", state: LifecycleBoundaryTransactionTarget}
	fixture.jobs.transaction = &composedBoundaryTransaction{fixture: fixture, name: "jobs", state: LifecycleBoundaryTransactionSource}
	fixture.registries.transaction = &composedBoundaryTransaction{fixture: fixture, name: "registries", state: LifecycleBoundaryTransactionTarget}

	if _, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request); err != nil {
		t.Fatalf("install retry = %v, calls=%#v", err, fixture.calls)
	}
	publish := slices.Index(fixture.calls, "runtime.publish-drained:target-instance")
	if publish < 0 || slices.Index(fixture.calls[:publish], "runtime.stop:target-instance") >= 0 ||
		countCall(fixture.calls, "jobs.resume:install:target") != 1 || countCallPrefix(fixture.calls, "runtime.resume") != 0 {
		t.Fatalf("candidate was not retained for republish: %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryCommittedDeactivationDrainFailureStaysClosed(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineDisable, 3)
	fixture.journal.committed = true
	fixture.jobs.drainErr = errComposedDelegate
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, errComposedDelegate) || countCallPrefix(fixture.calls, "runtime.resume") != 0 || countCallPrefix(fixture.calls, "jobs.resume") != 0 {
		t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
	}
	if countCall(fixture.calls, "runtime.drain:target-instance") == 0 {
		t.Fatalf("runtime was not kept closed: %#v", fixture.calls)
	}
}

func TestComposedLifecycleBoundaryCleanupRequiresDurableRecoveryProof(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		removal   string
		result    LifecycleBoundaryCleanupResult
	}{
		{
			name: "disable package", operation: extensions.LifecycleMachineDisable, position: 3,
			result: LifecycleBoundaryCleanupResult{IdentityRetained: true, RuntimeRecoveryRetained: true},
		},
		{
			name: "retired rollback bytes", operation: extensions.LifecycleMachineRollback, position: 6,
			result: LifecycleBoundaryCleanupResult{IdentityRetained: true, PackageRetained: true},
		},
		{
			name: "preserve marker", operation: extensions.LifecycleMachineUninstall, position: 6, removal: extensions.LifecycleRemovalPreserve,
			result: LifecycleBoundaryCleanupResult{DurableTombstone: true, TombstoneID: "tomb", IdentityRetained: true, PackageRetained: true, RuntimeRecoveryRetained: true},
		},
		{
			name: "export artifact", operation: extensions.LifecycleMachineUninstall, position: 6, removal: extensions.LifecycleRemovalExportThenRemove,
			result: LifecycleBoundaryCleanupResult{DurableTombstone: true, TombstoneID: "tomb", IdentityRetained: true, PackageRetained: true, RuntimeRecoveryRetained: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			if test.removal != "" {
				fixture.request.RemovalMode = test.removal
			}
			fixture.cleanup.result = test.result
			_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
			if !errors.Is(err, ErrLifecycleBoundaryInvalid) {
				t.Fatalf("error = %v, calls=%#v", err, fixture.calls)
			}
		})
	}
}

func runtimeSourceCompensationCalls() []string {
	return []string{
		"runtime.drain:target-instance", "runtime.wait:target-instance",
		"runtime.publish-drained:source-instance", "jobs.resume:upgrade:source",
	}
}

func TestComposedLifecycleBoundaryCommittedJobFailureRetriesOnlyForward(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
	}{
		{"activation", extensions.LifecycleMachineEnable, 5},
		{"deactivation", extensions.LifecycleMachineDisable, 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			fixture.jobs.reconcileErr = errComposedDelegate
			_, err := fixture.boundary.RunLifecycleHostBoundary(t.Context(), fixture.request)
			if !errors.Is(err, errComposedDelegate) {
				t.Fatalf("first error = %v", err)
			}
			if !fixture.journal.committed || fixture.jobs.reconcileCalls != 1 {
				t.Fatalf("marker/reconcile = %v/%d", fixture.journal.committed, fixture.jobs.reconcileCalls)
			}
			if countCallSuffix(fixture.calls, ".restore") != 0 || countCallPrefix(fixture.calls, "jobs.resume") != 0 {
				t.Fatalf("committed failure moved backward: %#v", fixture.calls)
			}
			if test.operation == extensions.LifecycleMachineEnable && countCall(fixture.calls, "runtime.resume:target-instance") != 0 {
				t.Fatalf("target opened before committed reconciliation: %#v", fixture.calls)
			}
			if test.operation == extensions.LifecycleMachineDisable && countCallPrefix(fixture.calls, "cleanup:") != 0 {
				t.Fatalf("deactivation cleaned up before reconciliation: %#v", fixture.calls)
			}

			fixture.jobs.reconcileErr = nil
			fixture.calls = nil
			result, err := fixture.boundary.RunLifecycleHostBoundary(t.Context(), fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			if fixture.jobs.reconcileCalls != 2 || countCallSuffix(fixture.calls, ".restore") != 0 {
				t.Fatalf("forward retry calls = %#v, reconcile=%d", fixture.calls, fixture.jobs.reconcileCalls)
			}
			wantStage := "published"
			if test.operation == extensions.LifecycleMachineDisable {
				wantStage = "disabled"
			}
			assertComposedBoundaryResult(t, result, fixture.request, wantStage)
		})
	}
}

func precommitActivationCompensationCalls() []string {
	return append([]string{
		"registries.restore", "jobs.restore", "state.restore", "migrations.resume-proof:upgrade",
	}, runtimeSourceCompensationCalls()...)
}

func precommitDeactivationCompensationCalls() []string {
	return []string{
		"registries.restore", "jobs.restore", "state.restore",
		"jobs.resume:disable:source",
	}
}

func countCall(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}

func countCallSuffix(calls []string, suffix string) int {
	count := 0
	for _, call := range calls {
		if strings.HasSuffix(call, suffix) {
			count++
		}
	}
	return count
}
