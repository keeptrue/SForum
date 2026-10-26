package extensionsruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type lifecycleJobPublicationRef struct {
	OperationID int64
	StepID      string
	Mode        LifecycleBoundaryPublicationMode
	Attempt     int
}

type lifecycleJobPublicationRecord struct {
	ID                  int64
	Fence               lifecyclePublicationFence
	FirstAttempt        int
	LastAttempt         int
	Source              lifecycleJobDesiredSnapshot
	Target              lifecycleJobDesiredSnapshot
	Plan                lifecycleJobReconciliationPlan
	PublicationState    LifecycleBoundaryTransactionState
	ReconciliationState string
	ReconcileAttempt    int
	Evidence            *lifecycleJobReconciliationEvidence
	Revision            int64
}

type lifecycleJobReconciliationEvidence struct {
	Schema           string                                `json:"schema"`
	Operation        extensions.LifecycleMachineOperation  `json:"operation"`
	Mode             LifecycleBoundaryJobMode              `json:"mode"`
	PublicationMode  LifecycleBoundaryPublicationMode      `json:"publicationMode"`
	ExtensionID      string                                `json:"extensionId"`
	Attempt          int                                   `json:"attempt"`
	IgnoredFinalized int                                   `json:"ignoredFinalized"`
	Executions       []lifecycleJobReconciliationExecution `json:"executions"`
}

type lifecycleJobReconciliationExecution struct {
	JobID            int64  `json:"jobId"`
	Action           string `json:"action"`
	Reason           string `json:"reason"`
	MigrationID      string `json:"migrationId,omitempty"`
	ReplacementJobID int64  `json:"replacementJobId,omitempty"`
}

