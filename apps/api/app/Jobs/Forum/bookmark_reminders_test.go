package forumjobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type bookmarkReminderRunnerFunc func(context.Context, time.Time, int) (int, int, error)

func (f bookmarkReminderRunnerFunc) RunDueBookmarkReminders(ctx context.Context, now time.Time, limit int) (int, int, error) {
	return f(ctx, now, limit)
}

// TestBookmarkRemindersArgsEnqueueContract 固定 job 契约：kind 与 schedule ID
// 一致、走 maintenance 队列、最多重试 3 次。
func TestBookmarkRemindersArgsEnqueueContract(t *testing.T) {
	args := BookmarkRemindersArgs{}
	if args.Kind() != BookmarkRemindersKind {
		t.Fatalf("kind = %q, want %q", args.Kind(), BookmarkRemindersKind)
	}
	if BookmarkRemindersKind != supportjobs.ScheduleForumBookmarkReminders {
		t.Fatalf("job kind %q must equal schedule id %q", BookmarkRemindersKind, supportjobs.ScheduleForumBookmarkReminders)
	}
	options := args.EnqueueOptions()
	if options.Queue != supportjobs.QueueMaintenance {
		t.Fatalf("queue = %q, want %q", options.Queue, supportjobs.QueueMaintenance)
	}
	if options.MaxAttempts != 3 {
		t.Fatalf("max attempts = %d, want 3", options.MaxAttempts)
	}
}

// TestBookmarkRemindersWorkerUsesDefaultBatchAndPassesNow 固定默认批量 200 与
// 「一次调用只跑一轮」：worker 不做循环，抢占语义由 store 的单条 UPDATE 保证。
func TestBookmarkRemindersWorkerUsesDefaultBatchAndPassesNow(t *testing.T) {
	var (
		calls      int
		gotLimit   int
		gotNowZero bool
	)
	worker := &BookmarkRemindersWorker{Runner: bookmarkReminderRunnerFunc(func(_ context.Context, now time.Time, limit int) (int, int, error) {
		calls++
		gotLimit = limit
		gotNowZero = now.IsZero()
		return 3, 2, nil
	})}

	if err := worker.Work(context.Background(), &river.Job[BookmarkRemindersArgs]{}); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if calls != 1 {
		t.Fatalf("runner calls = %d, want 1", calls)
	}
	if gotLimit != BookmarkRemindersDefaultBatch {
		t.Fatalf("limit = %d, want %d", gotLimit, BookmarkRemindersDefaultBatch)
	}
	if gotNowZero {
		t.Fatal("worker must pass a concrete UTC now to the runner")
	}
}

// TestBookmarkRemindersWorkerHonorsBatchSize 固定可配置批量上限。
func TestBookmarkRemindersWorkerHonorsBatchSize(t *testing.T) {
	gotLimit := 0
	worker := &BookmarkRemindersWorker{
		BatchSize: 7,
		Runner: bookmarkReminderRunnerFunc(func(_ context.Context, _ time.Time, limit int) (int, int, error) {
			gotLimit = limit
			return 0, 0, nil
		}),
	}
	if err := worker.Work(context.Background(), &river.Job[BookmarkRemindersArgs]{}); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if gotLimit != 7 {
		t.Fatalf("limit = %d, want 7", gotLimit)
	}
}

// TestBookmarkRemindersWorkerFailsClosed 固定失败语义：runner 缺失或报错都必须
// 让 job 失败（重试），不能静默吞掉提醒。
func TestBookmarkRemindersWorkerFailsClosed(t *testing.T) {
	if err := (&BookmarkRemindersWorker{}).Work(context.Background(), &river.Job[BookmarkRemindersArgs]{}); err == nil {
		t.Fatal("missing runner must fail the job")
	}
	sentinel := errors.New("claim failed")
	worker := &BookmarkRemindersWorker{Runner: bookmarkReminderRunnerFunc(func(context.Context, time.Time, int) (int, int, error) {
		return 0, 0, sentinel
	})}
	if err := worker.Work(context.Background(), &river.Job[BookmarkRemindersArgs]{}); !errors.Is(err, sentinel) {
		t.Fatalf("runner error must propagate, got %v", err)
	}
}
