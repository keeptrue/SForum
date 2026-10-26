package extensionsruntime

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	"google.golang.org/grpc"
)

func TestManagerRouteStreamDelegateUsesCallersExactAdmissionLease(t *testing.T) {
	want := &ProtocolV2RouteStream{}
	starter := &managerRouteStreamStarter{
		managerRouteStarter: managerRouteStarter{target: RouteTarget{InstanceID: "runtime-1"}},
		stream:              want,
	}
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
	got, err := manager.OpenRouteStreamInstance(lease.Context, snapshot.Identity, protocolV2ManagerRouteStreamRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got != want || starter.activeCalls != 1 || starter.streamCalls != 1 {
		t.Fatalf("stream=%p want=%p activeCalls=%d calls=%d", got, want, starter.activeCalls, starter.streamCalls)
	}
	if _, err := manager.OpenRouteStreamInstance(lease.Context, RuntimeInstanceIdentity{
		ExtensionID: extension.ID, InstanceID: "missing-runtime",
	}, protocolV2ManagerRouteStreamRequest()); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("missing exact stream error=%v", err)
	}
}

func TestProtocolStarterRouteStreamRequiresExactRuntimeIdentity(t *testing.T) {
	client := newProtocolV2RouteStreamTestClient(t, func(stream grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		<-stream.Context().Done()
		return stream.Context().Err()
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
	stream, err := starter.OpenRouteStreamInstance(context.Background(), identity, protocolV2ManagerRouteStreamRequest())
	if err != nil {
		t.Fatal(err)
	}
	stream.Cancel()
	for name, mutate := range map[string]func(*protocolRuntimeInstance){
		"version": func(instance *protocolRuntimeInstance) { instance.extensionVersion = "2.0.0" },
		"digest":  func(instance *protocolRuntimeInstance) { instance.artifactDigest = "digest-v2" },
	} {
		t.Run(name, func(t *testing.T) {
			instance := starter.runtimeInstances[identity.ExtensionID][identity.InstanceID]
			originalVersion, originalDigest := instance.extensionVersion, instance.artifactDigest
			mutate(instance)
			_, err := starter.OpenRouteStreamInstance(context.Background(), identity, protocolV2ManagerRouteStreamRequest())
			instance.extensionVersion, instance.artifactDigest = originalVersion, originalDigest
			if !errors.Is(err, ErrProtocolV2RouteStreamInvalid) {
				t.Fatalf("identity drift error=%v", err)
			}
		})
	}
}

type managerRouteStreamStarter struct {
	managerRouteStarter
	stream      *ProtocolV2RouteStream
	streamCalls int
}

func (s *managerRouteStreamStarter) OpenRouteStreamInstance(
	_ context.Context,
	identity RuntimeInstanceIdentity,
	_ ProtocolV2RouteStreamRequest,
) (*ProtocolV2RouteStream, error) {
	snapshot, err := s.manager.InspectRuntimeInstance(identity)
	if err != nil {
		return nil, err
	}
	s.streamCalls++
	s.activeCalls = snapshot.Admission.ActiveTotal
	return s.stream, nil
}

func protocolV2ManagerRouteStreamRequest() ProtocolV2RouteStreamRequest {
	return ProtocolV2RouteStreamRequest{
		RouteID: "demo.stream", ContractVersion: "demo.stream@1", Method: http.MethodPost,
		Path: "/stream", Mode: extensionmanifest.RouteModeStream,
		Authority: protocolV2FilteredHostRequestAuthority(), Timeout: time.Second,
	}
}

var _ Starter = (*managerRouteStreamStarter)(nil)
var _ exactRouteStreamInstanceInvoker = (*managerRouteStreamStarter)(nil)