func (b *PostgresLifecycleBoundaryJobs) PrepareLifecycleJobPublication(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) (LifecycleBoundaryTransaction, error) {
	if b == nil || b.pool == nil || ctx == nil {
		return nil, ErrLifecycleBoundaryJobsUnavailable
	}
	if err := b.requirePlanningDependencies(ctx); err != nil {
		return nil, err
	}
	jobMode, err := lifecycleBoundaryJobModeForOperation(request.Operation)
	if err != nil {
		return nil, err
	}
	fence, err := lifecyclePublicationFenceFor(request, mode)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLifecycleBoundaryJobsInvalid, err)
	}
	callFence, err := lifecycleBoundaryCallFenceFor(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLifecycleBoundaryJobsInvalid, err)
	}
	material, err := b.lifecycleJobPublicationMaterial(ctx, request, jobMode, mode)
	if err != nil {
		return nil, err
	}
	sourceJSON, err := encodeLifecycleJobJSON(material.Source)
	if err != nil {
		return nil, err
	}
	targetJSON, err := encodeLifecycleJobJSON(material.Target)
	if err != nil {
		return nil, err
	}
	planJSON, err := encodeLifecycleJobJSON(material.Plan)
	if err != nil {
		return nil, err
	}

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin lifecycle jobs prepare: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateLifecycleBoundaryPostgresFence(ctx, tx, callFence, true); err != nil {
		return nil, err
	}
	marker, err := loadLifecyclePublication(ctx, tx, fence.OperationID, fence.StepID, fence.Mode, true)
	if err != nil || !marker.matches(fence) || marker.LastAttempt != fence.Attempt {
		if err == nil {
			err = ErrLifecycleBoundaryJobsConflict
		}
		return nil, fmt.Errorf("validate lifecycle jobs marker: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO extension_lifecycle_job_publications (
			operation_id, operation, step_id, position, publication_mode,
			source_extension_id, source_extension_version, source_package_digest,
			source_version_id, source_runtime_instance_id,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id,
			first_attempt, last_attempt, runtime_attempts,
			source_snapshot, target_snapshot, reconciliation_plan
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $16,
			jsonb_build_array(jsonb_build_object(
			  'attempt', $16::integer,
			  'sourceRuntimeInstanceId', $10::text,
			  'targetRuntimeInstanceId', $15::text
			)),
			$17::jsonb, $18::jsonb, $19::jsonb
		)
		ON CONFLICT (operation_id, step_id, publication_mode) DO NOTHING
	`, fence.OperationID, fence.Operation, fence.StepID, fence.Position, fence.Mode,
		fence.Source.nullableID(), fence.Source.nullableVersion(), fence.Source.nullableDigest(),
		fence.Source.nullableVersionID(), fence.Source.nullableInstanceID(),
		fence.Target.ExtensionID, fence.Target.ExtensionVersion, fence.Target.PackageDigest,
		fence.Target.VersionID, fence.Target.RuntimeInstanceID, fence.Attempt,
		sourceJSON, targetJSON, planJSON)
	if err != nil {
		return nil, fmt.Errorf("insert lifecycle jobs publication: %w", err)
	}
	record, err := loadLifecycleJobPublication(ctx, tx, fence.OperationID, fence.StepID, fence.Mode, true)
	if err != nil {
		return nil, err
	}
	if !record.matchesOperation(fence, material.Source, material.Target) || fence.Attempt < record.LastAttempt {
		return nil, ErrLifecycleBoundaryJobsConflict
	}
	if marker.CommitMarker && record.PublicationState != LifecycleBoundaryTransactionTarget {
		return nil, ErrLifecycleBoundaryJobsConflict
	}
	if fence.Attempt > record.LastAttempt || !record.Fence.Source.sameArtifact(fence.Source) ||
		!record.Fence.Target.sameArtifact(fence.Target) || record.Fence.Source.RuntimeInstanceID != fence.Source.RuntimeInstanceID ||
		record.Fence.Target.RuntimeInstanceID != fence.Target.RuntimeInstanceID {
		tag, updateErr := tx.Exec(ctx, `
			UPDATE extension_lifecycle_job_publications
			SET last_attempt = $2,
			    source_runtime_instance_id = $5,
			    target_runtime_instance_id = $6,
			    source_snapshot = $7::jsonb,
			    target_snapshot = $8::jsonb,
			    runtime_attempts = runtime_attempts || jsonb_build_array(jsonb_build_object(
			      'attempt', $2::integer,
			      'sourceRuntimeInstanceId', $5::text,
			      'targetRuntimeInstanceId', $6::text
			    )),
			    revision = revision + 1,
			    updated_at = statement_timestamp()
			WHERE id = $1 AND revision = $3 AND last_attempt = $4
		`, record.ID, fence.Attempt, record.Revision, record.LastAttempt,
			fence.Source.nullableInstanceID(), fence.Target.RuntimeInstanceID, sourceJSON, targetJSON)
		if updateErr != nil {
			return nil, fmt.Errorf("rebind lifecycle jobs publication: %w", updateErr)
		}
		if tag.RowsAffected() != 1 {
			return nil, ErrLifecycleBoundaryJobsConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit lifecycle jobs prepare: %w", err)
	}
	return &postgresLifecycleJobTransaction{
		jobs:  b,
		ref:   lifecycleJobPublicationRef{OperationID: fence.OperationID, StepID: fence.StepID, Mode: fence.Mode, Attempt: fence.Attempt},
		fence: fence, source: material.Source, target: material.Target,
	}, nil
}

type postgresLifecycleJobTransaction struct {
	jobs   *PostgresLifecycleBoundaryJobs
	ref    lifecycleJobPublicationRef
	fence  lifecyclePublicationFence
	source lifecycleJobDesiredSnapshot
	target lifecycleJobDesiredSnapshot
}

func (t *postgresLifecycleJobTransaction) Inspect(ctx context.Context) (LifecycleBoundaryTransactionState, error) {
	record, err := t.load(ctx, false)
	if err != nil {
		return "", err
	}
	committed, err := t.markerCommitted(ctx)
	if err != nil {
		return "", err
	}
	if committed && record.PublicationState != LifecycleBoundaryTransactionTarget {
		return "", ErrLifecycleBoundaryJobsConflict
	}
	return record.PublicationState, nil
}

func (t *postgresLifecycleJobTransaction) Publish(ctx context.Context) error {
	return t.transition(ctx, LifecycleBoundaryTransactionTarget, false)
}

func (t *postgresLifecycleJobTransaction) Restore(ctx context.Context) error {
	return t.transition(ctx, LifecycleBoundaryTransactionSource, true)
}

func (t *postgresLifecycleJobTransaction) transition(
	ctx context.Context,
	target LifecycleBoundaryTransactionState,
	requireUncommitted bool,
) error {
	if t == nil || t.jobs == nil || t.jobs.pool == nil || ctx == nil {
		return ErrLifecycleBoundaryJobsUnavailable
	}
	tx, err := t.jobs.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	record, err := loadLifecycleJobPublication(ctx, tx, t.ref.OperationID, t.ref.StepID, t.ref.Mode, true)
	if err != nil || !t.matches(record) {
		if err == nil {
			err = ErrLifecycleBoundaryJobsConflict
		}
		return err
	}
	marker, err := loadLifecyclePublication(ctx, tx, t.ref.OperationID, t.ref.StepID, t.ref.Mode, true)
	if err != nil || !marker.matches(t.fence) || marker.LastAttempt != t.ref.Attempt {
		if err == nil {
			err = ErrLifecycleBoundaryJobsConflict
		}
		return err
	}
	if requireUncommitted && marker.CommitMarker {
		return ErrLifecycleBoundaryJobsCommitted
	}
	if record.PublicationState == target {
		return tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE extension_lifecycle_job_publications
		SET publication_state = $2,
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1 AND revision = $3 AND last_attempt = $4
		  AND publication_state = $5
	`, record.ID, target, record.Revision, t.ref.Attempt, record.PublicationState)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLifecycleBoundaryJobsConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit lifecycle jobs %s: %w", target, err)
	}
	return nil
}

