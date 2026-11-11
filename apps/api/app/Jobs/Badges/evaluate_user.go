package badgesjobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// EvaluateUserKind 是「评估单个用户的自动徽章」job kind。
const EvaluateUserKind = "badges.evaluate_user"

// GrantedBadge 是本次新授予的徽章（供通知层渲染）。
type GrantedBadge struct {
	Slug string
	Name string
}

// UserEvaluator 评估一个用户，返回本次新授予的徽章。
type UserEvaluator interface {
	EvaluateUser(ctx context.Context, userID int64) ([]GrantedBadge, error)
}

// GrantedNotifier 把新授予写成站内通知（M4 提供实现；nil 时跳过）。
type GrantedNotifier interface {
	NotifyBadgeGranted(ctx context.Context, userID int64, badges []GrantedBadge) error
}

// EvaluateUserArgs 是评估 job 的参数。唯一字段就是去重维度：
// river 的 ByArgs 唯一性因此等价于「同一用户短时间只跑一次」。
type EvaluateUserArgs struct {
	UserID int64 `json:"userId"`
}

func (EvaluateUserArgs) Kind() string { return EvaluateUserKind }

// EnqueueOptions 固定队列与重试策略。
//
// 去重键等价于 badges:evaluate:{userID}：ByArgs 让 river 按参数（只有 UserID）
// 去重，同一用户在一次评估尚未执行完时重复入队会被合并。
func (EvaluateUserArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueDefault,
		MaxAttempts: 3,
	}
}

// EvaluateUserWorker 执行评估。
type EvaluateUserWorker struct {
	river.WorkerDefaults[EvaluateUserArgs]
	Evaluator UserEvaluator
	Notifier  GrantedNotifier
	Logger    *slog.Logger
}

func (w *EvaluateUserWorker) Work(ctx context.Context, job *river.Job[EvaluateUserArgs]) error {
	if w.Evaluator == nil {
		return fmt.Errorf("badges evaluate worker requires evaluator")
	}
	if job == nil || job.Args.UserID <= 0 {
		return nil
	}
	granted, err := w.Evaluator.EvaluateUser(ctx, job.Args.UserID)
	if err != nil {
		return fmt.Errorf("evaluate badges for user %d: %w", job.Args.UserID, err)
	}
	if len(granted) == 0 {
		return nil
	}
	if w.Notifier != nil {
		if err := w.Notifier.NotifyBadgeGranted(ctx, job.Args.UserID, granted); err != nil {
			return fmt.Errorf("notify badge granted: %w", err)
		}
	}
	if w.Logger != nil {
		w.Logger.Info("badges granted", "user_id", job.Args.UserID, "count", len(granted))
	}
	return nil
}

// RegisterEvaluateUser 注册评估 worker。
func RegisterEvaluateUser(registry *supportjobs.Registry, evaluator UserEvaluator, notifier GrantedNotifier, logger *slog.Logger) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[EvaluateUserArgs](workers, &EvaluateUserWorker{
			Evaluator: evaluator,
			Notifier:  notifier,
			Logger:    logger,
		})
	})
}
