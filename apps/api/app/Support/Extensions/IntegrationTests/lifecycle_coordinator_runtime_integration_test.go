package extensionsruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func TestLifecycleCoordinatorRuntimeAdapterUsesFrozenProtocolV2(t *testing.T) {
	starter, extension := p4LifecycleStart(t)
	adapter := extensionsruntime.NewLifecycleCoordinatorRuntimeAdapter(starter)

	// The adapter supplies the caller snapshot, while ProtocolStarter remains
	// authoritative for the lifecycle contract frozen at process start.
	extension.Manifest.Lifecycle.ContractVersion = "forged.lifecycle@9"
	extension.Manifest.Lifecycle.Uninstall = nil
	progress := make([]string, 0, 2)
	result, err := adapter.RunLifecycleAction(context.Background(), p4LifecycleCoordinatorRequest(t, extension, map[string]any{
		"mode": "adapter-success", "expectedAction": float64(protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_AFTER),
	}), func(update extensions.LifecycleCoordinatorActionProgress) error {
		progress = append(progress, update.Status+":"+update.Checkpoint)
		return nil
	})
	if err != nil || result.Status != extensions.LifecycleStepSucceeded || result.Checkpoint != "done" ||
		result.CompletedUnits != 3 || result.TotalUnits != 3 ||
		!slices.Equal(progress, []string{"planned:resume-7", "running:half"}) {
		t.Fatalf("result = %#v progress=%#v err=%v", result, progress, err)
	}
	var values map[string]any
	if json.Unmarshal(result.ResultDocument, &values) != nil || values["applied"] != true {
		t.Fatalf("result document = %s", result.ResultDocument)
	}
}

func TestLifecycleCoordinatorRuntimeAdapterCallbackFailureCancelsStream(t *testing.T) {
	starter, extension := p4LifecycleStart(t)
	adapter := extensionsruntime.NewLifecycleCoordinatorRuntimeAdapter(starter)
	cancelled := filepath.Join(t.TempDir(), "cancelled")
	sentinel := errors.New("persist progress failed")
	request := p4LifecycleCoordinatorRequest(t, extension, map[string]any{
		"mode": "adapter-callback-cancel", "cancelledMarker": cancelled,
		"expectedAction": float64(protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNINSTALL_AFTER),
	})
	_, err := adapter.RunLifecycleAction(context.Background(), request, func(extensions.LifecycleCoordinatorActionProgress) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("callback error = %v", err)
	}
	p4LifecycleAwaitFile(t, cancelled)
}

func p4LifecycleCoordinatorRequest(t *testing.T, extension extensions.Extension, values map[string]any) extensions.LifecycleCoordinatorActionRequest {
	t.Helper()
	document, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.LifecycleCoordinatorActionRequest{
		Extension: extension, Operation: extensions.LifecycleMachineUninstall,
		Action: extensions.LifecycleMachineUninstallAfter, StepID: "enable-primary", PlanVersion: "p4.lifecycle@1",
		Checkpoint: "resume-7", InputDocument: document, Forced: true,
	}
}
