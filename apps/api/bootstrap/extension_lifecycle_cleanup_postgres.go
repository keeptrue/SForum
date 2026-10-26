package bootstrap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

var errProductionLifecycleCleanupConflict = errors.New("bootstrap: production lifecycle cleanup exact artifact conflict")

type productionLifecycleCleanupIdentity struct {
	tx                pgx.Tx
	request           extensionsruntime.LifecycleCleanupPurgeRequest
	runtimeInstanceID string
	actorUserID       sql.NullInt64
	present           bool
	finished          bool
}

func beginProductionLifecycleCleanupIdentity(
	ctx context.Context,
	pool *pgxpool.Pool,
	request extensionsruntime.LifecycleCleanupPurgeRequest,
) (*productionLifecycleCleanupIdentity, error) {
	if ctx == nil || pool == nil {
		return nil, errProductionLifecycleDependency
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin production lifecycle cleanup: %w", err)
	}
	identity := &productionLifecycleCleanupIdentity{tx: tx, request: request}
	ready := false
	defer func() {
		if !ready {
			_ = tx.Rollback(context.Background())
		}
	}()

	// 事务锁串行化物理 purge 与未知 commit 重试，避免 session 锁污染连接池。
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, productionLifecycleCleanupLockKey(request.CleanupID)); err != nil {
		return nil, fmt.Errorf("lock production lifecycle cleanup: %w", err)
	}
	if err = identity.loadExactRuntime(ctx); err != nil {
		return nil, err
	}
	if err = identity.lockExactExtension(ctx); err != nil {
		return nil, err
	}
	ready = true
	return identity, nil
}

func (i *productionLifecycleCleanupIdentity) loadExactRuntime(ctx context.Context) error {
	if i == nil || i.tx == nil {
		return errProductionLifecycleDependency
	}
	request := i.request
	err := i.tx.QueryRow(ctx, `
		SELECT cleanup.retained_runtime_instance_id, operation.requested_by_user_id
		FROM extension_lifecycle_cleanup_records AS cleanup
		JOIN extension_lifecycle_operations AS operation
		  ON operation.id = cleanup.operation_id
		WHERE cleanup.cleanup_id = $1
		  AND cleanup.operation_id = $2
		  AND cleanup.cleanup_mode = $3
		  AND cleanup.record_kind = 'uninstall_tombstone'
		  AND cleanup.status IN ('pending', 'finalized')
		  AND cleanup.retained_extension_id = $4
		  AND cleanup.retained_extension_version = $5
		  AND cleanup.retained_package_digest = $6
		  AND cleanup.retained_version_id = $7
		  AND cleanup.retained_package_path = $8
		  AND COALESCE(cleanup.retention_marker, '') = $9
		  AND COALESCE(cleanup.export_artifact_id, '') = $10
		  AND COALESCE(cleanup.export_digest, '') = $11
		  AND operation.operation = 'uninstall'
		  AND operation.extension_id = $4
		  AND operation.extension_version = $5
		  AND operation.package_digest = $6
		  AND operation.terminal_result = 'succeeded'
		  AND operation.completed_at IS NOT NULL
	`, request.CleanupID, request.OperationID, request.CleanupMode,
		request.RetainedExtensionID, request.RetainedExtensionVersion,
		request.RetainedPackageDigest, request.RetainedVersionID,
		request.RetainedPackagePath, request.RetentionMarker,
		request.ExportArtifactID, request.ExportDigest,
	).Scan(&i.runtimeInstanceID, &i.actorUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errProductionLifecycleCleanupConflict
	}
	if err != nil {
		return fmt.Errorf("load exact production lifecycle cleanup: %w", err)
	}
	if i.runtimeInstanceID == "" {
		return errProductionLifecycleCleanupConflict
	}
	return nil
}

