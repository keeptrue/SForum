package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
)

func TestExactLifecycleCoordinatorHostDispatchesEveryAuthoritativeGate(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		kind      lifecycleHostGateKind
	}{
		{"install prepare", extensions.LifecycleMachineInstall, 0, lifecycleHostGatePrepare},
		{"install migrate", extensions.LifecycleMachineInstall, 2, lifecycleHostGateBoundary},
		{"install starting", extensions.LifecycleMachineInstall, 4, lifecycleHostGateStarting},
		{"install healthy", extensions.LifecycleMachineInstall, 6, lifecycleHostGateHealthy},
		{"install registering", extensions.LifecycleMachineInstall, 7, lifecycleHostGateBoundary},
		{"install enabled", extensions.LifecycleMachineInstall, 8, lifecycleHostGateBoundary},
		{"enable prepare", extensions.LifecycleMachineEnable, 0, lifecycleHostGatePrepare},
		{"enable starting", extensions.LifecycleMachineEnable, 1, lifecycleHostGateStarting},
		{"enable healthy", extensions.LifecycleMachineEnable, 3, lifecycleHostGateHealthy},
		{"enable registering", extensions.LifecycleMachineEnable, 4, lifecycleHostGateBoundary},
		{"enable enabled", extensions.LifecycleMachineEnable, 5, lifecycleHostGateBoundary},
		{"disable prepare", extensions.LifecycleMachineDisable, 0, lifecycleHostGatePrepare},
		{"disable drain", extensions.LifecycleMachineDisable, 1, lifecycleHostGateDrain},
		{"disable terminal", extensions.LifecycleMachineDisable, 3, lifecycleHostGateBoundary},
		{"upgrade prepare", extensions.LifecycleMachineUpgrade, 0, lifecycleHostGatePrepare},
		{"upgrade drain", extensions.LifecycleMachineUpgrade, 2, lifecycleHostGateDrain},
		{"upgrade migrate", extensions.LifecycleMachineUpgrade, 4, lifecycleHostGateBoundary},
		{"upgrade starting", extensions.LifecycleMachineUpgrade, 5, lifecycleHostGateStarting},
		{"upgrade healthy", extensions.LifecycleMachineUpgrade, 6, lifecycleHostGateHealthy},
		{"upgrade registering", extensions.LifecycleMachineUpgrade, 7, lifecycleHostGateBoundary},
		{"upgrade publish", extensions.LifecycleMachineUpgrade, 8, lifecycleHostGateBoundary},
		{"upgrade terminal", extensions.LifecycleMachineUpgrade, 10, lifecycleHostGateBoundary},
		{"rollback prepare", extensions.LifecycleMachineRollback, 0, lifecycleHostGatePrepare},
		{"rollback drain", extensions.LifecycleMachineRollback, 1, lifecycleHostGateDrain},
		{"rollback starting", extensions.LifecycleMachineRollback, 2, lifecycleHostGateStarting},
		{"rollback healthy", extensions.LifecycleMachineRollback, 4, lifecycleHostGateHealthy},
		{"rollback registering", extensions.LifecycleMachineRollback, 5, lifecycleHostGateBoundary},
		{"rollback enabled", extensions.LifecycleMachineRollback, 6, lifecycleHostGateBoundary},
		{"uninstall prepare", extensions.LifecycleMachineUninstall, 0, lifecycleHostGatePrepare},
		{"uninstall drain", extensions.LifecycleMachineUninstall, 2, lifecycleHostGateDrain},
		{"uninstall cleanup", extensions.LifecycleMachineUninstall, 3, lifecycleHostGateBoundary},
		{"uninstall terminal", extensions.LifecycleMachineUninstall, 6, lifecycleHostGateBoundary},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := lifecycleHostTestRequest(t, test.operation, test.position)
			runtime := newLifecycleHostRuntimeTestDouble()
			seedLifecycleHostExactInstances(runtime, request)
			boundary := &lifecycleHostBoundaryTestDouble{result: LifecycleHostBoundaryResult{
				Checkpoint: "boundary-checkpoint", ResultDocument: json.RawMessage(`{"boundary":true}`),
			}}
			ctx := context.WithValue(context.Background(), lifecycleHostContextKey{}, test.name)

			result, err := NewExactLifecycleCoordinatorHost(runtime, boundary).RunLifecycleHostGate(ctx, request)
			if err != nil {
				t.Fatalf("RunLifecycleHostGate() error = %v", err)
			}
			if got := lookupLifecycleHostGateKind(test.operation, test.position); got != test.kind {
				t.Fatalf("dispatcher kind = %d, want %d", got, test.kind)
			}
			assertLifecycleHostDispatch(t, runtime, boundary, request, result, test.kind, ctx)
		})
	}
}

