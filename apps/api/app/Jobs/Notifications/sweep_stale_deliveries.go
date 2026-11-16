package notificationjobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// SweepStaleDeliveriesArgs 扫描超龄的邮件投递并收成 failed 终态（P2-6）。
//
// 它兜住的是「调度侧丢了」的那一类：进程在写 sending 之后崩溃、job 被 River 丢弃、
// 或者落库失败被吞掉。没有这个周期任务，这些行会永久停在 queued/sending，
// 既不会被重试也不会进终态，管理面只能手工改库。
type SweepStaleDeliveriesArgs struct{}

func (SweepStaleDeliveriesArgs) Kind() string { return "mail.sweep_stale" }

// StaleDeliverySweeper 是 sweeper 需要的最小 store 契约。
type StaleDeliverySweeper interface {
	SweepStaleDeliveries(ctx context.Context, olderThan time.Duration) (int, error)
}

type SweepStaleDeliveriesWorker struct {
	river.WorkerDefaults[SweepStaleDeliveriesArgs]
	Store StaleDeliverySweeper
	// Threshold 为零时用 notifications.StaleDeliveryThreshold（30 分钟）。
	Threshold time.Duration
	Logger    *slog.Logger
}

func (w *SweepStaleDeliveriesWorker) Work(ctx context.Context, _ *river.Job[SweepStaleDeliveriesArgs]) error {
	if w.Store == nil {
		return fmt.Errorf("mail stale delivery sweeper requires a store")
	}
	threshold := w.Threshold
	if threshold <= 0 {
		threshold = notifications.StaleDeliveryThreshold
	}
	swept, err := w.Store.SweepStaleDeliveries(ctx, threshold)
	if err != nil {
		return fmt.Errorf("sweep stale mail deliveries: %w", err)
	}
	// 扫到就记一条：这是「本该发送却没发出去」的可观测信号，不只是清理动作。
	if swept > 0 {
		w.logger().Warn("swept stale mail deliveries", "swept", swept, "threshold", threshold.String())
	}
	return nil
}

func (w *SweepStaleDeliveriesWorker) logger() *slog.Logger {
	if w != nil && w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

// EnqueueOptions 走维护队列：它不是业务写路径，不该和用户可见的邮件争配额。
func (SweepStaleDeliveriesArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 3,
	}
}

func RegisterStaleDeliverySweeper(registry *supportjobs.Registry, worker *SweepStaleDeliveriesWorker) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[SweepStaleDeliveriesArgs](workers, worker)
	})
}
