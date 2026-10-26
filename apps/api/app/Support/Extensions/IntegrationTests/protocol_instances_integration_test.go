package extensionsruntime_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func TestProtocolV2StagesPublishesRollsBackAndStopsExactInstances(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })

	firstTarget, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	first := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: firstTarget.InstanceID}
	assertProtocolInstanceState(t, starter, first, extensionsruntime.ProtocolRuntimePublished)
	resolved, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != first.InstanceID {
		t.Fatalf("first published service = %#v, %v", resolved, err)
	}
	firstProvider := resolved.Winner.Provider
	unsupportedTransition := extension
	unsupportedTransition.Manifest.Backend.ProtocolVersion = 1
	unsupportedTransition.Manifest.Backend.HostAPIVersion = ""
	if _, err := starter.Start(context.Background(), unsupportedTransition); !errors.Is(err, extensionsruntime.ErrUnsupportedProtocol) {
		t.Fatalf("unsupported protocol admission error = %v", err)
	}
	resolved, err = gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != first.InstanceID {
		t.Fatalf("rejected protocol start changed active service = %#v, %v", resolved, err)
	}

	// Start 的请求 context 只约束启动；候选进程由 exact lifecycle 持有。
	stageCtx, cancelStage := context.WithCancel(context.Background())
	secondTarget, err := starter.StartInstance(stageCtx, extension)
	if err != nil {
		t.Fatal(err)
	}
	cancelStage()
	second := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: secondTarget.InstanceID}
	if second.InstanceID == "" || second.InstanceID == first.InstanceID {
		t.Fatalf("exact identities first=%#v second=%#v", first, second)
	}
	assertProtocolInstanceState(t, starter, second, extensionsruntime.ProtocolRuntimeStaged)
	if health, err := starter.HealthInstance(context.Background(), second); err != nil || !health.OK {
		t.Fatalf("staged health = %#v, %v", health, err)
	}
	stillFirst, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || stillFirst.Winner.InstanceID != first.InstanceID {
		t.Fatalf("staged process changed active registry = %#v, %v", stillFirst, err)
	}

	published, err := starter.PublishInstance(context.Background(), second)
	if err != nil || published.State != extensionsruntime.ProtocolRuntimePublished {
		t.Fatalf("publish second = %#v, %v", published, err)
	}
	assertProtocolInstanceState(t, starter, first, extensionsruntime.ProtocolRuntimeRetained)
	if health, err := starter.HealthInstance(context.Background(), first); err != nil || !health.OK {
		t.Fatalf("retained old process health = %#v, %v", health, err)
	}
	resolved, err = gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != second.InstanceID {
		t.Fatalf("second published service = %#v, %v", resolved, err)
	}
	oldOutput, oldDetail, err := firstProvider.Invoke(
		context.Background(), nil, "runtime.v2.service.echo", "1.0.0", "echo",
		&protocolwire.TypedDocument{SchemaId: "runtime.v2.service.echo.request", SchemaVersion: "1"},
	)
	if err != nil || oldDetail != nil || oldOutput.GetSchemaId() != "runtime.v2.service.echo.response" {
		t.Fatalf("inflight old provider after publish = %#v, %#v, %v", oldOutput, oldDetail, err)
	}

	// Retained process can be republished as an exact rollback without restart.
	rolledBack, err := starter.PublishInstance(context.Background(), first)
	if err != nil || rolledBack.State != extensionsruntime.ProtocolRuntimePublished {
		t.Fatalf("rollback publish = %#v, %v", rolledBack, err)
	}
	assertProtocolInstanceState(t, starter, second, extensionsruntime.ProtocolRuntimeRetained)
	resolved, err = gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != first.InstanceID {
		t.Fatalf("rollback service = %#v, %v", resolved, err)
	}
	if _, err := starter.PublishInstance(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	// Stopping the stale instance must never unregister the replacement.
	if err := starter.StopInstance(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := starter.InspectInstance(first); !errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound) {
		t.Fatalf("stopped inspect error = %v", err)
	}
	if err := starter.StopInstance(context.Background(), first); !errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound) {
		t.Fatalf("stale stop error = %v", err)
	}
	resolved, err = gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != second.InstanceID {
		t.Fatalf("stale stop removed replacement = %#v, %v", resolved, err)
	}

	thirdTarget, err := starter.StartInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	third := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: thirdTarget.InstanceID}
	cancelledPublish, cancelPublish := context.WithCancel(context.Background())
	cancelPublish()
	if _, err := starter.PublishInstance(cancelledPublish, third); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publish error = %v", err)
	}
	assertProtocolInstanceState(t, starter, third, extensionsruntime.ProtocolRuntimeStaged)
	resolved, err = gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != second.InstanceID {
		t.Fatalf("cancelled publish changed replacement = %#v, %v", resolved, err)
	}
	if err := starter.DiscardInstance(context.Background(), third); err != nil {
		t.Fatal(err)
	}
	if _, err := starter.InspectInstance(third); !errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound) {
		t.Fatalf("discarded inspect error = %v", err)
	}
	if err := starter.DiscardInstance(context.Background(), second); !errors.Is(err, extensionsruntime.ErrProtocolInstancePublished) {
		t.Fatalf("published discard error = %v", err)
	}
}