func TestExactLifecycleCoordinatorHostRevalidatesAndRecreatesExactRuntime(t *testing.T) {
	t.Run("reuses persisted exact instance", func(t *testing.T) {
		request := lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0)
		request.Revalidation = true
		request.PreviousResult = json.RawMessage(`{"schema":"durable-attempt"}`)
		request.TargetBinding.RuntimeInstanceID = "persisted-target"
		runtime := newLifecycleHostRuntimeTestDouble()
		runtime.instances[lifecycleHostIdentityFor(request.TargetBinding)] = lifecycleHostRuntimeSnapshot(request.TargetExtension, "persisted-target")

		result, err := NewExactLifecycleCoordinatorHost(runtime, nil).RunLifecycleHostGate(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if result.TargetBinding != request.TargetBinding || result.Checkpoint != request.Checkpoint ||
			result.RevalidationPolicy != extensions.LifecycleGateRevalidationRequired {
			t.Fatalf("revalidation result = %#v", result)
		}
		assertLifecycleHostCalls(t, runtime.calls, "inspect:persisted-target")
	})

	t.Run("discovers matching active instance after persisted instance disappeared", func(t *testing.T) {
		request := lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0)
		request.Revalidation = true
		request.TargetBinding.RuntimeInstanceID = "lost-target"
		runtime := newLifecycleHostRuntimeTestDouble()
		runtime.active[request.TargetExtension.ID] = lifecycleHostRuntimeSnapshot(request.TargetExtension, "active-target")

		result, err := NewExactLifecycleCoordinatorHost(runtime, nil).RunLifecycleHostGate(context.Background(), request)
		if err != nil || result.TargetBinding.RuntimeInstanceID != "active-target" {
			t.Fatalf("active recovery result = %#v, error = %v", result, err)
		}
		assertLifecycleHostCalls(t, runtime.calls, "inspect:lost-target", "active:"+request.TargetExtension.ID)
	})

	t.Run("stages exact artifact when active instance is another version", func(t *testing.T) {
		request := lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0)
		request.Revalidation = true
		request.TargetBinding.RuntimeInstanceID = "lost-target"
		runtime := newLifecycleHostRuntimeTestDouble()
		foreign := request.TargetExtension
		foreign.Version = "9.9.9"
		foreign.PackageDigest = strings.Repeat("f", 64)
		runtime.active[request.TargetExtension.ID] = lifecycleHostRuntimeSnapshot(foreign, "foreign-active")

		result, err := NewExactLifecycleCoordinatorHost(runtime, nil).RunLifecycleHostGate(context.Background(), request)
		if err != nil || result.TargetBinding.RuntimeInstanceID != "staged-"+request.TargetExtension.Version {
			t.Fatalf("staged recovery result = %#v, error = %v", result, err)
		}
		assertLifecycleHostCalls(t, runtime.calls,
			"inspect:lost-target", "active:"+request.TargetExtension.ID, "stage:"+request.TargetExtension.Version,
		)
	})

	t.Run("does not accept another identity for persisted binding", func(t *testing.T) {
		request := lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0)
		request.TargetBinding.RuntimeInstanceID = "persisted-target"
		runtime := newLifecycleHostRuntimeTestDouble()
		runtime.instances[lifecycleHostIdentityFor(request.TargetBinding)] = lifecycleHostRuntimeSnapshot(request.TargetExtension, "other-target")

		_, err := NewExactLifecycleCoordinatorHost(runtime, nil).RunLifecycleHostGate(context.Background(), request)
		if !errors.Is(err, ErrRuntimeInstanceConflict) {
			t.Fatalf("identity drift error = %v", err)
		}
		assertLifecycleHostCalls(t, runtime.calls, "inspect:persisted-target")
	})

	t.Run("does not bind a staged snapshot from another artifact", func(t *testing.T) {
		request := lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0)
		runtime := newLifecycleHostRuntimeTestDouble()
		foreign := request.TargetExtension
		foreign.PackageDigest = strings.Repeat("e", 64)
		result := lifecycleHostRuntimeSnapshot(foreign, "staged-foreign")
		runtime.stageResult = &result

		_, err := NewExactLifecycleCoordinatorHost(runtime, nil).RunLifecycleHostGate(context.Background(), request)
		if !errors.Is(err, ErrRuntimeInstanceConflict) {
			t.Fatalf("staged artifact drift error = %v", err)
		}
		assertLifecycleHostCalls(t, runtime.calls, "active:"+request.TargetExtension.ID, "stage:"+request.TargetExtension.Version)
	})
}

