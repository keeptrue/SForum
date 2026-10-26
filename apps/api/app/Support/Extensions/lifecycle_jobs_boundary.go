package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

var (
	ErrLifecycleBoundaryJobsUnavailable = errors.New("extension lifecycle jobs boundary is unavailable")
	ErrLifecycleBoundaryJobsInvalid     = errors.New("extension lifecycle jobs boundary input is invalid")
	ErrLifecycleBoundaryJobsConflict    = errors.New("extension lifecycle jobs publication conflict")
	ErrLifecycleBoundaryJobsUncommitted = errors.New("extension lifecycle jobs publication is not committed")
	ErrLifecycleBoundaryJobsCommitted   = errors.New("extension lifecycle jobs publication is already committed")
)

// LifecycleBoundaryJobRuntime is the Manager surface shared by direct enqueue
// admission and lifecycle drain. There is deliberately no second job gate.
type LifecycleBoundaryJobRuntime interface {
	InspectRuntimeInstance(RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error)
	BeginDrain(RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error)
	WaitDrain(context.Context, RuntimeInstanceIdentity) error
	ResumeRuntimeInstance(RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error)
}

// LifecycleBoundaryJobMigrationSource resolves only the already accepted
// payload migration contract. River row access and replacement remain owned by
// HostAPI's PluginJobLifecycleCoordinator.
type LifecycleBoundaryJobMigrationSource interface {
	LifecycleJobMigrations(
		context.Context,
		LifecycleBoundaryRequest,
		LifecycleBoundaryJobMode,
	) ([]supportjobs.PluginJobMigration, map[string]hostapi.PluginJobPayloadMigrator, error)
}

// LifecycleBoundaryCommittedJobs is intentionally separate from the prepared
// transaction. River cancel/migrate is irreversible and may run only after the
// shared publication marker has durably selected the target.
type LifecycleBoundaryCommittedJobs interface {
	ReconcileCommittedLifecycleJobs(
		context.Context,
		LifecycleBoundaryRequest,
		LifecycleBoundaryJobMode,
		LifecycleBoundaryPublicationMode,
	) error
}

type PostgresLifecycleBoundaryJobsConfig struct {
	Pool        *pgxpool.Pool
	Runtime     LifecycleBoundaryJobRuntime
	Schedules   *supportjobs.PluginScheduleAdmissionRegistry
	Coordinator *hostapi.PluginJobLifecycleCoordinator
	Trust       RuntimeTrustSource
	Migrations  LifecycleBoundaryJobMigrationSource
	Journal     LifecycleBoundaryPublicationJournal
}

// PostgresLifecycleBoundaryJobs composes River's existing lifecycle planner,
// Manager exact-runtime admission, and the schedule trigger admission registry.
// PostgreSQL stores only desired snapshots and sanitized reconciliation proof.
type PostgresLifecycleBoundaryJobs struct {
	pool        *pgxpool.Pool
	runtime     LifecycleBoundaryJobRuntime
	schedules   *supportjobs.PluginScheduleAdmissionRegistry
	coordinator *hostapi.PluginJobLifecycleCoordinator
	trust       RuntimeTrustSource
	migrations  LifecycleBoundaryJobMigrationSource
	journal     LifecycleBoundaryPublicationJournal
}

func NewPostgresLifecycleBoundaryJobs(config PostgresLifecycleBoundaryJobsConfig) *PostgresLifecycleBoundaryJobs {
	return &PostgresLifecycleBoundaryJobs{
		pool: config.Pool, runtime: config.Runtime, schedules: config.Schedules,
		coordinator: config.Coordinator, trust: config.Trust,
		migrations: config.Migrations, journal: config.Journal,
	}
}

type lifecycleJobArtifactSnapshot struct {
	ExtensionID       string `json:"extensionId,omitempty"`
	ExtensionVersion  string `json:"extensionVersion,omitempty"`
	PackageDigest     string `json:"packageDigest,omitempty"`
	VersionID         int64  `json:"versionId,omitempty"`
	RuntimeInstanceID string `json:"runtimeInstanceId,omitempty"`
	Present           bool   `json:"present"`
}