func (t *postgresLifecycleJobTransaction) load(ctx context.Context, lock bool) (lifecycleJobPublicationRecord, error) {
	if t == nil || t.jobs == nil || t.jobs.pool == nil || ctx == nil {
		return lifecycleJobPublicationRecord{}, ErrLifecycleBoundaryJobsUnavailable
	}
	record, err := loadLifecycleJobPublication(ctx, t.jobs.pool, t.ref.OperationID, t.ref.StepID, t.ref.Mode, lock)
	if err != nil || !t.matches(record) {
		if err == nil {
			err = ErrLifecycleBoundaryJobsConflict
		}
		return lifecycleJobPublicationRecord{}, err
	}
	return record, nil
}

func (t *postgresLifecycleJobTransaction) matches(record lifecycleJobPublicationRecord) bool {
	return record.Fence == t.fence && record.LastAttempt == t.ref.Attempt &&
		reflect.DeepEqual(record.Source, t.source) && reflect.DeepEqual(record.Target, t.target)
}

func (t *postgresLifecycleJobTransaction) markerCommitted(ctx context.Context) (bool, error) {
	if t == nil || t.jobs == nil || t.jobs.pool == nil {
		return false, ErrLifecycleBoundaryJobsUnavailable
	}
	marker, err := loadLifecyclePublication(
		ctx, t.jobs.pool, t.ref.OperationID, t.ref.StepID, t.ref.Mode, false,
	)
	if err != nil {
		return false, err
	}
	if !marker.matches(t.fence) || marker.LastAttempt != t.ref.Attempt {
		return false, ErrLifecycleBoundaryJobsConflict
	}
	return marker.CommitMarker, nil
}

