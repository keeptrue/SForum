package badgesjobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// BackfillRulesKind 是「按新规则回扫全站用户」的 job kind。
//
// 为什么必须异步：保存规则后立刻全量评估会让一次 HTTP 请求变成「遍历所有用户 ×
// 跑一遍条件解析器」，正是既有残留 #17（RecomputeAll 在请求内同步跑）不能扩大的
// 那类形态。HTTP 只入队并回执 `queued` 数量。
const BackfillRulesKind = "badges.backfill_rules"

// BackfillRulesArgs 是回扫 job 的参数。
//
// 游标是 `user_id` 而不是偏移量：回扫过程中用户可能被插入/删除，OFFSET 会漏人或
// 重复；按 id 递增游标天然可续。`Batch` 进参数是为了让「同一页」具备稳定的去重键
// ——river 的 ByArgs 唯一性因此等价于「同一徽章的同一页不会被并发跑两次」。
type BackfillRulesArgs struct {
	BadgeID      int64 `json:"badgeId"`
	CursorUserID int64 `json:"cursorUserId"`
	Batch        int   `json:"batch"`
}

func (BackfillRulesArgs) Kind() string { return BackfillRulesKind }

// EnqueueOptions 固定队列与重试策略。
func (BackfillRulesArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueDefault,
		MaxAttempts: 3,
	}
}

// PageEnqueuer 入队下一页。
//
// 用接口而不是直接依赖 `*supportjobs.Dispatcher`：worker 注册发生在 River client
// 建好之前，装配层需要「先注册、后绑定」的延迟实现（见 bootstrap 的
// deferredBackfillEnqueuer）。
type PageEnqueuer interface {
	Enqueue(ctx context.Context, args BackfillRulesArgs) error
}

// RuleBackfillRunner 执行一页回扫，返回下一页游标与是否结束。
type RuleBackfillRunner interface {
	BackfillRuleBatch(ctx context.Context, badgeID int64, afterUserID int64, batch int) (nextCursor int64, done bool, err error)
}

// BackfillRunRecorder 记录回扫进度（实现见 Models/Badges 的 PostgresBackfillRuns）。
//
// 只有两个方法：入队侧建 running 行不归 worker 管（那一行必须在 job 还没被消费时
// 就存在，否则「卡在队列里」这种最常见的故障反而看不见）。worker 只负责推进与失败。
//
// nil 容错：未注入时退化成旧行为（只写日志），这样装配漏接不会让回扫停摆——但
// 「进度可见」就退化了，所以 bootstrap 一定接上。
type BackfillRunRecorder interface {
	AdvanceBackfillRun(ctx context.Context, badgeID int64, lastUserID int64, done bool) error
	FailBackfillRun(ctx context.Context, badgeID int64, failedCursorUserID int64, message string) error
}

// BackfillRulesWorker 逐页回扫并在未结束时把下一页入队。
//
// 一页一个 job（而不是一个 job 跑到底）的理由：长任务在 worker 重启/部署时会整段
// 重跑，而按页推进最多重跑一页；进度也天然可观察（job 列表里能看到游标前进）。
//
// 每个终止分支都要落一行状态：一页一个 job 的代价是「没有人负责说结束」，
// 链断在第 N 页而没有任何地方报错，运营看到的就是「回扫好像跑完了」。
type BackfillRulesWorker struct {
	river.WorkerDefaults[BackfillRulesArgs]
	Runner   RuleBackfillRunner
	Enqueuer PageEnqueuer
	Runs     BackfillRunRecorder
	Logger   *slog.Logger
}

