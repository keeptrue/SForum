package attachmentjobs

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

const CompressImageKind = "attachments.compress_image"

type CompressImageArgs struct {
	TaskID int64 `json:"taskId"`
}

func (CompressImageArgs) Kind() string { return CompressImageKind }

func (a CompressImageArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue: supportjobs.QueueMaintenance, MaxAttempts: 3,
		Unique: river.UniqueOpts{ByArgs: true},
	}
}

type CompressionProcessor interface {
	ProcessTask(context.Context, int64) error
}

type CompressImageWorker struct {
	river.WorkerDefaults[CompressImageArgs]
	Processor CompressionProcessor
	Logger    *slog.Logger
}

func (w *CompressImageWorker) Work(ctx context.Context, job *river.Job[CompressImageArgs]) error {
	if w == nil || w.Processor == nil || job == nil || job.Args.TaskID <= 0 {
		return nil
	}
	return w.Processor.ProcessTask(ctx, job.Args.TaskID)
}

const ReconcileCompressionKind = "attachments.reconcile_compression"

type ReconcileCompressionArgs struct{}

func (ReconcileCompressionArgs) Kind() string { return ReconcileCompressionKind }

type ReconcileCompressionWorker struct {
	river.WorkerDefaults[ReconcileCompressionArgs]
	Store     *attachments.PostgresStore
	Processor CompressionProcessor
	Logger    *slog.Logger
}

func (w *ReconcileCompressionWorker) Work(ctx context.Context, _ *river.Job[ReconcileCompressionArgs]) error {
	if w == nil || w.Store == nil || w.Processor == nil {
		return nil
	}
	ids, err := w.Store.ListPendingCompressionTaskIDs(ctx, 100)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := w.Processor.ProcessTask(ctx, id); err != nil && w.Logger != nil {
			w.Logger.Warn("reconcile attachment compression task failed", "task_id", id, "error", err)
		}
	}
	return nil
}

func RegisterCompression(registry *supportjobs.Registry, store *attachments.PostgresStore, processor CompressionProcessor, logger *slog.Logger) {
	registry.Add(func(workers *river.Workers) error {
		if err := river.AddWorkerSafely[CompressImageArgs](workers, &CompressImageWorker{Processor: processor, Logger: logger}); err != nil {
			return err
		}
		return river.AddWorkerSafely[ReconcileCompressionArgs](workers, &ReconcileCompressionWorker{Store: store, Processor: processor, Logger: logger})
	})
}
