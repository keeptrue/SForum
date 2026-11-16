package trustlevels_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// 本文件覆盖 P3-10 的账本乐观锁：读账本 → 求值 → 写账本 → 发事件四步里，
// 「写」必须是带版本条件的 CAS，冲突就重读重算。否则并发重算会带着旧的
// FromLevel 各发一条事件（账本最终一致，但下游通知/徽章会重复或错序）。

// syncPublisher 是并发安全的事件记录器（fixture 自带的 recordingPublisher 会竞态）。
type syncPublisher struct {
	mu        sync.Mutex
	envelopes []appevents.Envelope
}

func (p *syncPublisher) Emit(_ context.Context, envelope appevents.Envelope) appevents.Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.envelopes = append(p.envelopes, envelope)
	return appevents.Result{OK: true}
}

func (p *syncPublisher) snapshot() []appevents.Envelope {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]appevents.Envelope(nil), p.envelopes...)
}

// barrierLedger 让前两次 LoadLedger 在读完之后互相等待，确保两个并发重算读到
// **同一个** revision。没有这道闸，「谁先读完」由调度决定，第二个可能直接读到
// 新值而不触发 CAS 冲突——那样测的就不是并发，而是运气。
type barrierLedger struct {
	inner trustlevels.LedgerStore
	mu    sync.Mutex
	reads int
	both  chan struct{}
}

func (l *barrierLedger) LoadLedger(ctx context.Context, userID int64) (trustlevels.LedgerEntry, bool, error) {
	entry, found, err := l.inner.LoadLedger(ctx, userID)
	if err != nil {
		return entry, found, err
	}
	l.mu.Lock()
	l.reads++
	reads := l.reads
	if reads == 2 {
		close(l.both)
	}
	l.mu.Unlock()
	// 只对齐前两次读；冲突后的重读不能再等，否则死锁。
	if reads <= 2 {
		select {
		case <-l.both:
		case <-time.After(10 * time.Second):
			return trustlevels.LedgerEntry{}, false, errors.New("barrier ledger: peer recompute never reached the ledger read")
		}
	}
	return entry, found, nil
}

func (l *barrierLedger) SaveLedgerCAS(ctx context.Context, userID int64, expectedRevision int64, entry trustlevels.LedgerEntry) (bool, error) {
	return l.inner.SaveLedgerCAS(ctx, userID, expectedRevision, entry)
}

// conflictLedger 的 CAS 永远报「被抢先」，用来覆盖重试用尽的失败姿态。
type conflictLedger struct {
	inner trustlevels.LedgerStore
	mu    sync.Mutex
	cas   int
}

func (l *conflictLedger) LoadLedger(ctx context.Context, userID int64) (trustlevels.LedgerEntry, bool, error) {
	return l.inner.LoadLedger(ctx, userID)
}

func (l *conflictLedger) SaveLedgerCAS(context.Context, int64, int64, trustlevels.LedgerEntry) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cas++
	return false, nil
}

func (l *conflictLedger) casAttempts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cas
}

