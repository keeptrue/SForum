package extensionsruntime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
)

const lifecycleBoundaryResultSchema = "sforum.lifecycle.host-boundary@1"

var (
	ErrLifecycleBoundaryInvalid            = errors.New("extension lifecycle composed boundary is invalid")
	ErrLifecycleBoundaryDependencyMissing  = errors.New("extension lifecycle composed boundary dependency is unavailable")
	ErrLifecycleBoundaryCompensationFailed = errors.New("extension lifecycle composed boundary compensation failed")
	ErrLifecycleBoundarySourceResumeUnsafe = errors.New("extension lifecycle source resume is not proven safe")
)

// LifecycleBoundaryRequest is the allowlisted Host input passed to composed
// delegates. Trust grants, authority snapshots, opaque coordinator checkpoints,
// and previous Host results deliberately never cross this boundary.
type LifecycleBoundaryRequest struct {
	OperationID     int64
	Operation       extensions.LifecycleMachineOperation
	Position        int
	StepID          string
	Attempt         int
	SourceExtension *extensions.Extension
	TargetExtension extensions.Extension
	SourceBinding   extensions.LifecycleRuntimeBinding
	TargetBinding   extensions.LifecycleRuntimeBinding
	RemovalMode     string
	Forced          bool
	ActorUserID     int64
	AuditEventID    int64
	actionResults   map[extensions.LifecycleMachineAction]json.RawMessage
	// artifactLoader 让同一个 gate 内的多次 registry 校验共享同一批已加载且已
	// 校验的制品（显式校验与发布准备读的是同一份冻结制品）。由
	// RunLifecycleHostBoundary 创建，克隆时按值携带。
	artifactLoader *extensionopenapi.ArtifactLoader
}

// ActionResult returns a fresh copy on every read. Delegates can inspect a
// durable plugin result but cannot mutate another delegate's view or the
// coordinator-owned ledger copy.
func (r LifecycleBoundaryRequest) ActionResult(action extensions.LifecycleMachineAction) (json.RawMessage, bool) {
	document, ok := r.actionResults[action]
	return cloneHostGateJSON(document), ok
}

func (r LifecycleBoundaryRequest) ActionNames() []extensions.LifecycleMachineAction {
	actions := make([]extensions.LifecycleMachineAction, 0, len(r.actionResults))
	for action := range r.actionResults {
		actions = append(actions, action)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i] < actions[j] })
	return actions
}

type LifecycleBoundaryMigrationMode string

const (
	LifecycleBoundaryMigrationInstall  LifecycleBoundaryMigrationMode = "install"
	LifecycleBoundaryMigrationUpgrade  LifecycleBoundaryMigrationMode = "upgrade"
	LifecycleBoundaryMigrationRollback LifecycleBoundaryMigrationMode = "rollback"
)

type LifecycleBoundaryJobMode string

const (
	LifecycleBoundaryJobsInstall   LifecycleBoundaryJobMode = "install"
	LifecycleBoundaryJobsEnable    LifecycleBoundaryJobMode = "enable"
	LifecycleBoundaryJobsDisable   LifecycleBoundaryJobMode = "disable"
	LifecycleBoundaryJobsUpgrade   LifecycleBoundaryJobMode = "upgrade"
	LifecycleBoundaryJobsRollback  LifecycleBoundaryJobMode = "rollback"
	LifecycleBoundaryJobsUninstall LifecycleBoundaryJobMode = "uninstall"
)

type LifecycleBoundaryPublicationMode string

const (
	LifecycleBoundaryActivate   LifecycleBoundaryPublicationMode = "activate"
	LifecycleBoundaryDeactivate LifecycleBoundaryPublicationMode = "deactivate"
)

type LifecycleBoundaryCleanupMode string

const (
	LifecycleBoundaryCleanupDisable       LifecycleBoundaryCleanupMode = "disable"
	LifecycleBoundaryCleanupRetiredSource LifecycleBoundaryCleanupMode = "retired_source"
	LifecycleBoundaryCleanupPreserve      LifecycleBoundaryCleanupMode = "uninstall_preserve"
	LifecycleBoundaryCleanupExport        LifecycleBoundaryCleanupMode = "uninstall_export_then_remove"
	LifecycleBoundaryCleanupComplete      LifecycleBoundaryCleanupMode = "uninstall_complete_removal"
)