func TestExactLifecycleCoordinatorHostRejectsRuntimeSnapshotDrift(t *testing.T) {
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		forced    bool
		configure func(*lifecycleHostRuntimeTestDouble, extensions.LifecycleCoordinatorGateRequest)
		want      error
	}{
		{
			name: "starting identity", operation: extensions.LifecycleMachineInstall, position: 4,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, request extensions.LifecycleCoordinatorGateRequest) {
				runtime.instances[lifecycleHostIdentityFor(request.TargetBinding)] = lifecycleHostRuntimeSnapshot(request.TargetExtension, "another-target")
			},
			want: ErrRuntimeInstanceConflict,
		},
		{
			name: "health artifact", operation: extensions.LifecycleMachineInstall, position: 6,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, request extensions.LifecycleCoordinatorGateRequest) {
				snapshot := lifecycleHostProtocolSnapshot(request.TargetExtension, request.TargetBinding.RuntimeInstanceID)
				snapshot.ArtifactDigest = strings.Repeat("d", 64)
				runtime.healthResults[lifecycleHostIdentityFor(request.TargetBinding)] = snapshot
			},
			want: ErrRuntimeInstanceConflict,
		},
		{
			name: "health readiness", operation: extensions.LifecycleMachineInstall, position: 6,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, request extensions.LifecycleCoordinatorGateRequest) {
				snapshot := lifecycleHostProtocolSnapshot(request.TargetExtension, request.TargetBinding.RuntimeInstanceID)
				snapshot.Ready = false
				runtime.healthResults[lifecycleHostIdentityFor(request.TargetBinding)] = snapshot
			},
			want: ErrProtocolInstanceNotReady,
		},
		{
			name: "begin drain identity", operation: extensions.LifecycleMachineDisable, position: 1,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, _ extensions.LifecycleCoordinatorGateRequest) {
				result := RuntimeAdmissionSnapshot{Identity: RuntimeInstanceIdentity{ExtensionID: "demo.lifecycle", InstanceID: "another-source"}, Draining: true}
				runtime.beginResult = &result
			},
			want: ErrRuntimeInstanceConflict,
		},
		{
			name: "begin drain remains open", operation: extensions.LifecycleMachineDisable, position: 1,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, request extensions.LifecycleCoordinatorGateRequest) {
				result := RuntimeAdmissionSnapshot{Identity: lifecycleHostIdentityFor(request.SourceBinding)}
				runtime.beginResult = &result
			},
			want: ErrLifecycleHostGateInvalid,
		},
		{
			name: "force drain identity", operation: extensions.LifecycleMachineUninstall, position: 2, forced: true,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, _ extensions.LifecycleCoordinatorGateRequest) {
				result := RuntimeAdmissionSnapshot{Identity: RuntimeInstanceIdentity{ExtensionID: "demo.lifecycle", InstanceID: "another-source"}, Draining: true, Forced: true}
				runtime.forceResult = &result
			},
			want: ErrRuntimeInstanceConflict,
		},
		{
			name: "force drain is not forced", operation: extensions.LifecycleMachineUninstall, position: 2, forced: true,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, request extensions.LifecycleCoordinatorGateRequest) {
				result := RuntimeAdmissionSnapshot{Identity: lifecycleHostIdentityFor(request.SourceBinding), Draining: true}
				runtime.forceResult = &result
			},
			want: ErrLifecycleHostGateInvalid,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := lifecycleHostTestRequest(t, test.operation, test.position)
			request.Forced = test.forced
			runtime := newLifecycleHostRuntimeTestDouble()
			seedLifecycleHostExactInstances(runtime, request)
			test.configure(runtime, request)

			_, err := NewExactLifecycleCoordinatorHost(runtime, &lifecycleHostBoundaryTestDouble{}).RunLifecycleHostGate(context.Background(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("RunLifecycleHostGate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestExactLifecycleCoordinatorHostRebuildsFreshManagerForProcessRevalidation(t *testing.T) {
	tests := []struct {
		name     string
		position int
	}{
		{"drain", 2},
		{"starting", 5},
		{"healthy", 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := lifecycleHostTestRequest(t, extensions.LifecycleMachineUpgrade, test.position)
			request.Revalidation = true

			oldManager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
			oldSource, err := oldManager.StageRuntimeInstance(context.Background(), *request.SourceExtension)
			if err != nil {
				t.Fatal(err)
			}
			oldTarget, err := oldManager.StageRuntimeInstance(context.Background(), request.TargetExtension)
			if err != nil {
				t.Fatal(err)
			}
			request.SourceBinding = lifecycleHostBinding(*request.SourceExtension, oldSource)
			request.TargetBinding = lifecycleHostBinding(request.TargetExtension, oldTarget)

			freshStarter := newManagerStagedStarter()
			freshStarter.next = 100
			freshManager := NewManager(ManagerConfig{Starter: freshStarter})
			result, err := NewExactLifecycleCoordinatorHost(
				freshManager, &lifecycleHostBoundaryTestDouble{},
			).RunLifecycleHostGate(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.RevalidationPolicy != extensions.LifecycleGateRevalidationRequired ||
				result.SourceBinding.RuntimeInstanceID == oldSource.Identity.InstanceID ||
				result.TargetBinding.RuntimeInstanceID == oldTarget.Identity.InstanceID {
				t.Fatalf("revalidation result = %#v", result)
			}
			for role, binding := range map[string]extensions.LifecycleRuntimeBinding{
				"source": result.SourceBinding, "target": result.TargetBinding,
			} {
				snapshot, inspectErr := freshManager.InspectRuntimeInstance(lifecycleHostIdentityFor(binding))
				if inspectErr != nil || snapshot.ExtensionVersion != binding.ExtensionVersion ||
					snapshot.ArtifactDigest != binding.PackageDigest {
					t.Fatalf("fresh %s runtime = %#v, %v", role, snapshot, inspectErr)
				}
			}
		})
	}
}

func TestExactLifecycleCoordinatorHostDoesNotReplaceStaleBindingOutsideRevalidation(t *testing.T) {
	request := lifecycleHostTestRequest(t, extensions.LifecycleMachineUpgrade, 5)
	starter := newManagerStagedStarter()
	starter.next = 100
	manager := NewManager(ManagerConfig{Starter: starter})
	_, err := NewExactLifecycleCoordinatorHost(manager, &lifecycleHostBoundaryTestDouble{}).RunLifecycleHostGate(
		context.Background(), request,
	)
	if !errors.Is(err, ErrRuntimeInstanceNotFound) || starter.next != 100 {
		t.Fatalf("non-revalidation stale binding = %v, starts=%d", err, starter.next-100)
	}
}

func TestExactLifecycleCoordinatorHostForceDrainIsUninstallOnly(t *testing.T) {
	request := lifecycleHostTestRequest(t, extensions.LifecycleMachineUninstall, 2)
	request.Forced = true
	runtime := newLifecycleHostRuntimeTestDouble()
	boundary := &lifecycleHostBoundaryTestDouble{}
	boundary.drainInspect = func(_ extensions.LifecycleCoordinatorGateRequest) {
		if len(runtime.calls) != 0 {
			t.Fatalf("runtime drain started before jobs/schedules closed: %#v", runtime.calls)
		}
	}

	result, err := NewExactLifecycleCoordinatorHost(runtime, boundary).RunLifecycleHostGate(context.Background(), request)
	if err != nil || result.SourceBinding != request.SourceBinding {
		t.Fatalf("forced uninstall drain = %#v, error = %v", result, err)
	}
	assertLifecycleHostCalls(t, runtime.calls, "begin:target-instance", "force:target-instance", "wait:target-instance")
	if len(runtime.forceCauses) != 1 || runtime.forceCauses[0] == nil ||
		!strings.Contains(runtime.forceCauses[0].Error(), fmt.Sprint(request.OperationID)) {
		t.Fatalf("force causes = %#v", runtime.forceCauses)
	}

	disable := lifecycleHostTestRequest(t, extensions.LifecycleMachineDisable, 1)
	disable.Forced = true
	disableRuntime := newLifecycleHostRuntimeTestDouble()
	_, err = NewExactLifecycleCoordinatorHost(disableRuntime, &lifecycleHostBoundaryTestDouble{}).RunLifecycleHostGate(context.Background(), disable)
	if !errors.Is(err, ErrInvalidLifecycleRun) || len(disableRuntime.calls) != 0 {
		t.Fatalf("forced disable error = %v, calls = %#v", err, disableRuntime.calls)
	}
}

func TestExactLifecycleCoordinatorHostRequiresComposedBoundary(t *testing.T) {
	request := lifecycleHostTestRequest(t, extensions.LifecycleMachineUpgrade, 8)
	host := NewExactLifecycleCoordinatorHost(newLifecycleHostRuntimeTestDouble(), nil)
	if _, err := host.RunLifecycleHostGate(context.Background(), request); !errors.Is(err, ErrLifecycleHostBoundaryMissing) {
		t.Fatalf("missing boundary error = %v", err)
	}

	document := json.RawMessage(`{"published":"target-instance"}`)
	request.ActionResults = map[extensions.LifecycleMachineAction]json.RawMessage{
		extensions.LifecycleMachineUpgradePlan: json.RawMessage(`{"approved":true}`),
	}
	boundary := &lifecycleHostBoundaryTestDouble{result: LifecycleHostBoundaryResult{
		Checkpoint: "published-42", ResultDocument: document,
	}}
	ctx := context.WithValue(context.Background(), lifecycleHostContextKey{}, "boundary")
	result, err := NewExactLifecycleCoordinatorHost(newLifecycleHostRuntimeTestDouble(), boundary).RunLifecycleHostGate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Checkpoint != "published-42" || result.RevalidationPolicy != extensions.LifecycleGateDurable ||
		string(result.ResultDocument) != `{"published":"target-instance"}` {
		t.Fatalf("boundary result = %#v", result)
	}
	if len(boundary.calls) != 1 || boundary.contexts[0] != ctx || boundary.calls[0].StepID != request.StepID ||
		string(boundary.calls[0].ActionResults[extensions.LifecycleMachineUpgradePlan]) != `{"approved":true}` {
		t.Fatalf("boundary calls = %#v", boundary.calls)
	}
	boundary.result.ResultDocument[2] = 'X'
	if string(result.ResultDocument) != `{"published":"target-instance"}` {
		t.Fatalf("boundary result aliases implementation memory: %q", result.ResultDocument)
	}
}

func TestExactLifecycleCoordinatorHostPropagatesDependencyErrors(t *testing.T) {
	dependencyError := errors.New("dependency failed")
	tests := []struct {
		name      string
		operation extensions.LifecycleMachineOperation
		position  int
		forced    bool
		configure func(*lifecycleHostRuntimeTestDouble, *lifecycleHostBoundaryTestDouble, extensions.LifecycleCoordinatorGateRequest)
		wantCalls []string
	}{
		{
			name: "stage", operation: extensions.LifecycleMachineInstall, position: 0,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, _ *lifecycleHostBoundaryTestDouble, _ extensions.LifecycleCoordinatorGateRequest) {
				runtime.stageErr = dependencyError
			},
			wantCalls: []string{"active:demo.lifecycle", "stage:2.0.0"},
		},
		{
			name: "inspect", operation: extensions.LifecycleMachineInstall, position: 4,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, _ *lifecycleHostBoundaryTestDouble, request extensions.LifecycleCoordinatorGateRequest) {
				runtime.inspectErrors[lifecycleHostIdentityFor(request.TargetBinding)] = dependencyError
			},
			wantCalls: []string{"inspect:target-instance"},
		},
		{
			name: "health", operation: extensions.LifecycleMachineInstall, position: 6,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, _ *lifecycleHostBoundaryTestDouble, _ extensions.LifecycleCoordinatorGateRequest) {
				runtime.healthErr = dependencyError
			},
			wantCalls: []string{"health:target-instance"},
		},
		{
			name: "begin drain", operation: extensions.LifecycleMachineDisable, position: 1,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, _ *lifecycleHostBoundaryTestDouble, _ extensions.LifecycleCoordinatorGateRequest) {
				runtime.beginErr = dependencyError
			},
			wantCalls: []string{"begin:target-instance"},
		},
		{
			name: "wait drain", operation: extensions.LifecycleMachineDisable, position: 1,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, _ *lifecycleHostBoundaryTestDouble, _ extensions.LifecycleCoordinatorGateRequest) {
				runtime.waitErr = dependencyError
			},
			wantCalls: []string{"begin:target-instance", "wait:target-instance"},
		},
		{
			name: "force drain", operation: extensions.LifecycleMachineUninstall, position: 2, forced: true,
			configure: func(runtime *lifecycleHostRuntimeTestDouble, _ *lifecycleHostBoundaryTestDouble, _ extensions.LifecycleCoordinatorGateRequest) {
				runtime.forceErr = dependencyError
			},
			wantCalls: []string{"begin:target-instance", "force:target-instance"},
		},
		{
			name: "boundary", operation: extensions.LifecycleMachineInstall, position: 2,
			configure: func(_ *lifecycleHostRuntimeTestDouble, boundary *lifecycleHostBoundaryTestDouble, _ extensions.LifecycleCoordinatorGateRequest) {
				boundary.err = dependencyError
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := lifecycleHostTestRequest(t, test.operation, test.position)
			request.Forced = test.forced
			runtime := newLifecycleHostRuntimeTestDouble()
			seedLifecycleHostExactInstances(runtime, request)
			boundary := &lifecycleHostBoundaryTestDouble{}
			test.configure(runtime, boundary, request)

			_, err := NewExactLifecycleCoordinatorHost(runtime, boundary).RunLifecycleHostGate(context.Background(), request)
			if !errors.Is(err, dependencyError) {
				t.Fatalf("RunLifecycleHostGate() error = %v", err)
			}
			assertLifecycleHostCalls(t, runtime.calls, test.wantCalls...)
			if test.name == "boundary" && len(boundary.calls) != 1 {
				t.Fatalf("boundary calls = %d", len(boundary.calls))
			}
		})
	}
}

