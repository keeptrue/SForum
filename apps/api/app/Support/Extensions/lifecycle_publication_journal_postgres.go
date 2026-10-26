package extensionsruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var (
	ErrLifecyclePublicationJournalInvalid     = errors.New("extension lifecycle publication journal input is invalid")
	ErrLifecyclePublicationJournalNotPrepared = errors.New("extension lifecycle publication is not prepared")
	ErrLifecyclePublicationJournalConflict    = errors.New("extension lifecycle publication exact fence conflict")
)

// PostgresLifecycleBoundaryPublicationJournal is the durable decision point
// for crash recovery. It records only exact artifact identities; extension rows
// and mutable manifests are deliberately not part of the retained authority.
type PostgresLifecycleBoundaryPublicationJournal struct {
	pool *pgxpool.Pool
}

func NewPostgresLifecycleBoundaryPublicationJournal(pool *pgxpool.Pool) *PostgresLifecycleBoundaryPublicationJournal {
	return &PostgresLifecycleBoundaryPublicationJournal{pool: pool}
}

func (j *PostgresLifecycleBoundaryPublicationJournal) PrepareLifecyclePublication(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) error {
	fence, err := lifecyclePublicationFenceFor(request, mode)
	if err != nil {
		return err
	}
	if j == nil || j.pool == nil || ctx == nil {
		return ErrLifecyclePublicationJournalInvalid
	}

	tx, err := j.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin lifecycle publication prepare: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateLifecyclePublicationOperation(ctx, tx, fence); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO extension_lifecycle_publications (
			operation_id, operation, step_id, position, publication_mode,
			source_extension_id, source_extension_version, source_package_digest,
			source_version_id, source_runtime_instance_id,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id,
			first_attempt, last_attempt, runtime_attempts
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $16,
			jsonb_build_array(jsonb_build_object(
				'attempt', $16::integer,
				'sourceRuntimeInstanceId', $10::text,
				'targetRuntimeInstanceId', $15::text
			))
		)
		ON CONFLICT (operation_id, step_id, publication_mode) DO NOTHING
	`, fence.OperationID, fence.Operation, fence.StepID, fence.Position, fence.Mode,
		fence.Source.nullableID(), fence.Source.nullableVersion(), fence.Source.nullableDigest(),
		fence.Source.nullableVersionID(), fence.Source.nullableInstanceID(),
		fence.Target.ExtensionID, fence.Target.ExtensionVersion, fence.Target.PackageDigest,
		fence.Target.VersionID, fence.Target.RuntimeInstanceID, fence.Attempt)
	if err != nil {
		return fmt.Errorf("insert lifecycle publication fence: %w", err)
	}

	record, err := loadLifecyclePublication(ctx, tx, fence.OperationID, fence.StepID, fence.Mode, true)
	if err != nil {
		return mapLifecyclePublicationLoadError("load prepared lifecycle publication", err)
	}
	if !record.matchesOperation(fence) || fence.Attempt < record.LastAttempt {
		return ErrLifecyclePublicationJournalConflict
	}
	if fence.Attempt > record.LastAttempt || !record.matches(fence) {
		tag, updateErr := tx.Exec(ctx, `
			UPDATE extension_lifecycle_publications
			SET last_attempt = $2,
			    source_runtime_instance_id = $5,
			    target_runtime_instance_id = $6,
			    runtime_attempts = runtime_attempts || jsonb_build_array(jsonb_build_object(
			      'attempt', $2::integer,
			      'sourceRuntimeInstanceId', $5::text,
			      'targetRuntimeInstanceId', $6::text
			    )),
			    revision = revision + 1,
			    updated_at = statement_timestamp()
			WHERE id = $1 AND revision = $3 AND last_attempt = $4
		`, record.ID, fence.Attempt, record.Revision, record.LastAttempt,
			fence.Source.nullableInstanceID(), fence.Target.RuntimeInstanceID)
		if updateErr != nil {
			return fmt.Errorf("advance lifecycle publication attempt: %w", updateErr)
		}
		if tag.RowsAffected() != 1 {
			return ErrLifecyclePublicationJournalConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit lifecycle publication prepare: %w", err)
	}
	return nil
}

func (j *PostgresLifecycleBoundaryPublicationJournal) LifecyclePublicationCommitted(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) (bool, error) {
	fence, err := lifecyclePublicationFenceFor(request, mode)
	if err != nil {
		return false, err
	}
	if j == nil || j.pool == nil || ctx == nil {
		return false, ErrLifecyclePublicationJournalInvalid
	}
	record, err := loadLifecyclePublication(ctx, j.pool, fence.OperationID, fence.StepID, fence.Mode, false)
	return lifecyclePublicationCommittedResult(record, err, fence, true)
}

// LifecyclePublicationCommittedForOperation is the read-only recovery query
// used by an earlier source-drain step. Gate attempts are per-step, so this
// query deliberately ignores Attempt while retaining every logical and exact-
// artifact fence. It never prepares or advances the publication row.
func (j *PostgresLifecycleBoundaryPublicationJournal) LifecyclePublicationCommittedForOperation(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) (bool, error) {
	fence, err := lifecyclePublicationFenceFor(request, mode)
	if err != nil {
		return false, err
	}
	if j == nil || j.pool == nil || ctx == nil {
		return false, ErrLifecyclePublicationJournalInvalid
	}
	record, err := loadLifecyclePublication(ctx, j.pool, fence.OperationID, fence.StepID, fence.Mode, false)
	return lifecyclePublicationCommittedResult(record, err, fence, false)
}

func lifecyclePublicationCommittedResult(
	record lifecyclePublicationRecord,
	err error,
	fence lifecyclePublicationFence,
	requireAttempt bool,
) (bool, error) {
	// Early source-drain compensation inspects before the publication step has
	// prepared its marker. Absence means publication has not committed yet.
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, mapLifecyclePublicationLoadError("inspect lifecycle publication", err)
	}
	matches := record.matches(fence)
	if !requireAttempt {
		// Runtime instance ids are process-local. A Host restart may rebuild the
		// same immutable artifact under a new instance id, but it cannot change
		// the durable decision about which artifact publication committed.
		matches = record.matchesOperation(fence)
	}
	if !matches || (requireAttempt && record.LastAttempt != fence.Attempt) {
		return false, ErrLifecyclePublicationJournalConflict
	}
	return record.CommitMarker, nil
}

func (j *PostgresLifecycleBoundaryPublicationJournal) CommitLifecyclePublication(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) error {
	fence, err := lifecyclePublicationFenceFor(request, mode)
	if err != nil {
		return err
	}
	if j == nil || j.pool == nil || ctx == nil {
		return ErrLifecyclePublicationJournalInvalid
	}
	tx, err := j.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin lifecycle publication commit: %w", err)
	}
	defer tx.Rollback(ctx)
	record, err := loadLifecyclePublication(ctx, tx, fence.OperationID, fence.StepID, fence.Mode, true)
	if err != nil {
		return mapLifecyclePublicationLoadError("load lifecycle publication for commit", err)
	}
	if !record.matches(fence) || record.LastAttempt != fence.Attempt {
		return ErrLifecyclePublicationJournalConflict
	}
	if record.CommitMarker {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit existing lifecycle publication marker: %w", err)
		}
		return nil
	}
	if err := requireLifecycleMigrationPublicationProof(ctx, tx, request, mode); err != nil {
		return err
	}
	if err := requireLifecyclePublicationLiveTrust(ctx, tx, fence); err != nil {
		return err
	}
	transition, err := lifecyclePluginRuntimePublicationTransition(request, mode)
	if err != nil {
		return err
	}
	publication, err := extensions.PublishPluginRuntimePublicationTransitionTx(ctx, tx, transition)
	if err != nil {
		return fmt.Errorf("publish lifecycle plugin runtime revision: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE extension_lifecycle_publications
		SET commit_marker = TRUE,
		    committed_attempt = $2,
		    plugin_runtime_publication_revision = $4,
		    committed_at = statement_timestamp(),
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1
		  AND revision = $3
		  AND last_attempt = $2
		  AND commit_marker = FALSE
		  AND plugin_runtime_publication_revision IS NULL
	`, record.ID, fence.Attempt, record.Revision, publication.Revision)
	if err != nil {
		return fmt.Errorf("write lifecycle publication commit marker: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLifecyclePublicationJournalConflict
	}
	// A connection failure here is intentionally not translated to conflict:
	// the caller must inspect the durable marker with a fresh context.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit lifecycle publication marker: %w", err)
	}
	return nil
}

