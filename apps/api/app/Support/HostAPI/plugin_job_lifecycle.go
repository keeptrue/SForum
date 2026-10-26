package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

var (
	ErrPluginJobLifecycleUnavailable = errors.New("plugin job lifecycle store is unavailable")
	ErrPluginJobLifecycleIsolation   = errors.New("plugin job lifecycle row belongs to another extension")
	ErrPluginJobMigrationPending     = errors.New("plugin job migration ledger entry has no replacement job")
	ErrPluginJobMigrationConflict    = errors.New("plugin job migration ledger entry conflicts with the requested migration")
	ErrPluginJobMigratorUnavailable  = errors.New("plugin job payload migrator is unavailable")
	ErrPluginJobLifecyclePlanDrift   = errors.New("plugin job lifecycle plan changed after preparation")
)

// PluginJobLifecycleRow 是由存储适配器在事务内锁定的 River 行快照。
// 适配器只能枚举 extension.plugin_job，且必须先按 extensionId 缩小范围。
type PluginJobLifecycleRow struct {
	JobID       int64
	Kind        string
	State       rivertype.JobState
	EncodedArgs json.RawMessage
	Attempt     int
	MaxAttempts int
	Queue       string
	Priority    int
	ScheduledAt time.Time
	Tags        []string
}

type PluginJobLifecycleInput struct {
	ExtensionID string
	// SourceContracts 与 TargetContracts 必须来自启动时冻结的完整 Manifest。
	// 显式空 map 表示没有声明；nil 表示调用方没有提供权威快照。
	SourceContracts        map[string]PluginJobRuntimeContract
	TargetContracts        map[string]PluginJobRuntimeContract
	SourceRuntimeAvailable bool
	Migrations             []supportjobs.PluginJobMigration
	Migrators              map[string]PluginJobPayloadMigrator
}

type PluginJobLifecyclePlanEntry struct {
	Row      PluginJobLifecycleRow
	Args     PluginJobArgs
	Target   PluginJobRuntimeContract
	Decision supportjobs.PluginJobDecision
}

type PluginJobLifecyclePlan struct {
	ExtensionID      string
	Entries          []PluginJobLifecyclePlanEntry
	IgnoredFinalized int
}

// PluginJobPayloadMigrator 只迁移 schema-owned payload。不可执行外部副作用；
// exact artifact、trust grant 与完整信封始终由 Host 重建。
type PluginJobPayloadMigrator interface {
	MigratePluginJobPayload(context.Context, PluginJobPayloadMigrationInput) (map[string]any, error)
}

type PluginJobPayloadMigrationInput struct {
	MigrationID string
	Source      supportjobs.PluginJobContract
	Target      supportjobs.PluginJobContract
	Payload     map[string]any
}

type PluginJobMigrationLedgerEntry struct {
	OldJobID           int64
	ExtensionID        string
	MigrationID        string
	SourceContract     supportjobs.PluginJobContract
	SourceTrustGrantID string
	TargetContract     supportjobs.PluginJobContract
	TargetTrustGrantID string
}

type PluginJobMigrationClaim struct {
	Claimed  bool
	NewJobID int64
	Ledger   PluginJobMigrationLedgerEntry
}

// PluginJobLifecycleTx 只暴露 Host ledger 与 River 的公开事务操作。
// Insert/Cancel 必须使用 River 的 InsertTx/JobCancelTx；实现不得直接更新
// River 的 args/encoded_args 私有存储列。Claim 必须回传持久化后的完整
// ledger identity，Complete 必须用同一 identity 条件化链接 replacement。
type PluginJobLifecycleTx interface {
	LockPluginJobs(context.Context, string) ([]PluginJobLifecycleRow, error)
	ClaimPluginJobMigration(context.Context, PluginJobMigrationLedgerEntry) (PluginJobMigrationClaim, error)
	InsertPluginJob(context.Context, PluginJobArgs, *river.InsertOpts) (int64, error)
	CompletePluginJobMigration(context.Context, PluginJobMigrationLedgerEntry, int64) error
	CancelPluginJob(context.Context, int64) error
}

type PluginJobLifecycleStore interface {
	WithPluginJobLifecycleTx(context.Context, func(PluginJobLifecycleTx) error) error
}

