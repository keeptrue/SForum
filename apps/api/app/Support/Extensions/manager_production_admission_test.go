package extensionsruntime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestManagerPluginJobUsesExactAdmissionAndDrainCancellation(t *testing.T) {
	starter := &admissionJobStarter{started: make(chan context.Context, 1)}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := pluginJobRuntimeExtension()
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := extensions.PluginJobContractForExtension(extension, "demo.sync")
	if err != nil {
		t.Fatal(err)
	}
	invocation := supportjobs.PluginJobInvocation{Contract: contract, TrustGrantID: "grant-1"}
	result := make(chan error, 1)
	go func() { result <- manager.ExecutePluginJob(context.Background(), invocation) }()

	select {
	case <-starter.started:
	case <-time.After(time.Second):
		t.Fatal("plugin job did not reach the runtime")
	}
	snapshot, err := manager.BeginDrain(active.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveByClass[RuntimeCallJob] != 1 {
		t.Fatalf("drain snapshot = %#v", snapshot)
	}
	if err := manager.ExecutePluginJob(context.Background(), invocation); !errors.Is(err, extensions.ErrRuntimeUnavailable) || !errors.Is(err, ErrRuntimeAdmissionDraining) {
		t.Fatalf("post-drain job error = %v", err)
	}

	cause := errors.New("forced job drain")
	if _, err := manager.ForceDrain(active.Identity, cause); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, cause) {
			t.Fatalf("cancelled job error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("forced drain did not cancel the plugin job")
	}
	if err := manager.WaitDrain(context.Background(), active.Identity); err != nil {
		t.Fatal(err)
	}
}

func TestManagerHookUsesAdmissionAndRejectsStaleListener(t *testing.T) {
	started := make(chan context.Context, 1)
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(ctx context.Context, _ extensions.Extension, _ HookInput) HookResult {
		started <- ctx
		<-ctx.Done()
		return HookResult{OK: false, Reason: "cancelled", Message: context.Cause(ctx).Error()}
	})})
	manager := NewManager(ManagerConfig{HookBus: bus})
	extension := runtimeExtension("admission.hook")
	extension.Version = extension.Manifest.Version
	extension.PackageDigest = "hook-digest"
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan appevents.Result, 1)
	go func() {
		result <- manager.Emit(context.Background(), appevents.Envelope{
			Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter,
			PatchFields: []string{"title"}, Payload: map[string]any{"title": "test"},
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("hook did not reach the runtime")
	}
	snapshot, err := manager.BeginDrain(active.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveByClass[RuntimeCallHook] != 1 {
		t.Fatalf("hook drain snapshot = %#v", snapshot)
	}
	blocked := manager.Emit(context.Background(), appevents.Envelope{
		Name: appevents.TopicBeforeCreate, Kind: appevents.KindFilter,
		PatchFields: []string{"title"}, Payload: map[string]any{"title": "blocked"},
	})
	if blocked.OK || blocked.Reason != "extension.runtime_unavailable" {
		t.Fatalf("post-drain hook = %#v", blocked)
	}
	if _, err := manager.ForceDrain(active.Identity, errors.New("forced hook drain")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("forced drain did not cancel the hook")
	}
}

func TestManagerProviderCallsFailClosedAfterDrain(t *testing.T) {
	starter := &admissionProviderStarter{}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerRuntimeExtension("admission.provider", "1.0.0", "provider-digest")
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(active.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SendMail(context.Background(), extension.ID, MailProviderRequest{}); !errors.Is(err, extensions.ErrRuntimeUnavailable) || !errors.Is(err, ErrRuntimeAdmissionDraining) {
		t.Fatalf("post-drain mail error = %v", err)
	}
	if _, err := manager.StorageStat(context.Background(), extension.ID, StorageStatRequest{}); !errors.Is(err, extensions.ErrRuntimeUnavailable) || !errors.Is(err, ErrRuntimeAdmissionDraining) {
		t.Fatalf("post-drain storage error = %v", err)
	}
	if _, err := manager.StorageClose(context.Background(), extension.ID, StorageCloseRequest{}); !errors.Is(err, extensions.ErrRuntimeUnavailable) || !errors.Is(err, ErrRuntimeAdmissionDraining) {
		t.Fatalf("post-drain storage close error = %v", err)
	}
	if starter.calls.Load() != 0 {
		t.Fatalf("provider transport calls after drain = %d", starter.calls.Load())
	}
}

type admissionJobStarter struct {
	started chan context.Context
}

func (s *admissionJobStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{InstanceID: "admission-job-instance"}, nil
}

func (*admissionJobStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (s *admissionJobStarter) ExecutePluginJob(ctx context.Context, _ supportjobs.PluginJobInvocation) error {
	s.started <- ctx
	<-ctx.Done()
	return context.Cause(ctx)
}

type admissionProviderStarter struct {
	StorageProviderInvoker
	calls atomic.Int32
}

func (*admissionProviderStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{InstanceID: "admission-provider-instance"}, nil
}

func (*admissionProviderStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (s *admissionProviderStarter) SendMail(context.Context, string, MailProviderRequest) (MailProviderResponse, error) {
	s.calls.Add(1)
	return MailProviderResponse{OK: true}, nil
}

func (s *admissionProviderStarter) StorageStat(context.Context, string, StorageStatRequest) (StorageStatResponse, error) {
	s.calls.Add(1)
	return StorageStatResponse{OK: true}, nil
}

func (s *admissionProviderStarter) StorageClose(context.Context, string, StorageCloseRequest) (StorageResult, error) {
	s.calls.Add(1)
	return StorageResult{OK: true}, nil
}