type lifecycleJobDesiredSnapshot struct {
	Enabled      bool                                    `json:"enabled"`
	Artifact     lifecycleJobArtifactSnapshot            `json:"artifact"`
	TrustGrantID string                                  `json:"trustGrantId,omitempty"`
	Jobs         []hostapi.PluginJobRuntimeContract      `json:"jobs"`
	Schedules    []supportjobs.PluginScheduleDeclaration `json:"schedules"`
}

type lifecycleJobReconciliationPlan struct {
	Schema           string                                `json:"schema"`
	Operation        extensions.LifecycleMachineOperation  `json:"operation"`
	Mode             LifecycleBoundaryJobMode              `json:"mode"`
	PublicationMode  LifecycleBoundaryPublicationMode      `json:"publicationMode"`
	ExtensionID      string                                `json:"extensionId"`
	IgnoredFinalized int                                   `json:"ignoredFinalized"`
	Entries          []lifecycleJobReconciliationPlanEntry `json:"entries"`
}

type lifecycleJobReconciliationPlanEntry struct {
	JobID       int64                       `json:"jobId"`
	Action      supportjobs.PluginJobAction `json:"action"`
	Reason      string                      `json:"reason"`
	MigrationID string                      `json:"migrationId,omitempty"`
}

type lifecycleJobPublicationMaterial struct {
	Source lifecycleJobDesiredSnapshot
	Target lifecycleJobDesiredSnapshot
	Input  hostapi.PluginJobLifecycleInput
	Plan   lifecycleJobReconciliationPlan
}

func (b *PostgresLifecycleBoundaryJobs) DrainLifecycleJobs(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	role extensions.LifecycleCoordinatorRuntimeRole,
) error {
	if err := b.requireOperationalDependencies(ctx); err != nil {
		return err
	}
	extension, binding, err := lifecycleJobRole(request, mode, role)
	if err != nil {
		return err
	}
	identity, err := lifecycleJobRuntimeIdentity(extension, binding)
	if err != nil {
		return err
	}

	// Close both admission families before waiting on either one. A slow
	// schedule trigger must not leave direct enqueue open, and a slow River
	// insert must not leave periodic triggers open.
	scheduleIdentity, waitSchedule, scheduleErr := b.beginDrainLifecycleSchedules(extension, identity)
	runtimeErr := b.beginDrainLifecycleJobRuntime(extension, identity)
	if scheduleErr != nil || runtimeErr != nil {
		return errors.Join(scheduleErr, runtimeErr)
	}
	if waitSchedule {
		scheduleErr = b.schedules.WaitDrain(ctx, scheduleIdentity)
	}
	runtimeErr = b.runtime.WaitDrain(ctx, identity)
	return errors.Join(scheduleErr, runtimeErr)
}

func (b *PostgresLifecycleBoundaryJobs) ResumeLifecycleJobs(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	role extensions.LifecycleCoordinatorRuntimeRole,
) error {
	if err := b.requireOperationalDependencies(ctx); err != nil {
		return err
	}
	extension, binding, err := lifecycleJobRole(request, mode, role)
	if err != nil {
		return err
	}
	identity, err := lifecycleJobRuntimeIdentity(extension, binding)
	if err != nil {
		return err
	}
	publicationMode, err := lifecycleJobPublicationMode(request.Operation)
	if err != nil {
		return err
	}
	if err := b.authorizeLifecycleJobResume(ctx, request, publicationMode, role); err != nil {
		return err
	}
	snapshot, err := b.buildLifecycleJobSnapshot(ctx, extension, binding, true, role == extensions.LifecycleRuntimeTarget)
	if err != nil {
		return err
	}
	return b.resumeLifecycleJobAdmissions(ctx, extension, identity, snapshot)
}