type PluginJobLifecycleExecution struct {
	JobID            int64
	Action           supportjobs.PluginJobAction
	Reason           string
	MigrationID      string
	ReplacementJobID int64
}

type PluginJobLifecycleResult struct {
	Plan       PluginJobLifecyclePlan
	Executions []PluginJobLifecycleExecution
	Committed  bool
}

// PluginJobLifecycleExpectedPlan is the sanitized durable fence prepared while
// enqueue and schedule admission are closed. It deliberately contains no args
// or payload bytes.
type PluginJobLifecycleExpectedPlan struct {
	ExtensionID string
	Entries     []PluginJobLifecycleExpectedDecision
}

type PluginJobLifecycleExpectedDecision struct {
	JobID       int64
	Action      supportjobs.PluginJobAction
	Reason      string
	MigrationID string
}

type PluginJobLifecycleCoordinator struct {
	Store PluginJobLifecycleStore
}

// PlanPluginJobLifecycle 对锁定的行生成稳定、可审计的升级动作，不产生副作用。
func PlanPluginJobLifecycle(input PluginJobLifecycleInput, rows []PluginJobLifecycleRow) (PluginJobLifecyclePlan, error) {
	extensionID := strings.TrimSpace(input.ExtensionID)
	if extensionID == "" {
		return PluginJobLifecyclePlan{}, fmt.Errorf("%w: extension id is required", ErrInvalidRequest)
	}
	if err := validatePluginJobLifecycleRuntimes(extensionID, input); err != nil {
		return PluginJobLifecyclePlan{}, err
	}

	ordered := append([]PluginJobLifecycleRow(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].JobID < ordered[j].JobID })
	plan := PluginJobLifecyclePlan{ExtensionID: extensionID, Entries: make([]PluginJobLifecyclePlanEntry, 0, len(ordered))}
	for _, row := range ordered {
		if isFinalizedPluginJobState(row.State) {
			plan.IgnoredFinalized++
			continue
		}
		if row.JobID <= 0 || row.Kind != PluginJobKind {
			return PluginJobLifecyclePlan{}, fmt.Errorf("%w: unexpected row %d kind %q", ErrPluginJobLifecycleIsolation, row.JobID, row.Kind)
		}

		entry := PluginJobLifecyclePlanEntry{Row: clonePluginJobLifecycleRow(row)}
		if err := json.Unmarshal(row.EncodedArgs, &entry.Args); err != nil {
			entry.Decision = cancelPluginJobLifecycleDecision(supportjobs.PluginJobReasonEnvelopeInvalid)
			plan.Entries = append(plan.Entries, entry)
			continue
		}
		if entry.Args.ExtensionID != extensionID {
			return PluginJobLifecyclePlan{}, fmt.Errorf("%w: row %d has extension %q, expected %q", ErrPluginJobLifecycleIsolation, row.JobID, entry.Args.ExtensionID, extensionID)
		}
		if !entry.Args.validEnvelope() {
			entry.Decision = cancelPluginJobLifecycleDecision(supportjobs.PluginJobReasonEnvelopeInvalid)
			plan.Entries = append(plan.Entries, entry)
			continue
		}
		if !isKnownPluginJobState(row.State) {
			entry.Decision = cancelPluginJobLifecycleDecision(supportjobs.PluginJobReasonStateUnknown)
			plan.Entries = append(plan.Entries, entry)
			continue
		}

		source, sourceDeclared := input.SourceContracts[entry.Args.JobName]
		target, targetDeclared := input.TargetContracts[entry.Args.JobName]
		entry.Target = target
		if sourceDeclared && !targetDeclared {
			entry.Decision = cancelPluginJobLifecycleDecision(supportjobs.PluginJobReasonTargetRemoved)
			plan.Entries = append(plan.Entries, entry)
			continue
		}
		if !sourceDeclared && !targetDeclared {
			entry.Decision = cancelPluginJobLifecycleDecision(supportjobs.PluginJobReasonJobUnknown)
			plan.Entries = append(plan.Entries, entry)
			continue
		}

		entry.Decision = supportjobs.DecidePluginJobUpgrade(supportjobs.PluginJobUpgrade{
			Queued: entry.Args.Contract(), Source: source.Contract, Target: target.Contract,
			SourceRuntimeAvailable: input.SourceRuntimeAvailable, Migrations: input.Migrations,
		})
		switch entry.Decision.Action {
		case supportjobs.PluginJobExecute:
			if entry.Args.TrustGrantID != target.TrustGrantID {
				entry.Decision = cancelPluginJobLifecycleDecision(supportjobs.PluginJobReasonTrustGrantStale)
			}
		case supportjobs.PluginJobDrain:
			if entry.Args.TrustGrantID != source.TrustGrantID {
				entry.Decision = cancelPluginJobLifecycleDecision(supportjobs.PluginJobReasonTrustGrantStale)
			}
		case supportjobs.PluginJobMigrate:
			if row.State == rivertype.JobStateRunning {
				entry.Decision = cancelPluginJobLifecycleDecision(supportjobs.PluginJobReasonRunningMigration)
			} else if input.Migrators == nil || input.Migrators[entry.Decision.MigrationID] == nil {
				entry.Decision = cancelPluginJobLifecycleDecision(supportjobs.PluginJobReasonMigratorMissing)
			}
		}
		plan.Entries = append(plan.Entries, entry)
	}
	return plan, nil
}