func TestProtocolV2PublishesAndRollsBackCompleteInstanceSet(t *testing.T) {
	extensionA := protocolV2RuntimeSetExtension(t, "runtime.set.a")
	extensionB := protocolV2RuntimeSetExtension(t, "runtime.set.b")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "runtime-set-grant", ImpactDigest: "runtime-set-impact",
		}},
		HostAPI: gateway,
	})
	t.Cleanup(func() {
		_ = starter.Stop(context.Background(), extensionB)
		_ = starter.Stop(context.Background(), extensionA)
	})

	oldA := startProtocolV2RuntimeSetInstance(t, starter, extensionA, true)
	oldB := startProtocolV2RuntimeSetInstance(t, starter, extensionB, true)
	registry := gateway.ProtocolV2ServiceRegistry()
	revision := registry.Revision()
	assertProtocolV2RuntimeSetOwners(t, registry, oldA, oldB)

	newA := startProtocolV2RuntimeSetInstance(t, starter, extensionA, false)
	newB := startProtocolV2RuntimeSetInstance(t, starter, extensionB, false)
	if _, err := starter.HealthInstance(context.Background(), newA); err != nil {
		t.Fatalf("health new a: %v", err)
	}
	if _, err := starter.HealthInstance(context.Background(), newB); err != nil {
		t.Fatalf("health new b: %v", err)
	}
	published, releasePublished, err := starter.PublishInstanceSet(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{newB, newA})
	if err != nil || len(published) != 2 {
		t.Fatalf("publish complete set = %#v, %v", published, err)
	}
	releasePublished.Release()
	if registry.Revision() != revision+1 {
		t.Fatalf("complete set revision = %d, want %d", registry.Revision(), revision+1)
	}
	assertProtocolV2RuntimeSetOwners(t, registry, newA, newB)
	if !starter.RuntimeInstanceSetVisible(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{newB, newA}) {
		t.Fatal("complete protocol/service set is not visible")
	}
	assertProtocolInstanceState(t, starter, oldA, extensionsruntime.ProtocolRuntimeRetained)
	assertProtocolInstanceState(t, starter, oldB, extensionsruntime.ProtocolRuntimeRetained)
	if !gateway.UnregisterProtocolV2ServiceInstance(newA.ExtensionID, newA.InstanceID) {
		t.Fatal("remove one exact service owner")
	}
	if starter.RuntimeInstanceSetVisible(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{newA, newB}) {
		t.Fatal("compensated service graph reported as a visible complete set")
	}
	_, releaseRepair, err := starter.PublishInstanceSet(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{newA, newB})
	if err != nil {
		t.Fatalf("repair complete set: %v", err)
	}
	releaseRepair.Release()
	if !starter.RuntimeInstanceSetVisible(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{newA, newB}) {
		t.Fatal("complete-set republish did not repair service graph")
	}

	rolledBack, releaseRollback, err := starter.PublishInstanceSet(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{oldA, oldB})
	if err != nil || len(rolledBack) != 2 {
		t.Fatalf("rollback complete set = %#v, %v", rolledBack, err)
	}
	releaseRollback.Release()
	if registry.Revision() != revision+4 {
		t.Fatalf("rollback set revision = %d, want %d", registry.Revision(), revision+4)
	}
	assertProtocolV2RuntimeSetOwners(t, registry, oldA, oldB)
	assertProtocolInstanceState(t, starter, newA, extensionsruntime.ProtocolRuntimeRetained)
	assertProtocolInstanceState(t, starter, newB, extensionsruntime.ProtocolRuntimeRetained)
}

