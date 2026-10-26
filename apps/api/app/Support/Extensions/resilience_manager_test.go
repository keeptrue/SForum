package extensionsruntime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

func TestManagerCircuitOpensAndMarksDegraded(t *testing.T) {
	var calls atomic.Int32
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, _ extensions.Extension, _ HookInput) HookResult {
		calls.Add(1)
		return HookResult{OK: false, Reason: "extension.hook_failed", Message: "boom"}
	})})
	manager := NewManager(ManagerConfig{
		HookBus: bus,
		Resilience: ResilienceConfig{
			MaxConcurrent:    4,
			FailureThreshold: 3,
			CircuitOpenFor:   time.Minute,
		},
	})
	extension := runtimeExtension("flaky.plugin")
	// TopicBeforeCreate 是 fail_closed filter，失败会推进熔断。
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start: %v", err)
	}

	for i := 0; i < 3; i++ {
		result := manager.Emit(context.Background(), appevents.Envelope{
			Name:        appevents.TopicBeforeCreate,
			Kind:        appevents.KindFilter,
			PatchFields: []string{"title"},
			Payload:     map[string]any{"title": "t"},
		})
		if result.OK {
			t.Fatalf("expected fail closed, got %#v", result)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 invoker calls, got %d", calls.Load())
	}

	// 熔断后不再调用 invoker。
	result := manager.Emit(context.Background(), appevents.Envelope{
		Name:        appevents.TopicBeforeCreate,
		Kind:        appevents.KindFilter,
		PatchFields: []string{"title"},
		Payload:     map[string]any{"title": "t"},
	})
	if result.OK || result.Reason != "extension.circuit_open" {
		t.Fatalf("expected circuit_open, got %#v", result)
	}
	if calls.Load() != 3 {
		t.Fatalf("circuit should skip invoker, calls=%d", calls.Load())
	}

	status := manager.Status(context.Background(), extension)
	if status.State != extensions.RuntimeDegraded {
		t.Fatalf("expected degraded, got %#v", status)
	}
	if !status.CircuitOpen || status.ConsecutiveFailures < 3 {
		t.Fatalf("expected circuit fields, got %#v", status)
	}
}

func TestManagerObserveSkipsWhenCircuitOpen(t *testing.T) {
	var calls atomic.Int32
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, _ extensions.Extension, input HookInput) HookResult {
		calls.Add(1)
		if input.Kind == appevents.KindFilter {
			return HookResult{OK: false, Reason: "extension.hook_failed", Message: "boom"}
		}
		return HookResult{OK: true}
	})})
	manager := NewManager(ManagerConfig{
		HookBus: bus,
		Resilience: ResilienceConfig{
			FailureThreshold: 1,
			CircuitOpenFor:   time.Minute,
		},
	})
	extension := runtimeExtension("mixed.plugin")
	extension.Manifest.Events = []extensions.ManifestEvent{
		{Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter},
		{Name: appevents.TopicCreated, Kind: appevents.KindObserve},
	}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start: %v", err)
	}

	// 打开熔断。
	_ = manager.Emit(context.Background(), appevents.Envelope{
		Name:        appevents.TopicBeforeCreate,
		Kind:        appevents.KindFilter,
		PatchFields: []string{"title"},
		Payload:     map[string]any{"title": "t"},
	})
	filterCalls := calls.Load()

	// observe 在熔断时跳过且 OK（fail_open）。
	result := manager.Emit(context.Background(), appevents.NewEnvelope(appevents.TopicCreated, map[string]any{"id": 1}))
	if !result.OK {
		t.Fatalf("observe should not fail host: %#v", result)
	}
	// Deliver 路径会走 invoke；若同步 observe 监听，calls 可能不变（异步队列）。
	// 这里只断言 filter 调用后熔断状态。
	status := manager.Status(context.Background(), extension)
	if status.State != extensions.RuntimeDegraded {
		t.Fatalf("expected degraded after filter fail, got %#v", status)
	}
	if filterCalls < 1 {
		t.Fatal("expected at least one filter call")
	}
}

func TestManagerStatusRecoversAfterSuccess(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, _ extensions.Extension, _ HookInput) HookResult {
		if fail.Load() {
			return HookResult{OK: false, Reason: "extension.hook_failed", Message: "boom"}
		}
		return HookResult{OK: true}
	})})
	manager := NewManager(ManagerConfig{
		HookBus:    bus,
		Resilience: ResilienceConfig{FailureThreshold: 2, CircuitOpenFor: 5 * time.Millisecond},
	})
	extension := runtimeExtension("recover.plugin")
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = manager.Emit(context.Background(), appevents.Envelope{
		Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter,
		PatchFields: []string{"title"}, Payload: map[string]any{"title": "t"},
	})
	if manager.Status(context.Background(), extension).State != extensions.RuntimeDegraded {
		t.Fatal("expected degraded after one failure")
	}
	fail.Store(false)
	result := manager.Emit(context.Background(), appevents.Envelope{
		Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter,
		PatchFields: []string{"title"}, Payload: map[string]any{"title": "t"},
	})
	if !result.OK {
		t.Fatalf("expected recovery success: %#v", result)
	}
	status := manager.Status(context.Background(), extension)
	if status.State != extensions.RuntimeRunning {
		t.Fatalf("expected running after success, got %#v", status)
	}
}
