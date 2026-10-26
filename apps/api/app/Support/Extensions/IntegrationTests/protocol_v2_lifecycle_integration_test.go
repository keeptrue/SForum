package extensionsruntime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const p4LifecycleHelperEnv = "p4-lifecycle-runtime"

func TestProtocolV2LifecycleAdapterAcrossRealSubprocess(t *testing.T) {
	starter, extension := p4LifecycleStart(t)

	t.Run("exact invocation and successful progress", func(t *testing.T) {
		invocation := p4LifecycleInvocation(t, "success")
		callbackStates := make([]extensionsruntime.LifecycleProgressState, 0, 3)
		invocation.OnProgress = func(progress extensionsruntime.LifecycleProgress) error {
			callbackStates = append(callbackStates, progress.State)
			return nil
		}
		result, err := starter.RunLifecycle(context.Background(), extension, invocation)
		if err != nil {
			t.Fatal(err)
		}
		if result.StepID != "enable-primary" || result.State != extensionsruntime.LifecycleProgressSucceeded || result.Checkpoint != "done" ||
			result.CheckpointSchema != "p4.lifecycle.checkpoint@1" || len(result.Progress) != 3 ||
			result.Result.GetSchemaId() != "p4.lifecycle.progress" {
			t.Fatalf("successful lifecycle result = %#v", result)
		}
		wantStates := []extensionsruntime.LifecycleProgressState{
			extensionsruntime.LifecycleProgressPlanned,
			extensionsruntime.LifecycleProgressRunning,
			extensionsruntime.LifecycleProgressSucceeded,
		}
		for index, state := range wantStates {
			if result.Progress[index].State != state {
				t.Fatalf("progress %d state = %s, want %s", index, result.Progress[index].State, state)
			}
		}
		if !slices.Equal(callbackStates, wantStates) {
			t.Fatalf("callback states = %#v", callbackStates)
		}
	})

	t.Run("every lifecycle action crosses the typed transport", func(t *testing.T) {
		actions := []extensionsruntime.LifecycleAction{
			extensionsruntime.LifecycleActionInstallPlan,
			extensionsruntime.LifecycleActionInstall,
			extensionsruntime.LifecycleActionEnable,
			extensionsruntime.LifecycleActionDisable,
			extensionsruntime.LifecycleActionUpgradePlan,
			extensionsruntime.LifecycleActionUpgradeBefore,
			extensionsruntime.LifecycleActionUpgradeAfter,
			extensionsruntime.LifecycleActionRollback,
			extensionsruntime.LifecycleActionUninstallPlan,
			extensionsruntime.LifecycleActionUninstall,
			extensionsruntime.LifecycleActionUninstallAfter,
		}
		for _, action := range actions {
			t.Run(action.String(), func(t *testing.T) {
				invocation := p4LifecycleInvocation(t, "success")
				invocation.Action = action
				values := invocation.Input.GetValue().AsMap()
				values["expectedAction"] = float64(action)
				invocation.Input.Value, _ = structpb.NewStruct(values)
				result, err := starter.RunLifecycle(context.Background(), extension, invocation)
				if err != nil || result.State != extensionsruntime.LifecycleProgressSucceeded {
					t.Fatalf("action %s result=%#v err=%v", action, result, err)
				}
			})
		}
	})

	t.Run("malformed progress fails closed", func(t *testing.T) {
		for _, mode := range []string{"wrong-step", "wrong-context", "wrong-result-schema", "missing-terminal", "after-terminal", "regress", "failed-no-error", "cancel-wrong-code", "success-incomplete"} {
			t.Run(mode, func(t *testing.T) {
				_, err := starter.RunLifecycle(context.Background(), extension, p4LifecycleInvocation(t, mode))
				if !errors.Is(err, extensionsruntime.ErrInvalidLifecycleStream) {
					t.Fatalf("%s stream error = %v", mode, err)
				}
			})
		}
	})

	t.Run("typed failure preserves checkpoint and retry data", func(t *testing.T) {
		result, err := starter.RunLifecycle(context.Background(), extension, p4LifecycleInvocation(t, "typed-failure"))
		var remote *extensionsruntime.LifecycleRemoteError
		if !errors.As(err, &remote) {
			t.Fatalf("typed failure error = %#v", err)
		}
		if remote.Code != protocolwire.ErrorCode_ERROR_CODE_CONFLICT || remote.Reason != "p4.lifecycle.conflict" || !remote.Retryable ||
			remote.Metadata["resource"] != "schema" || remote.RetryAfter.IsZero() || remote.Checkpoint != "resume-7" ||
			result.State != extensionsruntime.LifecycleProgressFailed || len(result.Progress) != 1 {
			t.Fatalf("typed failure result=%#v error=%#v", result, remote)
		}
	})

	t.Run("typed cancellation unwraps context cancellation", func(t *testing.T) {
		result, err := starter.RunLifecycle(context.Background(), extension, p4LifecycleInvocation(t, "typed-cancel"))
		var remote *extensionsruntime.LifecycleRemoteError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &remote) || remote.State != extensionsruntime.LifecycleProgressCancelled ||
			result.State != extensionsruntime.LifecycleProgressCancelled {
			t.Fatalf("typed cancellation result=%#v error=%#v", result, err)
		}
	})

	t.Run("caller cancellation reaches lifecycle handler", func(t *testing.T) {
		ready := filepath.Join(t.TempDir(), "ready")
		cancelled := filepath.Join(filepath.Dir(ready), "cancelled")
		invocation := p4LifecycleInvocation(t, "wait-cancel")
		values := invocation.Input.GetValue().AsMap()
		values["readyMarker"] = ready
		values["cancelledMarker"] = cancelled
		invocation.Input.Value, _ = structpb.NewStruct(values)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := starter.RunLifecycle(ctx, extension, invocation)
			result <- err
		}()
		p4LifecycleAwaitFile(t, ready)
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("caller cancellation error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled lifecycle call did not return")
		}
		p4LifecycleAwaitFile(t, cancelled)
	})

	t.Run("invalid invocation never reaches transport", func(t *testing.T) {
		tests := []extensionsruntime.LifecycleInvocation{
			{Action: protocolwire.LifecycleAction_LIFECYCLE_ACTION_UNSPECIFIED, PlanVersion: "p4.lifecycle@1", StepID: "step"},
			{Action: extensionsruntime.LifecycleActionEnable, PlanVersion: "p4.lifecycle", StepID: "step"},
			{Action: extensionsruntime.LifecycleActionEnable, PlanVersion: "other.lifecycle@1", StepID: "step"},
			{Action: extensionsruntime.LifecycleActionEnable, PlanVersion: "p4.lifecycle@1", StepID: " step "},
			{Action: extensionsruntime.LifecycleActionEnable, PlanVersion: "p4.lifecycle@1", StepID: "step", Input: &protocolwire.TypedDocument{}},
		}
		for _, invocation := range tests {
			if _, err := starter.RunLifecycle(context.Background(), extension, invocation); !errors.Is(err, extensionsruntime.ErrInvalidLifecycleRun) {
				t.Fatalf("invalid invocation error = %v", err)
			}
		}
	})

	telemetry := starter.ProtocolTelemetry(extension.ID)
	if telemetry.ProtocolVersion != 2 || telemetry.CallCount < 12 {
		t.Fatalf("lifecycle telemetry = %#v", telemetry)
	}
}