func TestExactLifecycleCoordinatorHostCommittedPublicationNeverReopensEarlyDrain(t *testing.T) {
	dependencyError := errors.New("drain revalidation failed")
	tests := []struct {
		name      string
		configure func(*lifecycleHostRuntimeTestDouble)
	}{
		{"begin", func(runtime *lifecycleHostRuntimeTestDouble) { runtime.beginErr = dependencyError }},
		{"wait", func(runtime *lifecycleHostRuntimeTestDouble) { runtime.waitErr = dependencyError }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 2)
			fixture.request.Attempt = 2
			fixture.journal.committed = true
			runtime := newLifecycleHostRuntimeTestDouble()
			seedLifecycleHostExactInstances(runtime, fixture.request)
			test.configure(runtime)

			_, err := NewExactLifecycleCoordinatorHost(runtime, fixture.boundary).RunLifecycleHostGate(
				context.Background(), fixture.request,
			)
			if !errors.Is(err, dependencyError) {
				t.Fatalf("RunLifecycleHostGate() error = %v", err)
			}
			if slices.Contains(runtime.calls, "resume:source-instance") ||
				countCall(fixture.calls, "jobs.resume:upgrade:source") != 0 {
				t.Fatalf("committed source reopened: runtime=%#v boundary=%#v", runtime.calls, fixture.calls)
			}
			if len(fixture.journal.operationReads) != 1 || fixture.journal.operationReads[0].Position != 8 {
				t.Fatalf("publication marker reads = %#v", fixture.journal.operationReads)
			}
		})
	}
}

