package searchjobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type Reconciler interface {
	Reconcile(ctx context.Context) error
}

// ReconcileArgs 周期核对权威 topics 与当前 provider 的 Host 同步账本。
type ReconcileArgs struct{}

func (ReconcileArgs) Kind() string { return "search.reconcile" }

func (ReconcileArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 5,
		Unique: river.UniqueOpts{
			ByArgs:  true,
			ByState: activeSearchJobStates(),
		},
	}
}

type ReconcileWorker struct {
	river.WorkerDefaults[ReconcileArgs]
	Reconciler Reconciler
}

func (w *ReconcileWorker) Work(ctx context.Context, _ *river.Job[ReconcileArgs]) error {
	if w == nil || w.Reconciler == nil {
		return fmt.Errorf("search reconcile worker requires reconciler")
	}
	return w.Reconciler.Reconcile(ctx)
}

func RegisterReconcile(registry *supportjobs.Registry, reconciler Reconciler) {
	if registry == nil || reconciler == nil {
		return
	}
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[ReconcileArgs](workers, &ReconcileWorker{Reconciler: reconciler})
	})
}
