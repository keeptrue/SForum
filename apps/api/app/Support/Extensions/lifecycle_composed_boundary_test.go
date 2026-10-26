package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestComposedLifecycleBoundaryDispatchesEveryHostSideEffectPosition(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		wantStage string
		wantCalls []string
	}{
		{"install migrations", extensions.LifecycleMachineInstall, 2, "migrations", []string{"preflight", "migrations:install"}},
		{"install registry", extensions.LifecycleMachineInstall, 7, "registry_prepared", []string{"jobs.validate:install", "registries.validate"}},
		{"install publication", extensions.LifecycleMachineInstall, 8, "published", activationCalls("install")},
		{"enable registry", extensions.LifecycleMachineEnable, 4, "registry_prepared", []string{"preflight", "jobs.validate:enable", "registries.validate"}},
		{"enable publication", extensions.LifecycleMachineEnable, 5, "published", activationCalls("enable")},
		{"disable", extensions.LifecycleMachineDisable, 3, "disabled", append(deactivationCalls("disable"), "cleanup:disable", "runtime.drain:target-instance", "runtime.wait:target-instance", "runtime.stop:target-instance")},
		{"upgrade migrations", extensions.LifecycleMachineUpgrade, 4, "migrations", append(sourceDrainCalls("upgrade", "source-instance"), "preflight", "migrations:upgrade", "jobs.validate:upgrade")},
		{"upgrade registry", extensions.LifecycleMachineUpgrade, 7, "registry_prepared", append(sourceDrainCalls("upgrade", "source-instance"), "registries.validate")},
		{"upgrade publication", extensions.LifecycleMachineUpgrade, 8, "published", activationCalls("upgrade")},
		{"upgrade retire", extensions.LifecycleMachineUpgrade, 10, "source_retired", []string{"cleanup:retired_source", "runtime.drain:source-instance", "runtime.wait:source-instance", "runtime.stop:source-instance"}},
		{"rollback registry", extensions.LifecycleMachineRollback, 5, "registry_prepared", append(sourceDrainCalls("rollback", "source-instance"), "preflight", "migrations:rollback", "jobs.validate:rollback", "registries.validate")},
		{"rollback publication", extensions.LifecycleMachineRollback, 6, "published", append(activationCalls("rollback"), "cleanup:retired_source", "runtime.drain:source-instance", "runtime.wait:source-instance", "runtime.stop:source-instance")},
		{"uninstall unregister", extensions.LifecycleMachineUninstall, 3, "registrations_removed", deactivationCalls("uninstall")},
		{"uninstall removal", extensions.LifecycleMachineUninstall, 6, "removal_staged", []string{"runtime.drain:target-instance", "runtime.wait:target-instance", "runtime.stop:target-instance", "cleanup:uninstall_preserve"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			result, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(fixture.calls, test.wantCalls) {
				t.Fatalf("calls = %#v, want %#v", fixture.calls, test.wantCalls)
			}
			assertComposedBoundaryResult(t, result, fixture.request, test.wantStage)
		})
	}
}

func TestComposedLifecycleBoundaryUninstallRemovalModes(t *testing.T) {
	tests := []struct {
		removal string
		cleanup LifecycleBoundaryCleanupMode
	}{
		{extensions.LifecycleRemovalPreserve, LifecycleBoundaryCleanupPreserve},
		{extensions.LifecycleRemovalExportThenRemove, LifecycleBoundaryCleanupExport},
		{extensions.LifecycleRemovalComplete, LifecycleBoundaryCleanupComplete},
	}
	for _, test := range tests {
		t.Run(test.removal, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUninstall, 6)
			fixture.request.RemovalMode = test.removal
			result, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			want := "cleanup:" + string(test.cleanup)
			if !slices.Contains(fixture.calls, want) {
				t.Fatalf("calls = %#v, want %q", fixture.calls, want)
			}
			if strings.Contains(string(result.ResultDocument), "authority") || !strings.Contains(string(result.ResultDocument), `"removalMode":"`+test.removal+`"`) {
				t.Fatalf("result = %s", result.ResultDocument)
			}
		})
	}
}

