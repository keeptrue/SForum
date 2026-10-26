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

// FlushViewCountsWorker 周期任务：Drain Redis deltas → PG ApplyViewCountDeltas。
type FlushViewCountsWorker struct {
	river.WorkerDefaults[FlushViewCountsArgs]
	Drainer forum.ViewDeltaDrainer
	Store   forum.ViewCountApplier
	Logger  *slog.Logger
}

func (w *FlushViewCountsWorker) Work(ctx context.Context, _ *river.Job[FlushViewCountsArgs]) error {
	if w.Drainer == nil || w.Store == nil {
		return fmt.Errorf("flush view counts requires drainer and store")
	}
	deltas, err := w.Drainer.DrainDeltas(ctx)
	if err != nil {
		return fmt.Errorf("drain view deltas: %w", err)
	}
	if len(deltas) == 0 {
		return nil
	}
	n, err := w.Store.ApplyViewCountDeltas(ctx, deltas)
	if err != nil {
		return fmt.Errorf("apply view deltas: %w", err)
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