// LifecycleBoundaryTransaction is prepared without side effects. Publish and
// Restore must be idempotent and exact-operation fenced. Publish may return an
// error after changing state, so callers always invoke Restore after a failed
// publication attempt. Inspect and Restore must reconstruct prior-attempt state
// from durable operation/step identity, not an in-memory closure. Job/schedule
// transactions switch desired snapshots while keeping admission closed; only
// ResumeLifecycleJobs may open an exact source or target role.
type LifecycleBoundaryTransaction interface {
	Inspect(context.Context) (LifecycleBoundaryTransactionState, error)
	Publish(context.Context) error
	Restore(context.Context) error
}

type LifecycleBoundaryTransactionState string

const (
	LifecycleBoundaryTransactionSource LifecycleBoundaryTransactionState = "source"
	LifecycleBoundaryTransactionTarget LifecycleBoundaryTransactionState = "target"
)

type LifecycleBoundaryPreflight interface {
	CheckLifecycleBoundary(context.Context, LifecycleBoundaryRequest) error
}

// Migration and cleanup delegates own durable idempotency by OperationID and
// StepID. A retry must resume or prove the exact step complete; it may not infer
// success from a different artifact or attempt.
type LifecycleBoundaryMigrations interface {
	ReconcileLifecycleMigrations(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryMigrationMode) error
	// CanResumeLifecycleSource is a durable proof that migration state is either
	// rolled back or backward-compatible with the frozen source artifact.
	CanResumeLifecycleSource(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryMigrationMode) (bool, error)
}

type LifecycleBoundaryJobs interface {
	// Drain/Resume atomically cover queued-job enqueue admission and schedule
	// trigger admission for the exact source/target artifact. Resume publishes
	// schedules while RuntimeCallJob remains drained, then opens that exact
	// runtime once as the final action.
	DrainLifecycleJobs(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryJobMode, extensions.LifecycleCoordinatorRuntimeRole) error
	ResumeLifecycleJobs(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryJobMode, extensions.LifecycleCoordinatorRuntimeRole) error
	ValidateLifecycleJobs(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryJobMode) error
	PrepareLifecycleJobPublication(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryPublicationMode) (LifecycleBoundaryTransaction, error)
	// River cancel/migrate is intentionally outside the reversible prepared
	// transaction and may execute only after the shared marker commits.
	ReconcileCommittedLifecycleJobs(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryJobMode, LifecycleBoundaryPublicationMode) error
}

// LifecycleHostDrainBoundary extends the dispatcher at its earlier draining
// gate, before plugin upgrade/uninstall actions run. Production paths require
// this capability; runtime drain alone cannot close schedules or job enqueue.
type LifecycleHostDrainBoundary interface {
	DrainLifecycleHostSources(context.Context, extensions.LifecycleCoordinatorGateRequest) error
	CanResumeLifecycleHostSources(context.Context, extensions.LifecycleCoordinatorGateRequest) (bool, error)
	ResumeLifecycleHostSources(context.Context, extensions.LifecycleCoordinatorGateRequest) error
}

// LifecycleBoundaryRegistries is an aggregate transaction over every registry
// affected by one artifact (routes, pages, services, schedules, and later V3
// families). Adapters must not expose a partially published family snapshot.
type LifecycleBoundaryRegistries interface {
	ValidateLifecycleRegistries(context.Context, LifecycleBoundaryRequest) error
	PrepareLifecycleRegistryPublication(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryPublicationMode) (LifecycleBoundaryTransaction, error)
}

// LifecycleBoundaryState prepares the exact durable extension-version/status
// CAS. It must restore only the captured source state and reject stale writers.
type LifecycleBoundaryState interface {
	PrepareLifecycleStatePublication(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryPublicationMode) (LifecycleBoundaryTransaction, error)
}

