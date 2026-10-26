package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

type exactLifecycleCoordinatorRunnerFunc func(
	context.Context,
	RuntimeInstanceIdentity,
	extensions.Extension,
	LifecycleInvocation,
) (LifecycleRunResult, error)

func (f exactLifecycleCoordinatorRunnerFunc) RunLifecycleInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	invocation LifecycleInvocation,
) (LifecycleRunResult, error) {
	return f(ctx, identity, extension, invocation)
}

type exactLifecycleCoordinatorAdmissionFunc func(
	context.Context,
	RuntimeInstanceIdentity,
	RuntimeCallClass,
) (*RuntimeAdmissionLease, error)

func (f exactLifecycleCoordinatorAdmissionFunc) AcquireRuntimeCall(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	class RuntimeCallClass,
) (*RuntimeAdmissionLease, error) {
	return f(ctx, identity, class)
}

func TestExactLifecycleCoordinatorRuntimeAdapterSelectsEveryActionRole(t *testing.T) {
	tests := []struct {
		operation extensions.LifecycleMachineOperation
		action    extensions.LifecycleMachineAction
		position  int
		role      extensions.LifecycleCoordinatorRuntimeRole
	}{
		{extensions.LifecycleMachineInstall, extensions.LifecycleMachineInstallPlan, 1, extensions.LifecycleRuntimeTarget},
		{extensions.LifecycleMachineInstall, extensions.LifecycleMachineInstallAction, 3, extensions.LifecycleRuntimeTarget},
		{extensions.LifecycleMachineInstall, extensions.LifecycleMachineEnableAction, 5, extensions.LifecycleRuntimeTarget},
		{extensions.LifecycleMachineEnable, extensions.LifecycleMachineEnableAction, 2, extensions.LifecycleRuntimeTarget},
		{extensions.LifecycleMachineDisable, extensions.LifecycleMachineDisableAction, 2, extensions.LifecycleRuntimeSource},
		{extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineUpgradePlan, 1, extensions.LifecycleRuntimeTarget},
		{extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineUpgradeBefore, 3, extensions.LifecycleRuntimeSource},
		{extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineUpgradeAfter, 8, extensions.LifecycleRuntimeTarget},
		{extensions.LifecycleMachineRollback, extensions.LifecycleMachineRollbackAction, 3, extensions.LifecycleRuntimeTarget},
		{extensions.LifecycleMachineUninstall, extensions.LifecycleMachineUninstallPlan, 1, extensions.LifecycleRuntimeSource},
		{extensions.LifecycleMachineUninstall, extensions.LifecycleMachineUninstallStep, 4, extensions.LifecycleRuntimeSource},
		{extensions.LifecycleMachineUninstall, extensions.LifecycleMachineUninstallAfter, 5, extensions.LifecycleRuntimeSource},
	}
	for _, test := range tests {
		t.Run(string(test.operation)+"/"+string(test.action), func(t *testing.T) {
			request := exactCoordinatorTestRequest(t, test.operation, test.action, test.position, test.role)
			calls := 0
			adapter := &ExactLifecycleCoordinatorRuntimeAdapter{
				admission: exactCoordinatorTestAdmission(t),
				runner: exactLifecycleCoordinatorRunnerFunc(func(
					_ context.Context,
					identity RuntimeInstanceIdentity,
					extension extensions.Extension,
					invocation LifecycleInvocation,
				) (LifecycleRunResult, error) {
					calls++
					expectedBinding := request.TargetBinding
					expectedExtension := request.TargetExtension
					expectedPlan := request.PlanVersion
					if test.role == extensions.LifecycleRuntimeSource {
						expectedBinding = request.SourceBinding
						expectedExtension = request.Extension
						expectedPlan = expectedExtension.Manifest.Lifecycle.ContractVersion
					}
					if identity != (RuntimeInstanceIdentity{ExtensionID: expectedBinding.ExtensionID, InstanceID: expectedBinding.RuntimeInstanceID}) ||
						extension.ID != expectedExtension.ID || extension.Version != expectedExtension.Version ||
						extension.PackageDigest != expectedExtension.PackageDigest || invocation.PlanVersion != expectedPlan ||
						invocation.StepID != request.StepID || invocation.Checkpoint != request.Checkpoint || invocation.Forced != request.Forced {
						t.Fatalf("identity=%#v extension=%#v invocation=%#v", identity, extension, invocation)
					}
					return LifecycleRunResult{State: LifecycleProgressSucceeded, Checkpoint: request.Checkpoint}, nil
				})}
			result, err := adapter.RunLifecycleAction(context.Background(), request, nil)
			if err != nil || result.Status != extensions.LifecycleStepSucceeded || calls != 1 {
				t.Fatalf("result=%#v calls=%d err=%v", result, calls, err)
			}
		})
	}
}

