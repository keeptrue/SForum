package attachments

import (
	"context"
	"fmt"
	"time"
)

// referenceRecountStatsSQL 一次快照内同时给出漂移方向统计与样本 id。
// 刻意不返回全表行，只回计数与最多 20 个样本，避免大站把明细拉到应用层。
const referenceRecountStatsSQL = `
WITH live AS (
  SELECT a.id, a.reference_count AS stored, count(r.id) AS actual
  FROM attachments a
  LEFT JOIN attachment_references r ON r.attachment_id = a.id
  GROUP BY a.id, a.reference_count
), drift AS (
  SELECT id, stored, actual FROM live WHERE stored <> actual
)
SELECT
  (SELECT count(*) FROM live) AS total,
  (SELECT count(*) FROM drift WHERE stored > actual) AS lowered,
  (SELECT count(*) FROM drift WHERE stored < actual) AS raised,
  (SELECT array_agg(id ORDER BY id) FROM (SELECT id FROM drift ORDER BY id LIMIT 20) sampled) AS samples
`

// referenceRecountApplySQL 只改漂移行，重复执行结果不变（幂等）。
const referenceRecountApplySQL = `
UPDATE attachments a SET reference_count = live.actual, updated_at = now()
FROM (
  SELECT a2.id, count(r.id) AS actual
  FROM attachments a2
  LEFT JOIN attachment_references r ON r.attachment_id = a2.id
  GROUP BY a2.id
) live
WHERE a.id = live.id AND a.reference_count <> live.actual
`

// ReconcileReferenceCounts 统计并（可选）修正 reference_count 漂移。
// dryRun=true 只读：事务内只有 SELECT，绝不执行 UPDATE。
func (s *PostgresStore) ReconcileReferenceCounts(ctx context.Context, dryRun bool) (RecountResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RecountResult{}, fmt.Errorf("begin reference recount: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var total, lowered, raised int64
	var samples []int64
	if err := tx.QueryRow(ctx, referenceRecountStatsSQL).Scan(&total, &lowered, &raised, &samples); err != nil {
		return RecountResult{}, fmt.Errorf("count reference drift: %w", err)
	}
	result := RecountResult{
		Lowered:   int(lowered),
		Raised:    int(raised),
		Unchanged: int(total - lowered - raised),
		Applied:   !dryRun,
		Samples:   samples,
	}
	if result.Samples == nil {
		result.Samples = []int64{}
	}
	if dryRun {
		return result, nil
	}
	if _, err := tx.Exec(ctx, referenceRecountApplySQL); err != nil {
		return RecountResult{}, fmt.Errorf("apply reference recount: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RecountResult{}, fmt.Errorf("commit reference recount: %w", err)
	}
	return result, nil
}

// CountDeferredCleanupCandidates 统计「无引用、未删除、但还没到保留宽限期」的附件数。
// 与 ListCleanupCandidates 的孤儿口径互补：前者是本次不动、到期自动清理的部分。
func (s *PostgresStore) CountDeferredCleanupCandidates(ctx context.Context, cutoff time.Time) (int, error) {
	var count int64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM attachments
		WHERE reference_count = 0
		  AND status <> 'deleted'
		  AND created_at > $1
	`, cutoff).Scan(&count); err != nil {
		return 0, fmt.Errorf("count deferred cleanup candidates: %w", err)
	}
	return int(count), nil
}