// LifecycleBoundaryPublicationJournal is stored outside the deletable
// extensions row. Prepare durably fences operation/step/mode and both exact
// artifacts before publication. Committed is the sole authority deciding
// whether crash recovery converges backward or forward.
type LifecycleBoundaryPublicationJournal interface {
	PrepareLifecyclePublication(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryPublicationMode) error
	LifecyclePublicationCommitted(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryPublicationMode) (bool, error)
	// LifecyclePublicationCommittedForOperation is used by earlier Host gates
	// whose per-step attempt is unrelated to the publication step attempt. It
	// still validates the canonical publication step and both exact artifacts.
	LifecyclePublicationCommittedForOperation(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryPublicationMode) (bool, error)
	CommitLifecyclePublication(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryPublicationMode) error
}

type LifecycleBoundaryCleanupResult struct {
	DurableTombstone        bool
	TombstoneID             string
	IdentityRetained        bool
	PackageRetained         bool
	RuntimeRecoveryRetained bool
	RetentionMarker         string
	ExportArtifactID        string
	ExportDigest            string
}

// Uninstall cleanup only stages a durable pending purge. The exact extension
// identity, package bytes, and recovery material must remain until the
// coordinator has durably committed the terminal operation; a later finalizer
// owns irreversible deletion.
type LifecycleBoundaryCleanup interface {
	StageLifecycleHostCleanup(context.Context, LifecycleBoundaryRequest, LifecycleBoundaryCleanupMode) (LifecycleBoundaryCleanupResult, error)
}

type LifecycleBoundaryRuntime interface {
	PublishDrainedRuntimeInstance(context.Context, RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error)
	PublishRuntimeInstance(context.Context, RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error)
	BeginDrain(RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error)
	WaitDrain(context.Context, RuntimeInstanceIdentity) error
	ResumeRuntimeInstance(RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error)
	StopRuntimeInstance(context.Context, RuntimeInstanceIdentity) error
}

type ComposedLifecycleHostBoundaryDependencies struct {
	Runtime    LifecycleBoundaryRuntime
	Preflight  LifecycleBoundaryPreflight
	Migrations LifecycleBoundaryMigrations
	Jobs       LifecycleBoundaryJobs
	Registries LifecycleBoundaryRegistries
	State      LifecycleBoundaryState
	Journal    LifecycleBoundaryPublicationJournal
	Cleanup    LifecycleBoundaryCleanup
}

type ComposedLifecycleHostBoundary struct {
	dependencies ComposedLifecycleHostBoundaryDependencies
}

func NewComposedLifecycleHostBoundary(dependencies ComposedLifecycleHostBoundaryDependencies) *ComposedLifecycleHostBoundary {
	return &ComposedLifecycleHostBoundary{dependencies: dependencies}
}

func (b *ComposedLifecycleHostBoundary) RunLifecycleHostBoundary(
	ctx context.Context,
	gate extensions.LifecycleCoordinatorGateRequest,
) (LifecycleHostBoundaryResult, error) {
	if b == nil || ctx == nil {
		return LifecycleHostBoundaryResult{}, fmt.Errorf("%w: boundary and context are required", ErrLifecycleBoundaryInvalid)
	}
	if err := ctx.Err(); err != nil {
		return LifecycleHostBoundaryResult{}, err
	}
	if err := validateLifecycleHostGateRequest(gate); err != nil {
		return LifecycleHostBoundaryResult{}, fmt.Errorf("%w: %v", ErrLifecycleBoundaryInvalid, err)
	}
	if gate.Revalidation {
		return LifecycleHostBoundaryResult{}, fmt.Errorf("%w: durable composed boundaries cannot serve process revalidation", ErrLifecycleBoundaryInvalid)
	}
	request, err := newLifecycleBoundaryRequest(gate)
	if err != nil {
		return LifecycleHostBoundaryResult{}, err
	}

	var stage string
	switch gate.Operation {
	case extensions.LifecycleMachineInstall:
		stage, err = b.runInstall(ctx, request)
	case extensions.LifecycleMachineEnable:
		stage, err = b.runEnable(ctx, request)
	case extensions.LifecycleMachineDisable:
		stage, err = b.runDisable(ctx, request)
	case extensions.LifecycleMachineUpgrade:
		stage, err = b.runUpgrade(ctx, request)
	case extensions.LifecycleMachineRollback:
		stage, err = b.runRollback(ctx, request)
	case extensions.LifecycleMachineUninstall:
		stage, err = b.runUninstall(ctx, request)
	default:
		err = fmt.Errorf("%w: unsupported operation %q", ErrLifecycleBoundaryInvalid, gate.Operation)
	}
	if err != nil {
		return LifecycleHostBoundaryResult{}, err
	}
	return lifecycleBoundarySuccess(request, stage)
}

