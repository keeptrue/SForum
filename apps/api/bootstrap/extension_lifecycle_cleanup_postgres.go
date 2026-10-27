package bootstrap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
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
	// identityRetained 表示本次 purge 没有物理删除 extensions 行：
	// 精确版本已被不可变 runtime publication 钉住，删身份就得删证据。
	identityRetained bool
}

// beginProductionLifecycleCleanupIdentity 打开 purge 事务并完成 exact-artifact 复核。
//
// 隐式契约：返回的 identity 持有未提交事务，**调用方必须自己 defer identity.rollback()**。
// commitPurge 的失败路径不会回滚（它只返回错误），生产调用方在
// extension_lifecycle_cleanup.go 里已经 defer；任何新的调用点漏掉这一句，事务会一直
// 持有 extensions 的行锁，直到连接池关闭时把调用方挂住。
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
		// publication member 对 extension_versions 是 RESTRICT，且 member 表有
		// BEFORE DELETE 触发器直接拒绝删除（append-only 证据）。因此"版本进过
		// desired full-set"的扩展不能硬删身份——那等于为了删身份去删证据。
		// 与正常卸载路径同一条语义（lifecycle.go 的 isPublishedPluginRuntimeIdentityRetained）：
		// 保留身份，扩展留 disabled，receipt 标 identityPurged=false。
		pinned, err := i.publishedRuntimeIdentityPinned(ctx)
		if err != nil {
			return err
		}
		if pinned {
			i.identityRetained = true
			if _, err := i.tx.Exec(ctx, `DELETE FROM mail_provider_selection WHERE extension_id = $1`, i.request.RetainedExtensionID); err != nil {
				return fmt.Errorf("clear lifecycle mail provider identity: %w", err)
			}
			if err := i.recordPurgeEvidence(ctx, nil); err != nil {
				return err
			}
			if err := i.tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit production lifecycle cleanup identity: %w", err)
			}
			i.finished = true
			return nil
		}
		if _, err := i.tx.Exec(ctx, `DELETE FROM mail_provider_selection WHERE extension_id = $1`, i.request.RetainedExtensionID); err != nil {
			return fmt.Errorf("clear lifecycle mail provider identity: %w", err)
		}
		// extensions 上还有 5 张 ON DELETE RESTRICT 子表，必须按「先子后父」清空：
		// extension_version_gc_removals（版本被 GC 的锚点）、
		// extension_missing_artifact_removals（制品丢失的锚点）、
		// extension_builtin_removals、attachment_storage_instances、
		// notification_channel_selections。RESTRICT 是刻意的完整性语义，这里只能
		// 逐表清行，不许改成 CASCADE 绕过；清了多少行要留审计，不能静默丢弃证据。
		cleared, err := i.clearRestrictedExtensionChildren(ctx)
		if err != nil {
			return err
		}
		if err := i.recordPurgeEvidence(ctx, cleared); err != nil {
			return err
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

// restrictedExtensionChildTables 是 extensions 上 ON DELETE RESTRICT 的子表。
// 顺序按「先清台账、再清身份引用」排列；这些表彼此没有引用，顺序只影响可读性。
var restrictedExtensionChildTables = []string{
	"extension_version_gc_removals",
	"extension_missing_artifact_removals",
	"extension_builtin_removals",
	"attachment_storage_instances",
	"notification_channel_selections",
}

func (i *productionLifecycleCleanupIdentity) clearRestrictedExtensionChildren(
	ctx context.Context,
) (map[string]int64, error) {
	cleared := make(map[string]int64, len(restrictedExtensionChildTables))
	for _, table := range restrictedExtensionChildTables {
		tag, err := i.tx.Exec(ctx,
			`DELETE FROM `+table+` WHERE extension_id = $1`, i.request.RetainedExtensionID,
		)
		if err != nil {
			return nil, fmt.Errorf("clear lifecycle restricted child %s: %w", table, err)
		}
		cleared[table] = tag.RowsAffected()
	}
	return cleared, nil
}

// recordPurgeEvidence 把清掉的子表行数写进审计：墓碑行本身被删除，但"删了什么"
// 必须留下证据，否则 uninstall 会变成一次静默的数据消失。
func (i *productionLifecycleCleanupIdentity) recordPurgeEvidence(
	ctx context.Context,
	cleared map[string]int64,
) error {
	metadata, err := json.Marshal(map[string]any{
		"mode":          "lifecycle_purge",
		"cleanupId":     i.request.CleanupID,
		"operationId":   i.request.OperationID,
		"extensionId":   i.request.RetainedExtensionID,
		"extensionVer":  i.request.RetainedExtensionVersion,
		"packageDigest": i.request.RetainedPackageDigest,
		"cleared":       cleared,
	})
	if err != nil {
		return fmt.Errorf("marshal lifecycle purge evidence: %w", err)
	}
	if _, err := i.tx.Exec(ctx, `
		INSERT INTO audit_events (action, metadata)
		VALUES ($1, $2::jsonb)
	`, audit.ActionExtensionUninstalled, string(metadata)); err != nil {
		return fmt.Errorf("record lifecycle purge evidence: %w", err)
	}
	return nil
}

// publishedRuntimeIdentityPinned 判定保留版本是否已被不可变 runtime publication 引用。
// 命中时只能保留身份：member 表 append-only，清证据再删身份在 schema 层被禁止。
func (i *productionLifecycleCleanupIdentity) publishedRuntimeIdentityPinned(ctx context.Context) (bool, error) {
	var pinned bool
	if err := i.tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM plugin_runtime_publication_members WHERE extension_version_id = $1
		)
	`, i.request.RetainedVersionID).Scan(&pinned); err != nil {
		return false, fmt.Errorf("inspect plugin runtime publication membership: %w", err)
	}
	return pinned, nil
}
