package trustlevelsjobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"

	trustlevelsjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/TrustLevels"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// fakeRecomputer 记录被重算的用户，并可注入错误。
type fakeRecomputer struct {
	users []int64
	err   error
}

func (f *fakeRecomputer) RecomputeUser(_ context.Context, userID int64) error {
	f.users = append(f.users, userID)
	return f.err
}

// TestRecomputeUserArgsCarryByArgsUniqueness 钉住「去重靠 args」这条判据。
//
// 去重键等价于 trustlevels:recompute:{userID}。没有 ByArgs，一次活动写请求的
// 多次触发（主题/评论/点赞）会变成多个 job 各自跑一遍现算。
func TestRecomputeUserArgsCarryByArgsUniqueness(t *testing.T) {
	args := trustlevelsjobs.RecomputeUserArgs{UserID: 42}
	if args.Kind() != trustlevelsjobs.RecomputeUserKind {
		t.Fatalf("kind = %q, want %q", args.Kind(), trustlevelsjobs.RecomputeUserKind)
	}
	options := args.EnqueueOptions()
	if options.Queue != supportjobs.QueueDefault {
		t.Fatalf("queue = %q, want %q", options.Queue, supportjobs.QueueDefault)
	}
	if options.MaxAttempts != 3 {
		t.Fatalf("max attempts = %d, want 3", options.MaxAttempts)
	}
	insertOpts := options.RiverInsertOpts()
	if !insertOpts.UniqueOpts.ByArgs {
		t.Fatal("trust level recompute must dedupe ByArgs: 同一用户短时间多次触发要合并")
	}
	if insertOpts.Queue != supportjobs.QueueDefault || insertOpts.MaxAttempts != 3 {
		t.Fatalf("river insert opts = %#v", insertOpts)
	}
}

// TestRecomputeBatchArgsCarryByArgsUniqueness 钉住批量 job 的队列与去重：
// 它跑在 maintenance 队列（不跟用户可见的写路径抢 worker）。
func TestRecomputeBatchArgsCarryByArgsUniqueness(t *testing.T) {
	args := trustlevelsjobs.RecomputeBatchArgs{Mode: trustlevels.RecomputeModeStale, Cursor: 0}
	if args.Kind() != trustlevelsjobs.RecomputeBatchKind {
		t.Fatalf("kind = %q", args.Kind())
	}
	insertOpts := args.EnqueueOptions().RiverInsertOpts()
	if insertOpts.Queue != supportjobs.QueueMaintenance {
		t.Fatalf("queue = %q, want %q", insertOpts.Queue, supportjobs.QueueMaintenance)
	}
	if !insertOpts.UniqueOpts.ByArgs {
		t.Fatal("batch job must dedupe ByArgs: 同一起点的重复触发要合并")
	}
}

func TestRecomputeUserWorkerCallsRecomputerAndPropagatesErrors(t *testing.T) {
	recomputer := &fakeRecomputer{}
	worker := &trustlevelsjobs.RecomputeUserWorker{Recomputer: recomputer}
	if err := worker.Work(context.Background(), &river.Job[trustlevelsjobs.RecomputeUserArgs]{
		Args: trustlevelsjobs.RecomputeUserArgs{UserID: 7},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if len(recomputer.users) != 1 || recomputer.users[0] != 7 {
		t.Fatalf("recomputed users = %v, want [7]", recomputer.users)
	}

	failing := &fakeRecomputer{err: errors.New("db down")}
	err := (&trustlevelsjobs.RecomputeUserWorker{Recomputer: failing}).Work(
		context.Background(), &river.Job[trustlevelsjobs.RecomputeUserArgs]{
			Args: trustlevelsjobs.RecomputeUserArgs{UserID: 8},
		})
	if err == nil {
		t.Fatal("重算失败必须返回 error，否则 river 会把一次失败当成功")
	}
}

func TestRecomputeUserWorkerRejectsMissingRecomputer(t *testing.T) {
	err := (&trustlevelsjobs.RecomputeUserWorker{}).Work(
		context.Background(), &river.Job[trustlevelsjobs.RecomputeUserArgs]{
			Args: trustlevelsjobs.RecomputeUserArgs{UserID: 1},
		})
	if err == nil {
		t.Fatal("未装配 recomputer 时必须报错：静默成功会让等级变更事件永不发出")
	}
}

// fakeBatchExecutor 返回预设的一页结果。
type fakeBatchExecutor struct {
	result trustlevels.BatchResult
	err    error
}

func (f *fakeBatchExecutor) RunBatch(context.Context, string, int64, int) (trustlevels.BatchResult, error) {
	return f.result, f.err
}

// fakeBatchEnqueuer 记录自链的下一页参数。
type fakeBatchEnqueuer struct {
	args []trustlevelsjobs.RecomputeBatchArgs
}

func (f *fakeBatchEnqueuer) EnqueueRecomputeBatch(_ context.Context, args trustlevelsjobs.RecomputeBatchArgs) error {
	f.args = append(f.args, args)
	return nil
}

func TestRecomputeBatchWorkerChainsNextPage(t *testing.T) {
	enqueuer := &fakeBatchEnqueuer{}
	worker := &trustlevelsjobs.RecomputeBatchWorker{
		Executor: &fakeBatchExecutor{result: trustlevels.BatchResult{Processed: 500, NextCursor: 500, Done: false}},
		Enqueuer: enqueuer,
	}
	if err := worker.Work(context.Background(), &river.Job[trustlevelsjobs.RecomputeBatchArgs]{
		Args: trustlevelsjobs.RecomputeBatchArgs{Mode: trustlevels.RecomputeModeAll, Cursor: 0, Batch: 500},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if len(enqueuer.args) != 1 {
		t.Fatalf("next page enqueued %d times, want 1", len(enqueuer.args))
	}
	next := enqueuer.args[0]
	if next.Cursor != 500 || next.Mode != trustlevels.RecomputeModeAll || next.Batch != 500 {
		t.Fatalf("next page args = %#v", next)
	}
}

func TestRecomputeBatchWorkerStopsAtTail(t *testing.T) {
	enqueuer := &fakeBatchEnqueuer{}
	worker := &trustlevelsjobs.RecomputeBatchWorker{
		Executor: &fakeBatchExecutor{result: trustlevels.BatchResult{Processed: 0, NextCursor: 900, Done: true}},
		Enqueuer: enqueuer,
	}
	if err := worker.Work(context.Background(), &river.Job[trustlevelsjobs.RecomputeBatchArgs]{
		Args: trustlevelsjobs.RecomputeBatchArgs{Mode: trustlevels.RecomputeModeStale},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if len(enqueuer.args) != 0 {
		t.Fatalf("扫到头之后不该再排下一页，得到 %#v", enqueuer.args)
	}
}