func (b *ComposedLifecycleHostBoundary) DrainLifecycleHostSources(
	ctx context.Context,
	gate extensions.LifecycleCoordinatorGateRequest,
) error {
	request, mode, err := b.lifecycleDrainRequest(ctx, gate)
	if err != nil {
		return err
	}
	if b.dependencies.Jobs == nil {
		return lifecycleBoundaryMissing("jobs and schedules", request)
	}
	return b.dependencies.Jobs.DrainLifecycleJobs(
		ctx, cloneLifecycleBoundaryRequest(request), mode, extensions.LifecycleRuntimeSource,
	)
}

func (b *ComposedLifecycleHostBoundary) ResumeLifecycleHostSources(
	ctx context.Context,
	gate extensions.LifecycleCoordinatorGateRequest,
) error {
	request, mode, err := b.lifecycleDrainRequest(ctx, gate)
	if err != nil {
		return err
	}
	if b.dependencies.Jobs == nil {
		return lifecycleBoundaryMissing("jobs and schedules", request)
	}
	return b.dependencies.Jobs.ResumeLifecycleJobs(
		ctx, cloneLifecycleBoundaryRequest(request), mode, extensions.LifecycleRuntimeSource,
	)
}

func (b *ComposedLifecycleHostBoundary) CanResumeLifecycleHostSources(
	ctx context.Context,
	gate extensions.LifecycleCoordinatorGateRequest,
) (bool, error) {
	request, _, err := b.lifecycleDrainRequest(ctx, gate)
	if err != nil {
		return false, err
	}
	if b.dependencies.Journal == nil {
		return false, lifecycleBoundaryMissing("publication journal", request)
	}
	publication, mode, err := lifecycleBoundaryCanonicalPublication(request)
	if err != nil {
		return false, err
	}
	committed, err := b.dependencies.Journal.LifecyclePublicationCommittedForOperation(
		ctx, publication, mode,
	)
	if err != nil {
		return false, err
	}
	if committed {
		return false, nil
	}
	if err := b.requireLifecycleSourceResumeProof(ctx, request); err != nil {
		return false, err
	}
	return true, nil
}

func lifecycleBoundaryCanonicalPublication(
	request LifecycleBoundaryRequest,
) (LifecycleBoundaryRequest, LifecycleBoundaryPublicationMode, error) {
	position := -1
	mode := LifecycleBoundaryActivate
	switch request.Operation {
	case extensions.LifecycleMachineInstall:
		position = 8
	case extensions.LifecycleMachineEnable:
		position = 5
	case extensions.LifecycleMachineDisable:
		position, mode = 3, LifecycleBoundaryDeactivate
	case extensions.LifecycleMachineUpgrade:
		position = 8
	case extensions.LifecycleMachineRollback:
		position = 6
	case extensions.LifecycleMachineUninstall:
		position, mode = 3, LifecycleBoundaryDeactivate
	default:
		return LifecycleBoundaryRequest{}, "", fmt.Errorf(
			"%w: operation %q has no publication boundary", ErrLifecycleBoundaryInvalid, request.Operation,
		)
	}
	path, err := extensions.RecommendedLifecyclePath(request.Operation)
	if err != nil || position >= len(path) || path[position].Action != "" {
		return LifecycleBoundaryRequest{}, "", fmt.Errorf(
			"%w: canonical publication boundary is unavailable for %q", ErrLifecycleBoundaryInvalid, request.Operation,
		)
	}
	publication := cloneLifecycleBoundaryRequest(request)
	publication.Position = position
	publication.StepID = fmt.Sprintf(
		"lifecycle.%s.%02d.host.%s", request.Operation, position, path[position].State,
	)
	return publication, mode, nil
}

