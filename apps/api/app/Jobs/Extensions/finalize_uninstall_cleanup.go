package extensionsjobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// FinalizeUninstallCleanupSweeper 是 worker 侧的最小依赖。由 embed 路径注入
// API 已装配的 extensions LifecycleService；独立 worker 刻意不提供——卸载物理
// 清理依赖 quarantine / attachment / provider / finalizer 完整宿主边界，
// worker 侧临时重建会绕过 exact receipt 门禁。
type FinalizeUninstallCleanupSweeper interface {
	RunPendingUninstallCleanupSweep(ctx context.Context, limit int) (extensions.LifecycleCleanupSweepResult, error)
}

// FinalizeUninstallCleanupArgs 周期收敛「台账已 succeeded 但 tombstone 仍
// pending」的卸载物理清理。单条失败隔离并留待下一轮，job 本身只在数据源
// 不可用时失败交给 River 重试。
type FinalizeUninstallCleanupArgs struct{}

func (FinalizeUninstallCleanupArgs) Kind() string { return "extensions.finalize_uninstall_cleanup" }

func (FinalizeUninstallCleanupArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 5,
	}
}

type FinalizeUninstallCleanupWorker struct {
	river.WorkerDefaults[FinalizeUninstallCleanupArgs]
	Sweeper FinalizeUninstallCleanupSweeper
}

func (w *FinalizeUninstallCleanupWorker) Work(ctx context.Context, _ *river.Job[FinalizeUninstallCleanupArgs]) error {
	if w == nil || w.Sweeper == nil {
		return fmt.Errorf("uninstall cleanup sweep worker requires sweeper")
	}
	// 批上限克制：单轮最多 10 条，剩余条目下一轮继续。
	_, err := w.Sweeper.RunPendingUninstallCleanupSweep(ctx, 10)
	return err
}

func RegisterFinalizeUninstallCleanup(registry *supportjobs.Registry, sweeper FinalizeUninstallCleanupSweeper) {
	if registry == nil || sweeper == nil {
		return
	}
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[FinalizeUninstallCleanupArgs](workers, &FinalizeUninstallCleanupWorker{Sweeper: sweeper})
	})
}
