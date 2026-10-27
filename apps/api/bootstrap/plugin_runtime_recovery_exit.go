package bootstrap

import (
	"errors"
	"fmt"
	"sync"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var (
	// errPluginRuntimeRecoveryExitForeignSource 表示这次 revision 不是 recheck 发布的：
	// 人工改库、启动重放、admin 端点都会撞在这里。
	errPluginRuntimeRecoveryExitForeignSource = errors.New(
		"bootstrap: recovery-only exit requires a recheck-published recovery revision",
	)
	// errPluginRuntimeRecoveryExitStaleRevision 表示 revision 不新于进入 recovery-only 时
	// 那条 publication：没有新的显式 recheck，就不许再撤销一次。
	errPluginRuntimeRecoveryExitStaleRevision = errors.New(
		"bootstrap: recovery-only exit requires a revision newer than the failing publication",
	)
	// errPluginRuntimeRecoveryExitAlreadyConsumed 是防抖动闸门：同一 revision 只能撤销一次。
	errPluginRuntimeRecoveryExitAlreadyConsumed = errors.New(
		"bootstrap: this recovery revision was already consumed; a new recheck is required",
	)
)

// pluginRuntimeRecoveryExitRequest 是一次撤销申请。收敛是否成功由调用方先保证：
// 闸门只回答"这条 revision 有没有资格触发撤销"，不负责收敛。
type pluginRuntimeRecoveryExitRequest struct {
	Revision int64
	Reason   extensions.PluginRuntimePublicationReason
}

// pluginRuntimeRecoveryExitGate 把"可撤销"锁死在一条路径上。
//
// 三条约束的落点：
//  1. 来源：只接受 reason='recovery' 且 revision > failedRevision；
//  2. 收敛：由调用方在 Consume 之前完成首轮 ready 检查，闸门不参与；
//  3. 一次性：消费过的 revision 永久作废，同一 revision 不可能撤销两次。
//
// 第 3 条在"尝试撤销之前"就消费：即使收敛随后失败，这条 revision 也不得复用——
// 否则间歇性失败的扩展会让 503/200 反复横跳。
type pluginRuntimeRecoveryExitGate struct {
	mu             sync.Mutex
	failedRevision int64
	consumed       map[int64]struct{}
}

func newPluginRuntimeRecoveryExitGate(failedRevision int64) *pluginRuntimeRecoveryExitGate {
	return &pluginRuntimeRecoveryExitGate{
		failedRevision: failedRevision,
		consumed:       make(map[int64]struct{}),
	}
}

// Consume 校验并消费一次撤销资格。返回 nil 表示调用方可以撤销 recovery-only；
// 返回错误表示保持 503，不得放行。
func (g *pluginRuntimeRecoveryExitGate) Consume(request pluginRuntimeRecoveryExitRequest) error {
	if g == nil {
		return errPluginRuntimeRecoveryExitForeignSource
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if request.Reason != extensions.PluginRuntimePublicationRecovery {
		return fmt.Errorf("%w: reason=%q", errPluginRuntimeRecoveryExitForeignSource, request.Reason)
	}
	if request.Revision <= g.failedRevision {
		return fmt.Errorf("%w: revision=%d failed=%d",
			errPluginRuntimeRecoveryExitStaleRevision, request.Revision, g.failedRevision)
	}
	if _, seen := g.consumed[request.Revision]; seen {
		return fmt.Errorf("%w: revision=%d", errPluginRuntimeRecoveryExitAlreadyConsumed, request.Revision)
	}
	g.consumed[request.Revision] = struct{}{}
	return nil
}

// Reset 在重新进入 recovery-only 时把基线换成本次失败的那条 revision。
// 已消费的 revision 仍然保持作废：重新进入不复活旧资格。
func (g *pluginRuntimeRecoveryExitGate) Reset(failedRevision int64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failedRevision = failedRevision
}

func (g *pluginRuntimeRecoveryExitGate) FailedRevision() int64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.failedRevision
}
