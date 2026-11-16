package extensionsjobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	deliverysweep "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/deliverysweep"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// SweepStaleEventDeliveriesArgs 扫描超龄的扩展事件投递并收成 failed 终态（P3-9）。
//
// 它兜住的是 observe 投递里「行写了、终态没写」的那一类：进程在建行之后崩溃、
// job 被 River 丢弃、或写终态失败被吞掉。没有这个周期任务，这些行会永久停在
// queued/running，管理面只能手工改库。
type SweepStaleEventDeliveriesArgs struct{}

func (SweepStaleEventDeliveriesArgs) Kind() string { return supportjobs.ScheduleExtensionsSweepStaleEventDeliveries }

// StaleEventDeliverySweeper 是 worker 需要的最小依赖面（测试可注入失败实现）。
type StaleEventDeliverySweeper interface {
	SweepStaleEventDeliveries(ctx context.Context, olderThan time.Duration) (int, error)
}

type SweepStaleEventDeliveriesWorker struct {
	river.WorkerDefaults[SweepStaleEventDeliveriesArgs]
	Store StaleEventDeliverySweeper
	// Threshold 为零时用 deliverysweep.StaleEventDeliveryThreshold。
	Threshold time.Duration
	Logger    *slog.Logger
}

func (w *SweepStaleEventDeliveriesWorker) Work(ctx context.Context, _ *river.Job[SweepStaleEventDeliveriesArgs]) error {
	if w.Store == nil {
		return fmt.Errorf("extension delivery sweeper requires a store")
	}
	threshold := w.Threshold
	if threshold <= 0 {
		threshold = deliverysweep.StaleEventDeliveryThreshold
	}
	swept, err := w.Store.SweepStaleEventDeliveries(ctx, threshold)
	if err != nil {
		// 返回错误交给 River 重试；不吞掉，也不 panic——下一轮 schedule 照常触发。
		return fmt.Errorf("sweep stale extension event deliveries: %w", err)
	}
	if swept > 0 {
		w.logger().Warn("swept stale extension event deliveries",
			"swept", swept, "threshold", threshold.String())
	}
	return nil
}

func (w *SweepStaleEventDeliveriesWorker) logger() *slog.Logger {
	if w != nil && w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

// EnqueueOptions 走维护队列：清理动作不该和用户可见的投递争配额。
func (SweepStaleEventDeliveriesArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 3,
	}
}

func RegisterStaleEventDeliverySweeper(registry *supportjobs.Registry, worker *SweepStaleEventDeliveriesWorker) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[SweepStaleEventDeliveriesArgs](workers, worker)
	})
}