func TestProtocolV2InstanceSetFailureLeavesCompleteOldGraph(t *testing.T) {
	extensionA := protocolV2RuntimeSetExtension(t, "runtime.failure.a")
	extensionB := protocolV2RuntimeSetExtension(t, "runtime.failure.b")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "runtime-set-grant", ImpactDigest: "runtime-set-impact",
		}},
		HostAPI: gateway,
	})
	t.Cleanup(func() {
		_ = starter.Stop(context.Background(), extensionB)
		_ = starter.Stop(context.Background(), extensionA)
	})

	oldA := startProtocolV2RuntimeSetInstance(t, starter, extensionA, true)
	oldB := startProtocolV2RuntimeSetInstance(t, starter, extensionB, true)
	newA := startProtocolV2RuntimeSetInstance(t, starter, extensionA, false)
	newB := startProtocolV2RuntimeSetInstance(t, starter, extensionB, false)
	if _, err := starter.HealthInstance(context.Background(), newA); err != nil {
		t.Fatalf("health new a: %v", err)
	}
	registry := gateway.ProtocolV2ServiceRegistry()
	revision := registry.Revision()
	_, releaseFailed, err := starter.PublishInstanceSet(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{newA, newB})
	if releaseFailed != nil {
		releaseFailed.Release()
	}
	if !errors.Is(err, extensionsruntime.ErrProtocolInstanceNotReady) {
		t.Fatalf("unready complete-set error = %v", err)
	}
	if registry.Revision() != revision {
		t.Fatalf("failed complete set advanced revision: %d -> %d", revision, registry.Revision())
	}
	assertProtocolV2RuntimeSetOwners(t, registry, oldA, oldB)
	assertProtocolInstanceState(t, starter, newA, extensionsruntime.ProtocolRuntimeStaged)
	assertProtocolInstanceState(t, starter, newB, extensionsruntime.ProtocolRuntimeStaged)
}