func TestExactLifecycleCoordinatorHostPostMigrationRevalidationRequiresSourceResumeProof(t *testing.T) {
	cause := errors.New("revalidation jobs drain failed")
	fixture := newComposedBoundaryFixture(t, extensions.LifecycleMachineUpgrade, 2)
	fixture.request.Revalidation = true
	fixture.request.Attempt = 2
	fixture.jobs.drainErr = cause
	fixture.migrations.resumeDenied = true
	runtime := newLifecycleHostRuntimeTestDouble()

	_, err := NewExactLifecycleCoordinatorHost(runtime, fixture.boundary).RunLifecycleHostGate(
		context.Background(), fixture.request,
	)
	if !errors.Is(err, cause) || !errors.Is(err, ErrLifecycleBoundaryCompensationFailed) ||
		!strings.Contains(err.Error(), ErrLifecycleBoundarySourceResumeUnsafe.Error()) {
		t.Fatalf("RunLifecycleHostGate() error = %v", err)
	}
	if countCall(fixture.calls, "migrations.resume-proof:upgrade") != 1 ||
		countCall(fixture.calls, "jobs.resume:upgrade:source") != 0 ||
		len(fixture.journal.operationReads) != 1 || fixture.journal.operationReads[0].Position != 8 {
		t.Fatalf("boundary compensation calls = %#v, marker reads = %#v", fixture.calls, fixture.journal.operationReads)
	}
	for _, call := range runtime.calls {
		if strings.HasPrefix(call, "resume:") {
			t.Fatalf("post-migration source reopened: %#v", runtime.calls)
		}
	}
	if countCall(runtime.calls, "begin:staged-1.0.0") != 1 ||
		countCall(runtime.calls, "stage:1.0.0") != 1 || countCall(runtime.calls, "stage:2.0.0") != 1 {
		t.Fatalf("fresh runtime fail-closed calls = %#v", runtime.calls)
	}
}

func TestExactLifecycleCoordinatorHostEarlyDrainCompensationFailsClosed(t *testing.T) {
	cause := errors.New("jobs drain failed")
	compensationError := errors.New("compensation failed")
	tests := []struct {
		name      string
		configure func(*lifecycleHostRuntimeTestDouble, *lifecycleHostBoundaryTestDouble)
		wantCalls []string
		wantJobs  int
	}{
		{
			name: "marker unknown",
			configure: func(_ *lifecycleHostRuntimeTestDouble, boundary *lifecycleHostBoundaryTestDouble) {
				boundary.resumePolicyErr = compensationError
			},
			wantCalls: []string{"begin:source-instance", "wait:source-instance"},
		},
		{
			name: "jobs resume failure",
			configure: func(_ *lifecycleHostRuntimeTestDouble, boundary *lifecycleHostBoundaryTestDouble) {
				boundary.drainResumeErr = compensationError
			},
			wantCalls: []string{"begin:source-instance", "wait:source-instance"},
			wantJobs:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := lifecycleHostTestRequest(t, extensions.LifecycleMachineUpgrade, 2)
			runtime := newLifecycleHostRuntimeTestDouble()
			seedLifecycleHostExactInstances(runtime, request)
			boundary := &lifecycleHostBoundaryTestDouble{drainErr: cause}
			test.configure(runtime, boundary)

			_, err := NewExactLifecycleCoordinatorHost(runtime, boundary).RunLifecycleHostGate(context.Background(), request)
			if !errors.Is(err, cause) || !errors.Is(err, ErrLifecycleBoundaryCompensationFailed) ||
				!strings.Contains(err.Error(), compensationError.Error()) {
				t.Fatalf("RunLifecycleHostGate() error = %v", err)
			}
			assertLifecycleHostCalls(t, runtime.calls, test.wantCalls...)
			if len(boundary.drainResumeCalls) != test.wantJobs {
				t.Fatalf("jobs resume calls = %#v", boundary.drainResumeCalls)
			}
		})
	}
}

func TestExactLifecycleCoordinatorHostRejectsInvalidGateBeforeSideEffects(t *testing.T) {
	runtime := newLifecycleHostRuntimeTestDouble()
	host := NewExactLifecycleCoordinatorHost(runtime, &lifecycleHostBoundaryTestDouble{})

	tests := []struct {
		name    string
		request extensions.LifecycleCoordinatorGateRequest
	}{
		{"plugin action position", lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 1)},
		{"unknown position", lifecycleHostRequestWithUnknownPosition(t)},
		{"unknown operation", lifecycleHostRequestWithUnknownOperation(t)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := len(runtime.calls)
			_, err := host.RunLifecycleHostGate(context.Background(), test.request)
			if !errors.Is(err, ErrLifecycleHostGateInvalid) || len(runtime.calls) != before {
				t.Fatalf("error = %v, calls = %#v", err, runtime.calls[before:])
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := host.RunLifecycleHostGate(ctx, extensions.LifecycleCoordinatorGateRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context error = %v", err)
	}
	if _, err := host.RunLifecycleHostGate(nil, lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0)); !errors.Is(err, ErrLifecycleHostGateInvalid) {
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := (*ExactLifecycleCoordinatorHost)(nil).RunLifecycleHostGate(
		context.Background(), lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0),
	); !errors.Is(err, ErrLifecycleHostGateInvalid) {
		t.Fatalf("nil host error = %v", err)
	}
	if _, err := NewExactLifecycleCoordinatorHost(nil, nil).RunLifecycleHostGate(
		context.Background(), lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0),
	); !errors.Is(err, ErrLifecycleHostGateInvalid) {
		t.Fatalf("nil runtime error = %v", err)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("invalid requests reached runtime: %#v", runtime.calls)
	}
}

type lifecycleHostRuntimeTestDouble struct {
	calls          []string
	instances      map[RuntimeInstanceIdentity]RuntimeInstanceSnapshot
	inspectErrors  map[RuntimeInstanceIdentity]error
	active         map[string]RuntimeInstanceSnapshot
	activeErrors   map[string]error
	healthResults  map[RuntimeInstanceIdentity]ProtocolRuntimeInstanceSnapshot
	stageResult    *RuntimeInstanceSnapshot
	beginResult    *RuntimeAdmissionSnapshot
	forceResult    *RuntimeAdmissionSnapshot
	resumeResult   *RuntimeAdmissionSnapshot
	stageErr       error
	healthErr      error
	beginErr       error
	waitErr        error
	forceErr       error
	resumeErr      error
	forceCauses    []error
	stageContexts  []context.Context
	healthContexts []context.Context
	waitContexts   []context.Context
}

func newLifecycleHostRuntimeTestDouble() *lifecycleHostRuntimeTestDouble {
	return &lifecycleHostRuntimeTestDouble{
		instances:     make(map[RuntimeInstanceIdentity]RuntimeInstanceSnapshot),
		inspectErrors: make(map[RuntimeInstanceIdentity]error),
		active:        make(map[string]RuntimeInstanceSnapshot), activeErrors: make(map[string]error),
		healthResults: make(map[RuntimeInstanceIdentity]ProtocolRuntimeInstanceSnapshot),
	}
}

