package bootstrap

import (
	"testing"

	"github.com/riverqueue/river"

	trustlevelsjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/TrustLevels"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// TestRegisterTrustLevelWorkersRegistersBothKinds 是「注册了」的唯一可执行判据。
//
// river 不导出 Workers 的枚举接口，所以用**重复注册必须撞车**来观测「已经在里面」：
// 这个 kind 已被注册时 AddWorkerSafely 返回 error。
//
// 为什么需要它（pm 抓到的静默失效）：M4b 第 1 段里两个 Register* 只有定义没有调用点，
// 入队是通的、消费者不存在 —— job 永远躺着、`trust_level.changed` 运行期根本发不出来，
// 而 CI 全绿。把这里的注册行注释掉，本测试必须红。
func TestRegisterTrustLevelWorkersRegistersBothKinds(t *testing.T) {
	registry := supportjobs.NewRegistry()
	registerTrustLevelWorkers(registry, &trustLevelWorkers{}, nil)
	workers, err := registry.Build()
	if err != nil {
		t.Fatalf("build workers: %v", err)
	}
	if err := river.AddWorkerSafely[trustlevelsjobs.RecomputeUserArgs](workers, &trustlevelsjobs.RecomputeUserWorker{}); err == nil {
		t.Fatal("trustlevels.recompute_user 没有注册：job 会入队但永远没人消费，等级变更事件永不发出")
	}
	if err := river.AddWorkerSafely[trustlevelsjobs.RecomputeBatchArgs](workers, &trustlevelsjobs.RecomputeBatchWorker{}); err == nil {
		t.Fatal("trustlevels.recompute_batch 没有注册：策略批量重算与每日 sweep 都会入队但没人消费")
	}
}

// TestRegisterTrustLevelWorkersToleratesMissingDeps 钉住空依赖不 panic。
//
// standalone worker 不受支持（AGENTS.md），所以 deps 可能为 nil；这时只 warn、
// 不注册——但也绝不能炸掉整个 worker 装配。
func TestRegisterTrustLevelWorkersToleratesMissingDeps(t *testing.T) {
	registry := supportjobs.NewRegistry()
	registerTrustLevelWorkers(registry, nil, nil)
	if _, err := registry.Build(); err != nil {
		t.Fatalf("空依赖不该让 registry 构建失败：%v", err)
	}
}
