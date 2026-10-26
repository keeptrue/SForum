package extensionsruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type componentProbeLease struct {
	ctx         context.Context
	validate    func(int32) error
	onRelease   func()
	validates   atomic.Int32
	released    atomic.Bool
	releaseOnce sync.Once
}

func (l *componentProbeLease) Context() context.Context { return l.ctx }

func (l *componentProbeLease) Validate(context.Context) error {
	count := l.validates.Add(1)
	if l.validate != nil {
		return l.validate(count)
	}
	return nil
}

func (l *componentProbeLease) Release() {
	if l == nil {
		return
	}
	l.releaseOnce.Do(func() {
		l.released.Store(true)
		if l.onRelease != nil {
			l.onRelease()
		}
	})
}

type componentTerminatorFunc func(ComponentRendererTermination)

func (f componentTerminatorFunc) TerminateComponentCall(termination ComponentRendererTermination) {
	f(termination)
}

func TestComponentCompositionLeaseRevokedDuringFinalValidationDoesNotEscape(t *testing.T) {
	id := "composition.final-revoke"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "replace", extensionmanifest.ComponentActionReplace, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-final-revoke"); err != nil {
		t.Fatal(err)
	}
	leaseCtx, revoke := context.WithCancel(context.Background())
	lease := &componentProbeLease{ctx: leaseCtx}
	lease.validate = func(count int32) error {
		if count == 4 {
			revoke()
		}
		return nil
	}
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry,
		Renderer: ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{
				Artifact: call.Artifact, Document: map[string]any{"html": "revoked"},
				Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>revoked</main>", PrimaryContent: true}},
			}, nil
		}),
		Admission: componentTestAdmissionFunc(func(
			_ context.Context,
			request ComponentRuntimeAdmissionRequest,
		) (ComponentRuntimeAdmissionLease, error) {
			if request.Revision != 1 || request.Artifact.RuntimeInstanceID != "runtime-final-revoke" ||
				request.ContributionID != extension.Manifest.Components[0].ID {
				t.Fatalf("inexact admission request: %#v", request)
			}
			return lease, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
	})
	if !errors.Is(err, ErrComponentCompositionUnauthorized) || len(result.Segments) != 0 ||
		lease.validates.Load() != 4 || !lease.released.Load() {
		t.Fatalf("revoked release: result=%#v validations=%d released=%t err=%v",
			result, lease.validates.Load(), lease.released.Load(), err)
	}
}

func TestComponentCompositionHideRequiresExactLeaseAndFallsBackWhenUnavailable(t *testing.T) {
	id := "composition.hide-lease"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "hide", extensionmanifest.ComponentActionHide, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-hide"); err != nil {
		t.Fatal(err)
	}
	var rendererCalled atomic.Bool
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry,
		Renderer: ComponentSSRRendererFunc(func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
			rendererCalled.Store(true)
			return ComponentRenderResponse{}, errors.New("hide must not invoke extension code")
		}),
		Admission: componentTestAdmissionFunc(func(
			context.Context,
			ComponentRuntimeAdmissionRequest,
		) (ComponentRuntimeAdmissionLease, error) {
			return nil, errors.New("trust unavailable")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
	})
	if err != nil || result.Hidden || rendererCalled.Load() || result.Result["html"] != "core" ||
		len(result.Segments) != 1 || len(result.Segments[0].Fallback) != 1 ||
		result.Segments[0].Fallback[0].Reason != "unauthorized_artifact" {
		t.Fatalf("hide unavailable fallback: result=%#v called=%t err=%v", result, rendererCalled.Load(), err)
	}
}

func TestComponentCompositionHideRevokedDuringFallbackValidationDoesNotEscape(t *testing.T) {
	id := "composition.hide-revoke"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "hide", extensionmanifest.ComponentActionHide, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-hide-revoke"); err != nil {
		t.Fatal(err)
	}
	leaseCtx, revoke := context.WithCancel(context.Background())
	lease := &componentProbeLease{ctx: leaseCtx}
	binding := componentCompositionTestBinding()
	baseValidate := binding.Contract.ValidateResult
	binding.Contract.ValidateResult = func(ctx context.Context, document map[string]any) error {
		err := baseValidate(ctx, document)
		revoke()
		return err
	}
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry,
		Renderer: ComponentSSRRendererFunc(func(context.Context, ComponentRenderCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{}, errors.New("unexpected renderer call")
		}),
		Admission: componentTestAdmissionFunc(func(
			context.Context,
			ComponentRuntimeAdmissionRequest,
		) (ComponentRuntimeAdmissionLease, error) {
			return lease, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: binding,
	})
	if !errors.Is(err, ErrComponentCompositionUnauthorized) || len(result.Segments) != 0 || !lease.released.Load() {
		t.Fatalf("revoked hide release: result=%#v released=%t err=%v", result, lease.released.Load(), err)
	}
}