// ① 两并发重算同一用户：只有一个写事件，另一个重试后基于新值算出「同级」。
func TestRecomputeConcurrentSameUserEmitsExactlyOneEvent(t *testing.T) {
	fixture := newRecomputeFixture(t, recomputePolicy(t, ""))
	fixture.seedLedger(t, 2, "resident", time.Now().Add(-48*time.Hour).UTC().Truncate(time.Second))

	publisher := &syncPublisher{}
	gated := &barrierLedger{inner: fixture.ledger, both: make(chan struct{})}
	recomputer := trustlevels.NewRecomputer(
		fixture.resolver, gated, trustlevels.NewPostgresUserTrustInputReader(fixture.pool), publisher,
	)

	outcomes := make([]trustlevels.RecomputeOutcome, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			outcomes[index], errs[index] = recomputer.RecomputeUser(fixture.ctx, fixture.userID)
		}(i)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("recompute %d: %v", index, err)
		}
	}

	envelopes := publisher.snapshot()
	if len(envelopes) != 1 {
		t.Fatalf("事件数 = %d，并发重算必须恰好发 1 条（重复发送会让订阅方重复处理）", len(envelopes))
	}
	if got := envelopes[0].Payload["fromLevel"]; got != 2 {
		t.Fatalf("fromLevel = %v, want 2（必须来自最新的账本基准）", got)
	}
	if got := envelopes[0].Payload["toLevel"]; got != 3 {
		t.Fatalf("toLevel = %v, want 3", got)
	}
	emitted := 0
	for _, outcome := range outcomes {
		if outcome.EventEmitted {
			emitted++
		}
	}
	if emitted != 1 {
		t.Fatalf("EventEmitted 次数 = %d，want 1（outcomes=%#v）", emitted, outcomes)
	}
	// 落败方必须是因为「重读后发现同级」而跳过，而不是重试用尽。
	for index, outcome := range outcomes {
		if outcome.EventEmitted {
			continue
		}
		if outcome.Skipped != "unchanged" {
			t.Fatalf("落败方 outcome[%d].Skipped = %q，want unchanged（outcomes=%#v）", index, outcome.Skipped, outcomes)
		}
	}
	// 账本只能被推进一次：level 3、revision 2（种子写是 revision 1）。
	entry := fixture.ledgerEntry(t)
	if entry.Level != 3 || entry.Revision != 2 {
		t.Fatalf("ledger = level %d revision %d，want 3/2", entry.Level, entry.Revision)
	}
}

// ② 连续两次重算：第二次是「同级」，不产生重复事件、不再推进 revision。
func TestRecomputeSequentialRunsDoNotDuplicateEvent(t *testing.T) {
	fixture := newRecomputeFixture(t, recomputePolicy(t, ""))
	fixture.seedLedger(t, 2, "resident", time.Now().Add(-48*time.Hour).UTC().Truncate(time.Second))

	first, err := fixture.recomputer.RecomputeUser(fixture.ctx, fixture.userID)
	if err != nil {
		t.Fatalf("first recompute: %v", err)
	}
	if !first.EventEmitted {
		t.Fatalf("首轮应发事件，实际 skipped=%q", first.Skipped)
	}
	second, err := fixture.recomputer.RecomputeUser(fixture.ctx, fixture.userID)
	if err != nil {
		t.Fatalf("second recompute: %v", err)
	}
	if second.EventEmitted || second.Skipped != "unchanged" {
		t.Fatalf("次轮 = %#v，want skipped=unchanged 且不发事件", second)
	}
	if len(fixture.publisher.envelopes) != 1 {
		t.Fatalf("事件数 = %d，want 1", len(fixture.publisher.envelopes))
	}
	if entry := fixture.ledgerEntry(t); entry.Level != 3 || entry.Revision != 2 {
		t.Fatalf("ledger = level %d revision %d，want 3/2（同级不写账本）", entry.Level, entry.Revision)
	}
}

// ③ CAS 一直被抢先：重试用尽后不发事件、不 panic，账本保持原样。
func TestRecomputeGivesUpWithoutEmittingStaleEvent(t *testing.T) {
	fixture := newRecomputeFixture(t, recomputePolicy(t, ""))
	fixture.seedLedger(t, 2, "resident", time.Now().Add(-48*time.Hour).UTC().Truncate(time.Second))

	publisher := &syncPublisher{}
	conflicting := &conflictLedger{inner: fixture.ledger}
	recomputer := trustlevels.NewRecomputer(
		fixture.resolver, conflicting, trustlevels.NewPostgresUserTrustInputReader(fixture.pool), publisher,
	)

	outcome, err := recomputer.RecomputeUser(fixture.ctx, fixture.userID)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if outcome.EventEmitted || outcome.LedgerWritten {
		t.Fatalf("冲突重试用尽后不得写账本/发事件：%#v", outcome)
	}
	if outcome.Skipped != "ledger-conflict" {
		t.Fatalf("Skipped = %q，want ledger-conflict", outcome.Skipped)
	}
	if len(publisher.snapshot()) != 0 {
		t.Fatalf("重试用尽仍发了事件：%#v", publisher.snapshot())
	}
	if conflicting.casAttempts() != 3 {
		t.Fatalf("CAS 尝试次数 = %d，want 3（有限次重试）", conflicting.casAttempts())
	}
	if entry := fixture.ledgerEntry(t); entry.Level != 2 || entry.Revision != 1 {
		t.Fatalf("ledger 被改写：level %d revision %d，want 2/1", entry.Level, entry.Revision)
	}
}

