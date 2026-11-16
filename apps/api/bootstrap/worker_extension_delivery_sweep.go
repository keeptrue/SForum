package bootstrap

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	deliverysweep "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/deliverysweep"
	extensionsjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// registerExtensionEventDeliverySweep 注册扩展事件投递的 stale 收口 worker，
// 并把它挂到既有的 Schedule Registry（与 P2-6 的邮件 sweeper 同一处调度入口，
// 不新起 goroutine、不新起 ticker）。
//
// 为什么必须在 worker 侧：账本行由 API 进程的 observe 投递写入，收口是纯维护动作，
// 与用户可见路径无关，走 maintenance 队列即可。
func registerExtensionEventDeliverySweep(
	registry *supportjobs.Registry,
	coreScheduleConstructors map[string]river.PeriodicJobConstructor,
	wrapEnabled func(string, river.PeriodicJobConstructor) river.PeriodicJobConstructor,
	pool *pgxpool.Pool,
	logger *slog.Logger,
) {
	extensionsjobs.RegisterStaleEventDeliverySweeper(registry, &extensionsjobs.SweepStaleEventDeliveriesWorker{
		Store:  deliverysweep.NewPostgresSweeper(pool),
		Logger: logger,
	})
	coreScheduleConstructors[supportjobs.ScheduleExtensionsSweepStaleEventDeliveries] = wrapEnabled(
		supportjobs.ScheduleExtensionsSweepStaleEventDeliveries,
		func() (river.JobArgs, *river.InsertOpts) {
			return extensionsjobs.SweepStaleEventDeliveriesArgs{}, nil
		},
	)
}