func TestExactLifecycleCoordinatorRuntimeAdapterMapsCrossVersionSourceProgress(t *testing.T) {
	request := exactCoordinatorTestRequest(
		t, extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineUpgradeBefore, 3, extensions.LifecycleRuntimeSource,
	)
	request.Checkpoint = " opaque source checkpoint \n"
	request.InputDocument = json.RawMessage(`{"mode":"prepare"}`)
	resultValue, err := structpb.NewStruct(map[string]any{"prepared": true})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &ExactLifecycleCoordinatorRuntimeAdapter{
		admission: exactCoordinatorTestAdmission(t),
		runner: exactLifecycleCoordinatorRunnerFunc(func(
			_ context.Context,
			identity RuntimeInstanceIdentity,
			extension extensions.Extension,
			invocation LifecycleInvocation,
		) (LifecycleRunResult, error) {
			if identity.InstanceID != "source-instance" || extension.Version != "1.0.0" ||
				invocation.PlanVersion != "demo.source.lifecycle@1" || request.PlanVersion != "demo.target.lifecycle@2" ||
				invocation.Input.GetSchemaId() != "demo.source.lifecycle" || invocation.Input.GetSchemaVersion() != "1" ||
				invocation.Input.GetValue().AsMap()["mode"] != "prepare" || invocation.Checkpoint != request.Checkpoint {
				t.Fatalf("identity=%#v extension=%#v invocation=%#v", identity, extension, invocation)
			}
			updates := []LifecycleProgress{
				{State: LifecycleProgressPlanned, TotalUnits: 2, Checkpoint: request.Checkpoint, Message: "planned"},
				{State: LifecycleProgressRunning, CompletedUnits: 1, TotalUnits: 2, Checkpoint: "half", Message: "running"},
				{State: LifecycleProgressSucceeded, CompletedUnits: 2, TotalUnits: 2, Checkpoint: "done", Message: "complete"},
			}
			for _, update := range updates {
				if err := invocation.OnProgress(update); err != nil {
					return LifecycleRunResult{}, err
				}
			}
			return LifecycleRunResult{
				State: LifecycleProgressSucceeded, Checkpoint: "done",
				Result: &protocolv2.TypedDocument{SchemaId: "demo.result", SchemaVersion: "1", Value: resultValue},
			}, nil
		})}
	progress := make([]string, 0, 2)
	result, err := adapter.RunLifecycleAction(context.Background(), request, func(update extensions.LifecycleCoordinatorActionProgress) error {
		progress = append(progress, update.Status+":"+update.Checkpoint)
		return nil
	})
	if err != nil || result.Status != extensions.LifecycleStepSucceeded || result.Checkpoint != "done" ||
		result.CompletedUnits != 2 || result.TotalUnits != 2 || result.Message != "complete" ||
		!slices.Equal(progress, []string{"planned:" + request.Checkpoint, "running:half"}) {
		t.Fatalf("result=%#v progress=%#v err=%v", result, progress, err)
	}
	var values map[string]any
	if json.Unmarshal(result.ResultDocument, &values) != nil || values["prepared"] != true {
		t.Fatalf("result document=%s", result.ResultDocument)
	}
}

