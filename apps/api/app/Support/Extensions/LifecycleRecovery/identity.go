package lifecyclerecovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

var errIdentityRecoveryUnavailable = errors.New("extension lifecycle identity recovery is unavailable")

// PostgresIdentityStartupRecovery owns the exact-evidence query and atomic
// compensating Identity Registry revision used before normal startup restore.
type PostgresIdentityStartupRecovery struct {
	pool  *pgxpool.Pool
	store identityregistry.PublicationStore
}

// NewPostgresIdentityStartupRecovery constructs the production recovery
// collaborator without starting or admitting plugin code.
func NewPostgresIdentityStartupRecovery(
	pool *pgxpool.Pool,
	store identityregistry.PublicationStore,
) *PostgresIdentityStartupRecovery {
	return &PostgresIdentityStartupRecovery{pool: pool, store: store}
}

func (r *PostgresIdentityStartupRecovery) RecoverStaleIdentityPublication(
	ctx context.Context,
	publication identityregistry.Publication,
) (identityregistry.DurableState, bool, error) {
	if r == nil || r.pool == nil || r.store == nil || ctx == nil {
		return identityregistry.DurableState{}, false, errIdentityRecoveryUnavailable
	}
	store, ok := r.store.(identityregistry.TransactionalPublicationStore)
	if !ok {
		return identityregistry.DurableState{}, false, errIdentityRecoveryUnavailable
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return identityregistry.DurableState{}, false, fmt.Errorf("begin stale identity publication recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	durable, err := store.LoadDurableStateTx(ctx, tx)
	if err != nil {
		return identityregistry.DurableState{}, false, fmt.Errorf("load stale identity publication: %w", err)
	}
	if err := identityregistry.ValidateDurablePublicationTombstone(durable, publication); err != nil {
		return durable, false, nil
	}
	actorUserID, auditEventID, err := r.loadExactSourceRecoveryEvidence(ctx, tx, publication.Artifact)
	if errors.Is(err, pgx.ErrNoRows) {
		return durable, false, nil
	}
	if err != nil {
		return identityregistry.DurableState{}, false, err
	}
	recovered, err := store.ReconcileTx(ctx, tx, identityregistry.ReconcilePublicationInput{
		ExtensionID:   publication.Artifact.ExtensionID,
		AllowedSource: &publication.Artifact,
		AllowedTarget: &publication.Artifact,
		Desired:       &publication,
		ActorUserID:   actorUserID,
		AuditEventID:  auditEventID,
	})
	if err != nil {
		return identityregistry.DurableState{}, false, fmt.Errorf("append recovered identity publication: %w", err)
	}
	if err := identityregistry.ValidateDurablePublication(recovered, publication); err != nil {
		return identityregistry.DurableState{}, false, fmt.Errorf("validate recovered identity publication: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return identityregistry.DurableState{}, false, fmt.Errorf("commit stale identity publication recovery: %w", err)
	}
	return recovered, true, nil
}

func (r *PostgresIdentityStartupRecovery) loadExactSourceRecoveryEvidence(
	ctx context.Context,
	tx pgx.Tx,
	artifact identityregistry.Artifact,
) (int64, int64, error) {
	var actorUserID, auditEventID int64
	err := tx.QueryRow(ctx, `
		SELECT operation.requested_by_user_id, operation.audit_event_id
		FROM extension_lifecycle_operations AS operation
		JOIN extension_lifecycle_publications AS publication
		  ON publication.operation_id = operation.id
		 AND publication.operation = operation.operation
		 AND publication.publication_mode = 'deactivate'
		JOIN extension_lifecycle_registry_publications AS registry
		  ON registry.publication_id = publication.id
		 AND registry.operation_id = operation.id
		JOIN extension_lifecycle_state_publications AS state
		  ON state.operation_id = operation.id
		 AND state.step_id = publication.step_id
		 AND state.publication_mode = publication.publication_mode
		JOIN extensions AS extension
		  ON extension.id = operation.extension_id
		JOIN extension_versions AS active_version
		  ON active_version.id = extension.active_version_id
		 AND active_version.extension_id = extension.id
		JOIN audit_events AS audit
		  ON audit.id = operation.audit_event_id
		 AND audit.actor_user_id = operation.requested_by_user_id
		 AND audit.action = 'extension.disable'
		 AND audit.metadata ->> 'extensionId' = operation.extension_id
		 AND audit.metadata ->> 'version' = operation.extension_version
		 AND audit.metadata ->> 'packageDigest' = operation.package_digest
		WHERE operation.id = (
		  SELECT max(latest.id)
		  FROM extension_lifecycle_operations AS latest
		  WHERE latest.extension_id = operation.extension_id
		)
		  AND operation.extension_id = $1
		  AND operation.extension_version = $2
		  AND operation.package_digest = $3
		  AND operation.operation = 'disable'
		  AND operation.state = 'failed'
		  AND operation.terminal_result IN ('failed', 'cancelled')
		  AND operation.completed_at IS NOT NULL
		  AND operation.requested_by_user_id > 0
		  AND operation.audit_event_id > 0
		  AND extension.type = 'plugin'
		  AND extension.status = 'enabled'
		  AND active_version.id = $4
		  AND active_version.version = $2
		  AND active_version.package_digest = $3
		  AND publication.commit_marker = FALSE
		  AND publication.source_extension_id = $1
		  AND publication.source_extension_version = $2
		  AND publication.source_package_digest = $3
		  AND publication.source_version_id = $4
		  AND publication.last_attempt = operation.attempt_count
		  AND registry.transaction_state = 'source'
		  AND registry.source_extension_id = $1
		  AND registry.source_extension_version = $2
		  AND registry.source_package_digest = $3
		  AND registry.source_version_id = $4
		  AND registry.last_attempt = operation.attempt_count
		  AND state.transaction_state = 'source'
		  AND state.source_status = 'enabled'
		  AND state.source_active_version_id = $4
		  AND state.source_active_version = $2
		  AND state.source_active_package_digest = $3
		  AND state.last_attempt = operation.attempt_count
		LIMIT 1
		FOR UPDATE OF operation, publication, registry, state, extension, active_version, audit
	`, artifact.ExtensionID, artifact.ExtensionVersion, artifact.PackageDigest, artifact.VersionID).Scan(
		&actorUserID,
		&auditEventID,
	)
	if err != nil {
		return 0, 0, err
	}
	return actorUserID, auditEventID, nil
}
