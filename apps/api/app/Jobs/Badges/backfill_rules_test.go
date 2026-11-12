package badgesjobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/riverqueue/river"
)

// fakeRunner 记录每次调用的游标，返回预设结果。
type fakeRunner struct {
	calls   []int64
	next    int64
	done    bool
	failure error
}

func (f *fakeRunner) BackfillRuleBatch(_ context.Context, _ int64, afterUserID int64, _ int) (int64, bool, error) {
	f.calls = append(f.calls, afterUserID)
	if f.failure != nil {
		return afterUserID, false, f.failure
	}
	return f.next, f.done, nil
}

// fakeEnqueuer 记录入队的下一页参数。
type fakeEnqueuer struct {
	args []BackfillRulesArgs
	fail error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, args BackfillRulesArgs) error {
	if f.fail != nil {
		return f.fail
	}
	f.args = append(f.args, args)
	return nil
}

func runJob(t *testing.T, worker *BackfillRulesWorker, args BackfillRulesArgs) error {
	t.Helper()
	return worker.Work(context.Background(), &river.Job[BackfillRulesArgs]{Args: args})
}

// TestBackfillRulesWorkerChainsPages 断言游标续跑：未结束就把下一页入队。
func TestBackfillRulesWorkerChainsPages(t *testing.T) {
	runner := &fakeRunner{next: 500, done: false}
	enqueuer := &fakeEnqueuer{}
	worker := &BackfillRulesWorker{Runner: runner, Enqueuer: enqueuer}

	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 0, Batch: 500}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != 0 {
		t.Fatalf("第一页应从游标 0 开始，得到 %v", runner.calls)
	}
	if len(enqueuer.args) != 1 || enqueuer.args[0].CursorUserID != 500 || enqueuer.args[0].BadgeID != 7 {
		t.Fatalf("下一页应带游标 500，得到 %+v", enqueuer.args)
	}

	// 最后一页：不再入队。
	runner.done = true
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 500, Batch: 500}); err != nil {
		t.Fatal(err)
	}
	if len(enqueuer.args) != 1 {
		t.Fatalf("结束后不应再入队，得到 %+v", enqueuer.args)
	}
}

// TestBackfillRulesWorkerSurfacesFailures 断言两件事：
//   - 入队下一页失败必须返回 error（否则整条回扫链静默停在半路，而运营已拿到 queued 回执）；
//   - 执行页失败同样返回 error（让 river 重试当前页）。
func TestBackfillRulesWorkerSurfacesFailures(t *testing.T) {
	runner := &fakeRunner{next: 500, done: false}
	enqueuer := &fakeEnqueuer{fail: errors.New("queue down")}
	worker := &BackfillRulesWorker{Runner: runner, Enqueuer: enqueuer}
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, Batch: 500}); err == nil {
		t.Fatal("入队下一页失败必须返回 error")
	}

	failing := &fakeRunner{failure: errors.New("db down")}
	if err := runJob(t, &BackfillRulesWorker{Runner: failing, Enqueuer: &fakeEnqueuer{}}, BackfillRulesArgs{BadgeID: 7}); err == nil {
		t.Fatal("执行页失败必须返回 error")
	}
}

// TestBackfillRulesArgsDedupeKey 断言 ByArgs 去重的维度：同一徽章同一页参数完全
// 相同（river 因此把它们合并），不同游标是不同页。
func TestBackfillRulesArgsDedupeKey(t *testing.T) {
	first := BackfillRulesArgs{BadgeID: 7, CursorUserID: 0, Batch: 500}
	same := BackfillRulesArgs{BadgeID: 7, CursorUserID: 0, Batch: 500}
	next := BackfillRulesArgs{BadgeID: 7, CursorUserID: 500, Batch: 500}
	if first != same {
		t.Fatal("同一页参数必须完全相等（river ByArgs 去重的依据）")
	}
	if first == next {
		t.Fatal("不同游标必须是不同页")
	}
}

// advanceCall / failCall 记录状态写入的实参，断言用值而不是「调用过」。
type advanceCall struct {
	badgeID    int64
	lastUserID int64
	done       bool
}

type failCall struct {
	badgeID int64
	cursor  int64
	message string
}

type fakeRunRecorder struct {
	advances   []advanceCall
	failures   []failCall
	advanceErr error
	failErr    error
}

func (f *fakeRunRecorder) AdvanceBackfillRun(_ context.Context, badgeID int64, lastUserID int64, done bool) error {
	if f.advanceErr != nil {
		return f.advanceErr
	}
	f.advances = append(f.advances, advanceCall{badgeID: badgeID, lastUserID: lastUserID, done: done})
	return nil
}

func (f *fakeRunRecorder) FailBackfillRun(_ context.Context, badgeID int64, failedCursorUserID int64, message string) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.failures = append(f.failures, failCall{badgeID: badgeID, cursor: failedCursorUserID, message: message})
	return nil
}

