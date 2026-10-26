package extensionsruntime

import (
	"context"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
)

func TestManagerRouteDelegateDoesNotOpenSecondAdmissionLease(t *testing.T) {
	starter := &managerRouteStarter{target: RouteTarget{InstanceID: "runtime-1"}}
	manager := NewManager(ManagerConfig{Starter: starter})
	starter.manager = manager
	extension := managerRuntimeExtension("demo.plugin", "1.0.0", "digest-v1")
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	snapshot, lease, err := manager.AcquireActiveRuntimeCall(context.Background(), extension.ID, RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := manager.InvokeRouteInstance(lease.Context, snapshot.Identity, protocolV2RouteTestRequest()); err != nil {
		t.Fatal(err)
	}
	if starter.activeCalls != 1 || starter.calls != 1 {
		t.Fatalf("activeCalls=%d calls=%d", starter.activeCalls, starter.calls)
	}
}

func TestProtocolStarterRouteCallsDoNotHoldLifecycleLockAcrossGRPC(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		started <- struct{}{}
		<-release
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	identity := RuntimeInstanceIdentity{ExtensionID: "demo.plugin", InstanceID: "runtime-1"}
	starter := &ProtocolStarter{runtimeInstances: map[string]map[string]*protocolRuntimeInstance{
		identity.ExtensionID: {
			identity.InstanceID: {
				identity: identity, extensionVersion: "1.0.0", artifactDigest: "digest-v1",
				protocolVersion: 2, protocol: client,
			},
		},
	}}
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := starter.InvokeRouteInstance(context.Background(), identity, protocolV2RouteTestRequest())
			errors <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("concurrent route call was serialized behind lifecycle lock")
		}
	}
	close(release)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
}

type managerRouteStarter struct {
	manager     *Manager
	target      RouteTarget
	activeCalls int
	calls       int
}

func (s *managerRouteStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return s.target, nil
}

func (*managerRouteStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (s *managerRouteStarter) InvokeRouteInstance(
	_ context.Context,
	identity RuntimeInstanceIdentity,
	_ ProtocolV2RouteRequest,
) (ProtocolV2RouteResponse, error) {
	snapshot, err := s.manager.InspectRuntimeInstance(identity)
	if err != nil {
		return ProtocolV2RouteResponse{}, err
	}
	s.calls++
	s.activeCalls = snapshot.Admission.ActiveTotal
	return ProtocolV2RouteResponse{StatusCode: 204}, nil
}

var _ exactRouteInstanceInvoker = (*managerRouteStarter)(nil)
