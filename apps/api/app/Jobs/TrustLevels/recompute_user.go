package trustlevelsjobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// RecomputeUserKind 是「重算单个用户信任等级」job kind（M4b-2）。
//
// 它**不是**授权路径：等级仍然由每次装载现算（D2）。这个 job 只做两件事——
// 与变更账本比较、在真的变了的时候发 `trust_level.changed`。
const RecomputeUserKind = "trustlevels.recompute_user"

// RecomputeUserArgs 的唯一字段就是去重维度：配合 ByArgs 唯一性，
// 同一用户在上一轮还没跑完时的重复入队会被合并。
type RecomputeUserArgs struct {
	UserID int64 `json:"userId"`
}

func (RecomputeUserArgs) Kind() string { return RecomputeUserKind }

// EnqueueOptions 固定队列与重试，并打开 **ByArgs 去重**。
//
// 去重键等价于 trustlevels:recompute:{userID}：活动写路径一次请求可能触发多次
// （主题 + 评论 + 点赞），ByArgs 让同一用户短时间内的多次触发合一次。
func (RecomputeUserArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueDefault,
		MaxAttempts: 3,
		Unique:      river.UniqueOpts{ByArgs: true},
	}
}

// UserRecomputer 是 job 对领域层的窄接口（实现：trustlevels.Recomputer）。
//
// 返回 error 才会重试：求值 incomplete 属于「结论可信但偏低」，领域层自己吞掉
// 并且不写账本/不发事件，**不**该让 river 反复重试同一个抖动。
type UserRecomputer interface {
	RecomputeUser(ctx context.Context, userID int64) error
}

// RecomputeUserWorker 执行一次重算。
type RecomputeUserWorker struct {
	river.WorkerDefaults[RecomputeUserArgs]
	Recomputer UserRecomputer
	Logger     *slog.Logger
}

func (w *RecomputeUserWorker) Work(ctx context.Context, job *river.Job[RecomputeUserArgs]) error {
	if w.Recomputer == nil {
		return fmt.Errorf("trust level recompute worker requires a recomputer")
	}
	if job == nil || job.Args.UserID <= 0 {
		return nil
	}
	if err := w.Recomputer.RecomputeUser(ctx, job.Args.UserID); err != nil {
		return fmt.Errorf("recompute trust level for user %d: %w", job.Args.UserID, err)
	}
	return nil
}

// RegisterRecomputeUser 注册 worker。
func RegisterRecomputeUser(registry *supportjobs.Registry, recomputer UserRecomputer, logger *slog.Logger) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[RecomputeUserArgs](workers, &RecomputeUserWorker{
			Recomputer: recomputer,
			Logger:     logger,
		})
	})
}
