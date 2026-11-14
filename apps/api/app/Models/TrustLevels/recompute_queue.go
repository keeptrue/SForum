package trustlevels

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 批量重算的两种输入集。
//
//   - RecomputeModeAll：策略变更后「所有人都要按新门槛重算一次」；
//   - RecomputeModeStale：每日 sweep 兜底，只碰「账本缺失或太久没算过」的人——
//     角色变更 / OAuth 解绑 / 徽章收回这类变化**不产生活动事件**，没有 sweep 就永远
//     不会被发现。
const (
	RecomputeModeAll   = "all"
	RecomputeModeStale = "stale"
)

// RecomputeTargets 是批量重算的用户来源。按主键游标分页：一次扫全表会把
// worker 的池占满，而游标让每一页都是主键范围扫描。
type RecomputeTargets interface {
	ListUsersAfter(ctx context.Context, cursor int64, limit int) ([]int64, error)
	ListStaleAfter(ctx context.Context, cursor int64, limit int, staleBefore time.Time) ([]int64, error)
}

// PostgresRecomputeTargets 是 RecomputeTargets 的真库实现。
//
// 两个查询都只取**活跃人类用户**：bot 不进等级判定（D25），停用/封禁账号不产生
// 有价值的等级变更事件。
type PostgresRecomputeTargets struct {
	pool *pgxpool.Pool
}

func NewPostgresRecomputeTargets(pool *pgxpool.Pool) *PostgresRecomputeTargets {
	return &PostgresRecomputeTargets{pool: pool}
}

func (t *PostgresRecomputeTargets) ListUsersAfter(ctx context.Context, cursor int64, limit int) ([]int64, error) {
	return t.page(ctx, `
		SELECT id FROM public.users
		WHERE status = 'active' AND kind = 'human' AND id > $1
		ORDER BY id
		LIMIT $2`, cursor, limit)
}

func (t *PostgresRecomputeTargets) ListStaleAfter(ctx context.Context, cursor int64, limit int, staleBefore time.Time) ([]int64, error) {
	return t.page(ctx, `
		SELECT users.id
		FROM public.users
		LEFT JOIN public.user_trust_levels AS ledger ON ledger.user_id = users.id
		WHERE users.status = 'active' AND users.kind = 'human' AND users.id > $1
		  AND (ledger.user_id IS NULL OR ledger.computed_at < $3)
		ORDER BY users.id
		LIMIT $2`, cursor, limit, staleBefore.UTC())
}

func (t *PostgresRecomputeTargets) page(ctx context.Context, sql string, args ...any) ([]int64, error) {
	if t == nil || t.pool == nil {
		return nil, errors.New("trustlevels: recompute targets are not configured")
	}
	rows, err := t.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list recompute targets: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, 256)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan recompute target: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recompute targets: %w", err)
	}
	return ids, nil
}

// RecomputeFanout 把「一页用户」翻成逐个 recompute job（river 侧实现）。
//
// 批量入队而不是逐个入队：一页几百人时单条 Insert 会让 worker 自己变成瓶颈。
type RecomputeFanout interface {
	EnqueueUserRecompute(ctx context.Context, userIDs []int64) error
}

// BatchResult 是一页的结论：NextCursor 给下一页用，Done 表示扫到头了。
type BatchResult struct {
	Processed  int
	NextCursor int64
	Done       bool
}

// BatchRunner 执行一页批量重算：取用户 → 批量入队 → 回传游标。
//
// 它**不**自己跑等级判定：判定在 recompute job 里做，失败重试与去重都归 river。
type BatchRunner struct {
	targets  RecomputeTargets
	fanout   RecomputeFanout
	staleAge time.Duration
	now      func() time.Time
}

func NewBatchRunner(targets RecomputeTargets, fanout RecomputeFanout) *BatchRunner {
	return &BatchRunner{targets: targets, fanout: fanout, staleAge: 24 * time.Hour, now: time.Now}
}

// WithStaleAge 设置 sweep 的「太久没算过」阈值（默认 24h）。
func (r *BatchRunner) WithStaleAge(age time.Duration) *BatchRunner {
	if r != nil && age > 0 {
		r.staleAge = age
	}
	return r
}

// RunBatch 处理一页。mode 只接受 all/stale；未知 mode 直接报错（静默跑成 all
// 会让一次笔误把全站用户重算一遍）。
func (r *BatchRunner) RunBatch(ctx context.Context, mode string, cursor int64, limit int) (BatchResult, error) {
	if r == nil || r.targets == nil || r.fanout == nil {
		return BatchResult{}, errors.New("trustlevels: batch runner is not configured")
	}
	if limit <= 0 {
		limit = 500
	}
	var ids []int64
	var err error
	switch mode {
	case RecomputeModeAll:
		ids, err = r.targets.ListUsersAfter(ctx, cursor, limit)
	case RecomputeModeStale:
		ids, err = r.targets.ListStaleAfter(ctx, cursor, limit, r.now().Add(-r.staleAge))
	default:
		return BatchResult{}, fmt.Errorf("trustlevels: unknown recompute mode %q", mode)
	}
	if err != nil {
		return BatchResult{}, err
	}
	if len(ids) == 0 {
		return BatchResult{NextCursor: cursor, Done: true}, nil
	}
	if err := r.fanout.EnqueueUserRecompute(ctx, ids); err != nil {
		return BatchResult{}, err
	}
	return BatchResult{
		Processed:  len(ids),
		NextCursor: ids[len(ids)-1],
		// 满页不代表还有下一页（正好整除），但多跑一页空查询比漏人便宜。
		Done: len(ids) < limit,
	}, nil
}
