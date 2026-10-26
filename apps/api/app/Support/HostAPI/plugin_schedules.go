package hostapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/riverqueue/river"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// PluginScheduleTriggerWorker converts a Host-owned periodic marker into the
// exact declared plugin job while holding the same admission lease used by
// disable and upgrade drain.
type PluginScheduleTriggerWorker struct {
	river.WorkerDefaults[supportjobs.PluginScheduleTriggerArgs]
	Schedules *supportjobs.PluginScheduleAdmissionRegistry
	Jobs      VersionedPluginJobEnqueuer
}

func (w *PluginScheduleTriggerWorker) Work(ctx context.Context, job *river.Job[supportjobs.PluginScheduleTriggerArgs]) error {
	if w == nil || w.Schedules == nil || w.Jobs == nil {
		return errors.New("plugin schedule trigger worker is not configured")
	}
	if job == nil || !job.Args.Identity().Valid() || job.Args.ScheduleID == "" {
		return river.JobCancel(errors.New("invalid plugin schedule trigger envelope"))
	}
	declaration, lease, err := w.Schedules.AcquireTrigger(ctx, job.Args.Identity(), job.Args.ScheduleID)
	if err != nil {
		if errors.Is(err, supportjobs.ErrPluginScheduleRuntimeStale) ||
			errors.Is(err, supportjobs.ErrPluginScheduleDraining) ||
			errors.Is(err, supportjobs.ErrPluginScheduleNotDeclared) {
			return river.JobCancel(err)
		}
		return err
	}
	defer lease.Release()
	if !declaration.Contract.Valid() || declaration.TrustGrantID == "" ||
		declaration.Contract.JobName != declaration.JobName ||
		declaration.Contract.JobContract != declaration.JobContract {
		return river.JobCancel(fmt.Errorf("invalid exact plugin schedule contract %q", declaration.ScheduleID))
	}
	return w.Jobs.EnqueueVersionedPluginJob(
		lease.Context, declaration.Contract, declaration.TrustGrantID, map[string]any{},
	)
}
