package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestLifecycleBoundaryJobsPlansAllSixOperationsWithoutMutatingRiver(t *testing.T) {
	tests := []struct {
		operation extensions.LifecycleMachineOperation
		position  int
		want      supportjobs.PluginJobAction
	}{
		{extensions.LifecycleMachineInstall, 7, supportjobs.PluginJobExecute},
		{extensions.LifecycleMachineEnable, 4, supportjobs.PluginJobExecute},
		{extensions.LifecycleMachineDisable, 3, supportjobs.PluginJobCancel},
		{extensions.LifecycleMachineUpgrade, 4, supportjobs.PluginJobDrain},
		{extensions.LifecycleMachineRollback, 5, supportjobs.PluginJobDrain},
		{extensions.LifecycleMachineUninstall, 3, supportjobs.PluginJobCancel},
	}
	for _, test := range tests {
		t.Run(string(test.operation), func(t *testing.T) {
			request := lifecyclePublicationTestRequest(t, test.operation, test.position)
			attachLifecycleJobManifest(&request.TargetExtension)
			if request.SourceExtension != nil {
				attachLifecycleJobManifest(request.SourceExtension)
			}
			queued := request.TargetExtension
			if test.operation == extensions.LifecycleMachineUpgrade || test.operation == extensions.LifecycleMachineRollback {
				queued = *request.SourceExtension
			}
			row := lifecycleBoundaryJobRow(t, queued, "grant:"+queued.Version, 101)
			store := &lifecycleBoundaryJobStore{rows: []hostapi.PluginJobLifecycleRow{row}}
			boundary := NewPostgresLifecycleBoundaryJobs(PostgresLifecycleBoundaryJobsConfig{
				Coordinator: &hostapi.PluginJobLifecycleCoordinator{Store: store},
				Trust:       lifecycleBoundaryJobTrust{},
			})
			mode, err := lifecycleBoundaryJobModeForOperation(test.operation)
			if err != nil {
				t.Fatal(err)
			}
			publicationMode, err := lifecycleJobPublicationMode(test.operation)
			if err != nil {
				t.Fatal(err)
			}
			material, err := boundary.lifecycleJobPublicationMaterial(t.Context(), request, mode, publicationMode)
			if err != nil {
				t.Fatal(err)
			}
			if len(material.Plan.Entries) != 1 || material.Plan.Entries[0].Action != test.want {
				t.Fatalf("plan = %#v, want action %q", material.Plan, test.want)
			}
			if store.cancelled != 0 || store.inserted != 0 || store.claimed != 0 {
				t.Fatalf("validation mutated River: %#v", store)
			}
		})
	}
}

