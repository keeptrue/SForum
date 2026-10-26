package identityjobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// CleanupSessionsArgs 清理已下线超过保留期的历史会话行。
// 由 periodic job 每天触发一次；keepDays 从 runtime option 读取（在 Worker 里注入）。
type CleanupSessionsArgs struct{}

func (CleanupSessionsArgs) Kind() string { return "identity.cleanup_sessions" }

// CleanupSessionsStore 是清理 job 需要的 store 契约（仅一个方法，避免依赖完整 identity.Store）。
type CleanupSessionsStore interface {
	DeleteOldRevokedSessions(ctx context.Context, keepDays int) (int, error)
}

// KeepDaysResolver 返回历史会话保留天数（从 runtime option 读取）。
type KeepDaysResolver func(ctx context.Context) (int, error)

type CleanupSessionsWorker struct {
	river.WorkerDefaults[CleanupSessionsArgs]
	Store    CleanupSessionsStore
	KeepDays KeepDaysResolver
	Logger   *slog.Logger
}

func (w *CleanupSessionsWorker) Work(ctx context.Context, _ *river.Job[CleanupSessionsArgs]) error {
	if w.Store == nil {
		return fmt.Errorf("cleanup sessions worker requires store")
	}
	keepDays := identity.RecommendedSessionsKeepDays
	if w.KeepDays != nil {
		if resolved, err := w.KeepDays(ctx); err == nil && resolved > 0 {
			keepDays = resolved
		}
	}
	deleted, err := w.Store.DeleteOldRevokedSessions(ctx, keepDays)
	if err != nil {
		return fmt.Errorf("cleanup revoked sessions: %w", err)
	}
	if w.Logger != nil {
		w.Logger.Info("cleanup revoked sessions", "deleted", deleted, "keep_days", keepDays)
	}
	return nil
}

// EnqueueOptions 显式注册到 identity 队列（与 search/extensions 分离）。
func (CleanupSessionsArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueDefault,
		MaxAttempts: 3,
	}
}
