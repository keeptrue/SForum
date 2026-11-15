package forumjobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// FlushViewCountsArgs 将 Redis 浏览增量刷入 topics.view_count / hot_score。
type FlushViewCountsArgs struct{}

func (FlushViewCountsArgs) Kind() string { return "forum.flush_view_counts" }

func (FlushViewCountsArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 3,
	}
}

// FlushViewCountsWorker 周期任务：读 Redis 增量 → 落 PG → 扣减已落库部分。
//
// 顺序不能反过来：先扣减再落库时，落库失败会把这段浏览永久丢掉（RV-52）。
type FlushViewCountsWorker struct {
	river.WorkerDefaults[FlushViewCountsArgs]
	Source forum.ViewDeltaSource
	Acker  forum.ViewDeltaAcker
	Store  forum.ViewCountApplier
	Logger *slog.Logger
}

func (w *FlushViewCountsWorker) Work(ctx context.Context, _ *river.Job[FlushViewCountsArgs]) error {
	if w.Source == nil || w.Acker == nil || w.Store == nil {
		return fmt.Errorf("flush view counts requires source, acker and store")
	}
	deltas, err := w.Source.ReadViewDeltas(ctx)
	if err != nil {
		return fmt.Errorf("read view deltas: %w", err)
	}
	if len(deltas) == 0 {
		return nil
	}
	n, err := w.Store.ApplyViewCountDeltas(ctx, deltas)
	if err != nil {
		// 增量仍在 Redis：下一轮重试会再次落库（at-least-once，宁可多算不能丢）。
		return fmt.Errorf("apply view deltas: %w", err)
	}
	if err := w.Acker.AckViewDeltas(ctx, deltas); err != nil {
		// 扣减失败同样留在 Redis：下一轮重复落库一次，但不会丢增量。
		return fmt.Errorf("ack view deltas: %w", err)
	}
	if w.Logger != nil && n > 0 {
		w.Logger.Info("flushed topic view counts", "topics", n, "delta_keys", len(deltas))
	}
	return nil
}

// RegisterFlushViewCounts 注册刷盘 worker。
func RegisterFlushViewCounts(registry *supportjobs.Registry, worker *FlushViewCountsWorker) {
	if worker == nil || registry == nil {
		return
	}
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[FlushViewCountsArgs](workers, worker)
	})
}
