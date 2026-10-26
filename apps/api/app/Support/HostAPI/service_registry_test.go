package hostapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"

	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type registryTestProvider struct {
	id string
}

func (p *registryTestProvider) Invoke(_ context.Context, _ *protocolv2.RequestContext, serviceID, version, operation string, input *protocolv2.TypedDocument) (*protocolv2.TypedDocument, *protocolv2.ErrorDetail, error) {
	if p.id == "transport-error" {
		return nil, nil, io.ErrUnexpectedEOF
	}
	return input, nil, nil
}

func (p *registryTestProvider) Stream(_ context.Context, _ *protocolv2.RequestContext, _, _, _ string, stream ServiceBidiStream) (*protocolv2.ErrorDetail, error) {
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, stream.CloseSend()
		}
		if err != nil {
			return nil, err
		}
		if err := stream.Send(message); err != nil {
			return nil, err
		}
	}
}

func TestServiceRegistryResolveAndListDeterministically(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &registryTestProvider{id: "catalog"}
	if err := registry.ReplaceExtension("catalog.plugin", []ServiceRegistration{
		serviceRegistration("catalog.plugin", "runtime-1", "catalog.lookup", "1.0.0", 0, provider),
		serviceRegistration("catalog.plugin", "runtime-1", "catalog.lookup", "2.0.0", 0, provider),
		serviceRegistration("catalog.plugin", "runtime-1", "catalog.health", "1.5.0", 0, provider),
	}); err != nil {
		t.Fatal(err)
	}
	if registry.Revision() != 1 {
		t.Fatalf("revision = %d, want 1", registry.Revision())
	}

	all, err := registry.List("", "")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(all))
	for _, service := range all {
		got = append(got, service.ServiceID+"@"+service.Winner.Descriptor.GetVersion())
	}
	want := []string{"catalog.health@1.5.0", "catalog.lookup@2.0.0", "catalog.lookup@1.0.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}

	resolved, err := registry.Resolve("catalog.lookup", ">=1.0.0 <2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Winner.Descriptor.GetVersion() != "1.0.0" || resolved.Revision != 1 || resolved.Winner.Provider != provider {
		t.Fatalf("unexpected resolution: %#v", resolved)
	}
	if _, err := registry.Resolve("catalog.lookup", ">=3.0.0"); !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("missing resolution error = %v", err)
	}
}

func TestServiceRegistryRejectsLooseSemverAndConstraints(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &registryTestProvider{}
	loose := serviceRegistration("demo.plugin", "runtime-1", "demo.lookup", "1.2", 0, provider)
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{loose}); !errors.Is(err, ErrInvalidServiceRegistration) {
		t.Fatalf("loose version error = %v", err)
	}
	invalidSchema := serviceRegistration("demo.plugin", "runtime-1", "demo.lookup", "1.2.3", 0, provider)
	invalidSchema.Descriptor.RequestSchemaId = "demo.lookup.request"
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{invalidSchema}); !errors.Is(err, ErrInvalidServiceRegistration) {
		t.Fatalf("unversioned schema error = %v", err)
	}

	valid := serviceRegistration("demo.plugin", "runtime-1", "demo.lookup", "1.2.3", 0, provider)
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{valid}); err != nil {
		t.Fatal(err)
	}
	for _, constraint := range []string{"^1.2", "v1.2.3", "1.x", "*", "=>1.2.3"} {
		if _, err := registry.List("demo.lookup", constraint); !errors.Is(err, ErrInvalidServiceConstraint) {
			t.Errorf("constraint %q error = %v", constraint, err)
		}
	}
	for _, constraint := range []string{"^1.2.3", ">= 1.0.0, < 2.0.0", "1.0.0 - 1.9.9", "=1.2.3 || =2.0.0"} {
		if _, err := registry.List("demo.lookup", constraint); err != nil {
			t.Errorf("constraint %q rejected: %v", constraint, err)
		}
	}
}

func TestServiceRegistryReplaceIsAtomicOnValidationFailure(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &registryTestProvider{}
	initial := serviceRegistration("demo.plugin", "runtime-old", "demo.old", "1.0.0", 0, provider)
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{initial}); err != nil {
		t.Fatal(err)
	}

	valid := serviceRegistration("demo.plugin", "runtime-new", "demo.new", "2.0.0", 0, provider)
	invalid := serviceRegistration("demo.plugin", "runtime-new", "demo.invalid", "2", 0, provider)
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{valid, invalid}); !errors.Is(err, ErrInvalidServiceRegistration) {
		t.Fatalf("replace error = %v", err)
	}
	if registry.Revision() != 1 {
		t.Fatalf("failed replacement advanced revision to %d", registry.Revision())
	}
	if _, err := registry.Resolve("demo.old", "1.0.0"); err != nil {
		t.Fatalf("old snapshot lost: %v", err)
	}
	if _, err := registry.Resolve("demo.new", "2.0.0"); !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("partial new snapshot visible: %v", err)
	}
}