func TestComposedLifecycleBoundaryActionResultsAreAllowlistedAndReadOnly(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 4)
	originalPlan := append(json.RawMessage(nil), fixture.request.ActionResults[extensions.LifecycleMachineUpgradePlan]...)
	originalBefore := append(json.RawMessage(nil), fixture.request.ActionResults[extensions.LifecycleMachineUpgradeBefore]...)

	fixture.preflight.inspect = func(request LifecycleBoundaryRequest) {
		plan, ok := request.ActionResult(extensions.LifecycleMachineUpgradePlan)
		if !ok || len(plan) == 0 {
			t.Fatal("upgrade plan missing")
		}
		plan[0] = '['
		again, _ := request.ActionResult(extensions.LifecycleMachineUpgradePlan)
		if string(again) != string(originalPlan) {
			t.Fatalf("second read = %s", again)
		}
		request.actionResults[extensions.LifecycleMachineDisableAction] = json.RawMessage(`{"forged":true}`)
	}
	fixture.migrations.inspect = func(request LifecycleBoundaryRequest) {
		if names := request.ActionNames(); !slices.Equal(names, []extensions.LifecycleMachineAction{
			extensions.LifecycleMachineUpgradeBefore, extensions.LifecycleMachineUpgradePlan,
		}) {
			t.Fatalf("action names = %#v", names)
		}
		plan, _ := request.ActionResult(extensions.LifecycleMachineUpgradePlan)
		before, _ := request.ActionResult(extensions.LifecycleMachineUpgradeBefore)
		if string(plan) != string(originalPlan) || string(before) != string(originalBefore) {
			t.Fatalf("delegate result view = %s / %s", plan, before)
		}
	}

	if _, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if string(fixture.request.ActionResults[extensions.LifecycleMachineUpgradePlan]) != string(originalPlan) ||
		string(fixture.request.ActionResults[extensions.LifecycleMachineUpgradeBefore]) != string(originalBefore) {
		t.Fatal("boundary mutated coordinator action results")
	}
}

func TestComposedLifecycleBoundaryRejectsNonAllowlistedActionResults(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*extensions.LifecycleCoordinatorGateRequest)
	}{
		{"missing", func(request *extensions.LifecycleCoordinatorGateRequest) {
			delete(request.ActionResults, extensions.LifecycleMachineInstallPlan)
		}},
		{"extra", func(request *extensions.LifecycleCoordinatorGateRequest) {
			request.ActionResults[extensions.LifecycleMachineDisableAction] = json.RawMessage(`{}`)
		}},
		{"malformed", func(request *extensions.LifecycleCoordinatorGateRequest) {
			request.ActionResults[extensions.LifecycleMachineInstallPlan] = json.RawMessage(`{`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineInstall, 2)
			test.mutate(&fixture.request)
			_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
			if !errors.Is(err, ErrLifecycleBoundaryInvalid) || len(fixture.calls) != 0 {
				t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
			}
		})
	}
}

func TestComposedLifecycleBoundaryFailsClosedForMissingDependencies(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		remove    func(*ComposedLifecycleHostBoundaryDependencies)
	}{
		{"preflight", extensions.LifecycleMachineInstall, 2, func(d *ComposedLifecycleHostBoundaryDependencies) { d.Preflight = nil }},
		{"migrations", extensions.LifecycleMachineInstall, 2, func(d *ComposedLifecycleHostBoundaryDependencies) { d.Migrations = nil }},
		{"runtime", extensions.LifecycleMachineInstall, 8, func(d *ComposedLifecycleHostBoundaryDependencies) { d.Runtime = nil }},
		{"state", extensions.LifecycleMachineInstall, 8, func(d *ComposedLifecycleHostBoundaryDependencies) { d.State = nil }},
		{"jobs", extensions.LifecycleMachineInstall, 8, func(d *ComposedLifecycleHostBoundaryDependencies) { d.Jobs = nil }},
		{"registries", extensions.LifecycleMachineInstall, 8, func(d *ComposedLifecycleHostBoundaryDependencies) { d.Registries = nil }},
		{"cleanup", extensions.LifecycleMachineDisable, 3, func(d *ComposedLifecycleHostBoundaryDependencies) { d.Cleanup = nil }},
		{"rollback cleanup", extensions.LifecycleMachineRollback, 6, func(d *ComposedLifecycleHostBoundaryDependencies) { d.Cleanup = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			dependencies := fixture.boundary.dependencies
			test.remove(&dependencies)
			boundary := NewComposedLifecycleHostBoundary(dependencies)
			_, err := boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
			if !errors.Is(err, ErrLifecycleBoundaryDependencyMissing) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestComposedLifecycleBoundaryRejectsNilTransactions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*composedBoundaryFixture)
	}{
		{"state", func(f *composedBoundaryFixture) { f.state.nilTransaction = true }},
		{"jobs", func(f *composedBoundaryFixture) { f.jobs.nilTransaction = true }},
		{"registries", func(f *composedBoundaryFixture) { f.registries.nilTransaction = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
			test.mutate(fixture)
			_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
			if !errors.Is(err, ErrLifecycleBoundaryDependencyMissing) || countCallPrefix(fixture.calls, "runtime.publish") != 0 {
				t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
			}
		})
	}
}