func TestLifecycleBoundaryJobsDrainClosesEnqueueAndScheduleBeforeWaiting(t *testing.T) {
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineDisable, 3)
	attachLifecycleJobManifest(&request.TargetExtension)
	request.SourceExtension = &request.TargetExtension

	manager := NewManager(ManagerConfig{})
	if err := manager.Start(t.Context(), request.TargetExtension); err != nil {
		t.Fatal(err)
	}
	runtime, err := manager.ActiveRuntimeInstance(request.TargetExtension.ID)
	if err != nil {
		t.Fatal(err)
	}
	request.SourceBinding = lifecycleHostBindingForTest(request.TargetExtension, runtime.Identity.InstanceID)
	request.TargetBinding = request.SourceBinding

	schedules := supportjobs.NewPluginScheduleAdmissionRegistry()
	scheduleRuntime := supportjobs.PluginScheduleRuntime{
		Identity: supportjobs.PluginScheduleRuntimeIdentity{
			ExtensionID: request.TargetExtension.ID, ExtensionVersion: request.TargetExtension.Version,
			ArtifactDigest: request.TargetExtension.PackageDigest, InstanceID: runtime.Identity.InstanceID,
		},
		Schedules: []supportjobs.PluginScheduleDeclaration{{
			ScheduleID: "demo.schedule", JobName: "demo.sync",
			JobContract: "demo.job@1", Cron: "0 * * * *", Timezone: "UTC",
		}},
	}
	if _, err := schedules.PublishActive(scheduleRuntime); err != nil {
		t.Fatal(err)
	}
	jobLease, err := manager.AcquireRuntimeCall(t.Context(), runtime.Identity, RuntimeCallJob)
	if err != nil {
		t.Fatal(err)
	}
	_, triggerLease, err := schedules.AcquireTrigger(t.Context(), scheduleRuntime.Identity, "demo.schedule")
	if err != nil {
		t.Fatal(err)
	}

	boundary := NewPostgresLifecycleBoundaryJobs(PostgresLifecycleBoundaryJobsConfig{
		Runtime: manager, Schedules: schedules,
	})
	done := make(chan error, 1)
	go func() {
		done <- boundary.DrainLifecycleJobs(
			context.Background(), request, LifecycleBoundaryJobsDisable, extensions.LifecycleRuntimeSource,
		)
	}()

	eventuallyLifecycleJobs(t, func() bool {
		snapshot, inspectErr := manager.InspectRuntimeInstance(runtime.Identity)
		if inspectErr != nil || !snapshot.Admission.Draining {
			return false
		}
		schedule, inspectErr := schedules.Snapshot(scheduleRuntime.Identity)
		return inspectErr == nil && schedule.Draining
	})
	if _, err := manager.AcquireRuntimeCall(t.Context(), runtime.Identity, RuntimeCallJob); !errors.Is(err, ErrRuntimeAdmissionDraining) {
		t.Fatalf("direct enqueue admission error = %v", err)
	}
	if _, _, err := schedules.AcquireTrigger(t.Context(), scheduleRuntime.Identity, "demo.schedule"); !errors.Is(err, supportjobs.ErrPluginScheduleDraining) {
		t.Fatalf("schedule trigger admission error = %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("drain returned before inflight leases exited: %v", err)
	default:
	}

	triggerLease.Release()
	jobLease.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleBoundaryJobsPublishesSchedulesBeforeSingleRuntimeOpening(t *testing.T) {
	resumeFailure := errors.New("runtime opened then reported failure")
	for _, test := range []struct {
		name      string
		resumeErr error
	}{
		{name: "success"},
		{name: "resume failure redrains both admissions", resumeErr: resumeFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 4)
			attachLifecycleJobManifest(&request.TargetExtension)
			manager := NewManager(ManagerConfig{})
			if err := manager.Start(t.Context(), request.TargetExtension); err != nil {
				t.Fatal(err)
			}
			runtime, err := manager.ActiveRuntimeInstance(request.TargetExtension.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.BeginDrain(runtime.Identity); err != nil {
				t.Fatal(err)
			}
			if err := manager.WaitDrain(t.Context(), runtime.Identity); err != nil {
				t.Fatal(err)
			}

			schedules := supportjobs.NewPluginScheduleAdmissionRegistry()
			scheduleIdentity := supportjobs.PluginScheduleRuntimeIdentity{
				ExtensionID: request.TargetExtension.ID, ExtensionVersion: request.TargetExtension.Version,
				ArtifactDigest: request.TargetExtension.PackageDigest, InstanceID: runtime.Identity.InstanceID,
			}
			probe := &lifecycleJobOpeningRuntime{
				Manager: manager, schedules: schedules, scheduleIdentity: scheduleIdentity,
				scheduleID: "demo.schedule", resumeErr: test.resumeErr,
			}
			boundary := NewPostgresLifecycleBoundaryJobs(PostgresLifecycleBoundaryJobsConfig{
				Runtime: probe, Schedules: schedules,
			})
			desired := lifecycleJobDesiredSnapshot{
				Enabled: true,
				Artifact: lifecycleJobArtifactSnapshot{
					ExtensionID: request.TargetExtension.ID, ExtensionVersion: request.TargetExtension.Version,
					PackageDigest: request.TargetExtension.PackageDigest, VersionID: request.TargetExtension.ActiveVersionID,
					RuntimeInstanceID: runtime.Identity.InstanceID, Present: true,
				},
				Schedules: []supportjobs.PluginScheduleDeclaration{{
					ScheduleID: "demo.schedule", JobName: "demo.sync", JobContract: "demo.job@1",
					Cron: "0 * * * *", Timezone: "UTC",
				}},
			}
			err = boundary.resumeLifecycleJobAdmissions(
				t.Context(), request.TargetExtension, runtime.Identity, desired,
			)
			if test.resumeErr == nil && err != nil {
				t.Fatal(err)
			}
			if test.resumeErr != nil && !errors.Is(err, test.resumeErr) {
				t.Fatalf("resume error = %v", err)
			}
			if probe.resumeCalls != 1 || !probe.sawSchedulesPublished || !probe.sawRuntimeDrained ||
				!errors.Is(probe.transientEnqueueErr, ErrRuntimeAdmissionDraining) {
				t.Fatalf("opening probe = %#v", probe)
			}

			runtimeAfter, inspectErr := manager.InspectRuntimeInstance(runtime.Identity)
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			scheduleAfter, inspectErr := schedules.Snapshot(scheduleIdentity)
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			wantDrained := test.resumeErr != nil
			if runtimeAfter.Admission.Draining != wantDrained || scheduleAfter.Draining != wantDrained {
				t.Fatalf("final admission runtime=%#v schedule=%#v", runtimeAfter.Admission, scheduleAfter)
			}
		})
	}
}