func TestProtocolV2LifecycleUsesFrozenManifestContract(t *testing.T) {
	extension := p4LifecycleExtension(t)
	extension.Manifest.Lifecycle.Rollback = nil
	original := extension.Manifest.Lifecycle
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: p4LifecycleTrust{}})
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })

	// Mutating the caller-owned manifest after Start must not alter the runtime contract.
	original.ContractVersion = "forged.lifecycle@9"
	original.Enable.ProgressSchema = "forged.progress@9"
	original.Enable.CheckpointSchema = "forged.checkpoint@9"
	operation := &extensionmanifest.ManifestLifecycleOperation{
		Plan: "forged.rollback.plan", Execute: "forged.rollback.execute",
		ProgressSchema: "p4.lifecycle.progress@1", CheckpointSchema: "p4.lifecycle.checkpoint@1",
	}
	forgedCaller := extension
	forgedCaller.Manifest.Lifecycle = &extensions.ManifestLifecycle{ContractVersion: "p4.lifecycle@1", Rollback: operation}

	result, err := starter.RunLifecycle(context.Background(), forgedCaller, p4LifecycleInvocation(t, "success"))
	if err != nil || result.State != extensionsruntime.LifecycleProgressSucceeded || result.CheckpointSchema != "p4.lifecycle.checkpoint@1" {
		t.Fatalf("frozen declared lifecycle result=%#v err=%v", result, err)
	}
	rollback := p4LifecycleInvocation(t, "success")
	rollback.Action = extensionsruntime.LifecycleActionRollback
	if _, err := starter.RunLifecycle(context.Background(), forgedCaller, rollback); !errors.Is(err, extensionsruntime.ErrInvalidLifecycleRun) {
		t.Fatalf("forged caller authorized undeclared rollback: %v", err)
	}
}

func TestP4LifecycleHelperProcess(t *testing.T) {
	if os.Getenv("SFORUM_PLUGIN_HELPER") != p4LifecycleHelperEnv {
		return
	}
	server := &p4LifecycleServer{Server: pluginv2sdk.NewServer().WithRuntimeStreams(pluginv2sdk.RuntimeStreams{Lifecycle: p4LifecycleHandler})}
	pluginv2sdk.Serve(server)
	os.Exit(0)
}

