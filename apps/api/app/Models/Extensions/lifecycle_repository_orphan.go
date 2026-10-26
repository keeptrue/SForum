package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

const (
	// 批量默认 16、上限 64：回收跑在启动栅栏路径上，单轮必须小而快，
	// 不允许一次事务拖住整个 genesis 预算。
	lifecycleOrphanReclaimDefaultLimit = 16
	lifecycleOrphanReclaimMaxLimit     = 64
	// 从未 claim 过 lease 的开放操作要求 updated_at 早于该宽限才可回收
	// （一个 lease 周期 30s + 心跳余量）：避免把"刚创建、协调器还没来得及
	// claim"的活操作误杀。曾有 claim 的操作不受此宽限约束——lease 过期本身
	// 已经证明持有人静默超过一个完整租期。
	lifecycleOrphanReclaimIdleGrace = "45 seconds"

	// lifecycleOrphanReclaimErrorCode 是回收留下的统一错误码；它与
	// lifecycle_repository.go 的终态常量组合使用，语义是"宿主确认放弃"。
	lifecycleOrphanReclaimErrorCode = "lifecycle.orphaned"
)

// ReclaimOrphanedLifecycleOperations 回收孤儿 lifecycle operation：开放
// （completed_at IS NULL）且不存在任何持有未过期 lease 的 running/waiting
// step 的操作。对每条命中操作，在单事务内锁行复核、把开放 step 置为
// failed、以 revision CAS 关闭操作（terminal_result=cancelled）并写审计。
// CAS 落空（并发关闭/状态漂移）只跳过该条，不算错误。返回回收的 operation id。
//
// 红线：绝不触碰仍有未过期 lease 的操作——那是"进程可能只是慢"，必须继续
// 阻塞 plugin runtime 收敛栅栏。
func (r *PostgresLifecycleRepository) ReclaimOrphanedLifecycleOperations(ctx context.Context, limit int) ([]int64, error) {
	if r == nil || r.pool == nil || ctx == nil {
		return nil, ErrLifecycleInvalidInput
	}
	if limit <= 0 {
		limit = lifecycleOrphanReclaimDefaultLimit
	}
	if limit > lifecycleOrphanReclaimMaxLimit {
		limit = lifecycleOrphanReclaimMaxLimit
	}
	candidates, err := r.listOrphanedLifecycleOperationCandidates(ctx, limit)
	if err != nil {
		return nil, err
	}
	reclaimed := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return reclaimed, err
		}
		closed, err := r.reclaimOneOrphanedLifecycleOperation(ctx, candidate)
		if err != nil {
			return reclaimed, err
		}
		if closed {
			reclaimed = append(reclaimed, candidate)
		}
	}
	return reclaimed, nil
}

// listOrphanedLifecycleOperationCandidates 筛选可回收的开放操作：
//   - 无任何持有未过期 lease 的 running/waiting step（没有活进程在推进它）；
//   - 且要么存在曾被 claim 过、现已过期的开放 step（持有人静默超过整个
//     租期，直接判定死亡），要么操作 updated_at 早于死亡宽限。
func (r *PostgresLifecycleRepository) listOrphanedLifecycleOperationCandidates(ctx context.Context, limit int) ([]int64, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id
		FROM extension_lifecycle_operations o
		WHERE o.completed_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM extension_lifecycle_steps s
			WHERE s.operation_id = o.id
			  AND s.status IN ('running', 'waiting')
			  AND s.lease_expires_at > statement_timestamp()
		  )
		  AND (
			EXISTS (
				SELECT 1 FROM extension_lifecycle_steps s2
				WHERE s2.operation_id = o.id
				  AND s2.status IN ('planned', 'running', 'waiting')
				  AND s2.lease_owner_token <> ''
			)
			OR o.updated_at < statement_timestamp() - interval '`+lifecycleOrphanReclaimIdleGrace+`'
		  )
		ORDER BY o.updated_at ASC, o.id ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list orphaned lifecycle operations: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan orphaned lifecycle operation: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orphaned lifecycle operations: %w", err)
	}
	return ids, nil
}

func (r *PostgresLifecycleRepository) reclaimOneOrphanedLifecycleOperation(ctx context.Context, operationID int64) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin lifecycle orphan reclaim: %w", err)
	}
	defer tx.Rollback(ctx)

	var revision int64
	var state, extensionID string
	var completedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT revision, state, extension_id, completed_at
		FROM extension_lifecycle_operations
		WHERE id = $1
		FOR UPDATE
	`, operationID).Scan(&revision, &state, &extensionID, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock lifecycle orphan reclaim: %w", err)
	}
	// 候选清单与加锁之间可能已被并发路径（另一个节点/CLI）关闭；静默跳过。
	if completedAt != nil {
		return false, nil
	}

	const message = "host reclaimed the orphaned lifecycle operation; no live lease was held by any step"
	stepTag, err := tx.Exec(ctx, `
		UPDATE extension_lifecycle_steps
		SET status = 'failed',
		    error_code = $2, error_reason = $2, error_message = $3,
		    error_retryable = false,
		    lease_owner_token = '', lease_expires_at = NULL, lease_heartbeat_at = NULL,
		    completed_at = statement_timestamp(), updated_at = statement_timestamp()
		WHERE operation_id = $1 AND status IN ('planned', 'running', 'waiting')
	`, operationID, lifecycleOrphanReclaimErrorCode, message)
	if err != nil {
		return false, fmt.Errorf("fail orphaned lifecycle steps: %w", err)
	}

	// revision CAS：即便已持行锁，仍保持与 CompleteOperation 相同的写法，
	// 让并发语义与正常关闭路径完全一致。
	tag, err := tx.Exec(ctx, `
		UPDATE extension_lifecycle_operations
		SET state = 'failed', terminal_result = 'cancelled',
		    error_code = $2, error_reason = $2, error_message = $3,
		    error_retryable = false,
		    completed_at = statement_timestamp(), updated_at = statement_timestamp(),
		    revision = revision + 1
		WHERE id = $1 AND revision = $4 AND state = $5 AND completed_at IS NULL
	`, operationID, lifecycleOrphanReclaimErrorCode, message, revision, state)
	if err != nil {
		return false, fmt.Errorf("close orphaned lifecycle operation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		// CAS 落空说明状态在筛选后发生了变化；跳过即可，不算错误。
		return false, nil
	}

	metadata, err := json.Marshal(map[string]any{
		"operationId": operationID,
		"extensionId": extensionID,
		"reason":      lifecycleOrphanReclaimErrorCode,
		"closedSteps": stepTag.RowsAffected(),
	})
	if err != nil {
		return false, fmt.Errorf("marshal lifecycle orphan reclaim audit: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (action, metadata)
		VALUES ($1, $2::jsonb)
	`, audit.ActionExtensionLifecycleOrphanReclaim, string(metadata)); err != nil {
		return false, fmt.Errorf("record lifecycle orphan reclaim audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit lifecycle orphan reclaim: %w", err)
	}
	return true, nil
}

// ReclaimOrphanedLifecycleOperations 是 PostgresStore 对孤儿回收的薄封装，
// 暴露给 bootstrap genesis 与 plugin runtime coordinator 的可选窄接口断言。
func (s *PostgresStore) ReclaimOrphanedLifecycleOperations(ctx context.Context, limit int) ([]int64, error) {
	if s == nil || s.pool == nil {
		return nil, ErrLifecycleInvalidInput
	}
	return NewPostgresLifecycleRepository(s.pool).ReclaimOrphanedLifecycleOperations(ctx, limit)
}
