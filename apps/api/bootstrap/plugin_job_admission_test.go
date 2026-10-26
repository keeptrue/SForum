package bootstrap

import (
	"context"
	"errors"
	"testing"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

type pluginJobAdmissionRuntimeStub struct {
	snapshot extensionsruntime.RuntimeInstanceSnapshot
	gate     *extensionsruntime.RuntimeAdmissionGate
	err      error
	class    extensionsruntime.RuntimeCallClass
}

func (s *pluginJobAdmissionRuntimeStub) AcquireActiveRuntimeCall(
	ctx context.Context,
	_ string,
	class extensionsruntime.RuntimeCallClass,
) (extensionsruntime.RuntimeInstanceSnapshot, *extensionsruntime.RuntimeAdmissionLease, error) {
	s.class = class
	if s.err != nil {
		return extensionsruntime.RuntimeInstanceSnapshot{}, nil, s.err
	}
	lease, err := s.gate.Acquire(ctx, class)
	return s.snapshot, lease, err
}

func TestPluginJobEnqueueAdmissionAllowsOnlyExactActiveSnapshot(t *testing.T) {
	identity := hostapi.PluginJobEnqueueIdentity{
		ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "sha256:exact", InstanceID: "instance-a",
	}
	stub := newPluginJobAdmissionRuntimeStub(t, identity)
	admission := newPluginJobEnqueueAdmission(stub)
	lease, err := admission.AcquirePluginJobEnqueue(context.Background(), identity)
	if err != nil || lease == nil || stub.class != extensionsruntime.RuntimeCallJob {
		t.Fatalf("acquire = %#v, %v class=%q", lease, err, stub.class)
	}
	if snapshot := stub.gate.Snapshot(); snapshot.ActiveByClass[extensionsruntime.RuntimeCallJob] != 1 {
		t.Fatalf("gate snapshot = %#v", snapshot)
	}
	lease.Release()
	if snapshot := stub.gate.Snapshot(); snapshot.ActiveTotal != 0 {
		t.Fatalf("released snapshot = %#v", snapshot)
	}
}

func TestPluginJobEnqueueAdmissionReleasesMismatchedSnapshot(t *testing.T) {
	identity := hostapi.PluginJobEnqueueIdentity{
		ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "sha256:exact", InstanceID: "instance-a",
	}
	mutations := []struct {
		name   string
		mutate func(*extensionsruntime.RuntimeInstanceSnapshot)
	}{
		{name: "instance", mutate: func(s *extensionsruntime.RuntimeInstanceSnapshot) { s.Identity.InstanceID = "instance-b" }},
		{name: "version", mutate: func(s *extensionsruntime.RuntimeInstanceSnapshot) { s.ExtensionVersion = "2.0.0" }},
		{name: "digest", mutate: func(s *extensionsruntime.RuntimeInstanceSnapshot) { s.ArtifactDigest = "sha256:other" }},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			stub := newPluginJobAdmissionRuntimeStub(t, identity)
			tt.mutate(&stub.snapshot)
			lease, err := newPluginJobEnqueueAdmission(stub).AcquirePluginJobEnqueue(context.Background(), identity)
			if lease != nil || !errors.Is(err, hostapi.ErrPluginJobEnqueueStale) {
				t.Fatalf("acquire = %#v, %v", lease, err)
			}
			if snapshot := stub.gate.Snapshot(); snapshot.ActiveTotal != 0 {
				t.Fatalf("mismatch leaked lease: %#v", snapshot)
			}
		})
	}
}

func TestPluginJobEnqueueAdmissionMapsDrainAndStaleErrors(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want error
	}{
		{name: "draining", in: extensionsruntime.ErrRuntimeAdmissionDraining, want: hostapi.ErrPluginJobEnqueueDraining},
		{name: "forced", in: extensionsruntime.ErrRuntimeAdmissionForced, want: hostapi.ErrPluginJobEnqueueDraining},
		{name: "missing", in: extensionsruntime.ErrRuntimeInstanceNotFound, want: hostapi.ErrPluginJobEnqueueStale},
		{name: "inactive", in: extensionsruntime.ErrRuntimeInstanceNotActive, want: hostapi.ErrPluginJobEnqueueStale},
	}
	identity := hostapi.PluginJobEnqueueIdentity{ExtensionID: "demo.plugin", ExtensionVersion: "1", ArtifactDigest: "digest", InstanceID: "instance"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &pluginJobAdmissionRuntimeStub{err: tt.in}
			lease, err := newPluginJobEnqueueAdmission(stub).AcquirePluginJobEnqueue(context.Background(), identity)
			if lease != nil || !errors.Is(err, tt.want) {
				t.Fatalf("acquire = %#v, %v", lease, err)
			}
		})
	}
}

func TestPluginJobEnqueueLeaseDistinguishesForcedDrainFromCallerCancellation(t *testing.T) {
	identity := hostapi.PluginJobEnqueueIdentity{
		ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "sha256:exact", InstanceID: "instance-a",
	}
	stub := newPluginJobAdmissionRuntimeStub(t, identity)
	lease, err := newPluginJobEnqueueAdmission(stub).AcquirePluginJobEnqueue(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	stub.gate.ForceCancel(errors.New("forced lifecycle drain"))
	failure, ok := lease.(hostapi.PluginJobEnqueueLeaseFailure)
	if !ok || !errors.Is(failure.PluginJobEnqueueFailure(), hostapi.ErrPluginJobEnqueueDraining) {
		t.Fatalf("forced failure = %#v", failure)
	}
	lease.Release()

	cancelled, cancel := context.WithCancel(context.Background())
	stub = newPluginJobAdmissionRuntimeStub(t, identity)
	lease, err = newPluginJobEnqueueAdmission(stub).AcquirePluginJobEnqueue(cancelled, identity)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	failure = lease.(hostapi.PluginJobEnqueueLeaseFailure)
	if !errors.Is(failure.PluginJobEnqueueFailure(), context.Canceled) || errors.Is(failure.PluginJobEnqueueFailure(), hostapi.ErrPluginJobEnqueueDraining) {
		t.Fatalf("caller cancellation = %v", failure.PluginJobEnqueueFailure())
	}
	lease.Release()
}

func newPluginJobAdmissionRuntimeStub(t *testing.T, identity hostapi.PluginJobEnqueueIdentity) *pluginJobAdmissionRuntimeStub {
	t.Helper()
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: identity.ExtensionID, InstanceID: identity.InstanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &pluginJobAdmissionRuntimeStub{
		gate: gate,
		snapshot: extensionsruntime.RuntimeInstanceSnapshot{
			Identity:         extensionsruntime.RuntimeInstanceIdentity{ExtensionID: identity.ExtensionID, InstanceID: identity.InstanceID},
			ExtensionVersion: identity.ExtensionVersion, ArtifactDigest: identity.ArtifactDigest, Active: true,
		},
	}
}
