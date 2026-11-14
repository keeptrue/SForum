package trustlevelsjobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// RecomputeBatchKind 是「按游标翻一页用户、逐个入队重算」job kind。
//
// 两种来源共用同一个 kind，由 Mode 区分：策略保存后 mode=all（全员按新门槛重算），
// 每日 sweep mode=stale（只碰账本缺失或太久没算过的人）。
const RecomputeBatchKind = "trustlevels.recompute_batch"

// RecomputeBatchArgs 里 Cursor 同时是分页位置与去重维度：ByArgs 让「同一起点的
// 重复触发」合并，而不同起点（下一页）各自独立。
type RecomputeBatchArgs struct {
	Mode   string `json:"mode"`
	Cursor int64  `json:"cursor"`
	// Batch 是每页用户数；0 表示由领域层取默认值。
	Batch int `json:"batch,omitempty"`
}

func (RecomputeBatchArgs) Kind() string { return RecomputeBatchKind }

func (RecomputeBatchArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 3,
		Unique:      river.UniqueOpts{ByArgs: true},
	}
}

// BatchExecutor 执行一页（实现：trustlevels.BatchRunner）。
type BatchExecutor interface {
	RunBatch(ctx context.Context, mode string, cursor int64, batch int) (trustlevels.BatchResult, error)
}

// BatchEnqueuer 把「下一页」再排进队列（实现：bootstrap 的 dispatcher 适配器）。
type BatchEnqueuer interface {
	EnqueueRecomputeBatch(ctx context.Context, args RecomputeBatchArgs) error
}

// RecomputeBatchWorker 执行一页并自链下一页。
//
// 为什么用自链而不是一个 job 里 for 循环扫全表：一次 job 扫 10 万人会把 worker
// 的池按小时占住，且中途失败要整轮重来。自链让每页各自重试、各自去重，
// 也天然把压力摊到多个 job 上。
type RecomputeBatchWorker struct {
	river.WorkerDefaults[RecomputeBatchArgs]
	Executor BatchExecutor
	Enqueuer BatchEnqueuer
	Logger   *slog.Logger
}

func (w *RecomputeBatchWorker) Work(ctx context.Context, job *river.Job[RecomputeBatchArgs]) error {
	if w.Executor == nil {
		return fmt.Errorf("trust level batch worker requires an executor")
	}
	if job == nil {
		return nil
	}
	result, err := w.Executor.RunBatch(ctx, job.Args.Mode, job.Args.Cursor, job.Args.Batch)
	if err != nil {
		return fmt.Errorf("run trust level recompute batch (mode=%s cursor=%d): %w",
			job.Args.Mode, job.Args.Cursor, err)
	}
	if w.Logger != nil {
		w.Logger.Info("trust level recompute batch",
			"mode", job.Args.Mode, "cursor", job.Args.Cursor,
			"processed", result.Processed, "next_cursor", result.NextCursor, "done", result.Done)
	}
	if result.Done || result.Processed == 0 {
		return nil
	}
	if w.Enqueuer == nil {
		return fmt.Errorf("trust level batch worker requires an enqueuer to chain the next page")
	}
	return w.Enqueuer.EnqueueRecomputeBatch(ctx, RecomputeBatchArgs{
		Mode:   job.Args.Mode,
		Cursor: result.NextCursor,
		Batch:  job.Args.Batch,
	})
}

// RegisterRecomputeBatch 注册批量 worker。
func RegisterRecomputeBatch(
	registry *supportjobs.Registry,
	executor BatchExecutor,
	enqueuer BatchEnqueuer,
	logger *slog.Logger,
) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[RecomputeBatchArgs](workers, &RecomputeBatchWorker{
			Executor: executor,
			Enqueuer: enqueuer,
			Logger:   logger,
		})
	})
}