func TestLifecycleJobEvidenceIsBijectiveWithDurablePlan(t *testing.T) {
	plan := lifecycleJobReconciliationPlan{
		IgnoredFinalized: 3,
		Entries: []lifecycleJobReconciliationPlanEntry{
			{JobID: 11, Action: supportjobs.PluginJobCancel, Reason: "disabled"},
			{JobID: 12, Action: supportjobs.PluginJobMigrate, Reason: "schema changed", MigrationID: "migrate-v2"},
		},
	}
	valid := lifecycleJobReconciliationEvidence{
		IgnoredFinalized: 3,
		Executions: []lifecycleJobReconciliationExecution{
			{JobID: 11, Action: string(supportjobs.PluginJobCancel), Reason: "disabled"},
			{JobID: 12, Action: string(supportjobs.PluginJobMigrate), Reason: "schema changed", MigrationID: "migrate-v2", ReplacementJobID: 22},
		},
	}
	if err := validateLifecycleJobEvidence(plan, valid); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*lifecycleJobReconciliationEvidence)
	}{
		{name: "missing", mutate: func(e *lifecycleJobReconciliationEvidence) { e.Executions = e.Executions[:1] }},
		{name: "duplicate", mutate: func(e *lifecycleJobReconciliationEvidence) { e.Executions[1] = e.Executions[0] }},
		{name: "ignored finalized drift", mutate: func(e *lifecycleJobReconciliationEvidence) { e.IgnoredFinalized++ }},
		{name: "missing replacement", mutate: func(e *lifecycleJobReconciliationEvidence) { e.Executions[1].ReplacementJobID = 0 }},
		{name: "illegal replacement", mutate: func(e *lifecycleJobReconciliationEvidence) { e.Executions[0].ReplacementJobID = 99 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := valid
			evidence.Executions = append([]lifecycleJobReconciliationExecution(nil), valid.Executions...)
			test.mutate(&evidence)
			if err := validateLifecycleJobEvidence(plan, evidence); !errors.Is(err, ErrLifecycleBoundaryJobsConflict) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	evidence := sanitizeLifecycleJobReconciliationResult(
		LifecycleBoundaryRequest{Operation: extensions.LifecycleMachineUpgrade},
		LifecycleBoundaryJobsUpgrade, LifecycleBoundaryActivate, 2, plan,
		hostapi.PluginJobLifecycleResult{Plan: hostapi.PluginJobLifecyclePlan{IgnoredFinalized: 99}},
	)
	if evidence.IgnoredFinalized != plan.IgnoredFinalized {
		t.Fatalf("ignored finalized = %d", evidence.IgnoredFinalized)
	}
}

func TestLifecycleJobDurablePlanAndEvidenceFailClosed(t *testing.T) {
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 5)
	plan := lifecycleJobReconciliationPlan{
		Schema: "sforum.lifecycle.job-reconciliation-plan@1", Operation: request.Operation,
		Mode: LifecycleBoundaryJobsEnable, PublicationMode: LifecycleBoundaryActivate,
		ExtensionID: request.TargetExtension.ID,
		Entries: []lifecycleJobReconciliationPlanEntry{{
			JobID: 42, Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonTargetRemoved,
		}},
	}
	if err := validateLifecycleJobReconciliationPlan(
		request, LifecycleBoundaryJobsEnable, LifecycleBoundaryActivate, plan,
	); err != nil {
		t.Fatal(err)
	}
	evidence := &lifecycleJobReconciliationEvidence{
		Schema: "sforum.lifecycle.job-reconciliation@1", Operation: request.Operation,
		Mode: LifecycleBoundaryJobsEnable, PublicationMode: LifecycleBoundaryActivate,
		ExtensionID: request.TargetExtension.ID, Attempt: 2,
		Executions: []lifecycleJobReconciliationExecution{{
			JobID: 42, Action: string(supportjobs.PluginJobCancel), Reason: supportjobs.PluginJobReasonTargetRemoved,
		}},
	}
	record := lifecycleJobPublicationRecord{Plan: plan, ReconcileAttempt: 2, Evidence: evidence}
	if err := validateLifecycleJobPersistedEvidence(
		request, LifecycleBoundaryJobsEnable, LifecycleBoundaryActivate, record,
	); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*lifecycleJobReconciliationPlan)
	}{
		{name: "schema drift", mutate: func(p *lifecycleJobReconciliationPlan) { p.Schema = "unknown" }},
		{name: "duplicate", mutate: func(p *lifecycleJobReconciliationPlan) { p.Entries = append(p.Entries, p.Entries[0]) }},
		{name: "unknown action", mutate: func(p *lifecycleJobReconciliationPlan) { p.Entries[0].Action = "replace" }},
		{name: "migration on cancel", mutate: func(p *lifecycleJobReconciliationPlan) { p.Entries[0].MigrationID = "illegal" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := plan
			candidate.Entries = append([]lifecycleJobReconciliationPlanEntry(nil), plan.Entries...)
			test.mutate(&candidate)
			if err := validateLifecycleJobReconciliationPlan(
				request, LifecycleBoundaryJobsEnable, LifecycleBoundaryActivate, candidate,
			); !errors.Is(err, ErrLifecycleBoundaryJobsConflict) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	brokenRecord := record
	brokenEvidence := *evidence
	brokenEvidence.Executions = nil
	brokenRecord.Evidence = &brokenEvidence
	if err := validateLifecycleJobPersistedEvidence(
		request, LifecycleBoundaryJobsEnable, LifecycleBoundaryActivate, brokenRecord,
	); !errors.Is(err, ErrLifecycleBoundaryJobsConflict) {
		t.Fatalf("persisted evidence error = %v", err)
	}
}

func TestLifecycleBoundaryJobsRejectsScheduleContractDrift(t *testing.T) {
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 4)
	attachLifecycleJobManifest(&request.TargetExtension)
	request.TargetExtension.Manifest.Schedules[0].JobID = "missing.job"
	store := &lifecycleBoundaryJobStore{}
	boundary := NewPostgresLifecycleBoundaryJobs(PostgresLifecycleBoundaryJobsConfig{
		Coordinator: &hostapi.PluginJobLifecycleCoordinator{Store: store},
		Trust:       lifecycleBoundaryJobTrust{},
	})
	err := boundary.ValidateLifecycleJobs(t.Context(), request, LifecycleBoundaryJobsEnable)
	if !errors.Is(err, ErrLifecycleBoundaryJobsInvalid) {
		t.Fatalf("error = %v", err)
	}
	if store.transactions != 0 {
		t.Fatal("invalid schedule reached River planning transaction")
	}
}

func TestLifecycleBoundaryJobsRejectsDuplicateDeclarationsBeforeRiver(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*extensions.Extension)
	}{
		{name: "job id", mutate: func(extension *extensions.Extension) {
			duplicate := extension.Manifest.Jobs[0]
			duplicate.Name = "demo.duplicate"
			extension.Manifest.Jobs = append(extension.Manifest.Jobs, duplicate)
		}},
		{name: "schedule id", mutate: func(extension *extensions.Extension) {
			extension.Manifest.Schedules = append(extension.Manifest.Schedules, extension.Manifest.Schedules[0])
		}},
		{name: "empty cron", mutate: func(extension *extensions.Extension) {
			extension.Manifest.Schedules[0].Cron = ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 5)
			attachLifecycleJobManifest(&request.TargetExtension)
			test.mutate(&request.TargetExtension)
			store := &lifecycleBoundaryJobStore{}
			boundary := NewPostgresLifecycleBoundaryJobs(PostgresLifecycleBoundaryJobsConfig{
				Coordinator: &hostapi.PluginJobLifecycleCoordinator{Store: store},
				Trust:       lifecycleBoundaryJobTrust{},
			})
			err := boundary.ValidateLifecycleJobs(t.Context(), request, LifecycleBoundaryJobsEnable)
			if !errors.Is(err, ErrLifecycleBoundaryJobsInvalid) || store.transactions != 0 {
				t.Fatalf("error = %v; transactions = %d", err, store.transactions)
			}
		})
	}
}