func (c *PluginJobLifecycleCoordinator) Reconcile(ctx context.Context, input PluginJobLifecycleInput) (PluginJobLifecycleResult, error) {
	return c.reconcile(ctx, input, nil)
}

// ReconcileExpected performs lock -> plan -> expected-plan fence -> execute in
// one River transaction. A retry after an unknown Host evidence commit may
// prove an earlier cancel/migration from durable River and migration-ledger
// facts; it never replays an unproven side effect.
func (c *PluginJobLifecycleCoordinator) ReconcileExpected(
	ctx context.Context,
	input PluginJobLifecycleInput,
	expected PluginJobLifecycleExpectedPlan,
) (PluginJobLifecycleResult, error) {
	return c.reconcile(ctx, input, &expected)
}

func (c *PluginJobLifecycleCoordinator) reconcile(
	ctx context.Context,
	input PluginJobLifecycleInput,
	expected *PluginJobLifecycleExpectedPlan,
) (PluginJobLifecycleResult, error) {
	if c == nil || c.Store == nil {
		return PluginJobLifecycleResult{}, ErrPluginJobLifecycleUnavailable
	}
	var result PluginJobLifecycleResult
	err := c.Store.WithPluginJobLifecycleTx(ctx, func(tx PluginJobLifecycleTx) error {
		rows, err := tx.LockPluginJobs(ctx, strings.TrimSpace(input.ExtensionID))
		if err != nil {
			return fmt.Errorf("lock plugin jobs: %w", err)
		}
		result.Plan, err = PlanPluginJobLifecycle(input, rows)
		if err != nil {
			return err
		}
		plan := result.Plan
		var recovered []PluginJobLifecycleExecution
		if expected != nil {
			plan, recovered, err = fenceExpectedPluginJobLifecyclePlan(ctx, tx, input, rows, result.Plan, *expected)
			if err != nil {
				return err
			}
		}
		executions, executeErr := executePluginJobLifecyclePlan(ctx, tx, input, plan)
		result.Executions = append(recovered, executions...)
		err = executeErr
		return err
	})
	if err != nil {
		return result, err
	}
	result.Committed = true
	return result, nil
}