// resumeLifecycleJobAdmissions publishes the exact schedule set while the
// shared RuntimeCallJob gate is still drained. Resuming that runtime is the
// only final opening action, so a transiently visible schedule cannot enqueue
// against the old or half-published artifact.
func (b *PostgresLifecycleBoundaryJobs) resumeLifecycleJobAdmissions(
	ctx context.Context,
	extension extensions.Extension,
	identity RuntimeInstanceIdentity,
	snapshot lifecycleJobDesiredSnapshot,
) error {
	runtimeSnapshot, err := b.inspectExactLifecycleJobRuntime(extension, identity)
	if err != nil {
		return err
	}
	if err := validateLifecycleBoundaryAdmission(
		"publish lifecycle schedules", runtimeSnapshot.Admission, identity, true, true,
	); err != nil {
		return err
	}

	scheduleIdentity := lifecycleScheduleIdentity(snapshot.Artifact)
	schedulePublished := false
	if len(snapshot.Schedules) > 0 {
		published, publishErr := b.schedules.PublishActive(supportjobs.PluginScheduleRuntime{
			Identity:  scheduleIdentity,
			Schedules: append([]supportjobs.PluginScheduleDeclaration(nil), snapshot.Schedules...),
		})
		schedulePublished = publishErr == nil
		if publishErr == nil && (published.Identity != scheduleIdentity || !published.Active || published.Draining) {
			publishErr = fmt.Errorf("%w: schedule publication returned another or closed runtime", ErrLifecycleBoundaryJobsConflict)
		}
		if publishErr != nil {
			return errors.Join(publishErr, b.redrainLifecycleJobAdmissions(ctx, identity, scheduleIdentity, schedulePublished))
		}
	}

	resumed, err := b.runtime.ResumeRuntimeInstance(identity)
	if err == nil {
		err = validateLifecycleBoundaryAdmission("resume lifecycle jobs", resumed, identity, false, true)
	}
	if err != nil {
		return errors.Join(err, b.redrainLifecycleJobAdmissions(ctx, identity, scheduleIdentity, schedulePublished))
	}
	return nil
}

func (b *PostgresLifecycleBoundaryJobs) redrainLifecycleJobAdmissions(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	scheduleIdentity supportjobs.PluginScheduleRuntimeIdentity,
	schedulePublished bool,
) error {
	compensationCtx, cancel := lifecycleBoundaryCompensationContext(ctx)
	defer cancel()

	var scheduleCloseErr error
	if schedulePublished {
		schedule, err := b.schedules.BeginDrain(scheduleIdentity)
		if err == nil && (schedule.Identity != scheduleIdentity || !schedule.Draining) {
			err = ErrLifecycleBoundaryJobsConflict
		}
		scheduleCloseErr = err
	}
	runtime, runtimeCloseErr := b.runtime.BeginDrain(identity)
	if runtimeCloseErr == nil {
		runtimeCloseErr = validateLifecycleBoundaryAdmission("redrain lifecycle jobs", runtime, identity, true, false)
	}
	// Both gates are closed before waiting on either inflight family.
	if scheduleCloseErr == nil && schedulePublished {
		scheduleCloseErr = b.schedules.WaitDrain(compensationCtx, scheduleIdentity)
	}
	if runtimeCloseErr == nil {
		runtimeCloseErr = b.runtime.WaitDrain(compensationCtx, identity)
	}
	return errors.Join(scheduleCloseErr, runtimeCloseErr)
}

func (b *PostgresLifecycleBoundaryJobs) ValidateLifecycleJobs(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
) error {
	if err := b.requirePlanningDependencies(ctx); err != nil {
		return err
	}
	publicationMode, err := lifecycleJobPublicationMode(request.Operation)
	if err != nil {
		return err
	}
	_, err = b.lifecycleJobPublicationMaterial(ctx, request, mode, publicationMode)
	return err
}

