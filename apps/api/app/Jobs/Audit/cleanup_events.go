package auditjobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// CleanupEventsArgs 清理超过保留期的 audit_events 行。
type CleanupEventsArgs struct{}

func (CleanupEventsArgs) Kind() string { return "audit.cleanup_events" }

func (CleanupEventsArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 3,
		Unique:      river.UniqueOpts{ByArgs: true},
	}
}

// Cleaner 删除过期审计行。
type Cleaner interface {
	DeleteOlderThan(ctx context.Context, keepDays int) (int64, error)
}

// KeepDaysResolver 返回保留天数（F1 默认 90；后续可接 runtime option）。
type KeepDaysResolver func(ctx context.Context) (int, error)

type CleanupEventsWorker struct {
	river.WorkerDefaults[CleanupEventsArgs]
	Cleaner  Cleaner
	KeepDays KeepDaysResolver
	Logger   *slog.Logger
}

func (w *CleanupEventsWorker) Work(ctx context.Context, _ *river.Job[CleanupEventsArgs]) error {
	if w.Cleaner == nil {
		return fmt.Errorf("audit cleanup requires cleaner")
	}
	keepDays := 90
	if w.KeepDays != nil {
		if days, err := w.KeepDays(ctx); err == nil && days > 0 {
			keepDays = days
		}
	}
	deleted, err := w.Cleaner.DeleteOlderThan(ctx, keepDays)
	if err != nil {
		return err
	}
	if w.Logger != nil {
		w.Logger.Info("cleanup audit events", "deleted", deleted, "keep_days", keepDays)
	}
	return nil
}

func Register(registry *supportjobs.Registry, worker *CleanupEventsWorker) {
	if worker == nil {
		return
	}
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[CleanupEventsArgs](workers, worker)
	})
}
