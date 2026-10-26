package extensionsruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

type lifecycleCoordinatorRunner interface {
	RunLifecycle(context.Context, extensions.Extension, LifecycleInvocation) (LifecycleRunResult, error)
}

// LifecycleCoordinatorRuntimeAdapter connects the durable Host coordinator to
// the exact protocol-v2 runtime already frozen by ProtocolStarter.Start.
type LifecycleCoordinatorRuntimeAdapter struct {
	runner lifecycleCoordinatorRunner
}

func NewLifecycleCoordinatorRuntimeAdapter(starter *ProtocolStarter) *LifecycleCoordinatorRuntimeAdapter {
	return &LifecycleCoordinatorRuntimeAdapter{runner: starter}
}

func (a *LifecycleCoordinatorRuntimeAdapter) RunLifecycleAction(
	ctx context.Context,
	request extensions.LifecycleCoordinatorActionRequest,
	onProgress func(extensions.LifecycleCoordinatorActionProgress) error,
) (extensions.LifecycleCoordinatorActionResult, error) {
	if a == nil || a.runner == nil {
		return extensions.LifecycleCoordinatorActionResult{}, extensions.ErrRuntimeUnavailable
	}
	action, err := lifecycleCoordinatorAction(request.Action)
	if err != nil {
		return extensions.LifecycleCoordinatorActionResult{}, err
	}
	input, err := lifecycleCoordinatorInput(request.PlanVersion, request.InputDocument)
	if err != nil {
		return extensions.LifecycleCoordinatorActionResult{}, err
	}

	var latest LifecycleProgress
	run, runErr := a.runner.RunLifecycle(ctx, request.Extension, LifecycleInvocation{
		Action: action, PlanVersion: request.PlanVersion, StepID: request.StepID,
		Checkpoint: request.Checkpoint, Input: input, Forced: request.Forced,
		OnProgress: func(progress LifecycleProgress) error {
			latest = cloneLifecycleProgress(progress)
			if isLifecycleTerminal(progress.State) || onProgress == nil {
				return nil
			}
			mapped, mapErr := lifecycleCoordinatorProgressUpdate(progress)
			if mapErr != nil {
				return mapErr
			}
			return onProgress(mapped)
		},
	})
	result, mapErr := lifecycleCoordinatorResult(run, latest)
	if mapErr != nil {
		return extensions.LifecycleCoordinatorActionResult{}, mapErr
	}
	var remote *LifecycleRemoteError
	if errors.As(runErr, &remote) {
		result.Error = remote.LifecycleCoordinatorFailure()
	}
	return result, runErr
}

func lifecycleCoordinatorAction(action extensions.LifecycleMachineAction) (LifecycleAction, error) {
	switch action {
	case extensions.LifecycleMachineInstallPlan:
		return LifecycleActionInstallPlan, nil
	case extensions.LifecycleMachineInstallAction:
		return LifecycleActionInstall, nil
	case extensions.LifecycleMachineEnableAction:
		return LifecycleActionEnable, nil
	case extensions.LifecycleMachineDisableAction:
		return LifecycleActionDisable, nil
	case extensions.LifecycleMachineUpgradePlan:
		return LifecycleActionUpgradePlan, nil
	case extensions.LifecycleMachineUpgradeBefore:
		return LifecycleActionUpgradeBefore, nil
	case extensions.LifecycleMachineUpgradeAfter:
		return LifecycleActionUpgradeAfter, nil
	case extensions.LifecycleMachineRollbackAction:
		return LifecycleActionRollback, nil
	case extensions.LifecycleMachineUninstallPlan:
		return LifecycleActionUninstallPlan, nil
	case extensions.LifecycleMachineUninstallStep:
		return LifecycleActionUninstall, nil
	case extensions.LifecycleMachineUninstallAfter:
		return LifecycleActionUninstallAfter, nil
	default:
		return 0, fmt.Errorf("%w: unsupported coordinator lifecycle action %q", ErrInvalidLifecycleRun, action)
	}
}

