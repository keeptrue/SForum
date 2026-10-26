package attachmentjobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type OrphanCleaner interface {
	CleanupOrphanAttachments(ctx context.Context, limit int) error
}

type CleanupOrphansArgs struct {
	Limit int `json:"limit"`
}

func (CleanupOrphansArgs) Kind() string {
	return "attachments.cleanup_orphans"
}

func (CleanupOrphansArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 10,
		Unique:      river.UniqueOpts{ByArgs: true},
	}
}

type CleanupOrphansWorker struct {
	river.WorkerDefaults[CleanupOrphansArgs]
	Cleaner OrphanCleaner
}

func (w *CleanupOrphansWorker) Work(ctx context.Context, job *river.Job[CleanupOrphansArgs]) error {
	if w.Cleaner == nil {
		return fmt.Errorf("attachment cleanup worker requires cleaner")
	}
	limit := job.Args.Limit
	if limit <= 0 {
		limit = 100
	}
	return w.Cleaner.CleanupOrphanAttachments(ctx, limit)
}

func Register(registry *supportjobs.Registry, cleaner OrphanCleaner) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[CleanupOrphansArgs](workers, &CleanupOrphansWorker{Cleaner: cleaner})
	})
}
