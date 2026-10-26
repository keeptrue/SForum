package extensionsruntime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

// serviceE2ECapabilitySource grants extensions.call to every test consumer so
// Host service discovery admission can pass; service-level users.read is still
// enforced separately by service authority checks.
type serviceE2ECapabilitySource struct{}

func (serviceE2ECapabilitySource) CapabilitiesFor(_ context.Context, extensionID string) (capabilities.Set, error) {
	if extensionID == "" {
		return nil, errors.New("missing extension id")
	}
	return capabilities.NewSet([]string{capabilities.HostAPI, capabilities.ExtensionsCall, capabilities.UsersRead}), nil
}

func (serviceE2ECapabilitySource) DeclaredJobKinds(context.Context, string) ([]string, error) {
	return nil, nil
}

func TestProtocolV2ServiceDiscoveryAcrossRealPluginProcesses(t *testing.T) {
	// 本夹具验证真实 broker/provider 组合；production 由 bootstrap 的 Manager
	// adapter 对 Winner exact instance 执行 RuntimeCallService admission。
	// Host service discovery requires a capability source that grants
	// extensions.call to consumer plugins (process capability, not wire grant).
	gateway := hostapi.NewGateway(hostapi.New(hostapi.Config{
		ServiceAdmission: serviceE2ETestAdmission{},
		Capabilities:     serviceE2ECapabilitySource{},
	}))
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: serviceE2ETrust{}, HostAPI: gateway,
	})

	low := serviceE2EProviderExtension(t, "low", 10)
	high := serviceE2EProviderExtension(t, "high", 100)
	consumer := serviceE2EConsumerExtension(t, true)
	t.Cleanup(func() {
		_ = starter.Stop(context.Background(), consumer)
		_ = starter.Stop(context.Background(), high)
		_ = starter.Stop(context.Background(), low)
	})

	startServiceE2EExtension(t, starter, low)
	registry := gateway.ProtocolV2ServiceRegistry()
	_ = resolveServiceE2E(t, registry, serviceE2ESharedUnaryID)
	startServiceE2EExtension(t, starter, consumer)
	firstLow := resolveServiceE2E(t, registry, serviceE2ESharedUnaryID)

	initial := invokeServiceE2EConsumer(t, starter, consumer, "probe")
	assertServiceE2EProbe(t, initial, low.ID, firstLow.Winner.InstanceID, low.ID)

	// A second real provider process publishes the same target/version. Higher
	// Manifest priority must win without disturbing the low provider's stream.
	startServiceE2EExtension(t, starter, high)
	conflict := findServiceE2EConflict(t, registry.Conflicts(), serviceE2ESharedUnaryID)
	if len(conflict.Candidates) != 2 || conflict.Candidates[0].ExtensionID != high.ID || conflict.Candidates[1].ExtensionID != low.ID {
		t.Fatalf("conflict order = %#v", conflict.Candidates)
	}
	highResolved := resolveServiceE2E(t, registry, serviceE2ESharedUnaryID)
	withConflict := invokeServiceE2EConsumer(t, starter, consumer, "probe")
	assertServiceE2EProbe(t, withConflict, high.ID, highResolved.Winner.InstanceID, low.ID)

	if err := starter.Stop(context.Background(), high); err != nil {
		t.Fatal(err)
	}
	withoutConflict := invokeServiceE2EConsumer(t, starter, consumer, "invoke")
	if withoutConflict.UnaryProvider != low.ID {
		t.Fatalf("low provider did not recover after conflict stop: %#v", withoutConflict)
	}

	// Retain the exact old provider channel. Stop must remove discovery state and
	// make both that channel and its runtime identity unusable.
	oldResolved := resolveServiceE2E(t, registry, serviceE2ESharedUnaryID)
	oldProvider := oldResolved.Winner.Provider
	oldInstance := oldResolved.Winner.InstanceID
	if err := starter.Stop(context.Background(), low); err != nil {
		t.Fatal(err)
	}
	missing := invokeServiceE2EConsumer(t, starter, consumer, "resolve")
	if missing.ResolveReason != "host.service_not_found" {
		t.Fatalf("stopped provider remained discoverable: %#v", missing)
	}
	assertServiceE2EOldProviderUnavailable(t, oldProvider, oldResolved.Winner.Descriptor.GetServiceId())

	startServiceE2EExtension(t, starter, low)
	restarted := resolveServiceE2E(t, registry, serviceE2ESharedUnaryID)
	if restarted.Winner.InstanceID == oldInstance {
		t.Fatalf("provider restart reused instance %q", oldInstance)
	}
	if gateway.UnregisterProtocolV2ServiceInstance(low.ID, oldInstance) {
		t.Fatal("stale instance removed the replacement registration")
	}
	afterRestart := invokeServiceE2EConsumer(t, starter, consumer, "invoke")
	if afterRestart.UnaryProvider != low.ID || afterRestart.UnaryInstance != restarted.Winner.InstanceID {
		t.Fatalf("consumer reached stale provider after restart: %#v", afterRestart)
	}
	assertServiceE2EOldProviderUnavailable(t, oldProvider, oldResolved.Winner.Descriptor.GetServiceId())

	// Crash happens inside the provider's real unary handler. The consumer sees
	// a typed transport failure and the starter's exit watcher reaps discovery.
	crashedProvider := restarted.Winner.Provider
	crashedServiceID := restarted.Winner.Descriptor.GetServiceId()
	crash := invokeServiceE2EConsumer(t, starter, consumer, "crash")
	if crash.InvokeReason != "host.service_transport_unavailable" {
		t.Fatalf("provider crash was not typed: %#v", crash)
	}
	waitForServiceE2EMissing(t, registry, serviceE2ESharedUnaryID)
	assertServiceE2EOldProviderUnavailable(t, crashedProvider, crashedServiceID)

	startServiceE2EExtension(t, starter, low)
	// Replacing the consumer creates a fresh process/channel/token with a grant
	// set that omits users.read. List hides the service; every call path denies.
	deniedConsumer := serviceE2EConsumerExtension(t, false)
	startServiceE2EExtension(t, starter, deniedConsumer)
	denied := invokeServiceE2EConsumer(t, starter, deniedConsumer, "denied")
	if denied.ListCount != 0 || denied.ResolveReason != "host.service_authority_denied" ||
		denied.InvokeReason != "host.service_authority_denied" || denied.StreamReason != "host.service_authority_denied" {
		t.Fatalf("consumer authority was not enforced across discovery modes: %#v", denied)
	}
}