func fenceExpectedPluginJobLifecyclePlan(
	ctx context.Context,
	tx PluginJobLifecycleTx,
	input PluginJobLifecycleInput,
	rows []PluginJobLifecycleRow,
	actual PluginJobLifecyclePlan,
	expected PluginJobLifecycleExpectedPlan,
) (PluginJobLifecyclePlan, []PluginJobLifecycleExecution, error) {
	if expected.ExtensionID != input.ExtensionID || strings.TrimSpace(expected.ExtensionID) == "" {
		return PluginJobLifecyclePlan{}, nil, ErrPluginJobLifecyclePlanDrift
	}
	expectedByID := make(map[int64]PluginJobLifecycleExpectedDecision, len(expected.Entries))
	for _, decision := range expected.Entries {
		if decision.JobID <= 0 || !knownExpectedPluginJobAction(decision.Action) {
			return PluginJobLifecyclePlan{}, nil, ErrPluginJobLifecyclePlanDrift
		}
		if _, duplicate := expectedByID[decision.JobID]; duplicate {
			return PluginJobLifecyclePlan{}, nil, ErrPluginJobLifecyclePlanDrift
		}
		expectedByID[decision.JobID] = decision
	}
	rowsByID := make(map[int64]PluginJobLifecycleRow, len(rows))
	for _, row := range rows {
		rowsByID[row.JobID] = row
	}
	actualByID := make(map[int64]PluginJobLifecyclePlanEntry, len(actual.Entries))
	for _, entry := range actual.Entries {
		actualByID[entry.Row.JobID] = entry
	}

	toExecute := PluginJobLifecyclePlan{
		ExtensionID: input.ExtensionID, IgnoredFinalized: actual.IgnoredFinalized,
		Entries: make([]PluginJobLifecyclePlanEntry, 0, len(expected.Entries)),
	}
	recovered := make([]PluginJobLifecycleExecution, 0)
	consumedActual := make(map[int64]bool, len(actual.Entries))
	for _, decision := range expected.Entries {
		if entry, ok := actualByID[decision.JobID]; ok {
			if !sameExpectedPluginJobDecision(decision, entry.Decision) {
				return PluginJobLifecyclePlan{}, nil, ErrPluginJobLifecyclePlanDrift
			}
			consumedActual[decision.JobID] = true
			toExecute.Entries = append(toExecute.Entries, entry)
			continue
		}
		row, ok := rowsByID[decision.JobID]
		if !ok || !isFinalizedPluginJobState(row.State) {
			return PluginJobLifecyclePlan{}, nil, ErrPluginJobLifecyclePlanDrift
		}
		execution, replacementID, err := proveFinalizedExpectedPluginJob(ctx, tx, input, row, decision)
		if err != nil {
			return PluginJobLifecyclePlan{}, nil, err
		}
		if replacementID > 0 {
			replacementRow, exists := rowsByID[replacementID]
			if !exists || !exactTargetPluginJobRow(replacementRow, input.TargetContracts) {
				return PluginJobLifecyclePlan{}, nil, ErrPluginJobLifecyclePlanDrift
			}
			replacement, exists := actualByID[replacementID]
			if exists {
				if replacement.Decision.Action != supportjobs.PluginJobExecute ||
					replacement.Decision.Reason != supportjobs.PluginJobReasonExactMatch {
					return PluginJobLifecyclePlan{}, nil, ErrPluginJobLifecyclePlanDrift
				}
				consumedActual[replacementID] = true
			}
		}
		recovered = append(recovered, execution)
	}
	for _, entry := range actual.Entries {
		if !consumedActual[entry.Row.JobID] {
			return PluginJobLifecyclePlan{}, nil, ErrPluginJobLifecyclePlanDrift
		}
	}
	return toExecute, recovered, nil
}

func exactTargetPluginJobRow(
	row PluginJobLifecycleRow,
	targets map[string]PluginJobRuntimeContract,
) bool {
	var args PluginJobArgs
	if json.Unmarshal(row.EncodedArgs, &args) != nil || !args.validEnvelope() {
		return false
	}
	target, ok := targets[args.JobName]
	return ok && args.Contract().Equal(target.Contract) && args.TrustGrantID == target.TrustGrantID
}

