package extensionsruntime_test

import (
	"context"
	"errors"
	"testing"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

func TestManagerStagedRuntimeUsesRealProtocolV2Process(t *testing.T) {
	extension := p4LifecycleExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: p4LifecycleTrust{}})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})

	staged, err := manager.StageRuntimeInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	remove := func() {
		if snapshot, inspectErr := manager.InspectRuntimeInstance(staged.Identity); inspectErr == nil {
			if snapshot.Active {
				_, _ = manager.BeginDrain(staged.Identity)
				_ = manager.WaitDrain(context.Background(), staged.Identity)
				_ = manager.StopRuntimeInstance(context.Background(), staged.Identity)
			} else {
				_ = manager.DiscardRuntimeInstance(context.Background(), staged.Identity)
			}
		}
	}
	t.Cleanup(remove)

	protocolSnapshot, err := starter.InspectInstance(staged.Identity)
	if err != nil || protocolSnapshot.State != extensionsruntime.ProtocolRuntimeStaged || protocolSnapshot.Ready {
		t.Fatalf("staged protocol snapshot = %#v, %v", protocolSnapshot, err)
	}
	if _, err := manager.AcquireRuntimeCall(context.Background(), staged.Identity, extensionsruntime.RuntimeCallRoute); !errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotActive) {
		t.Fatalf("staged ordinary admission = %v", err)
	}
	healthy, err := manager.HealthRuntimeInstance(context.Background(), staged.Identity)
	if err != nil || !healthy.Healthy || !healthy.Ready || !healthy.ReadinessChecked {
		t.Fatalf("healthy protocol snapshot = %#v, %v", healthy, err)
	}
	published, err := manager.PublishRuntimeInstance(context.Background(), staged.Identity)
	if err != nil || !published.Active || published.Identity != staged.Identity {
		t.Fatalf("published Manager snapshot = %#v, %v", published, err)
	}
	lease, err := manager.AcquireRuntimeCall(context.Background(), staged.Identity, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if _, err := manager.BeginDrain(staged.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.WaitDrain(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := starter.InspectInstance(staged.Identity); !errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound) {
		t.Fatalf("stopped process remains: %v", err)
	}
}