func TestExactLifecycleCoordinatorRuntimeAdapterRejectsContextDriftBeforeDispatch(t *testing.T) {
	mutateAuthority := func(t *testing.T, request *extensions.LifecycleCoordinatorActionRequest, mutate func(*extensions.LifecycleAuthoritySnapshot)) {
		t.Helper()
		var authority extensions.LifecycleAuthoritySnapshot
		if err := json.Unmarshal(request.AuthoritySnapshot, &authority); err != nil {
			t.Fatal(err)
		}
		mutate(&authority)
		request.AuthoritySnapshot, _ = json.Marshal(authority)
	}
	tests := []struct {
		name   string
		mutate func(*testing.T, *extensions.LifecycleCoordinatorActionRequest)
	}{
		{"runtime role", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.RuntimeRole = extensions.LifecycleRuntimeTarget
		}},
		{"source extension id", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.SourceBinding.ExtensionID = "other.plugin"
		}},
		{"source extension version", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.SourceBinding.ExtensionVersion = "0.9.0"
		}},
		{"source digest", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.SourceBinding.PackageDigest = strings.Repeat("f", 64)
		}},
		{"source instance", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.SourceBinding.RuntimeInstanceID = ""
		}},
		{"target instance", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.TargetBinding.RuntimeInstanceID = ""
		}},
		{"source version id", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.SourceBinding.VersionID++ }},
		{"selected artifact", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.Extension = r.TargetExtension }},
		{"selected provenance", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.Extension.Source = extensions.SourceBuiltin
		}},
		{"selected manifest", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.Extension.Manifest.Name = "drifted"
		}},
		{"target digest", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.TargetBinding.PackageDigest = strings.Repeat("e", 64)
		}},
		{"operation action", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.Operation = extensions.LifecycleMachineEnable
		}},
		{"stable step", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.StepID = "lifecycle.upgrade.99.upgrade.before"
		}},
		{"operation id", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.OperationID = 0 }},
		{"attempt", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.Attempt = 0 }},
		{"actor", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.ActorUserID = 0 }},
		{"audit", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.AuditEventID = 0 }},
		{"forced", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.Forced = true }},
		{"removal mode", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.RemovalMode = extensions.LifecycleRemovalPreserve
		}},
		{"plan", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.PlanVersion = "invalid" }},
		{"source missing", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.SourceExtension = nil }},
		{"authority json", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			r.AuthoritySnapshot = json.RawMessage(`{"broken"`)
		}},
		{"authority actor", func(t *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			mutateAuthority(t, r, func(a *extensions.LifecycleAuthoritySnapshot) { a.ActorUserID++ })
		}},
		{"authority target", func(t *testing.T, r *extensions.LifecycleCoordinatorActionRequest) {
			mutateAuthority(t, r, func(a *extensions.LifecycleAuthoritySnapshot) { a.Impact.PackageDigest = strings.Repeat("c", 64) })
		}},
		{"trust grant", func(_ *testing.T, r *extensions.LifecycleCoordinatorActionRequest) { r.TrustGrantID++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := exactCoordinatorTestRequest(
				t, extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineUpgradeBefore, 3, extensions.LifecycleRuntimeSource,
			)
			test.mutate(t, &request)
			calls := 0
			adapter := &ExactLifecycleCoordinatorRuntimeAdapter{
				admission: exactCoordinatorTestAdmission(t),
				runner: exactLifecycleCoordinatorRunnerFunc(func(
					context.Context, RuntimeInstanceIdentity, extensions.Extension, LifecycleInvocation,
				) (LifecycleRunResult, error) {
					calls++
					return LifecycleRunResult{}, nil
				})}
			if _, err := adapter.RunLifecycleAction(context.Background(), request, nil); !errors.Is(err, ErrInvalidLifecycleRun) {
				t.Fatalf("error=%v", err)
			}
			if calls != 0 {
				t.Fatalf("runner calls=%d", calls)
			}
		})
	}
}

func TestExactLifecycleCoordinatorAuthorityKeepsRecoveryActorSeparate(t *testing.T) {
	request := exactCoordinatorTestRequest(
		t, extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineUpgradeBefore, 3, extensions.LifecycleRuntimeSource,
	)
	request.ActorUserID = 62
	request.AuditEventID = 72
	if _, err := validateExactCoordinatorRequest(request); err != nil {
		t.Fatalf("recovery actor rejected exact authority: %v", err)
	}
	request.AuthorityActorUserID++
	if _, err := validateExactCoordinatorRequest(request); !errors.Is(err, ErrInvalidLifecycleRun) {
		t.Fatalf("drifted authority actor = %v", err)
	}
}

