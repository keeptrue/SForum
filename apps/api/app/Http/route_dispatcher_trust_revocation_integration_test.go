package http

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteWebSocketTrustRevocationDrainsExactProtocolV2Lease(t *testing.T) {
	tests := []struct {
		name          string
		commitUnknown bool
	}{
		{name: "success"},
		{name: "commit unknown", commitUnknown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extension := routeStreamE2EExtension(t)
			trust := &routeStreamMutableTrust{}
			trust.Set(extensions.RuntimeTrustIdentity{TrustGrantID: "stream-r1", ImpactDigest: "stream-impact-r1"})
			starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: trust})
			manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
			if err := manager.Start(t.Context(), extension); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { manager.Close(context.Background()) })
			r1, err := manager.ActiveRuntimeInstance(extension.ID)
			if err != nil {
				t.Fatal(err)
			}

			registry := routes.NewRegistry()
			publishRouteStreamRuntime(t, registry, extension, r1)
			dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
				Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(manager),
				Guard: HostRouteGuardAuthorizer{},
			})
			app := fiber.New(fiber.Config{StreamRequestBody: true, DisablePreParseMultipartForm: true})
			app.Use(routeDispatcherMiddleware(dispatcher, nil))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			serverDone := make(chan error, 1)
			go func() { serverDone <- app.Listener(listener) }()
			t.Cleanup(func() {
				_ = app.Shutdown()
				_ = listener.Close()
				<-serverDone
			})
			webSocketURL := "ws://" + listener.Addr().String() + "/socket"

			oldSocket := dialRouteStreamWebSocket(t, webSocketURL)
			t.Cleanup(func() { _ = oldSocket.Close() })
			assertExactRouteLease(t, manager, r1, false)

			// Deliberately start without a policy entry: the revocation fence must
			// carry the active runtime's exact artifact as the fallback token.
			policy := &routeStreamTrustPolicy{}
			fence := extensionsruntime.NewExecutableTrustRevocationFence(manager, policy)
			durableEntered := make(chan struct{})
			releaseDurable := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseDurable) }) }
			t.Cleanup(release)
			revokeDone := make(chan error, 1)
			go func() {
				revokeDone <- fence.RevokeExecutableTrust(
					t.Context(), extension.ID, "operator_revoked",
					func(ctx context.Context) error {
						close(durableEntered)
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-releaseDurable:
						}
						if test.commitUnknown {
							return errors.Join(extensions.ErrTrustRevocationCommitUnknown, errors.New("commit response lost"))
						}
						return nil
					},
				)
			}()
			awaitRouteStreamSignal(t, durableEntered, "durable revoke callback")
			assertExactRouteLease(t, manager, r1, true)
			select {
			case err := <-revokeDone:
				t.Fatalf("revoke returned before durable callback release: %v", err)
			default:
			}
			requireRouteStreamWebSocketRejected(t, webSocketURL)

			assertRouteStreamWebSocketEchoAndNormalClose(t, oldSocket, "old-runtime")
			waitExactRouteDrain(t, manager, r1)
			release()
			revokeErr := awaitRouteStreamResult(t, revokeDone, "trust revoke")
			if test.commitUnknown {
				if !errors.Is(revokeErr, extensions.ErrTrustRevocationCommitUnknown) {
					t.Fatalf("commit-unknown revoke error=%v", revokeErr)
				}
			} else if revokeErr != nil {
				t.Fatal(revokeErr)
			}
			if policy.Invalidations() != 1 {
				t.Fatalf("guard policy invalidations=%d", policy.Invalidations())
			}
			invalidated := policy.InvalidatedEntry()
			if invalidated.ExtensionID != extension.ID || invalidated.Version != r1.ExtensionVersion ||
				invalidated.PackageDigest != r1.ArtifactDigest || !invalidated.CurrentTrustRequired ||
				!invalidated.CurrentArtifactTrusted {
				t.Fatalf("guard policy fallback invalidation=%#v", invalidated)
			}
			revoked, err := manager.InspectRuntimeInstance(r1.Identity)
			if err != nil || !revoked.Admission.Draining || !revoked.Admission.Quarantined || revoked.Admission.ActiveTotal != 0 {
				t.Fatalf("revoked runtime=%#v err=%v", revoked, err)
			}
			requireRouteStreamWebSocketRejected(t, webSocketURL)

			trust.Set(extensions.RuntimeTrustIdentity{TrustGrantID: "stream-r3", ImpactDigest: "stream-impact-r3"})
			if err := manager.Start(t.Context(), extension); err != nil {
				t.Fatalf("start R+2 runtime: %v", err)
			}
			r3, err := manager.ActiveRuntimeInstance(extension.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r3.Identity == r1.Identity {
				t.Fatalf("reauthorization reused revoked runtime: %#v", r3.Identity)
			}
			publishRouteStreamRuntime(t, registry, extension, r3)
			newSocket := dialRouteStreamWebSocket(t, webSocketURL)
			t.Cleanup(func() { _ = newSocket.Close() })
			assertExactRouteLease(t, manager, r3, false)
			assertRouteStreamWebSocketEchoAndNormalClose(t, newSocket, "reauthorized-runtime")
			waitExactRouteDrain(t, manager, r3)
			if identities := trust.Resolutions(); len(identities) != 2 ||
				identities[0].TrustGrantID != "stream-r1" || identities[1].TrustGrantID != "stream-r3" {
				t.Fatalf("runtime trust resolutions=%#v", identities)
			}
		})
	}
}

