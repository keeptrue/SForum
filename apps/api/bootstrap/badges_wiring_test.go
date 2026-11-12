package bootstrap

import (
	"testing"
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
	worker := newBadgesBackfillWorker(nil, nil, nil)
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
