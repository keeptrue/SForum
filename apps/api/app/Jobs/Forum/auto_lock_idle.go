package forumjobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// IdleLocker 将超过闲置天数的 active 主题锁帖。
type IdleLocker interface {
	AutoLockIdleTopics(ctx context.Context, idleDays int, limit int) (int, error)
}

// IdleDaysResolver 读取 forum.topics.auto_lock_idle_days（0=关闭）。
type IdleDaysResolver func(ctx context.Context) (int, error)

// AutoLockIdleArgs 闲置主题自动锁帖周期任务。
type AutoLockIdleArgs struct{}

func (AutoLockIdleArgs) Kind() string { return "forum.auto_lock_idle" }

func (AutoLockIdleArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 3,
	}
}

type AutoLockIdleWorker struct {
	river.WorkerDefaults[AutoLockIdleArgs]
	Locker    IdleLocker
	IdleDays  IdleDaysResolver
	Logger    *slog.Logger
	BatchSize int
}

func (w *AutoLockIdleWorker) Work(ctx context.Context, _ *river.Job[AutoLockIdleArgs]) error {
	if w.Locker == nil {
		return fmt.Errorf("auto lock idle worker requires locker")
	}
	idleDays := 0
	if w.IdleDays != nil {
		if days, err := w.IdleDays(ctx); err == nil {
			idleDays = days
		}
	}
	if idleDays <= 0 {
		return nil
	}
	limit := w.BatchSize
	if limit <= 0 {
		limit = 100
	}
	n, err := w.Locker.AutoLockIdleTopics(ctx, idleDays, limit)
	if err != nil {
		return fmt.Errorf("auto lock idle topics: %w", err)
	}
	if w.Logger != nil && n > 0 {
		w.Logger.Info("auto locked idle topics", "count", n, "idle_days", idleDays)
	}
	return nil
}

// Register 注册 worker 到 jobs registry。
func Register(registry *supportjobs.Registry, locker IdleLocker, idleDays IdleDaysResolver, logger *slog.Logger) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[AutoLockIdleArgs](workers, &AutoLockIdleWorker{
			Locker:   locker,
			IdleDays: idleDays,
			Logger:   logger,
		})
	})
}