func TestServiceRegistryConflictsAreInspectableAndPrioritized(t *testing.T) {
	registry := NewServiceRegistry()
	low := &registryTestProvider{id: "low"}
	high := &registryTestProvider{id: "high"}
	if err := registry.ReplaceExtension("z.low", []ServiceRegistration{
		serviceRegistration("z.low", "runtime-low", "shared.lookup", "1.0.0", 10, low),
	}); err != nil {
		t.Fatal(err)
	}
	replacement := serviceRegistration("a.high", "runtime-high", "a.high.lookup", "1.0.0", 100, high)
	replacement.Action = ServiceActionReplace
	replacement.TargetID = "shared.lookup"
	if err := registry.ReplaceExtension("a.high", []ServiceRegistration{replacement}); err != nil {
		t.Fatal(err)
	}

	resolved, err := registry.Resolve("shared.lookup", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.HasConflict() || resolved.Winner.ExtensionID != "a.high" || resolved.Winner.Provider != high {
		t.Fatalf("unexpected conflict resolution: %#v", resolved)
	}
	conflicts := registry.Conflicts()
	if len(conflicts) != 1 || conflicts[0].ServiceID != "shared.lookup" || conflicts[0].Version != "1.0.0" {
		t.Fatalf("Conflicts() = %#v", conflicts)
	}
	providers := []string{conflicts[0].Candidates[0].ExtensionID, conflicts[0].Candidates[1].ExtensionID}
	if !reflect.DeepEqual(providers, []string{"a.high", "z.low"}) {
		t.Fatalf("candidate order = %#v", providers)
	}
}

func TestServiceRegistryTieBreakDoesNotDependOnRegistrationOrder(t *testing.T) {
	build := func(order []string) *ServiceRegistry {
		registry := NewServiceRegistry()
		for _, extensionID := range order {
			provider := &registryTestProvider{id: extensionID}
			registration := serviceRegistration(extensionID, "runtime-"+extensionID, "shared.lookup", "1.0.0", 10, provider)
			if err := registry.ReplaceExtension(extensionID, []ServiceRegistration{registration}); err != nil {
				t.Fatal(err)
			}
		}
		return registry
	}
	left := build([]string{"z.plugin", "a.plugin"})
	right := build([]string{"a.plugin", "z.plugin"})
	leftResult, _ := left.Resolve("shared.lookup", "1.0.0")
	rightResult, _ := right.Resolve("shared.lookup", "1.0.0")
	if leftResult.Winner.ExtensionID != "a.plugin" || rightResult.Winner.ExtensionID != "a.plugin" {
		t.Fatalf("tie break changed with insertion order: %q / %q", leftResult.Winner.ExtensionID, rightResult.Winner.ExtensionID)
	}
}

func TestServiceRegistryRejectsDuplicateOwnedVersionAndInvalidComposition(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &registryTestProvider{}
	first := serviceRegistration("demo.plugin", "runtime-1", "demo.lookup", "1.0.0", 0, provider)
	duplicate := first
	duplicate.Descriptor = cloneTestDescriptor(first.Descriptor)
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{first, duplicate}); !errors.Is(err, ErrInvalidServiceRegistration) {
		t.Fatalf("duplicate error = %v", err)
	}

	replace := serviceRegistration("demo.plugin", "runtime-1", "demo.replace", "1.0.0", 0, provider)
	replace.Action = ServiceActionReplace
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{replace}); !errors.Is(err, ErrInvalidServiceRegistration) {
		t.Fatalf("missing target error = %v", err)
	}
	addWithTarget := first
	addWithTarget.TargetID = "other.lookup"
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{addWithTarget}); !errors.Is(err, ErrInvalidServiceRegistration) {
		t.Fatalf("add target error = %v", err)
	}
}

func TestServiceRegistryUnregisterAdvancesRevisionOnlyWhenChanged(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &registryTestProvider{}
	if registry.UnregisterExtension("missing.plugin") {
		t.Fatal("missing extension reported as removed")
	}
	if registry.Revision() != 0 {
		t.Fatalf("no-op unregister revision = %d", registry.Revision())
	}
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{
		serviceRegistration("demo.plugin", "runtime-1", "demo.lookup", "1.0.0", 0, provider),
	}); err != nil {
		t.Fatal(err)
	}
	if !registry.UnregisterExtension("demo.plugin") || registry.Revision() != 2 {
		t.Fatalf("unregister revision = %d", registry.Revision())
	}
	if _, err := registry.Resolve("demo.lookup", ""); !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("removed service resolution error = %v", err)
	}
}

