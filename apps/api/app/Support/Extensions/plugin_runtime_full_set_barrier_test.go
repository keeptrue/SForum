package extensionsruntime

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestManagerRuntimeSetBarrierSerializesFullSetWithLegacyStartStop(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	old := inventory.seed(t, "barrier.upgrade", 1, "1.0.0", "barrier-upgrade-v1")
	removed := inventory.seed(t, "barrier.stop", 2, "1.0.0", "barrier-stop-v1")
	legacy := managerStagedExtension("barrier.start", "1.0.0", pluginRuntimeFullSetDigest("barrier-start-v1"))
	inventory.seed(t, old.ID, 3, "2.0.0", "barrier-upgrade-v2")
	if err := manager.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), removed); err != nil {
		t.Fatal(err)
	}

	starter.inner.publishStarted = make(chan struct{}, 3)
	starter.inner.publishContinue = make(chan struct{})
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applyDone := make(chan error, 1)
	go func() {
		_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
			extensions.PluginRuntimePublicationUpgrade,
			inventory.member(old.ID, 3, "2.0.0", "barrier-upgrade-v2"),
			inventory.member(removed.ID, 2, "1.0.0", "barrier-stop-v1"),
		))
		applyDone <- err
	}()
	<-starter.inner.publishStarted

	startDone := make(chan error, 1)
	stopDone := make(chan error, 1)
	go func() { startDone <- manager.Start(context.Background(), legacy) }()
	go func() { stopDone <- manager.Stop(context.Background(), removed) }()
	assertTransitionBlocked(t, startDone, "legacy Start")
	assertTransitionBlocked(t, stopDone, "legacy Stop")
	close(starter.inner.publishContinue)
	assertTransitionResult(t, applyDone, "full-set apply")
	assertTransitionResult(t, startDone, "legacy Start")
	assertTransitionResult(t, stopDone, "legacy Stop")

	upgraded, err := manager.ActiveRuntimeInstance(old.ID)
	if err != nil || upgraded.ExtensionVersion != "2.0.0" {
		t.Fatalf("upgraded runtime = %#v, %v", upgraded, err)
	}
	if _, err := manager.ActiveRuntimeInstance(legacy.ID); err != nil {
		t.Fatalf("serialized legacy Start did not run: %v", err)
	}
	if _, err := manager.ActiveRuntimeInstance(removed.ID); err == nil {
		t.Fatal("serialized legacy Stop did not run")
	}
}

