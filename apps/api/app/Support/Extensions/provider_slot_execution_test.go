package extensionsruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestManagerVersionedProviderFallsBackWithIsolatedInputAndHostRevalidation(t *testing.T) {
	starter := newProviderInvocationStarter()
	starter.invoke = func(_ context.Context, extension extensions.Extension, request VersionedProviderRequest) (VersionedProviderResponse, error) {
		starter.record(extension.ID)
		nested := request.Input["nested"].(map[string]any)
		if extension.ID == "providers.consumer" {
			nested["value"] = "mutated-by-failed-provider"
			return VersionedProviderResponse{}, errors.New("consumer crashed")
		}
		if nested["value"] != "safe" {
			return VersionedProviderResponse{}, errors.New("failed provider polluted fallback input")
		}
		return VersionedProviderResponse{Output: map[string]any{"status": "delivered"}}, nil
	}
	manager := NewManager(ManagerConfig{Starter: starter})
	owner, consumer := startProviderOwnerAndConsumer(t, manager, "next")
	callerInput := map[string]any{"nested": map[string]any{"value": "safe"}}
	validations := []string{}
	result, err := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
		Caller: providerCaller(t, manager, consumer), SlotID: providerSlotID,
		ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
		InputSchema: providerRequestSchema, Input: callerInput,
		Revalidate: func(_ context.Context, schema string, document map[string]any) error {
			validations = append(validations, schema)
			if schema == providerRequestSchema && document["nested"].(map[string]any)["value"] != "safe" {
				return errors.New("request input was mutated")
			}
			return nil
		},
	})
	if err != nil || result.ExtensionID != owner.ID || result.ProviderID != providerSlotID || result.Attempts != 2 || result.Output["status"] != "delivered" {
		t.Fatalf("fallback result = %#v, %v", result, err)
	}
	if got := starter.calls(); !reflect.DeepEqual(got, []string{consumer.ID, owner.ID}) {
		t.Fatalf("provider calls = %#v", got)
	}
	if !reflect.DeepEqual(validations, []string{providerRequestSchema, providerRequestSchema, providerResponseSchema}) {
		t.Fatalf("Host revalidations = %#v", validations)
	}
	if callerInput["nested"].(map[string]any)["value"] != "safe" {
		t.Fatalf("caller input was mutated = %#v", callerInput)
	}
}

func TestManagerVersionedProviderClosedAndInvalidOutputStopOrFallback(t *testing.T) {
	t.Run("closed stops after first failure", func(t *testing.T) {
		starter := newProviderInvocationStarter()
		starter.invoke = func(_ context.Context, extension extensions.Extension, _ VersionedProviderRequest) (VersionedProviderResponse, error) {
			starter.record(extension.ID)
			return VersionedProviderResponse{}, errors.New("provider failed")
		}
		manager := NewManager(ManagerConfig{Starter: starter})
		_, consumer := startProviderOwnerAndConsumer(t, manager, "closed")
		result, err := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
			Caller: providerCaller(t, manager, consumer), SlotID: providerSlotID,
			ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
			InputSchema: providerRequestSchema, Input: map[string]any{},
			Revalidate: func(context.Context, string, map[string]any) error { return nil },
		})
		if !errors.Is(err, ErrProviderSlotNoProvider) || result.Attempts != 1 || !reflect.DeepEqual(starter.calls(), []string{consumer.ID}) {
			t.Fatalf("closed fallback = %#v, calls=%#v, err=%v", result, starter.calls(), err)
		}
	})

	t.Run("next rejects invalid output and tries the next candidate", func(t *testing.T) {
		starter := newProviderInvocationStarter()
		starter.invoke = func(_ context.Context, extension extensions.Extension, _ VersionedProviderRequest) (VersionedProviderResponse, error) {
			starter.record(extension.ID)
			if extension.ID == "providers.consumer" {
				return VersionedProviderResponse{Output: map[string]any{"status": "invalid"}}, nil
			}
			return VersionedProviderResponse{Output: map[string]any{"status": "valid"}}, nil
		}
		manager := NewManager(ManagerConfig{Starter: starter})
		owner, consumer := startProviderOwnerAndConsumer(t, manager, "next")
		result, err := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
			Caller: providerCaller(t, manager, consumer), SlotID: providerSlotID,
			ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
			InputSchema: providerRequestSchema, Input: map[string]any{},
			Revalidate: func(_ context.Context, schema string, document map[string]any) error {
				if schema == providerResponseSchema && document["status"] == "invalid" {
					return errors.New("output schema rejected")
				}
				return nil
			},
		})
		if err != nil || result.ExtensionID != owner.ID || result.Attempts != 2 || result.Output["status"] != "valid" {
			t.Fatalf("output fallback = %#v, %v", result, err)
		}
	})
}

