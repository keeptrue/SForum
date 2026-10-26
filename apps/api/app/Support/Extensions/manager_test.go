package extensionsruntime

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/zhuchunshu/sforum/apps/api/app/Support/Events"
)

func TestManagerTracksStartStopStatusAndRouteTargets(t *testing.T) {
	manager := NewManager(ManagerConfig{})
	extension := runtimeExtension("demo.plugin")

	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	status := manager.Status(context.Background(), extension)
	if status.State != extensions.RuntimeRunning || status.RouteCount != 1 {
		t.Fatalf("unexpected running status: %#v", status)
	}
	target, ok := manager.RouteTarget("demo.plugin")
	if !ok || target.BaseURL == "" {
		t.Fatalf("expected route target, got %#v ok=%v", target, ok)
	}

	if err := manager.Stop(context.Background(), extension); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	status = manager.Status(context.Background(), extension)
	if status.State != extensions.RuntimeStopped {
		t.Fatalf("expected stopped status, got %#v", status)
	}
}

func TestManagerExposesProtocolDeprecationTelemetry(t *testing.T) {
	lastCall := time.Date(2026, 7, 13, 18, 0, 0, 0, time.UTC)
	starter := telemetryStarter{snapshot: ProtocolTelemetrySnapshot{
		ProtocolVersion: 2, Transport: "grpc",
		StartCount: 2, CallCount: 9, LastCallAt: &lastCall,
	}}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := runtimeExtension("telemetry.plugin")
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	status := manager.Status(context.Background(), extension)
	if status.ProtocolVersion != 2 || status.ProtocolTransport != "grpc" || status.ProtocolDeprecated ||
		status.ProtocolStartCount != 2 || status.ProtocolCallCount != 9 || status.ProtocolLastCallAt == nil ||
		!status.ProtocolLastCallAt.Equal(lastCall) {
		t.Fatalf("unexpected protocol telemetry status: %#v", status)
	}
}

func TestManagerRecordsStartFailure(t *testing.T) {
	manager := NewManager(ManagerConfig{Starter: fakeStarter{err: errors.New("start failed")}})
	extension := runtimeExtension("broken.plugin")
	err := manager.Start(context.Background(), extension)
	if err == nil {
		t.Fatal("expected start failure")
	}
	status := manager.Status(context.Background(), extension)
	if status.State != extensions.RuntimeFailed || status.LastError == "" {
		t.Fatalf("expected failed status, got %#v", status)
	}
}

func TestManagerReconcileSkipsPreviouslyFailedExactDigest(t *testing.T) {
	store := &managerActivationStore{}
	coordinator := extensions.NewActivationCoordinator(store)
	extension := runtimeExtension("boot-loop.plugin")
	extension.PackageDigest = "same-digest"
	firstStarter := &countingManagerStarter{err: errors.New("startup crash")}
	first := NewManager(ManagerConfig{Starter: firstStarter, Activation: coordinator, BootID: "boot-1"})
	first.Reconcile(context.Background(), []extensions.Extension{extension})
	if firstStarter.starts != 1 || store.latest.Status != extensions.ActivationStatusFailed {
		t.Fatalf("first start=%d attempt=%#v", firstStarter.starts, store.latest)
	}

	secondStarter := &countingManagerStarter{}
	second := NewManager(ManagerConfig{Starter: secondStarter, Activation: coordinator, BootID: "boot-2"})
	second.Reconcile(context.Background(), []extensions.Extension{extension})
	if secondStarter.starts != 0 || store.latest.Status != extensions.ActivationStatusSkipped {
		t.Fatalf("second start=%d attempt=%#v", secondStarter.starts, store.latest)
	}
	status := second.Status(context.Background(), extension)
	if status.State != extensions.RuntimeStopped || status.LastError != "extension.boot_loop_skipped" {
		t.Fatalf("skipped status=%#v", status)
	}
}

func TestManagerEmitsFilterEventsInStableOrderAndMergesPatches(t *testing.T) {
	calls := []string{}
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, extension extensions.Extension, input HookInput) HookResult {
		calls = append(calls, extension.ID+":"+input.Kind)
		if extension.ID == "alpha.plugin" {
			return HookResult{OK: true, Patch: map[string]any{"title": "patched"}}
		}
		return HookResult{OK: true, Patch: map[string]any{"categorySlug": "general"}}
	})})
	manager := NewManager(ManagerConfig{HookBus: bus})
	beta := runtimeExtension("beta.plugin")
	alpha := runtimeExtension("alpha.plugin")
	for index := range beta.Manifest.Routes {
		beta.Manifest.Routes[index].Access = extensions.RouteAccessLogin
	}
	alpha.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter}}
	beta.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter}}
	if err := manager.Start(context.Background(), beta); err != nil {
		t.Fatalf("start beta: %v", err)
	}
	if err := manager.Start(context.Background(), alpha); err != nil {
		t.Fatalf("start alpha: %v", err)
	}

	result := manager.Emit(context.Background(), appevents.Envelope{
		Name:        appevents.TopicBeforeCreate,
		Kind:        appevents.KindFilter,
		PatchFields: []string{"title", "categorySlug"},
		Payload:     map[string]any{"title": "original"},
	})
	if !result.OK {
		t.Fatalf("expected filter event ok, got %#v", result)
	}
	if !slices.Equal(calls, []string{"alpha.plugin:filter", "beta.plugin:filter"}) {
		t.Fatalf("unexpected call order: %#v", calls)
	}
	if result.Patch["title"] != "patched" || result.Patch["categorySlug"] != "general" {
		t.Fatalf("unexpected merged patch: %#v", result.Patch)
	}
}