func publishRouteStreamRuntime(
	t *testing.T,
	registry *routes.Registry,
	extension extensions.Extension,
	runtime extensionsruntime.RuntimeInstanceSnapshot,
) {
	t.Helper()
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: routes.PluginArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
			RuntimeInstanceID: runtime.Identity.InstanceID,
		},
		Routes: extension.Manifest.Routes,
	}}}); err != nil {
		t.Fatal(err)
	}
}

func dialRouteStreamWebSocket(t *testing.T, target string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: []string{"sforum.stream.v1"}}
	connection, response, err := dialer.Dial(target, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial response=%v err=%v", response, err)
	}
	return connection
}

func requireRouteStreamWebSocketRejected(t *testing.T, target string) {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: []string{"sforum.stream.v1"}}
	connection, response, err := dialer.Dial(target, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if connection != nil {
		_ = connection.Close()
	}
	if err == nil {
		t.Fatal("new WebSocket admission crossed the revoked runtime gate")
	}
}

func assertExactRouteLease(
	t *testing.T,
	manager *extensionsruntime.Manager,
	runtime extensionsruntime.RuntimeInstanceSnapshot,
	draining bool,
) {
	t.Helper()
	snapshot, err := manager.InspectRuntimeInstance(runtime.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Admission.Draining != draining || snapshot.Admission.ActiveTotal != 1 ||
		snapshot.Admission.ActiveByClass[extensionsruntime.RuntimeCallRoute] != 1 {
		t.Fatalf("exact WebSocket lease=%#v", snapshot.Admission)
	}
}

func assertRouteStreamWebSocketEchoAndNormalClose(t *testing.T, connection *websocket.Conn, payload string) {
	t.Helper()
	if err := connection.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	messageType, echoed, err := connection.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage || string(echoed) != payload {
		t.Fatalf("messageType=%d payload=%q err=%v", messageType, echoed, err)
	}
	_, _, err = connection.ReadMessage()
	var closeError *websocket.CloseError
	if !errors.As(err, &closeError) || closeError.Code != websocket.CloseNormalClosure {
		t.Fatalf("WebSocket did not complete normally: %v", err)
	}
}

func waitExactRouteDrain(
	t *testing.T,
	manager *extensionsruntime.Manager,
	runtime extensionsruntime.RuntimeInstanceSnapshot,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := manager.WaitDrain(ctx, runtime.Identity); err != nil {
		t.Fatalf("wait exact runtime drain: %v", err)
	}
	snapshot, err := manager.InspectRuntimeInstance(runtime.Identity)
	if err != nil || snapshot.Admission.ActiveTotal != 0 {
		t.Fatalf("drained runtime=%#v err=%v", snapshot, err)
	}
}

func awaitRouteStreamSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func awaitRouteStreamResult(t *testing.T, result <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}

type routeStreamMutableTrust struct {
	mu          sync.Mutex
	identity    extensions.RuntimeTrustIdentity
	resolutions []extensions.RuntimeTrustIdentity
}

func (s *routeStreamMutableTrust) Set(identity extensions.RuntimeTrustIdentity) {
	s.mu.Lock()
	s.identity = identity
	s.mu.Unlock()
}

func (s *routeStreamMutableTrust) RuntimeIdentity(
	context.Context,
	extensions.Extension,
) (extensions.RuntimeTrustIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolutions = append(s.resolutions, s.identity)
	return s.identity, nil
}

func (s *routeStreamMutableTrust) Resolutions() []extensions.RuntimeTrustIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]extensions.RuntimeTrustIdentity(nil), s.resolutions...)
}

type routeStreamTrustPolicy struct {
	mu            sync.Mutex
	pending       *extensions.GuardPolicyEntry
	invalidated   extensions.GuardPolicyEntry
	invalidations int
}

func (p *routeStreamTrustPolicy) CaptureExecutableTrustExactWithFallback(
	extensionID string,
	fallback extensions.GuardPolicyEntry,
) (extensions.GuardPolicyEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := fallback
	p.pending = &entry
	return entry, false
}

func (p *routeStreamTrustPolicy) ReleaseExecutableTrustCaptureExact(
	extensionID string,
	captured extensions.GuardPolicyEntry,
) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil || captured.ExtensionID != extensionID || *p.pending != captured {
		return false
	}
	p.pending = nil
	return true
}

func (p *routeStreamTrustPolicy) InvalidateExecutableTrustExact(
	extensionID string,
	captured extensions.GuardPolicyEntry,
) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil || captured.ExtensionID != extensionID || *p.pending != captured {
		return false
	}
	p.pending = nil
	p.invalidated = captured
	p.invalidations++
	return true
}

func (p *routeStreamTrustPolicy) Invalidations() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.invalidations
}

func (p *routeStreamTrustPolicy) InvalidatedEntry() extensions.GuardPolicyEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.invalidated
}

var _ extensionsruntime.RuntimeTrustSource = (*routeStreamMutableTrust)(nil)