func TestManagerVersionedProviderHonorsDurablePreferredCandidate(t *testing.T) {
	starter := newProviderInvocationStarter()
	starter.invoke = func(_ context.Context, extension extensions.Extension, _ VersionedProviderRequest) (VersionedProviderResponse, error) {
		starter.record(extension.ID)
		return VersionedProviderResponse{Output: map[string]any{"status": extension.ID}}, nil
	}
	manager := NewManager(ManagerConfig{Starter: starter})
	owner, consumer := startProviderOwnerAndConsumer(t, manager, "next")
	store := &providerSlotSelectionMemoryStore{}
	manager.BindProviderSlotSelections(store)
	selection, err := manager.ProviderSlotSelections().Select(t.Context(), providerSlotID, owner.Manifest.Providers[0].ID, 0, 11, 21)
	if err != nil {
		t.Fatal(err)
	}
	store.selection = selection

	result, err := manager.InvokeVersionedProvider(t.Context(), VersionedProviderInvocation{
		Caller: providerCaller(t, manager, consumer), SlotID: providerSlotID,
		ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
		InputSchema: providerRequestSchema, Input: map[string]any{},
		Revalidate: func(context.Context, string, map[string]any) error { return nil },
	})
	if err != nil || result.ExtensionID != owner.ID || !reflect.DeepEqual(starter.calls(), []string{owner.ID}) {
		t.Fatalf("selected provider result=%#v calls=%#v err=%v", result, starter.calls(), err)
	}
}

func TestManagerVersionedProviderFailsClosedForStaleDurableChoice(t *testing.T) {
	starter := newProviderInvocationStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	owner, consumer := startProviderOwnerAndConsumer(t, manager, "closed")
	store := &providerSlotSelectionMemoryStore{}
	manager.BindProviderSlotSelections(store)
	selection, err := manager.ProviderSlotSelections().Select(t.Context(), providerSlotID, owner.Manifest.Providers[0].ID, 0, 11, 21)
	if err != nil {
		t.Fatal(err)
	}
	selection.ProviderArtifact.PackageDigest = strings.Repeat("f", 64)
	store.selection = selection

	_, err = manager.InvokeVersionedProvider(t.Context(), VersionedProviderInvocation{
		Caller: providerCaller(t, manager, consumer), SlotID: providerSlotID,
		ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
		InputSchema: providerRequestSchema, Input: map[string]any{},
		Revalidate: func(context.Context, string, map[string]any) error { return nil },
	})
	if !errors.Is(err, ErrProviderSlotSelectionStale) || len(starter.calls()) != 0 {
		t.Fatalf("stale closed selection calls=%#v err=%v", starter.calls(), err)
	}
}

