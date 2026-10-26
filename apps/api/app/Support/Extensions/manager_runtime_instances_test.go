package extensionsruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestManagerRuntimeInstanceRestartRetainsDrainingGate(t *testing.T) {
	starter := &managerRuntimeStarter{results: []managerRuntimeStartResult{
		{target: RouteTarget{BaseURL: "http://127.0.0.1:41001", InstanceID: "instance-1"}},
		{target: RouteTarget{BaseURL: "http://127.0.0.1:41002", InstanceID: "instance-2"}},
	}}
	manager := NewManager(ManagerConfig{Starter: starter})
	first := managerRuntimeExtension("retained.plugin", "1.0.0", "digest-1")
	first.ActiveVersionID = 11
	if err := manager.Start(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	firstIdentity := RuntimeInstanceIdentity{ExtensionID: first.ID, InstanceID: "instance-1"}
	firstRoute := acquireManagerRuntimeCall(t, manager, firstIdentity, RuntimeCallRoute)

	second := managerRuntimeExtension(first.ID, "2.0.0", "digest-2")
	second.ActiveVersionID = 22
	if err := manager.Start(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	secondIdentity := RuntimeInstanceIdentity{ExtensionID: second.ID, InstanceID: "instance-2"}

	oldSnapshot, err := manager.InspectRuntimeInstance(firstIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if oldSnapshot.Active || !oldSnapshot.Admission.Draining || oldSnapshot.Admission.Forced ||
		oldSnapshot.Admission.ActiveTotal != 1 || oldSnapshot.ExtensionVersion != "1.0.0" ||
		oldSnapshot.ArtifactDigest != "digest-1" || oldSnapshot.VersionID != 11 || oldSnapshot.Target.InstanceID != "instance-1" {
		t.Fatalf("retained instance = %#v", oldSnapshot)
	}
	active, err := manager.ActiveRuntimeInstance(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !active.Active || active.Identity != secondIdentity || active.ExtensionVersion != "2.0.0" ||
		active.ArtifactDigest != "digest-2" || active.VersionID != 22 || active.Admission.Draining {
		t.Fatalf("active replacement = %#v", active)
	}
	if _, err := manager.AcquireRuntimeCall(context.Background(), firstIdentity, RuntimeCallRoute); !errors.Is(err, ErrRuntimeInstanceNotActive) {
		t.Fatalf("old ordinary acquire error = %v", err)
	}
	cleanup := acquireManagerRuntimeCall(t, manager, firstIdentity, RuntimeCallLifecycleCleanup)
	secondRoute := acquireManagerRuntimeCall(t, manager, secondIdentity, RuntimeCallRoute)

	firstRoute.Release()
	cleanup.Release()
	secondRoute.Release()
}

func TestManagerForceDrainOldInstanceDoesNotCancelReplacement(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "force.plugin")
	oldIdentity := RuntimeInstanceIdentity{ExtensionID: "force.plugin", InstanceID: "instance-1"}
	newIdentity := RuntimeInstanceIdentity{ExtensionID: "force.plugin", InstanceID: "instance-2"}
	oldCleanup := acquireManagerRuntimeCall(t, manager, oldIdentity, RuntimeCallLifecycleCleanup)
	newRoute := acquireManagerRuntimeCall(t, manager, newIdentity, RuntimeCallRoute)
	cause := errors.New("forced uninstall timeout")

	snapshot, err := manager.ForceDrain(oldIdentity, cause)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Forced || !errors.Is(snapshot.ForceCause, cause) {
		t.Fatalf("forced snapshot = %#v", snapshot)
	}
	select {
	case <-oldCleanup.Context.Done():
		if !errors.Is(context.Cause(oldCleanup.Context), cause) {
			t.Fatalf("old cancellation cause = %v", context.Cause(oldCleanup.Context))
		}
	case <-time.After(time.Second):
		t.Fatal("old retained call was not cancelled")
	}
	select {
	case <-newRoute.Context.Done():
		t.Fatalf("replacement was cancelled: %v", context.Cause(newRoute.Context))
	default:
	}
	newSnapshot, err := manager.InspectRuntimeInstance(newIdentity)
	if err != nil || newSnapshot.Admission.Forced || newSnapshot.Admission.Draining {
		t.Fatalf("replacement snapshot = %#v, %v", newSnapshot, err)
	}

	oldCleanup.Release()
	newRoute.Release()
}

func TestManagerContextTransitionsBoundPerExtensionLifecycleWait(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "context-lock.plugin")
	identity := RuntimeInstanceIdentity{ExtensionID: "context-lock.plugin", InstanceID: "instance-2"}

	unlock := manager.lockRuntimeLifecycle(identity.ExtensionID)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, err := manager.BeginDrainContext(ctx, identity)
	cancel()
	unlock()
	if !errors.Is(err, context.DeadlineExceeded) || !manager.RuntimeInstanceAvailable(identity) {
		t.Fatalf("bounded begin drain = %v, available=%t", err, manager.RuntimeInstanceAvailable(identity))
	}

	if _, err := manager.BeginDrain(identity); err != nil {
		t.Fatal(err)
	}
	unlock = manager.lockRuntimeLifecycle(identity.ExtensionID)
	ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, err = manager.ResumeRuntimeInstanceContext(ctx, identity)
	cancel()
	unlock()
	if !errors.Is(err, context.DeadlineExceeded) || manager.RuntimeInstanceAvailable(identity) {
		t.Fatalf("bounded resume = %v, available=%t", err, manager.RuntimeInstanceAvailable(identity))
	}
	if _, err := manager.ResumeRuntimeInstance(identity); err != nil || !manager.RuntimeInstanceAvailable(identity) {
		t.Fatalf("resume after lifecycle release = %v, available=%t", err, manager.RuntimeInstanceAvailable(identity))
	}
}

func TestManagerWaitAndRemoveExactRetainedInstance(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "remove.plugin")
	oldIdentity := RuntimeInstanceIdentity{ExtensionID: "remove.plugin", InstanceID: "instance-1"}
	newIdentity := RuntimeInstanceIdentity{ExtensionID: "remove.plugin", InstanceID: "instance-2"}
	cleanup := acquireManagerRuntimeCall(t, manager, oldIdentity, RuntimeCallLifecycleCleanup)

	if err := manager.RemoveRuntimeInstance(oldIdentity); !errors.Is(err, ErrRuntimeInstanceBusy) {
		t.Fatalf("busy removal error = %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.WaitDrain(waitCtx, oldIdentity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy wait error = %v", err)
	}
	cleanup.Release()
	if err := manager.WaitDrain(context.Background(), oldIdentity); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveRuntimeInstance(oldIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InspectRuntimeInstance(oldIdentity); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("removed inspect error = %v", err)
	}
	if err := manager.RemoveRuntimeInstance(newIdentity); !errors.Is(err, ErrRuntimeInstanceActive) {
		t.Fatalf("active removal error = %v", err)
	}
	active, err := manager.ActiveRuntimeInstance("remove.plugin")
	if err != nil || active.Identity != newIdentity || !active.Active {
		t.Fatalf("active replacement = %#v, %v", active, err)
	}
}

func TestManagerStaleRuntimeInstanceOperationsNeverTouchReplacement(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "stale.plugin")
	stale := RuntimeInstanceIdentity{ExtensionID: "stale.plugin", InstanceID: "missing-instance"}
	activeIdentity := RuntimeInstanceIdentity{ExtensionID: "stale.plugin", InstanceID: "instance-2"}

	if _, err := manager.AcquireRuntimeCall(context.Background(), stale, RuntimeCallLifecycleCleanup); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("stale acquire error = %v", err)
	}
	if _, err := manager.BeginDrain(stale); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("stale begin drain error = %v", err)
	}
	if err := manager.WaitDrain(context.Background(), stale); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("stale wait error = %v", err)
	}
	if _, err := manager.ForceDrain(stale, errors.New("stale force")); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("stale force error = %v", err)
	}
	if _, err := manager.InspectRuntimeInstance(stale); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("stale inspect error = %v", err)
	}
	if err := manager.RemoveRuntimeInstance(stale); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("stale remove error = %v", err)
	}

	active, err := manager.InspectRuntimeInstance(activeIdentity)
	if err != nil || !active.Active || active.Admission.Draining || active.Admission.Forced {
		t.Fatalf("replacement changed after stale operations: %#v, %v", active, err)
	}
}

