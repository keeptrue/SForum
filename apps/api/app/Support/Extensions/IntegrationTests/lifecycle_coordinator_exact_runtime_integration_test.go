package extensionsruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

const exactCoordinatorHelperEnv = "SFORUM_EXACT_COORDINATOR_HELPER"

func TestExactLifecycleCoordinatorRuntimeAdapterUsesManagedStagedProcess(t *testing.T) {
	extension := exactCoordinatorIntegrationExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: p4LifecycleTrust{}})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	adapter, err := manager.NewExactLifecycleCoordinatorRuntimeAdapter()
	if err != nil {
		t.Fatal(err)
	}
	staged, err := manager.StageRuntimeInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DiscardRuntimeInstance(context.Background(), staged.Identity) })
	protocolSnapshot, err := starter.InspectInstance(staged.Identity)
	if err != nil || protocolSnapshot.State != extensionsruntime.ProtocolRuntimeStaged {
		t.Fatalf("staged protocol snapshot=%#v err=%v", protocolSnapshot, err)
	}
	if _, err := manager.AcquireRuntimeCall(context.Background(), staged.Identity, extensionsruntime.RuntimeCallRoute); !errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotActive) {
		t.Fatalf("staged ordinary admission error=%v", err)
	}

	request := exactCoordinatorIntegrationRequest(
		t, extension, staged.Identity, extensions.LifecycleMachineEnable,
		extensions.LifecycleMachineEnableAction, extensions.LifecycleRuntimeTarget,
		"lifecycle.enable.02.enable", false,
	)
	result, err := adapter.RunLifecycleAction(context.Background(), request, nil)
	if err != nil || result.Status != extensions.LifecycleStepSucceeded {
		t.Fatalf("staged result=%#v err=%v", result, err)
	}
	managed, err := manager.InspectRuntimeInstance(staged.Identity)
	if err != nil || managed.Active || managed.Admission.ActiveTotal != 0 {
		t.Fatalf("released staged runtime=%#v err=%v", managed, err)
	}
}

func TestManagerExactLifecycleCoordinatorRuntimeAdapterFailsClosedWithoutProtocolV2Starter(t *testing.T) {
	var nilManager *extensionsruntime.Manager
	if _, err := nilManager.NewExactLifecycleCoordinatorRuntimeAdapter(); !errors.Is(err, extensionsruntime.ErrRuntimeAdmissionInvalid) {
		t.Fatalf("nil Manager adapter error=%v", err)
	}
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{})
	if _, err := manager.NewExactLifecycleCoordinatorRuntimeAdapter(); !errors.Is(err, extensionsruntime.ErrProtocolInstanceUnsupported) {
		t.Fatalf("local starter adapter error=%v", err)
	}
}