func TestProtocolV2RuntimeSetLeaseKeepsQueuedRawWriterOutOfRollback(t *testing.T) {
	extensionA := protocolV2RuntimeSetExtension(t, "runtime.lease.a")
	extensionB := protocolV2RuntimeSetExtension(t, "runtime.lease.b")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "runtime-set-grant", ImpactDigest: "runtime-set-impact",
		}},
		HostAPI: gateway,
	})
	t.Cleanup(func() {
		_ = starter.Stop(context.Background(), extensionB)
		_ = starter.Stop(context.Background(), extensionA)
	})

	oldA := startProtocolV2RuntimeSetInstance(t, starter, extensionA, true)
	oldB := startProtocolV2RuntimeSetInstance(t, starter, extensionB, true)
	newA := startProtocolV2RuntimeSetInstance(t, starter, extensionA, false)
	newB := startProtocolV2RuntimeSetInstance(t, starter, extensionB, false)
	if _, err := starter.HealthInstance(context.Background(), newA); err != nil {
		t.Fatalf("health new a: %v", err)
	}
	if _, err := starter.HealthInstance(context.Background(), newB); err != nil {
		t.Fatalf("health new b: %v", err)
	}

	registry := gateway.ProtocolV2ServiceRegistry()
	published, lease, err := starter.PublishInstanceSet(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{newA, newB})
	if err != nil || len(published) != 2 || lease == nil {
		t.Fatalf("publish forward set = %#v lease=%v err=%v", published, lease != nil, err)
	}
	defer lease.Release()
	assertProtocolV2RuntimeSetOwners(t, registry, newA, newB)

	rawSet := protocolV2RawWriterSet(t, registry, "runtime.raw.writer")
	rawStarted := make(chan struct{})
	rawDone := make(chan error, 1)
	go func() {
		close(rawStarted)
		rawDone <- registry.ReplaceRuntimeSet(rawSet)
	}()
	<-rawStarted
	select {
	case err := <-rawDone:
		t.Fatalf("raw writer crossed forward lease before rollback: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	restored, err := lease.Restore(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{oldA, oldB})
	if err != nil || len(restored) != 2 {
		t.Fatalf("restore old set under forward lease = %#v, %v", restored, err)
	}
	assertProtocolV2RuntimeSetOwners(t, registry, oldA, oldB)
	assertProtocolInstanceState(t, starter, oldA, extensionsruntime.ProtocolRuntimePublished)
	assertProtocolInstanceState(t, starter, oldB, extensionsruntime.ProtocolRuntimePublished)
	select {
	case err := <-rawDone:
		t.Fatalf("raw writer crossed continuous lease during rollback: %v", err)
	default:
	}

	lease.Release()
	if err := <-rawDone; !errors.Is(err, hostapi.ErrServiceRuntimeSetConflict) {
		t.Fatalf("queued raw writer after rollback error = %v", err)
	}
	assertProtocolV2RuntimeSetOwners(t, registry, oldA, oldB)
}

func TestProtocolV2RuntimeSetWriterWaitCancellationReleasesPointerReaders(t *testing.T) {
	extensionA := protocolV2RuntimeSetExtension(t, "runtime.cancel.a")
	extensionB := protocolV2RuntimeSetExtension(t, "runtime.cancel.b")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "runtime-set-grant", ImpactDigest: "runtime-set-impact",
		}},
		HostAPI: gateway,
	})
	t.Cleanup(func() {
		_ = starter.Stop(context.Background(), extensionB)
		_ = starter.Stop(context.Background(), extensionA)
	})

	oldA := startProtocolV2RuntimeSetInstance(t, starter, extensionA, true)
	oldB := startProtocolV2RuntimeSetInstance(t, starter, extensionB, true)
	newA := startProtocolV2RuntimeSetInstance(t, starter, extensionA, false)
	newB := startProtocolV2RuntimeSetInstance(t, starter, extensionB, false)
	if _, err := starter.HealthInstance(context.Background(), newA); err != nil {
		t.Fatalf("health new a: %v", err)
	}
	if _, err := starter.HealthInstance(context.Background(), newB); err != nil {
		t.Fatalf("health new b: %v", err)
	}

	registry := gateway.ProtocolV2ServiceRegistry()
	rawTransaction, err := registry.PrepareRuntimeSet(protocolV2RawWriterSet(t, registry, "runtime.cancel.writer"))
	if err != nil {
		t.Fatalf("prepare blocking raw set: %v", err)
	}
	rawLease, err := rawTransaction.CommitAndAcquire()
	if err != nil {
		t.Fatalf("publish blocking raw set: %v", err)
	}
	defer rawLease.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, lease, err := starter.PublishInstanceSet(ctx, []extensionsruntime.RuntimeInstanceIdentity{newA, newB}); !errors.Is(err, context.DeadlineExceeded) || lease != nil {
		if lease != nil {
			lease.Release()
		}
		t.Fatalf("queued production publish = lease:%v err:%v", lease != nil, err)
	}

	readerDone := make(chan error, 1)
	go func() {
		_, err := starter.InspectInstance(oldA)
		readerDone <- err
	}()
	select {
	case err := <-readerDone:
		if err != nil {
			t.Fatalf("old pointer after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled service writer wait retained ProtocolStarter pointer lock")
	}

	rawLease.Release()
	repaired, repairLease, err := starter.PublishInstanceSet(context.Background(), []extensionsruntime.RuntimeInstanceIdentity{oldA, oldB})
	if err != nil || len(repaired) != 2 || repairLease == nil {
		t.Fatalf("repair old production set = %#v lease=%v err=%v", repaired, repairLease != nil, err)
	}
	repairLease.Release()
	assertProtocolV2RuntimeSetOwners(t, registry, oldA, oldB)
}

