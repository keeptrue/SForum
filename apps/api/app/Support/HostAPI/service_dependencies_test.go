package hostapi

import (
	"context"
	"reflect"
	"testing"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestServiceRuntimeDependencyTracksExactCallerAndProviderUpgrades(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &registryTestProvider{}
	providerV1 := testServiceRuntime("provider.plugin", "1.4.0", "provider-v1", nil, nil,
		[]ServiceRegistration{serviceRegistration("provider.plugin", "provider-v1", "shared.lookup", "1.0.0", 0, provider)})
	callerV1 := testServiceRuntime("caller.plugin", "1.0.0", "caller-v1", []ServiceDependency{{
		ExtensionID: "provider.plugin", VersionConstraint: "^1.0.0", Kind: ServiceDependencyOptional,
	}}, nil, nil)
	if err := registry.ReplaceRuntime(providerV1); err != nil {
		t.Fatal(err)
	}
	invalidProvider := providerV1
	invalidProvider.ExtensionVersion = "2"
	invalidProvider.InstanceID = "provider-invalid"
	if err := registry.ReplaceRuntime(invalidProvider); err == nil {
		t.Fatal("invalid runtime replacement succeeded")
	}
	if stillV1, err := registry.ResolveExact("shared.lookup", "1.0.0"); err != nil || stillV1.Winner.InstanceID != "provider-v1" {
		t.Fatalf("failed runtime replacement changed the service snapshot: %#v err=%v", stillV1, err)
	}
	if err := registry.ReplaceRuntime(callerV1); err != nil {
		t.Fatal(err)
	}

	resolved, err := registry.ResolveExact("shared.lookup", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if decision := resolved.AuthorizeDependency(testServiceCaller(callerV1)); !decision.Allowed {
		t.Fatalf("declared optional dependency denied: %#v", decision)
	}

	callerV2 := testServiceRuntime("caller.plugin", "1.1.0", "caller-v2", callerV1.Dependencies, nil, nil)
	if err := registry.ReplaceRuntime(callerV2); err != nil {
		t.Fatal(err)
	}
	resolved, _ = registry.ResolveExact("shared.lookup", "1.0.0")
	if decision := resolved.AuthorizeDependency(testServiceCaller(callerV1)); decision.Allowed || decision.Reason != "caller_stale" {
		t.Fatalf("old caller survived replacement: %#v", decision)
	}

	providerV2 := testServiceRuntime("provider.plugin", "2.0.0", "provider-v2", nil, nil,
		[]ServiceRegistration{serviceRegistration("provider.plugin", "provider-v2", "shared.lookup", "1.0.0", 0, provider)})
	if err := registry.ReplaceRuntime(providerV2); err != nil {
		t.Fatal(err)
	}
	resolved, _ = registry.ResolveExact("shared.lookup", "1.0.0")
	decision := resolved.AuthorizeDependency(testServiceCaller(callerV2))
	if decision.Allowed || decision.Reason != "version_mismatch" || decision.ProviderVersion != "2.0.0" {
		t.Fatalf("provider version drift was not closed: %#v", decision)
	}

	if !registry.UnregisterProtocolV2ServiceInstance("caller.plugin", "caller-v2") {
		t.Fatal("caller runtime was not removed")
	}
	resolved, _ = registry.ResolveExact("shared.lookup", "1.0.0")
	if decision := resolved.AuthorizeDependency(testServiceCaller(callerV2)); decision.Allowed || decision.Reason != "caller_stale" {
		t.Fatalf("disabled caller remained authorized: %#v", decision)
	}
}

func TestServiceRuntimeCapabilityDependencyFailsClosedOnMissingAndAmbiguity(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &registryTestProvider{}
	caller := testServiceRuntime("caller.plugin", "1.0.0", "caller", []ServiceDependency{{
		Capability: "search.provider", VersionConstraint: "^1.0.0", Kind: ServiceDependencyRequired,
	}}, nil, nil)
	service := serviceRegistration("provider.a", "provider-a", "shared.search", "1.0.0", 0, provider)
	providerA := testServiceRuntime("provider.a", "1.0.0", "provider-a", nil, nil, []ServiceRegistration{service})
	if err := registry.ReplaceRuntime(providerA); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(caller); err != nil {
		t.Fatal(err)
	}
	resolved, _ := registry.ResolveExact("shared.search", "1.0.0")
	if decision := resolved.AuthorizeDependency(testServiceCaller(caller)); decision.Allowed || decision.Reason != "missing" {
		t.Fatalf("missing capability provider decision = %#v", decision)
	}

	providerA.Provides = []ServiceCapability{{ID: "search.provider", Version: "1.1.0"}}
	if err := registry.ReplaceRuntime(providerA); err != nil {
		t.Fatal(err)
	}
	resolved, _ = registry.ResolveExact("shared.search", "1.0.0")
	if decision := resolved.AuthorizeDependency(testServiceCaller(caller)); !decision.Allowed {
		t.Fatalf("single capability provider denied: %#v", decision)
	}

	providerBService := serviceRegistration("provider.b", "provider-b", "provider.b.search", "1.0.0", 10, provider)
	providerBService.Action = ServiceActionReplace
	providerBService.TargetID = "shared.search"
	providerB := testServiceRuntime("provider.b", "1.0.0", "provider-b", nil,
		[]ServiceCapability{{ID: "search.provider", Version: "1.2.0"}}, []ServiceRegistration{providerBService})
	if err := registry.ReplaceRuntime(providerB); err != nil {
		t.Fatal(err)
	}
	resolved, _ = registry.ResolveExact("shared.search", "1.0.0")
	decision := resolved.AuthorizeDependency(testServiceCaller(caller))
	if decision.Allowed || decision.Reason != "ambiguous" || !reflect.DeepEqual(decision.Candidates, []string{"provider.a", "provider.b"}) {
		t.Fatalf("ambiguous capability providers decision = %#v", decision)
	}
}

func TestProtocolV2ServiceDependencyDenialIsConsistentAcrossCallModes(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{output: v2ServiceDocument("shared.lookup.response", "1")}
	providerRuntime := testServiceRuntime("provider.plugin", "1.0.0", "provider", nil, nil, []ServiceRegistration{
		v2ServiceRegistration("provider.plugin", "provider", "shared.lookup", "1.0.0", provider),
	})
	callerRuntime := testServiceRuntime("caller.plugin", "1.0.0", "caller", nil, nil, nil)
	if err := registry.ReplaceRuntime(providerRuntime); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(callerRuntime); err != nil {
		t.Fatal(err)
	}
	server := newProtocolV2ServiceTestServer(registry, nil)
	requestContext := testServiceRequestContext(callerRuntime)
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), requestContext.GetExtension())

	listed, _ := server.List(ctx, &hostv2.ServiceListRequest{Context: requestContext})
	if len(listed.GetServices()) != 0 {
		t.Fatalf("undeclared service leaked through List: %#v", listed.GetServices())
	}
	resolved, _ := server.Resolve(ctx, &hostv2.ServiceResolveRequest{Context: requestContext, ServiceId: "shared.lookup", VersionConstraint: "1.0.0"})
	invoked, _ := server.Invoke(ctx, &hostv2.ServiceInvokeRequest{
		Context: requestContext, ServiceId: "shared.lookup", Version: "1.0.0", Operation: "find",
		Input: v2ServiceDocument("shared.lookup.request", "1"),
	})
	stream := &fakeV2HostServiceStream{ctx: ctx, recv: []*hostv2.ServiceStreamFrame{
		v2ServiceOpenFrame(requestContext, "shared.lookup", "1.0.0", "find"),
	}}
	if err := server.Stream(stream); err != nil {
		t.Fatal(err)
	}
	for label, detail := range map[string]*protocolv2.ErrorDetail{
		"resolve": resolved.GetError(), "invoke": invoked.GetError(), "stream": stream.sent[0].GetError(),
	} {
		if detail.GetReason() != "host.service_dependency_undeclared" || detail.GetCode() != protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED {
			t.Fatalf("%s dependency denial = %#v", label, detail)
		}
	}
	if provider.invokeCalls != 0 {
		t.Fatalf("denied provider calls = %d", provider.invokeCalls)
	}
}