func (b *ComposedLifecycleHostBoundary) lifecycleDrainRequest(
	ctx context.Context,
	gate extensions.LifecycleCoordinatorGateRequest,
) (LifecycleBoundaryRequest, LifecycleBoundaryJobMode, error) {
	if b == nil || ctx == nil {
		return LifecycleBoundaryRequest{}, "", fmt.Errorf("%w: boundary and context are required", ErrLifecycleBoundaryInvalid)
	}
	if err := ctx.Err(); err != nil {
		return LifecycleBoundaryRequest{}, "", err
	}
	if err := validateLifecycleHostGateRequest(gate); err != nil || lookupLifecycleHostGateKind(gate.Operation, gate.Position) != lifecycleHostGateDrain {
		return LifecycleBoundaryRequest{}, "", fmt.Errorf("%w: request is not a lifecycle drain gate", ErrLifecycleBoundaryInvalid)
	}
	request, err := newLifecycleBoundaryRequest(gate)
	if err != nil {
		return LifecycleBoundaryRequest{}, "", err
	}
	mode, err := lifecycleBoundaryJobModeForOperation(gate.Operation)
	return request, mode, err
}

func (b *ComposedLifecycleHostBoundary) runInstall(ctx context.Context, request LifecycleBoundaryRequest) (string, error) {
	switch request.Position {
	case 2:
		if err := b.checkPreflight(ctx, request); err != nil {
			return "", err
		}
		if err := b.runMigrations(ctx, request, LifecycleBoundaryMigrationInstall); err != nil {
			return "", err
		}
		return "migrations", nil
	case 7:
		if err := b.validateJobs(ctx, request, LifecycleBoundaryJobsInstall); err != nil {
			return "", err
		}
		if err := b.validateRegistries(ctx, request); err != nil {
			return "", err
		}
		return "registry_prepared", nil
	case 8:
		return "published", b.publishActivation(ctx, request, LifecycleBoundaryJobsInstall)
	default:
		return "", lifecycleBoundaryUnsupported(request)
	}
}

func (b *ComposedLifecycleHostBoundary) runEnable(ctx context.Context, request LifecycleBoundaryRequest) (string, error) {
	switch request.Position {
	case 4:
		if err := b.checkPreflight(ctx, request); err != nil {
			return "", err
		}
		if err := b.validateJobs(ctx, request, LifecycleBoundaryJobsEnable); err != nil {
			return "", err
		}
		if err := b.validateRegistries(ctx, request); err != nil {
			return "", err
		}
		return "registry_prepared", nil
	case 5:
		return "published", b.publishActivation(ctx, request, LifecycleBoundaryJobsEnable)
	default:
		return "", lifecycleBoundaryUnsupported(request)
	}
}

func (b *ComposedLifecycleHostBoundary) runDisable(ctx context.Context, request LifecycleBoundaryRequest) (string, error) {
	if request.Position != 3 {
		return "", lifecycleBoundaryUnsupported(request)
	}
	if err := b.drainSourceAdmissions(ctx, request, LifecycleBoundaryJobsDisable); err != nil {
		return "", b.failBeforePublication(ctx, request, err)
	}
	if b.dependencies.Cleanup == nil {
		return "", b.failBeforePublication(ctx, request, lifecycleBoundaryMissing("cleanup", request))
	}
	if err := b.checkPreflight(ctx, request); err != nil {
		return "", b.failBeforePublication(ctx, request, err)
	}
	if err := b.publishDeactivation(ctx, request); err != nil {
		return "", err
	}
	if err := b.runCleanup(ctx, request, LifecycleBoundaryCleanupDisable); err != nil {
		return "", err
	}
	if err := b.stopSourceRuntime(ctx, request); err != nil {
		return "", err
	}
	return "disabled", nil
}

func (b *ComposedLifecycleHostBoundary) runUpgrade(ctx context.Context, request LifecycleBoundaryRequest) (string, error) {
	switch request.Position {
	case 4:
		if err := b.drainSourceAdmissions(ctx, request, LifecycleBoundaryJobsUpgrade); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		if err := b.checkPreflight(ctx, request); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		if err := b.runMigrations(ctx, request, LifecycleBoundaryMigrationUpgrade); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		if err := b.validateJobs(ctx, request, LifecycleBoundaryJobsUpgrade); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		return "migrations", nil
	case 7:
		if err := b.drainSourceAdmissions(ctx, request, LifecycleBoundaryJobsUpgrade); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		if err := b.validateRegistries(ctx, request); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		return "registry_prepared", nil
	case 8:
		return "published", b.publishActivation(ctx, request, LifecycleBoundaryJobsUpgrade)
	case 10:
		if err := b.runCleanup(ctx, request, LifecycleBoundaryCleanupRetiredSource); err != nil {
			return "", err
		}
		if err := b.stopSourceRuntime(ctx, request); err != nil {
			return "", err
		}
		return "source_retired", nil
	default:
		return "", lifecycleBoundaryUnsupported(request)
	}
}