func TestComposedLifecycleBoundaryResultIsStrictlyAllowlisted(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 8)
	fixture.request.Checkpoint = "opaque-lease-token"
	fixture.request.PreviousResult = json.RawMessage(`{"secret":"previous"}`)
	result, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Checkpoint != "composed-v1:upgrade:08:"+fixture.request.TargetExtension.PackageDigest {
		t.Fatalf("checkpoint = %q", result.Checkpoint)
	}
	var document map[string]any
	if err := json.Unmarshal(result.ResultDocument, &document); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"operation", "position", "schema", "stage", "status"}
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, wantKeys) || strings.Contains(string(result.ResultDocument), "secret") ||
		strings.Contains(string(result.ResultDocument), "trustGrant") || strings.Contains(result.Checkpoint, "opaque") {
		t.Fatalf("result = %s, checkpoint = %q", result.ResultDocument, result.Checkpoint)
	}
}

func TestComposedLifecycleBoundaryHonorsCancelledContext(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineInstall, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fixture.boundary.RunLifecycleHostBoundary(ctx, fixture.request)
	if !errors.Is(err, context.Canceled) || len(fixture.calls) != 0 {
		t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
	}
}

func TestComposedLifecycleBoundaryRejectsProcessRevalidation(t *testing.T) {
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineInstall, 2)
	fixture.request.Revalidation = true
	_, err := fixture.boundary.RunLifecycleHostBoundary(context.Background(), fixture.request)
	if !errors.Is(err, ErrLifecycleBoundaryInvalid) || len(fixture.calls) != 0 {
		t.Fatalf("error = %v, calls = %#v", err, fixture.calls)
	}
}

func TestComposedLifecycleBoundaryUsesCanonicalPublicationMarkerForEarlyDrain(t *testing.T) {
	tests := []struct {
		operation           extensions.LifecycleMachineOperation
		drainPosition       int
		publicationPosition int
		mode                LifecycleBoundaryPublicationMode
	}{
		{extensions.LifecycleMachineDisable, 1, 3, LifecycleBoundaryDeactivate},
		{extensions.LifecycleMachineUpgrade, 2, 8, LifecycleBoundaryActivate},
		{extensions.LifecycleMachineRollback, 1, 6, LifecycleBoundaryActivate},
		{extensions.LifecycleMachineUninstall, 2, 3, LifecycleBoundaryDeactivate},
	}
	for _, test := range tests {
		t.Run(string(test.operation), func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.drainPosition)
			fixture.request.Attempt = 2
			fixture.journal.committed = true

			canResume, err := fixture.boundary.CanResumeLifecycleHostSources(context.Background(), fixture.request)
			if err != nil || canResume {
				t.Fatalf("CanResumeLifecycleHostSources() = %v, %v", canResume, err)
			}
			if len(fixture.journal.operationReads) != 1 {
				t.Fatalf("operation marker reads = %#v", fixture.journal.operationReads)
			}
			read := fixture.journal.operationReads[0]
			path, _ := extensions.RecommendedLifecyclePath(test.operation)
			wantStepID := fmt.Sprintf(
				"lifecycle.%s.%02d.host.%s", test.operation, test.publicationPosition, path[test.publicationPosition].State,
			)
			if read.Position != test.publicationPosition || read.StepID != wantStepID ||
				!slices.Equal(fixture.calls, []string{"journal.inspect-operation:" + string(test.mode)}) {
				t.Fatalf("canonical read = %#v, calls=%#v", read, fixture.calls)
			}
		})
	}
}