func TestLifecycleBoundaryJobsPlansDeclaredMigrationForOlderQueuedArtifact(t *testing.T) {
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineUpgrade, 4)
	attachLifecycleJobManifest(&request.TargetExtension)
	attachLifecycleJobManifest(request.SourceExtension)
	older := *request.SourceExtension
	older.Version = "0.9.0"
	older.Manifest.Version = older.Version
	older.PackageDigest = strings.Repeat("d", 64)
	older.ActiveVersionID = 9
	row := lifecycleBoundaryJobRow(t, older, "grant:"+older.Version, 102)
	targetContract, err := extensions.PluginJobContractForExtension(request.TargetExtension, "demo.sync")
	if err != nil {
		t.Fatal(err)
	}
	olderContract, err := extensions.PluginJobContractForExtension(older, "demo.sync")
	if err != nil {
		t.Fatal(err)
	}
	migration := supportjobs.PluginJobMigration{ID: "demo-v0-v2", From: olderContract, To: targetContract}
	store := &lifecycleBoundaryJobStore{rows: []hostapi.PluginJobLifecycleRow{row}}
	boundary := NewPostgresLifecycleBoundaryJobs(PostgresLifecycleBoundaryJobsConfig{
		Coordinator: &hostapi.PluginJobLifecycleCoordinator{Store: store},
		Trust:       lifecycleBoundaryJobTrust{},
		Migrations: lifecycleBoundaryMigrationSource{
			migration: migration,
			migrator: lifecycleBoundaryPayloadMigrator(func(input hostapi.PluginJobPayloadMigrationInput) (map[string]any, error) {
				return input.Payload, nil
			}),
		},
	})
	material, err := boundary.lifecycleJobPublicationMaterial(
		t.Context(), request, LifecycleBoundaryJobsUpgrade, LifecycleBoundaryActivate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(material.Plan.Entries) != 1 || material.Plan.Entries[0].Action != supportjobs.PluginJobMigrate ||
		material.Plan.Entries[0].MigrationID != migration.ID {
		t.Fatalf("migration plan = %#v", material.Plan)
	}
	if store.claimed != 0 || store.inserted != 0 || store.cancelled != 0 {
		t.Fatal("pure planning executed the declared migration")
	}
}