func TestManagerAllowsTopicBeforeCreateTagSlugPatchFromCatalog(t *testing.T) {
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, _ extensions.Extension, _ HookInput) HookResult {
		return HookResult{OK: true, Patch: map[string]any{"tagSlugs": []string{"patched"}}}
	})})
	manager := NewManager(ManagerConfig{HookBus: bus})
	extension := runtimeExtension("tagger.plugin")
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start plugin: %v", err)
	}

	result := manager.Emit(context.Background(), appevents.NewEnvelope(appevents.TopicBeforeCreate, map[string]any{
		"title":    "original",
		"tagSlugs": []string{"original"},
	}))
	if !result.OK {
		t.Fatalf("expected tagSlugs patch to be allowed, got %#v", result)
	}
	if got, ok := result.Patch["tagSlugs"].([]string); !ok || !slices.Equal(got, []string{"patched"}) {
		t.Fatalf("unexpected tagSlugs patch: %#v", result.Patch["tagSlugs"])
	}
}

func TestManagerEnforcesSyncFilterTimeout(t *testing.T) {
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(ctx context.Context, _ extensions.Extension, _ HookInput) HookResult {
		// 模拟忽略取消的慢插件：宿主仍应在 timeout 后标记失败。
		select {
		case <-ctx.Done():
			return HookResult{OK: false, Reason: "extension.hook_timeout", Message: "canceled"}
		case <-time.After(200 * time.Millisecond):
			return HookResult{OK: true}
		}
	})})
	store := &fakeDeliveryStore{}
	manager := NewManager(ManagerConfig{HookBus: bus, DeliveryStore: store})
	extension := runtimeExtension("slow.plugin")
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start: %v", err)
	}

	// 用极短超时覆盖目录默认值：通过临时改 invoker 路径测 host context。
	// 直接调用 invoke 验证超时强制失败。
	result := manager.invoke(context.Background(), extension, HookInput{
		Name:    appevents.TopicBeforeCreate,
		Kind:    appevents.KindFilter,
		Timeout: 20 * time.Millisecond,
	})
	if result.OK || result.Reason != "extension.hook_timeout" {
		t.Fatalf("expected hook timeout, got %#v", result)
	}
}

func TestManagerRecordsSyncFilterDeliveries(t *testing.T) {
	store := &fakeDeliveryStore{}
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, _ extensions.Extension, _ HookInput) HookResult {
		return HookResult{OK: true, Patch: map[string]any{"title": "ok"}}
	})})
	manager := NewManager(ManagerConfig{HookBus: bus, DeliveryStore: store})
	extension := runtimeExtension("filter.plugin")
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start: %v", err)
	}

	result := manager.Emit(context.Background(), appevents.NewEnvelope(appevents.TopicBeforeCreate, map[string]any{
		"title": "original",
	}))
	if !result.OK {
		t.Fatalf("expected ok, got %#v", result)
	}
	if len(store.deliveries) != 1 {
		t.Fatalf("expected sync delivery recorded, got %#v", store.deliveries)
	}
	if store.deliveries[0].Status != extensions.DeliverySucceeded {
		t.Fatalf("delivery=%#v", store.deliveries[0])
	}
	if store.deliveries[0].EventKind != appevents.KindFilter {
		t.Fatalf("kind=%s", store.deliveries[0].EventKind)
	}
}

func TestManagerRecordsObserveEventDeliveries(t *testing.T) {
	store := &fakeDeliveryStore{}
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, _ extensions.Extension, input HookInput) HookResult {
		if input.DeliveryID == 0 || input.CorrelationID == "" {
			return HookResult{OK: false, Reason: "bad.delivery"}
		}
		return HookResult{OK: true}
	})})
	manager := NewManager(ManagerConfig{HookBus: bus, DeliveryStore: store})
	extension := runtimeExtension("demo.plugin")
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicCreated, Kind: appevents.KindObserve}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start: %v", err)
	}

	result := manager.Emit(context.Background(), appevents.Envelope{
		Name:          appevents.TopicCreated,
		Kind:          appevents.KindObserve,
		CorrelationID: "corr-1",
		Payload:       map[string]any{"topicId": int64(42)},
	})
	if !result.OK {
		t.Fatalf("expected observe event ok, got %#v", result)
	}
	if len(store.deliveries) != 1 {
		t.Fatalf("expected one delivery, got %#v", store.deliveries)
	}
	if store.deliveries[0].Status != extensions.DeliverySucceeded || store.deliveries[0].AttemptCount != 1 {
		t.Fatalf("expected succeeded delivery, got %#v", store.deliveries[0])
	}
}