type serviceE2ETestAdmission struct{}

func (serviceE2ETestAdmission) AcquireServiceProvider(
	ctx context.Context,
	_ hostapi.ServiceProviderIdentity,
) (hostapi.ServiceProviderAdmissionLease, error) {
	return serviceE2ETestLease{ctx: ctx}, nil
}

type serviceE2ETestLease struct {
	ctx context.Context
}

func (l serviceE2ETestLease) Context() context.Context { return l.ctx }
func (serviceE2ETestLease) Release()                   {}

func startServiceE2EExtension(t *testing.T, starter *extensionsruntime.ProtocolStarter, extension extensions.Extension) {
	t.Helper()
	// ProtocolStarter binds the subprocess lifetime to Start's context.
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatalf("start %s: %v", extension.ID, err)
	}
}

func invokeServiceE2EConsumer(
	t *testing.T,
	starter *extensionsruntime.ProtocolStarter,
	consumer extensions.Extension,
	action string,
) serviceE2EReport {
	t.Helper()
	result := starter.InvokeHook(context.Background(), consumer, extensionsruntime.HookInput{
		Name: serviceE2EConsumerHookName, Kind: "filter", CorrelationID: "service-e2e-" + action,
		Timeout: 4 * time.Second, Payload: map[string]any{"action": action},
	})
	if !result.OK {
		t.Fatalf("consumer action %s failed: %#v", action, result)
	}
	var report serviceE2EReport
	if err := report.Unmarshal(result.Message); err != nil {
		t.Fatalf("decode consumer action %s: %v; message=%q", action, err, result.Message)
	}
	return report
}

func resolveServiceE2E(t *testing.T, registry *hostapi.ServiceRegistry, serviceID string) hostapi.ResolvedService {
	t.Helper()
	resolved, err := registry.ResolveExact(serviceID, serviceE2EVersion)
	if err != nil {
		services, listErr := registry.List("", "")
		t.Fatalf("resolve %s: %v; registry=%#v listErr=%v", serviceID, err, services, listErr)
	}
	return resolved
}

func findServiceE2EConflict(t *testing.T, conflicts []hostapi.ServiceConflict, serviceID string) hostapi.ServiceConflict {
	t.Helper()
	for _, conflict := range conflicts {
		if conflict.ServiceID == serviceID {
			return conflict
		}
	}
	t.Fatalf("conflict %s not found: %#v", serviceID, conflicts)
	return hostapi.ServiceConflict{}
}

func assertServiceE2EProbe(t *testing.T, report serviceE2EReport, providerID, instanceID, streamProviderID string) {
	t.Helper()
	if report.ListReason != "" || report.ListCount != 2 || !serviceE2EContains(report.ListIDs, serviceE2ESharedUnaryID) ||
		!serviceE2EContains(report.ListIDs, serviceE2ESharedStreamID) {
		t.Fatalf("List result = %#v", report)
	}
	if report.ResolveReason != "" || report.ResolveProvider != providerID || report.ResolveVersion != serviceE2EVersion {
		t.Fatalf("Resolve result = %#v", report)
	}
	if report.InvokeReason != "" || report.UnaryProvider != providerID || report.UnaryInstance != instanceID || report.UnaryValue != "unary-value" {
		t.Fatalf("Invoke result = %#v", report)
	}
	if report.StreamReason != "" || len(report.StreamProviders) != 2 || len(report.StreamValues) != 2 ||
		len(report.StreamInstances) != 2 || report.StreamProviders[0] != streamProviderID || report.StreamProviders[1] != streamProviderID ||
		report.StreamInstances[0] == "" || report.StreamInstances[0] != report.StreamInstances[1] ||
		report.StreamValues[0] != "stream-one" || report.StreamValues[1] != "stream-two" {
		t.Fatalf("Stream result = %#v", report)
	}
}

func assertServiceE2EOldProviderUnavailable(t *testing.T, provider hostapi.ServiceProvider, serviceID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, _, err := provider.Invoke(ctx, nil, serviceID, serviceE2EVersion, "echo", serviceE2EDocument("stale"))
	if err == nil {
		t.Fatal("stopped provider channel remained callable")
	}
}

func waitForServiceE2EMissing(t *testing.T, registry *hostapi.ServiceRegistry, serviceID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err := registry.ResolveExact(serviceID, serviceE2EVersion)
		if errors.Is(err, hostapi.ErrServiceNotFound) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service %s remained registered after provider exit", serviceID)
}

func serviceE2EContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