func (r *lifecycleHostRuntimeTestDouble) StageRuntimeInstance(
	ctx context.Context,
	extension extensions.Extension,
) (RuntimeInstanceSnapshot, error) {
	r.calls = append(r.calls, "stage:"+extension.Version)
	r.stageContexts = append(r.stageContexts, ctx)
	if r.stageErr != nil {
		return RuntimeInstanceSnapshot{}, r.stageErr
	}
	if r.stageResult != nil {
		return *r.stageResult, nil
	}
	return lifecycleHostRuntimeSnapshot(extension, "staged-"+extension.Version), nil
}

func (r *lifecycleHostRuntimeTestDouble) InspectRuntimeInstance(identity RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error) {
	r.calls = append(r.calls, "inspect:"+identity.InstanceID)
	if err := r.inspectErrors[identity]; err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	if snapshot, ok := r.instances[identity]; ok {
		return snapshot, nil
	}
	return RuntimeInstanceSnapshot{}, ErrRuntimeInstanceNotFound
}

func (r *lifecycleHostRuntimeTestDouble) ActiveRuntimeInstance(extensionID string) (RuntimeInstanceSnapshot, error) {
	r.calls = append(r.calls, "active:"+extensionID)
	if err := r.activeErrors[extensionID]; err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	if snapshot, ok := r.active[extensionID]; ok {
		return snapshot, nil
	}
	return RuntimeInstanceSnapshot{}, ErrRuntimeInstanceNotFound
}

func (r *lifecycleHostRuntimeTestDouble) HealthRuntimeInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
) (ProtocolRuntimeInstanceSnapshot, error) {
	r.calls = append(r.calls, "health:"+identity.InstanceID)
	r.healthContexts = append(r.healthContexts, ctx)
	if r.healthErr != nil {
		return ProtocolRuntimeInstanceSnapshot{}, r.healthErr
	}
	if snapshot, ok := r.healthResults[identity]; ok {
		return snapshot, nil
	}
	return ProtocolRuntimeInstanceSnapshot{}, ErrRuntimeInstanceNotFound
}

func (r *lifecycleHostRuntimeTestDouble) BeginDrain(identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	r.calls = append(r.calls, "begin:"+identity.InstanceID)
	if r.beginErr != nil {
		return RuntimeAdmissionSnapshot{}, r.beginErr
	}
	if r.beginResult != nil {
		return *r.beginResult, nil
	}
	return RuntimeAdmissionSnapshot{Identity: identity, Draining: true}, nil
}

func (r *lifecycleHostRuntimeTestDouble) WaitDrain(ctx context.Context, identity RuntimeInstanceIdentity) error {
	r.calls = append(r.calls, "wait:"+identity.InstanceID)
	r.waitContexts = append(r.waitContexts, ctx)
	return r.waitErr
}

func (r *lifecycleHostRuntimeTestDouble) ForceDrain(identity RuntimeInstanceIdentity, cause error) (RuntimeAdmissionSnapshot, error) {
	r.calls = append(r.calls, "force:"+identity.InstanceID)
	r.forceCauses = append(r.forceCauses, cause)
	if r.forceErr != nil {
		return RuntimeAdmissionSnapshot{}, r.forceErr
	}
	if r.forceResult != nil {
		return *r.forceResult, nil
	}
	return RuntimeAdmissionSnapshot{Identity: identity, Draining: true, Forced: true, ForceCause: cause}, nil
}

type lifecycleHostBoundaryTestDouble struct {
	contexts         []context.Context
	calls            []extensions.LifecycleCoordinatorGateRequest
	drainCalls       []extensions.LifecycleCoordinatorGateRequest
	drainResumeCalls []extensions.LifecycleCoordinatorGateRequest
	result           LifecycleHostBoundaryResult
	err              error
	drainErr         error
	drainResumeErr   error
	resumePolicyErr  error
	resumeDenied     bool
	drainInspect     func(extensions.LifecycleCoordinatorGateRequest)
}

func (b *lifecycleHostBoundaryTestDouble) RunLifecycleHostBoundary(
	ctx context.Context,
	request extensions.LifecycleCoordinatorGateRequest,
) (LifecycleHostBoundaryResult, error) {
	b.contexts = append(b.contexts, ctx)
	b.calls = append(b.calls, request)
	return b.result, b.err
}

type lifecycleHostContextKey struct{}

// assertLifecycleHostDerivedContext 校验派生上下文保留了调用方上下文的值，
// 并且除操作者数据库授权外没有别的派生内容。
func assertLifecycleHostDerivedContext(t *testing.T, parent, derived context.Context, label string) {
	t.Helper()
	if derived == parent {
		return
	}
	// 唯一允许的派生是启动期注入的操作者数据库授权。
	if _, ok := extensions.DatabaseGrantAuthorityFrom(derived); !ok {
		t.Fatalf("%s did not receive the caller context: unexpected derivation", label)
	}
	observed, _ := derived.Value(lifecycleHostContextKey{}).(string)
	want, _ := parent.Value(lifecycleHostContextKey{}).(string)
	if observed != want {
		t.Fatalf("%s context values drifted: %q want %q", label, observed, want)
	}
}

func lifecycleHostTestRequest(
	t *testing.T,
	operation extensions.LifecycleMachineOperation,
	position int,
) extensions.LifecycleCoordinatorGateRequest {
	t.Helper()
	source := exactCoordinatorTestExtension(
		"demo.lifecycle", "1.0.0", strings.Repeat("a", 64), "demo.source.lifecycle@1", 11,
	)
	target := exactCoordinatorTestExtension(
		"demo.lifecycle", "2.0.0", strings.Repeat("b", 64), "demo.target.lifecycle@2", 12,
	)
	path, err := extensions.RecommendedLifecyclePath(operation)
	if err != nil || position < 0 || position >= len(path) {
		t.Fatalf("invalid test gate %s/%d: %v", operation, position, err)
	}
	request := extensions.LifecycleCoordinatorGateRequest{
		Extension: target, TargetExtension: target,
		TargetBinding: lifecycleHostBindingForTest(target, "target-instance"),
		OperationID:   41, Operation: operation, State: path[position].State, Position: position,
		StepID:  fmt.Sprintf("lifecycle.%s.%02d.host.%s", operation, position, path[position].State),
		Attempt: 2, Checkpoint: "checkpoint-1",
		AuthorityType: extensions.LifecycleAuthorityTrustGrant, TrustGrantID: 51,
		AuthorityActorUserID: 61, ActorUserID: 61, AuditEventID: 71,
	}
	switch operation {
	case extensions.LifecycleMachineInstall:
	case extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineRollback:
		request.SourceExtension = &source
		request.SourceBinding = lifecycleHostBindingForTest(source, "source-instance")
	default:
		request.SourceBinding = request.TargetBinding
	}
	if position == 0 {
		request.TargetBinding.RuntimeInstanceID = ""
		request.SourceBinding.RuntimeInstanceID = ""
	}
	if operation == extensions.LifecycleMachineUninstall {
		request.RemovalMode = extensions.LifecycleRemovalPreserve
	}
	request.AuthoritySnapshot = exactCoordinatorTestAuthority(t, target, request.AuthorityActorUserID, request.TrustGrantID)
	return request
}