func TestExactLifecycleCoordinatorRuntimeAdapterForceDrainCancelsStagedProcess(t *testing.T) {
	extension := exactCoordinatorIntegrationExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: p4LifecycleTrust{}})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	staged, err := manager.StageRuntimeInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	discarded := false
	t.Cleanup(func() {
		if !discarded {
			_ = manager.DiscardRuntimeInstance(context.Background(), staged.Identity)
		}
	})
	marker := filepath.Join(t.TempDir(), "lifecycle-started")
	request := exactCoordinatorIntegrationRequest(
		t, extension, staged.Identity, extensions.LifecycleMachineEnable,
		extensions.LifecycleMachineEnableAction, extensions.LifecycleRuntimeTarget,
		"lifecycle.enable.02.enable", false,
	)
	request.InputDocument, err = json.Marshal(map[string]any{"mode": "block", "startedMarker": marker})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, runErr := extensionsruntime.NewExactLifecycleCoordinatorRuntimeAdapter(manager, starter).
			RunLifecycleAction(context.Background(), request, nil)
		result <- runErr
	}()
	p4LifecycleAwaitFile(t, marker)
	if snapshot, err := manager.BeginDrain(staged.Identity); err != nil ||
		snapshot.ActiveByClass[extensionsruntime.RuntimeCallLifecycleCleanup] != 1 {
		t.Fatalf("staged drain snapshot=%#v err=%v", snapshot, err)
	}
	if err := manager.DiscardRuntimeInstance(context.Background(), staged.Identity); !errors.Is(err, extensionsruntime.ErrRuntimeInstanceBusy) {
		t.Fatalf("busy staged discard error=%v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if err := manager.WaitDrain(waitCtx, staged.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("staged wait before force=%v", err)
	}
	forceCause := errors.New("operator forced staged lifecycle cleanup")
	if snapshot, err := manager.ForceDrain(staged.Identity, forceCause); err != nil || !snapshot.Forced {
		t.Fatalf("force drain snapshot=%#v err=%v", snapshot, err)
	}
	select {
	case runErr := <-result:
		if !errors.Is(runErr, forceCause) || !errors.Is(runErr, context.Canceled) {
			t.Fatalf("forced staged result=%v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("forced staged lifecycle did not return")
	}
	if err := manager.WaitDrain(context.Background(), staged.Identity); err != nil {
		t.Fatalf("staged lease was not released: %v", err)
	}
	if err := manager.DiscardRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	discarded = true
}

func TestExactLifecycleCoordinatorRuntimeAdapterUsesManagedActiveAndRetainedProcesses(t *testing.T) {
	extension := exactCoordinatorIntegrationExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: p4LifecycleTrust{}})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	first, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	second, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity == second.Identity {
		t.Fatalf("runtime identity was reused: %#v", first.Identity)
	}
	retained, err := manager.InspectRuntimeInstance(first.Identity)
	if err != nil || retained.Active || !retained.Admission.Draining {
		t.Fatalf("retained runtime=%#v err=%v", retained, err)
	}

	adapter := extensionsruntime.NewExactLifecycleCoordinatorRuntimeAdapter(manager, starter)
	retainedRequest := exactCoordinatorIntegrationRequest(
		t, extension, first.Identity, extensions.LifecycleMachineDisable,
		extensions.LifecycleMachineDisableAction, extensions.LifecycleRuntimeSource,
		"lifecycle.disable.02.disable", false,
	)
	retainedResult, err := adapter.RunLifecycleAction(context.Background(), retainedRequest, nil)
	if err != nil || retainedResult.Status != extensions.LifecycleStepSucceeded {
		t.Fatalf("retained result=%#v err=%v", retainedResult, err)
	}

	activeRequest := exactCoordinatorIntegrationRequest(
		t, extension, second.Identity, extensions.LifecycleMachineEnable,
		extensions.LifecycleMachineEnableAction, extensions.LifecycleRuntimeTarget,
		"lifecycle.enable.02.enable", false,
	)
	activeResult, err := adapter.RunLifecycleAction(context.Background(), activeRequest, nil)
	if err != nil || activeResult.Status != extensions.LifecycleStepSucceeded {
		t.Fatalf("active result=%#v err=%v", activeResult, err)
	}
	for _, identity := range []extensionsruntime.RuntimeInstanceIdentity{first.Identity, second.Identity} {
		snapshot, inspectErr := manager.InspectRuntimeInstance(identity)
		if inspectErr != nil || snapshot.Admission.ActiveTotal != 0 {
			t.Fatalf("released runtime=%#v err=%v", snapshot, inspectErr)
		}
	}

	before := starter.ProtocolTelemetry(extension.ID).CallCount
	stale := retainedRequest
	stale.SourceBinding.RuntimeInstanceID = "missing-instance"
	if _, err := adapter.RunLifecycleAction(context.Background(), stale, nil); !errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound) {
		t.Fatalf("stale exact lifecycle error=%v", err)
	}
	if after := starter.ProtocolTelemetry(extension.ID).CallCount; after != before {
		t.Fatalf("stale identity fell back to active runtime: calls %d -> %d", before, after)
	}
}

func TestExactCoordinatorLifecycleHelperProcess(t *testing.T) {
	if os.Getenv(exactCoordinatorHelperEnv) != "1" {
		return
	}
	server := pluginv2sdk.NewServer().WithRuntimeStreams(pluginv2sdk.RuntimeStreams{
		Lifecycle: func(ctx context.Context, request *protocolwire.LifecycleRequest, progress *pluginv2sdk.ProgressStream) error {
			if request.GetContext().GetExtension().GetExtensionId() != "exact.lifecycle.fixture" ||
				request.GetPlanVersion() != "exact.lifecycle@1" ||
				!strings.HasPrefix(request.GetStepId(), "lifecycle.") || request.GetCheckpoint() != "resume-exact" {
				return &pluginv2sdk.RuntimeStreamError{
					Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
					Reason: "exact.lifecycle.request_invalid", Message: "Exact lifecycle context was not preserved.",
				}
			}
			values := request.GetInput().GetValue().AsMap()
			if values["mode"] == "block" {
				marker, _ := values["startedMarker"].(string)
				if marker == "" {
					return errors.New("started marker is required")
				}
				if err := os.WriteFile(marker, []byte("started"), 0o600); err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			}
			if err := progress.Send(&protocolwire.ProgressUpdate{
				StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
				CompletedUnits: 1, TotalUnits: 2, Checkpoint: "half",
			}); err != nil {
				return err
			}
			value, _ := structpb.NewStruct(map[string]any{"instance": request.GetContext().GetExtension().GetInstanceId()})
			return progress.Send(&protocolwire.ProgressUpdate{
				StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
				CompletedUnits: 2, TotalUnits: 2, Checkpoint: "done",
				Result: &protocolwire.TypedDocument{SchemaId: "exact.lifecycle.progress", SchemaVersion: "1", Value: value},
			})
		},
	})
	pluginv2sdk.Serve(server)
	os.Exit(0)
}