func TestComposedLifecycleBoundaryRequiresMigrationProofForUncommittedEarlyDrain(t *testing.T) {
	tests := []struct {
		operation extensions.LifecycleMachineOperation
		position  int
		mode      LifecycleBoundaryMigrationMode
	}{
		{extensions.LifecycleMachineUpgrade, 2, LifecycleBoundaryMigrationUpgrade},
		{extensions.LifecycleMachineRollback, 1, LifecycleBoundaryMigrationRollback},
	}
	for _, test := range tests {
		t.Run(string(test.operation), func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, test.operation, test.position)
			fixture.migrations.resumeDenied = true

			canResume, err := fixture.boundary.CanResumeLifecycleHostSources(context.Background(), fixture.request)
			if canResume || !errors.Is(err, ErrLifecycleBoundarySourceResumeUnsafe) {
				t.Fatalf("CanResumeLifecycleHostSources() = %v, %v", canResume, err)
			}
			want := []string{
				"journal.inspect-operation:activate",
				"migrations.resume-proof:" + string(test.mode),
			}
			if !slices.Equal(fixture.calls, want) {
				t.Fatalf("resume proof calls = %#v, want %#v", fixture.calls, want)
			}
		})
	}
}

type composedBoundaryFixture struct {
	request    extensions.LifecycleCoordinatorGateRequest
	boundary   *ComposedLifecycleHostBoundary
	calls      []string
	runtime    *composedBoundaryRuntime
	preflight  *composedBoundaryPreflight
	migrations *composedBoundaryMigrations
	jobs       *composedBoundaryJobs
	registries *composedBoundaryRegistries
	state      *composedBoundaryState
	journal    *composedBoundaryJournal
	cleanup    *composedBoundaryCleanup
}

func newComposedBoundaryFixture(t *testing.T, operation extensions.LifecycleMachineOperation, position int) *composedBoundaryFixture {
	t.Helper()
	request := lifecycleHostTestRequest(t, operation, position)
	request.ActionResults = make(map[extensions.LifecycleMachineAction]json.RawMessage)
	for _, action := range lifecycleBoundaryAllowedActions(operation, position) {
		request.ActionResults[action] = json.RawMessage(fmt.Sprintf(`{"action":%q}`, action))
	}
	fixture := &composedBoundaryFixture{request: request}
	fixture.runtime = &composedBoundaryRuntime{fixture: fixture}
	fixture.preflight = &composedBoundaryPreflight{fixture: fixture}
	fixture.migrations = &composedBoundaryMigrations{fixture: fixture}
	fixture.jobs = &composedBoundaryJobs{fixture: fixture}
	fixture.registries = &composedBoundaryRegistries{fixture: fixture}
	fixture.state = &composedBoundaryState{fixture: fixture}
	fixture.journal = &composedBoundaryJournal{fixture: fixture}
	fixture.cleanup = &composedBoundaryCleanup{fixture: fixture}
	fixture.boundary = NewComposedLifecycleHostBoundary(ComposedLifecycleHostBoundaryDependencies{
		Runtime: fixture.runtime, Preflight: fixture.preflight, Migrations: fixture.migrations,
		Jobs: fixture.jobs, Registries: fixture.registries, State: fixture.state, Journal: fixture.journal, Cleanup: fixture.cleanup,
	})
	return fixture
}

func (f *composedBoundaryFixture) record(call string) {
	f.calls = append(f.calls, call)
}

type composedBoundaryRuntime struct {
	fixture     *composedBoundaryFixture
	fail        map[string]error
	wrong       map[string]bool
	drainActive int
	stopRemoves bool
	removed     map[RuntimeInstanceIdentity]bool
}