func TestExactLifecycleCoordinatorRuntimeAdapterValidatesTargetPlanAndUninstallContext(t *testing.T) {
	targetRequest := exactCoordinatorTestRequest(
		t, extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineUpgradePlan, 1, extensions.LifecycleRuntimeTarget,
	)
	targetRequest.PlanVersion = "other.target.lifecycle@2"
	adapter := &ExactLifecycleCoordinatorRuntimeAdapter{
		admission: exactCoordinatorTestAdmission(t),
		runner: exactLifecycleCoordinatorRunnerFunc(func(
			context.Context, RuntimeInstanceIdentity, extensions.Extension, LifecycleInvocation,
		) (LifecycleRunResult, error) {
			t.Fatal("target plan drift reached runtime")
			return LifecycleRunResult{}, nil
		})}
	if _, err := adapter.RunLifecycleAction(context.Background(), targetRequest, nil); !errors.Is(err, ErrInvalidLifecycleRun) {
		t.Fatalf("target plan error=%v", err)
	}

	uninstall := exactCoordinatorTestRequest(
		t, extensions.LifecycleMachineUninstall, extensions.LifecycleMachineUninstallStep, 4, extensions.LifecycleRuntimeSource,
	)
	uninstall.Forced = true
	uninstall.RemovalMode = extensions.LifecycleRemovalComplete
	called := false
	adapter.runner = exactLifecycleCoordinatorRunnerFunc(func(
		_ context.Context,
		_ RuntimeInstanceIdentity,
		_ extensions.Extension,
		invocation LifecycleInvocation,
	) (LifecycleRunResult, error) {
		called = true
		if !invocation.Forced {
			t.Fatal("forced uninstall context was lost")
		}
		return LifecycleRunResult{State: LifecycleProgressSucceeded}, nil
	})
	if _, err := adapter.RunLifecycleAction(context.Background(), uninstall, nil); err != nil || !called {
		t.Fatalf("forced uninstall called=%t err=%v", called, err)
	}
	uninstall.RemovalMode = ""
	if _, err := adapter.RunLifecycleAction(context.Background(), uninstall, nil); !errors.Is(err, ErrInvalidLifecycleRun) {
		t.Fatalf("missing removal mode error=%v", err)
	}
}