type p4LifecycleServer struct {
	*pluginv2sdk.Server
}

func (s *p4LifecycleServer) RunLifecycle(request *protocolwire.LifecycleRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
	mode, _ := request.GetInput().GetValue().AsMap()["mode"].(string)
	if mode == "wrong-context" {
		return stream.Send(&protocolwire.ProgressUpdate{
			Context: &protocolwire.ResponseContext{
				RequestId: "forged-request", Extension: &protocolwire.ExtensionIdentity{ExtensionId: "forged.runtime"},
				ServerTime: timestamppb.New(time.Now()),
			},
			StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
		})
	}
	return s.Server.RunLifecycle(request, stream)
}

func p4LifecycleHandler(ctx context.Context, request *protocolwire.LifecycleRequest, progress *pluginv2sdk.ProgressStream) error {
	values := request.GetInput().GetValue().AsMap()
	mode, _ := values["mode"].(string)
	adapterMode := strings.HasPrefix(mode, "adapter-")
	expectedAction := protocolwire.LifecycleAction_LIFECYCLE_ACTION_ENABLE
	if raw, ok := values["expectedAction"].(float64); ok {
		expectedAction = protocolwire.LifecycleAction(int32(raw))
	}
	expectedInputSchema := "p4.lifecycle.input"
	if adapterMode {
		expectedInputSchema = "p4.lifecycle"
	}
	if request.GetContext().GetExtension().GetExtensionId() != "p4.lifecycle.fixture" ||
		request.GetContext().GetExtension().GetArtifactDigest() != strings.Repeat("d", 64) ||
		request.GetContext().GetExtension().GetTrustGrantId() != "p4-grant" ||
		request.GetAction() != expectedAction ||
		request.GetPlanVersion() != "p4.lifecycle@1" || request.GetStepId() != "enable-primary" ||
		request.GetCheckpoint() != "resume-7" || request.GetInput().GetSchemaId() != expectedInputSchema ||
		request.GetInput().GetSchemaVersion() != "1" || request.GetDryRun() == adapterMode || request.GetForced() != adapterMode {
		return &pluginv2sdk.RuntimeStreamError{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "p4.lifecycle.request_invalid",
			Message: "Lifecycle request did not preserve the exact invocation contract.",
		}
	}
	send := func(state protocolwire.ProgressState, completed, total uint32, checkpoint string) error {
		return progress.Send(&protocolwire.ProgressUpdate{
			StepId: request.GetStepId(), State: state, CompletedUnits: completed, TotalUnits: total, Checkpoint: checkpoint,
		})
	}
	switch mode {
	case "wrong-step":
		return progress.Send(&protocolwire.ProgressUpdate{StepId: "other-step", State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED})
	case "wrong-result-schema":
		result := p4LifecycleDocument(map[string]any{"applied": true})
		result.SchemaId = "p4.lifecycle.wrong"
		return progress.Send(&protocolwire.ProgressUpdate{
			StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
			CompletedUnits: 1, TotalUnits: 1, Result: result,
		})
	case "missing-terminal":
		return send(protocolwire.ProgressState_PROGRESS_STATE_RUNNING, 1, 2, "half")
	case "after-terminal":
		if err := send(protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, 1, 1, "done"); err != nil {
			return err
		}
		return send(protocolwire.ProgressState_PROGRESS_STATE_RUNNING, 1, 1, "late")
	case "regress":
		if err := send(protocolwire.ProgressState_PROGRESS_STATE_RUNNING, 2, 3, "two"); err != nil {
			return err
		}
		return send(protocolwire.ProgressState_PROGRESS_STATE_RUNNING, 1, 3, "one")
	case "failed-no-error":
		return send(protocolwire.ProgressState_PROGRESS_STATE_FAILED, 0, 0, "failed")
	case "cancel-wrong-code":
		return progress.Send(&protocolwire.ProgressUpdate{
			StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_CANCELLED,
			Error: &protocolwire.ErrorDetail{Code: protocolwire.ErrorCode_ERROR_CODE_CONFLICT, Reason: "p4.lifecycle.conflict", Message: "Not a cancellation."},
		})
	case "success-incomplete":
		return send(protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, 1, 2, "incomplete")
	case "typed-failure":
		return &pluginv2sdk.RuntimeStreamError{
			Code: protocolwire.ErrorCode_ERROR_CODE_CONFLICT, Reason: "p4.lifecycle.conflict", Message: "Schema is already owned.",
			Retryable: true, RetryAfter: time.Now().Add(time.Minute), Metadata: map[string]string{"resource": "schema"},
		}
	case "typed-cancel":
		return progress.Send(&protocolwire.ProgressUpdate{
			StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_CANCELLED,
			Error: &protocolwire.ErrorDetail{Code: protocolwire.ErrorCode_ERROR_CODE_CANCELLED, Reason: "p4.lifecycle.cancelled", Message: "Cancelled by operator."},
		})
	case "wait-cancel":
		ready, _ := values["readyMarker"].(string)
		cancelled, _ := values["cancelledMarker"].(string)
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			return err
		}
		<-ctx.Done()
		if err := os.WriteFile(cancelled, []byte(ctx.Err().Error()), 0o600); err != nil {
			return err
		}
		return ctx.Err()
	case "adapter-callback-cancel":
		if err := send(protocolwire.ProgressState_PROGRESS_STATE_PLANNED, 0, 2, request.GetCheckpoint()); err != nil {
			return err
		}
		cancelled, _ := values["cancelledMarker"].(string)
		<-ctx.Done()
		if err := os.WriteFile(cancelled, []byte(ctx.Err().Error()), 0o600); err != nil {
			return err
		}
		return ctx.Err()
	default:
		for _, update := range []*protocolwire.ProgressUpdate{
			{StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_PLANNED, TotalUnits: 3, Checkpoint: request.GetCheckpoint()},
			{StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING, CompletedUnits: 1, TotalUnits: 3, Checkpoint: "half"},
			{StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED, CompletedUnits: 3, TotalUnits: 3, Checkpoint: "done", Result: p4LifecycleDocument(map[string]any{"applied": true})},
		} {
			if err := progress.Send(update); err != nil {
				return err
			}
		}
		return nil
	}
}