func (r *composedBoundaryRuntime) PublishRuntimeInstance(_ context.Context, identity RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error) {
	r.fixture.record("runtime.publish:" + identity.InstanceID)
	return r.publishSnapshot(identity, false)
}

func (r *composedBoundaryRuntime) PublishDrainedRuntimeInstance(_ context.Context, identity RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error) {
	r.fixture.record("runtime.publish-drained:" + identity.InstanceID)
	return r.publishSnapshot(identity, true)
}

func (r *composedBoundaryRuntime) publishSnapshot(identity RuntimeInstanceIdentity, draining bool) (RuntimeInstanceSnapshot, error) {
	if r.removed[identity] {
		return RuntimeInstanceSnapshot{}, ErrRuntimeInstanceNotFound
	}
	if err := r.failure("publish:" + identity.InstanceID); err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	extension := r.fixture.request.TargetExtension
	if identity.InstanceID == r.fixture.request.SourceBinding.RuntimeInstanceID && r.fixture.request.SourceExtension != nil {
		extension = *r.fixture.request.SourceExtension
	}
	snapshot := RuntimeInstanceSnapshot{
		Identity: identity, ExtensionVersion: extension.Version, ArtifactDigest: extension.PackageDigest, Active: true,
		Admission: RuntimeAdmissionSnapshot{Identity: identity, Draining: draining},
	}
	if r.wrong["publish:"+identity.InstanceID] {
		snapshot.ArtifactDigest = strings.Repeat("f", 64)
	}
	return snapshot, nil
}

func (r *composedBoundaryRuntime) BeginDrain(identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	r.fixture.record("runtime.drain:" + identity.InstanceID)
	return RuntimeAdmissionSnapshot{Identity: identity, Draining: true, ActiveTotal: r.drainActive}, r.failure("drain:" + identity.InstanceID)
}

func (r *composedBoundaryRuntime) WaitDrain(_ context.Context, identity RuntimeInstanceIdentity) error {
	r.fixture.record("runtime.wait:" + identity.InstanceID)
	return r.failure("wait:" + identity.InstanceID)
}

func (r *composedBoundaryRuntime) ResumeRuntimeInstance(identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	r.fixture.record("runtime.resume:" + identity.InstanceID)
	return RuntimeAdmissionSnapshot{Identity: identity}, r.failure("resume:" + identity.InstanceID)
}

func (r *composedBoundaryRuntime) StopRuntimeInstance(_ context.Context, identity RuntimeInstanceIdentity) error {
	r.fixture.record("runtime.stop:" + identity.InstanceID)
	if r.stopRemoves {
		if r.removed == nil {
			r.removed = make(map[RuntimeInstanceIdentity]bool)
		}
		r.removed[identity] = true
	}
	return r.failure("stop:" + identity.InstanceID)
}

func (r *composedBoundaryRuntime) failure(key string) error {
	if r.fail == nil {
		return nil
	}
	return r.fail[key]
}

type composedBoundaryPreflight struct {
	fixture *composedBoundaryFixture
	err     error
	inspect func(LifecycleBoundaryRequest)
}

func (d *composedBoundaryPreflight) CheckLifecycleBoundary(_ context.Context, request LifecycleBoundaryRequest) error {
	d.fixture.record("preflight")
	if d.inspect != nil {
		d.inspect(request)
	}
	return d.err
}

type composedBoundaryMigrations struct {
	fixture      *composedBoundaryFixture
	err          error
	resumeErr    error
	resumeDenied bool
	inspect      func(LifecycleBoundaryRequest)
}

func (d *composedBoundaryMigrations) CanResumeLifecycleSource(
	_ context.Context,
	_ LifecycleBoundaryRequest,
	mode LifecycleBoundaryMigrationMode,
) (bool, error) {
	d.fixture.record("migrations.resume-proof:" + string(mode))
	return !d.resumeDenied, d.resumeErr
}

func (d *composedBoundaryMigrations) ReconcileLifecycleMigrations(_ context.Context, request LifecycleBoundaryRequest, mode LifecycleBoundaryMigrationMode) error {
	d.fixture.record("migrations:" + string(mode))
	if d.inspect != nil {
		d.inspect(request)
	}
	return d.err
}