// staleThenLiveActivity 让同一 resolver 的第一次求值用旧统计、之后用新统计，
// 用来复现「落败方手里握着一份比赢家更旧的求值结果」这个真实交错。
type staleThenLiveActivity struct {
	stale trustlevels.Activity
	live  trustlevels.Activity
	mu    sync.Mutex
	calls int
}

func (r *staleThenLiveActivity) ActivityFor(context.Context, int64) (trustlevels.Activity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls == 1 {
		return r.stale, nil
	}
	return r.live, nil
}

func (r *staleThenLiveActivity) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// staleFirstLedger 第一次读账本返回「冲突前的快照」，之后返回真实账本，
// 模拟「读完账本、还没 CAS 成功，就被别的重算插进来写了一次」。
type staleFirstLedger struct {
	inner trustlevels.LedgerStore
	stale trustlevels.LedgerEntry
	mu    sync.Mutex
	loads int
}

func (l *staleFirstLedger) LoadLedger(ctx context.Context, userID int64) (trustlevels.LedgerEntry, bool, error) {
	l.mu.Lock()
	l.loads++
	loads := l.loads
	l.mu.Unlock()
	if loads == 1 {
		return l.stale, true, nil
	}
	return l.inner.LoadLedger(ctx, userID)
}

func (l *staleFirstLedger) SaveLedgerCAS(ctx context.Context, userID int64, expectedRevision int64, entry trustlevels.LedgerEntry) (bool, error) {
	return l.inner.SaveLedgerCAS(ctx, userID, expectedRevision, entry)
}

func (l *staleFirstLedger) loadCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loads
}

