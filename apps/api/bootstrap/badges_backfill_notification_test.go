package bootstrap

import (
	"context"
	"errors"
	"testing"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// 回扫 runner 的授予通知行为。
//
// 为什么这些断言落在 bootstrap 包：`badgesRuleBackfillRunner` 是装配层的协作者
// （它把 store / evaluator / notifier 三件事缝在一起），既不属于领域层也不属于 job
// 层。真跑一遍需要全站用户 + 规则 + River，而这些断言要证明的是**接线**：
// 逐个用户通知、空授予不发、通知失败不中断本页。

type fakeBadgeUserLister struct {
	userIDs []int64
	err     error
	calls   int
}

func (f *fakeBadgeUserLister) ListActiveUserIDsAfter(_ context.Context, _ int64, _ int) ([]int64, error) {
	f.calls++
	return f.userIDs, f.err
}

type fakeBadgeUserEvaluator struct {
	granted map[int64][]badges.Badge
	err     map[int64]error
	seen    []int64
}

func (f *fakeBadgeUserEvaluator) EvaluateUser(_ context.Context, userID int64) ([]badges.Badge, error) {
	f.seen = append(f.seen, userID)
	if err, ok := f.err[userID]; ok {
		return nil, err
	}
	return f.granted[userID], nil
}

type recordingBackfillNotifier struct {
	users   []int64
	badges  [][]badges.GrantedBadge
	failFor map[int64]error
}

func (n *recordingBackfillNotifier) NotifyBadgeGranted(_ context.Context, userID int64, granted []badges.GrantedBadge) error {
	n.users = append(n.users, userID)
	n.badges = append(n.badges, granted)
	if err, ok := n.failFor[userID]; ok {
		return err
	}
	return nil
}

// TestBackfillRunnerNotifiesEveryGrantingUser 断言「谁被授予，谁就收到通知」。
func TestBackfillRunnerNotifiesEveryGrantingUser(t *testing.T) {
	notifier := &recordingBackfillNotifier{}
	runner := badgesRuleBackfillRunner{
		users:     &fakeBadgeUserLister{userIDs: []int64{11, 12}},
		evaluator: &fakeBadgeUserEvaluator{granted: map[int64][]badges.Badge{
			11: {{ID: 7, Slug: "first-topic", Name: "初次发帖"}},
			12: {{ID: 7, Slug: "first-topic", Name: "初次发帖"}, {ID: 9, Slug: "discussant", Name: "讨论者"}},
		}},
		notifier: notifier,
	}
	next, done, err := runner.BackfillRuleBatch(context.Background(), 7, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !done || next != 12 {
		t.Fatalf("cursor/done = %d/%t, want 12/true", next, done)
	}
	if len(notifier.users) != 2 || notifier.users[0] != 11 || notifier.users[1] != 12 {
		t.Fatalf("通知收件人 = %v, want [11 12]（每个被授予的用户各一次）", notifier.users)
	}
	if len(notifier.badges[1]) != 2 {
		t.Fatalf("用户 12 的徽章数 = %d, want 2（一次调用带该用户全部新授予）", len(notifier.badges[1]))
	}
	// 同一徽章发给两个不同用户：这正是去重键必须带收件人的原因。runner 不做任何
	// 跨用户去重，投影层的键负责这件事。
	if notifier.badges[0][0].Slug != notifier.badges[1][0].Slug {
		t.Fatal("夹具意图是同一 slug 授予两个用户")
	}
}

// TestBackfillRunnerSkipsNotificationWhenNothingGranted 断言零授予不产生通知调用。
func TestBackfillRunnerSkipsNotificationWhenNothingGranted(t *testing.T) {
	notifier := &recordingBackfillNotifier{}
	runner := badgesRuleBackfillRunner{
		users:     &fakeBadgeUserLister{userIDs: []int64{21, 22}},
		evaluator: &fakeBadgeUserEvaluator{granted: map[int64][]badges.Badge{}},
		notifier:  notifier,
	}
	if _, _, err := runner.BackfillRuleBatch(context.Background(), 7, 0, 500); err != nil {
		t.Fatal(err)
	}
	if len(notifier.users) != 0 {
		t.Fatalf("零授予时不得调用通知出口，实际 %v", notifier.users)
	}
}

// TestBackfillRunnerContinuesAfterNotificationFailure 断言通知失败不中断本页。
//
// 授予已经落库；把通知失败上抛会让 river 重跑整页，而重跑时这些授予已存在、
// `EvaluateUser` 返回空，通知就**永远不会再发**——错被重试洗掉。所以这里要求
// 继续评估后面的用户，并且本页整体返回成功。
func TestBackfillRunnerContinuesAfterNotificationFailure(t *testing.T) {
	notifier := &recordingBackfillNotifier{failFor: map[int64]error{31: errors.New("outbox down")}}
	evaluator := &fakeBadgeUserEvaluator{granted: map[int64][]badges.Badge{
		31: {{ID: 7, Slug: "first-topic", Name: "初次发帖"}},
		32: {{ID: 7, Slug: "first-topic", Name: "初次发帖"}},
	}}
	runner := badgesRuleBackfillRunner{
		users:     &fakeBadgeUserLister{userIDs: []int64{31, 32}},
		evaluator: evaluator,
		notifier:  notifier,
	}
	_, _, err := runner.BackfillRuleBatch(context.Background(), 7, 0, 500)
	if err != nil {
		t.Fatalf("通知失败不得让本页失败：%v", err)
	}
	if len(evaluator.seen) != 2 {
		t.Fatalf("评估用户 = %v, want [31 32]（通知失败后仍继续）", evaluator.seen)
	}
	if len(notifier.users) != 2 {
		t.Fatalf("通知调用 = %v, want 两个用户各一次", notifier.users)
	}
}

// TestBackfillRunnerFailsOnEvaluationError 断言评估错误仍然上抛。
//
// 与通知失败相反：评估失败意味着「这一页的授予状态未知」，重试是正确反应
// （授予幂等，重跑只会补上漏掉的）。两者语义不同，不能一起吞。
func TestBackfillRunnerFailsOnEvaluationError(t *testing.T) {
	evaluator := &fakeBadgeUserEvaluator{
		granted: map[int64][]badges.Badge{},
		err:     map[int64]error{41: errors.New("rule engine exploded")},
	}
	runner := badgesRuleBackfillRunner{
		users:     &fakeBadgeUserLister{userIDs: []int64{41, 42}},
		evaluator: evaluator,
		notifier:  &recordingBackfillNotifier{},
	}
	next, done, err := runner.BackfillRuleBatch(context.Background(), 7, 0, 500)
	if err == nil {
		t.Fatal("评估错误必须上抛，让 river 重试本页")
	}
	if done {
		t.Fatal("失败页不得标记完成")
	}
	if next != 41 {
		t.Fatalf("失败游标 = %d, want 41（停在该用户，便于运维定位）", next)
	}
}

// TestBackfillRunnerWithoutNotifierStillGrants 断言未注入通知出口时仍然授予。
//
// 与领域侧 `SetGrantNotifier` 同姿态：缺装配是「授予成功、没有通知」，
// 不是让回扫失败。
func TestBackfillRunnerWithoutNotifierStillGrants(t *testing.T) {
	evaluator := &fakeBadgeUserEvaluator{granted: map[int64][]badges.Badge{
		51: {{ID: 7, Slug: "first-topic", Name: "初次发帖"}},
	}}
	runner := badgesRuleBackfillRunner{
		users:     &fakeBadgeUserLister{userIDs: []int64{51}},
		evaluator: evaluator,
	}
	if _, done, err := runner.BackfillRuleBatch(context.Background(), 7, 0, 500); err != nil || !done {
		t.Fatalf("err/done = %v/%t, want nil/true", err, done)
	}
	if len(evaluator.seen) != 1 {
		t.Fatalf("未注入通知出口也必须评估并授予，seen = %v", evaluator.seen)
	}
}

// TestBackfillRunnerEmptyPageCompletesWithoutPanic 断言空页正常收口。
//
// 回归一个真实崩溃：两条返回都要取 `userIDs[len-1]`，空切片会 index -1 panic。
// 触发条件不是「站上没有活跃用户」这种边角——游标走到末页后 river 仍可能重入同一页
// （重试、并发消费），而 panic 会让 job 无限重试，回扫永远不结束、状态行永远 running。
func TestBackfillRunnerEmptyPageCompletesWithoutPanic(t *testing.T) {
	runner := badgesRuleBackfillRunner{
		users:     &fakeBadgeUserLister{userIDs: nil},
		evaluator: &fakeBadgeUserEvaluator{granted: map[int64][]badges.Badge{}},
		notifier:  &recordingBackfillNotifier{},
	}
	next, done, err := runner.BackfillRuleBatch(context.Background(), 7, 42, 500)
	if err != nil {
		t.Fatalf("空页不得报错：%v", err)
	}
	if !done {
		t.Fatal("空页必须标记完成，否则 river 会一直重入")
	}
	if next != 42 {
		t.Fatalf("空页游标 = %d, want 42（原样返回，停在确实处理到的用户）", next)
	}
}