func TestManagerRuntimeSetBarrierSerializesFullSetWithStagedPublishAndStop(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	old := inventory.seed(t, "barrier.fullset", 1, "1.0.0", "barrier-fullset-v1")
	inventory.seed(t, old.ID, 2, "2.0.0", "barrier-fullset-v2")
	if err := manager.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	publishCandidate, err := manager.StageRuntimeInstance(
		context.Background(), managerStagedExtension("barrier.publish", "1.0.0", pluginRuntimeFullSetDigest("barrier-publish")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(context.Background(), publishCandidate.Identity); err != nil {
		t.Fatal(err)
	}
	stopCandidate, err := manager.StageRuntimeInstance(
		context.Background(), managerStagedExtension("barrier.stop-staged", "1.0.0", pluginRuntimeFullSetDigest("barrier-stop-staged")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(stopCandidate.Identity); err != nil {
		t.Fatal(err)
	}

	starter.inner.publishStarted = make(chan struct{}, 3)
	starter.inner.publishContinue = make(chan struct{})
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applyDone := make(chan error, 1)
	go func() {
		_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
			extensions.PluginRuntimePublicationUpgrade,
			inventory.member(old.ID, 2, "2.0.0", "barrier-fullset-v2"),
		))
		applyDone <- err
	}()
	<-starter.inner.publishStarted

	publishDone := make(chan error, 1)
	stopDone := make(chan error, 1)
	go func() {
		_, err := manager.PublishRuntimeInstance(context.Background(), publishCandidate.Identity)
		publishDone <- err
	}()
	go func() { stopDone <- manager.StopRuntimeInstance(context.Background(), stopCandidate.Identity) }()
	assertTransitionBlocked(t, publishDone, "staged PublishRuntimeInstance")
	assertTransitionBlocked(t, stopDone, "staged StopRuntimeInstance")
	close(starter.inner.publishContinue)
	assertTransitionResult(t, applyDone, "full-set apply")
	assertTransitionResult(t, publishDone, "staged PublishRuntimeInstance")
	assertTransitionResult(t, stopDone, "staged StopRuntimeInstance")

	upgraded, err := manager.ActiveRuntimeInstance(old.ID)
	if err != nil || upgraded.ExtensionVersion != "2.0.0" {
		t.Fatalf("full-set runtime = %#v, %v", upgraded, err)
	}
	if _, err := manager.ActiveRuntimeInstance(publishCandidate.Identity.ExtensionID); err != nil {
		t.Fatalf("staged publish did not run after barrier: %v", err)
	}
	if _, err := manager.InspectRuntimeInstance(stopCandidate.Identity); err == nil {
		t.Fatal("staged stop did not run after barrier")
	}
}

func TestManagerRuntimeSetBarrierReconcileAndCloseDoNotReenter(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{
		Starter: starter,
		// A non-nil coordinator with no store calls the supplied RuntimeManager;
		// this exercises the no-barrier adapter rather than only the nil branch.
		Activation: extensions.NewActivationCoordinator(nil),
		BootID:     "barrier-boot",
	})
	a := managerStagedExtension("barrier.reconcile.a", "1.0.0", pluginRuntimeFullSetDigest("reconcile-a"))
	b := managerStagedExtension("barrier.reconcile.b", "1.0.0", pluginRuntimeFullSetDigest("reconcile-b"))
	a.Status = extensions.StatusEnabled
	b.Status = extensions.StatusEnabled

	reconcileDone := make(chan struct{})
	go func() {
		manager.Reconcile(context.Background(), []extensions.Extension{a, b})
		close(reconcileDone)
	}()
	assertTransitionClosed(t, reconcileDone, "Reconcile")
	if _, err := manager.ActiveRuntimeInstance(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ActiveRuntimeInstance(b.ID); err != nil {
		t.Fatal(err)
	}

	closeDone := make(chan struct{})
	go func() {
		manager.Close(context.Background())
		close(closeDone)
	}()
	assertTransitionClosed(t, closeDone, "Close")
	if _, err := manager.ActiveRuntimeInstance(a.ID); err == nil {
		t.Fatal("Close left a active")
	}
	if _, err := manager.ActiveRuntimeInstance(b.ID); err == nil {
		t.Fatal("Close left b active")
	}
}

func TestPluginRuntimeFullSetBarrierWaitHonorsHeartbeatCancellation(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	ext := inventory.seed(t, "barrier.cancel", 1, "1.0.0", "barrier-cancel")
	holderUnlock, err := manager.lockRuntimeSetTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holderUnlock()

	ctx, cancel := context.WithCancel(context.Background())
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	done := make(chan error, 1)
	go func() {
		_, err := applier.ApplyPluginRuntimeFullSet(ctx, inventory.publication(
			extensions.PluginRuntimePublicationEnable,
			inventory.member(ext.ID, 1, "1.0.0", "barrier-cancel"),
		))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("barrier cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("full-set apply ignored heartbeat cancellation while waiting for barrier")
	}
	if starter.startCount() != 0 {
		t.Fatal("cancelled barrier waiter started plugin code")
	}
}

func TestPluginRuntimeFullSetSerializesDirectHookRegistrationTransaction(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	desired := inventory.seed(t, "hookset.desired", 1, "1.0.0", "hookset-desired")
	foreign := managerStagedExtension("hookset.foreign", "1.0.0", pluginRuntimeFullSetDigest("hookset-foreign"))
	bus := manager.HookBus()

	// Hold the second registry lock so RegisterRuntime is deterministically
	// suspended after taking HookBus.mu. Before the fix it did not own bus.mu at
	// this point and could overwrite only plugins after the full-set commit.
	bus.registry.mu.Lock()
	registerDone := make(chan error, 1)
	go func() { registerDone <- bus.RegisterRuntime(foreign, "foreign-runtime") }()
	deadline := time.Now().Add(time.Second)
	for {
		if !bus.mu.TryLock() {
			break
		}
		bus.mu.Unlock()
		if time.Now().After(deadline) {
			bus.registry.mu.Unlock()
			t.Fatal("direct RegisterRuntime did not acquire HookBus transaction lock")
		}
		time.Sleep(time.Millisecond)
	}

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applyDone := make(chan error, 1)
	go func() {
		_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
			extensions.PluginRuntimePublicationEnable,
			inventory.member(desired.ID, 1, "1.0.0", "hookset-desired"),
		))
		applyDone <- err
	}()
	assertTransitionBlocked(t, applyDone, "full-set behind direct HookBus transaction")
	bus.registry.mu.Unlock()
	assertTransitionResult(t, registerDone, "direct HookBus registration")
	assertTransitionResult(t, applyDone, "full-set HookBus replacement")

	if _, ok := bus.RuntimeSnapshot(foreign.ID); ok {
		t.Fatal("delayed direct registration overwrote full-set plugin map")
	}
	active, err := manager.ActiveRuntimeInstance(desired.ID)
	if err != nil {
		t.Fatal(err)
	}
	hook, ok := bus.RuntimeSnapshot(desired.ID)
	if !ok || hook.InstanceID != active.Identity.InstanceID {
		t.Fatalf("HookBus did not converge to desired runtime: %#v", hook)
	}
}

func TestPluginRuntimeFullSetGenerationBarrierBlocksDirectFamilyReaders(t *testing.T) {
	bus := NewHookBus(HookBusConfig{})
	bus.mu.Lock()
	done := make(chan string, 4)
	go func() {
		bus.VersionedRegistry().Snapshot()
		done <- "hooks"
	}()
	go func() {
		bus.ProviderSlots().Snapshot()
		done <- "providers"
	}()
	go func() {
		bus.Commands().Snapshot()
		done <- "commands"
	}()
	go func() {
		bus.AdminSurfaces().Snapshot("")
		done <- "admin"
	}()

	select {
	case family := <-done:
		bus.mu.Unlock()
		t.Fatalf("%s reader crossed an in-progress aggregate generation", family)
	case <-time.After(20 * time.Millisecond):
	}
	bus.mu.Unlock()

	seen := make(map[string]bool, 4)
	for range 4 {
		select {
		case family := <-done:
			seen[family] = true
		case <-time.After(time.Second):
			t.Fatalf("generation reader remained blocked after commit: %#v", seen)
		}
	}
	for _, family := range []string{"hooks", "providers", "commands", "admin"} {
		if !seen[family] {
			t.Fatalf("generation reader did not complete: %s", family)
		}
	}
}

func TestPluginRuntimeFullSetDrainTimeoutReopensOldGate(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	old := inventory.seed(t, "drain-timeout.plugin", 1, "1.0.0", "drain-timeout-v1")
	inventory.seed(t, old.ID, 2, "2.0.0", "drain-timeout-v2")
	if err := manager.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, _ := manager.ActiveRuntimeInstance(old.ID)
	_, blockingLease, err := manager.AcquireActiveRuntimeCall(context.Background(), old.ID, RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer blockingLease.Release()

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applier.drainTimeout = 30 * time.Millisecond
	_, err = applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationUpgrade,
		inventory.member(old.ID, 2, "2.0.0", "drain-timeout-v2"),
	))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain timeout = %v", err)
	}

	// Resume permits existing ordinary calls to finish and immediately admits
	// new traffic; rollback must not wait for the original lease to release.
	snapshot, newLease, err := manager.AcquireActiveRuntimeCall(context.Background(), old.ID, RuntimeCallRoute)
	if err != nil {
		t.Fatalf("old gate remained closed: %v", err)
	}
	newLease.Release()
	if snapshot.Identity != before.Identity || snapshot.Admission.Draining {
		t.Fatalf("old exact runtime not restored: %#v", snapshot)
	}
}