func requireLifecyclePublicationLiveTrust(
	ctx context.Context,
	tx pgx.Tx,
	fence lifecyclePublicationFence,
) error {
	if fence.Mode != LifecycleBoundaryActivate {
		return nil
	}
	var authorityType string
	var trustGrantID int64
	if err := tx.QueryRow(ctx, `
		SELECT authority_type, COALESCE(trust_grant_id, 0)
		FROM extension_lifecycle_operations
		WHERE id = $1 AND extension_id = $2 AND extension_version = $3
		  AND package_digest = $4
		FOR KEY SHARE
	`, fence.OperationID, fence.Target.ExtensionID, fence.Target.ExtensionVersion, fence.Target.PackageDigest).Scan(
		&authorityType, &trustGrantID,
	); errors.Is(err, pgx.ErrNoRows) {
		return ErrLifecyclePublicationJournalConflict
	} else if err != nil {
		return fmt.Errorf("load lifecycle publication trust authority: %w", err)
	}
	switch authorityType {
	case extensions.LifecycleAuthorityBuiltin:
		return nil
	case extensions.LifecycleAuthorityTrustGrant:
		if err := extensions.RequireLiveExecutableTrustGrantTx(
			ctx, tx, trustGrantID,
			fence.Target.ExtensionID, fence.Target.ExtensionVersion, fence.Target.PackageDigest,
		); err != nil {
			return fmt.Errorf("%w: live executable trust: %v", ErrLifecyclePublicationJournalConflict, err)
		}
		return nil
	default:
		return ErrLifecyclePublicationJournalConflict
	}
}