func TestManagerStopRetainsAndDeactivatesExactGate(t *testing.T) {
	starter := &managerRuntimeStarter{results: []managerRuntimeStartResult{{
		target: RouteTarget{BaseURL: "http://127.0.0.1:41001", InstanceID: "instance-1"},
	}}}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerRuntimeExtension("stop.plugin", "1.0.0", "digest-1")
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	identity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: "instance-1"}
	lease := acquireManagerRuntimeCall(t, manager, identity, RuntimeCallJob)
	if err := manager.Stop(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	if starter.stopCount() != 1 {
		t.Fatalf("starter stop count = %d", starter.stopCount())
	}
	if _, err := manager.ActiveRuntimeInstance(extension.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("stopped active lookup error = %v", err)
	}
	snapshot, err := manager.InspectRuntimeInstance(identity)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Active || !snapshot.Admission.Draining || snapshot.Admission.ActiveTotal != 1 {
		t.Fatalf("stopped retained instance = %#v", snapshot)
	}
	if _, err := manager.AcquireRuntimeCall(context.Background(), identity, RuntimeCallRoute); !errors.Is(err, ErrRuntimeInstanceNotActive) {
		t.Fatalf("stopped ordinary acquire error = %v", err)
	}
	cleanup := acquireManagerRuntimeCall(t, manager, identity, RuntimeCallLifecycleCleanup)
	lease.Release()
	cleanup.Release()
	if err := manager.WaitDrain(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
}

func TestManagerLegacyRuntimeGetsUniqueFallbackIdentity(t *testing.T) {
	starter := &managerRuntimeStarter{results: []managerRuntimeStartResult{
		{target: RouteTarget{BaseURL: "http://127.0.0.1:41001"}},
		{target: RouteTarget{BaseURL: "http://127.0.0.1:41002"}},
	}}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerRuntimeExtension("legacy.plugin", "1.0.0", "digest-1")
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	first, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity.InstanceID == "" || first.Target.InstanceID != first.Identity.InstanceID {
		t.Fatalf("first fallback identity = %#v", first)
	}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	second, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Identity.InstanceID == "" || second.Identity.InstanceID == first.Identity.InstanceID ||
		second.Target.InstanceID != second.Identity.InstanceID {
		t.Fatalf("fallback identities first=%#v second=%#v", first.Identity, second.Identity)
	}
	retained, err := manager.InspectRuntimeInstance(first.Identity)
	if err != nil || retained.Active || !retained.Admission.Draining {
		t.Fatalf("legacy retained instance = %#v, %v", retained, err)
	}
}

func TestManagerFailedReplacementKeepsCurrentInstanceAdmissible(t *testing.T) {
	starter := &managerRuntimeStarter{results: []managerRuntimeStartResult{
		{target: RouteTarget{InstanceID: "instance-1"}},
		{err: errors.New("replacement failed readiness")},
	}}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerRuntimeExtension("rollback.plugin", "1.0.0", "digest-1")
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	replacement := managerRuntimeExtension(extension.ID, "2.0.0", "digest-2")
	if err := manager.Start(context.Background(), replacement); err == nil {
		t.Fatal("expected replacement failure")
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || active.Identity.InstanceID != "instance-1" || active.Admission.Draining {
		t.Fatalf("active after failed replacement = %#v, %v", active, err)
	}
	lease := acquireManagerRuntimeCall(t, manager, active.Identity, RuntimeCallRoute)
	lease.Release()
}

func TestManagerConcurrentRestartFencesOldOrdinaryAdmissions(t *testing.T) {
	starter := &managerRuntimeStarter{results: []managerRuntimeStartResult{
		{target: RouteTarget{InstanceID: "instance-1"}},
		{target: RouteTarget{InstanceID: "instance-2"}},
	}}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerRuntimeExtension("concurrent.plugin", "1.0.0", "digest-1")
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	oldIdentity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: "instance-1"}
	start := make(chan struct{})
	results := make(chan error, 128)
	var group sync.WaitGroup
	for range 128 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			lease, err := manager.AcquireRuntimeCall(context.Background(), oldIdentity, RuntimeCallRoute)
			if lease != nil {
				lease.Release()
			}
			results <- err
		}()
	}
	close(start)
	if err := manager.Start(context.Background(), managerRuntimeExtension(extension.ID, "2.0.0", "digest-2")); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, ErrRuntimeInstanceNotActive) && !errors.Is(err, ErrRuntimeAdmissionDraining) {
			t.Fatalf("concurrent acquire error = %v", err)
		}
	}
	for range 128 {
		if _, err := manager.AcquireRuntimeCall(context.Background(), oldIdentity, RuntimeCallRoute); !errors.Is(err, ErrRuntimeInstanceNotActive) {
			t.Fatalf("post-restart old acquire error = %v", err)
		}
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || active.Identity.InstanceID != "instance-2" || active.Admission.Draining {
		t.Fatalf("active after concurrent restart = %#v, %v", active, err)
	}
}