func TestPluginRuntimeFullSetPublishesOneObservableRegistryGeneration(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	a1 := inventory.seed(t, "generation.a", 1, "1.0.0", "generation-a1")
	b1 := inventory.seed(t, "generation.b", 2, "1.0.0", "generation-b1")
	inventory.seed(t, a1.ID, 3, "2.0.0", "generation-a2")
	inventory.seed(t, b1.ID, 4, "2.0.0", "generation-b2")
	if err := manager.Start(context.Background(), a1); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), b1); err != nil {
		t.Fatal(err)
	}
	before := manager.HookBus().RuntimeRegistryGeneration()
	if !runtimeRegistryGenerationCoherent(before) || before.PublicationRevision != 0 {
		t.Fatalf("initial cross-family generation = %#v", before)
	}

	starter.inner.publishStarted = make(chan struct{}, 2)
	starter.inner.publishContinue = make(chan struct{})
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	done := make(chan error, 1)
	go func() {
		_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
			extensions.PluginRuntimePublicationUpgrade,
			inventory.member(a1.ID, 3, "2.0.0", "generation-a2"),
			inventory.member(b1.ID, 4, "2.0.0", "generation-b2"),
		))
		done <- err
	}()
	<-starter.inner.publishStarted

	var mixed atomic.Bool
	stop := make(chan struct{})
	var observers sync.WaitGroup
	observers.Add(1)
	go func() {
		defer observers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			snapshot := manager.HookBus().RuntimeRegistryGeneration()
			if !runtimeRegistryGenerationCoherent(snapshot) ||
				(snapshot.Generation != before.Generation && snapshot.Generation != before.Generation+1) {
				mixed.Store(true)
				return
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	close(starter.inner.publishContinue)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(stop)
	observers.Wait()
	if mixed.Load() {
		t.Fatal("observer saw a partial Hook/Provider/Command/Admin generation")
	}
	after := manager.HookBus().RuntimeRegistryGeneration()
	if !runtimeRegistryGenerationCoherent(after) || after.Generation != before.Generation+1 ||
		after.PublicationRevision != 1 || reflect.DeepEqual(after.RuntimeMembers, before.RuntimeMembers) {
		t.Fatalf("committed cross-family generation = %#v before=%#v", after, before)
	}
}

func TestPluginRuntimeFullSetFailedAdmissionRollbackKeepsWholeSetClosed(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	a1 := inventory.seed(t, "failclosed.a", 1, "1.0.0", "failclosed-a1")
	b1 := inventory.seed(t, "failclosed.b", 2, "1.0.0", "failclosed-b1")
	inventory.seed(t, a1.ID, 3, "2.0.0", "failclosed-a2")
	inventory.seed(t, b1.ID, 4, "2.0.0", "failclosed-b2")
	if err := manager.Start(context.Background(), a1); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), b1); err != nil {
		t.Fatal(err)
	}
	oldA, _ := manager.ActiveRuntimeInstance(a1.ID)
	oldB, _ := manager.ActiveRuntimeInstance(b1.ID)
	starter.mu.Lock()
	starter.acquireSetError = errors.New("candidate exited before acknowledgement")
	starter.revalidateSetHook = func() {
		manager.mu.RLock()
		instance, err := manager.runtimeInstanceLocked(oldB.Identity)
		manager.mu.RUnlock()
		if err == nil {
			instance.gate.ForceCancel(errors.New("forced rollback admission failure"))
		}
	}
	starter.mu.Unlock()

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationUpgrade,
		inventory.member(a1.ID, 3, "2.0.0", "failclosed-a2"),
		inventory.member(b1.ID, 4, "2.0.0", "failclosed-b2"),
	))
	if err == nil {
		t.Fatal("expected candidate revalidation and admission rollback failure")
	}
	for _, old := range []RuntimeInstanceSnapshot{oldA, oldB} {
		active, activeErr := manager.ActiveRuntimeInstance(old.Identity.ExtensionID)
		if activeErr != nil || active.Identity != old.Identity || !active.Admission.Draining {
			t.Fatalf("failed rollback exposed partial old set: %#v, %v", active, activeErr)
		}
		if _, lease, callErr := manager.AcquireActiveRuntimeCall(context.Background(), old.Identity.ExtensionID, RuntimeCallRoute); callErr == nil {
			lease.Release()
			t.Fatalf("failed rollback reopened %s", old.Identity.ExtensionID)
		}
	}
}

func runtimeRegistryGenerationCoherent(snapshot RuntimeRegistryGenerationSnapshot) bool {
	return reflect.DeepEqual(snapshot.RuntimeMembers, snapshot.HookMembers) &&
		reflect.DeepEqual(snapshot.RuntimeMembers, snapshot.ProviderMembers) &&
		reflect.DeepEqual(snapshot.RuntimeMembers, snapshot.CommandMembers) &&
		reflect.DeepEqual(snapshot.RuntimeMembers, snapshot.AdminSurfaceMembers)
}

func assertTransitionBlocked(t *testing.T, done <-chan error, name string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s bypassed runtime-set barrier: %v", name, err)
	case <-time.After(30 * time.Millisecond):
	}
}

func assertTransitionResult(t *testing.T, done <-chan error, name string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s deadlocked", name)
	}
}

func assertTransitionClosed(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s deadlocked", name)
	}
}