func protocolV2RawWriterSet(t *testing.T, registry *hostapi.ServiceRegistry, extensionID string) []hostapi.ServiceRuntimePublication {
	t.Helper()
	resolved, err := registry.ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil {
		t.Fatalf("resolve raw writer provider: %v", err)
	}
	rawRegistration := resolved.Winner
	rawRegistration.ExtensionID = extensionID
	rawRegistration.InstanceID = extensionID + "-instance"
	return []hostapi.ServiceRuntimePublication{{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0",
		ArtifactDigest: extensionID + "-digest", TrustGrantID: extensionID + "-grant",
		RuntimeEpoch: 1, InstanceID: rawRegistration.InstanceID,
		Registrations: []hostapi.ServiceRegistration{rawRegistration},
	}}
}

func protocolV2RuntimeSetExtension(t *testing.T, extensionID string) extensions.Extension {
	t.Helper()
	extension := protocolV2TestExtension(t, "v2")
	extension.ID = extensionID
	extension.Name = extensionID
	extension.Manifest.ID = extensionID
	return extension
}

func startProtocolV2RuntimeSetInstance(
	t *testing.T,
	starter *extensionsruntime.ProtocolStarter,
	extension extensions.Extension,
	publish bool,
) extensionsruntime.RuntimeInstanceIdentity {
	t.Helper()
	var target extensionsruntime.RouteTarget
	var err error
	if publish {
		target, err = starter.Start(context.Background(), extension)
	} else {
		target, err = starter.StartInstance(context.Background(), extension)
	}
	if err != nil {
		t.Fatalf("start %s publish=%v: %v", extension.ID, publish, err)
	}
	return extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: target.InstanceID}
}

func assertProtocolV2RuntimeSetOwners(
	t *testing.T,
	registry *hostapi.ServiceRegistry,
	want ...extensionsruntime.RuntimeInstanceIdentity,
) {
	t.Helper()
	for _, identity := range want {
		snapshot, ok, err := registry.ExtensionSnapshot(identity.ExtensionID)
		if err != nil || !ok || snapshot.InstanceID != identity.InstanceID {
			t.Fatalf("service owner %s = %#v, %v", identity.ExtensionID, snapshot, err)
		}
	}
}

func TestUnsupportedProtocolIsRejectedBeforeStagingProtocolV2(t *testing.T) {
	unsupported := protocolV2TestExtension(t, "unsupported")
	unsupported.Manifest.Backend.ProtocolVersion = 1
	unsupported.Manifest.Backend.HostAPIVersion = ""
	candidate := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	if _, err := starter.Start(context.Background(), unsupported); !errors.Is(err, extensionsruntime.ErrUnsupportedProtocol) {
		t.Fatalf("unsupported protocol start error = %v", err)
	}
	target, err := starter.StartInstance(context.Background(), candidate)
	if err != nil {
		t.Fatalf("stage v2 after rejecting v1: %v", err)
	}
	t.Cleanup(func() {
		_ = starter.DiscardInstance(context.Background(), extensionsruntime.RuntimeInstanceIdentity{
			ExtensionID: candidate.ID, InstanceID: target.InstanceID,
		})
	})
}