// TestBackfillRulesWorkerRecordsRunningProgress 断言续页那一页落的是
// running + 新游标（done=false），而不是 completed。
func TestBackfillRulesWorkerRecordsRunningProgress(t *testing.T) {
	runs := &fakeRunRecorder{}
	worker := &BackfillRulesWorker{
		Runner:   &fakeRunner{next: 500, done: false},
		Enqueuer: &fakeEnqueuer{},
		Runs:     runs,
	}
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 0, Batch: 500}); err != nil {
		t.Fatal(err)
	}
	if len(runs.advances) != 1 {
		t.Fatalf("续页必须写一次状态，得到 %+v", runs.advances)
	}
	got := runs.advances[0]
	if got.badgeID != 7 || got.lastUserID != 500 || got.done {
		t.Fatalf("续页状态 = %+v，want badge 7 / cursor 500 / done=false", got)
	}
}

// TestBackfillRulesWorkerRecordsCompletedOnLastPage 断言末页落 completed。
//
// 变异自检：把 `AdvanceBackfillRun(..., true)` 改成 false，这条会红。
func TestBackfillRulesWorkerRecordsCompletedOnLastPage(t *testing.T) {
	runs := &fakeRunRecorder{}
	worker := &BackfillRulesWorker{
		Runner:   &fakeRunner{next: 900, done: true},
		Enqueuer: &fakeEnqueuer{},
		Runs:     runs,
	}
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 500, Batch: 500}); err != nil {
		t.Fatal(err)
	}
	if len(runs.advances) != 1 || !runs.advances[0].done {
		t.Fatalf("末页必须落 completed，得到 %+v", runs.advances)
	}
	if runs.advances[0].lastUserID != 900 {
		t.Fatalf("末页游标 = %d，want 900", runs.advances[0].lastUserID)
	}
}

// TestBackfillRulesWorkerRecordsFailedCursor 断言失败页把**本次 args 的游标**
// 写进 failed_cursor_user_id（不是 runner 返回的游标：失败时 runner 的返回值不可信）。
//
// 变异自检：删掉失败分支里的 FailBackfillRun 调用，这条会红。
func TestBackfillRulesWorkerRecordsFailedCursor(t *testing.T) {
	runs := &fakeRunRecorder{}
	worker := &BackfillRulesWorker{
		Runner:   &fakeRunner{failure: errors.New("db down")},
		Enqueuer: &fakeEnqueuer{},
		Runs:     runs,
	}
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 250, Batch: 500}); err == nil {
		t.Fatal("页失败必须返回 error")
	}
	if len(runs.failures) != 1 {
		t.Fatalf("失败页必须写一次失败状态，得到 %+v", runs.failures)
	}
	got := runs.failures[0]
	if got.badgeID != 7 || got.cursor != 250 {
		t.Fatalf("失败状态 = %+v，want badge 7 / cursor 250", got)
	}
	if !strings.Contains(got.message, "db down") {
		t.Fatalf("失败原因应带原始错误文本，得到 %q", got.message)
	}
}

// TestBackfillRulesWorkerRetryAdvancesAfterFailure 断言重试成功后走的是
// **Advance**（store 侧语义：status 翻回 running、error 清空、failed_cursor 归零），
// 而不是继续停在 failed。SQL 语义由 Models/Badges 的真库用例
// TestBackfillRunsFlipFailedBackToRunning 钉住。
func TestBackfillRulesWorkerRetryAdvancesAfterFailure(t *testing.T) {
	runs := &fakeRunRecorder{}
	worker := &BackfillRulesWorker{
		Runner:   &fakeRunner{next: 500, done: false},
		Enqueuer: &fakeEnqueuer{},
		Runs:     runs,
	}
	// 第一次（river 重试前的失败页）。
	failing := &BackfillRulesWorker{
		Runner:   &fakeRunner{failure: errors.New("db down")},
		Enqueuer: &fakeEnqueuer{},
		Runs:     runs,
	}
	if err := runJob(t, failing, BackfillRulesArgs{BadgeID: 7, CursorUserID: 250, Batch: 500}); err == nil {
		t.Fatal("第一次必须失败")
	}
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 250, Batch: 500}); err != nil {
		t.Fatal(err)
	}
	if len(runs.failures) != 1 || len(runs.advances) != 1 {
		t.Fatalf("期望一次 failed + 一次 advance，得到 failures=%+v advances=%+v", runs.failures, runs.advances)
	}
	if runs.advances[0].done || runs.advances[0].lastUserID != 500 {
		t.Fatalf("重试成功必须落 running 且带新游标，得到 %+v", runs.advances[0])
	}
}

