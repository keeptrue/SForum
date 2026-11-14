package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	trustlevelsjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/TrustLevels"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// 本文件把 M4b-2 的重算链路装到现成的对象上：不新建 resolver、不新建 publisher。
//
// 两个复用点（都是刻意的）：
//   - resolver 复用 infrastructure 阶段那个**唯一**实例（M8 断言：全仓只有一处
//     trustlevels.NewResolver），否则 job 与 HTTP 两条路径会对同一个人算出两份等级；
//   - publisher 复用 API 已经在用的 extensionRuntime（Events 的 plugin bus），
//     新建第二个会把同一条事件投递两遍。

// trustLevelRecomputerAdapter 把领域 Recomputer 适配成 job 层窄接口。
type trustLevelRecomputerAdapter struct {
	recomputer *trustlevels.Recomputer
}

func (a trustLevelRecomputerAdapter) RecomputeUser(ctx context.Context, userID int64) error {
	if a.recomputer == nil {
		return errors.New("trust level recomputer is not configured")
	}
	_, err := a.recomputer.RecomputeUser(ctx, userID)
	return err
}

// trustLevelFanout 把一页用户批量入队成 recompute job（单次 InsertMany）。
type trustLevelFanout struct {
	dispatcher *supportjobs.Dispatcher
}

func (f trustLevelFanout) EnqueueUserRecompute(ctx context.Context, userIDs []int64) error {
	if f.dispatcher == nil || len(userIDs) == 0 {
		return nil
	}
	args := make([]river.JobArgs, 0, len(userIDs))
	for _, userID := range userIDs {
		args = append(args, trustlevelsjobs.RecomputeUserArgs{UserID: userID})
	}
	if _, err := f.dispatcher.EnqueueMany(ctx, args, trustlevelsjobs.RecomputeUserArgs{}.EnqueueOptions()); err != nil {
		return fmt.Errorf("enqueue trust level recompute jobs: %w", err)
	}
	return nil
}

// trustLevelBatchEnqueuer 把「下一页」排进 River（批量 worker 自链用）。
type trustLevelBatchEnqueuer struct {
	dispatcher *supportjobs.Dispatcher
}

func (e trustLevelBatchEnqueuer) EnqueueRecomputeBatch(ctx context.Context, args trustlevelsjobs.RecomputeBatchArgs) error {
	if e.dispatcher == nil {
		return errors.New("trust level batch enqueuer is not configured")
	}
	_, err := e.dispatcher.Enqueue(ctx, args, args.EnqueueOptions())
	return err
}

// trustLevelWorkers 是 recompute 链路对 worker 装配的三件套。
//
// 为什么按端口给而不是直接给 *trustlevels.Recomputer：worker 只该看到 job 需要的
// 最小面（注册、执行、自链），这样新加一个 job 也不会顺手把领域对象塞进 worker。
type trustLevelWorkers struct {
	Recomputer    trustlevelsjobs.UserRecomputer
	BatchExecutor trustlevelsjobs.BatchExecutor
	BatchEnqueuer trustlevelsjobs.BatchEnqueuer
}

// newTrustLevelWorkers 用 API 已装配好的 resolver / publisher / job client 造三件套。
//
// 只在 embed 路径调用：AGENTS.md 明确 standalone Worker startup 不受支持，
// dev/生产都由 API 内嵌并拥有 worker。
func newTrustLevelWorkers(core *apiCoreStack, logger *slog.Logger) *trustLevelWorkers {
	if core == nil || core.trustLevelResolver == nil || core.pool == nil || core.jobClient == nil {
		if logger != nil {
			logger.Warn("trust level recompute workers not registered: missing resolver/pool/job client")
		}
		return nil
	}
	dispatcher := supportjobs.NewDispatcher(core.jobClient)
	recomputer := trustlevels.NewRecomputer(
		core.trustLevelResolver,
		trustlevels.NewPostgresLedgerStore(core.pool),
		trustlevels.NewPostgresUserTrustInputReader(core.pool),
		core.trustLevelEventPublisher,
	).WithLogger(logger)
	if core.trustLevelEventPublisher == nil && logger != nil {
		logger.Warn("trust level changed events will not be delivered: no event publisher bound")
	}
	batchRunner := trustlevels.NewBatchRunner(
		trustlevels.NewPostgresRecomputeTargets(core.pool),
		trustLevelFanout{dispatcher: dispatcher},
	)
	return &trustLevelWorkers{
		Recomputer:    trustLevelRecomputerAdapter{recomputer: recomputer},
		BatchExecutor: trustLevelBatchRunnerAdapter{runner: batchRunner},
		BatchEnqueuer: trustLevelBatchEnqueuer{dispatcher: dispatcher},
	}
}

// registerTrustLevelWorkers 是 recompute 链路**唯一**的 worker 注册面。
//
// 与 badgesjobs.RegisterEvaluateUser 同一个装配面（bootstrap）：两个 worker 都在这里
// 注册，重算 job 才有消费者。少这一处，入队依旧成功、job 永远躺着、事件永远不发——
// 而 CI 全绿（pm 在 M4b 第 1 段就是抓到这一点）。
func registerTrustLevelWorkers(registry *supportjobs.Registry, deps *trustLevelWorkers, logger *slog.Logger) {
	if registry == nil {
		return
	}
	if deps == nil {
		if logger != nil {
			logger.Warn("trust level recompute workers not registered: worker deps missing")
		}
		return
	}
	trustlevelsjobs.RegisterRecomputeUser(registry, deps.Recomputer, logger)
	trustlevelsjobs.RegisterRecomputeBatch(registry, deps.BatchExecutor, deps.BatchEnqueuer, logger)
}

// trustLevelBatchRunnerAdapter 把领域 BatchRunner 适配成 job 层窄接口。
type trustLevelBatchRunnerAdapter struct {
	runner *trustlevels.BatchRunner
}

func (a trustLevelBatchRunnerAdapter) RunBatch(
	ctx context.Context, mode string, cursor int64, batch int,
) (trustlevels.BatchResult, error) {
	if a.runner == nil {
		return trustlevels.BatchResult{}, errors.New("trust level batch runner is not configured")
	}
	return a.runner.RunBatch(ctx, mode, cursor, batch)
}