type managerRuntimeStartResult struct {
	target RouteTarget
	err    error
}

type managerRuntimeStarter struct {
	mu      sync.Mutex
	results []managerRuntimeStartResult
	starts  int
	stops   int
}

func (s *managerRuntimeStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starts >= len(s.results) {
		return RouteTarget{}, errors.New("unexpected runtime start")
	}
	result := s.results[s.starts]
	s.starts++
	return result.target, result.err
}

func (s *managerRuntimeStarter) Stop(context.Context, extensions.Extension) error {
	s.mu.Lock()
	s.stops++
	s.mu.Unlock()
	return nil
}

func (s *managerRuntimeStarter) stopCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stops
}

func managerRuntimeExtension(id, version, digest string) extensions.Extension {
	extension := runtimeExtension(id)
	extension.Version = version
	extension.Manifest.Version = version
	extension.PackageDigest = digest
	return extension
}

func newTwoInstanceRuntimeManager(t *testing.T, extensionID string) *Manager {
	t.Helper()
	starter := &managerRuntimeStarter{results: []managerRuntimeStartResult{
		{target: RouteTarget{InstanceID: "instance-1"}},
		{target: RouteTarget{InstanceID: "instance-2"}},
	}}
	manager := NewManager(ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), managerRuntimeExtension(extensionID, "1.0.0", "digest-1")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), managerRuntimeExtension(extensionID, "2.0.0", "digest-2")); err != nil {
		t.Fatal(err)
	}
	return manager
}

func acquireManagerRuntimeCall(t *testing.T, manager *Manager, identity RuntimeInstanceIdentity, class RuntimeCallClass) *RuntimeAdmissionLease {
	t.Helper()
	lease, err := manager.AcquireRuntimeCall(context.Background(), identity, class)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
