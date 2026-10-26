package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

type lifecycleCoordinatorRunnerFunc func(context.Context, extensions.Extension, LifecycleInvocation) (LifecycleRunResult, error)

func (f lifecycleCoordinatorRunnerFunc) RunLifecycle(
	ctx context.Context,
	extension extensions.Extension,
	invocation LifecycleInvocation,
) (LifecycleRunResult, error) {
	return f(ctx, extension, invocation)
}

func TestLifecycleCoordinatorRuntimeAdapterMapsEveryActionAndProgress(t *testing.T) {
	tests := []struct {
		model extensions.LifecycleMachineAction
		wire  LifecycleAction
	}{
		{extensions.LifecycleMachineInstallPlan, LifecycleActionInstallPlan},
		{extensions.LifecycleMachineInstallAction, LifecycleActionInstall},
		{extensions.LifecycleMachineEnableAction, LifecycleActionEnable},
		{extensions.LifecycleMachineDisableAction, LifecycleActionDisable},
		{extensions.LifecycleMachineUpgradePlan, LifecycleActionUpgradePlan},
		{extensions.LifecycleMachineUpgradeBefore, LifecycleActionUpgradeBefore},
		{extensions.LifecycleMachineUpgradeAfter, LifecycleActionUpgradeAfter},
		{extensions.LifecycleMachineRollbackAction, LifecycleActionRollback},
		{extensions.LifecycleMachineUninstallPlan, LifecycleActionUninstallPlan},
		{extensions.LifecycleMachineUninstallStep, LifecycleActionUninstall},
		{extensions.LifecycleMachineUninstallAfter, LifecycleActionUninstallAfter},
	}
	for _, test := range tests {
		t.Run(string(test.model), func(t *testing.T) {
			resultValue, _ := structpb.NewStruct(map[string]any{"applied": true})
			adapter := &LifecycleCoordinatorRuntimeAdapter{runner: lifecycleCoordinatorRunnerFunc(func(
				_ context.Context,
				extension extensions.Extension,
				invocation LifecycleInvocation,
			) (LifecycleRunResult, error) {
				if extension.ID != "demo.lifecycle" || extension.PackageDigest != "digest" ||
					invocation.Action != test.wire || invocation.PlanVersion != "demo.lifecycle@3" ||
					invocation.StepID != "stable-step" || invocation.Checkpoint != "resume-2" ||
					!invocation.Forced || invocation.DryRun || invocation.Input.GetSchemaId() != "demo.lifecycle" ||
					invocation.Input.GetSchemaVersion() != "3" || invocation.Input.GetValue().AsMap()["mode"] != "apply" {
					t.Fatalf("invocation = %#v extension=%#v", invocation, extension)
				}
				updates := []LifecycleProgress{
					{State: LifecycleProgressPlanned, TotalUnits: 4, Checkpoint: "resume-2", Message: "planned"},
					{State: LifecycleProgressRunning, CompletedUnits: 1, TotalUnits: 4, Checkpoint: "one", Message: "running"},
					{State: LifecycleProgressWaiting, CompletedUnits: 2, TotalUnits: 4, Checkpoint: "two", Message: "waiting"},
					{State: LifecycleProgressSucceeded, CompletedUnits: 4, TotalUnits: 4, Checkpoint: "done", Message: "complete", Result: &protocolv2.TypedDocument{
						SchemaId: "demo.progress", SchemaVersion: "1", Value: resultValue,
					}},
				}
				for _, update := range updates {
					if err := invocation.OnProgress(update); err != nil {
						return LifecycleRunResult{}, err
					}
				}
				return LifecycleRunResult{
					StepID: invocation.StepID, State: LifecycleProgressSucceeded, Checkpoint: "done",
					Result: updates[3].Result, Progress: updates,
				}, nil
			})}
			progress := make([]string, 0, 3)
			result, err := adapter.RunLifecycleAction(context.Background(), extensions.LifecycleCoordinatorActionRequest{
				Extension: extensions.Extension{ID: "demo.lifecycle", PackageDigest: "digest"},
				Action:    test.model, StepID: "stable-step", PlanVersion: "demo.lifecycle@3",
				Checkpoint: "resume-2", InputDocument: json.RawMessage(`{"mode":"apply"}`), Forced: true,
			}, func(update extensions.LifecycleCoordinatorActionProgress) error {
				progress = append(progress, update.Status+":"+update.Checkpoint)
				return nil
			})
			if err != nil || result.Status != extensions.LifecycleStepSucceeded || result.Checkpoint != "done" ||
				result.CompletedUnits != 4 || result.TotalUnits != 4 || result.Message != "complete" ||
				!slices.Equal(progress, []string{"planned:resume-2", "running:one", "waiting:two"}) {
				t.Fatalf("result = %#v progress=%#v err=%v", result, progress, err)
			}
			var values map[string]any
			if json.Unmarshal(result.ResultDocument, &values) != nil || values["applied"] != true {
				t.Fatalf("result document = %s", result.ResultDocument)
			}
		})
	}
}