func (b *ComposedLifecycleHostBoundary) runRollback(ctx context.Context, request LifecycleBoundaryRequest) (string, error) {
	switch request.Position {
	case 5:
		if err := b.drainSourceAdmissions(ctx, request, LifecycleBoundaryJobsRollback); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		if err := b.checkPreflight(ctx, request); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		if err := b.runMigrations(ctx, request, LifecycleBoundaryMigrationRollback); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		if err := b.validateJobs(ctx, request, LifecycleBoundaryJobsRollback); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		if err := b.validateRegistries(ctx, request); err != nil {
			return "", b.failBeforePublication(ctx, request, err)
		}
		return "registry_prepared", nil
	case 6:
		if b.dependencies.Cleanup == nil {
			return "", b.failBeforePublication(ctx, request, lifecycleBoundaryMissing("cleanup", request))
		}
		if err := b.publishActivation(ctx, request, LifecycleBoundaryJobsRollback); err != nil {
			return "", err
		}
		if err := b.runCleanup(ctx, request, LifecycleBoundaryCleanupRetiredSource); err != nil {
			return "", err
		}
		if err := b.stopSourceRuntime(ctx, request); err != nil {
			return "", err
		}
		return "published", nil
	default:
		return "", lifecycleBoundaryUnsupported(request)
	}
}

func (b *ComposedLifecycleHostBoundary) runUninstall(ctx context.Context, request LifecycleBoundaryRequest) (string, error) {
	switch request.Position {
	case 3:
		if err := b.drainSourceAdmissions(ctx, request, LifecycleBoundaryJobsUninstall); err != nil {
			return "", b.failBeforePublication(ctx, request, fmt.Errorf("uninstall drain source admissions: %w", err))
		}
		if err := b.checkPreflight(ctx, request); err != nil {
			return "", b.failBeforePublication(ctx, request, fmt.Errorf("uninstall static preflight: %w", err))
		}
		if err := b.publishDeactivation(ctx, request); err != nil {
			return "", fmt.Errorf("uninstall deactivation publication: %w", err)
		}
		// The exact source runtime remains retained for uninstall/uninstall.after.
		return "registrations_removed", nil
	case 6:
		if b.dependencies.Cleanup == nil {
			return "", lifecycleBoundaryMissing("cleanup", request)
		}
		mode, err := lifecycleBoundaryUninstallCleanupMode(request.RemovalMode)
		if err != nil {
			return "", err
		}
		// Plugin hooks are complete. Stop exact code before package/data removal;
		// not-found is accepted only here for crash-resume idempotency.
		if err := b.stopSourceRuntime(ctx, request); err != nil {
			return "", err
		}
		if err := b.runCleanup(ctx, request, mode); err != nil {
			return "", err
		}
		return "removal_staged", nil
	default:
		return "", lifecycleBoundaryUnsupported(request)
	}
}

func (b *ComposedLifecycleHostBoundary) checkPreflight(ctx context.Context, request LifecycleBoundaryRequest) error {
	if b.dependencies.Preflight == nil {
		return lifecycleBoundaryMissing("preflight", request)
	}
	return b.dependencies.Preflight.CheckLifecycleBoundary(ctx, cloneLifecycleBoundaryRequest(request))
}

func (b *ComposedLifecycleHostBoundary) runMigrations(ctx context.Context, request LifecycleBoundaryRequest, mode LifecycleBoundaryMigrationMode) error {
	if b.dependencies.Migrations == nil {
		return lifecycleBoundaryMissing("migrations", request)
	}
	return b.dependencies.Migrations.ReconcileLifecycleMigrations(ctx, cloneLifecycleBoundaryRequest(request), mode)
}

func (b *ComposedLifecycleHostBoundary) validateJobs(ctx context.Context, request LifecycleBoundaryRequest, mode LifecycleBoundaryJobMode) error {
	if b.dependencies.Jobs == nil {
		return lifecycleBoundaryMissing("jobs and schedules", request)
	}
	return b.dependencies.Jobs.ValidateLifecycleJobs(ctx, cloneLifecycleBoundaryRequest(request), mode)
}

