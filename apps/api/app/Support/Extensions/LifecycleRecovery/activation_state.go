package lifecyclerecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	audit "github.com/zhuchunshu/sforum/apps/api/app/Support/Audit"
)

const (
	interruptedActivationDefaultLimit = 16
	interruptedActivationMaxLimit     = 64
	interruptedActivationReason       = "interrupted activation state restored to source"
)

var errActivationRecoveryUnavailable = errors.New("extension lifecycle activation recovery is unavailable")

// RecoveredActivation describes one half-published activation that was restored
// to its exact source artifact. Version ids are the immutable
// extension_versions rows, so callers can audit the exact before/after pair.
type RecoveredActivation struct {
	OperationID        int64  `json:"operationId"`
	ExtensionID        string `json:"extensionId"`
	StatePublicationID int64  `json:"statePublicationId"`
	FromVersionID      int64  `json:"fromVersionId"`
	FromVersion        string `json:"fromVersion"`
	FromPackageDigest  string `json:"fromPackageDigest"`
	ToVersionID        int64  `json:"toVersionId"`
	ToVersion          string `json:"toVersion"`
	ToPackageDigest    string `json:"toPackageDigest"`
	AuditEventID       int64  `json:"auditEventId"`
}

// PostgresInterruptedActivationRecovery repairs an activation whose state
// transaction committed the target artifact but whose publication never wrote
// its commit marker (the step was abandoned, then the host reclaimed the
// operation). Runtime, registries and jobs converge back to the last committed
// publication on restart, but extensions.active_version_id keeps pointing at
// the uncommitted target; every later lifecycle operation then drains a decoy
// source instance and fails closed.
//
// The repair only runs on exact durable evidence (see interruptedActivationPredicate)
// and only moves the extension row back to the source artifact recorded by the
// same state transaction. It never touches runtime instances, registries, jobs,
// or the drain guard. It is idempotent: after the repair the state row is source
// with restored_at set, so a second run finds no candidate. It is meant for
// startup (before plugin runtime convergence) and for an idle out-of-band CLI,
// not for concurrent execution against an in-flight lifecycle operation.
type PostgresInterruptedActivationRecovery struct {
	pool *pgxpool.Pool
}

// NewPostgresInterruptedActivationRecovery constructs the production repair
// collaborator. It needs only the Host pool: the evidence is durable, so no
// process, registry, or manager state is required.
func NewPostgresInterruptedActivationRecovery(pool *pgxpool.Pool) *PostgresInterruptedActivationRecovery {
	return &PostgresInterruptedActivationRecovery{pool: pool}
}

// RecoverInterruptedActivations repairs up to limit candidates (0 = default)
// and returns the exact repairs it committed. A candidate that drifts between
// discovery and locking is skipped, not failed: the caller's contract is
// "repair what is still provably safe, leave everything else alone".
func (r *PostgresInterruptedActivationRecovery) RecoverInterruptedActivations(
	ctx context.Context,
	limit int,
) ([]RecoveredActivation, error) {
	if r == nil || r.pool == nil || ctx == nil {
		return nil, errActivationRecoveryUnavailable
	}
	if limit <= 0 {
		limit = interruptedActivationDefaultLimit
	}
	if limit > interruptedActivationMaxLimit {
		limit = interruptedActivationMaxLimit
	}
	operationIDs, err := r.listInterruptedActivations(ctx, limit)
	if err != nil {
		return nil, err
	}
	recovered := make([]RecoveredActivation, 0, len(operationIDs))
	for _, operationID := range operationIDs {
		if err := ctx.Err(); err != nil {
			return recovered, err
		}
		item, repaired, err := r.recoverInterruptedActivation(ctx, operationID)
		if err != nil {
			return recovered, err
		}
		if repaired {
			recovered = append(recovered, item)
		}
	}
	return recovered, nil
}