func TestLifecycleCoordinatorRuntimeAdapterInputContracts(t *testing.T) {
	calls := 0
	adapter := &LifecycleCoordinatorRuntimeAdapter{runner: lifecycleCoordinatorRunnerFunc(func(
		_ context.Context,
		_ extensions.Extension,
		invocation LifecycleInvocation,
	) (LifecycleRunResult, error) {
		calls++
		if invocation.Input != nil {
			t.Fatalf("empty input = %#v", invocation.Input)
		}
		if err := invocation.OnProgress(LifecycleProgress{State: LifecycleProgressSucceeded}); err != nil {
			return LifecycleRunResult{}, err
		}
		return LifecycleRunResult{State: LifecycleProgressSucceeded}, nil
	})}
	request := extensions.LifecycleCoordinatorActionRequest{
		Extension: extensions.Extension{ID: "demo.lifecycle"}, Action: extensions.LifecycleMachineEnableAction,
		StepID: "step", PlanVersion: "demo.lifecycle@1",
	}
	if result, err := adapter.RunLifecycleAction(context.Background(), request, nil); err != nil || result.Status != extensions.LifecycleStepSucceeded {
		t.Fatalf("empty input result = %#v, %v", result, err)
	}
	for _, document := range []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`null`), json.RawMessage(`{"broken"`)} {
		request.InputDocument = document
		if _, err := adapter.RunLifecycleAction(context.Background(), request, nil); !errors.Is(err, ErrInvalidLifecycleRun) {
			t.Fatalf("invalid input %s = %v", document, err)
		}
	}
	request.Action = "unknown"
	if _, err := adapter.RunLifecycleAction(context.Background(), request, nil); !errors.Is(err, ErrInvalidLifecycleRun) {
		t.Fatalf("unknown action = %v", err)
	}
	if calls != 1 {
		t.Fatalf("runner calls = %d", calls)
	}
}

func TestLifecycleRemoteErrorCarriesCoordinatorFailure(t *testing.T) {
	retryAfter := time.Now().Add(time.Minute).UTC().Round(time.Second)
	remote := &LifecycleRemoteError{
		StepID: "step", State: LifecycleProgressFailed, Code: protocolv2.ErrorCode_ERROR_CODE_CONFLICT,
		Reason: "plugin.schema_conflict", Message: "Schema is already owned.", Retryable: true,
		RetryAfter: retryAfter, Metadata: map[string]string{"resource": "schema", "owner": "other.plugin"},
	}
	failure := remote.LifecycleCoordinatorFailure()
	if failure.Code != "ERROR_CODE_CONFLICT" || failure.Reason != remote.Reason || failure.Message != remote.Message ||
		!failure.Retryable || failure.RetryAfter == nil || !failure.RetryAfter.Equal(retryAfter) {
		t.Fatalf("failure = %#v", failure)
	}
	var metadata map[string]string
	if json.Unmarshal(failure.Metadata, &metadata) != nil || metadata["resource"] != "schema" || metadata["owner"] != "other.plugin" {
		t.Fatalf("metadata = %s", failure.Metadata)
	}
}

func TestLifecycleCoordinatorRuntimeAdapterMapsRemoteFailure(t *testing.T) {
	remote := &LifecycleRemoteError{
		State: LifecycleProgressCancelled, Code: protocolv2.ErrorCode_ERROR_CODE_CANCELLED,
		Reason: "plugin.cancelled", Message: "Cancelled by operator.", Metadata: map[string]string{"source": "operator"},
	}
	adapter := &LifecycleCoordinatorRuntimeAdapter{runner: lifecycleCoordinatorRunnerFunc(func(
		_ context.Context,
		_ extensions.Extension,
		invocation LifecycleInvocation,
	) (LifecycleRunResult, error) {
		terminal := LifecycleProgress{State: LifecycleProgressCancelled, CompletedUnits: 1, TotalUnits: 2, Checkpoint: "half", Message: "cancelled"}
		if err := invocation.OnProgress(terminal); err != nil {
			return LifecycleRunResult{}, err
		}
		return LifecycleRunResult{State: terminal.State, Checkpoint: terminal.Checkpoint}, remote
	})}
	result, err := adapter.RunLifecycleAction(context.Background(), extensions.LifecycleCoordinatorActionRequest{
		Extension: extensions.Extension{ID: "demo.lifecycle"}, Action: extensions.LifecycleMachineEnableAction,
		StepID: "step", PlanVersion: "demo.lifecycle@1",
	}, nil)
	var carrier extensions.LifecycleCoordinatorFailureCarrier
	if !errors.Is(err, context.Canceled) || !errors.As(err, &carrier) || result.Status != extensions.LifecycleStepCancelled ||
		result.Checkpoint != "half" || result.Error.Code != "ERROR_CODE_CANCELLED" || result.Error.Reason != remote.Reason {
		t.Fatalf("result = %#v err=%#v", result, err)
	}
}