type composedBoundaryJobs struct {
	fixture        *composedBoundaryFixture
	drainErr       error
	drainErrors    map[extensions.LifecycleCoordinatorRuntimeRole]error
	resumeErr      error
	validateErr    error
	prepareErr     error
	reconcileErr   error
	reconcileCalls int
	nilTransaction bool
	transaction    *composedBoundaryTransaction
}

func (d *composedBoundaryJobs) ReconcileCommittedLifecycleJobs(
	_ context.Context,
	_ LifecycleBoundaryRequest,
	_ LifecycleBoundaryJobMode,
	_ LifecycleBoundaryPublicationMode,
) error {
	d.reconcileCalls++
	return d.reconcileErr
}

func (d *composedBoundaryJobs) DrainLifecycleJobs(_ context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryJobMode, role extensions.LifecycleCoordinatorRuntimeRole) error {
	d.fixture.record("jobs.drain:" + string(mode) + ":" + string(role))
	if err := d.drainErrors[role]; err != nil {
		return err
	}
	return d.drainErr
}

func (d *composedBoundaryJobs) ResumeLifecycleJobs(_ context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryJobMode, role extensions.LifecycleCoordinatorRuntimeRole) error {
	d.fixture.record("jobs.resume:" + string(mode) + ":" + string(role))
	return d.resumeErr
}

func (d *composedBoundaryJobs) ValidateLifecycleJobs(_ context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryJobMode) error {
	d.fixture.record("jobs.validate:" + string(mode))
	return d.validateErr
}

func (d *composedBoundaryJobs) PrepareLifecycleJobPublication(_ context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryPublicationMode) (LifecycleBoundaryTransaction, error) {
	d.fixture.record("jobs.prepare:" + string(mode))
	if d.prepareErr != nil || d.nilTransaction {
		return nil, d.prepareErr
	}
	if d.transaction == nil {
		d.transaction = &composedBoundaryTransaction{fixture: d.fixture, name: "jobs"}
	}
	return d.transaction, nil
}

type composedBoundaryRegistries struct {
	fixture        *composedBoundaryFixture
	validateErr    error
	prepareErr     error
	nilTransaction bool
	transaction    *composedBoundaryTransaction
}

func (d *composedBoundaryRegistries) ValidateLifecycleRegistries(_ context.Context, _ LifecycleBoundaryRequest) error {
	d.fixture.record("registries.validate")
	return d.validateErr
}

func (d *composedBoundaryRegistries) PrepareLifecycleRegistryPublication(_ context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryPublicationMode) (LifecycleBoundaryTransaction, error) {
	d.fixture.record("registries.prepare:" + string(mode))
	if d.prepareErr != nil || d.nilTransaction {
		return nil, d.prepareErr
	}
	if d.transaction == nil {
		d.transaction = &composedBoundaryTransaction{fixture: d.fixture, name: "registries"}
	}
	return d.transaction, nil
}

type composedBoundaryState struct {
	fixture        *composedBoundaryFixture
	prepareErr     error
	nilTransaction bool
	transaction    *composedBoundaryTransaction
}

type composedBoundaryJournal struct {
	fixture         *composedBoundaryFixture
	prepareErr      error
	inspectErr      error
	commitErr       error
	commitWrites    bool
	committed       bool
	onCommit        func()
	inspectContexts []context.Context
	inspectErrors   []error
	operationReads  []LifecycleBoundaryRequest
}

func (j *composedBoundaryJournal) PrepareLifecyclePublication(_ context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryPublicationMode) error {
	j.fixture.record("journal.prepare:" + string(mode))
	return j.prepareErr
}

func (j *composedBoundaryJournal) LifecyclePublicationCommitted(ctx context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryPublicationMode) (bool, error) {
	j.fixture.record("journal.inspect:" + string(mode))
	j.inspectContexts = append(j.inspectContexts, ctx)
	j.inspectErrors = append(j.inspectErrors, ctx.Err())
	return j.committed, j.inspectErr
}