func proveFinalizedExpectedPluginJob(
	ctx context.Context,
	tx PluginJobLifecycleTx,
	input PluginJobLifecycleInput,
	row PluginJobLifecycleRow,
	expected PluginJobLifecycleExpectedDecision,
) (PluginJobLifecycleExecution, int64, error) {
	execution := PluginJobLifecycleExecution{
		JobID: row.JobID, Action: expected.Action, Reason: expected.Reason, MigrationID: expected.MigrationID,
	}
	var args PluginJobArgs
	if err := json.Unmarshal(row.EncodedArgs, &args); err != nil || args.ExtensionID != input.ExtensionID {
		return PluginJobLifecycleExecution{}, 0, ErrPluginJobLifecyclePlanDrift
	}
	switch expected.Action {
	case supportjobs.PluginJobCancel:
		if row.State != rivertype.JobStateCancelled {
			return PluginJobLifecycleExecution{}, 0, ErrPluginJobLifecyclePlanDrift
		}
		return execution, 0, nil
	case supportjobs.PluginJobMigrate:
		if row.State != rivertype.JobStateCancelled || expected.MigrationID == "" {
			return PluginJobLifecycleExecution{}, 0, ErrPluginJobLifecyclePlanDrift
		}
		target, ok := input.TargetContracts[args.JobName]
		if !ok {
			return PluginJobLifecycleExecution{}, 0, ErrPluginJobLifecyclePlanDrift
		}
		ledger := PluginJobMigrationLedgerEntry{
			OldJobID: row.JobID, ExtensionID: args.ExtensionID, MigrationID: expected.MigrationID,
			SourceContract: args.Contract(), SourceTrustGrantID: args.TrustGrantID,
			TargetContract: target.Contract, TargetTrustGrantID: target.TrustGrantID,
		}
		claim, err := tx.ClaimPluginJobMigration(ctx, ledger)
		if err != nil || claim.Claimed || claim.NewJobID <= 0 || !samePluginJobMigrationLedger(claim.Ledger, ledger) {
			return PluginJobLifecycleExecution{}, 0, errors.Join(ErrPluginJobLifecyclePlanDrift, err)
		}
		execution.ReplacementJobID = claim.NewJobID
		return execution, claim.NewJobID, nil
	case supportjobs.PluginJobExecute:
		target, ok := input.TargetContracts[args.JobName]
		if !ok || !args.Contract().Equal(target.Contract) || args.TrustGrantID != target.TrustGrantID {
			return PluginJobLifecycleExecution{}, 0, ErrPluginJobLifecyclePlanDrift
		}
		return execution, 0, nil
	case supportjobs.PluginJobDrain:
		source, ok := input.SourceContracts[args.JobName]
		if !ok || !args.Contract().Equal(source.Contract) || args.TrustGrantID != source.TrustGrantID {
			return PluginJobLifecycleExecution{}, 0, ErrPluginJobLifecyclePlanDrift
		}
		return execution, 0, nil
	default:
		return PluginJobLifecycleExecution{}, 0, ErrPluginJobLifecyclePlanDrift
	}
}

func knownExpectedPluginJobAction(action supportjobs.PluginJobAction) bool {
	switch action {
	case supportjobs.PluginJobExecute, supportjobs.PluginJobDrain,
		supportjobs.PluginJobMigrate, supportjobs.PluginJobCancel:
		return true
	default:
		return false
	}
}

func sameExpectedPluginJobDecision(expected PluginJobLifecycleExpectedDecision, actual supportjobs.PluginJobDecision) bool {
	return expected.Action == actual.Action && expected.Reason == actual.Reason && expected.MigrationID == actual.MigrationID
}

func executePluginJobLifecyclePlan(
	ctx context.Context,
	tx PluginJobLifecycleTx,
	input PluginJobLifecycleInput,
	plan PluginJobLifecyclePlan,
) ([]PluginJobLifecycleExecution, error) {
	executions := make([]PluginJobLifecycleExecution, 0, len(plan.Entries))
	for _, entry := range plan.Entries {
		execution := PluginJobLifecycleExecution{
			JobID: entry.Row.JobID, Action: entry.Decision.Action, Reason: entry.Decision.Reason,
			MigrationID: entry.Decision.MigrationID,
		}
		switch entry.Decision.Action {
		case supportjobs.PluginJobExecute, supportjobs.PluginJobDrain:
			// 运行时或旧 runtime 自己完成任务，生命周期事务不改 River 行。
		case supportjobs.PluginJobCancel:
			if err := tx.CancelPluginJob(ctx, entry.Row.JobID); err != nil {
				return executions, fmt.Errorf("cancel plugin job %d: %w", entry.Row.JobID, err)
			}
		case supportjobs.PluginJobMigrate:
			newJobID, err := migratePluginJob(ctx, tx, input, entry)
			if err != nil {
				return executions, err
			}
			execution.ReplacementJobID = newJobID
		default:
			return executions, fmt.Errorf("%w: unsupported action %q", ErrInvalidRequest, entry.Decision.Action)
		}
		executions = append(executions, execution)
	}
	return executions, nil
}