func (b *PostgresLifecycleBoundaryJobs) ReconcileCommittedLifecycleJobs(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	publicationMode LifecycleBoundaryPublicationMode,
) error {
	if b == nil || b.pool == nil || b.journal == nil || ctx == nil {
		return ErrLifecycleBoundaryJobsUnavailable
	}
	if request.ActorUserID <= 0 || request.AuditEventID <= 0 {
		return ErrLifecycleBoundaryJobsInvalid
	}
	if err := b.requirePlanningDependencies(ctx); err != nil {
		return err
	}
	fence, err := lifecyclePublicationFenceFor(request, publicationMode)
	if err != nil || validateLifecycleJobMode(request.Operation, mode, publicationMode) != nil {
		return ErrLifecycleBoundaryJobsInvalid
	}
	callFence, err := lifecycleBoundaryCallFenceFor(request)
	if err != nil {
		return ErrLifecycleBoundaryJobsInvalid
	}
	committed, err := b.journal.LifecyclePublicationCommitted(ctx, request, publicationMode)
	if err != nil {
		return err
	}
	if !committed {
		return ErrLifecycleBoundaryJobsUncommitted
	}
	material, err := b.lifecycleJobPublicationMaterial(ctx, request, mode, publicationMode)
	if err != nil {
		return err
	}

	lockKey := lifecycleJobAdvisoryLockKey(request.TargetExtension.ID)
	connection, err := acquireLifecycleJobAdvisoryLock(ctx, b.pool, lockKey)
	if err != nil {
		return err
	}
	defer releaseLifecycleJobAdvisoryLock(connection, lockKey)
	if err := validateLifecycleJobPostgresCallFence(ctx, b.pool, callFence); err != nil {
		return err
	}

	record, err := loadLifecycleJobPublication(ctx, b.pool, fence.OperationID, fence.StepID, fence.Mode, false)
	if err != nil || !record.matchesOperation(fence, material.Source, material.Target) ||
		record.LastAttempt != fence.Attempt || record.PublicationState != LifecycleBoundaryTransactionTarget {
		if err == nil {
			err = ErrLifecycleBoundaryJobsConflict
		}
		return err
	}
	if err := validateLifecycleJobReconciliationPlan(request, mode, publicationMode, record.Plan); err != nil {
		return err
	}
	if record.ReconciliationState == "succeeded" {
		return validateLifecycleJobPersistedEvidence(request, mode, publicationMode, record)
	}
	attempt := record.ReconcileAttempt + 1
	tag, err := b.pool.Exec(ctx, `
		UPDATE extension_lifecycle_job_publications
		SET reconciliation_state = 'running',
		    reconciliation_attempt = $2,
		    reconciliation_result = NULL,
		    reconciliation_error = '',
		    reconciled_by_user_id = $3,
		    reconciliation_audit_event_id = $4,
		    reconciled_at = NULL,
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1 AND revision = $5 AND publication_state = 'target'
		  AND reconciliation_state <> 'succeeded'
	`, record.ID, attempt, request.ActorUserID, request.AuditEventID, record.Revision)
	if err != nil || tag.RowsAffected() != 1 {
		if err == nil {
			err = ErrLifecycleBoundaryJobsConflict
		}
		return err
	}

	result, reconcileErr := b.coordinator.ReconcileExpected(
		ctx, material.Input, lifecycleJobExpectedPlan(record.Plan),
	)
	if reconcileErr != nil {
		b.persistLifecycleJobReconciliationFailure(request, fence, attempt, reconcileErr)
		return reconcileErr
	}
	if !result.Committed {
		reconcileErr = ErrLifecycleBoundaryJobsConflict
		b.persistLifecycleJobReconciliationFailure(request, fence, attempt, reconcileErr)
		return reconcileErr
	}
	evidence := sanitizeLifecycleJobReconciliationResult(request, mode, publicationMode, attempt, record.Plan, result)
	if err := validateLifecycleJobEvidence(record.Plan, evidence); err != nil {
		b.persistLifecycleJobReconciliationFailure(request, fence, attempt, err)
		return err
	}
	document, err := encodeLifecycleJobJSON(evidence)
	if err != nil {
		return err
	}
	tag, err = b.pool.Exec(ctx, `
		UPDATE extension_lifecycle_job_publications
		SET reconciliation_state = 'succeeded',
		    reconciliation_result = $5::jsonb,
		    reconciliation_error = '',
		    reconciled_by_user_id = $6,
		    reconciliation_audit_event_id = $7,
		    reconciled_at = statement_timestamp(),
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = $3
		  AND last_attempt = $4 AND reconciliation_state = 'running'
		  AND reconciliation_attempt = $8
	`, fence.OperationID, fence.StepID, fence.Mode, fence.Attempt, document,
		request.ActorUserID, request.AuditEventID, attempt)
	if err != nil || tag.RowsAffected() != 1 {
		if err == nil {
			err = ErrLifecycleBoundaryJobsConflict
		}
		return err
	}
	return nil
}

func (b *PostgresLifecycleBoundaryJobs) persistLifecycleJobReconciliationFailure(
	request LifecycleBoundaryRequest,
	fence lifecyclePublicationFence,
	attempt int,
	cause error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	message := lifecycleJobReconciliationFailureCode(cause)
	_, _ = b.pool.Exec(ctx, `
		UPDATE extension_lifecycle_job_publications
		SET reconciliation_state = 'failed',
		    reconciliation_error = $5,
		    reconciled_by_user_id = $6,
		    reconciliation_audit_event_id = $7,
		    reconciled_at = NULL,
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = $3
		  AND last_attempt = $4 AND reconciliation_state = 'running'
		  AND reconciliation_attempt = $8
	`, fence.OperationID, fence.StepID, fence.Mode, fence.Attempt, message,
		request.ActorUserID, request.AuditEventID, attempt)
}

