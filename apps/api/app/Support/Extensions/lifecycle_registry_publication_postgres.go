package extensionsruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

type PostgresLifecycleRegistryPublicationRepository struct {
	pool                *pgxpool.Pool
	moveConnectionSlots chan struct{}
}

const lifecycleRegistryPublicationMoveCleanupTimeout = 2 * time.Second

func NewPostgresLifecycleRegistryPublicationRepository(
	pool *pgxpool.Pool,
) *PostgresLifecycleRegistryPublicationRepository {
	return &PostgresLifecycleRegistryPublicationRepository{
		pool:                pool,
		moveConnectionSlots: make(chan struct{}, 1),
	}
}

type lifecycleRegistryPublicationAuthority struct {
	PublicationID int64
	Fence         lifecyclePublicationFence
	LastAttempt   int
	CommitMarker  bool
	OperationOpen bool
}

type lifecycleRegistryPublicationRecord struct {
	ID           int64
	Fence        lifecyclePublicationFence
	SourceDigest string
	TargetDigest string
	Phase        LifecycleRegistryPublicationPhase
	FirstAttempt int
	LastAttempt  int
	Revision     int64
}

func (r *PostgresLifecycleRegistryPublicationRepository) PrepareLifecycleRegistryPublication(
	ctx context.Context,
	input PrepareLifecycleRegistryPublicationInput,
) (LifecycleRegistryPublicationRef, error) {
	if r == nil || r.pool == nil || ctx == nil || !validLifecycleRegistryPrepareInput(input) {
		return LifecycleRegistryPublicationRef{}, ErrLifecycleRegistryPublicationInvalid
	}
	ref := lifecycleRegistryRef(input.Fence)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return LifecycleRegistryPublicationRef{}, fmt.Errorf("begin lifecycle registry publication prepare: %w", err)
	}
	defer tx.Rollback(ctx)
	authority, err := lockLifecycleRegistryPublicationAuthority(ctx, tx, ref)
	if err != nil {
		return LifecycleRegistryPublicationRef{}, err
	}
	if !authority.OperationOpen || authority.LastAttempt != input.Fence.Attempt ||
		!lifecycleRegistryFenceMatches(authority.Fence, input.Fence) {
		return LifecycleRegistryPublicationRef{}, ErrLifecycleRegistryPublicationConflict
	}

	record, err := loadLifecycleRegistryPublication(ctx, tx, ref, true)
	if errors.Is(err, pgx.ErrNoRows) {
		if authority.CommitMarker {
			return LifecycleRegistryPublicationRef{}, ErrLifecycleRegistryPublicationConflict
		}
		if err := insertLifecycleRegistryPublication(ctx, tx, authority.PublicationID, input); err != nil {
			return LifecycleRegistryPublicationRef{}, err
		}
	} else if err != nil {
		return LifecycleRegistryPublicationRef{}, mapLifecycleRegistryPublicationError("load prepared lifecycle registry publication", err)
	} else {
		if !record.matchesInput(input) || input.Fence.Attempt < record.LastAttempt ||
			(authority.CommitMarker && record.Phase != LifecycleRegistryPublicationTarget) {
			return LifecycleRegistryPublicationRef{}, ErrLifecycleRegistryPublicationConflict
		}
		if input.Fence.Attempt > record.LastAttempt || !lifecycleRegistryFenceMatches(record.Fence, input.Fence) {
			tag, updateErr := tx.Exec(ctx, `
				UPDATE extension_lifecycle_registry_publications
				SET source_runtime_instance_id = $2,
				    target_runtime_instance_id = $3,
				    last_attempt = $4,
				    revision = revision + 1,
				    updated_at = statement_timestamp()
				WHERE id = $1 AND revision = $5 AND last_attempt = $6
			`, record.ID, input.Fence.Source.nullableInstanceID(), input.Fence.Target.RuntimeInstanceID,
				input.Fence.Attempt, record.Revision, record.LastAttempt)
			if updateErr != nil {
				return LifecycleRegistryPublicationRef{}, fmt.Errorf("advance lifecycle registry publication attempt: %w", updateErr)
			}
			if tag.RowsAffected() != 1 {
				return LifecycleRegistryPublicationRef{}, ErrLifecycleRegistryPublicationConflict
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return LifecycleRegistryPublicationRef{}, fmt.Errorf("commit lifecycle registry publication prepare: %w", err)
	}
	return ref, nil
}

func (r *PostgresLifecycleRegistryPublicationRepository) InspectLifecycleRegistryPublication(
	ctx context.Context,
	ref LifecycleRegistryPublicationRef,
) (LifecycleRegistryPublicationPhase, error) {
	if r == nil || r.pool == nil || ctx == nil || !validLifecycleRegistryRef(ref) {
		return "", ErrLifecycleRegistryPublicationInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin lifecycle registry publication inspect: %w", err)
	}
	defer tx.Rollback(ctx)
	authority, record, err := lockLifecycleRegistryPublication(ctx, tx, ref)
	if err != nil {
		return "", err
	}
	if authority.CommitMarker && record.Phase != LifecycleRegistryPublicationTarget {
		return "", ErrLifecycleRegistryPublicationConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit lifecycle registry publication inspect: %w", err)
	}
	return record.Phase, nil
}

func (r *PostgresLifecycleRegistryPublicationRepository) MoveLifecycleRegistryPublication(
	ctx context.Context,
	ref LifecycleRegistryPublicationRef,
	destination LifecycleRegistryPublicationPhase,
	apply func() error,
) error {
	if apply == nil {
		return ErrLifecycleRegistryPublicationInvalid
	}
	return r.moveLifecycleRegistryPublication(ctx, ref, destination, func(pgx.Tx) error {
		return apply()
	})
}

func (r *PostgresLifecycleRegistryPublicationRepository) MoveLifecycleRegistryPublicationTx(
	ctx context.Context,
	ref LifecycleRegistryPublicationRef,
	destination LifecycleRegistryPublicationPhase,
	apply func(pgx.Tx) error,
) error {
	return r.moveLifecycleRegistryPublication(ctx, ref, destination, apply)
}

func (r *PostgresLifecycleRegistryPublicationRepository) moveLifecycleRegistryPublication(
	ctx context.Context,
	ref LifecycleRegistryPublicationRef,
	destination LifecycleRegistryPublicationPhase,
	apply func(pgx.Tx) error,
) error {
	if r == nil || r.pool == nil || r.moveConnectionSlots == nil || ctx == nil ||
		!validLifecycleRegistryRef(ref) || apply == nil ||
		(destination != LifecycleRegistryPublicationSource && destination != LifecycleRegistryPublicationTarget) {
		return ErrLifecycleRegistryPublicationInvalid
	}
	if err := r.acquireMoveConnection(ctx); err != nil {
		return fmt.Errorf("acquire lifecycle registry publication move connection: %w", err)
	}
	defer r.releaseMoveConnection()

	// The outer transaction invokes registry callbacks that may synchronously
	// borrow the Host pool. A reserved direct connection prevents MaxConns=1
	// self-starvation while retaining the existing durable row fences.
	connection, err := pgx.ConnectConfig(ctx, r.pool.Config().ConnConfig)
	if err != nil {
		return fmt.Errorf("connect lifecycle registry publication move: %w", err)
	}
	var tx pgx.Tx
	defer func() {
		cleanupLifecycleRegistryPublicationMoveConnection(ctx, tx, connection)
	}()
	tx, err = connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin lifecycle registry publication move: %w", err)
	}
	authority, record, err := lockLifecycleRegistryPublication(ctx, tx, ref)
	if err != nil {
		return err
	}
	if destination == LifecycleRegistryPublicationSource && authority.CommitMarker {
		return ErrLifecycleRegistryPublicationCommitted
	}
	if authority.CommitMarker && record.Phase != LifecycleRegistryPublicationTarget {
		return ErrLifecycleRegistryPublicationConflict
	}
	// The row locks remain held while this node swaps its local immutable
	// snapshots. Other nodes perform the same deterministic reconstruction.
	if err := apply(tx); err != nil {
		return err
	}
	if record.Phase != destination {
		tag, updateErr := tx.Exec(ctx, `
			UPDATE extension_lifecycle_registry_publications
			SET transaction_state = $2,
			    revision = revision + 1,
			    updated_at = statement_timestamp(),
			    published_at = CASE WHEN $2 = 'target' THEN COALESCE(published_at, statement_timestamp()) ELSE published_at END,
			    restored_at = CASE WHEN $2 = 'source' THEN statement_timestamp() ELSE restored_at END
			WHERE id = $1 AND revision = $3 AND transaction_state = $4 AND last_attempt = $5
		`, record.ID, destination, record.Revision, record.Phase, ref.Attempt)
		if updateErr != nil {
			return fmt.Errorf("advance lifecycle registry publication: %w", updateErr)
		}
		if tag.RowsAffected() != 1 {
			return ErrLifecycleRegistryPublicationConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		// Commit status is unknown. The caller inspects this durable phase and
		// converges again while ordinary runtime admission remains closed.
		return fmt.Errorf("commit lifecycle registry publication move: %w", err)
	}
	return nil
}

func (r *PostgresLifecycleRegistryPublicationRepository) acquireMoveConnection(ctx context.Context) error {
	select {
	case r.moveConnectionSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *PostgresLifecycleRegistryPublicationRepository) releaseMoveConnection() {
	<-r.moveConnectionSlots
}

func cleanupLifecycleRegistryPublicationMoveConnection(
	parent context.Context,
	tx pgx.Tx,
	connection *pgx.Conn,
) {
	if tx != nil {
		rollbackCtx, cancelRollback := context.WithTimeout(
			context.WithoutCancel(parent),
			lifecycleRegistryPublicationMoveCleanupTimeout,
		)
		_ = tx.Rollback(rollbackCtx)
		cancelRollback()
	}
	if connection != nil {
		closeCtx, cancelClose := context.WithTimeout(
			context.WithoutCancel(parent),
			lifecycleRegistryPublicationMoveCleanupTimeout,
		)
		_ = connection.Close(closeCtx)
		cancelClose()
	}
}

func lifecycleRegistryRef(fence lifecyclePublicationFence) LifecycleRegistryPublicationRef {
	return LifecycleRegistryPublicationRef{
		OperationID: fence.OperationID, StepID: fence.StepID, Mode: fence.Mode, Attempt: fence.Attempt,
	}
}

func validLifecycleRegistryPrepareInput(input PrepareLifecycleRegistryPublicationInput) bool {
	return input.Fence.OperationID > 0 && input.Fence.Attempt > 0 &&
		validLifecycleCleanupDigest(input.SourceDigest) && validLifecycleCleanupDigest(input.TargetDigest) &&
		validLifecycleRegistryCompatibleDigests(input.CompatibleSourceDigests, input.SourceDigest) &&
		validLifecycleRegistryCompatibleDigests(input.CompatibleTargetDigests, input.TargetDigest)
}

func validLifecycleRegistryCompatibleDigests(values []string, primary string) bool {
	// Editor plan @10 may resume any exact @1..@9 material digest.
	// Media @9 could resume @1..@8; earlier families keep the same bound.
	if len(values) > 9 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validLifecycleCleanupDigest(value) || value == primary {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validLifecycleRegistryRef(ref LifecycleRegistryPublicationRef) bool {
	return ref.OperationID > 0 && ref.Attempt > 0 && ref.StepID != "" &&
		ref.StepID == strings.TrimSpace(ref.StepID) && len(ref.StepID) <= 512 &&
		(ref.Mode == LifecycleBoundaryActivate || ref.Mode == LifecycleBoundaryDeactivate)
}

func lockLifecycleRegistryPublication(
	ctx context.Context,
	tx pgx.Tx,
	ref LifecycleRegistryPublicationRef,
) (lifecycleRegistryPublicationAuthority, lifecycleRegistryPublicationRecord, error) {
	authority, err := lockLifecycleRegistryPublicationAuthority(ctx, tx, ref)
	if err != nil {
		return lifecycleRegistryPublicationAuthority{}, lifecycleRegistryPublicationRecord{}, err
	}
	if !authority.OperationOpen || authority.LastAttempt != ref.Attempt {
		return lifecycleRegistryPublicationAuthority{}, lifecycleRegistryPublicationRecord{}, ErrLifecycleRegistryPublicationConflict
	}
	record, err := loadLifecycleRegistryPublication(ctx, tx, ref, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleRegistryPublicationAuthority{}, lifecycleRegistryPublicationRecord{}, ErrLifecycleRegistryPublicationNotPrepared
	}
	if err != nil {
		return lifecycleRegistryPublicationAuthority{}, lifecycleRegistryPublicationRecord{}, mapLifecycleRegistryPublicationError("lock lifecycle registry publication", err)
	}
	if record.LastAttempt != ref.Attempt || !lifecycleRegistryFenceMatches(record.Fence, authority.Fence) {
		return lifecycleRegistryPublicationAuthority{}, lifecycleRegistryPublicationRecord{}, ErrLifecycleRegistryPublicationConflict
	}
	return authority, record, nil
}

func lockLifecycleRegistryPublicationAuthority(
	ctx context.Context,
	tx pgx.Tx,
	ref LifecycleRegistryPublicationRef,
) (lifecycleRegistryPublicationAuthority, error) {
	var authority lifecycleRegistryPublicationAuthority
	var operation string
	var sourceID, sourceVersion, sourceDigest, sourceRuntime sql.NullString
	var sourceVersionID sql.NullInt64
	err := tx.QueryRow(ctx, `
		SELECT publication.id, publication.operation, publication.position,
		       publication.source_extension_id, publication.source_extension_version,
		       publication.source_package_digest, publication.source_version_id,
		       publication.source_runtime_instance_id,
		       publication.target_extension_id, publication.target_extension_version,
		       publication.target_package_digest, publication.target_version_id,
		       publication.target_runtime_instance_id,
		       publication.last_attempt, publication.commit_marker,
		       operation.completed_at IS NULL AND operation.terminal_result IS NULL
		FROM extension_lifecycle_publications AS publication
		JOIN extension_lifecycle_operations AS operation ON operation.id = publication.operation_id
		WHERE publication.operation_id = $1
		  AND publication.step_id = $2
		  AND publication.publication_mode = $3
		FOR UPDATE OF publication, operation
	`, ref.OperationID, ref.StepID, ref.Mode).Scan(
		&authority.PublicationID, &operation, &authority.Fence.Position,
		&sourceID, &sourceVersion, &sourceDigest, &sourceVersionID, &sourceRuntime,
		&authority.Fence.Target.ExtensionID, &authority.Fence.Target.ExtensionVersion,
		&authority.Fence.Target.PackageDigest, &authority.Fence.Target.VersionID,
		&authority.Fence.Target.RuntimeInstanceID,
		&authority.LastAttempt, &authority.CommitMarker, &authority.OperationOpen,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleRegistryPublicationAuthority{}, ErrLifecycleRegistryPublicationNotPrepared
	}
	if err != nil {
		return lifecycleRegistryPublicationAuthority{}, fmt.Errorf("lock lifecycle registry publication authority: %w", err)
	}
	authority.Fence.OperationID = ref.OperationID
	authority.Fence.Operation = extensions.LifecycleMachineOperation(operation)
	authority.Fence.StepID = ref.StepID
	authority.Fence.Mode = ref.Mode
	authority.Fence.Attempt = authority.LastAttempt
	authority.Fence.Target.Present = true
	if sourceID.Valid || sourceVersion.Valid || sourceDigest.Valid || sourceVersionID.Valid || sourceRuntime.Valid {
		if !sourceID.Valid || !sourceVersion.Valid || !sourceDigest.Valid || !sourceVersionID.Valid || !sourceRuntime.Valid {
			return lifecycleRegistryPublicationAuthority{}, ErrLifecycleRegistryPublicationConflict
		}
		authority.Fence.Source = lifecyclePublicationArtifact{
			ExtensionID: sourceID.String, ExtensionVersion: sourceVersion.String,
			PackageDigest: sourceDigest.String, VersionID: sourceVersionID.Int64,
			RuntimeInstanceID: sourceRuntime.String, Present: true,
		}
	}
	return authority, nil
}

func loadLifecycleRegistryPublication(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	ref LifecycleRegistryPublicationRef,
	lock bool,
) (lifecycleRegistryPublicationRecord, error) {
	lockSQL := ""
	if lock {
		lockSQL = " FOR UPDATE"
	}
	var record lifecycleRegistryPublicationRecord
	var operation string
	var sourceID, sourceVersion, sourceDigest, sourceRuntime sql.NullString
	var sourceVersionID sql.NullInt64
	err := querier.QueryRow(ctx, `
		SELECT id, operation, position,
		       source_extension_id, source_extension_version, source_package_digest,
		       source_version_id, source_runtime_instance_id, source_plan_digest,
		       target_extension_id, target_extension_version, target_package_digest,
		       target_version_id, target_runtime_instance_id, target_plan_digest,
		       transaction_state, first_attempt, last_attempt, revision
		FROM extension_lifecycle_registry_publications
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = $3`+lockSQL,
		ref.OperationID, ref.StepID, ref.Mode,
	).Scan(
		&record.ID, &operation, &record.Fence.Position,
		&sourceID, &sourceVersion, &sourceDigest, &sourceVersionID, &sourceRuntime, &record.SourceDigest,
		&record.Fence.Target.ExtensionID, &record.Fence.Target.ExtensionVersion,
		&record.Fence.Target.PackageDigest, &record.Fence.Target.VersionID,
		&record.Fence.Target.RuntimeInstanceID, &record.TargetDigest,
		&record.Phase, &record.FirstAttempt, &record.LastAttempt, &record.Revision,
	)
	if err != nil {
		return lifecycleRegistryPublicationRecord{}, err
	}
	record.Fence.OperationID = ref.OperationID
	record.Fence.Operation = extensions.LifecycleMachineOperation(operation)
	record.Fence.StepID = ref.StepID
	record.Fence.Mode = ref.Mode
	record.Fence.Attempt = record.LastAttempt
	record.Fence.Target.Present = true
	if sourceID.Valid || sourceVersion.Valid || sourceDigest.Valid || sourceVersionID.Valid || sourceRuntime.Valid {
		if !sourceID.Valid || !sourceVersion.Valid || !sourceDigest.Valid || !sourceVersionID.Valid || !sourceRuntime.Valid {
			return lifecycleRegistryPublicationRecord{}, ErrLifecycleRegistryPublicationConflict
		}
		record.Fence.Source = lifecyclePublicationArtifact{
			ExtensionID: sourceID.String, ExtensionVersion: sourceVersion.String,
			PackageDigest: sourceDigest.String, VersionID: sourceVersionID.Int64,
			RuntimeInstanceID: sourceRuntime.String, Present: true,
		}
	}
	return record, nil
}

func (record lifecycleRegistryPublicationRecord) matchesInput(input PrepareLifecycleRegistryPublicationInput) bool {
	return lifecycleRegistryFenceMatchesOperation(record.Fence, input.Fence) &&
		lifecycleRegistryDigestMatches(record.SourceDigest, input.SourceDigest, input.CompatibleSourceDigests) &&
		lifecycleRegistryDigestMatches(record.TargetDigest, input.TargetDigest, input.CompatibleTargetDigests)
}

func lifecycleRegistryDigestMatches(stored, primary string, compatible []string) bool {
	if stored == primary {
		return true
	}
	for _, value := range compatible {
		if stored == value {
			return true
		}
	}
	return false
}

func lifecycleRegistryFenceMatches(left, right lifecyclePublicationFence) bool {
	return lifecycleRegistryFenceMatchesOperation(left, right) && left.Attempt == right.Attempt &&
		left.Source == right.Source && left.Target == right.Target
}

func lifecycleRegistryFenceMatchesOperation(left, right lifecyclePublicationFence) bool {
	return left.OperationID == right.OperationID && left.Operation == right.Operation &&
		left.StepID == right.StepID && left.Position == right.Position && left.Mode == right.Mode &&
		left.Source.sameArtifact(right.Source) && left.Target.sameArtifact(right.Target)
}

func insertLifecycleRegistryPublication(
	ctx context.Context,
	tx pgx.Tx,
	publicationID int64,
	input PrepareLifecycleRegistryPublicationInput,
) error {
	fence := input.Fence
	_, err := tx.Exec(ctx, `
		INSERT INTO extension_lifecycle_registry_publications (
			publication_id, operation_id, operation, step_id, position, publication_mode,
			source_extension_id, source_extension_version, source_package_digest,
			source_version_id, source_runtime_instance_id, source_plan_digest,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id, target_plan_digest,
			first_attempt, last_attempt
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18,
			$19, $19
		)
	`, publicationID, fence.OperationID, fence.Operation, fence.StepID, fence.Position, fence.Mode,
		fence.Source.nullableID(), fence.Source.nullableVersion(), fence.Source.nullableDigest(),
		fence.Source.nullableVersionID(), fence.Source.nullableInstanceID(), input.SourceDigest,
		fence.Target.ExtensionID, fence.Target.ExtensionVersion, fence.Target.PackageDigest,
		fence.Target.VersionID, fence.Target.RuntimeInstanceID, input.TargetDigest, fence.Attempt)
	if err != nil {
		return fmt.Errorf("insert lifecycle registry publication: %w", err)
	}
	return nil
}

func mapLifecycleRegistryPublicationError(action string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLifecycleRegistryPublicationNotPrepared
	}
	return fmt.Errorf("%s: %w", action, err)
}

var _ LifecycleRegistryPublicationRepository = (*PostgresLifecycleRegistryPublicationRepository)(nil)