func TestServiceRegistryClonesDescriptorsAndNormalizesAuthority(t *testing.T) {
	registry := NewServiceRegistry()
	descriptor := &protocolv2.ServiceDescriptor{
		ServiceId: "demo.lookup", Version: "1.0.0",
		RequestSchemaId: "demo.request@1", ResponseSchemaId: "demo.response@1",
		RequiredAuthority: []string{" settings.read ", "audit.append", "settings.read"},
	}
	registration := ServiceRegistration{
		ExtensionID: "demo.plugin", InstanceID: "runtime-1", Action: ServiceActionAdd,
		Descriptor: descriptor, Provider: &registryTestProvider{},
	}
	if err := registry.ReplaceExtension("demo.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	descriptor.ServiceId = "mutated.input"
	descriptor.RequiredAuthority[0] = "mutated.authority"

	resolved, err := registry.Resolve("demo.lookup", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	wantAuthority := []string{"audit.append", "settings.read"}
	if !reflect.DeepEqual(resolved.Winner.Descriptor.GetRequiredAuthority(), wantAuthority) {
		t.Fatalf("required authority = %#v", resolved.Winner.Descriptor.GetRequiredAuthority())
	}
	resolved.Winner.Descriptor.ServiceId = "mutated.output"
	resolved.Candidates[0].Descriptor.RequiredAuthority[0] = "mutated.output.authority"
	again, err := registry.Resolve("demo.lookup", "1.0.0")
	if err != nil || again.Winner.Descriptor.GetServiceId() != "demo.lookup" || !reflect.DeepEqual(again.Winner.Descriptor.GetRequiredAuthority(), wantAuthority) {
		t.Fatalf("registry snapshot was externally mutated: %#v err=%v", again, err)
	}
}

func TestCheckServiceAuthorityReportsStableMissingSet(t *testing.T) {
	decision := CheckServiceAuthority(
		[]string{"settings.read", "audit.append", "settings.read"},
		[]string{" settings.read ", "jobs.enqueue", "jobs.enqueue"},
	)
	if decision.Allowed {
		t.Fatal("incomplete authority was allowed")
	}
	if !reflect.DeepEqual(decision.Required, []string{"audit.append", "settings.read"}) ||
		!reflect.DeepEqual(decision.Granted, []string{"jobs.enqueue", "settings.read"}) ||
		!reflect.DeepEqual(decision.Missing, []string{"audit.append"}) {
		t.Fatalf("unexpected authority decision: %#v", decision)
	}
	if !CheckServiceAuthority([]string{"settings.read"}, []string{"settings.read"}).Allowed {
		t.Fatal("complete authority was denied")
	}
}

func TestServiceRegistryReadersSeeWholeReplacementSnapshots(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &registryTestProvider{}
	replace := func(instance string) error {
		return registry.ReplaceExtension("demo.plugin", []ServiceRegistration{
			serviceRegistration("demo.plugin", instance, "demo.alpha", "1.0.0", 0, provider),
			serviceRegistration("demo.plugin", instance, "demo.beta", "1.0.0", 0, provider),
		})
	}
	if err := replace("runtime-0"); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errCh := make(chan error, 8)
	var readers sync.WaitGroup
	for reader := 0; reader < 8; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for iteration := 0; iteration < 200; iteration++ {
				services, err := registry.List("", "")
				if err != nil {
					errCh <- err
					return
				}
				if len(services) != 2 || services[0].Winner.InstanceID != services[1].Winner.InstanceID {
					errCh <- fmt.Errorf("partial snapshot: %#v", services)
					return
				}
			}
		}()
	}
	close(start)
	for revision := 1; revision <= 100; revision++ {
		if err := replace(fmt.Sprintf("runtime-%d", revision)); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestServiceProviderInterfacesPreserveTransportFailureAndStreamingShape(t *testing.T) {
	provider := &registryTestProvider{id: "transport-error"}
	var unary ServiceProvider = provider
	var streaming ServiceStreamingProvider = provider
	requestContext := &protocolv2.RequestContext{RequestId: "request-1", Locale: "zh-CN"}
	if _, detail, err := unary.Invoke(context.Background(), requestContext, "demo.lookup", "1.0.0", "get", nil); detail != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("transport failure detail=%#v err=%v", detail, err)
	}
	if streaming == nil {
		t.Fatal("streaming provider interface is nil")
	}
}

func serviceRegistration(extensionID, instanceID, serviceID, version string, priority int, provider ServiceProvider) ServiceRegistration {
	return ServiceRegistration{
		ExtensionID: extensionID, InstanceID: instanceID, Action: ServiceActionAdd, Priority: priority,
		Descriptor: &protocolv2.ServiceDescriptor{
			ServiceId: serviceID, Version: version,
			RequestSchemaId: serviceID + ".request@1", ResponseSchemaId: serviceID + ".response@1",
		},
		Provider: provider,
	}
}

func cloneTestDescriptor(value *protocolv2.ServiceDescriptor) *protocolv2.ServiceDescriptor {
	return &protocolv2.ServiceDescriptor{
		ServiceId: value.GetServiceId(), Version: value.GetVersion(),
		RequestSchemaId: value.GetRequestSchemaId(), ResponseSchemaId: value.GetResponseSchemaId(),
		ClientStreaming: value.GetClientStreaming(), ServerStreaming: value.GetServerStreaming(),
		RequiredAuthority: append([]string(nil), value.GetRequiredAuthority()...),
	}
}