func (j *composedBoundaryJournal) LifecyclePublicationCommittedForOperation(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) (bool, error) {
	j.fixture.record("journal.inspect-operation:" + string(mode))
	j.inspectContexts = append(j.inspectContexts, ctx)
	j.inspectErrors = append(j.inspectErrors, ctx.Err())
	j.operationReads = append(j.operationReads, cloneLifecycleBoundaryRequest(request))
	return j.committed, j.inspectErr
}

func (j *composedBoundaryJournal) CommitLifecyclePublication(_ context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryPublicationMode) error {
	j.fixture.record("journal.commit:" + string(mode))
	if j.onCommit != nil {
		j.onCommit()
	}
	if j.commitErr == nil || j.commitWrites {
		j.committed = true
	}
	return j.commitErr
}

func (d *composedBoundaryState) PrepareLifecycleStatePublication(_ context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryPublicationMode) (LifecycleBoundaryTransaction, error) {
	d.fixture.record("state.prepare:" + string(mode))
	if d.prepareErr != nil || d.nilTransaction {
		return nil, d.prepareErr
	}
	if d.transaction == nil {
		d.transaction = &composedBoundaryTransaction{fixture: d.fixture, name: "state"}
	}
	return d.transaction, nil
}

type composedBoundaryCleanup struct {
	fixture *composedBoundaryFixture
	err     error
	result  LifecycleBoundaryCleanupResult
}

func (d *composedBoundaryCleanup) StageLifecycleHostCleanup(_ context.Context, _ LifecycleBoundaryRequest, mode LifecycleBoundaryCleanupMode) (LifecycleBoundaryCleanupResult, error) {
	d.fixture.record("cleanup:" + string(mode))
	result := d.result
	if result == (LifecycleBoundaryCleanupResult{}) {
		result = LifecycleBoundaryCleanupResult{
			DurableTombstone: true, TombstoneID: "tombstone-41",
			IdentityRetained: true, PackageRetained: true, RuntimeRecoveryRetained: true,
		}
		if mode == LifecycleBoundaryCleanupPreserve {
			result.RetentionMarker = "retained-41"
		}
		if mode == LifecycleBoundaryCleanupExport {
			result.ExportArtifactID = "export-41"
			result.ExportDigest = strings.Repeat("e", 64)
		}
	}
	return result, d.err
}

type composedBoundaryTransaction struct {
	fixture           *composedBoundaryFixture
	name              string
	publishErr        error
	publishKeepsState bool
	restoreErr        error
	inspectErr        error
	state             LifecycleBoundaryTransactionState
}

func (t *composedBoundaryTransaction) Inspect(context.Context) (LifecycleBoundaryTransactionState, error) {
	t.fixture.record(t.name + ".inspect")
	state := t.state
	if state == "" {
		state = LifecycleBoundaryTransactionSource
	}
	return state, t.inspectErr
}

func (t *composedBoundaryTransaction) Publish(context.Context) error {
	t.fixture.record(t.name + ".publish")
	if t.publishErr == nil && !t.publishKeepsState {
		t.state = LifecycleBoundaryTransactionTarget
	}
	return t.publishErr
}

func (t *composedBoundaryTransaction) Restore(context.Context) error {
	t.fixture.record(t.name + ".restore")
	if t.restoreErr == nil {
		t.state = LifecycleBoundaryTransactionSource
	}
	return t.restoreErr
}

func activationCalls(job string) []string {
	calls := make([]string, 0, 30)
	if job == "upgrade" || job == "rollback" {
		calls = append(calls, sourceDrainCalls(job, "source-instance")...)
	}
	calls = append(calls,
		"jobs.drain:"+job+":target", "runtime.drain:target-instance", "runtime.wait:target-instance",
		"journal.prepare:activate", "journal.inspect:activate",
		"jobs.validate:"+job, "registries.validate", "state.prepare:activate", "jobs.prepare:activate", "registries.prepare:activate",
		"registries.inspect", "jobs.inspect", "state.inspect",
		"runtime.publish-drained:target-instance", "state.publish", "jobs.publish", "registries.publish",
		"registries.inspect", "jobs.inspect", "state.inspect", "journal.commit:activate",
		"jobs.resume:"+job+":target",
	)
	return calls
}

