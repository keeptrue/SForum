package bootstrap

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
)

// TestBadgesBackfillWorkerWiresProgressRecorder 是回扫进度的**装配守卫**。
//
// worker 对 `Runs == nil` 是容错的（退化成只写日志），这是为了不让一次装配漏接
// 把整条回扫停摆。代价是：漏接不会报错，只会让 `GET /admin/badges/backfill-status`
// 永远回空 items——「回扫在跑、进度看不见」正是这一刀要消灭的假绿形态。
//
// 所以信号必须落在测试上，而不是 panic：装配失败应该是启动崩溃还是可观测退化是
// 产品决定，测试只需要钉住「bootstrap 的装配路径不会产出 Runs=nil 的 worker」。
func TestBadgesBackfillWorkerWiresProgressRecorder(t *testing.T) {
	worker := newBadgesBackfillWorker(nil, nil, nil, nil)
	if worker == nil {
		t.Fatal("bootstrap 必须产出回扫 worker")
	}
	if worker.Runs == nil {
		t.Fatal("装配漏接进度写口：backfill-status 会永远回空 items")
	}
	if worker.Runner == nil {
		t.Fatal("装配漏接 runner：worker 一跑就返回 requires runner")
	}
}

// TestBadgesBackfillWorkerWiresGrantNotifier 是回扫**授予通知**的装配守卫。
//
// 回扫是自动授予的第五条路径。漏接通知出口时，`badgesRuleBackfillRunner.notifyGranted`
// 对 nil notifier 是静默跳过的（与领域侧 `SetGrantNotifier` 未注入同姿态），
// 于是「运营改严规则 → 回扫补出几百枚徽章 → 收件箱一片安静」会重新出现，
// 而且**零报错、零日志**。这条测试把装配本身钉死。
func TestBadgesBackfillWorkerWiresGrantNotifier(t *testing.T) {
	worker := newBadgesBackfillWorker(nil, nil, nil, nil)
	if worker == nil {
		t.Fatal("bootstrap 必须产出回扫 worker")
	}
	runner, ok := worker.Runner.(badgesRuleBackfillRunner)
	if !ok {
		t.Fatalf("runner 类型 = %T，want badgesRuleBackfillRunner", worker.Runner)
	}
	if runner.notifier == nil {
		t.Fatal("装配漏接授予通知出口：回扫补出的徽章不会产生任何通知行")
	}
	if runner.users == nil {
		t.Fatal("装配漏接用户游标读取：回扫一跑就空转并标记完成")
	}
	if runner.evaluator == nil {
		t.Fatal("装配漏接评估器：回扫不会授予任何徽章")
	}
	// 通知出口必须是那个「有 outbox 就能发」的实现，而不是任意占位。
	if _, ok := runner.notifier.(badgesDomainGrantedNotifier); !ok {
		t.Fatalf("notifier 类型 = %T，want badgesDomainGrantedNotifier", runner.notifier)
	}
}

// TestBadgesBackfillNotifierSharesOutbox 断言回扫与评估 job 用同一个 outbox。
//
// 两套 outbox 的后果不是「多发一条」，而是**邮件偏好/品牌解析各读一份配置**：
// 徽章通知的邮件模板与渠道开关会随入口不同而不同。装配层只有一套 outbox 才是正解，
// 这条测试盯的是「有人为了省一个参数又新建了一个 outbox」。
func TestBadgesBackfillNotifierSharesOutbox(t *testing.T) {
	outbox := notifications.NewOutbox(nil, nil, nil)
	worker := newBadgesBackfillWorker(nil, outbox, nil, nil)
	runner, ok := worker.Runner.(badgesRuleBackfillRunner)
	if !ok {
		t.Fatalf("runner 类型 = %T", worker.Runner)
	}
	notifier, ok := runner.notifier.(badgesDomainGrantedNotifier)
	if !ok {
		t.Fatalf("notifier 类型 = %T", runner.notifier)
	}
	if notifier.badgeGrantedNotifier.outbox != outbox {
		t.Fatal("回扫通知必须复用调用方传入的 outbox，不能自建")
	}
}