func migratePluginJob(
	ctx context.Context,
	tx PluginJobLifecycleTx,
	input PluginJobLifecycleInput,
	entry PluginJobLifecyclePlanEntry,
) (int64, error) {
	ledger := PluginJobMigrationLedgerEntry{
		OldJobID: entry.Row.JobID, ExtensionID: entry.Args.ExtensionID, MigrationID: entry.Decision.MigrationID,
		SourceContract: entry.Args.Contract(), SourceTrustGrantID: entry.Args.TrustGrantID,
		TargetContract: entry.Target.Contract, TargetTrustGrantID: entry.Target.TrustGrantID,
	}
	claim, err := tx.ClaimPluginJobMigration(ctx, ledger)
	if err != nil {
		return 0, fmt.Errorf("claim plugin job migration %d: %w", entry.Row.JobID, err)
	}
	if !samePluginJobMigrationLedger(claim.Ledger, ledger) {
		return 0, fmt.Errorf("%w: old job %d", ErrPluginJobMigrationConflict, entry.Row.JobID)
	}
	if claim.Claimed {
		if claim.NewJobID != 0 {
			return 0, fmt.Errorf("%w: newly claimed old job %d is already linked", ErrPluginJobMigrationConflict, entry.Row.JobID)
		}
	} else {
		if claim.NewJobID <= 0 {
			return 0, fmt.Errorf("%w: old job %d", ErrPluginJobMigrationPending, entry.Row.JobID)
		}
		if err := tx.CancelPluginJob(ctx, entry.Row.JobID); err != nil {
			return 0, fmt.Errorf("cancel migrated plugin job %d: %w", entry.Row.JobID, err)
		}
		return claim.NewJobID, nil
	}

	migrator := input.Migrators[entry.Decision.MigrationID]
	if migrator == nil {
		return 0, fmt.Errorf("%w: migration %q", ErrPluginJobMigratorUnavailable, entry.Decision.MigrationID)
	}
	sourcePayload, err := clonePluginJobPayload(entry.Args.Payload)
	if err != nil {
		return 0, fmt.Errorf("clone plugin job %d payload: %w", entry.Row.JobID, err)
	}
	payload, err := migrator.MigratePluginJobPayload(ctx, PluginJobPayloadMigrationInput{
		MigrationID: entry.Decision.MigrationID, Source: entry.Args.Contract(), Target: entry.Target.Contract,
		Payload: sourcePayload,
	})
	if err != nil {
		return 0, fmt.Errorf("migrate plugin job %d payload: %w", entry.Row.JobID, err)
	}
	replacementPayload, err := clonePluginJobPayload(payload)
	if err != nil {
		return 0, fmt.Errorf("clone plugin job %d migrated payload: %w", entry.Row.JobID, err)
	}
	replacement := exactReplacementPluginJobArgs(entry.Args, entry.Target, replacementPayload)
	if !replacement.validEnvelope() || !replacement.Contract().Equal(entry.Target.Contract) {
		return 0, fmt.Errorf("%w: migrator produced invalid replacement for job %d", ErrInvalidRequest, entry.Row.JobID)
	}
	newJobID, err := tx.InsertPluginJob(ctx, replacement, replacementPluginJobInsertOpts(entry.Row))
	if err != nil {
		return 0, fmt.Errorf("insert replacement for plugin job %d: %w", entry.Row.JobID, err)
	}
	if newJobID <= 0 {
		return 0, fmt.Errorf("%w: replacement for job %d has invalid id", ErrInvalidRequest, entry.Row.JobID)
	}
	if err := tx.CompletePluginJobMigration(ctx, ledger, newJobID); err != nil {
		return 0, fmt.Errorf("complete plugin job migration %d: %w", entry.Row.JobID, err)
	}
	if err := tx.CancelPluginJob(ctx, entry.Row.JobID); err != nil {
		return 0, fmt.Errorf("cancel source plugin job %d: %w", entry.Row.JobID, err)
	}
	return newJobID, nil
}

