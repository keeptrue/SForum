package badges

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 回扫运行状态。枚举值即契约（前端/巡检脚本按字符串判定），不要改字面量。
const (
	BackfillStatusRunning   = "running"
	BackfillStatusCompleted = "completed"
	BackfillStatusFailed    = "failed"
)

// MaxBackfillErrorLength 是 error 列的截断长度。
//
// 为什么截断：失败信息可能是一整条 pgx 错误链（含 SQL 与参数），原样入库会把一行
// 变成几十 KB，运维读端点也会被噪音淹没。500 字符足够保住「哪个约束/哪句话」。
const MaxBackfillErrorLength = 500

// BackfillRunStatus 是 `GET /admin/badges/backfill-status` 的单行（wire 形状）。
//
// Slug/Name 来自 badges 表 JOIN：徽章被删时整行消失（FK CASCADE 已兜），
// 所以这里不需要「未知徽章」这种状态。
type BackfillRunStatus struct {
	BadgeID            int64      `json:"badgeId"`
	Slug               string     `json:"slug"`
	Name               string     `json:"name"`
	Status             string     `json:"status"`
	LastUserID         int64      `json:"lastUserId"`
	FailedCursorUserID int64      `json:"failedCursorUserId"`
	StartedAt          time.Time  `json:"startedAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	FinishedAt         *time.Time `json:"finishedAt"`
	Error              string     `json:"error"`
}

// BackfillRunReader 是进度读口（HTTP 只依赖这一条）。
type BackfillRunReader interface {
	// ListLatestBackfillRuns 返回每个徽章**最新**一次回扫，按 started_at 倒序。
	// 没有任何 run 时返回空切片（不是错误：空 items 是 200 正常结果）。
	ListLatestBackfillRuns(ctx context.Context) ([]BackfillRunStatus, error)
}

// BackfillRunWriter 是进度写口（只有 job 侧调用：入队侧建行、每页推进/失败）。
//
// 三个方法都按「该徽章最新一行」更新，而不是按 run id：job args 的形状
// （`{badgeId,cursorUserId,batch}`）是冻结的去重键，里面没有 run id，硬塞一个会
// 破坏 river 的 ByArgs 去重语义。
type BackfillRunWriter interface {
	// StartBackfillRun 在入队前建一行 running。
	StartBackfillRun(ctx context.Context, badgeID int64, batch int) error
	// AdvanceBackfillRun 记录一页成功：done=false → running + 新游标，
	// done=true → completed + finished_at。成功即清空 error 与 failed_cursor_user_id
	// （重试成功后必须把 failed 翻回 running，否则进度永久停在失败页）。
	AdvanceBackfillRun(ctx context.Context, badgeID int64, lastUserID int64, done bool) error
	// FailBackfillRun 记录一页失败：failed + 失败页游标 + 原因，finished_at 置空
	// （river 还会重试，失败不是结束）。
	FailBackfillRun(ctx context.Context, badgeID int64, failedCursorUserID int64, message string) error
}

// SetBackfillRuns 注入回扫进度读口（bootstrap 装配；未注入时端点回空 items）。
func (a *AdminService) SetBackfillRuns(runs BackfillRunReader) {
	if a == nil {
		return
	}
	a.runs = runs
}

// BackfillStatus 读每个徽章的最新回扫状态。
//
// 权限与 `RuleHealth` 同口径：读 `badge.view`。空结果是正常结果（还没人点过重算），
// 不是 404——把「没跑过」伪装成「不存在」会让运营以为端点坏了。
func (a *AdminService) BackfillStatus(ctx context.Context, actor identity.Actor) ([]BackfillRunStatus, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return nil, err
	}
	if a == nil || a.runs == nil {
		return []BackfillRunStatus{}, nil
	}
	items, err := a.runs.ListLatestBackfillRuns(ctx)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []BackfillRunStatus{}
	}
	return items, nil
}

// PostgresBackfillRuns 是 BackfillRunReader + BackfillRunWriter 的 pgx 实现。
type PostgresBackfillRuns struct {
	pool *pgxpool.Pool
}

func NewPostgresBackfillRuns(pool *pgxpool.Pool) *PostgresBackfillRuns {
	return &PostgresBackfillRuns{pool: pool}
}

// latestRunIDSubquery 选中「该徽章最新一行」。
//
// 排序键与读端点一致（started_at DESC, id DESC）：同一毫秒内的两行靠 id 决出先后。
const latestBackfillRunIDSubquery = `
  SELECT id FROM badge_rule_backfill_runs
  WHERE badge_id = $1
  ORDER BY started_at DESC, id DESC
  LIMIT 1`

func (s *PostgresBackfillRuns) StartBackfillRun(ctx context.Context, badgeID int64, batch int) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("badge backfill runs store is not wired")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO badge_rule_backfill_runs (badge_id, status, batch)
		VALUES ($1, 'running', $2)
	`, badgeID, nullableBatch(batch))
	if err != nil {
		return fmt.Errorf("start badge backfill run for badge %d: %w", badgeID, err)
	}
	return nil
}