func TestExactLifecycleCoordinatorRuntimeAdapterMapsTypedCancellation(t *testing.T) {
	request := exactCoordinatorTestRequest(
		t, extensions.LifecycleMachineDisable, extensions.LifecycleMachineDisableAction, 2, extensions.LifecycleRuntimeSource,
	)
	remote := &LifecycleRemoteError{
		StepID: request.StepID, State: LifecycleProgressCancelled,
		Code: protocolv2.ErrorCode_ERROR_CODE_CANCELLED, Reason: "plugin.cancelled", Message: "Cancelled by operator.",
		Retryable: true, RetryAfter: time.Now().Add(time.Minute).UTC(), Metadata: map[string]string{"actor": "operator"},
	}
	adapter := &ExactLifecycleCoordinatorRuntimeAdapter{
		admission: exactCoordinatorTestAdmission(t),
		runner: exactLifecycleCoordinatorRunnerFunc(func(
			_ context.Context,
			_ RuntimeInstanceIdentity,
			_ extensions.Extension,
			invocation LifecycleInvocation,
		) (LifecycleRunResult, error) {
			if err := invocation.OnProgress(LifecycleProgress{
				State: LifecycleProgressCancelled, Checkpoint: "cancelled", CompletedUnits: 1, TotalUnits: 2,
			}); err != nil {
				return LifecycleRunResult{}, err
			}
			return LifecycleRunResult{State: LifecycleProgressCancelled, Checkpoint: "cancelled"}, remote
		})}
	result, err := adapter.RunLifecycleAction(context.Background(), request, nil)
	if !errors.Is(err, context.Canceled) || result.Status != extensions.LifecycleStepCancelled ||
		result.Error.Reason != remote.Reason || !result.Error.Retryable || result.Error.RetryAfter == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestExactLifecycleCoordinatorRuntimeAdapterLeaseCoversStreamAndForceDrain(t *testing.T) {
	request := exactCoordinatorTestRequest(
		t, extensions.LifecycleMachineDisable, extensions.LifecycleMachineDisableAction, 2, extensions.LifecycleRuntimeSource,
	)
	identity := RuntimeInstanceIdentity{ExtensionID: request.SourceBinding.ExtensionID, InstanceID: request.SourceBinding.RuntimeInstanceID}
	gate, err := NewRuntimeAdmissionGate(identity)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	runnerDone := make(chan struct{})
	forceCause := errors.New("forced retained runtime cleanup")
	adapter := &ExactLifecycleCoordinatorRuntimeAdapter{
		admission: exactLifecycleCoordinatorAdmissionFunc(func(
			ctx context.Context,
			actual RuntimeInstanceIdentity,
			class RuntimeCallClass,
		) (*RuntimeAdmissionLease, error) {
			if actual != identity || class != RuntimeCallLifecycleCleanup {
				t.Fatalf("admission identity=%#v class=%q", actual, class)
			}
			return gate.Acquire(ctx, class)
		}),
		runner: exactLifecycleCoordinatorRunnerFunc(func(
			ctx context.Context,
			actual RuntimeInstanceIdentity,
			_ extensions.Extension,
			_ LifecycleInvocation,
		) (LifecycleRunResult, error) {
			if actual != identity {
				t.Fatalf("runner identity=%#v", actual)
			}
			close(acquired)
			<-ctx.Done()
			if !errors.Is(context.Cause(ctx), forceCause) {
				t.Errorf("lease cancellation cause=%v", context.Cause(ctx))
			}
			close(runnerDone)
			return LifecycleRunResult{}, ctx.Err()
		}),
	}
	result := make(chan error, 1)
	go func() {
		_, runErr := adapter.RunLifecycleAction(context.Background(), request, nil)
		result <- runErr
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("lifecycle stream did not acquire admission")
	}
	if snapshot := gate.BeginDrain(); !snapshot.Draining || snapshot.ActiveByClass[RuntimeCallLifecycleCleanup] != 1 {
		t.Fatalf("draining cleanup snapshot=%#v", snapshot)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if err := gate.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait before release=%v", err)
	}
	if snapshot := gate.ForceCancel(forceCause); !snapshot.Forced || snapshot.ActiveByClass[RuntimeCallLifecycleCleanup] != 1 {
		t.Fatalf("forced cleanup snapshot=%#v", snapshot)
	}
	select {
	case <-runnerDone:
	case <-time.After(time.Second):
		t.Fatal("force drain did not cancel lifecycle stream")
	}
	select {
	case runErr := <-result:
		if !errors.Is(runErr, forceCause) || !errors.Is(runErr, context.Canceled) {
			t.Fatalf("force drain result=%v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle adapter did not return after force drain")
	}
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatalf("lease was not released: %v", err)
	}
	if snapshot := gate.Snapshot(); snapshot.ActiveTotal != 0 {
		t.Fatalf("released snapshot=%#v", snapshot)
	}
}

func TestExactLifecycleCoordinatorRuntimeAdapterRequiresManagerAdmission(t *testing.T) {
	request := exactCoordinatorTestRequest(
		t, extensions.LifecycleMachineEnable, extensions.LifecycleMachineEnableAction, 2, extensions.LifecycleRuntimeTarget,
	)
	runner := exactLifecycleCoordinatorRunnerFunc(func(
		context.Context, RuntimeInstanceIdentity, extensions.Extension, LifecycleInvocation,
	) (LifecycleRunResult, error) {
		t.Fatal("missing admission reached runtime")
		return LifecycleRunResult{}, nil
	})
	if _, err := (&ExactLifecycleCoordinatorRuntimeAdapter{runner: runner}).RunLifecycleAction(context.Background(), request, nil); !errors.Is(err, extensions.ErrRuntimeUnavailable) {
		t.Fatalf("missing admission error=%v", err)
	}
	denied := errors.New("exact instance is not managed")
	adapter := &ExactLifecycleCoordinatorRuntimeAdapter{
		admission: exactLifecycleCoordinatorAdmissionFunc(func(
			context.Context, RuntimeInstanceIdentity, RuntimeCallClass,
		) (*RuntimeAdmissionLease, error) {
			return nil, denied
		}),
		runner: runner,
	}
	if _, err := adapter.RunLifecycleAction(context.Background(), request, nil); !errors.Is(err, denied) {
		t.Fatalf("denied admission error=%v", err)
	}
}

func TestExactLifecycleCoordinatorRuntimeAdapterUsesManagerForActiveAndRetainedInstances(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "demo.lifecycle")
	tests := []struct {
		name       string
		request    extensions.LifecycleCoordinatorActionRequest
		instanceID string
	}{
		{
			name: "active",
			request: exactCoordinatorTestRequest(
				t, extensions.LifecycleMachineEnable, extensions.LifecycleMachineEnableAction, 2, extensions.LifecycleRuntimeTarget,
			),
			instanceID: "instance-2",
		},
		{
			name: "retained draining",
			request: exactCoordinatorTestRequest(
				t, extensions.LifecycleMachineDisable, extensions.LifecycleMachineDisableAction, 2, extensions.LifecycleRuntimeSource,
			),
			instanceID: "instance-1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := test.request
			if request.RuntimeRole == extensions.LifecycleRuntimeSource {
				request.SourceBinding.RuntimeInstanceID = test.instanceID
			} else {
				request.TargetBinding.RuntimeInstanceID = test.instanceID
			}
			adapter := &ExactLifecycleCoordinatorRuntimeAdapter{
				admission: manager,
				runner: exactLifecycleCoordinatorRunnerFunc(func(
					_ context.Context,
					identity RuntimeInstanceIdentity,
					_ extensions.Extension,
					_ LifecycleInvocation,
				) (LifecycleRunResult, error) {
					if identity.InstanceID != test.instanceID {
						t.Fatalf("runner identity=%#v", identity)
					}
					return LifecycleRunResult{State: LifecycleProgressSucceeded}, nil
				}),
			}
			if _, err := adapter.RunLifecycleAction(context.Background(), request, nil); err != nil {
				t.Fatal(err)
			}
			snapshot, err := manager.InspectRuntimeInstance(RuntimeInstanceIdentity{ExtensionID: "demo.lifecycle", InstanceID: test.instanceID})
			if err != nil || snapshot.Admission.ActiveTotal != 0 {
				t.Fatalf("released manager snapshot=%#v err=%v", snapshot, err)
			}
		})
	}
}

