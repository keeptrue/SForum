package main

import (
	"context"
	"fmt"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// runLifecycle 覆盖 install/enable/upgrade/rollback/disable/uninstall 的 plan+execute。
// 外部清理（export/webhook 模拟）写入可审计、可重试 evidence 文件。
func (s *commerceServer) runLifecycle(
	ctx context.Context,
	request *protocolwire.LifecycleRequest,
	progress *pluginv2.ProgressStream,
) error {
	if request == nil || progress == nil {
		return fmt.Errorf("missing lifecycle request")
	}
	stepID := strings.TrimSpace(request.GetStepId())
	if stepID == "" {
		stepID = lifecycleStepID(request.GetAction(), request.GetDryRun())
	}
	actionName := lifecycleActionName(request.GetAction())
	checkpoint := "plan"
	if !request.GetDryRun() {
		checkpoint = "execute"
	}

	// Host lifecycle 校验允许 PLANNED → RUNNING → SUCCEEDED。
	if err := progress.Send(&protocolwire.ProgressUpdate{
		StepId: stepID, State: protocolwire.ProgressState_PROGRESS_STATE_PLANNED,
		CompletedUnits: 0, TotalUnits: 3, Checkpoint: checkpoint + ".start",
	}); err != nil {
		return err
	}
	if err := progress.Send(&protocolwire.ProgressUpdate{
		StepId: stepID, State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
		CompletedUnits: 1, TotalUnits: 3, Checkpoint: checkpoint + ".running",
	}); err != nil {
		return err
	}

	// 外部清理模拟：export 订单、通知下游；失败可重试且留下证据。
	external := []string{}
	switch request.GetAction() {
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_PLAN,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_AFTER,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_DISABLE:
		external = append(external, "export-orders", "revoke-webhooks")
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_PLAN,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_BEFORE,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_AFTER:
		external = append(external, "backup-schema", "migrate-orders")
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_ROLLBACK:
		external = append(external, "restore-schema-snapshot")
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_INSTALL_PLAN,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_INSTALL:
		external = append(external, "provision-schema")
		if !request.GetDryRun() && s.store != nil {
			_ = s.store.ensureSchema(ctx)
		}
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_ENABLE:
		external = append(external, "warm-order-cache")
	}

	evidence := cleanupEvidence{
		Action: actionName, StepID: stepID, DryRun: request.GetDryRun(),
		Forced: request.GetForced(), External: external, Retryable: true,
		Checkpoint: checkpoint,
	}
	if err := s.store.appendCleanupEvidence(evidence); err != nil {
		evidence.Error = err.Error()
		_ = s.store.appendCleanupEvidence(evidence)
		return fmt.Errorf("record lifecycle evidence: %w", err)
	}

	if err := progress.Send(&protocolwire.ProgressUpdate{
		StepId: stepID, State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
		CompletedUnits: 2, TotalUnits: 3, Checkpoint: checkpoint + ".external",
	}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
	}
	return progress.Send(&protocolwire.ProgressUpdate{
		StepId: stepID, State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
		CompletedUnits: 3, TotalUnits: 3, Checkpoint: checkpoint + ".done",
	})
}

func lifecycleActionName(action protocolwire.LifecycleAction) string {
	switch action {
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_INSTALL_PLAN:
		return "install.plan"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_INSTALL:
		return "install.execute"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_ENABLE:
		return "enable.execute"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_DISABLE:
		return "disable.execute"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_PLAN:
		return "upgrade.plan"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_BEFORE:
		return "upgrade.before"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_AFTER:
		return "upgrade.after"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_ROLLBACK:
		return "rollback.execute"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_PLAN:
		return "uninstall.plan"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL:
		return "uninstall.execute"
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_AFTER:
		return "uninstall.after"
	default:
		return "unknown"
	}
}

func lifecycleStepID(action protocolwire.LifecycleAction, dryRun bool) string {
	name := lifecycleActionName(action)
	if dryRun && !strings.HasSuffix(name, ".plan") {
		return name + ".plan"
	}
	return name
}
