package bootstrap

import (
	"errors"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// 三条约束的允许/拒绝两侧。
func TestPluginRuntimeRecoveryExitGateRequiresRecheckRevision(t *testing.T) {
	gate := newPluginRuntimeRecoveryExitGate(934)

	// 拒绝：非 recovery 来源（人工改库 / 启动重放 / admin 端点都落在这里）。
	for _, reason := range []extensions.PluginRuntimePublicationReason{
		extensions.PluginRuntimePublicationStartupReconcile,
		extensions.PluginRuntimePublicationEnable,
		extensions.PluginRuntimePublicationDisable,
		extensions.PluginRuntimePublicationUpgrade,
		extensions.PluginRuntimePublicationRollback,
		extensions.PluginRuntimePublicationUninstall,
	} {
		err := gate.Consume(pluginRuntimeRecoveryExitRequest{Revision: 935, Reason: reason})
		if !errors.Is(err, errPluginRuntimeRecoveryExitForeignSource) {
			t.Fatalf("reason %q must not authorize an exit, got %v", reason, err)
		}
	}

	// 拒绝：不新于失败时那条 publication。
	for _, revision := range []int64{934, 900, 0, -1} {
		err := gate.Consume(pluginRuntimeRecoveryExitRequest{
			Revision: revision, Reason: extensions.PluginRuntimePublicationRecovery,
		})
		if !errors.Is(err, errPluginRuntimeRecoveryExitStaleRevision) {
			t.Fatalf("revision %d must be stale, got %v", revision, err)
		}
	}

	// 允许：recheck 发布的新 revision。
	if err := gate.Consume(pluginRuntimeRecoveryExitRequest{
		Revision: 935, Reason: extensions.PluginRuntimePublicationRecovery,
	}); err != nil {
		t.Fatalf("recheck revision must authorize an exit: %v", err)
	}
}

// 防抖动：同一 revision 只能撤销一次，重新进入 recovery-only 也不复活旧资格。
func TestPluginRuntimeRecoveryExitGateConsumesEachRevisionOnce(t *testing.T) {
	gate := newPluginRuntimeRecoveryExitGate(934)
	request := pluginRuntimeRecoveryExitRequest{
		Revision: 935, Reason: extensions.PluginRuntimePublicationRecovery,
	}
	if err := gate.Consume(request); err != nil {
		t.Fatalf("first exit: %v", err)
	}
	if err := gate.Consume(request); !errors.Is(err, errPluginRuntimeRecoveryExitAlreadyConsumed) {
		t.Fatalf("second exit on the same revision must be refused, got %v", err)
	}

	// 撤销后再次收敛失败 → 重新进入 recovery-only，基线推进到新的失败 revision；
	// 旧 revision 仍然作废，必须等一次新的 recheck。
	gate.Reset(940)
	err := gate.Consume(request)
	if !errors.Is(err, errPluginRuntimeRecoveryExitAlreadyConsumed) &&
		!errors.Is(err, errPluginRuntimeRecoveryExitStaleRevision) {
		t.Fatalf("consumed revision must stay refused after re-entry, got %v", err)
	}
	if err := gate.Consume(pluginRuntimeRecoveryExitRequest{
		Revision: 941, Reason: extensions.PluginRuntimePublicationRecovery,
	}); err != nil {
		t.Fatalf("a new recheck revision must authorize the next exit: %v", err)
	}
	if gate.FailedRevision() != 940 {
		t.Fatalf("failed revision = %d, want 940", gate.FailedRevision())
	}
}