func (b *PostgresLifecycleBoundaryJobs) authorizeLifecycleJobResume(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
	role extensions.LifecycleCoordinatorRuntimeRole,
) error {
	if b.journal == nil || b.pool == nil {
		return ErrLifecycleBoundaryJobsUnavailable
	}
	canonical, canonicalMode, err := lifecycleBoundaryCanonicalPublication(request)
	if err != nil || canonicalMode != mode {
		return ErrLifecycleBoundaryJobsInvalid
	}
	switch role {
	case extensions.LifecycleRuntimeSource:
		committed, err := b.journal.LifecyclePublicationCommittedForOperation(ctx, canonical, mode)
		if err != nil {
			return err
		}
		if committed {
			return ErrLifecycleBoundaryJobsCommitted
		}
		var publicationState string
		err = b.pool.QueryRow(ctx, `
			SELECT publication_state
			FROM extension_lifecycle_job_publications
			WHERE operation_id = $1 AND step_id = $2 AND publication_mode = $3
		`, canonical.OperationID, canonical.StepID, mode).Scan(&publicationState)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if publicationState != string(LifecycleBoundaryTransactionSource) {
			return ErrLifecycleBoundaryJobsConflict
		}
		return nil
	case extensions.LifecycleRuntimeTarget:
		committed, err := b.journal.LifecyclePublicationCommitted(ctx, canonical, mode)
		if err != nil {
			return err
		}
		if !committed {
			return ErrLifecycleBoundaryJobsUncommitted
		}
		fence, err := lifecyclePublicationFenceFor(canonical, mode)
		if err != nil {
			return ErrLifecycleBoundaryJobsInvalid
		}
		record, err := loadLifecycleJobPublication(
			ctx, b.pool, canonical.OperationID, canonical.StepID, mode, false,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLifecycleBoundaryJobsUncommitted
		}
		if err != nil {
			return err
		}
		if record.Fence != fence || record.LastAttempt != fence.Attempt ||
			record.PublicationState != LifecycleBoundaryTransactionTarget || record.ReconciliationState != "succeeded" {
			return ErrLifecycleBoundaryJobsUncommitted
		}
		jobMode, err := lifecycleBoundaryJobModeForOperation(request.Operation)
		if err != nil {
			return ErrLifecycleBoundaryJobsInvalid
		}
		if err := validateLifecycleJobReconciliationPlan(canonical, jobMode, mode, record.Plan); err != nil {
			return err
		}
		return validateLifecycleJobPersistedEvidence(canonical, jobMode, mode, record)
	default:
		return ErrLifecycleBoundaryJobsInvalid
	}
}

func sanitizeLifecycleJobReconciliationResult(
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	publicationMode LifecycleBoundaryPublicationMode,
	attempt int,
	plan lifecycleJobReconciliationPlan,
	result hostapi.PluginJobLifecycleResult,
) lifecycleJobReconciliationEvidence {
	evidence := lifecycleJobReconciliationEvidence{
		Schema: "sforum.lifecycle.job-reconciliation@1", Operation: request.Operation,
		Mode: mode, PublicationMode: publicationMode, ExtensionID: request.TargetExtension.ID,
		Attempt: attempt, IgnoredFinalized: plan.IgnoredFinalized,
		Executions: make([]lifecycleJobReconciliationExecution, 0, len(result.Executions)),
	}
	for _, execution := range result.Executions {
		evidence.Executions = append(evidence.Executions, lifecycleJobReconciliationExecution{
			JobID: execution.JobID, Action: string(execution.Action), Reason: execution.Reason,
			MigrationID: execution.MigrationID, ReplacementJobID: execution.ReplacementJobID,
		})
	}
	return evidence
}