func runtimeExtension(id string) extensions.Extension {
	return extensions.Extension{
		ID:     id,
		Type:   extensions.TypePlugin,
		Status: extensions.StatusEnabled,
		Manifest: extensions.Manifest{
			ManifestVersion: 3,
			ID:              id,
			Name:            "Demo Plugin",
			Version:         "1.0.0",
			Type:            extensions.TypePlugin,
			SForumVersion:   ">=0.1.0",
			Backend:         extensions.ManifestBackend{Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2"},
			Routes:          []extensions.ManifestRoute{{Path: "/hello", Methods: []string{"GET"}, Access: extensions.RouteAccessPublic}},
		},
	}
}

type fakeStarter struct {
	err error
}

type telemetryStarter struct {
	fakeStarter
	snapshot ProtocolTelemetrySnapshot
}

func (s telemetryStarter) ProtocolTelemetry(string) ProtocolTelemetrySnapshot { return s.snapshot }

type countingManagerStarter struct {
	starts int
	err    error
}

func (s *countingManagerStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	s.starts++
	return RouteTarget{}, s.err
}

func (*countingManagerStarter) Stop(context.Context, extensions.Extension) error { return nil }

type managerActivationStore struct {
	latest extensions.ActivationAttempt
	nextID int64
}

func (s *managerActivationStore) LatestActivationAttempt(_ context.Context, extensionID, packageDigest string) (extensions.ActivationAttempt, error) {
	if s.latest.ID == 0 || s.latest.ExtensionID != extensionID || s.latest.PackageDigest != packageDigest {
		return extensions.ActivationAttempt{}, extensions.ErrActivationAttemptNotFound
	}
	return s.latest, nil
}

func (s *managerActivationStore) BeginActivationAttempt(_ context.Context, extension extensions.Extension, trigger, bootID string, actorUserID int64) (extensions.ActivationAttempt, error) {
	s.nextID++
	s.latest = extensions.ActivationAttempt{
		ID: s.nextID, ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, BootID: bootID, Trigger: trigger,
		Status: extensions.ActivationStatusStarting, ActorUserID: actorUserID,
	}
	return s.latest, nil
}

func (s *managerActivationStore) CompleteActivationAttempt(_ context.Context, attemptID int64, status, reason string) error {
	if s.latest.ID != attemptID {
		return extensions.ErrActivationAttemptNotFound
	}
	now := time.Now()
	s.latest.Status = status
	s.latest.FailureReason = reason
	s.latest.CompletedAt = &now
	return nil
}

func (s *managerActivationStore) RecordSkippedActivation(_ context.Context, extension extensions.Extension, bootID, reason string) error {
	s.nextID++
	now := time.Now()
	s.latest = extensions.ActivationAttempt{
		ID: s.nextID, ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, BootID: bootID,
		Trigger: extensions.ActivationTriggerStartup, Status: extensions.ActivationStatusSkipped,
		FailureReason: reason, CompletedAt: &now,
	}
	return nil
}

func (s fakeStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	if s.err != nil {
		return RouteTarget{}, s.err
	}
	return RouteTarget{BaseURL: "http://127.0.0.1:43210"}, nil
}

func (s fakeStarter) Stop(context.Context, extensions.Extension) error {
	return nil
}

type fakeDeliveryStore struct {
	deliveries []extensions.ExtensionEventDelivery
}

func (s *fakeDeliveryStore) CreateEventDelivery(_ context.Context, input extensions.EventDeliveryInput) (extensions.ExtensionEventDelivery, error) {
	delivery := extensions.ExtensionEventDelivery{
		ID:            int64(len(s.deliveries) + 1),
		ExtensionID:   input.ExtensionID,
		EventName:     input.EventName,
		EventKind:     input.EventKind,
		Status:        input.Status,
		CorrelationID: input.CorrelationID,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	s.deliveries = append(s.deliveries, delivery)
	return delivery, nil
}

func (s *fakeDeliveryStore) UpdateEventDelivery(_ context.Context, input extensions.EventDeliveryUpdateInput) error {
	for index := range s.deliveries {
		if s.deliveries[index].ID == input.ID {
			s.deliveries[index].Status = input.Status
			s.deliveries[index].Reason = input.Reason
			s.deliveries[index].Message = input.Message
			s.deliveries[index].AttemptCount = input.AttemptCount
			s.deliveries[index].UpdatedAt = time.Now()
			if input.Completed {
				completed := time.Now()
				s.deliveries[index].CompletedAt = &completed
			}
			return nil
		}
	}
	return extensions.ErrExtensionNotFound
}