func exactCoordinatorTestRequest(
	t *testing.T,
	operation extensions.LifecycleMachineOperation,
	action extensions.LifecycleMachineAction,
	position int,
	role extensions.LifecycleCoordinatorRuntimeRole,
) extensions.LifecycleCoordinatorActionRequest {
	t.Helper()
	source := exactCoordinatorTestExtension("demo.lifecycle", "1.0.0", strings.Repeat("a", 64), "demo.source.lifecycle@1", 11)
	target := exactCoordinatorTestExtension("demo.lifecycle", "2.0.0", strings.Repeat("b", 64), "demo.target.lifecycle@2", 12)
	request := extensions.LifecycleCoordinatorActionRequest{
		TargetExtension: target,
		TargetBinding: extensions.LifecycleRuntimeBinding{
			ExtensionID: target.ID, ExtensionVersion: target.Version, PackageDigest: target.PackageDigest,
			RuntimeInstanceID: "target-instance", VersionID: target.ActiveVersionID,
		},
		OperationID: 41, Operation: operation, Action: action,
		StepID:      fmt.Sprintf("lifecycle.%s.%02d.%s", operation, position, action),
		PlanVersion: target.Manifest.Lifecycle.ContractVersion, Attempt: 2, Checkpoint: "resume-1",
		RuntimeRole: role, AuthorityType: extensions.LifecycleAuthorityTrustGrant, TrustGrantID: 51,
		AuthorityActorUserID: 61, ActorUserID: 61, AuditEventID: 71,
	}
	switch operation {
	case extensions.LifecycleMachineInstall:
		// Install has no source artifact.
	case extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineRollback:
		request.SourceExtension = &source
		request.SourceBinding = extensions.LifecycleRuntimeBinding{
			ExtensionID: source.ID, ExtensionVersion: source.Version, PackageDigest: source.PackageDigest,
			RuntimeInstanceID: "source-instance", VersionID: source.ActiveVersionID,
		}
	default:
		// Enable/disable/uninstall operate on the current target artifact.
		request.TargetExtension = target
		request.SourceBinding = request.TargetBinding
	}
	if role == extensions.LifecycleRuntimeSource {
		if request.SourceExtension != nil {
			request.Extension = *request.SourceExtension
		} else {
			request.Extension = request.TargetExtension
		}
	} else {
		request.Extension = request.TargetExtension
	}
	if operation == extensions.LifecycleMachineUninstall {
		request.RemovalMode = extensions.LifecycleRemovalPreserve
	}
	request.AuthoritySnapshot = exactCoordinatorTestAuthority(t, request.TargetExtension, request.AuthorityActorUserID, request.TrustGrantID)
	return request
}