type p4LifecycleTrust struct{}

func (p4LifecycleTrust) RuntimeIdentity(context.Context, extensions.Extension) (extensions.RuntimeTrustIdentity, error) {
	return extensions.RuntimeTrustIdentity{TrustGrantID: "p4-grant", ImpactDigest: "p4-impact"}, nil
}

func p4LifecycleStart(t *testing.T) (*extensionsruntime.ProtocolStarter, extensions.Extension) {
	t.Helper()
	extension := p4LifecycleExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: p4LifecycleTrust{}})
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatalf("start lifecycle v2 helper: %v", err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	return starter, extension
}

func p4LifecycleExtension(t *testing.T) extensions.Extension {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "p4-lifecycle")
	if err := os.MkdirAll(filepath.Join(packageRoot, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\n" +
		"SFORUM_PLUGIN_HELPER=" + p4LifecycleShellQuote(p4LifecycleHelperEnv) + " " +
		"exec " + p4LifecycleShellQuote(testBinary) + " -test.run='^TestP4LifecycleHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(packageRoot, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	operation := func() *extensionmanifest.ManifestLifecycleOperation {
		return &extensionmanifest.ManifestLifecycleOperation{
			Plan: "lifecycle.operation.plan", Execute: "lifecycle.operation.execute",
			ProgressSchema: "p4.lifecycle.progress@1", CheckpointSchema: "p4.lifecycle.checkpoint@1",
		}
	}
	lifecycle := &extensions.ManifestLifecycle{
		ContractVersion: "p4.lifecycle@1",
		Install:         operation(), Enable: operation(), Disable: operation(), Upgrade: operation(), Rollback: operation(), Uninstall: operation(),
	}
	return extensions.Extension{
		ID: "p4.lifecycle.fixture", Name: "P4 Lifecycle Fixture", Version: "1.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded, PackageDigest: strings.Repeat("d", 64), PackagePath: packageRoot,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "p4.lifecycle.fixture", Name: "P4 Lifecycle Fixture", Version: "1.0.0", Type: extensions.TypePlugin,
			Backend:   extensions.ManifestBackend{Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2"},
			Lifecycle: lifecycle,
		},
	}
}

func p4LifecycleInvocation(t *testing.T, mode string) extensionsruntime.LifecycleInvocation {
	t.Helper()
	value, err := structpb.NewStruct(map[string]any{"mode": mode})
	if err != nil {
		t.Fatal(err)
	}
	return extensionsruntime.LifecycleInvocation{
		Action: extensionsruntime.LifecycleActionEnable, PlanVersion: "p4.lifecycle@1", StepID: "enable-primary",
		Checkpoint: "resume-7", Input: &protocolwire.TypedDocument{SchemaId: "p4.lifecycle.input", SchemaVersion: "1", Value: value}, DryRun: true,
	}
}

func p4LifecycleDocument(values map[string]any) *protocolwire.TypedDocument {
	value, _ := structpb.NewStruct(values)
	return &protocolwire.TypedDocument{SchemaId: "p4.lifecycle.progress", SchemaVersion: "1", Value: value}
}

func p4LifecycleAwaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("lifecycle marker %s was not written", filepath.Base(path))
}

func p4LifecycleShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