func TestManagerVersionedProviderRetainsAdmissionWhenInvokerIgnoresTimeout(t *testing.T) {
	started := make(chan struct{})
	deadlineObserved := make(chan struct{})
	releaseInvocation := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseInvocation) }) }
	t.Cleanup(release)
	starter := newProviderInvocationStarter()
	starter.invoke = func(ctx context.Context, _ extensions.Extension, _ VersionedProviderRequest) (VersionedProviderResponse, error) {
		close(started)
		<-ctx.Done()
		close(deadlineObserved)
		<-releaseInvocation
		return VersionedProviderResponse{Output: map[string]any{"status": "late"}}, nil
	}
	manager := NewManager(ManagerConfig{Starter: starter})
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	owner.Manifest.Providers[0].Fallback = "closed"
	owner.Manifest.Providers[0].TimeoutMS = 20
	if err := manager.Start(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	type providerResult struct {
		result VersionedProviderInvocationResult
		err    error
	}
	finished := make(chan providerResult, 1)
	go func() {
		result, invokeErr := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
			SlotID: providerSlotID, ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
			InputSchema: providerRequestSchema, Input: map[string]any{},
			Revalidate: func(context.Context, string, map[string]any) error { return nil },
		})
		finished <- providerResult{result: result, err: invokeErr}
	}()
	waitProviderExecutionSignal(t, started, "provider invocation start")
	waitProviderExecutionSignal(t, deadlineObserved, "provider deadline")
	select {
	case outcome := <-finished:
		t.Fatalf("non-cooperative invocation returned before it exited: %#v", outcome)
	default:
	}
	snapshot, err := manager.InspectRuntimeInstance(active.Identity)
	if err != nil || snapshot.Admission.ActiveTotal != 1 ||
		snapshot.Admission.ActiveByClass[RuntimeCallProvider] != 1 {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	if resilience := manager.resilience.snapshot(owner.ID); resilience.ActiveCalls != 1 {
		t.Fatalf("resilience while blocked=%#v", resilience)
	}
	if _, err := manager.BeginDrain(active.Identity); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.WaitDrain(waitCtx, active.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain completed before invocation exit: %v", err)
	}
	release()
	select {
	case outcome := <-finished:
		if !errors.Is(outcome.err, context.DeadlineExceeded) || outcome.result.Attempts != 1 {
			t.Fatalf("timeout result=%#v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider invocation did not finish after release")
	}
	finalDrainCtx, finalDrainCancel := context.WithTimeout(context.Background(), time.Second)
	defer finalDrainCancel()
	if err := manager.WaitDrain(finalDrainCtx, active.Identity); err != nil {
		t.Fatal(err)
	}
	if resilience := manager.resilience.snapshot(owner.ID); resilience.ActiveCalls != 0 {
		t.Fatalf("resilience after exit=%#v", resilience)
	}
}

func TestManagerVersionedProviderTimedOutCallBlocksExactDisableUntilExit(t *testing.T) {
	started := make(chan struct{})
	deadlineObserved := make(chan struct{})
	releaseInvocation := make(chan struct{})
	invocationExited := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseInvocation) }) }
	t.Cleanup(release)

	starter := newProviderInvocationStarter()
	starter.invoke = func(ctx context.Context, _ extensions.Extension, _ VersionedProviderRequest) (VersionedProviderResponse, error) {
		close(started)
		<-ctx.Done()
		close(deadlineObserved)
		<-releaseInvocation
		close(invocationExited)
		return VersionedProviderResponse{Output: map[string]any{"status": "late"}}, nil
	}
	manager := NewManager(ManagerConfig{Starter: starter})
	owner := versionedProviderExtension("providers.disable", strings.Repeat("a", 64), providerSlotDefinition(10))
	owner.Manifest.Providers[0].Fallback = "closed"
	owner.Manifest.Providers[0].TimeoutMS = 20
	if err := manager.Start(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(owner.ID)
	if err != nil {
		t.Fatal(err)
	}

	type providerResult struct {
		result VersionedProviderInvocationResult
		err    error
	}
	finished := make(chan providerResult, 1)
	go func() {
		result, invokeErr := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
			SlotID: providerSlotID, ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
			InputSchema: providerRequestSchema, Input: map[string]any{},
			Revalidate: func(context.Context, string, map[string]any) error { return nil },
		})
		finished <- providerResult{result: result, err: invokeErr}
	}()
	waitProviderExecutionSignal(t, started, "provider invocation start")
	waitProviderExecutionSignal(t, deadlineObserved, "provider deadline")

	if _, err := manager.BeginDrain(active.Identity); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := manager.WaitDrain(waitCtx, active.Identity); !errors.Is(err, context.DeadlineExceeded) {
		cancelWait()
		t.Fatalf("disable drain completed before provider exit: %v", err)
	}
	cancelWait()
	if err := manager.StopRuntimeInstance(context.Background(), active.Identity); !errors.Is(err, ErrRuntimeInstanceBusy) {
		t.Fatalf("disable stopped a running exact provider: %v", err)
	}
	if current, err := manager.ActiveRuntimeInstance(owner.ID); err != nil || current.Identity != active.Identity {
		t.Fatalf("blocked disable changed active runtime: %#v, %v", current, err)
	}

	release()
	waitProviderExecutionSignal(t, invocationExited, "provider invocation exit")
	select {
	case outcome := <-finished:
		if !errors.Is(outcome.err, context.DeadlineExceeded) || outcome.result.Attempts != 1 {
			t.Fatalf("timeout result=%#v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider invocation did not finish after release")
	}
	finalDrainCtx, cancelFinalDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelFinalDrain()
	if err := manager.WaitDrain(finalDrainCtx, active.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopRuntimeInstance(context.Background(), active.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ActiveRuntimeInstance(owner.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("disabled provider remained active: %v", err)
	}
}

func TestManagerVersionedProviderTimedOutCallBlocksStagedUpgradeUntilExit(t *testing.T) {
	firstStarted := make(chan struct{})
	firstDeadline := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstExited := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)

	versions := []string{}
	var versionsMu sync.Mutex
	starter := newProviderInvocationStarter()
	starter.invoke = func(ctx context.Context, extension extensions.Extension, _ VersionedProviderRequest) (VersionedProviderResponse, error) {
		versionsMu.Lock()
		versions = append(versions, extension.Version)
		versionsMu.Unlock()
		switch extension.Version {
		case "1.0.0":
			close(firstStarted)
			<-ctx.Done()
			close(firstDeadline)
			<-releaseFirst
			close(firstExited)
			return VersionedProviderResponse{Output: map[string]any{"status": "late"}}, nil
		case "2.0.0":
			select {
			case <-firstExited:
			default:
				return VersionedProviderResponse{}, errors.New("replacement provider overlapped source invocation")
			}
			return VersionedProviderResponse{Output: map[string]any{"status": "upgraded"}}, nil
		default:
			return VersionedProviderResponse{}, errors.New("unexpected provider version")
		}
	}
	manager := NewManager(ManagerConfig{Starter: starter})
	v1 := versionedProviderExtension("providers.upgrade", strings.Repeat("a", 64), providerSlotDefinition(10))
	v1.Manifest.Providers[0].Fallback = "closed"
	v1.Manifest.Providers[0].TimeoutMS = 20
	if err := manager.Start(context.Background(), v1); err != nil {
		t.Fatal(err)
	}
	source, err := manager.ActiveRuntimeInstance(v1.ID)
	if err != nil {
		t.Fatal(err)
	}

	type providerResult struct {
		result VersionedProviderInvocationResult
		err    error
	}
	firstFinished := make(chan providerResult, 1)
	go func() {
		result, invokeErr := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
			SlotID: providerSlotID, ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
			InputSchema: providerRequestSchema, Input: map[string]any{},
			Revalidate: func(context.Context, string, map[string]any) error { return nil },
		})
		firstFinished <- providerResult{result: result, err: invokeErr}
	}()
	waitProviderExecutionSignal(t, firstStarted, "source provider start")
	waitProviderExecutionSignal(t, firstDeadline, "source provider deadline")

	v2 := versionedProviderExtension(v1.ID, strings.Repeat("b", 64), providerSlotDefinition(10))
	v2.Version = "2.0.0"
	v2.Manifest.Version = "2.0.0"
	v2.Manifest.Providers[0].Fallback = "closed"
	replacement, err := manager.StageRuntimeInstance(context.Background(), v2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(context.Background(), replacement.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishRuntimeInstance(context.Background(), replacement.Identity); !errors.Is(err, ErrRuntimeInstanceNotDrained) {
		t.Fatalf("published replacement before source drain: %v", err)
	}
	if _, err := manager.BeginDrain(source.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishRuntimeInstance(context.Background(), replacement.Identity); !errors.Is(err, ErrRuntimeInstanceNotDrained) {
		t.Fatalf("published replacement while source provider was running: %v", err)
	}
	current, err := manager.ActiveRuntimeInstance(v1.ID)
	if err != nil || current.Identity != source.Identity || current.ExtensionVersion != "1.0.0" {
		t.Fatalf("blocked upgrade changed active runtime: %#v, %v", current, err)
	}
	resolution, err := manager.HookBus().ProviderSlots().Discover(ProviderSlotCaller{}, providerSlotID, providerContractVersion)
	if err != nil || resolution.Contract.Artifact.ExtensionVersion != "1.0.0" ||
		resolution.Contract.Artifact.RuntimeInstanceID != source.Identity.InstanceID {
		t.Fatalf("blocked upgrade changed provider registry: %#v, %v", resolution, err)
	}

	release()
	waitProviderExecutionSignal(t, firstExited, "source provider exit")
	select {
	case outcome := <-firstFinished:
		if !errors.Is(outcome.err, context.DeadlineExceeded) || outcome.result.Attempts != 1 {
			t.Fatalf("source timeout result=%#v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("source provider invocation did not finish after release")
	}
	finalDrainCtx, cancelFinalDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelFinalDrain()
	if err := manager.WaitDrain(finalDrainCtx, source.Identity); err != nil {
		t.Fatal(err)
	}
	published, err := manager.PublishRuntimeInstance(context.Background(), replacement.Identity)
	if err != nil || published.Identity != replacement.Identity || published.ExtensionVersion != "2.0.0" || !published.Active {
		t.Fatalf("published replacement=%#v err=%v", published, err)
	}

	upgradedResult, err := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
		SlotID: providerSlotID, ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
		InputSchema: providerRequestSchema, Input: map[string]any{},
		Revalidate: func(context.Context, string, map[string]any) error { return nil },
	})
	if err != nil || upgradedResult.ExtensionID != v2.ID || upgradedResult.RuntimeInstanceID != replacement.Identity.InstanceID ||
		upgradedResult.Output["status"] != "upgraded" {
		t.Fatalf("upgraded provider result=%#v err=%v", upgradedResult, err)
	}
	versionsMu.Lock()
	gotVersions := append([]string(nil), versions...)
	versionsMu.Unlock()
	if !reflect.DeepEqual(gotVersions, []string{"1.0.0", "2.0.0"}) {
		t.Fatalf("provider invocation versions=%#v", gotVersions)
	}
	if err := manager.StopRuntimeInstance(context.Background(), source.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(replacement.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.WaitDrain(context.Background(), replacement.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopRuntimeInstance(context.Background(), replacement.Identity); err != nil {
		t.Fatal(err)
	}
}

func TestManagerVersionedProviderWaitsForTimedOutCandidateBeforeFallback(t *testing.T) {
	firstStarted := make(chan struct{})
	firstDeadline := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstExited := make(chan struct{})
	fallbackStarted := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)
	starter := newProviderInvocationStarter()
	starter.invoke = func(ctx context.Context, extension extensions.Extension, _ VersionedProviderRequest) (VersionedProviderResponse, error) {
		starter.record(extension.ID)
		if extension.ID == "providers.consumer" {
			close(firstStarted)
			<-ctx.Done()
			close(firstDeadline)
			<-releaseFirst
			close(firstExited)
			return VersionedProviderResponse{Output: map[string]any{"status": "late"}}, nil
		}
		select {
		case <-firstExited:
		default:
			return VersionedProviderResponse{}, errors.New("fallback overlapped first provider")
		}
		close(fallbackStarted)
		return VersionedProviderResponse{Output: map[string]any{"status": "fallback"}}, nil
	}
	manager := NewManager(ManagerConfig{Starter: starter})
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	owner.Manifest.Providers[0].Fallback = "next"
	owner.Manifest.Providers[0].TimeoutMS = 20
	consumer := versionedProviderExtension("providers.consumer", strings.Repeat("b", 64), providerSlotConsumer("providers.consumer.delivery", 50))
	consumer.Manifest.Providers[0].Fallback = "next"
	consumer.Manifest.Providers[0].TimeoutMS = 20
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^1.0.0", Kind: "required"}}
	if err := manager.Start(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
	consumerRuntime, err := manager.ActiveRuntimeInstance(consumer.ID)
	if err != nil {
		t.Fatal(err)
	}
	caller := providerCaller(t, manager, consumer)
	type providerResult struct {
		result VersionedProviderInvocationResult
		err    error
	}
	finished := make(chan providerResult, 1)
	go func() {
		result, invokeErr := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
			Caller: caller, SlotID: providerSlotID,
			ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
			InputSchema: providerRequestSchema, Input: map[string]any{},
			Revalidate: func(context.Context, string, map[string]any) error { return nil },
		})
		finished <- providerResult{result: result, err: invokeErr}
	}()
	waitProviderExecutionSignal(t, firstStarted, "first provider start")
	waitProviderExecutionSignal(t, firstDeadline, "first provider deadline")
	select {
	case <-fallbackStarted:
		t.Fatal("fallback overlapped a timed-out provider that was still running")
	case <-time.After(30 * time.Millisecond):
	}
	snapshot, err := manager.InspectRuntimeInstance(consumerRuntime.Identity)
	if err != nil || snapshot.Admission.ActiveTotal != 1 ||
		snapshot.Admission.ActiveByClass[RuntimeCallProvider] != 1 {
		t.Fatalf("consumer snapshot=%#v err=%v", snapshot, err)
	}
	if resilience := manager.resilience.snapshot(consumer.ID); resilience.ActiveCalls != 1 {
		t.Fatalf("consumer resilience while blocked=%#v", resilience)
	}
	release()
	select {
	case outcome := <-finished:
		if outcome.err != nil || outcome.result.ExtensionID != owner.ID ||
			outcome.result.Attempts != 2 || outcome.result.Output["status"] != "fallback" {
			t.Fatalf("fallback result=%#v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider fallback did not finish")
	}
	waitProviderExecutionSignal(t, fallbackStarted, "fallback start")
	if got := starter.calls(); !reflect.DeepEqual(got, []string{consumer.ID, owner.ID}) {
		t.Fatalf("provider calls=%#v", got)
	}
	snapshot, err = manager.InspectRuntimeInstance(consumerRuntime.Identity)
	if err != nil || snapshot.Admission.ActiveTotal != 0 {
		t.Fatalf("final consumer snapshot=%#v err=%v", snapshot, err)
	}
	if resilience := manager.resilience.snapshot(consumer.ID); resilience.ActiveCalls != 0 {
		t.Fatalf("final consumer resilience=%#v", resilience)
	}
}

func waitProviderExecutionSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func TestManagerVersionedProviderDoesNotInvokeAfterRequestValidationDeadline(t *testing.T) {
	starter := newProviderInvocationStarter()
	starter.invoke = func(_ context.Context, extension extensions.Extension, _ VersionedProviderRequest) (VersionedProviderResponse, error) {
		starter.record(extension.ID)
		return VersionedProviderResponse{Output: map[string]any{"status": "unexpected"}}, nil
	}
	manager := NewManager(ManagerConfig{Starter: starter})
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	owner.Manifest.Providers[0].Fallback = "closed"
	owner.Manifest.Providers[0].TimeoutMS = 20
	if err := manager.Start(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	result, err := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
		SlotID: providerSlotID, ContractVersion: providerContractVersion,
		Operation: VersionedProviderOperationInvoke, InputSchema: providerRequestSchema,
		Input: map[string]any{}, Revalidate: func(ctx context.Context, schema string, _ map[string]any) error {
			if schema == providerRequestSchema {
				<-ctx.Done()
				return context.Cause(ctx)
			}
			return nil
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) || result.Attempts != 1 || len(starter.calls()) != 0 {
		t.Fatalf("result=%#v calls=%#v err=%v", result, starter.calls(), err)
	}
}

func TestManagerVersionedProviderDoesNotPublishAfterResponseValidationDeadline(t *testing.T) {
	starter := newProviderInvocationStarter()
	starter.invoke = func(_ context.Context, extension extensions.Extension, _ VersionedProviderRequest) (VersionedProviderResponse, error) {
		starter.record(extension.ID)
		return VersionedProviderResponse{Output: map[string]any{"status": "late"}}, nil
	}
	manager := NewManager(ManagerConfig{Starter: starter})
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	owner.Manifest.Providers[0].Fallback = "closed"
	owner.Manifest.Providers[0].TimeoutMS = 20
	if err := manager.Start(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	result, err := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
		SlotID: providerSlotID, ContractVersion: providerContractVersion,
		Operation: VersionedProviderOperationInvoke, InputSchema: providerRequestSchema,
		Input: map[string]any{}, Revalidate: func(ctx context.Context, schema string, _ map[string]any) error {
			if schema == providerResponseSchema {
				<-ctx.Done()
				return context.Cause(ctx)
			}
			return nil
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) || result.Attempts != 1 ||
		!reflect.DeepEqual(starter.calls(), []string{owner.ID}) {
		t.Fatalf("result=%#v calls=%#v err=%v", result, starter.calls(), err)
	}
}

func TestManagerVersionedProviderRequiresHostRevalidatorAndExactInstance(t *testing.T) {
	manager := NewManager(ManagerConfig{Starter: newProviderInvocationStarter()})
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	if err := manager.Start(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
		SlotID: providerSlotID, ContractVersion: providerContractVersion, Operation: VersionedProviderOperationInvoke,
		InputSchema: providerRequestSchema,
	}); !errors.Is(err, ErrProviderSlotInvalid) {
		t.Fatalf("missing Host revalidator = %v", err)
	}
	if _, err := manager.InvokeVersionedProvider(context.Background(), VersionedProviderInvocation{
		SlotID: providerSlotID, ContractVersion: providerContractVersion, Operation: "undeclared",
		InputSchema: providerRequestSchema,
		Revalidate:  func(context.Context, string, map[string]any) error { return nil },
	}); !errors.Is(err, ErrProviderSlotInvalid) {
		t.Fatalf("undeclared operation = %v", err)
	}

	emptyInstanceManager := NewManager(ManagerConfig{Starter: fakeStarter{}})
	if err := emptyInstanceManager.Start(context.Background(), owner); !errors.Is(err, ErrVersionedRuntimeContractInvalid) {
		t.Fatalf("provider-only empty instance = %v", err)
	}
	if snapshot := emptyInstanceManager.HookBus().ProviderSlots().Snapshot(); len(snapshot.Contracts) != 0 {
		t.Fatalf("invalid runtime published providers = %#v", snapshot)
	}
}

type providerInvocationStarter struct {
	*managerStagedStarter
	mu     sync.Mutex
	called []string
	invoke func(context.Context, extensions.Extension, VersionedProviderRequest) (VersionedProviderResponse, error)
	probe  func(context.Context, string, ProviderProbeRequest) (ProviderProbeResponse, error)
}

func newProviderInvocationStarter() *providerInvocationStarter {
	return &providerInvocationStarter{managerStagedStarter: newManagerStagedStarter()}
}

func (s *providerInvocationStarter) InvokeVersionedProvider(ctx context.Context, extension extensions.Extension, request VersionedProviderRequest) (VersionedProviderResponse, error) {
	if s.invoke == nil {
		return VersionedProviderResponse{}, errors.New("provider invoker is not configured")
	}
	return s.invoke(ctx, extension, request)
}

func (s *providerInvocationStarter) record(extensionID string) {
	s.mu.Lock()
	s.called = append(s.called, extensionID)
	s.mu.Unlock()
}

func (s *providerInvocationStarter) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.called...)
}

func startProviderOwnerAndConsumer(t *testing.T, manager *Manager, fallback string) (extensions.Extension, extensions.Extension) {
	t.Helper()
	owner := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	owner.Manifest.Providers[0].Fallback = fallback
	consumer := versionedProviderExtension("providers.consumer", strings.Repeat("b", 64), providerSlotConsumer("providers.consumer.delivery", 50))
	consumer.Manifest.Providers[0].Fallback = fallback
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^1.0.0", Kind: "required"}}
	if err := manager.Start(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
	return owner, consumer
}

func providerCaller(t *testing.T, manager *Manager, extension extensions.Extension) ProviderSlotCaller {
	t.Helper()
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ProviderSlotCaller{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version, ArtifactDigest: extension.PackageDigest,
		RuntimeInstanceID: active.Identity.InstanceID, Attested: true,
	}
}