// TestBackfillRulesWorkerFlagsBrokenChainWhenEnqueuerMissing 断言「本页成功但没有
// 入队器」不再静默 return nil，而是落 failed 并把原因写明。
func TestBackfillRulesWorkerFlagsBrokenChainWhenEnqueuerMissing(t *testing.T) {
	runs := &fakeRunRecorder{}
	worker := &BackfillRulesWorker{Runner: &fakeRunner{next: 500, done: false}, Runs: runs}
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 0, Batch: 500}); err != nil {
		t.Fatalf("入队器缺失不应让 river 重试（重试也变不出入队器）：%v", err)
	}
	if len(runs.failures) != 1 {
		t.Fatalf("链断必须可见，得到 %+v", runs.failures)
	}
	if !strings.Contains(runs.failures[0].message, "enqueuer not wired") {
		t.Fatalf("失败原因应说明入队器没接，得到 %q", runs.failures[0].message)
	}
	if len(runs.advances) != 0 {
		t.Fatalf("链断时不应先写 running 进度，得到 %+v", runs.advances)
	}
}

// TestBackfillRulesWorkerReturnsErrorWhenProgressWriteFails 断言状态写失败会让
// river 重试当前页：否则游标永久停在旧值，进度显示与实际永久失真。
func TestBackfillRulesWorkerReturnsErrorWhenProgressWriteFails(t *testing.T) {
	runs := &fakeRunRecorder{advanceErr: errors.New("runs table gone")}
	worker := &BackfillRulesWorker{
		Runner:   &fakeRunner{next: 500, done: false},
		Enqueuer: &fakeEnqueuer{},
		Runs:     runs,
	}
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 0, Batch: 500}); err == nil {
		t.Fatal("状态写失败必须返回 error 让 river 重试当前页")
	}
}

// TestBackfillRulesWorkerKeepsMainErrorWhenFailureWriteFails 断言状态写失败不掩盖主错：
// 返回的错误链里必须还能用 errors.Is 取到 runner 的原始错误。
func TestBackfillRulesWorkerKeepsMainErrorWhenFailureWriteFails(t *testing.T) {
	mainErr := errors.New("db down")
	runs := &fakeRunRecorder{failErr: errors.New("runs table gone")}
	worker := &BackfillRulesWorker{
		Runner:   &fakeRunner{failure: mainErr},
		Enqueuer: &fakeEnqueuer{},
		Runs:     runs,
	}
	err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 250, Batch: 500})
	if !errors.Is(err, mainErr) {
		t.Fatalf("主错被掩盖了：%v", err)
	}
}

// TestBackfillRulesWorkerCompletesLastPageWithoutEnqueuer 是「防过度修正」的反向断言。
//
// 链断（`Enqueuer == nil` 且未结束）必须落 failed；但**末页本来就不需要入队器**，
// 它必须落 completed。把两者混成一律 failed，会让每次回扫的最后一页都显示失败。
func TestBackfillRulesWorkerCompletesLastPageWithoutEnqueuer(t *testing.T) {
	runs := &fakeRunRecorder{}
	worker := &BackfillRulesWorker{
		Runner: &fakeRunner{next: 900, done: true},
		Runs:   runs,
	}
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 500, Batch: 500}); err != nil {
		t.Fatalf("末页不该报错：%v", err)
	}
	if len(runs.failures) != 0 {
		t.Fatalf("末页缺入队器不是失败，得到 %+v", runs.failures)
	}
	if len(runs.advances) != 1 || !runs.advances[0].done {
		t.Fatalf("末页必须落 completed，得到 %+v", runs.advances)
	}
}

// TestBackfillRulesWorkerKeepsRunningWhenEnqueueFails 断言入队下一页失败时状态仍是 running。
//
// 当前页**成功了**（游标已推进），失败的是「安排下一页」。把它写成 failed 会让
// 运营看到一个假的失败页；真实语义是「这一页 ok，下一页没排上，river 会重试」。
func TestBackfillRulesWorkerKeepsRunningWhenEnqueueFails(t *testing.T) {
	runs := &fakeRunRecorder{}
	worker := &BackfillRulesWorker{
		Runner:   &fakeRunner{next: 500, done: false},
		Enqueuer: &fakeEnqueuer{fail: errors.New("queue down")},
		Runs:     runs,
	}
	if err := runJob(t, worker, BackfillRulesArgs{BadgeID: 7, CursorUserID: 0, Batch: 500}); err == nil {
		t.Fatal("入队失败必须返回 error（让 river 重试当前页）")
	}
	if len(runs.failures) != 0 {
		t.Fatalf("入队失败不属于「页失败」，不得落 failed：%+v", runs.failures)
	}
	if len(runs.advances) != 1 || runs.advances[0].done || runs.advances[0].lastUserID != 500 {
		t.Fatalf("状态必须是 running + 已推进游标，得到 %+v", runs.advances)
	}
}