func exactCoordinatorTestExtension(id, version, digest, contract string, versionID int64) extensions.Extension {
	operation := func() *extensionmanifest.ManifestLifecycleOperation {
		return &extensionmanifest.ManifestLifecycleOperation{
			Plan: "lifecycle.plan", Execute: "lifecycle.execute",
			ProgressSchema: "demo.progress@1", CheckpointSchema: "demo.checkpoint@1",
		}
	}
	return extensions.Extension{
		ID: id, Version: version, Type: extensions.TypePlugin, Source: extensions.SourceUploaded,
		PackageDigest: digest, ActiveVersionID: versionID,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Name: "Lifecycle fixture",
			Description: "Exact lifecycle runtime fixture.", URL: "https://example.test/lifecycle-fixture",
			Author: extensions.ManifestAuthor{Name: "SForum"}, Version: version,
			Type: extensions.TypePlugin, SForumVersion: ">=0.1.0",
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
				Digest: digest, HostAPIVersion: "sforum.host@2",
			},
			PackageFiles: []extensions.ManifestPackageFile{{
				ID: id + ".file.backend", Kind: "executable", Path: "backend/plugin", Digest: digest,
			}},
			Lifecycle: &extensions.ManifestLifecycle{
				ContractVersion: contract,
				Install:         operation(), Enable: operation(), Disable: operation(), Upgrade: operation(),
				Rollback: operation(), Uninstall: operation(),
			},
		},
	}
}

func TestExactCoordinatorFixtureUsesValidManifest(t *testing.T) {
	fixture := exactCoordinatorTestExtension(
		"fixture.lifecycle", "1.0.0", strings.Repeat("a", 64), "fixture.lifecycle@1", 1,
	)
	if err := extensionmanifest.Validate(fixture.Manifest); err != nil {
		t.Fatalf("exact lifecycle fixture manifest: %v", err)
	}
}

func exactCoordinatorTestAuthority(t *testing.T, target extensions.Extension, actorID, trustGrantID int64) json.RawMessage {
	t.Helper()
	impactDigest := "impact-" + target.Version
	authority := extensions.LifecycleAuthoritySnapshot{
		SchemaVersion: extensions.LifecycleAuthoritySnapshotSchemaV1,
		AuthorityType: extensions.LifecycleAuthorityTrustGrant,
		ActorUserID:   actorID,
		Impact: extensions.TrustImpact{
			SchemaVersion: extensions.TrustImpactSchemaV2, Action: extensions.TrustActionEnable,
			ExtensionID: target.ID, ExtensionVersion: target.Version, ExtensionType: target.Type,
			Source: target.Source, PackageDigest: target.PackageDigest, Digest: impactDigest,
			ArtifactDigests: map[string]string{"package": target.PackageDigest},
		},
		Grant: &extensions.TrustGrant{
			ID: trustGrantID, ExtensionID: target.ID, ExtensionVersion: target.Version,
			PackageDigest: target.PackageDigest, Action: extensions.TrustActionEnable, ImpactDigest: impactDigest,
		},
	}
	value, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func exactCoordinatorTestAdmission(t *testing.T) LifecycleCoordinatorRuntimeAdmission {
	t.Helper()
	return exactLifecycleCoordinatorAdmissionFunc(func(
		ctx context.Context,
		identity RuntimeInstanceIdentity,
		class RuntimeCallClass,
	) (*RuntimeAdmissionLease, error) {
		if class != RuntimeCallLifecycleCleanup {
			t.Fatalf("admission class=%q", class)
		}
		gate, err := NewRuntimeAdmissionGate(identity)
		if err != nil {
			return nil, err
		}
		return gate.Acquire(ctx, class)
	})
}