func validatePluginJobLifecycleRuntimes(extensionID string, input PluginJobLifecycleInput) error {
	if input.SourceContracts == nil || input.TargetContracts == nil {
		return fmt.Errorf("%w: explicit source and target contract maps are required", ErrInvalidRequest)
	}
	if err := validatePluginJobRuntimeMap(extensionID, input.SourceContracts, input.SourceRuntimeAvailable); err != nil {
		return fmt.Errorf("source contracts: %w", err)
	}
	if err := validatePluginJobRuntimeMap(extensionID, input.TargetContracts, true); err != nil {
		return fmt.Errorf("target contracts: %w", err)
	}
	return nil
}

func validatePluginJobRuntimeMap(extensionID string, contracts map[string]PluginJobRuntimeContract, requireTrust bool) error {
	for jobName, runtime := range contracts {
		if strings.TrimSpace(jobName) == "" || !runtime.Contract.Valid() || runtime.Contract.JobName != jobName {
			return fmt.Errorf("%w: exact contract for job %q is required", ErrInvalidRequest, jobName)
		}
		if runtime.Contract.ExtensionID != extensionID {
			return fmt.Errorf("%w: job %q belongs to %q", ErrPluginJobLifecycleIsolation, jobName, runtime.Contract.ExtensionID)
		}
		if requireTrust && strings.TrimSpace(runtime.TrustGrantID) == "" {
			return fmt.Errorf("%w: trust grant for job %q is required", ErrInvalidRequest, jobName)
		}
	}
	return nil
}

func cancelPluginJobLifecycleDecision(reason string) supportjobs.PluginJobDecision {
	return supportjobs.PluginJobDecision{Action: supportjobs.PluginJobCancel, Reason: reason}
}

func isFinalizedPluginJobState(state rivertype.JobState) bool {
	return state == rivertype.JobStateCancelled || state == rivertype.JobStateCompleted || state == rivertype.JobStateDiscarded
}

func isKnownPluginJobState(state rivertype.JobState) bool {
	switch state {
	case rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
		rivertype.JobStateRunning, rivertype.JobStateScheduled:
		return true
	default:
		return false
	}
}

func samePluginJobMigrationLedger(left, right PluginJobMigrationLedgerEntry) bool {
	return left.OldJobID == right.OldJobID &&
		left.ExtensionID == right.ExtensionID &&
		left.MigrationID == right.MigrationID &&
		left.SourceContract.Equal(right.SourceContract) &&
		left.SourceTrustGrantID == right.SourceTrustGrantID &&
		left.TargetContract.Equal(right.TargetContract) &&
		left.TargetTrustGrantID == right.TargetTrustGrantID
}

func exactReplacementPluginJobArgs(source PluginJobArgs, target PluginJobRuntimeContract, payload map[string]any) PluginJobArgs {
	return PluginJobArgs{
		EnvelopeVersion: supportjobs.PluginJobEnvelopeVersion,
		ExtensionID:     target.Contract.ExtensionID, ExtensionVersion: target.Contract.ExtensionVersion,
		ArtifactDigest: target.Contract.ArtifactDigest, TrustGrantID: target.TrustGrantID,
		JobName: target.Contract.JobName, JobContractVersion: target.Contract.JobContract,
		PayloadSchemaID: target.Contract.PayloadSchemaID, PayloadSchemaVersion: target.Contract.PayloadSchemaVersion,
		Payload: payload, EnqueuedAt: source.EnqueuedAt,
	}
}

func replacementPluginJobInsertOpts(row PluginJobLifecycleRow) *river.InsertOpts {
	remainingAttempts := row.MaxAttempts - row.Attempt
	if remainingAttempts < 1 {
		remainingAttempts = 1
	}
	priority := row.Priority
	if priority < 1 || priority > 4 {
		priority = 0
	}
	return &river.InsertOpts{
		MaxAttempts: remainingAttempts, Pending: row.State == rivertype.JobStatePending,
		Priority: priority, Queue: row.Queue, ScheduledAt: row.ScheduledAt,
		Tags: append([]string(nil), row.Tags...),
	}
}

func clonePluginJobLifecycleRow(row PluginJobLifecycleRow) PluginJobLifecycleRow {
	row.EncodedArgs = append(json.RawMessage(nil), row.EncodedArgs...)
	row.Tags = append([]string(nil), row.Tags...)
	return row
}

func clonePluginJobPayload(payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}