func exactCoordinatorIntegrationExtension(t *testing.T) extensions.Extension {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "exact-lifecycle")
	if err := os.MkdirAll(filepath.Join(packageRoot, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\n" + exactCoordinatorHelperEnv + "=1 exec " + shellQuote(binary) +
		" -test.run='^TestExactCoordinatorLifecycleHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(packageRoot, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	operation := func() *extensionmanifest.ManifestLifecycleOperation {
		return &extensionmanifest.ManifestLifecycleOperation{
			Plan: "lifecycle.plan", Execute: "lifecycle.execute",
			ProgressSchema: "exact.lifecycle.progress@1", CheckpointSchema: "exact.lifecycle.checkpoint@1",
		}
	}
	return extensions.Extension{
		ID: "exact.lifecycle.fixture", Version: "1.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: strings.Repeat("d", 64), PackagePath: packageRoot,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "exact.lifecycle.fixture", Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2",
			},
			Lifecycle: &extensions.ManifestLifecycle{
				ContractVersion: "exact.lifecycle@1",
				Install:         operation(), Enable: operation(), Disable: operation(), Upgrade: operation(),
				Rollback: operation(), Uninstall: operation(),
			},
		},
	}
}

func exactCoordinatorIntegrationRequest(
	t *testing.T,
	extension extensions.Extension,
	identity extensionsruntime.RuntimeInstanceIdentity,
	operation extensions.LifecycleMachineOperation,
	action extensions.LifecycleMachineAction,
	role extensions.LifecycleCoordinatorRuntimeRole,
	stepID string,
	forced bool,
) extensions.LifecycleCoordinatorActionRequest {
	t.Helper()
	binding := extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, RuntimeInstanceID: identity.InstanceID,
		VersionID: extension.ActiveVersionID,
	}
	impactDigest := "exact-impact"
	authority := extensions.LifecycleAuthoritySnapshot{
		SchemaVersion: extensions.LifecycleAuthoritySnapshotSchemaV1,
		AuthorityType: extensions.LifecycleAuthorityTrustGrant, ActorUserID: 81,
		Impact: extensions.TrustImpact{
			SchemaVersion: extensions.TrustImpactSchemaV2, Action: extensions.TrustActionEnable,
			ExtensionID: extension.ID, ExtensionVersion: extension.Version, ExtensionType: extension.Type,
			Source: extension.Source, PackageDigest: extension.PackageDigest, Digest: impactDigest,
			ArtifactDigests: map[string]string{"package": extension.PackageDigest},
		},
		Grant: &extensions.TrustGrant{
			ID: 91, ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, Action: extensions.TrustActionEnable, ImpactDigest: impactDigest,
		},
	}
	authorityJSON, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"mode": "exact", "action": action})
	if err != nil {
		t.Fatal(err)
	}
	request := extensions.LifecycleCoordinatorActionRequest{
		Extension: extension, TargetExtension: extension, TargetBinding: binding,
		OperationID: 101, Operation: operation, Action: action, RuntimeRole: role,
		StepID: stepID, PlanVersion: extension.Manifest.Lifecycle.ContractVersion,
		Attempt: 1, Checkpoint: "resume-exact", InputDocument: input,
		AuthorityType: extensions.LifecycleAuthorityTrustGrant, TrustGrantID: 91,
		AuthoritySnapshot: authorityJSON, Forced: forced, ActorUserID: 81, AuditEventID: 82,
	}
	if operation != extensions.LifecycleMachineInstall {
		request.SourceBinding = binding
	}
	if operation == extensions.LifecycleMachineUninstall {
		request.RemovalMode = extensions.LifecycleRemovalComplete
	}
	return request
}