func attachLifecycleJobManifest(extension *extensions.Extension) {
	// job/schedule id 必须带 extension id 前缀，否则 v3 Validate 拒绝；
	// CommitLifecyclePublication 会经 exactPluginRuntimeTransitionArtifact 校验完整 manifest。
	// policy 字段留空，由 Normalize 填推荐默认，与 River 行 contract 的 zero→default 语义一致。
	jobID := extension.ID + ".job.demo"
	scheduleID := extension.ID + ".schedule.demo"
	extension.Manifest.Jobs = []extensions.ManifestJob{{
		ID: jobID, ContractVersion: "demo.job@1", Name: "demo.sync",
		Handler: "jobs.demo", PayloadSchema: "demo.payload@1", RetryPolicy: "bounded",
	}}
	extension.Manifest.Schedules = []extensions.ManifestSchedule{{
		ID: scheduleID, ContractVersion: "demo.schedule@1", JobID: jobID,
		Cron: "0 * * * *", Timezone: "UTC",
	}}
}

// lifecycleJobDemoScheduleID 与 attachLifecycleJobManifest 写入的 schedule id 一致。
func lifecycleJobDemoScheduleID(extension extensions.Extension) string {
	return extension.ID + ".schedule.demo"
}

func lifecycleBoundaryJobRow(
	t *testing.T,
	extension extensions.Extension,
	trustGrantID string,
	id int64,
) hostapi.PluginJobLifecycleRow {
	t.Helper()
	contract, err := extensions.PluginJobContractForExtension(extension, "demo.sync")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(hostapi.PluginJobArgs{
		EnvelopeVersion: supportjobs.PluginJobEnvelopeVersion,
		ExtensionID:     contract.ExtensionID, ExtensionVersion: contract.ExtensionVersion,
		ArtifactDigest: contract.ArtifactDigest, TrustGrantID: trustGrantID,
		JobName: contract.JobName, JobContractVersion: contract.JobContract,
		PayloadSchemaID: contract.PayloadSchemaID, PayloadSchemaVersion: contract.PayloadSchemaVersion,
		Payload: map[string]any{"cursor": float64(1)}, EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return hostapi.PluginJobLifecycleRow{
		JobID: id, Kind: hostapi.PluginJobKind, State: rivertype.JobStateAvailable,
		EncodedArgs: encoded, MaxAttempts: 5, Queue: "default", Priority: 1,
		ScheduledAt: time.Now().UTC(),
	}
}

type lifecycleBoundaryJobTrust struct{}

func (lifecycleBoundaryJobTrust) RuntimeIdentity(
	_ context.Context,
	extension extensions.Extension,
) (extensions.RuntimeTrustIdentity, error) {
	return extensions.RuntimeTrustIdentity{TrustGrantID: "grant:" + extension.Version, ImpactDigest: extension.PackageDigest}, nil
}

type lifecycleBoundaryMigrationSource struct {
	migration supportjobs.PluginJobMigration
	migrator  hostapi.PluginJobPayloadMigrator
}

func (s lifecycleBoundaryMigrationSource) LifecycleJobMigrations(
	context.Context,
	LifecycleBoundaryRequest,
	LifecycleBoundaryJobMode,
) ([]supportjobs.PluginJobMigration, map[string]hostapi.PluginJobPayloadMigrator, error) {
	return []supportjobs.PluginJobMigration{s.migration}, map[string]hostapi.PluginJobPayloadMigrator{
		s.migration.ID: s.migrator,
	}, nil
}

type lifecycleBoundaryPayloadMigrator func(hostapi.PluginJobPayloadMigrationInput) (map[string]any, error)

type lifecycleJobOpeningRuntime struct {
	*Manager
	schedules             *supportjobs.PluginScheduleAdmissionRegistry
	scheduleIdentity      supportjobs.PluginScheduleRuntimeIdentity
	scheduleID            string
	resumeErr             error
	resumeCalls           int
	sawSchedulesPublished bool
	sawRuntimeDrained     bool
	transientEnqueueErr   error
}

func (r *lifecycleJobOpeningRuntime) ResumeRuntimeInstance(
	identity RuntimeInstanceIdentity,
) (RuntimeAdmissionSnapshot, error) {
	r.resumeCalls++
	schedule, err := r.schedules.Snapshot(r.scheduleIdentity)
	r.sawSchedulesPublished = err == nil && schedule.Active && !schedule.Draining
	runtime, inspectErr := r.Manager.InspectRuntimeInstance(identity)
	r.sawRuntimeDrained = inspectErr == nil && runtime.Admission.Draining

	_, trigger, triggerErr := r.schedules.AcquireTrigger(context.Background(), r.scheduleIdentity, r.scheduleID)
	if triggerErr == nil {
		lease, enqueueErr := r.Manager.AcquireRuntimeCall(context.Background(), identity, RuntimeCallJob)
		r.transientEnqueueErr = enqueueErr
		if lease != nil {
			lease.Release()
		}
		trigger.Release()
	} else {
		r.transientEnqueueErr = triggerErr
	}

	resumed, resumeErr := r.Manager.ResumeRuntimeInstance(identity)
	if resumeErr != nil {
		return resumed, resumeErr
	}
	if r.resumeErr != nil {
		return resumed, r.resumeErr
	}
	return resumed, nil
}

func (f lifecycleBoundaryPayloadMigrator) MigratePluginJobPayload(
	_ context.Context,
	input hostapi.PluginJobPayloadMigrationInput,
) (map[string]any, error) {
	return f(input)
}

type lifecycleBoundaryJobStore struct {
	mu           sync.Mutex
	rows         []hostapi.PluginJobLifecycleRow
	transactions int
	cancelled    int
	inserted     int
	claimed      int
}

func (s *lifecycleBoundaryJobStore) WithPluginJobLifecycleTx(
	ctx context.Context,
	fn func(hostapi.PluginJobLifecycleTx) error,
) error {
	s.mu.Lock()
	s.transactions++
	s.mu.Unlock()
	return fn(&lifecycleBoundaryJobTx{store: s})
}

type lifecycleBoundaryJobTx struct {
	store *lifecycleBoundaryJobStore
}

func (t *lifecycleBoundaryJobTx) LockPluginJobs(context.Context, string) ([]hostapi.PluginJobLifecycleRow, error) {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	return append([]hostapi.PluginJobLifecycleRow(nil), t.store.rows...), nil
}

func (t *lifecycleBoundaryJobTx) ClaimPluginJobMigration(
	context.Context,
	hostapi.PluginJobMigrationLedgerEntry,
) (hostapi.PluginJobMigrationClaim, error) {
	t.store.mu.Lock()
	t.store.claimed++
	t.store.mu.Unlock()
	return hostapi.PluginJobMigrationClaim{}, errors.New("unexpected migration claim")
}

func (t *lifecycleBoundaryJobTx) InsertPluginJob(
	context.Context,
	hostapi.PluginJobArgs,
	*river.InsertOpts,
) (int64, error) {
	t.store.mu.Lock()
	t.store.inserted++
	t.store.mu.Unlock()
	return 0, errors.New("unexpected insert")
}

func (*lifecycleBoundaryJobTx) CompletePluginJobMigration(
	context.Context,
	hostapi.PluginJobMigrationLedgerEntry,
	int64,
) error {
	return errors.New("unexpected migration completion")
}

func (t *lifecycleBoundaryJobTx) CancelPluginJob(context.Context, int64) error {
	t.store.mu.Lock()
	t.store.cancelled++
	t.store.mu.Unlock()
	return nil
}

func eventuallyLifecycleJobs(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(fmt.Errorf("condition did not become true"))
}
