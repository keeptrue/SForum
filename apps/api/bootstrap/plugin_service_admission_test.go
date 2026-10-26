package bootstrap

import (
	"context"
	"errors"
	"sync"
	"testing"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

type pluginServiceAdmissionRuntimeStub struct {
	gate *extensionsruntime.RuntimeAdmissionGate
	err  error

	mu       sync.Mutex
	identity extensionsruntime.RuntimeInstanceIdentity
	class    extensionsruntime.RuntimeCallClass
}

func (s *pluginServiceAdmissionRuntimeStub) AcquireRuntimeCall(
	ctx context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	class extensionsruntime.RuntimeCallClass,
) (*extensionsruntime.RuntimeAdmissionLease, error) {
	s.mu.Lock()
	s.identity = identity
	s.class = class
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.gate.Acquire(ctx, class)
}

func (s *pluginServiceAdmissionRuntimeStub) request() (extensionsruntime.RuntimeInstanceIdentity, extensionsruntime.RuntimeCallClass) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.identity, s.class
}

func TestPluginServiceAdmissionAcquiresExactServiceRuntime(t *testing.T) {
	identity := hostapi.ServiceProviderIdentity{ExtensionID: "provider.plugin", InstanceID: "instance-a"}
	stub := newPluginServiceAdmissionRuntimeStub(t, identity)
	lease, err := newPluginServiceProviderAdmission(stub).AcquireServiceProvider(context.Background(), identity)
	if err != nil || lease == nil {
		t.Fatalf("acquire = %#v, %v", lease, err)
	}
	requested, class := stub.request()
	if requested != (extensionsruntime.RuntimeInstanceIdentity{ExtensionID: identity.ExtensionID, InstanceID: identity.InstanceID}) || class != extensionsruntime.RuntimeCallService {
		t.Fatalf("request = %#v class=%q", requested, class)
	}
	if snapshot := stub.gate.Snapshot(); snapshot.ActiveByClass[extensionsruntime.RuntimeCallService] != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	lease.Release()
	if snapshot := stub.gate.Snapshot(); snapshot.ActiveTotal != 0 {
		t.Fatalf("release leaked lease: %#v", snapshot)
	}
}

func TestPluginServiceAdmissionMapsStaleDrainAndCallerFailures(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want error
	}{
		{name: "stale missing", in: extensionsruntime.ErrRuntimeInstanceNotFound, want: hostapi.ErrServiceProviderStale},
		{name: "stale inactive", in: extensionsruntime.ErrRuntimeInstanceNotActive, want: hostapi.ErrServiceProviderStale},
		{name: "draining", in: extensionsruntime.ErrRuntimeAdmissionDraining, want: hostapi.ErrServiceProviderDraining},
		{name: "forced", in: extensionsruntime.ErrRuntimeAdmissionForced, want: hostapi.ErrServiceProviderDraining},
		{name: "cancelled", in: context.Canceled, want: context.Canceled},
		{name: "deadline", in: context.DeadlineExceeded, want: context.DeadlineExceeded},
	}
	identity := hostapi.ServiceProviderIdentity{ExtensionID: "provider.plugin", InstanceID: "instance-a"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &pluginServiceAdmissionRuntimeStub{err: tt.in}
			lease, err := newPluginServiceProviderAdmission(stub).AcquireServiceProvider(context.Background(), identity)
			if lease != nil || !errors.Is(err, tt.want) {
				t.Fatalf("acquire = %#v, %v", lease, err)
			}
		})
	}
}

func TestPluginServiceAdmissionDistinguishesForcedDrainFromCallerCancellation(t *testing.T) {
	identity := hostapi.ServiceProviderIdentity{ExtensionID: "provider.plugin", InstanceID: "instance-a"}
	stub := newPluginServiceAdmissionRuntimeStub(t, identity)
	lease, err := newPluginServiceProviderAdmission(stub).AcquireServiceProvider(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	stub.gate.ForceCancel(errors.New("forced lifecycle drain"))
	failure := lease.(hostapi.ServiceProviderAdmissionLeaseFailure).ServiceProviderAdmissionFailure()
	if !errors.Is(failure, hostapi.ErrServiceProviderDraining) {
		t.Fatalf("forced failure = %v", failure)
	}
	lease.Release()

	caller, cancel := context.WithCancel(context.Background())
	stub = newPluginServiceAdmissionRuntimeStub(t, identity)
	lease, err = newPluginServiceProviderAdmission(stub).AcquireServiceProvider(caller, identity)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	failure = lease.(hostapi.ServiceProviderAdmissionLeaseFailure).ServiceProviderAdmissionFailure()
	if !errors.Is(failure, context.Canceled) || errors.Is(failure, hostapi.ErrServiceProviderDraining) {
		t.Fatalf("caller failure = %v", failure)
	}
	lease.Release()
}

func TestPluginServiceAcquireAndDrainAreLinearized(t *testing.T) {
	identity := hostapi.ServiceProviderIdentity{ExtensionID: "provider.plugin", InstanceID: "instance-race"}
	stub := newPluginServiceAdmissionRuntimeStub(t, identity)
	admission := newPluginServiceProviderAdmission(stub)
	const callers = 64
	start := make(chan struct{})
	var group sync.WaitGroup
	var mu sync.Mutex
	var leases []hostapi.ServiceProviderAdmissionLease
	var failures []error
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			lease, err := admission.AcquireServiceProvider(context.Background(), identity)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			leases = append(leases, lease)
		}()
	}
	drained := make(chan struct{})
	go func() {
		<-start
		stub.gate.BeginDrain()
		close(drained)
	}()
	close(start)
	group.Wait()
	<-drained
	for _, err := range failures {
		if !errors.Is(err, hostapi.ErrServiceProviderDraining) {
			t.Fatalf("race error = %v", err)
		}
	}
	if len(leases)+len(failures) != callers {
		t.Fatalf("outcomes leases=%d failures=%d", len(leases), len(failures))
	}
	if lease, err := admission.AcquireServiceProvider(context.Background(), identity); lease != nil || !errors.Is(err, hostapi.ErrServiceProviderDraining) {
		t.Fatalf("post-drain acquire = %#v, %v", lease, err)
	}
	for _, lease := range leases {
		lease.Release()
	}
	if snapshot := stub.gate.Snapshot(); snapshot.ActiveTotal != 0 {
		t.Fatalf("race leaked leases: %#v", snapshot)
	}
}

func newPluginServiceAdmissionRuntimeStub(t *testing.T, identity hostapi.ServiceProviderIdentity) *pluginServiceAdmissionRuntimeStub {
	t.Helper()
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: identity.ExtensionID, InstanceID: identity.InstanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &pluginServiceAdmissionRuntimeStub{gate: gate}
}