func validateLifecycleJobEvidence(plan lifecycleJobReconciliationPlan, evidence lifecycleJobReconciliationEvidence) error {
	planned := make(map[int64]lifecycleJobReconciliationPlanEntry, len(plan.Entries))
	for _, entry := range plan.Entries {
		if _, duplicate := planned[entry.JobID]; duplicate {
			return fmt.Errorf("%w: duplicate durable decision for job %d", ErrLifecycleBoundaryJobsConflict, entry.JobID)
		}
		planned[entry.JobID] = entry
	}
	if evidence.IgnoredFinalized != plan.IgnoredFinalized || len(evidence.Executions) != len(plan.Entries) {
		return fmt.Errorf("%w: reconciliation evidence cardinality drifted", ErrLifecycleBoundaryJobsConflict)
	}
	seen := make(map[int64]struct{}, len(evidence.Executions))
	for _, execution := range evidence.Executions {
		if _, duplicate := seen[execution.JobID]; duplicate {
			return fmt.Errorf("%w: duplicate reconciliation evidence for job %d", ErrLifecycleBoundaryJobsConflict, execution.JobID)
		}
		seen[execution.JobID] = struct{}{}
		entry, ok := planned[execution.JobID]
		if !ok || string(entry.Action) != execution.Action || entry.Reason != execution.Reason || entry.MigrationID != execution.MigrationID {
			return fmt.Errorf("%w: reconciliation decision drifted for job %d", ErrLifecycleBoundaryJobsConflict, execution.JobID)
		}
		if execution.Action == "migrate" && execution.ReplacementJobID <= 0 {
			return fmt.Errorf("%w: migration for job %d has no replacement", ErrLifecycleBoundaryJobsConflict, execution.JobID)
		}
		if execution.Action != "migrate" && execution.ReplacementJobID != 0 {
			return fmt.Errorf("%w: non-migration job %d has a replacement", ErrLifecycleBoundaryJobsConflict, execution.JobID)
		}
	}
	for jobID := range planned {
		if _, ok := seen[jobID]; !ok {
			return fmt.Errorf("%w: missing reconciliation evidence for job %d", ErrLifecycleBoundaryJobsConflict, jobID)
		}
	}
	return nil
}

func validateLifecycleJobReconciliationPlan(
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	publicationMode LifecycleBoundaryPublicationMode,
	plan lifecycleJobReconciliationPlan,
) error {
	if plan.Schema != "sforum.lifecycle.job-reconciliation-plan@1" || plan.Operation != request.Operation ||
		plan.Mode != mode || plan.PublicationMode != publicationMode ||
		plan.ExtensionID != request.TargetExtension.ID || plan.IgnoredFinalized < 0 {
		return fmt.Errorf("%w: durable reconciliation plan identity drifted", ErrLifecycleBoundaryJobsConflict)
	}
	seen := make(map[int64]struct{}, len(plan.Entries))
	for _, entry := range plan.Entries {
		if entry.JobID <= 0 || entry.Reason == "" || entry.Reason != strings.TrimSpace(entry.Reason) ||
			len(entry.Reason) > 128 || !knownLifecycleJobPlanAction(entry.Action) {
			return fmt.Errorf("%w: invalid durable decision for job %d", ErrLifecycleBoundaryJobsConflict, entry.JobID)
		}
		if _, duplicate := seen[entry.JobID]; duplicate {
			return fmt.Errorf("%w: duplicate durable decision for job %d", ErrLifecycleBoundaryJobsConflict, entry.JobID)
		}
		seen[entry.JobID] = struct{}{}
		if entry.Action == supportjobs.PluginJobMigrate {
			if entry.MigrationID == "" || entry.MigrationID != strings.TrimSpace(entry.MigrationID) || len(entry.MigrationID) > 512 {
				return fmt.Errorf("%w: migration decision for job %d has no exact migration", ErrLifecycleBoundaryJobsConflict, entry.JobID)
			}
		} else if entry.MigrationID != "" {
			return fmt.Errorf("%w: non-migration decision for job %d carries a migration", ErrLifecycleBoundaryJobsConflict, entry.JobID)
		}
	}
	return nil
}

func knownLifecycleJobPlanAction(action supportjobs.PluginJobAction) bool {
	switch action {
	case supportjobs.PluginJobExecute, supportjobs.PluginJobDrain,
		supportjobs.PluginJobMigrate, supportjobs.PluginJobCancel:
		return true
	default:
		return false
	}
}