func requireLifecycleMigrationPublicationProof(
	ctx context.Context,
	tx pgx.Tx,
	request LifecycleBoundaryRequest,
	publicationMode LifecycleBoundaryPublicationMode,
) error {
	migrationMode, required := lifecycleMigrationModeForPublication(request.Operation, publicationMode)
	if !required {
		return nil
	}
	plan, err := lifecycleMigrationPlanFor(request, migrationMode, false)
	if err != nil {
		return err
	}
	proof, err := loadLifecycleMigrationProof(ctx, tx, plan.OperationID, plan.Mode, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleMigrationPublicationBlockedError()
	}
	if err != nil {
		return mapLifecycleMigrationProofError("load lifecycle migration proof for publication", err)
	}
	if !proof.matches(plan) {
		return ErrLifecycleMigrationsConflict
	}
	if !lifecycleMigrationPublicationProofReady(proof) {
		return lifecycleMigrationPublicationBlockedError()
	}
	return nil
}

func lifecycleMigrationModeForPublication(
	operation extensions.LifecycleMachineOperation,
	publicationMode LifecycleBoundaryPublicationMode,
) (LifecycleBoundaryMigrationMode, bool) {
	if publicationMode != LifecycleBoundaryActivate {
		return "", false
	}
	switch operation {
	case extensions.LifecycleMachineInstall:
		return LifecycleBoundaryMigrationInstall, true
	case extensions.LifecycleMachineUpgrade:
		return LifecycleBoundaryMigrationUpgrade, true
	case extensions.LifecycleMachineRollback:
		return LifecycleBoundaryMigrationRollback, true
	default:
		return "", false
	}
}

func lifecycleMigrationPublicationProofReady(proof lifecycleMigrationProofRecord) bool {
	if proof.Status != lifecycleMigrationStatusTargetReady || !proof.TargetReady ||
		proof.FirstAttempt <= 0 || proof.LastAttempt < proof.FirstAttempt ||
		!proof.ProofKind.Valid || !proof.ProofID.Valid || !proof.ProofDigest.Valid ||
		!validLifecycleMigrationProofID(proof.ProofID.String) ||
		!validLifecycleCleanupDigest(proof.ProofDigest.String) {
		return false
	}
	switch proof.ProofKind.String {
	case lifecycleMigrationProofHostNoop, lifecycleMigrationProofP5, lifecycleMigrationProofReused:
		return true
	default:
		return false
	}
}

func lifecycleMigrationPublicationBlockedError() error {
	return lifecycleMigrationBlockedError{
		reason: "lifecycle.migration_proof_required",
		detail: "exact migration plan is not target-ready",
	}
}

