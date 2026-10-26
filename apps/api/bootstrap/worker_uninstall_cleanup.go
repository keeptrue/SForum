package bootstrap

import (
	"github.com/riverqueue/river"

	extensionsjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// applyUninstallCleanupSweepRegistration 把卸载物理清理收敛挂到 worker 装配：
// 周期构造器与执行器一起注册，仅 embed 路径（deps.UninstallCleanupSweeper
// 非 nil）生效；独立 worker 没有完整宿主边界，能力整体关闭。
func applyUninstallCleanupSweepRegistration(
	registry *supportjobs.Registry,
	constructors map[string]river.PeriodicJobConstructor,
	wrapEnabled func(string, river.PeriodicJobConstructor) river.PeriodicJobConstructor,
	deps workerRuntimeDeps,
) {
	if deps.UninstallCleanupSweeper == nil {
		return
	}
	constructors[supportjobs.ScheduleExtensionsFinalizeUninstallCleanup] = wrapEnabled(
		supportjobs.ScheduleExtensionsFinalizeUninstallCleanup,
		func() (river.JobArgs, *river.InsertOpts) {
			opts := extensionsjobs.FinalizeUninstallCleanupArgs{}.EnqueueOptions().RiverInsertOpts()
			return extensionsjobs.FinalizeUninstallCleanupArgs{}, opts
		},
	)
	extensionsjobs.RegisterFinalizeUninstallCleanup(registry, deps.UninstallCleanupSweeper)
}