func lifecycleCoordinatorInput(planVersion string, document json.RawMessage) (*protocolv2.TypedDocument, error) {
	if len(bytes.TrimSpace(document)) == 0 {
		return nil, nil
	}
	schemaID, schemaVersion, err := protocolV2SchemaRef(planVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid lifecycle input schema: %v", ErrInvalidLifecycleRun, err)
	}
	var values map[string]any
	if err := json.Unmarshal(document, &values); err != nil || values == nil {
		return nil, fmt.Errorf("%w: lifecycle action input must be a JSON object", ErrInvalidLifecycleRun)
	}
	value, err := structpb.NewStruct(values)
	if err != nil {
		return nil, fmt.Errorf("%w: encode lifecycle action input: %v", ErrInvalidLifecycleRun, err)
	}
	return &protocolv2.TypedDocument{SchemaId: schemaID, SchemaVersion: schemaVersion, Value: value}, nil
}

func lifecycleCoordinatorProgressUpdate(progress LifecycleProgress) (extensions.LifecycleCoordinatorActionProgress, error) {
	status, err := lifecycleCoordinatorStatus(progress.State)
	if err != nil {
		return extensions.LifecycleCoordinatorActionProgress{}, err
	}
	return extensions.LifecycleCoordinatorActionProgress{
		Status: status, Checkpoint: progress.Checkpoint,
		CompletedUnits: int64(progress.CompletedUnits), TotalUnits: int64(progress.TotalUnits), Message: progress.Message,
	}, nil
}

func lifecycleCoordinatorResult(run LifecycleRunResult, latest LifecycleProgress) (extensions.LifecycleCoordinatorActionResult, error) {
	state := run.State
	if state == 0 {
		state = latest.State
	}
	status := ""
	var err error
	if state != 0 {
		status, err = lifecycleCoordinatorStatus(state)
		if err != nil {
			return extensions.LifecycleCoordinatorActionResult{}, err
		}
	}
	resultDocument, err := lifecycleCoordinatorDocumentJSON(run.Result)
	if err != nil {
		return extensions.LifecycleCoordinatorActionResult{}, err
	}
	return extensions.LifecycleCoordinatorActionResult{
		Status: status, Checkpoint: run.Checkpoint,
		CompletedUnits: int64(latest.CompletedUnits), TotalUnits: int64(latest.TotalUnits), Message: latest.Message,
		ResultDocument: resultDocument,
	}, nil
}

func lifecycleCoordinatorStatus(state LifecycleProgressState) (string, error) {
	switch state {
	case LifecycleProgressPlanned:
		return extensions.LifecycleStepPlanned, nil
	case LifecycleProgressRunning:
		return extensions.LifecycleStepRunning, nil
	case LifecycleProgressWaiting:
		return extensions.LifecycleStepWaiting, nil
	case LifecycleProgressSucceeded:
		return extensions.LifecycleStepSucceeded, nil
	case LifecycleProgressFailed:
		return extensions.LifecycleStepFailed, nil
	case LifecycleProgressCancelled:
		return extensions.LifecycleStepCancelled, nil
	default:
		return "", fmt.Errorf("%w: unsupported lifecycle progress state %s", ErrInvalidLifecycleStream, state)
	}
}

func lifecycleCoordinatorDocumentJSON(document *protocolv2.TypedDocument) (json.RawMessage, error) {
	if document == nil {
		return nil, nil
	}
	if document.GetValue() == nil {
		return json.RawMessage(`{}`), nil
	}
	value, err := json.Marshal(document.GetValue().AsMap())
	if err != nil {
		return nil, fmt.Errorf("%w: encode lifecycle result: %v", ErrInvalidLifecycleStream, err)
	}
	return value, nil
}

var _ extensions.LifecycleCoordinatorRuntime = (*LifecycleCoordinatorRuntimeAdapter)(nil)