func TestProtocolV2ConcurrentPublishAndStaleStopKeepsReplacement(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	firstTarget, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	secondTarget, err := starter.StartInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	first := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: firstTarget.InstanceID}
	second := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: secondTarget.InstanceID}

	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		_, err := starter.PublishInstance(context.Background(), second)
		errorsCh <- err
	}()
	go func() {
		defer group.Done()
		<-start
		errorsCh <- starter.StopInstance(context.Background(), first)
	}()
	close(start)
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertProtocolInstanceState(t, starter, second, extensionsruntime.ProtocolRuntimePublished)
	resolved, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != second.InstanceID {
		t.Fatalf("concurrent replacement service = %#v, %v", resolved, err)
	}
}

func TestProtocolV2UnpublishedStartDefersReadinessUntilHealthAndPublish(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	failing := protocolV2TestExtension(t, "v2-readiness-fail")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust:   staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}},
		HostAPI: gateway,
	})
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	activeTarget, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}

	// 兼容 Start 仍是 launch+readiness+publish，readiness 失败不能替换活动实例。
	if _, err := starter.Start(context.Background(), failing); err == nil {
		t.Fatal("compatibility Start published a readiness-failing process")
	}
	resolved, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != activeTarget.InstanceID {
		t.Fatalf("failed compatibility start changed registry = %#v, %v", resolved, err)
	}

	// Staged start intentionally stops before readiness so install hooks can run first.
	candidateTarget, err := starter.StartInstance(context.Background(), failing)
	if err != nil {
		t.Fatalf("stage readiness-failing candidate: %v", err)
	}
	candidate := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: failing.ID, InstanceID: candidateTarget.InstanceID}
	candidateSnapshot, err := starter.InspectInstance(candidate)
	if err != nil || candidateSnapshot.State != extensionsruntime.ProtocolRuntimeStaged || !candidateSnapshot.Healthy ||
		candidateSnapshot.Ready || candidateSnapshot.ReadinessChecked {
		t.Fatalf("staged readiness candidate = %#v, %v", candidateSnapshot, err)
	}
	if _, err := starter.HealthInstance(context.Background(), candidate); !errors.Is(err, extensionsruntime.ErrProtocolInstanceNotReady) {
		t.Fatalf("readiness health error = %v", err)
	}
	if _, err := starter.PublishInstance(context.Background(), candidate); !errors.Is(err, extensionsruntime.ErrProtocolInstanceNotReady) {
		t.Fatalf("readiness publish error = %v", err)
	}
	candidateSnapshot, err = starter.InspectInstance(candidate)
	if err != nil || candidateSnapshot.State != extensionsruntime.ProtocolRuntimeStaged || !candidateSnapshot.Healthy ||
		candidateSnapshot.Ready || !candidateSnapshot.ReadinessChecked {
		t.Fatalf("failed readiness candidate state = %#v, %v", candidateSnapshot, err)
	}
	resolved, err = gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != activeTarget.InstanceID {
		t.Fatalf("readiness failure leaked registry = %#v, %v", resolved, err)
	}
	if err := starter.DiscardInstance(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
}

func assertProtocolInstanceState(
	t *testing.T,
	starter *extensionsruntime.ProtocolStarter,
	identity extensionsruntime.RuntimeInstanceIdentity,
	state extensionsruntime.ProtocolRuntimeInstanceState,
) {
	t.Helper()
	snapshot, err := starter.InspectInstance(identity)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Identity != identity || snapshot.State != state || !snapshot.Healthy || snapshot.ProtocolVersion != 2 ||
		snapshot.Target.InstanceID != identity.InstanceID || snapshot.StartedAt.IsZero() {
		t.Fatalf("instance snapshot = %#v, want state %s", snapshot, state)
	}
	if state != extensionsruntime.ProtocolRuntimeStaged && (!snapshot.Ready || !snapshot.ReadinessChecked) {
		t.Fatalf("published/retained instance is not ready: %#v", snapshot)
	}
}