func lifecyclePluginRuntimePublicationTransition(
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) (extensions.PluginRuntimePublicationTransition, error) {
	if !lifecyclePublicationOperationMode(request.Operation, mode) {
		return extensions.PluginRuntimePublicationTransition{}, ErrLifecyclePublicationJournalInvalid
	}

	var reason extensions.PluginRuntimePublicationReason
	switch request.Operation {
	case extensions.LifecycleMachineInstall, extensions.LifecycleMachineEnable:
		reason = extensions.PluginRuntimePublicationEnable
	case extensions.LifecycleMachineUpgrade:
		reason = extensions.PluginRuntimePublicationUpgrade
	case extensions.LifecycleMachineRollback:
		reason = extensions.PluginRuntimePublicationRollback
	case extensions.LifecycleMachineDisable:
		reason = extensions.PluginRuntimePublicationDisable
	case extensions.LifecycleMachineUninstall:
		reason = extensions.PluginRuntimePublicationUninstall
	default:
		return extensions.PluginRuntimePublicationTransition{}, ErrLifecyclePublicationJournalInvalid
	}

	target, err := cloneManagedRuntimeExtension(request.TargetExtension)
	if err != nil {
		return extensions.PluginRuntimePublicationTransition{}, fmt.Errorf(
			"%w: freeze target runtime artifact: %v", ErrLifecyclePublicationJournalInvalid, err,
		)
	}
	transition := extensions.PluginRuntimePublicationTransition{
		Target:      target,
		Activate:    mode == LifecycleBoundaryActivate,
		Reason:      reason,
		ActorUserID: request.ActorUserID,
	}
	if request.SourceExtension != nil {
		source, cloneErr := cloneManagedRuntimeExtension(*request.SourceExtension)
		if cloneErr != nil {
			return extensions.PluginRuntimePublicationTransition{}, fmt.Errorf(
				"%w: freeze source runtime artifact: %v", ErrLifecyclePublicationJournalInvalid, cloneErr,
			)
		}
		transition.Source = &source
	}
	return transition, nil
}

type lifecyclePublicationArtifact struct {
	ExtensionID       string
	ExtensionVersion  string
	PackageDigest     string
	VersionID         int64
	RuntimeInstanceID string
	Present           bool
}

func (a lifecyclePublicationArtifact) nullableID() any {
	if !a.Present {
		return nil
	}
	return a.ExtensionID
}

func (a lifecyclePublicationArtifact) nullableVersion() any {
	if !a.Present {
		return nil
	}
	return a.ExtensionVersion
}

func (a lifecyclePublicationArtifact) nullableDigest() any {
	if !a.Present {
		return nil
	}
	return a.PackageDigest
}

func (a lifecyclePublicationArtifact) nullableVersionID() any {
	if !a.Present {
		return nil
	}
	return a.VersionID
}

func (a lifecyclePublicationArtifact) nullableInstanceID() any {
	if !a.Present {
		return nil
	}
	return a.RuntimeInstanceID
}

type lifecyclePublicationFence struct {
	OperationID int64
	Operation   extensions.LifecycleMachineOperation
	StepID      string
	Position    int
	Mode        LifecycleBoundaryPublicationMode
	Attempt     int
	Source      lifecyclePublicationArtifact
	Target      lifecyclePublicationArtifact
}

type lifecyclePublicationRecord struct {
	ID                               int64
	Fence                            lifecyclePublicationFence
	FirstAttempt                     int
	LastAttempt                      int
	CommitAttempt                    sql.NullInt64
	CommitMarker                     bool
	PluginRuntimePublicationRevision sql.NullInt64
	Revision                         int64
}

func (r lifecyclePublicationRecord) matches(fence lifecyclePublicationFence) bool {
	return r.Fence.OperationID == fence.OperationID && r.Fence.Operation == fence.Operation &&
		r.Fence.StepID == fence.StepID && r.Fence.Position == fence.Position &&
		r.Fence.Mode == fence.Mode && r.Fence.Source == fence.Source && r.Fence.Target == fence.Target
}

func (r lifecyclePublicationRecord) matchesOperation(fence lifecyclePublicationFence) bool {
	return r.Fence.OperationID == fence.OperationID && r.Fence.Operation == fence.Operation &&
		r.Fence.StepID == fence.StepID && r.Fence.Position == fence.Position && r.Fence.Mode == fence.Mode &&
		r.Fence.Source.sameArtifact(fence.Source) && r.Fence.Target.sameArtifact(fence.Target)
}