func lifecycleHostRequestWithUnknownPosition(t *testing.T) extensions.LifecycleCoordinatorGateRequest {
	t.Helper()
	request := lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0)
	request.Position = 99
	request.StepID = "lifecycle.install.99.host.planned"
	return request
}

func lifecycleHostRequestWithUnknownOperation(t *testing.T) extensions.LifecycleCoordinatorGateRequest {
	t.Helper()
	request := lifecycleHostTestRequest(t, extensions.LifecycleMachineInstall, 0)
	request.Operation = extensions.LifecycleMachineOperation("unknown")
	request.StepID = "lifecycle.unknown.00.host.planned"
	return request
}

func lifecycleHostBindingForTest(extension extensions.Extension, instanceID string) extensions.LifecycleRuntimeBinding {
	return extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
		RuntimeInstanceID: instanceID, VersionID: extension.ActiveVersionID,
	}
}

func lifecycleHostIdentityFor(binding extensions.LifecycleRuntimeBinding) RuntimeInstanceIdentity {
	return RuntimeInstanceIdentity{ExtensionID: binding.ExtensionID, InstanceID: binding.RuntimeInstanceID}
}

func lifecycleHostRuntimeSnapshot(extension extensions.Extension, instanceID string) RuntimeInstanceSnapshot {
	return RuntimeInstanceSnapshot{
		Identity:         RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: instanceID},
		ExtensionVersion: extension.Version, ArtifactDigest: extension.PackageDigest,
		VersionID: extension.ActiveVersionID,
	}
}

func lifecycleHostProtocolSnapshot(extension extensions.Extension, instanceID string) ProtocolRuntimeInstanceSnapshot {
	return ProtocolRuntimeInstanceSnapshot{
		Identity:         RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: instanceID},
		ExtensionVersion: extension.Version, ArtifactDigest: extension.PackageDigest,
		Healthy: true, Ready: true, ReadinessChecked: true,
	}
}

func seedLifecycleHostExactInstances(
	runtime *lifecycleHostRuntimeTestDouble,
	request extensions.LifecycleCoordinatorGateRequest,
) {
	if request.TargetBinding.RuntimeInstanceID != "" {
		identity := lifecycleHostIdentityFor(request.TargetBinding)
		runtime.instances[identity] = lifecycleHostRuntimeSnapshot(request.TargetExtension, identity.InstanceID)
		runtime.healthResults[identity] = lifecycleHostProtocolSnapshot(request.TargetExtension, identity.InstanceID)
	}
	if request.SourceBinding.RuntimeInstanceID != "" && lifecycleHostRequiresSource(request.Operation) {
		source := lifecycleHostSourceExtension(request)
		identity := lifecycleHostIdentityFor(request.SourceBinding)
		runtime.instances[identity] = lifecycleHostRuntimeSnapshot(source, identity.InstanceID)
	}
}

func assertLifecycleHostDispatch(
	t *testing.T,
	runtime *lifecycleHostRuntimeTestDouble,
	boundary *lifecycleHostBoundaryTestDouble,
	request extensions.LifecycleCoordinatorGateRequest,
	result extensions.LifecycleCoordinatorGateResult,
	kind lifecycleHostGateKind,
	ctx context.Context,
) {
	t.Helper()
	wantRuntime := map[string]int{}
	wantBoundary := 0
	switch kind {
	case lifecycleHostGatePrepare:
		count := 1
		if lifecycleHostRequiresSource(request.Operation) && lifecycleHostRequiresTarget(request.Operation) {
			count = 2
		}
		wantRuntime["active"] = count
		wantRuntime["stage"] = count
		if len(runtime.stageContexts) != count {
			t.Fatalf("stage contexts = %d, want %d", len(runtime.stageContexts), count)
		}
		for _, actual := range runtime.stageContexts {
			// Prepare 阶段允许派生上下文：Host 会为目标进程的启动附加
			// 操作者数据库授权（actor + 审计锚点），但调用方上下文的值必须
			// 原样可见，且不得出现其它派生。
			assertLifecycleHostDerivedContext(t, ctx, actual, "stage")
		}
	case lifecycleHostGateStarting:
		wantRuntime["inspect"] = 1
	case lifecycleHostGateHealthy:
		wantRuntime["health"] = 1
		if len(runtime.healthContexts) != 1 || runtime.healthContexts[0] != ctx {
			t.Fatal("health did not receive caller context")
		}
	case lifecycleHostGateDrain:
		if len(boundary.drainCalls) != 1 || boundary.drainCalls[0].StepID != request.StepID {
			t.Fatalf("jobs/schedules drain calls = %#v", boundary.drainCalls)
		}
		wantRuntime["begin"] = 1
		wantRuntime["wait"] = 1
		if len(runtime.waitContexts) != 1 || runtime.waitContexts[0] != ctx {
			t.Fatal("drain wait did not receive caller context")
		}
	case lifecycleHostGateBoundary:
		wantBoundary = 1
		if len(boundary.contexts) != 1 || boundary.contexts[0] != ctx {
			t.Fatal("boundary did not receive caller context")
		}
	default:
		t.Fatalf("unsupported test kind %d", kind)
	}
	for _, callKind := range []string{"stage", "inspect", "active", "health", "begin", "force", "wait"} {
		if got := countLifecycleHostCalls(runtime.calls, callKind); got != wantRuntime[callKind] {
			t.Fatalf("%s calls = %d, want %d; all calls = %#v", callKind, got, wantRuntime[callKind], runtime.calls)
		}
	}
	if len(boundary.calls) != wantBoundary {
		t.Fatalf("boundary calls = %d, want %d", len(boundary.calls), wantBoundary)
	}

	if kind == lifecycleHostGateBoundary {
		if result.Checkpoint != "boundary-checkpoint" || result.RevalidationPolicy != extensions.LifecycleGateDurable ||
			string(result.ResultDocument) != `{"boundary":true}` {
			t.Fatalf("boundary result = %#v", result)
		}
		if boundary.calls[0].Operation != request.Operation || boundary.calls[0].Position != request.Position {
			t.Fatalf("boundary request = %#v", boundary.calls[0])
		}
		return
	}
	if result.Checkpoint != request.Checkpoint || result.RevalidationPolicy != extensions.LifecycleGateRevalidationRequired {
		t.Fatalf("process result = %#v", result)
	}
	if lifecycleHostRequiresSource(request.Operation) {
		if result.SourceBinding.RuntimeInstanceID == "" || result.SourceBinding.ExtensionVersion != lifecycleHostSourceExtension(request).Version {
			t.Fatalf("source binding = %#v", result.SourceBinding)
		}
	} else if result.SourceBinding != (extensions.LifecycleRuntimeBinding{}) {
		t.Fatalf("unexpected source binding = %#v", result.SourceBinding)
	}
	if lifecycleHostRequiresTarget(request.Operation) {
		if result.TargetBinding.RuntimeInstanceID == "" || result.TargetBinding.ExtensionVersion != request.TargetExtension.Version {
			t.Fatalf("target binding = %#v", result.TargetBinding)
		}
	} else if result.TargetBinding != (extensions.LifecycleRuntimeBinding{}) {
		t.Fatalf("unexpected target binding = %#v", result.TargetBinding)
	}
}