func deactivationCalls(job string) []string {
	return []string{
		"jobs.drain:" + job + ":source", "runtime.drain:target-instance", "runtime.wait:target-instance", "preflight",
		"jobs.drain:" + job + ":source", "runtime.drain:target-instance", "runtime.wait:target-instance",
		"journal.prepare:deactivate", "journal.inspect:deactivate",
		"jobs.validate:" + job, "registries.validate", "state.prepare:deactivate", "jobs.prepare:deactivate", "registries.prepare:deactivate",
		"registries.inspect", "jobs.inspect", "state.inspect",
		"state.publish", "jobs.publish", "registries.publish",
		"registries.inspect", "jobs.inspect", "state.inspect", "journal.commit:deactivate",
	}
}

func sourceDrainCalls(job, instanceID string) []string {
	return []string{"jobs.drain:" + job + ":source", "runtime.drain:" + instanceID, "runtime.wait:" + instanceID}
}

func assertComposedBoundaryResult(t *testing.T, result LifecycleHostBoundaryResult, request extensions.LifecycleCoordinatorGateRequest, stage string) {
	t.Helper()
	if result.Checkpoint != fmt.Sprintf("composed-v1:%s:%02d:%s", request.Operation, request.Position, request.TargetExtension.PackageDigest) {
		t.Fatalf("checkpoint = %q", result.Checkpoint)
	}
	var document struct {
		Schema    string `json:"schema"`
		Operation string `json:"operation"`
		Position  int    `json:"position"`
		Stage     string `json:"stage"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(result.ResultDocument, &document); err != nil || document.Schema != lifecycleBoundaryResultSchema ||
		document.Operation != string(request.Operation) || document.Position != request.Position || document.Stage != stage || document.Status != "succeeded" {
		t.Fatalf("result = %s, %v", result.ResultDocument, err)
	}
}

func countCallPrefix(calls []string, prefix string) int {
	count := 0
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			count++
		}
	}
	return count
}

func (b *lifecycleHostBoundaryTestDouble) DrainLifecycleHostSources(_ context.Context, request extensions.LifecycleCoordinatorGateRequest) error {
	b.drainCalls = append(b.drainCalls, request)
	if b.drainInspect != nil {
		b.drainInspect(request)
	}
	return b.drainErr
}

func (b *lifecycleHostBoundaryTestDouble) ResumeLifecycleHostSources(_ context.Context, request extensions.LifecycleCoordinatorGateRequest) error {
	b.drainResumeCalls = append(b.drainResumeCalls, request)
	return b.drainResumeErr
}

func (b *lifecycleHostBoundaryTestDouble) CanResumeLifecycleHostSources(_ context.Context, _ extensions.LifecycleCoordinatorGateRequest) (bool, error) {
	return !b.resumeDenied, b.resumePolicyErr
}

func (r *lifecycleHostRuntimeTestDouble) ResumeRuntimeInstance(identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	r.calls = append(r.calls, "resume:"+identity.InstanceID)
	if r.resumeErr != nil {
		return RuntimeAdmissionSnapshot{}, r.resumeErr
	}
	if r.resumeResult != nil {
		return *r.resumeResult, nil
	}
	return RuntimeAdmissionSnapshot{Identity: identity}, nil
}

var (
	_ LifecycleBoundaryRuntime            = (*composedBoundaryRuntime)(nil)
	_ LifecycleBoundaryPreflight          = (*composedBoundaryPreflight)(nil)
	_ LifecycleBoundaryMigrations         = (*composedBoundaryMigrations)(nil)
	_ LifecycleBoundaryJobs               = (*composedBoundaryJobs)(nil)
	_ LifecycleBoundaryRegistries         = (*composedBoundaryRegistries)(nil)
	_ LifecycleBoundaryState              = (*composedBoundaryState)(nil)
	_ LifecycleBoundaryPublicationJournal = (*composedBoundaryJournal)(nil)
	_ LifecycleBoundaryCleanup            = (*composedBoundaryCleanup)(nil)
	_ LifecycleBoundaryTransaction        = (*composedBoundaryTransaction)(nil)
)