func (b *PostgresLifecycleBoundaryJobs) requireOperationalDependencies(ctx context.Context) error {
	if b == nil || ctx == nil || b.runtime == nil || b.schedules == nil {
		return ErrLifecycleBoundaryJobsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (b *PostgresLifecycleBoundaryJobs) requirePlanningDependencies(ctx context.Context) error {
	if b == nil || ctx == nil || b.coordinator == nil || b.coordinator.Store == nil {
		return ErrLifecycleBoundaryJobsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (b *PostgresLifecycleBoundaryJobs) lifecycleJobPublicationMaterial(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	publicationMode LifecycleBoundaryPublicationMode,
) (lifecycleJobPublicationMaterial, error) {
	if err := validateLifecycleJobMode(request.Operation, mode, publicationMode); err != nil {
		return lifecycleJobPublicationMaterial{}, err
	}
	material := lifecycleJobPublicationMaterial{}
	var err error
	sourceEnabled := request.SourceExtension != nil && request.Operation != extensions.LifecycleMachineInstall && request.Operation != extensions.LifecycleMachineEnable
	if sourceEnabled {
		material.Source, err = b.buildLifecycleJobSnapshot(ctx, *request.SourceExtension, request.SourceBinding, true, request.Operation == extensions.LifecycleMachineUpgrade || request.Operation == extensions.LifecycleMachineRollback)
		if err != nil {
			return lifecycleJobPublicationMaterial{}, fmt.Errorf("source jobs: %w", err)
		}
	} else {
		material.Source = disabledLifecycleJobSnapshot()
	}
	targetEnabled := publicationMode == LifecycleBoundaryActivate
	material.Target, err = b.buildLifecycleJobSnapshot(ctx, request.TargetExtension, request.TargetBinding, targetEnabled, targetEnabled)
	if err != nil {
		return lifecycleJobPublicationMaterial{}, fmt.Errorf("target jobs: %w", err)
	}

	material.Input = hostapi.PluginJobLifecycleInput{
		ExtensionID:            request.TargetExtension.ID,
		SourceContracts:        lifecycleJobContractMap(material.Source),
		TargetContracts:        lifecycleJobContractMap(material.Target),
		SourceRuntimeAvailable: request.Operation == extensions.LifecycleMachineUpgrade || request.Operation == extensions.LifecycleMachineRollback,
	}
	if b.migrations != nil {
		material.Input.Migrations, material.Input.Migrators, err = b.migrations.LifecycleJobMigrations(ctx, cloneLifecycleBoundaryRequest(request), mode)
		if err != nil {
			return lifecycleJobPublicationMaterial{}, fmt.Errorf("resolve lifecycle job migrations: %w", err)
		}
	}
	plan, err := b.planLifecycleJobs(ctx, material.Input)
	if err != nil {
		return lifecycleJobPublicationMaterial{}, err
	}
	material.Plan = sanitizeLifecycleJobPlan(request, mode, publicationMode, plan)
	if err := validateLifecycleJobReconciliationPlan(request, mode, publicationMode, material.Plan); err != nil {
		return lifecycleJobPublicationMaterial{}, err
	}
	return material, nil
}

func (b *PostgresLifecycleBoundaryJobs) buildLifecycleJobSnapshot(
	ctx context.Context,
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
	enabled bool,
	requireTrust bool,
) (lifecycleJobDesiredSnapshot, error) {
	artifact, err := lifecycleJobArtifact(extension, binding, enabled)
	if err != nil {
		return lifecycleJobDesiredSnapshot{}, err
	}
	snapshot := lifecycleJobDesiredSnapshot{
		Enabled: enabled, Artifact: artifact,
		Jobs:      make([]hostapi.PluginJobRuntimeContract, 0),
		Schedules: make([]supportjobs.PluginScheduleDeclaration, 0),
	}
	if !enabled {
		return snapshot, nil
	}
	if len(extension.Manifest.Jobs) > 0 && requireTrust {
		if b.trust == nil {
			return lifecycleJobDesiredSnapshot{}, ErrLifecycleBoundaryJobsUnavailable
		}
		trust, trustErr := b.trust.RuntimeIdentity(ctx, extension)
		if trustErr != nil || strings.TrimSpace(trust.TrustGrantID) == "" {
			return lifecycleJobDesiredSnapshot{}, fmt.Errorf("resolve exact runtime trust: %w", errors.Join(trustErr, ErrLifecycleBoundaryJobsInvalid))
		}
		snapshot.TrustGrantID = strings.TrimSpace(trust.TrustGrantID)
	} else if requireTrust {
		// No job or schedule declaration consumes a trust id.
		snapshot.TrustGrantID = ""
	}
	jobsByID := make(map[string]hostapi.PluginJobRuntimeContract, len(extension.Manifest.Jobs))
	jobsByName := make(map[string]struct{}, len(extension.Manifest.Jobs))
	for _, declaration := range extension.Manifest.Jobs {
		contract, contractErr := extensions.PluginJobContractForExtension(extension, declaration.Name)
		if contractErr != nil {
			return lifecycleJobDesiredSnapshot{}, contractErr
		}
		if _, duplicate := jobsByName[contract.JobName]; duplicate {
			return lifecycleJobDesiredSnapshot{}, fmt.Errorf("%w: duplicate job name %q", ErrLifecycleBoundaryJobsInvalid, contract.JobName)
		}
		jobID := strings.TrimSpace(declaration.ID)
		if jobID == "" {
			return lifecycleJobDesiredSnapshot{}, fmt.Errorf("%w: job id is required", ErrLifecycleBoundaryJobsInvalid)
		}
		if _, duplicate := jobsByID[jobID]; duplicate {
			return lifecycleJobDesiredSnapshot{}, fmt.Errorf("%w: duplicate job id %q", ErrLifecycleBoundaryJobsInvalid, jobID)
		}
		runtime := hostapi.PluginJobRuntimeContract{Contract: contract, TrustGrantID: snapshot.TrustGrantID}
		jobsByID[jobID] = runtime
		jobsByName[contract.JobName] = struct{}{}
		snapshot.Jobs = append(snapshot.Jobs, runtime)
	}
	sort.Slice(snapshot.Jobs, func(i, j int) bool { return snapshot.Jobs[i].Contract.JobName < snapshot.Jobs[j].Contract.JobName })
	scheduleIDs := make(map[string]struct{}, len(extension.Manifest.Schedules))
	for _, declaration := range extension.Manifest.Schedules {
		scheduleID := strings.TrimSpace(declaration.ID)
		cron := strings.TrimSpace(declaration.Cron)
		if scheduleID == "" || cron == "" || strings.TrimSpace(declaration.ContractVersion) == "" {
			return lifecycleJobDesiredSnapshot{}, fmt.Errorf("%w: complete schedule contract is required", ErrLifecycleBoundaryJobsInvalid)
		}
		if _, duplicate := scheduleIDs[scheduleID]; duplicate {
			return lifecycleJobDesiredSnapshot{}, fmt.Errorf("%w: duplicate schedule id %q", ErrLifecycleBoundaryJobsInvalid, scheduleID)
		}
		job, ok := jobsByID[strings.TrimSpace(declaration.JobID)]
		if !ok {
			return lifecycleJobDesiredSnapshot{}, fmt.Errorf("%w: schedule %q references unknown job", ErrLifecycleBoundaryJobsInvalid, declaration.ID)
		}
		timezone := strings.TrimSpace(declaration.Timezone)
		if timezone == "" {
			timezone = "UTC"
		}
		snapshot.Schedules = append(snapshot.Schedules, supportjobs.PluginScheduleDeclaration{
			ScheduleID: scheduleID, JobName: job.Contract.JobName,
			JobContract: job.Contract.JobContract, Cron: cron, Timezone: timezone,
			Contract: job.Contract, TrustGrantID: job.TrustGrantID,
		})
		scheduleIDs[scheduleID] = struct{}{}
	}
	sort.Slice(snapshot.Schedules, func(i, j int) bool { return snapshot.Schedules[i].ScheduleID < snapshot.Schedules[j].ScheduleID })
	return snapshot, nil
}

func (b *PostgresLifecycleBoundaryJobs) planLifecycleJobs(
	ctx context.Context,
	input hostapi.PluginJobLifecycleInput,
) (hostapi.PluginJobLifecyclePlan, error) {
	var plan hostapi.PluginJobLifecyclePlan
	err := b.coordinator.Store.WithPluginJobLifecycleTx(ctx, func(tx hostapi.PluginJobLifecycleTx) error {
		rows, err := tx.LockPluginJobs(ctx, input.ExtensionID)
		if err != nil {
			return fmt.Errorf("lock plugin jobs for lifecycle plan: %w", err)
		}
		plan, err = hostapi.PlanPluginJobLifecycle(input, rows)
		return err
	})
	return plan, err
}

func (b *PostgresLifecycleBoundaryJobs) beginDrainLifecycleSchedules(
	extension extensions.Extension,
	identity RuntimeInstanceIdentity,
) (supportjobs.PluginScheduleRuntimeIdentity, bool, error) {
	if len(extension.Manifest.Schedules) == 0 {
		return supportjobs.PluginScheduleRuntimeIdentity{}, false, nil
	}
	scheduleIdentity := supportjobs.PluginScheduleRuntimeIdentity{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		ArtifactDigest: extension.PackageDigest, InstanceID: identity.InstanceID,
	}
	_, err := b.schedules.BeginDrain(scheduleIdentity)
	if errors.Is(err, supportjobs.ErrPluginScheduleRuntimeStale) {
		// No exact schedule runtime means there is no trigger admission to close.
		return scheduleIdentity, false, nil
	}
	if err != nil {
		return scheduleIdentity, false, err
	}
	return scheduleIdentity, true, nil
}

func (b *PostgresLifecycleBoundaryJobs) beginDrainLifecycleJobRuntime(
	extension extensions.Extension,
	identity RuntimeInstanceIdentity,
) error {
	if _, err := b.inspectExactLifecycleJobRuntime(extension, identity); err != nil {
		return err
	}
	snapshot, err := b.runtime.BeginDrain(identity)
	if err != nil {
		return err
	}
	if err := validateLifecycleBoundaryAdmission("drain lifecycle jobs", snapshot, identity, true, false); err != nil {
		return err
	}
	return nil
}

func (b *PostgresLifecycleBoundaryJobs) inspectExactLifecycleJobRuntime(
	extension extensions.Extension,
	identity RuntimeInstanceIdentity,
) (RuntimeInstanceSnapshot, error) {
	snapshot, err := b.runtime.InspectRuntimeInstance(identity)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	if !runtimeInstanceMatchesExtension(snapshot, extension) || snapshot.Identity != identity {
		return RuntimeInstanceSnapshot{}, fmt.Errorf("%w: lifecycle job runtime changed exact artifact", ErrLifecycleBoundaryJobsConflict)
	}
	return snapshot, nil
}

func lifecycleJobRole(
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	role extensions.LifecycleCoordinatorRuntimeRole,
) (extensions.Extension, extensions.LifecycleRuntimeBinding, error) {
	publicationMode, err := lifecycleJobPublicationMode(request.Operation)
	if err != nil || validateLifecycleJobMode(request.Operation, mode, publicationMode) != nil {
		return extensions.Extension{}, extensions.LifecycleRuntimeBinding{}, ErrLifecycleBoundaryJobsInvalid
	}
	switch role {
	case extensions.LifecycleRuntimeSource:
		if request.SourceExtension == nil {
			return extensions.Extension{}, extensions.LifecycleRuntimeBinding{}, fmt.Errorf("%w: source artifact is required", ErrLifecycleBoundaryJobsInvalid)
		}
		return *request.SourceExtension, request.SourceBinding, nil
	case extensions.LifecycleRuntimeTarget:
		if publicationMode != LifecycleBoundaryActivate {
			return extensions.Extension{}, extensions.LifecycleRuntimeBinding{}, fmt.Errorf("%w: deactivation has no target runtime", ErrLifecycleBoundaryJobsInvalid)
		}
		return request.TargetExtension, request.TargetBinding, nil
	default:
		return extensions.Extension{}, extensions.LifecycleRuntimeBinding{}, fmt.Errorf("%w: unknown runtime role %q", ErrLifecycleBoundaryJobsInvalid, role)
	}
}

func lifecycleJobRuntimeIdentity(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (RuntimeInstanceIdentity, error) {
	if err := validateExactCoordinatorBinding("lifecycle jobs", binding, extension, true); err != nil {
		return RuntimeInstanceIdentity{}, fmt.Errorf("%w: %v", ErrLifecycleBoundaryJobsInvalid, err)
	}
	return RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: binding.RuntimeInstanceID}, nil
}

func lifecycleJobArtifact(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
	requireRuntime bool,
) (lifecycleJobArtifactSnapshot, error) {
	if err := validateExactCoordinatorArtifact("lifecycle jobs", extension); err != nil || extension.ActiveVersionID <= 0 {
		return lifecycleJobArtifactSnapshot{}, fmt.Errorf("%w: artifact is not exact", ErrLifecycleBoundaryJobsInvalid)
	}
	if err := validateExactCoordinatorBinding("lifecycle jobs", binding, extension, requireRuntime); err != nil {
		return lifecycleJobArtifactSnapshot{}, fmt.Errorf("%w: binding is not exact", ErrLifecycleBoundaryJobsInvalid)
	}
	return lifecycleJobArtifactSnapshot{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: binding.RuntimeInstanceID, Present: true,
	}, nil
}

func lifecycleScheduleIdentity(artifact lifecycleJobArtifactSnapshot) supportjobs.PluginScheduleRuntimeIdentity {
	return supportjobs.PluginScheduleRuntimeIdentity{
		ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.ExtensionVersion,
		ArtifactDigest: artifact.PackageDigest, InstanceID: artifact.RuntimeInstanceID,
	}
}

func disabledLifecycleJobSnapshot() lifecycleJobDesiredSnapshot {
	return lifecycleJobDesiredSnapshot{
		Enabled: false, Artifact: lifecycleJobArtifactSnapshot{},
		Jobs:      make([]hostapi.PluginJobRuntimeContract, 0),
		Schedules: make([]supportjobs.PluginScheduleDeclaration, 0),
	}
}

func lifecycleJobContractMap(snapshot lifecycleJobDesiredSnapshot) map[string]hostapi.PluginJobRuntimeContract {
	contracts := make(map[string]hostapi.PluginJobRuntimeContract, len(snapshot.Jobs))
	if !snapshot.Enabled {
		return contracts
	}
	for _, runtime := range snapshot.Jobs {
		contracts[runtime.Contract.JobName] = runtime
	}
	return contracts
}

func sanitizeLifecycleJobPlan(
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	publicationMode LifecycleBoundaryPublicationMode,
	plan hostapi.PluginJobLifecyclePlan,
) lifecycleJobReconciliationPlan {
	result := lifecycleJobReconciliationPlan{
		Schema: "sforum.lifecycle.job-reconciliation-plan@1", Operation: request.Operation,
		Mode: mode, PublicationMode: publicationMode, ExtensionID: plan.ExtensionID,
		IgnoredFinalized: plan.IgnoredFinalized,
		Entries:          make([]lifecycleJobReconciliationPlanEntry, 0, len(plan.Entries)),
	}
	for _, entry := range plan.Entries {
		result.Entries = append(result.Entries, lifecycleJobReconciliationPlanEntry{
			JobID: entry.Row.JobID, Action: entry.Decision.Action,
			Reason: entry.Decision.Reason, MigrationID: entry.Decision.MigrationID,
		})
	}
	return result
}

func validateLifecycleJobMode(
	operation extensions.LifecycleMachineOperation,
	mode LifecycleBoundaryJobMode,
	publicationMode LifecycleBoundaryPublicationMode,
) error {
	want, err := lifecycleBoundaryJobModeForOperation(operation)
	if err != nil || want != mode {
		return fmt.Errorf("%w: operation %q does not match mode %q", ErrLifecycleBoundaryJobsInvalid, operation, mode)
	}
	wantPublication, err := lifecycleJobPublicationMode(operation)
	if err != nil || wantPublication != publicationMode {
		return fmt.Errorf("%w: operation %q does not match publication %q", ErrLifecycleBoundaryJobsInvalid, operation, publicationMode)
	}
	return nil
}

func lifecycleJobPublicationMode(operation extensions.LifecycleMachineOperation) (LifecycleBoundaryPublicationMode, error) {
	switch operation {
	case extensions.LifecycleMachineInstall, extensions.LifecycleMachineEnable,
		extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineRollback:
		return LifecycleBoundaryActivate, nil
	case extensions.LifecycleMachineDisable, extensions.LifecycleMachineUninstall:
		return LifecycleBoundaryDeactivate, nil
	default:
		return "", fmt.Errorf("%w: unsupported operation %q", ErrLifecycleBoundaryJobsInvalid, operation)
	}
}

func encodeLifecycleJobJSON(value any) (json.RawMessage, error) {
	document, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode lifecycle jobs document: %w", err)
	}
	return document, nil
}

var _ LifecycleBoundaryJobs = (*PostgresLifecycleBoundaryJobs)(nil)
var _ LifecycleBoundaryCommittedJobs = (*PostgresLifecycleBoundaryJobs)(nil)
var _ LifecycleBoundaryJobRuntime = (*Manager)(nil)