func (a lifecyclePublicationArtifact) sameArtifact(other lifecyclePublicationArtifact) bool {
	return a.Present == other.Present && a.ExtensionID == other.ExtensionID &&
		a.ExtensionVersion == other.ExtensionVersion && a.PackageDigest == other.PackageDigest &&
		a.VersionID == other.VersionID
}

func lifecyclePublicationFenceFor(
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) (lifecyclePublicationFence, error) {
	if request.OperationID <= 0 || request.Position < 0 || request.Attempt <= 0 ||
		request.StepID == "" || request.StepID != strings.TrimSpace(request.StepID) ||
		len(request.StepID) > 512 {
		return lifecyclePublicationFence{}, ErrLifecyclePublicationJournalInvalid
	}
	if !lifecyclePublicationOperationMode(request.Operation, mode) {
		return lifecyclePublicationFence{}, ErrLifecyclePublicationJournalInvalid
	}
	if err := validateExactCoordinatorArtifact("publication target", request.TargetExtension); err != nil ||
		request.TargetExtension.ActiveVersionID <= 0 || !validLifecycleCleanupDigest(request.TargetExtension.PackageDigest) {
		return lifecyclePublicationFence{}, fmt.Errorf("%w: target artifact is not exact", ErrLifecyclePublicationJournalInvalid)
	}
	requireTargetRuntime := mode == LifecycleBoundaryActivate
	if err := validateExactCoordinatorBinding(
		"publication target", request.TargetBinding, request.TargetExtension, requireTargetRuntime,
	); err != nil || len(request.TargetBinding.RuntimeInstanceID) > 512 {
		return lifecyclePublicationFence{}, fmt.Errorf("%w: target runtime binding is not exact", ErrLifecyclePublicationJournalInvalid)
	}

	fence := lifecyclePublicationFence{
		OperationID: request.OperationID, Operation: request.Operation,
		StepID: request.StepID, Position: request.Position, Mode: mode, Attempt: request.Attempt,
		Target: lifecyclePublicationArtifact{
			ExtensionID: request.TargetExtension.ID, ExtensionVersion: request.TargetExtension.Version,
			PackageDigest: request.TargetExtension.PackageDigest, VersionID: request.TargetExtension.ActiveVersionID,
			RuntimeInstanceID: request.TargetBinding.RuntimeInstanceID, Present: true,
		},
	}
	if request.SourceExtension != nil {
		source := *request.SourceExtension
		if err := validateExactCoordinatorArtifact("publication source", source); err != nil ||
			source.ID != request.TargetExtension.ID || source.ActiveVersionID <= 0 ||
			!validLifecycleCleanupDigest(source.PackageDigest) {
			return lifecyclePublicationFence{}, fmt.Errorf("%w: source artifact is not exact", ErrLifecyclePublicationJournalInvalid)
		}
		if err := validateExactCoordinatorBinding("publication source", request.SourceBinding, source, true); err != nil ||
			len(request.SourceBinding.RuntimeInstanceID) > 512 {
			return lifecyclePublicationFence{}, fmt.Errorf("%w: source runtime binding is not exact", ErrLifecyclePublicationJournalInvalid)
		}
		fence.Source = lifecyclePublicationArtifact{
			ExtensionID: source.ID, ExtensionVersion: source.Version, PackageDigest: source.PackageDigest,
			VersionID: source.ActiveVersionID, RuntimeInstanceID: request.SourceBinding.RuntimeInstanceID,
			Present: true,
		}
	}
	if (mode == LifecycleBoundaryDeactivate || request.Operation == extensions.LifecycleMachineUpgrade ||
		request.Operation == extensions.LifecycleMachineRollback) && !fence.Source.Present {
		return lifecyclePublicationFence{}, fmt.Errorf("%w: publication source is required", ErrLifecyclePublicationJournalInvalid)
	}
	if request.Operation == extensions.LifecycleMachineInstall && fence.Source.Present {
		return lifecyclePublicationFence{}, fmt.Errorf("%w: install publication cannot have a source", ErrLifecyclePublicationJournalInvalid)
	}
	return fence, nil
}