func testServiceRuntime(
	extensionID, version, instanceID string,
	dependencies []ServiceDependency,
	provides []ServiceCapability,
	registrations []ServiceRegistration,
) ServiceRuntimePublication {
	return ServiceRuntimePublication{
		ExtensionID: extensionID, ExtensionVersion: version, ArtifactDigest: "digest-" + instanceID,
		TrustGrantID: "grant-" + instanceID, RuntimeEpoch: 1, InstanceID: instanceID,
		Dependencies: dependencies, Provides: provides, Registrations: registrations,
	}
}

func testServiceCaller(runtime ServiceRuntimePublication) ServiceCaller {
	return ServiceCaller{
		ExtensionID: runtime.ExtensionID, ExtensionVersion: runtime.ExtensionVersion,
		ArtifactDigest: runtime.ArtifactDigest, TrustGrantID: runtime.TrustGrantID,
		RuntimeEpoch: runtime.RuntimeEpoch, InstanceID: runtime.InstanceID, Attested: true,
	}
}

func testServiceRequestContext(runtime ServiceRuntimePublication) *protocolv2.RequestContext {
	return &protocolv2.RequestContext{
		RequestId: "request-1", Locale: "und", Deadline: timestamppb.New(time.Now().Add(time.Minute)),
		Extension: &protocolv2.ExtensionIdentity{
			ExtensionId: runtime.ExtensionID, ExtensionVersion: runtime.ExtensionVersion,
			ArtifactDigest: runtime.ArtifactDigest, TrustGrantId: runtime.TrustGrantID,
			RuntimeEpoch: runtime.RuntimeEpoch, InstanceId: runtime.InstanceID,
		},
	}
}