func (b *ComposedLifecycleHostBoundary) validateRegistries(ctx context.Context, request LifecycleBoundaryRequest) error {
	if b.dependencies.Registries == nil {
		return lifecycleBoundaryMissing("registries", request)
	}
	return b.dependencies.Registries.ValidateLifecycleRegistries(ctx, cloneLifecycleBoundaryRequest(request))
}

func (b *ComposedLifecycleHostBoundary) runCleanup(ctx context.Context, request LifecycleBoundaryRequest, mode LifecycleBoundaryCleanupMode) error {
	if b.dependencies.Cleanup == nil {
		return lifecycleBoundaryMissing("cleanup", request)
	}
	result, err := b.dependencies.Cleanup.StageLifecycleHostCleanup(ctx, cloneLifecycleBoundaryRequest(request), mode)
	if err != nil {
		return err
	}
	if !result.IdentityRetained || !result.PackageRetained || !result.RuntimeRecoveryRetained {
		return fmt.Errorf("%w: cleanup must retain exact re-enable or rollback recovery state", ErrLifecycleBoundaryInvalid)
	}
	if request.Operation == extensions.LifecycleMachineUninstall && !result.DurableTombstone {
		return fmt.Errorf("%w: uninstall cleanup must stage a durable tombstone until terminal commit", ErrLifecycleBoundaryInvalid)
	}
	if request.Operation == extensions.LifecycleMachineUninstall {
		if !validLifecycleCleanupReference(result.TombstoneID) {
			return fmt.Errorf("%w: uninstall cleanup requires a durable tombstone identity", ErrLifecycleBoundaryInvalid)
		}
		switch mode {
		case LifecycleBoundaryCleanupPreserve:
			if !validLifecycleCleanupReference(result.RetentionMarker) {
				return fmt.Errorf("%w: preserve cleanup requires a retained-data marker", ErrLifecycleBoundaryInvalid)
			}
		case LifecycleBoundaryCleanupExport:
			if !validLifecycleCleanupReference(result.ExportArtifactID) || !validLifecycleCleanupDigest(result.ExportDigest) {
				return fmt.Errorf("%w: export cleanup requires a durable export artifact and digest", ErrLifecycleBoundaryInvalid)
			}
		}
	}
	return nil
}

func validLifecycleCleanupReference(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 200
}

func validLifecycleCleanupDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) || value != strings.TrimSpace(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func newLifecycleBoundaryRequest(gate extensions.LifecycleCoordinatorGateRequest) (LifecycleBoundaryRequest, error) {
	allowed := lifecycleBoundaryAllowedActions(gate.Operation, gate.Position)
	if allowed == nil || len(gate.ActionResults) != len(allowed) {
		return LifecycleBoundaryRequest{}, fmt.Errorf("%w: action result set is not allowlisted for %s position %d", ErrLifecycleBoundaryInvalid, gate.Operation, gate.Position)
	}
	results := make(map[extensions.LifecycleMachineAction]json.RawMessage, len(allowed))
	for _, action := range allowed {
		document, ok := gate.ActionResults[action]
		if !ok || (len(document) > 0 && !json.Valid(document)) {
			return LifecycleBoundaryRequest{}, fmt.Errorf("%w: action result %q is missing or malformed", ErrLifecycleBoundaryInvalid, action)
		}
		results[action] = cloneHostGateJSON(document)
	}
	target, err := cloneManagedRuntimeExtension(gate.TargetExtension)
	if err != nil {
		return LifecycleBoundaryRequest{}, fmt.Errorf("%w: clone target artifact: %v", ErrLifecycleBoundaryInvalid, err)
	}
	var source *extensions.Extension
	if gate.SourceExtension != nil {
		cloned, cloneErr := cloneManagedRuntimeExtension(*gate.SourceExtension)
		if cloneErr != nil {
			return LifecycleBoundaryRequest{}, fmt.Errorf("%w: clone source artifact: %v", ErrLifecycleBoundaryInvalid, cloneErr)
		}
		source = &cloned
	} else if lifecycleHostRequiresSource(gate.Operation) {
		cloned := target
		source = &cloned
	}
	return LifecycleBoundaryRequest{
		OperationID: gate.OperationID, Operation: gate.Operation, Position: gate.Position,
		StepID: gate.StepID, Attempt: gate.Attempt, SourceExtension: source, TargetExtension: target,
		SourceBinding: gate.SourceBinding, TargetBinding: gate.TargetBinding,
		RemovalMode: gate.RemovalMode, Forced: gate.Forced,
		ActorUserID: gate.ActorUserID, AuditEventID: gate.AuditEventID, actionResults: results,
		artifactLoader: extensionopenapi.NewArtifactLoader(),
	}, nil
}

func cloneLifecycleBoundaryRequest(request LifecycleBoundaryRequest) LifecycleBoundaryRequest {
	clone := request
	if target, err := cloneManagedRuntimeExtension(request.TargetExtension); err == nil {
		clone.TargetExtension = target
	}
	if request.SourceExtension != nil {
		if source, err := cloneManagedRuntimeExtension(*request.SourceExtension); err == nil {
			clone.SourceExtension = &source
		}
	}
	clone.actionResults = make(map[extensions.LifecycleMachineAction]json.RawMessage, len(request.actionResults))
	for action, document := range request.actionResults {
		clone.actionResults[action] = cloneHostGateJSON(document)
	}
	return clone
}

func lifecycleBoundaryAllowedActions(operation extensions.LifecycleMachineOperation, position int) []extensions.LifecycleMachineAction {
	path, err := extensions.RecommendedLifecyclePath(operation)
	if err != nil || position < 0 || position >= len(path) || path[position].Action != "" {
		return nil
	}
	actions := make([]extensions.LifecycleMachineAction, 0)
	for index := 0; index < position; index++ {
		if path[index].Action != "" {
			actions = append(actions, path[index].Action)
		}
	}
	return actions
}

func lifecycleBoundaryUninstallCleanupMode(removalMode string) (LifecycleBoundaryCleanupMode, error) {
	switch removalMode {
	case extensions.LifecycleRemovalPreserve:
		return LifecycleBoundaryCleanupPreserve, nil
	case extensions.LifecycleRemovalExportThenRemove:
		return LifecycleBoundaryCleanupExport, nil
	case extensions.LifecycleRemovalComplete:
		return LifecycleBoundaryCleanupComplete, nil
	default:
		return "", fmt.Errorf("%w: unknown uninstall removal mode %q", ErrLifecycleBoundaryInvalid, removalMode)
	}
}

func lifecycleBoundarySuccess(request LifecycleBoundaryRequest, stage string) (LifecycleHostBoundaryResult, error) {
	document, err := json.Marshal(struct {
		Schema      string                               `json:"schema"`
		Operation   extensions.LifecycleMachineOperation `json:"operation"`
		Position    int                                  `json:"position"`
		Stage       string                               `json:"stage"`
		Status      string                               `json:"status"`
		RemovalMode string                               `json:"removalMode,omitempty"`
	}{
		Schema: lifecycleBoundaryResultSchema, Operation: request.Operation,
		Position: request.Position, Stage: stage, Status: "succeeded", RemovalMode: request.RemovalMode,
	})
	if err != nil {
		return LifecycleHostBoundaryResult{}, err
	}
	return LifecycleHostBoundaryResult{
		Checkpoint:     fmt.Sprintf("composed-v1:%s:%02d:%s", request.Operation, request.Position, request.TargetExtension.PackageDigest),
		ResultDocument: document,
	}, nil
}

func lifecycleBoundaryUnsupported(request LifecycleBoundaryRequest) error {
	return fmt.Errorf("%w: unsupported %s position %d", ErrLifecycleBoundaryInvalid, request.Operation, request.Position)
}

func lifecycleBoundaryMissing(name string, request LifecycleBoundaryRequest) error {
	return fmt.Errorf("%w: %s is required for %s position %d", ErrLifecycleBoundaryDependencyMissing, name, request.Operation, request.Position)
}

var _ LifecycleHostBoundary = (*ComposedLifecycleHostBoundary)(nil)
var _ LifecycleHostDrainBoundary = (*ComposedLifecycleHostBoundary)(nil)
var _ LifecycleBoundaryRuntime = (*Manager)(nil)