const interruptedActivationFrom = `
	FROM extension_lifecycle_operations AS op
	JOIN extension_lifecycle_publications AS publication
	  ON publication.operation_id = op.id
	 AND publication.operation = op.operation
	JOIN extension_lifecycle_state_publications AS state
	  ON state.operation_id = op.id
	 AND state.step_id = publication.step_id
	 AND state.publication_mode = publication.publication_mode
	JOIN extension_lifecycle_registry_publications AS registry
	  ON registry.operation_id = op.id
	 AND registry.step_id = publication.step_id
	 AND registry.publication_mode = publication.publication_mode
	JOIN extensions AS extension
	  ON extension.id = op.extension_id
	JOIN plugin_runtime_publication_members AS member
	  ON member.extension_id = op.extension_id
	 AND member.publication_revision = (SELECT max(revision) FROM plugin_runtime_publications)
`

// interruptedActivationPredicate is the complete safety envelope. Every clause
// is durable evidence that the state transaction advanced alone and that the
// runtime side never left the source artifact:
//   - the operation is terminal and closed (orphan reclaim or a failed step);
//   - the activation publication never wrote its commit marker;
//   - the state transaction published the target and was never restored;
//   - the registry transaction is still source (registries never advanced);
//   - the latest committed runtime publication still pins the source artifact;
//   - the extension row still points at the target with an unchanged status.
//
// Any drift removes the row from the candidate set, so the repair is a no-op.
const interruptedActivationPredicate = `
	op.state = 'failed'
	AND op.terminal_result IN ('failed', 'cancelled')
	AND op.completed_at IS NOT NULL
	AND publication.publication_mode = 'activate'
	AND publication.commit_marker = FALSE
	AND publication.last_attempt = op.attempt_count
	AND state.transaction_state = 'target'
	AND state.restored_at IS NULL
	AND state.last_attempt = op.attempt_count
	AND state.source_status = state.target_status
	AND state.source_active_version_id IS NOT NULL
	AND state.source_active_version IS NOT NULL
	AND state.source_active_package_digest IS NOT NULL
	AND registry.transaction_state = 'source'
	AND registry.last_attempt = op.attempt_count
	AND extension.active_version_id = state.target_active_version_id
	AND extension.status = state.target_status
	AND member.extension_version_id = state.source_active_version_id
	AND member.extension_version = state.source_active_version
	AND member.package_digest = state.source_active_package_digest
`

func (r *PostgresInterruptedActivationRecovery) listInterruptedActivations(
	ctx context.Context,
	limit int,
) ([]int64, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT op.id`+interruptedActivationFrom+`
		WHERE `+interruptedActivationPredicate+`
		ORDER BY op.completed_at DESC, op.id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list interrupted activation publications: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan interrupted activation publication: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate interrupted activation publications: %w", err)
	}
	return ids, nil
}

type interruptedActivationRecord struct {
	OperationID        int64
	ExtensionID        string
	StatePublicationID int64
	StateRevision      int64
	SourceVersionID    int64
	SourceVersion      string
	SourceDigest       string
	TargetVersionID    int64
	TargetVersion      string
	TargetDigest       string
	TargetStatus       string
	ActiveVersionID    int64
}