func (i *productionLifecycleCleanupIdentity) lockExactExtension(ctx context.Context) error {
	request := i.request
	var activeVersionID sql.NullInt64
	err := i.tx.QueryRow(ctx, `
		SELECT active_version_id
		FROM extensions
		WHERE id = $1
		FOR UPDATE
	`, request.RetainedExtensionID).Scan(&activeVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		i.present = false
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock production lifecycle extension identity: %w", err)
	}
	if !activeVersionID.Valid || activeVersionID.Int64 != request.RetainedVersionID {
		return errProductionLifecycleCleanupConflict
	}

	var version, digest, packagePath string
	err = i.tx.QueryRow(ctx, `
		SELECT version, package_digest, package_path
		FROM extension_versions
		WHERE extension_id = $1 AND id = $2
		FOR KEY SHARE
	`, request.RetainedExtensionID, request.RetainedVersionID).Scan(&version, &digest, &packagePath)
	if errors.Is(err, pgx.ErrNoRows) {
		return errProductionLifecycleCleanupConflict
	}
	if err != nil {
		return fmt.Errorf("lock production lifecycle extension artifact: %w", err)
	}
	if version != request.RetainedExtensionVersion || digest != request.RetainedPackageDigest ||
		packagePath != request.RetainedPackagePath {
		return errProductionLifecycleCleanupConflict
	}
	i.present = true
	return nil
}

func (i *productionLifecycleCleanupIdentity) commitPurge(ctx context.Context) error {
	if i == nil || i.tx == nil || i.finished {
		return errProductionLifecycleDependency
	}
	if i.present {
		actorUserID := any(nil)
		if i.actorUserID.Valid && i.actorUserID.Int64 > 0 {
			actorUserID = i.actorUserID.Int64
		}
		if _, err := i.tx.Exec(ctx, `DELETE FROM page_provider_bindings WHERE extension_id = $1`, i.request.RetainedExtensionID); err != nil {
			return fmt.Errorf("clear lifecycle page provider bindings: %w", err)
		}
		if _, err := i.tx.Exec(ctx, `
			UPDATE extension_trust_grants
			SET revoked_at = statement_timestamp(),
			    revoked_by_user_id = $2,
			    revocation_reason = 'uninstall'
			WHERE extension_id = $1 AND revoked_at IS NULL
		`, i.request.RetainedExtensionID, actorUserID); err != nil {
			return fmt.Errorf("revoke lifecycle executable trust grants: %w", err)
		}
		if _, err := i.tx.Exec(ctx, `
			UPDATE extension_frontend_trust_grants
			SET revoked_at = statement_timestamp(),
			    revoked_by_user_id = $2
			WHERE extension_id = $1 AND revoked_at IS NULL
		`, i.request.RetainedExtensionID, actorUserID); err != nil {
			return fmt.Errorf("revoke lifecycle frontend trust grants: %w", err)
		}
		if _, err := i.tx.Exec(ctx, `DELETE FROM mail_provider_selection WHERE extension_id = $1`, i.request.RetainedExtensionID); err != nil {
			return fmt.Errorf("clear lifecycle mail provider identity: %w", err)
		}
		tag, err := i.tx.Exec(ctx, `
			DELETE FROM extensions
			WHERE id = $1 AND active_version_id = $2
		`, i.request.RetainedExtensionID, i.request.RetainedVersionID)
		if err != nil {
			return fmt.Errorf("delete exact lifecycle extension identity: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return errProductionLifecycleCleanupConflict
		}
	}
	if err := i.tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit production lifecycle cleanup identity: %w", err)
	}
	i.finished = true
	return nil
}

func (i *productionLifecycleCleanupIdentity) rollback() {
	if i == nil || i.tx == nil || i.finished {
		return
	}
	_ = i.tx.Rollback(context.Background())
	i.finished = true
}

func productionLifecycleCleanupLockKey(cleanupID string) int64 {
	digest := sha256.Sum256([]byte("sforum.lifecycle.cleanup\x00" + cleanupID))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}