// ③' 冲突重试必须**重新求值**，不能拿旧 evaluation 写回账本。
//
// 反例形状（审查员实测）：赢家已把账本写成 level=4/leader，落败方握着更旧的
// evaluation（TL3）只重读账本就 CAS，于是把账本写回 level=3/veteran 并发 `4→3`
// 降级事件——账本最终「一致」了，但一致到错误的值上。
func TestRecomputeRetryReevaluatesAndNeverWritesStaleResult(t *testing.T) {
	fixture := newRecomputeFixture(t, recomputePolicy(t, ""))
	fixture.seedLedger(t, 2, "resident", time.Now().Add(-48*time.Hour).UTC().Truncate(time.Second))
	staleLedgerEntry, found, err := fixture.ledger.LoadLedger(fixture.ctx, fixture.userID)
	if err != nil || !found {
		t.Fatalf("load seeded ledger: %v (found=%v)", err, found)
	}

	// 赢家看到的统计已经涨到 TL4；落败方手里还是 TL3 的那份快照。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE user_activity
		SET days_visited = 120, topic_count = 30, comment_count = 300,
		    like_received_count = 120, like_given_count = 90, bookmark_count = 25
		WHERE user_id = $1`, fixture.userID); err != nil {
		t.Fatalf("bump activity: %v", err)
	}
	liveActivity := trustlevels.Activity{
		DaysVisited: 120, TopicCount: 30, CommentCount: 300,
		LikeReceivedCount: 120, LikeGivenCount: 90, BookmarkCount: 25,
	}
	staleActivity := trustlevels.Activity{
		DaysVisited: 60, TopicCount: 10, CommentCount: 100,
		LikeReceivedCount: 40, LikeGivenCount: 25, BookmarkCount: 8,
	}

	publisher := &syncPublisher{}
	inputs := trustlevels.NewPostgresUserTrustInputReader(fixture.pool)
	winner := trustlevels.NewRecomputer(fixture.resolver, fixture.ledger, inputs, publisher)

	activity := &staleThenLiveActivity{stale: staleActivity, live: liveActivity}
	loserResolver := trustlevels.NewResolver(activity, loaderPolicySource{options: fixture.options})
	ledger := &staleFirstLedger{inner: fixture.ledger, stale: staleLedgerEntry}
	loser := trustlevels.NewRecomputer(loserResolver, ledger, inputs, publisher)

	// 赢家先跑完：账本 4/leader，事件 2→4。
	winnerOutcome, err := winner.RecomputeUser(fixture.ctx, fixture.userID)
	if err != nil {
		t.Fatalf("winner: %v", err)
	}
	if !winnerOutcome.EventEmitted || winnerOutcome.Evaluation.Level != 4 {
		t.Fatalf("赢家 outcome = %#v，want level 4 + 发事件", winnerOutcome)
	}
	if entry := fixture.ledgerEntry(t); entry.Level != 4 || entry.TierID != "leader" || entry.Revision != 2 {
		t.Fatalf("赢家写后 ledger = level %d tier %s revision %d，want 4/leader/2", entry.Level, entry.TierID, entry.Revision)
	}

	// 落败方：第一轮用旧求值(TL3)+旧快照(rev1) → CAS 冲突 → 重试必须重新求值。
	loserOutcome, err := loser.RecomputeUser(fixture.ctx, fixture.userID)
	if err != nil {
		t.Fatalf("loser: %v", err)
	}
	if loserOutcome.EventEmitted || loserOutcome.LedgerWritten {
		t.Fatalf("落败方不得写账本/发事件：%#v", loserOutcome)
	}
	if loserOutcome.Skipped != "unchanged" {
		t.Fatalf("落败方 Skipped = %q，want unchanged（重试后基于新求值发现同级）", loserOutcome.Skipped)
	}
	// 直接证据：真的重算了（两次求值）与真的重读了（两次读账本）。
	if got := activity.count(); got != 2 {
		t.Fatalf("求值次数 = %d，want 2（冲突后必须重新求值，而不是沿用旧 evaluation）", got)
	}
	if got := ledger.loadCount(); got != 2 {
		t.Fatalf("读账本次数 = %d，want 2", got)
	}

	// 反例断言：账本必须停在赢家的 4/leader，且事件流里不能出现降级。
	entry := fixture.ledgerEntry(t)
	if entry.Level != 4 || entry.TierID != "leader" || entry.Revision != 2 {
		t.Fatalf("落败方把账本写脏了：level %d tier %s revision %d，want 4/leader/2", entry.Level, entry.TierID, entry.Revision)
	}
	envelopes := publisher.snapshot()
	if len(envelopes) != 1 {
		t.Fatalf("事件数 = %d，want 1（只有赢家的 2→4）：%#v", len(envelopes), envelopes)
	}
	if got := envelopes[0].Payload["fromLevel"]; got != 2 {
		t.Fatalf("事件 fromLevel = %v，want 2", got)
	}
	if got := envelopes[0].Payload["toLevel"]; got != 4 {
		t.Fatalf("事件 toLevel = %v，want 4", got)
	}
	for index, envelope := range envelopes {
		from, _ := envelope.Payload["fromLevel"].(int)
		to, _ := envelope.Payload["toLevel"].(int)
		if from > to {
			t.Fatalf("事件[%d] 是降级 %d→%d（错向事件）：%#v", index, from, to, envelope.Payload)
		}
	}
}
