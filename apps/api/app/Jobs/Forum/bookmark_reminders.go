package forumjobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// BookmarkRemindersKind 是到期收藏提醒的 job kind（与 schedule ID 一致）。
const BookmarkRemindersKind = "forum.bookmark_reminders"

// BookmarkRemindersDefaultBatch 是单轮最多处理的提醒数。
// 提醒是低频个人动作，200 足够；批量太小会让积压拖尾，太大会拉长单事务。
const BookmarkRemindersDefaultBatch = 200

// BookmarkReminderRunner 抢占到期提醒并发通知，返回 (claimed, notified, err)。
type BookmarkReminderRunner interface {
	RunDueBookmarkReminders(ctx context.Context, now time.Time, limit int) (int, int, error)
}

// BookmarkRemindersArgs 到期收藏提醒周期任务。
type BookmarkRemindersArgs struct{}

func (BookmarkRemindersArgs) Kind() string { return BookmarkRemindersKind }

func (BookmarkRemindersArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueMaintenance,
		MaxAttempts: 3,
	}
}

type BookmarkRemindersWorker struct {
	river.WorkerDefaults[BookmarkRemindersArgs]
	Runner    BookmarkReminderRunner
	Logger    *slog.Logger
	BatchSize int
}

func (w *BookmarkRemindersWorker) Work(ctx context.Context, _ *river.Job[BookmarkRemindersArgs]) error {
	if w.Runner == nil {
		return fmt.Errorf("bookmark reminders worker requires runner")
	}
	limit := w.BatchSize
	if limit <= 0 {
		limit = BookmarkRemindersDefaultBatch
	}
	claimed, notified, err := w.Runner.RunDueBookmarkReminders(ctx, time.Now().UTC(), limit)
	if err != nil {
		return fmt.Errorf("run bookmark reminders: %w", err)
	}
	if w.Logger != nil && claimed > 0 {
		// claimed 与 notified 的差额是「目标已不可见」的行：只标已提醒、不发通知。
		w.Logger.Info("bookmark reminders processed", "claimed", claimed, "notified", notified)
	}
	return nil
}

// RegisterBookmarkReminders 注册到期收藏提醒 worker。
func RegisterBookmarkReminders(registry *supportjobs.Registry, runner BookmarkReminderRunner, logger *slog.Logger) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[BookmarkRemindersArgs](workers, &BookmarkRemindersWorker{
			Runner: runner,
			Logger: logger,
		})
	})
}