func (w *BackfillRulesWorker) Work(ctx context.Context, job *river.Job[BackfillRulesArgs]) error {
	if w.Runner == nil {
		return fmt.Errorf("badges backfill worker requires runner")
	}
	if job == nil || job.Args.BadgeID <= 0 {
		return nil
	}
	badgeID := job.Args.BadgeID
	batch := job.Args.Batch
	if batch <= 0 {
		batch = DefaultBackfillBatch
	}
	next, done, err := w.Runner.BackfillRuleBatch(ctx, badgeID, job.Args.CursorUserID, batch)
	if err != nil {
		// 这一页会由 river 重试；重试用尽后整条回扫链就停在这里，而运营侧早就拿到
		// 了 `queued: N` 的回执。没有这条日志就完全看不出「卡在第几个用户」。
		if w.Logger != nil {
			w.Logger.Error("badge rule backfill page failed",
				"badge_id", badgeID, "cursor_user_id", job.Args.CursorUserID, "error", err)
		}
		w.recordFailure(ctx, badgeID, job.Args.CursorUserID, err.Error())
		// 主错必须原样上抛：状态写失败不能把它换成「状态写失败」。
		return fmt.Errorf("backfill badge %d after user %d: %w", badgeID, job.Args.CursorUserID, err)
	}
	if done {
		// 末页：先记 completed 再结束。写失败要上抛让 river 重试当前页——页本身幂等
		// （评估与授予都是 ON CONFLICT DO NOTHING），重跑一页换一条正确的结束状态很值。
		if w.Runs != nil {
			if recordErr := w.Runs.AdvanceBackfillRun(ctx, badgeID, next, true); recordErr != nil {
				return fmt.Errorf("record completed badge backfill for badge %d: %w", badgeID, recordErr)
			}
		}
		return nil
	}
	if w.Enqueuer == nil {
		// 链断在这里：本页成功了，但没人接手下一页。旧实现直接 return nil，等于
		// 「回扫完成」的假象。现在把它落成 failed，运维在状态端点就能看见。
		w.recordFailure(ctx, badgeID, job.Args.CursorUserID, "backfill enqueuer not wired")
		return nil
	}
	// 非末页：先推进游标再入队。顺序固定——入队失败时状态里留下的游标必须是
	// 「确实跑完的那一页」，否则重试会让人以为下一页已经开始了。
	if w.Runs != nil {
		if recordErr := w.Runs.AdvanceBackfillRun(ctx, badgeID, next, false); recordErr != nil {
			return fmt.Errorf("record badge backfill progress for badge %d: %w", badgeID, recordErr)
		}
	}
	args := BackfillRulesArgs{BadgeID: badgeID, CursorUserID: next, Batch: batch}
	if err := w.Enqueuer.Enqueue(ctx, args); err != nil {
		// 入队失败要返回错误让 river 重试当前页：静默停在半路等于「回扫完成」的假象。
		return fmt.Errorf("enqueue next backfill page: %w", err)
	}
	return nil
}

// recordFailure 写 failed 状态；写失败只记日志，绝不覆盖主错。
func (w *BackfillRulesWorker) recordFailure(ctx context.Context, badgeID int64, failedCursorUserID int64, message string) {
	if w.Runs == nil {
		return
	}
	if err := w.Runs.FailBackfillRun(ctx, badgeID, failedCursorUserID, message); err != nil && w.Logger != nil {
		w.Logger.Error("record badge rule backfill failure",
			"badge_id", badgeID, "failed_cursor_user_id", failedCursorUserID, "error", err)
	}
}

// DefaultBackfillBatch 是每页用户数。500 在 1 万用户量级上是「一页几秒」的粒度，
// 单页失败重跑代价可控。
const DefaultBackfillBatch = 500

// RegisterBackfillRules 注册回扫 worker。
func RegisterBackfillRules(registry *supportjobs.Registry, runner RuleBackfillRunner, enqueuer PageEnqueuer, runs BackfillRunRecorder, logger *slog.Logger) {
	RegisterBackfillRulesWorker(registry, NewBackfillRulesWorker(runner, enqueuer, runs, logger))
}

// NewBackfillRulesWorker 是唯一的 worker 构造点：注册路径与装配守卫测试都必须走
// 这里，否则「测试里接上了 Runs，装配路径忘了接」这种偏差不会有人发现。
func NewBackfillRulesWorker(runner RuleBackfillRunner, enqueuer PageEnqueuer, runs BackfillRunRecorder, logger *slog.Logger) *BackfillRulesWorker {
	return &BackfillRulesWorker{Runner: runner, Enqueuer: enqueuer, Runs: runs, Logger: logger}
}

// RegisterBackfillRulesWorker 注册一个已经组装好的 worker。
func RegisterBackfillRulesWorker(registry *supportjobs.Registry, worker *BackfillRulesWorker) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[BackfillRulesArgs](workers, worker)
	})
}
