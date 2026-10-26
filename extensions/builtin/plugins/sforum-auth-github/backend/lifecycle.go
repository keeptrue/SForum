package main

import (
	"context"
	"errors"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const lifecycleContract = extID + ".lifecycle@1"

// handleLifecycle acknowledges the Host-owned lifecycle fence. This adapter has
// no provider side effects: OAuth credentials, identity links, and sessions
// remain exclusively owned by the Host and are never read here.
func handleLifecycle(
	ctx context.Context,
	request *protocolwire.LifecycleRequest,
	progress *pluginv2.ProgressStream,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request == nil || request.GetContext() == nil ||
		request.GetContext().GetExtension().GetExtensionId() != extID ||
		request.GetPlanVersion() != lifecycleContract || request.GetStepId() == "" {
		return errors.New("invalid lifecycle request")
	}
	switch request.GetAction() {
	case protocolwire.LifecycleAction_LIFECYCLE_ACTION_INSTALL_PLAN,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_INSTALL,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_ENABLE,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_DISABLE,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_PLAN,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_BEFORE,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UPGRADE_AFTER,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_ROLLBACK,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_PLAN,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL,
		protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_AFTER:
	default:
		return errors.New("unsupported lifecycle action")
	}
	return progress.Send(&protocolwire.ProgressUpdate{
		StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
		CompletedUnits: 1, TotalUnits: 1, Checkpoint: "complete",
	})
}