func lifecyclePublicationOperationMode(
	operation extensions.LifecycleMachineOperation,
	mode LifecycleBoundaryPublicationMode,
) bool {
	switch mode {
	case LifecycleBoundaryActivate:
		return operation == extensions.LifecycleMachineInstall || operation == extensions.LifecycleMachineEnable ||
			operation == extensions.LifecycleMachineUpgrade || operation == extensions.LifecycleMachineRollback
	case LifecycleBoundaryDeactivate:
		return operation == extensions.LifecycleMachineDisable || operation == extensions.LifecycleMachineUninstall
	default:
		return false
	}
}

type lifecyclePublicationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func validateLifecyclePublicationOperation(
	ctx context.Context,
	querier lifecyclePublicationQuerier,
	fence lifecyclePublicationFence,
) error {
	var operation, extensionID, extensionVersion, packageDigest string
	err := querier.QueryRow(ctx, `
		SELECT operation, extension_id, extension_version, package_digest
		FROM extension_lifecycle_operations
		WHERE id = $1
		FOR KEY SHARE
	`, fence.OperationID).Scan(&operation, &extensionID, &extensionVersion, &packageDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLifecyclePublicationJournalNotPrepared
	}
	if err != nil {
		return fmt.Errorf("load lifecycle publication operation: %w", err)
	}
	if operation != string(fence.Operation) || extensionID != fence.Target.ExtensionID ||
		extensionVersion != fence.Target.ExtensionVersion || packageDigest != fence.Target.PackageDigest {
		return ErrLifecyclePublicationJournalConflict
	}
	return nil
}

func loadLifecyclePublication(
	ctx context.Context,
	querier lifecyclePublicationQuerier,
	operationID int64,
	stepID string,
	mode LifecycleBoundaryPublicationMode,
	forUpdate bool,
) (lifecyclePublicationRecord, error) {
	query := `
		SELECT id, operation, position,
		       source_extension_id, source_extension_version, source_package_digest,
		       source_version_id, source_runtime_instance_id,
		       target_extension_id, target_extension_version, target_package_digest,
		       target_version_id, target_runtime_instance_id,
		       first_attempt, last_attempt, committed_attempt, commit_marker,
		       plugin_runtime_publication_revision, revision
		FROM extension_lifecycle_publications
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = $3
	`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var record lifecyclePublicationRecord
	var operation string
	var sourceID, sourceVersion, sourceDigest, sourceInstance sql.NullString
	var sourceVersionID sql.NullInt64
	err := querier.QueryRow(ctx, query, operationID, stepID, mode).Scan(
		&record.ID, &operation, &record.Fence.Position,
		&sourceID, &sourceVersion, &sourceDigest, &sourceVersionID, &sourceInstance,
		&record.Fence.Target.ExtensionID, &record.Fence.Target.ExtensionVersion,
		&record.Fence.Target.PackageDigest, &record.Fence.Target.VersionID,
		&record.Fence.Target.RuntimeInstanceID, &record.FirstAttempt, &record.LastAttempt,
		&record.CommitAttempt, &record.CommitMarker, &record.PluginRuntimePublicationRevision,
		&record.Revision,
	)
	if err != nil {
		return lifecyclePublicationRecord{}, err
	}
	record.Fence.OperationID = operationID
	record.Fence.Operation = extensions.LifecycleMachineOperation(operation)
	record.Fence.StepID = stepID
	record.Fence.Mode = mode
	record.Fence.Target.Present = true
	if sourceID.Valid || sourceVersion.Valid || sourceDigest.Valid || sourceVersionID.Valid || sourceInstance.Valid {
		if !sourceID.Valid || !sourceVersion.Valid || !sourceDigest.Valid || !sourceVersionID.Valid || !sourceInstance.Valid {
			return lifecyclePublicationRecord{}, ErrLifecyclePublicationJournalConflict
		}
		record.Fence.Source = lifecyclePublicationArtifact{
			ExtensionID: sourceID.String, ExtensionVersion: sourceVersion.String,
			PackageDigest: sourceDigest.String, VersionID: sourceVersionID.Int64,
			RuntimeInstanceID: sourceInstance.String, Present: true,
		}
	}
	return record, nil
}

func mapLifecyclePublicationLoadError(action string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLifecyclePublicationJournalNotPrepared
	}
	if errors.Is(err, ErrLifecyclePublicationJournalConflict) {
		return err
	}
	return fmt.Errorf("%s: %w", action, err)
}

var _ LifecycleBoundaryPublicationJournal = (*PostgresLifecycleBoundaryPublicationJournal)(nil)