func validateLifecycleJobPersistedEvidence(
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryJobMode,
	publicationMode LifecycleBoundaryPublicationMode,
	record lifecycleJobPublicationRecord,
) error {
	if record.Evidence == nil || record.Evidence.Schema != "sforum.lifecycle.job-reconciliation@1" ||
		record.Evidence.Operation != request.Operation || record.Evidence.Mode != mode ||
		record.Evidence.PublicationMode != publicationMode ||
		record.Evidence.ExtensionID != request.TargetExtension.ID ||
		record.Evidence.Attempt != record.ReconcileAttempt || record.Evidence.Attempt <= 0 {
		return fmt.Errorf("%w: persisted reconciliation evidence identity drifted", ErrLifecycleBoundaryJobsConflict)
	}
	return validateLifecycleJobEvidence(record.Plan, *record.Evidence)
}

func lifecycleJobExpectedPlan(plan lifecycleJobReconciliationPlan) hostapi.PluginJobLifecycleExpectedPlan {
	expected := hostapi.PluginJobLifecycleExpectedPlan{
		ExtensionID: plan.ExtensionID,
		Entries:     make([]hostapi.PluginJobLifecycleExpectedDecision, 0, len(plan.Entries)),
	}
	for _, entry := range plan.Entries {
		expected.Entries = append(expected.Entries, hostapi.PluginJobLifecycleExpectedDecision{
			JobID: entry.JobID, Action: entry.Action, Reason: entry.Reason, MigrationID: entry.MigrationID,
		})
	}
	return expected
}

func lifecycleJobReconciliationFailureCode(cause error) string {
	switch {
	case errors.Is(cause, hostapi.ErrPluginJobLifecyclePlanDrift):
		return "plugin_job.plan_drift"
	case errors.Is(cause, hostapi.ErrPluginJobMigrationConflict), errors.Is(cause, hostapi.ErrPluginJobMigrationPending):
		return "plugin_job.migration_conflict"
	case errors.Is(cause, hostapi.ErrPluginJobMigratorUnavailable):
		return "plugin_job.migrator_unavailable"
	case errors.Is(cause, context.Canceled):
		return "plugin_job.reconciliation_cancelled"
	case errors.Is(cause, context.DeadlineExceeded):
		return "plugin_job.reconciliation_deadline"
	default:
		return "plugin_job.reconciliation_failed"
	}
}

func (r lifecycleJobPublicationRecord) matchesOperation(
	fence lifecyclePublicationFence,
	source lifecycleJobDesiredSnapshot,
	target lifecycleJobDesiredSnapshot,
) bool {
	return r.Fence.OperationID == fence.OperationID && r.Fence.Operation == fence.Operation &&
		r.Fence.StepID == fence.StepID && r.Fence.Position == fence.Position && r.Fence.Mode == fence.Mode &&
		r.Fence.Source.sameArtifact(fence.Source) && r.Fence.Target.sameArtifact(fence.Target) &&
		sameLifecycleJobSnapshotContract(r.Source, source) && sameLifecycleJobSnapshotContract(r.Target, target)
}

func sameLifecycleJobSnapshotContract(left, right lifecycleJobDesiredSnapshot) bool {
	left.Artifact.RuntimeInstanceID = ""
	right.Artifact.RuntimeInstanceID = ""
	return reflect.DeepEqual(left, right)
}

type lifecycleJobPublicationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadLifecycleJobPublication(
	ctx context.Context,
	querier lifecycleJobPublicationQuerier,
	operationID int64,
	stepID string,
	mode LifecycleBoundaryPublicationMode,
	lock bool,
) (lifecycleJobPublicationRecord, error) {
	query := `
		SELECT id, operation, position,
		       source_extension_id, source_extension_version, source_package_digest,
		       source_version_id, source_runtime_instance_id,
		       target_extension_id, target_extension_version, target_package_digest,
		       target_version_id, target_runtime_instance_id,
		       first_attempt, last_attempt, source_snapshot, target_snapshot,
		       reconciliation_plan, publication_state, reconciliation_state,
		       reconciliation_attempt, reconciliation_result, revision
		FROM extension_lifecycle_job_publications
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = $3`
	if lock {
		query += " FOR UPDATE"
	}
	var record lifecycleJobPublicationRecord
	var sourceID, sourceVersion, sourceDigest, sourceRuntime sql.NullString
	var sourceVersionID sql.NullInt64
	var sourceJSON, targetJSON, planJSON, evidenceJSON []byte
	var operation string
	var publicationState string
	err := querier.QueryRow(ctx, query, operationID, stepID, mode).Scan(
		&record.ID, &operation, &record.Fence.Position,
		&sourceID, &sourceVersion, &sourceDigest, &sourceVersionID, &sourceRuntime,
		&record.Fence.Target.ExtensionID, &record.Fence.Target.ExtensionVersion,
		&record.Fence.Target.PackageDigest, &record.Fence.Target.VersionID,
		&record.Fence.Target.RuntimeInstanceID, &record.FirstAttempt, &record.LastAttempt,
		&sourceJSON, &targetJSON, &planJSON, &publicationState,
		&record.ReconciliationState, &record.ReconcileAttempt, &evidenceJSON, &record.Revision,
	)
	if err != nil {
		return lifecycleJobPublicationRecord{}, err
	}
	record.Fence.OperationID = operationID
	record.Fence.Operation = extensions.LifecycleMachineOperation(operation)
	record.Fence.StepID = stepID
	record.Fence.Mode = mode
	record.Fence.Attempt = record.LastAttempt
	record.Fence.Target.Present = true
	if sourceID.Valid {
		record.Fence.Source = lifecyclePublicationArtifact{
			ExtensionID: sourceID.String, ExtensionVersion: sourceVersion.String,
			PackageDigest: sourceDigest.String, VersionID: sourceVersionID.Int64,
			RuntimeInstanceID: sourceRuntime.String, Present: true,
		}
	}
	if publicationState != string(LifecycleBoundaryTransactionSource) && publicationState != string(LifecycleBoundaryTransactionTarget) {
		return lifecycleJobPublicationRecord{}, ErrLifecycleBoundaryJobsConflict
	}
	record.PublicationState = LifecycleBoundaryTransactionState(publicationState)
	if err := json.Unmarshal(sourceJSON, &record.Source); err != nil {
		return lifecycleJobPublicationRecord{}, err
	}
	if err := json.Unmarshal(targetJSON, &record.Target); err != nil {
		return lifecycleJobPublicationRecord{}, err
	}
	if err := json.Unmarshal(planJSON, &record.Plan); err != nil {
		return lifecycleJobPublicationRecord{}, err
	}
	if len(evidenceJSON) > 0 {
		var evidence lifecycleJobReconciliationEvidence
		if err := json.Unmarshal(evidenceJSON, &evidence); err != nil {
			return lifecycleJobPublicationRecord{}, err
		}
		record.Evidence = &evidence
	}
	return record, nil
}

func lifecycleJobAdvisoryLockKey(extensionID string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte("sforum:lifecycle-jobs:" + extensionID))
	return int64(hash.Sum64())
}

func acquireLifecycleJobAdvisoryLock(
	ctx context.Context,
	pool *pgxpool.Pool,
	key int64,
) (*pgxpool.Conn, error) {
	for {
		connection, err := pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var acquired bool
		err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired)
		if err != nil {
			discardLifecycleJobConnection(connection)
			return nil, err
		}
		if acquired {
			return connection, nil
		}
		connection.Release()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func releaseLifecycleJobAdvisoryLock(connection *pgxpool.Conn, key int64) {
	if connection == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var unlocked bool
	err := connection.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, key).Scan(&unlocked)
	cancel()
	if err == nil && unlocked {
		connection.Release()
		return
	}

	// Session advisory locks survive transaction rollback. If unlock is not
	// proven, discard the physical connection instead of poisoning the pool.
	discardLifecycleJobConnection(connection)
}

func discardLifecycleJobConnection(connection *pgxpool.Conn) {
	if connection == nil {
		return
	}
	raw := connection.Hijack()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = raw.Close(closeCtx)
	closeCancel()
}

func validateLifecycleJobPostgresCallFence(
	ctx context.Context,
	pool *pgxpool.Pool,
	fence lifecycleBoundaryCallFence,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin lifecycle jobs call fence: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateLifecycleBoundaryPostgresFence(ctx, tx, fence, true); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit lifecycle jobs call fence: %w", err)
	}
	return nil
}

var _ LifecycleBoundaryTransaction = (*postgresLifecycleJobTransaction)(nil)