func (s *PostgresBackfillRuns) AdvanceBackfillRun(ctx context.Context, badgeID int64, lastUserID int64, done bool) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("badge backfill runs store is not wired")
	}
	status := BackfillStatusRunning
	if done {
		status = BackfillStatusCompleted
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE badge_rule_backfill_runs
		SET status = $2,
		    last_user_id = $3,
		    failed_cursor_user_id = 0,
		    error = '',
		    updated_at = now(),
		    finished_at = CASE WHEN $2 = 'completed' THEN now() ELSE NULL END
		WHERE id = (`+latestBackfillRunIDSubquery+`)
	`, badgeID, status, lastUserID)
	if err != nil {
		return fmt.Errorf("advance badge backfill run for badge %d: %w", badgeID, err)
	}
	// 0 行受影响 = 该徽章还没有 run 行（旧 job 或入队侧建行失败）。这里刻意**不报错**：
	// 报错会让 river 反复重试同一页，而重试也永远写不进去，只会把「一页几秒」变成
	// 「一页永远」。入队侧建行失败已由 `StartBackfillRun` 的调用方上抛，不会静默。
	_ = tag
	return nil
}

func (s *PostgresBackfillRuns) FailBackfillRun(ctx context.Context, badgeID int64, failedCursorUserID int64, message string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("badge backfill runs store is not wired")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE badge_rule_backfill_runs
		SET status = 'failed',
		    failed_cursor_user_id = $2,
		    error = $3,
		    updated_at = now(),
		    finished_at = NULL
		WHERE id = (`+latestBackfillRunIDSubquery+`)
	`, badgeID, failedCursorUserID, truncateBackfillError(message))
	if err != nil {
		return fmt.Errorf("fail badge backfill run for badge %d: %w", badgeID, err)
	}
	return nil
}

func (s *PostgresBackfillRuns) ListLatestBackfillRuns(ctx context.Context) ([]BackfillRunStatus, error) {
	if s == nil || s.pool == nil {
		return []BackfillRunStatus{}, nil
	}
	// DISTINCT ON 先按 badge_id 分组取最新，再整表按 started_at 倒序输出：
	// 内层排序是 DISTINCT ON 的硬要求，外层排序才是给运营看的顺序。
	rows, err := s.pool.Query(ctx, `
		SELECT runs.badge_id, badges.slug, badges.name, runs.status,
		       runs.last_user_id, runs.failed_cursor_user_id,
		       runs.started_at, runs.updated_at, runs.finished_at, runs.error
		FROM (
			SELECT DISTINCT ON (badge_id) *
			FROM badge_rule_backfill_runs
			ORDER BY badge_id, started_at DESC, id DESC
		) AS runs
		JOIN badges ON badges.id = runs.badge_id
		ORDER BY runs.started_at DESC, runs.badge_id`)
	if err != nil {
		return nil, fmt.Errorf("list badge backfill runs: %w", err)
	}
	defer rows.Close()

	items := []BackfillRunStatus{}
	for rows.Next() {
		var item BackfillRunStatus
		var finishedAt *time.Time
		if err := rows.Scan(&item.BadgeID, &item.Slug, &item.Name, &item.Status,
			&item.LastUserID, &item.FailedCursorUserID,
			&item.StartedAt, &item.UpdatedAt, &finishedAt, &item.Error); err != nil {
			return nil, fmt.Errorf("scan badge backfill run: %w", err)
		}
		item.FinishedAt = finishedAt
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate badge backfill runs: %w", err)
	}
	return items, nil
}

// nullableBatch 把 batch<=0 落成 NULL（列语义：NULL = 未知/没记录）。
func nullableBatch(batch int) *int {
	if batch <= 0 {
		return nil
	}
	return &batch
}

// truncateBackfillError 按**字符**截断（不是字节）：中文错误信息按字节截会切成乱码。
func truncateBackfillError(message string) string {
	trimmed := strings.TrimSpace(message)
	runes := []rune(trimmed)
	if len(runes) <= MaxBackfillErrorLength {
		return trimmed
	}
	return string(runes[:MaxBackfillErrorLength])
}