func (r *PostgresInterruptedActivationRecovery) recoverInterruptedActivation(
	ctx context.Context,
	operationID int64,
) (RecoveredActivation, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return RecoveredActivation{}, false, fmt.Errorf("begin interrupted activation recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	record, err := lockInterruptedActivation(ctx, tx, operationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecoveredActivation{}, false, nil
	}
	if err != nil {
		return RecoveredActivation{}, false, err
	}

	stateTag, err := tx.Exec(ctx, `
		UPDATE extension_lifecycle_state_publications
		SET transaction_state = 'source',
		    restored_at = statement_timestamp(),
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1
		  AND transaction_state = 'target'
		  AND restored_at IS NULL
		  AND revision = $2
	`, record.StatePublicationID, record.StateRevision)
	if err != nil {
		return RecoveredActivation{}, false, fmt.Errorf("restore interrupted activation state publication: %w", err)
	}
	if stateTag.RowsAffected() != 1 {
		return RecoveredActivation{}, false, nil
	}

	extensionTag, err := tx.Exec(ctx, `
		UPDATE extensions
		SET active_version_id = $2,
		    updated_at = statement_timestamp()
		WHERE id = $1
		  AND active_version_id = $3
		  AND status = $4
	`, record.ExtensionID, record.SourceVersionID, record.TargetVersionID, record.TargetStatus)
	if err != nil {
		return RecoveredActivation{}, false, fmt.Errorf("restore interrupted activation extension pointer: %w", err)
	}
	if extensionTag.RowsAffected() != 1 {
		return RecoveredActivation{}, false, nil
	}

	metadata, err := json.Marshal(map[string]any{
		"operationId":        record.OperationID,
		"extensionId":        record.ExtensionID,
		"statePublicationId": record.StatePublicationID,
		"fromVersionId":      record.TargetVersionID,
		"fromVersion":        record.TargetVersion,
		"fromPackageDigest":  record.TargetDigest,
		"toVersionId":        record.SourceVersionID,
		"toVersion":          record.SourceVersion,
		"toPackageDigest":    record.SourceDigest,
		"reason":             interruptedActivationReason,
	})
	if err != nil {
		return RecoveredActivation{}, false, fmt.Errorf("encode interrupted activation recovery audit: %w", err)
	}
	var auditEventID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO audit_events (action, metadata)
		VALUES ($1, $2::jsonb)
		RETURNING id
	`, audit.ActionExtensionLifecycleActivationRecovered, string(metadata)).Scan(&auditEventID); err != nil {
		return RecoveredActivation{}, false, fmt.Errorf("record interrupted activation recovery audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RecoveredActivation{}, false, fmt.Errorf("commit interrupted activation recovery: %w", err)
	}
	return RecoveredActivation{
		OperationID:        record.OperationID,
		ExtensionID:        record.ExtensionID,
		StatePublicationID: record.StatePublicationID,
		FromVersionID:      record.TargetVersionID,
		FromVersion:        record.TargetVersion,
		FromPackageDigest:  record.TargetDigest,
		ToVersionID:        record.SourceVersionID,
		ToVersion:          record.SourceVersion,
		ToPackageDigest:    record.SourceDigest,
		AuditEventID:       auditEventID,
	}, true, nil
}

func lockInterruptedActivation(
	ctx context.Context,
	tx pgx.Tx,
	operationID int64,
) (interruptedActivationRecord, error) {
	var record interruptedActivationRecord
	err := tx.QueryRow(ctx, `
		SELECT op.id, op.extension_id,
		       state.id, state.revision,
		       state.source_active_version_id, state.source_active_version, state.source_active_package_digest,
		       state.target_active_version_id, state.target_active_version, state.target_active_package_digest,
		       state.target_status,
		       extension.active_version_id`+interruptedActivationFrom+`
		WHERE op.id = $1
		  AND `+interruptedActivationPredicate+`
		FOR UPDATE OF op, publication, state, registry, extension
	`, operationID).Scan(
		&record.OperationID, &record.ExtensionID,
		&record.StatePublicationID, &record.StateRevision,
		&record.SourceVersionID, &record.SourceVersion, &record.SourceDigest,
		&record.TargetVersionID, &record.TargetVersion, &record.TargetDigest,
		&record.TargetStatus,
		&record.ActiveVersionID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return interruptedActivationRecord{}, err
		}
		return interruptedActivationRecord{}, fmt.Errorf("lock interrupted activation publication: %w", err)
	}
	if record.ActiveVersionID != record.TargetVersionID {
		// The predicate already proves this; keep the invariant explicit so a
		// future predicate edit cannot silently widen the repair.
		return interruptedActivationRecord{}, pgx.ErrNoRows
	}
	return record, nil
}