func TestComponentCompositionNonCooperativeRendererRetainsLeaseAndCapacity(t *testing.T) {
	id := "composition.non-cooperative"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "replace", extensionmanifest.ComponentActionReplace, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-never-return"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	unblock := make(chan struct{})
	firstReleased := make(chan struct{})
	var renderCalls atomic.Int32
	var admissions atomic.Int32
	var terminations atomic.Int32
	admission := componentTestAdmissionFunc(func(
		context.Context,
		ComponentRuntimeAdmissionRequest,
	) (ComponentRuntimeAdmissionLease, error) {
		sequence := admissions.Add(1)
		lease := &componentProbeLease{ctx: context.Background()}
		if sequence == 1 {
			lease.onRelease = func() { close(firstReleased) }
		}
		return lease, nil
	})
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry, Admission: admission,
		DefaultTimeout: 10 * time.Millisecond, MaxConcurrentCalls: 1,
		ResolvePolicy: func(ComponentContribution) ComponentCallPolicy {
			return ComponentCallPolicy{FailurePolicy: appevents.FailurePolicyFailOpen, Timeout: 10 * time.Millisecond}
		},
		Terminator: componentTerminatorFunc(func(termination ComponentRendererTermination) {
			if termination.Request.Artifact.RuntimeInstanceID != "runtime-never-return" || termination.Cause == nil {
				t.Errorf("inexact termination: %#v", termination)
			}
			terminations.Add(1)
		}),
		Renderer: ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
			if renderCalls.Add(1) == 1 {
				close(started)
				<-unblock
			}
			return ComponentRenderResponse{
				Artifact: call.Artifact, Document: map[string]any{"html": "extension"},
				Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>extension</main>", PrimaryContent: true}},
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	compose := func() (ComponentCompositionResult, error) {
		return executor.Compose(context.Background(), ComponentCompositionRequest{
			TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
			Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
		})
	}
	first, err := compose()
	<-started
	if err != nil || first.Result["html"] != "core" || first.Segments[0].Fallback[0].Reason != "timeout" {
		t.Fatalf("first timeout fallback: result=%#v err=%v", first, err)
	}
	select {
	case <-firstReleased:
		t.Fatal("runtime lease released while renderer still runs")
	default:
	}
	second, err := compose()
	if err != nil || second.Result["html"] != "core" || second.Segments[0].Fallback[0].Reason != "renderer_busy" ||
		renderCalls.Load() != 1 || terminations.Load() != 1 {
		t.Fatalf("bounded second call: result=%#v calls=%d terminations=%d err=%v",
			second, renderCalls.Load(), terminations.Load(), err)
	}
	close(unblock)
	select {
	case <-firstReleased:
	case <-time.After(time.Second):
		t.Fatal("runtime lease was not released after renderer exited")
	}
	third, err := compose()
	if err != nil || third.Result["html"] != "extension" || renderCalls.Load() != 2 {
		t.Fatalf("capacity did not recover: result=%#v calls=%d err=%v", third, renderCalls.Load(), err)
	}
}

func TestComponentCompositionCancellationWaitsForCooperativeRendererExit(t *testing.T) {
	id := "composition.cancel"
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "replace", extensionmanifest.ComponentActionReplace, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-cancel"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	released := make(chan struct{})
	lease := &componentProbeLease{ctx: context.Background(), onRelease: func() { close(released) }}
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry: registry,
		Admission: componentTestAdmissionFunc(func(
			context.Context,
			ComponentRuntimeAdmissionRequest,
		) (ComponentRuntimeAdmissionLease, error) {
			return lease, nil
		}),
		Renderer: ComponentSSRRendererFunc(func(ctx context.Context, _ ComponentRenderCall) (ComponentRenderResponse, error) {
			close(started)
			<-ctx.Done()
			return ComponentRenderResponse{}, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, composeErr := executor.Compose(ctx, ComponentCompositionRequest{
			TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
			Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
		})
		done <- composeErr
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled composition error=%v", err)
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("cooperative renderer lease was not released")
	}
}