func countLifecycleHostCalls(calls []string, kind string) int {
	count := 0
	for _, call := range calls {
		if strings.HasPrefix(call, kind+":") {
			count++
		}
	}
	return count
}

func assertLifecycleHostCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("calls = %#v, want %#v", got, want)
		}
	}
}

var _ LifecycleHostRuntime = (*lifecycleHostRuntimeTestDouble)(nil)
var _ LifecycleHostBoundary = (*lifecycleHostBoundaryTestDouble)(nil)

func TestLifecycleHostRuntimeStartContextCarriesOperatorGrantAuthority(t *testing.T) {
	bound := lifecycleHostRuntimeStartContext(context.Background(), extensions.LifecycleCoordinatorGateRequest{
		ActorUserID: 51, AuditEventID: 61,
	})
	authority, ok := extensions.DatabaseGrantAuthorityFrom(bound)
	if !ok || authority.ActorUserID != 51 || authority.AuditEventID != 61 {
		t.Fatalf("operator bound gate must carry the grant authority: %#v ok=%v", authority, ok)
	}

	// 恢复/重放步骤没有操作者，也没有归属的审计事件：保持 host 权威。
	for _, request := range []extensions.LifecycleCoordinatorGateRequest{
		{},
		{ActorUserID: 51},
		{AuditEventID: 61},
	} {
		if _, ok := extensions.DatabaseGrantAuthorityFrom(lifecycleHostRuntimeStartContext(context.Background(), request)); ok {
			t.Fatalf("partial gate identity %#v must not produce an authority", request)
		}
	}
}

func TestExactLifecycleCoordinatorHostSourceRoleReusesRecordedActiveRuntime(t *testing.T) {
	request := lifecycleHostTestRequest(t, extensions.LifecycleMachineUpgrade, 0)
	source := lifecycleHostSourceExtension(request)
	runtime := newLifecycleHostRuntimeTestDouble()
	runtime.active[source.ID] = lifecycleHostRuntimeSnapshot(source, "live-source")

	result, err := NewExactLifecycleCoordinatorHost(runtime, &lifecycleHostBoundaryTestDouble{}).
		RunLifecycleHostGate(context.Background(), request)
	if err != nil {
		t.Fatalf("RunLifecycleHostGate() error = %v", err)
	}
	if result.SourceBinding.RuntimeInstanceID != "live-source" ||
		result.TargetBinding.RuntimeInstanceID != "staged-"+request.TargetExtension.Version {
		t.Fatalf("bindings = %#v", result)
	}
	if countLifecycleHostCalls(runtime.calls, "stage") != 1 ||
		countLifecycleHostCalls(runtime.calls, "stage:"+source.Version) != 0 {
		t.Fatalf("source role staged a replacement: %#v", runtime.calls)
	}
}

func TestExactLifecycleCoordinatorHostSourceRoleFailsClosedOnRuntimeDivergence(t *testing.T) {
	divergedSnapshot := func(source extensions.Extension) RuntimeInstanceSnapshot {
		snapshot := lifecycleHostRuntimeSnapshot(source, "live-foreign")
		snapshot.ExtensionVersion = source.Version
		snapshot.ArtifactDigest = strings.Repeat("c", 64)
		snapshot.VersionID = 999
		return snapshot
	}
	assertDiverged := func(t *testing.T, err error, source extensions.Extension, runtime *lifecycleHostRuntimeTestDouble) {
		t.Helper()
		if !errors.Is(err, ErrLifecycleHostRuntimeDiverged) {
			t.Fatalf("divergence error = %v", err)
		}
		message := err.Error()
		for _, want := range []string{
			"live-foreign", strings.Repeat("c", 64), source.PackageDigest, "999",
		} {
			if !strings.Contains(message, want) {
				t.Fatalf("divergence message %q does not carry %q", message, want)
			}
		}
		if countLifecycleHostCalls(runtime.calls, "stage") != 0 {
			t.Fatalf("diverged source role staged a decoy: %#v", runtime.calls)
		}
	}

	t.Run("prepare", func(t *testing.T) {
		request := lifecycleHostTestRequest(t, extensions.LifecycleMachineUpgrade, 0)
		source := lifecycleHostSourceExtension(request)
		runtime := newLifecycleHostRuntimeTestDouble()
		runtime.active[source.ID] = divergedSnapshot(source)

		_, err := NewExactLifecycleCoordinatorHost(runtime, nil).
			RunLifecycleHostGate(context.Background(), request)
		assertDiverged(t, err, source, runtime)
	})

	t.Run("revalidation", func(t *testing.T) {
		request := lifecycleHostTestRequest(t, extensions.LifecycleMachineUpgrade, 2)
		request.Revalidation = true
		request.SourceBinding.RuntimeInstanceID = "lost-source"
		source := lifecycleHostSourceExtension(request)
		runtime := newLifecycleHostRuntimeTestDouble()
		runtime.active[source.ID] = divergedSnapshot(source)

		_, err := NewExactLifecycleCoordinatorHost(runtime, nil).
			RunLifecycleHostGate(context.Background(), request)
		assertDiverged(t, err, source, runtime)
	})
}
