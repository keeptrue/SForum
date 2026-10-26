package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
)

func TestPluginRuntimeFullSetStageHealthFailurePreservesOld(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	old := inventory.seed(t, "preserve.plugin", 11, "1.0.0", "preserve-v1")
	if err := manager.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, err := manager.ActiveRuntimeInstance(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	inventory.seed(t, old.ID, 12, "2.0.0", "preserve-v2")
	starter.failNextHealth = errors.New("health boom")

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	_, err = applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(old.ID, 12, "2.0.0", "preserve-v2"),
	))
	if err == nil {
		t.Fatal("expected health failure")
	}
	after, err := manager.ActiveRuntimeInstance(old.ID)
	if err != nil || after.Identity != before.Identity || after.Admission.Draining {
		t.Fatalf("old set changed after health failure: %#v, %v", after, err)
	}
	assertActiveCallable(t, manager, old.ID, before.Identity)
}

func TestPluginRuntimeFullSetTwoPluginReplaceAddRemove(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	a1 := inventory.seed(t, "alpha.plugin", 101, "1.0.0", "alpha-v1")
	b1 := inventory.seed(t, "beta.plugin", 201, "1.0.0", "beta-v1")
	inventory.seed(t, "alpha.plugin", 102, "2.0.0", "alpha-v2")
	inventory.seed(t, "gamma.plugin", 301, "1.0.0", "gamma-v1")
	if err := manager.Start(context.Background(), a1); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), b1); err != nil {
		t.Fatal(err)
	}
	oldA, _ := manager.ActiveRuntimeInstance(a1.ID)
	oldB, _ := manager.ActiveRuntimeInstance(b1.ID)

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applied, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationUpgrade,
		inventory.member("alpha.plugin", 102, "2.0.0", "alpha-v2"),
		inventory.member("gamma.plugin", 301, "1.0.0", "gamma-v1"),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 {
		t.Fatalf("applied = %#v", applied)
	}
	if applied[0].ExtensionID != "alpha.plugin" || applied[1].ExtensionID != "gamma.plugin" {
		t.Fatalf("applied order = %#v", applied)
	}

	alpha, err := manager.ActiveRuntimeInstance("alpha.plugin")
	if err != nil || alpha.Identity == oldA.Identity || alpha.ExtensionVersion != "2.0.0" || alpha.Admission.Draining {
		t.Fatalf("alpha active = %#v, %v", alpha, err)
	}
	gamma, err := manager.ActiveRuntimeInstance("gamma.plugin")
	if err != nil || gamma.ExtensionVersion != "1.0.0" || gamma.Admission.Draining {
		t.Fatalf("gamma active = %#v, %v", gamma, err)
	}
	if _, err := manager.ActiveRuntimeInstance("beta.plugin"); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("beta still active: %v", err)
	}
	if snap, snapErr := manager.InspectRuntimeInstance(oldB.Identity); snapErr == nil && snap.Active {
		t.Fatalf("beta retained still active: %#v", snap)
	}
	if snap, snapErr := manager.InspectRuntimeInstance(oldA.Identity); snapErr == nil && snap.Active {
		t.Fatalf("old alpha still active: %#v", snap)
	}
	assertActiveCallable(t, manager, "alpha.plugin", alpha.Identity)
	assertActiveCallable(t, manager, "gamma.plugin", gamma.Identity)
}

func TestPluginRuntimeFullSetEmptyDisablesAll(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	a := inventory.seed(t, "empty.a", 1, "1.0.0", "empty-a")
	b := inventory.seed(t, "empty.b", 2, "1.0.0", "empty-b")
	if err := manager.Start(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applied, err := applier.ApplyPluginRuntimeFullSet(
		context.Background(),
		inventory.publication(extensions.PluginRuntimePublicationDisable),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied = %#v", applied)
	}
	if _, err := manager.ActiveRuntimeInstance(a.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("a still active: %v", err)
	}
	if _, err := manager.ActiveRuntimeInstance(b.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("b still active: %v", err)
	}
}

func TestPluginRuntimeFullSetJointDependencyRemovalClearsHooksBeforeCleanup(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	provider := inventory.seed(t, "hooks.provider", 31, "1.0.0", "joint-provider")
	consumer := inventory.seed(t, "hooks.consumer", 32, "1.0.0", "joint-consumer")
	providerHook := versionedHookDefinition()
	consumerHook := versionedHookConsumer(50)
	dependencies := []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	provider.Manifest.Hooks = []extensions.ManifestHook{providerHook}
	consumer.Manifest.Hooks = []extensions.ManifestHook{consumerHook}
	consumer.Manifest.Dependencies = dependencies
	inventory.setHooks(provider.ID, provider.Version, "joint-provider", provider.Manifest.Hooks, nil)
	inventory.setHooks(consumer.ID, consumer.Version, "joint-consumer", consumer.Manifest.Hooks, dependencies)
	if err := manager.Start(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
	providerRuntime, _ := manager.ActiveRuntimeInstance(provider.ID)
	consumerRuntime, _ := manager.ActiveRuntimeInstance(consumer.ID)
	// Process cleanup may fail, but stale fail-closed dependency hooks must not
	// survive after the desired empty set is acknowledged.
	starter.failStop(providerRuntime.Identity, errors.New("retain provider artifact"))
	starter.failStop(consumerRuntime.Identity, errors.New("retain consumer artifact"))

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	if _, err := applier.ApplyPluginRuntimeFullSet(
		context.Background(), inventory.publication(extensions.PluginRuntimePublicationDisable),
	); err != nil {
		t.Fatal(err)
	}
	if snapshot := manager.HookBus().VersionedRegistry().Snapshot(); len(snapshot.Contracts) != 0 || len(snapshot.Listeners) != 0 {
		t.Fatalf("stale dependency hooks survived desired-set commit: %#v", snapshot)
	}
	for _, identity := range []RuntimeInstanceIdentity{providerRuntime.Identity, consumerRuntime.Identity} {
		if _, ok := manager.HookBus().RuntimeSnapshot(identity.ExtensionID); ok {
			t.Fatalf("stale HookBus registration survived for %s", identity.ExtensionID)
		}
		retained, err := manager.InspectRuntimeInstance(identity)
		if err != nil || retained.Active {
			t.Fatalf("cleanup failure was not retained safely: %#v, %v", retained, err)
		}
	}
}

func TestPluginRuntimeFullSetIdempotentReplaySameInstanceIDs(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	ext := inventory.seed(t, "idempotent.plugin", 77, "1.0.0", "idempotent-v1")
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	pub := inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(ext.ID, 77, "1.0.0", "idempotent-v1"),
	)
	first, err := applier.ApplyPluginRuntimeFullSet(context.Background(), pub)
	if err != nil {
		t.Fatal(err)
	}
	startsAfterFirst := starter.startCount()
	second, err := applier.ApplyPluginRuntimeFullSet(context.Background(), pub)
	if err != nil {
		t.Fatal(err)
	}
	if starter.startCount() != startsAfterFirst {
		t.Fatalf("idempotent replay started new instances: before=%d after=%d", startsAfterFirst, starter.startCount())
	}
	if len(first) != 1 || len(second) != 1 || first[0].RuntimeInstanceID != second[0].RuntimeInstanceID {
		t.Fatalf("instance ids changed: first=%#v second=%#v", first, second)
	}
	assertActiveCallable(t, manager, ext.ID, RuntimeInstanceIdentity{
		ExtensionID: ext.ID, InstanceID: first[0].RuntimeInstanceID,
	})
}

func TestPluginRuntimeFullSetIdempotentReplayAllowsActiveTraffic(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	ext := inventory.seed(t, "traffic.plugin", 88, "1.0.0", "traffic-v1")
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	publication := inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(ext.ID, 88, "1.0.0", "traffic-v1"),
	)
	first, err := applier.ApplyPluginRuntimeFullSet(context.Background(), publication)
	if err != nil {
		t.Fatal(err)
	}
	leaseSnapshot, lease, err := manager.AcquireActiveRuntimeCall(context.Background(), ext.ID, RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	done := make(chan error, 1)
	go func() {
		_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), publication)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("idempotent replay waited for active traffic")
	}
	if len(first) != 1 || leaseSnapshot.Identity.InstanceID != first[0].RuntimeInstanceID {
		t.Fatalf("replay changed exact instance: first=%#v lease=%#v", first, leaseSnapshot)
	}
	if err := lease.Context.Err(); err != nil {
		t.Fatalf("replay cancelled reused call: %v", err)
	}
}

func TestPluginRuntimeFullSetUnhealthyReuseRestagesExactArtifact(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	ext := inventory.seed(t, "unhealthy.plugin", 89, "1.0.0", "unhealthy-v1")
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	publication := inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(ext.ID, 89, "1.0.0", "unhealthy-v1"),
	)
	first, err := applier.ApplyPluginRuntimeFullSet(context.Background(), publication)
	if err != nil {
		t.Fatal(err)
	}
	starter.failNextHealth = errors.New("runtime is no longer ready")
	second, err := applier.ApplyPluginRuntimeFullSet(context.Background(), publication)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].RuntimeInstanceID == second[0].RuntimeInstanceID {
		t.Fatalf("unhealthy exact runtime was reused: first=%#v second=%#v", first, second)
	}
	assertActiveCallable(t, manager, ext.ID, RuntimeInstanceIdentity{
		ExtensionID: ext.ID, InstanceID: second[0].RuntimeInstanceID,
	})
}

func TestPluginRuntimeFullSetCancellationRollsBack(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	old := inventory.seed(t, "cancel.plugin", 1, "1.0.0", "cancel-v1")
	inventory.seed(t, old.ID, 2, "2.0.0", "cancel-v2")
	if err := manager.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, _ := manager.ActiveRuntimeInstance(old.ID)

	ctx, cancel := context.WithCancel(context.Background())
	starter.inner.publishStarted = make(chan struct{}, 1)
	starter.inner.publishContinue = make(chan struct{})
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)

	result := make(chan error, 1)
	go func() {
		_, err := applier.ApplyPluginRuntimeFullSet(ctx, inventory.publication(
			extensions.PluginRuntimePublicationUpgrade,
			inventory.member(old.ID, 2, "2.0.0", "cancel-v2"),
		))
		result <- err
	}()
	<-starter.inner.publishStarted
	cancel()
	close(starter.inner.publishContinue)
	err := <-result
	if err == nil {
		t.Fatal("expected cancellation or rollback error")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		after, activeErr := manager.ActiveRuntimeInstance(old.ID)
		if activeErr == nil && after.Identity == before.Identity && !after.Admission.Draining {
			assertActiveCallable(t, manager, old.ID, before.Identity)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("old set not restored after cancel: %#v, %v (apply=%v)", after, activeErr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPluginRuntimeFullSetCandidateLossBeforeCommitRollsBack(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	old := inventory.seed(t, "candidate-loss.plugin", 1, "1.0.0", "candidate-loss-v1")
	inventory.seed(t, old.ID, 2, "2.0.0", "candidate-loss-v2")
	if err := manager.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, _ := manager.ActiveRuntimeInstance(old.ID)
	starter.mu.Lock()
	starter.finalFenceError = errors.New("candidate exited at final aggregate fence")
	starter.mu.Unlock()

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationUpgrade,
		inventory.member(old.ID, 2, "2.0.0", "candidate-loss-v2"),
	))
	if err == nil || !strings.Contains(err.Error(), "candidate exited at final aggregate fence") {
		t.Fatalf("candidate loss error = %v", err)
	}
	after, activeErr := manager.ActiveRuntimeInstance(old.ID)
	if activeErr != nil || after.Identity != before.Identity || after.Admission.Draining {
		t.Fatalf("old runtime not restored after candidate loss: %#v, %v", after, activeErr)
	}
	assertActiveCallable(t, manager, old.ID, before.Identity)
}

func TestPluginRuntimeFullSetPublicationOrderDeterministic(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	z := inventory.seed(t, "z.plugin", 3, "1.0.0", "z-v1")
	m := inventory.seed(t, "m.plugin", 2, "1.0.0", "m-v1")
	a := inventory.seed(t, "a.plugin", 1, "1.0.0", "a-v1")
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applied, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(z.ID, 3, "1.0.0", "z-v1"),
		inventory.member(a.ID, 1, "1.0.0", "a-v1"),
		inventory.member(m.ID, 2, "1.0.0", "m-v1"),
	))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(applied))
	for i, member := range applied {
		got[i] = member.ExtensionID
	}
	want := []string{"a.plugin", "m.plugin", "z.plugin"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v want %v", got, want)
	}
	if applied[0].RuntimeInstanceID != "instance-1" ||
		applied[1].RuntimeInstanceID != "instance-2" ||
		applied[2].RuntimeInstanceID != "instance-3" {
		t.Fatalf("stage order instance ids = %#v", applied)
	}
}

func TestPluginRuntimeFullSetUsesSystemTierOrderBeforeIDFallback(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	z := inventory.seed(t, "z.system", 3, "1.0.0", "z-v1")
	m := inventory.seed(t, "m.plugin", 2, "1.0.0", "m-v1")
	a := inventory.seed(t, "a.system", 1, "1.0.0", "a-v1")
	applier, err := NewManagerPluginRuntimeFullSetApplier(manager, inventory, []string{"z.system", "a.system"})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(a.ID, 1, "1.0.0", "a-v1"),
		inventory.member(m.ID, 2, "1.0.0", "m-v1"),
		inventory.member(z.ID, 3, "1.0.0", "z-v1"),
	))
	if err != nil {
		t.Fatal(err)
	}
	instances := map[string]string{}
	for _, member := range applied {
		instances[member.ExtensionID] = member.RuntimeInstanceID
	}
	if instances["z.system"] != "instance-1" || instances["a.system"] != "instance-2" || instances["m.plugin"] != "instance-3" {
		t.Fatalf("tier order was not applied before id fallback: %#v", instances)
	}
}

func TestPluginRuntimeFullSetCandidatePublishFailureRollsBack(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	a1 := inventory.seed(t, "pubfail.a", 1, "1.0.0", "pubfail-a1")
	b1 := inventory.seed(t, "pubfail.b", 2, "1.0.0", "pubfail-b1")
	inventory.seed(t, "pubfail.a", 3, "2.0.0", "pubfail-a2")
	inventory.seed(t, "pubfail.b", 4, "2.0.0", "pubfail-b2")
	if err := manager.Start(context.Background(), a1); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), b1); err != nil {
		t.Fatal(err)
	}
	beforeA, _ := manager.ActiveRuntimeInstance(a1.ID)
	beforeB, _ := manager.ActiveRuntimeInstance(b1.ID)
	// 仅统计 full-set 路径上的 PublishInstance：允许第 1 个候选成功，第 2 个失败。
	starter.resetPublishFault(1)

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationUpgrade,
		inventory.member("pubfail.a", 3, "2.0.0", "pubfail-a2"),
		inventory.member("pubfail.b", 4, "2.0.0", "pubfail-b2"),
	))
	if err == nil {
		t.Fatal("expected publish failure")
	}
	afterA, err := manager.ActiveRuntimeInstance(a1.ID)
	if err != nil || afterA.Identity != beforeA.Identity || afterA.Admission.Draining {
		t.Fatalf("a not restored: %#v, %v", afterA, err)
	}
	afterB, err := manager.ActiveRuntimeInstance(b1.ID)
	if err != nil || afterB.Identity != beforeB.Identity || afterB.Admission.Draining {
		t.Fatalf("b not restored: %#v, %v", afterB, err)
	}
	assertActiveCallable(t, manager, a1.ID, beforeA.Identity)
	assertActiveCallable(t, manager, b1.ID, beforeB.Identity)
}

func TestPluginRuntimeFullSetRollbackKeepsCandidateWhenOldRepublishFails(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	old := inventory.seed(t, "rollback.plugin", 1, "1.0.0", "rollback-v1")
	inventory.seed(t, old.ID, 2, "2.0.0", "rollback-v2")
	inventory.setHooks(
		old.ID, "2.0.0", "rollback-v2",
		[]extensions.ManifestHook{{
			ID: old.ID + ".consumer", TargetID: "missing.provider", Name: old.ID + ".event",
			ContractVersion: old.ID + ".consumer@1", Kind: "filter", Handler: "hook.consume",
			InputSchema: "demo.input@1", ResultSchema: "demo.output@1", Execution: "sync",
			FailurePolicy: "fail_closed", TimeoutMS: 1000,
		}},
		nil,
	)
	if err := manager.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, _ := manager.ActiveRuntimeInstance(old.ID)
	starter.inner.failPublish(before.Identity, errors.New("old registry cannot republish"))

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationUpgrade,
		inventory.member(old.ID, 2, "2.0.0", "rollback-v2"),
	))
	if err == nil || !strings.Contains(err.Error(), "old registry cannot republish") {
		t.Fatalf("rollback error = %v", err)
	}
	// The old Manager pointer remains inspectable, but admission stays closed
	// until the complete old protocol/service graph can be restored.
	oldSnapshot, activeErr := manager.ActiveRuntimeInstance(old.ID)
	if activeErr != nil || oldSnapshot.Identity != before.Identity || !oldSnapshot.Admission.Draining {
		t.Fatalf("failed rollback did not remain fail-closed: %#v, %v", oldSnapshot, activeErr)
	}
	if _, lease, err := manager.AcquireActiveRuntimeCall(context.Background(), old.ID, RuntimeCallRoute); err == nil {
		lease.Release()
		t.Fatal("failed protocol rollback reopened old Manager admission")
	}
	protocolActive := starter.inner.activeIdentity(old.ID)
	if protocolActive == before.Identity || protocolActive.InstanceID == "" {
		t.Fatalf("test did not retain the forward-recovery candidate: %#v", protocolActive)
	}
	retained, inspectErr := manager.InspectRuntimeInstance(protocolActive)
	if inspectErr != nil || retained.Active || !retained.Admission.Draining {
		t.Fatalf("candidate was stopped after failed old republish: %#v, %v", retained, inspectErr)
	}
}

func TestPluginRuntimeFullSetConcurrentRevisionsSerialized(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	// StartInstance 注入短延迟；若 full-set 未串行，并发 stage 会抬高 maxInFlightStarts。
	starter.startDelay = 15 * time.Millisecond
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	ext := inventory.seed(t, "serial.plugin", 1, "1.0.0", "serial-v1")
	inventory.seed(t, ext.ID, 2, "2.0.0", "serial-v2")
	inventory.seed(t, ext.ID, 3, "3.0.0", "serial-v3")
	pubs := []extensions.PluginRuntimePublication{
		inventory.publication(extensions.PluginRuntimePublicationEnable, inventory.member(ext.ID, 1, "1.0.0", "serial-v1")),
		inventory.publication(extensions.PluginRuntimePublicationUpgrade, inventory.member(ext.ID, 2, "2.0.0", "serial-v2")),
		inventory.publication(extensions.PluginRuntimePublicationUpgrade, inventory.member(ext.ID, 3, "3.0.0", "serial-v3")),
	}
	for index := range pubs {
		pubs[index].Revision = int64(index + 1)
	}
	inventory.setLatest(pubs[len(pubs)-1])
	var wg sync.WaitGroup
	errs := make(chan error, len(pubs))
	for _, pub := range pubs {
		applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
		wg.Add(1)
		go func(applier *ManagerPluginRuntimeFullSetApplier, publication extensions.PluginRuntimePublication) {
			defer wg.Done()
			_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), publication)
			errs <- err
		}(applier, pub)
	}
	wg.Wait()
	close(errs)
	superseded := 0
	for err := range errs {
		if errors.Is(err, extensions.ErrPluginRuntimePublicationSuperseded) {
			superseded++
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if superseded != 2 {
		t.Fatalf("superseded applies=%d want=2", superseded)
	}
	if got := starter.maxInFlightStarts.Load(); got != 1 {
		t.Fatalf("overlapping staged starts under full-set lock: max=%d", got)
	}
	activeSnap, err := manager.ActiveRuntimeInstance(ext.ID)
	if err != nil {
		t.Fatal(err)
	}
	if activeSnap.ExtensionVersion != "3.0.0" {
		t.Fatalf("latest publication did not win: %#v", activeSnap)
	}
	assertActiveCallable(t, manager, ext.ID, activeSnap.Identity)
}

func TestPluginRuntimeFullSetRejectsDurableRevisionOlderThanProcess(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	ext := inventory.seed(t, "nonregressive.plugin", 1, "1.0.0", "nonregressive-v1")
	inventory.seed(t, ext.ID, 2, "2.0.0", "nonregressive-v2")
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)

	first := inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(ext.ID, 1, "1.0.0", "nonregressive-v1"),
	)
	first.Revision = 1
	inventory.setLatest(first)
	if _, err := applier.ApplyPluginRuntimeFullSet(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := inventory.publication(
		extensions.PluginRuntimePublicationUpgrade,
		inventory.member(ext.ID, 2, "2.0.0", "nonregressive-v2"),
	)
	second.Revision = 2
	inventory.setLatest(second)
	if _, err := applier.ApplyPluginRuntimeFullSet(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	before, err := manager.ActiveRuntimeInstance(ext.ID)
	if err != nil {
		t.Fatal(err)
	}
	startsBefore := starter.startCount()

	// Model a stale/restored durable source: Latest agrees with the requested
	// R1, while this process has already atomically published R2.
	inventory.setLatest(first)
	if _, err := applier.ApplyPluginRuntimeFullSet(t.Context(), first); !errors.Is(err, extensions.ErrPluginRuntimePublicationSuperseded) {
		t.Fatalf("older durable revision error=%v", err)
	}
	after, err := manager.ActiveRuntimeInstance(ext.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Identity != before.Identity || after.ExtensionVersion != "2.0.0" ||
		after.ArtifactDigest != inventory.member(ext.ID, 2, "2.0.0", "nonregressive-v2").PackageDigest {
		t.Fatalf("older durable revision changed process state: before=%#v after=%#v", before, after)
	}
	if starter.startCount() != startsBefore {
		t.Fatalf("older durable revision started code: before=%d after=%d", startsBefore, starter.startCount())
	}
	if generation := manager.HookBus().RuntimeRegistryGeneration(); generation.PublicationRevision != second.Revision {
		t.Fatalf("process publication revision regressed: %#v", generation)
	}
	assertActiveCallable(t, manager, ext.ID, after.Identity)
}

func TestPluginRuntimeFullSetNoMixedCallableObservation(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	old := inventory.seed(t, "mix.plugin", 1, "1.0.0", "mix-v1")
	inventory.seed(t, old.ID, 2, "2.0.0", "mix-v2")
	if err := manager.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, _ := manager.ActiveRuntimeInstance(old.ID)

	starter.inner.publishStarted = make(chan struct{}, 1)
	starter.inner.publishContinue = make(chan struct{})
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)

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
			snap, lease, err := manager.AcquireActiveRuntimeCall(context.Background(), old.ID, RuntimeCallRoute)
			if err != nil {
				continue
			}
			// 可调用快照必须自洽：instance 与 version 同时属于旧或同时属于新，且不得 draining。
			if snap.Admission.Draining {
				mixed.Store(true)
			}
			if snap.Identity == before.Identity && snap.ExtensionVersion != "1.0.0" {
				mixed.Store(true)
			}
			if snap.Identity != before.Identity && snap.ExtensionVersion == "1.0.0" {
				mixed.Store(true)
			}
			lease.Release()
		}
	}()

	result := make(chan error, 1)
	go func() {
		_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
			extensions.PluginRuntimePublicationUpgrade,
			inventory.member(old.ID, 2, "2.0.0", "mix-v2"),
		))
		result <- err
	}()
	<-starter.inner.publishStarted
	time.Sleep(20 * time.Millisecond)
	close(starter.inner.publishContinue)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	close(stop)
	observers.Wait()
	if mixed.Load() {
		t.Fatal("observer saw mixed old/new callable state")
	}
	after, err := manager.ActiveRuntimeInstance(old.ID)
	if err != nil || after.ExtensionVersion != "2.0.0" || after.Admission.Draining {
		t.Fatalf("final active = %#v, %v", after, err)
	}
	assertActiveCallable(t, manager, old.ID, after.Identity)
}

func TestPluginRuntimeFullSetMultiPluginPointersSwapTogether(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	a1 := inventory.seed(t, "atomic.a", 1, "1.0.0", "atomic-a1")
	b1 := inventory.seed(t, "atomic.b", 2, "1.0.0", "atomic-b1")
	inventory.seed(t, a1.ID, 3, "2.0.0", "atomic-a2")
	inventory.seed(t, b1.ID, 4, "2.0.0", "atomic-b2")
	if err := manager.Start(context.Background(), a1); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), b1); err != nil {
		t.Fatal(err)
	}
	oldA, _ := manager.ActiveRuntimeInstance(a1.ID)
	oldB, _ := manager.ActiveRuntimeInstance(b1.ID)

	starter.inner.publishStarted = make(chan struct{}, 2)
	starter.inner.publishContinue = make(chan struct{})
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	result := make(chan error, 1)
	go func() {
		_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
			extensions.PluginRuntimePublicationUpgrade,
			inventory.member(a1.ID, 3, "2.0.0", "atomic-a2"),
			inventory.member(b1.ID, 4, "2.0.0", "atomic-b2"),
		))
		result <- err
	}()
	<-starter.inner.publishStarted

	var managerMixed atomic.Bool
	var protocolMixed atomic.Bool
	stop := make(chan struct{})
	var observer sync.WaitGroup
	observer.Add(1)
	go func() {
		defer observer.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			manager.mu.RLock()
			aID := manager.activeInstances[a1.ID]
			bID := manager.activeInstances[b1.ID]
			manager.mu.RUnlock()
			aOld := aID == oldA.Identity.InstanceID
			bOld := bID == oldB.Identity.InstanceID
			if aOld != bOld {
				managerMixed.Store(true)
			}
			starter.inner.mu.Lock()
			protocolA := starter.inner.active[a1.ID]
			protocolB := starter.inner.active[b1.ID]
			starter.inner.mu.Unlock()
			if (protocolA == oldA.Identity) != (protocolB == oldB.Identity) {
				protocolMixed.Store(true)
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	close(starter.inner.publishContinue)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	close(stop)
	observer.Wait()
	if managerMixed.Load() {
		t.Fatal("observer saw a mixed multi-plugin Manager pointer set")
	}
	if protocolMixed.Load() {
		t.Fatal("observer saw a mixed multi-plugin ProtocolStarter pointer set")
	}
	newA, err := manager.ActiveRuntimeInstance(a1.ID)
	if err != nil || newA.ExtensionVersion != "2.0.0" {
		t.Fatalf("new a = %#v, %v", newA, err)
	}
	newB, err := manager.ActiveRuntimeInstance(b1.ID)
	if err != nil || newB.ExtensionVersion != "2.0.0" {
		t.Fatalf("new b = %#v, %v", newB, err)
	}
	if starter.inner.activeIdentity(a1.ID) != newA.Identity || starter.inner.activeIdentity(b1.ID) != newB.Identity {
		t.Fatal("ProtocolStarter complete set did not converge with Manager")
	}
	for _, want := range []RuntimeInstanceIdentity{newA.Identity, newB.Identity} {
		hook, ok := manager.HookBus().RuntimeSnapshot(want.ExtensionID)
		if !ok || hook.InstanceID != want.InstanceID {
			t.Fatalf("hook graph did not swap with Manager: want=%#v hook=%#v", want, hook)
		}
	}
}

func TestPluginRuntimeFullSetExactVersionMismatch(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	ext := inventory.seed(t, "mismatch.plugin", 10, "1.0.0", "mismatch-v1")
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)

	_, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		extensions.PluginRuntimeMember{
			ExtensionID: ext.ID, ExtensionVersionID: 999,
			ExtensionVersion: "1.0.0", PackageDigest: pluginRuntimeFullSetDigest("mismatch-v1"),
		},
	))
	if !errors.Is(err, ErrPluginRuntimeFullSetConflict) {
		t.Fatalf("version id mismatch = %v", err)
	}

	_, err = applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		extensions.PluginRuntimeMember{
			ExtensionID: ext.ID, ExtensionVersionID: 10,
			ExtensionVersion: "1.0.0", PackageDigest: pluginRuntimeFullSetDigest("other-digest"),
		},
	))
	if err == nil {
		t.Fatal("expected digest mismatch error")
	}

	drifted := inventory.seed(t, "manifest.plugin", 20, "1.0.0", "manifest-v1")
	if _, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(drifted.ID, 20, "1.0.0", "manifest-v1"),
	)); err != nil {
		t.Fatal(err)
	}
	active, _ := manager.ActiveRuntimeInstance(drifted.ID)
	manager.mu.Lock()
	inst := manager.runtimeInstances[active.Identity.ExtensionID][active.Identity.InstanceID]
	inst.extension.Manifest.Name = "tampered"
	manager.mu.Unlock()
	starts := starter.startCount()
	if _, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(drifted.ID, 20, "1.0.0", "manifest-v1"),
	)); err != nil {
		t.Fatal(err)
	}
	if starter.startCount() <= starts {
		t.Fatal("expected restage after manifest mismatch")
	}
}

func TestPluginRuntimeFullSetDerivesCapabilitiesFromExactVersionManifest(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	ext := inventory.seed(t, "grants.plugin", 90, "1.0.0", "grants-v1")
	inventory.setCapabilities(
		ext.ID, ext.Version, "grants-v1",
		[]string{capabilities.AuditAppend},
		[]string{capabilities.SettingsOwn},
	)
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applied, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(ext.ID, 90, ext.Version, "grants-v1"),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("applied = %#v", applied)
	}
	started, ok := starter.startedExtension(RuntimeInstanceIdentity{
		ExtensionID: ext.ID, InstanceID: applied[0].RuntimeInstanceID,
	})
	if !ok {
		t.Fatal("starter did not capture exact extension")
	}
	grants := make(map[string]bool, len(started.CapabilityGrants))
	for _, grant := range started.CapabilityGrants {
		grants[grant.Key] = true
	}
	if !grants[capabilities.AuditAppend] || grants[capabilities.SettingsOwn] {
		t.Fatalf("starter received mutable metadata grants instead of exact Manifest grants: %#v", started.CapabilityGrants)
	}
}

func TestPluginRuntimeFullSetCleanupFailureRetainsSafely(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	old := inventory.seed(t, "cleanup.plugin", 1, "1.0.0", "cleanup-v1")
	inventory.seed(t, old.ID, 2, "2.0.0", "cleanup-v2")
	if err := manager.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	before, _ := manager.ActiveRuntimeInstance(old.ID)
	starter.failStop(before.Identity, errors.New("stop boom"))

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applied, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationUpgrade,
		inventory.member(old.ID, 2, "2.0.0", "cleanup-v2"),
	))
	if err != nil {
		t.Fatalf("cleanup failure must not fail apply: %v", err)
	}
	if len(applied) != 1 || applied[0].ExtensionVersion != "2.0.0" {
		t.Fatalf("applied = %#v", applied)
	}
	active, err := manager.ActiveRuntimeInstance(old.ID)
	if err != nil || active.ExtensionVersion != "2.0.0" || active.Admission.Draining {
		t.Fatalf("new set not live: %#v, %v", active, err)
	}
	retained, err := manager.InspectRuntimeInstance(before.Identity)
	if err != nil || retained.Active {
		t.Fatalf("expected retained old instance: %#v, %v", retained, err)
	}
	assertActiveCallable(t, manager, old.ID, active.Identity)
}

func TestPluginRuntimeFullSetRejectsNilDependencies(t *testing.T) {
	if _, err := NewManagerPluginRuntimeFullSetApplier(nil, newPluginRuntimeFullSetTestInventory()); !errors.Is(err, ErrPluginRuntimeFullSetInvalid) {
		t.Fatalf("nil manager = %v", err)
	}
	if _, err := NewManagerPluginRuntimeFullSetApplier(NewManager(ManagerConfig{}), nil); !errors.Is(err, ErrPluginRuntimeFullSetInvalid) {
		t.Fatalf("nil inventory = %v", err)
	}
}

func mustNewPluginRuntimeFullSetApplier(
	t *testing.T,
	manager *Manager,
	inventory PluginRuntimeFullSetInventory,
) *ManagerPluginRuntimeFullSetApplier {
	t.Helper()
	applier, err := NewManagerPluginRuntimeFullSetApplier(manager, inventory)
	if err != nil {
		t.Fatal(err)
	}
	return applier
}

func assertActiveCallable(t *testing.T, manager *Manager, extensionID string, want RuntimeInstanceIdentity) {
	t.Helper()
	snap, lease, err := manager.AcquireActiveRuntimeCall(context.Background(), extensionID, RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if snap.Identity != want || snap.Admission.Draining {
		t.Fatalf("callable snapshot = %#v want %#v", snap, want)
	}
}

func pluginRuntimeFullSetDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

type pluginRuntimeFullSetTestInventory struct {
	mu              sync.Mutex
	extensions      map[string]extensions.Extension
	versions        map[string]extensions.ExtensionVersion
	latest          extensions.PluginRuntimePublication
	afterGetVersion func()
	onLatest        func()
}

func newPluginRuntimeFullSetTestInventory() *pluginRuntimeFullSetTestInventory {
	return &pluginRuntimeFullSetTestInventory{
		extensions: make(map[string]extensions.Extension),
		versions:   make(map[string]extensions.ExtensionVersion),
	}
}

func (i *pluginRuntimeFullSetTestInventory) versionKey(id, version, digest string) string {
	return id + "\x00" + version + "\x00" + digest
}

func (i *pluginRuntimeFullSetTestInventory) seed(
	t *testing.T,
	id string,
	versionID int64,
	version string,
	digestLabel string,
) extensions.Extension {
	t.Helper()
	digest := pluginRuntimeFullSetDigest(digestLabel)
	extension := managerStagedExtension(id, version, digest)
	extension.ActiveVersionID = versionID
	extension.Name = id
	extension.Status = extensions.StatusEnabled
	extension.Source = "test"
	i.mu.Lock()
	defer i.mu.Unlock()
	// Get 故意返回错误的活动版本字段，确保 applier 只信 GetExtensionVersion。
	meta := extension
	meta.Version = "should-not-trust"
	meta.PackageDigest = pluginRuntimeFullSetDigest("should-not-trust")
	meta.ActiveVersionID = 0
	meta.Manifest.Version = "should-not-trust"
	i.extensions[id] = meta
	i.versions[i.versionKey(id, version, digest)] = extensions.ExtensionVersion{
		ID: versionID, Version: version, Manifest: extension.Manifest,
		PackageDigest: digest, PackagePath: extension.PackagePath,
		InstalledAt: time.Unix(versionID, 0).UTC(),
	}
	return extension
}

func (i *pluginRuntimeFullSetTestInventory) member(
	id string,
	versionID int64,
	version string,
	digestLabel string,
) extensions.PluginRuntimeMember {
	return extensions.PluginRuntimeMember{
		ExtensionID:        id,
		ExtensionVersionID: versionID,
		ExtensionVersion:   version,
		PackageDigest:      pluginRuntimeFullSetDigest(digestLabel),
	}
}

func (i *pluginRuntimeFullSetTestInventory) setHooks(
	id, version, digestLabel string,
	hooks []extensions.ManifestHook,
	dependencies []extensions.ManifestDependency,
) {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := i.versionKey(id, version, pluginRuntimeFullSetDigest(digestLabel))
	exact := i.versions[key]
	exact.Manifest.Hooks = append([]extensions.ManifestHook(nil), hooks...)
	exact.Manifest.Dependencies = append([]extensions.ManifestDependency(nil), dependencies...)
	i.versions[key] = exact
	metadata := i.extensions[id]
	metadata.Manifest.Hooks = append([]extensions.ManifestHook(nil), hooks...)
	metadata.Manifest.Dependencies = append([]extensions.ManifestDependency(nil), dependencies...)
	i.extensions[id] = metadata
}

func (i *pluginRuntimeFullSetTestInventory) setCapabilities(
	id, version, digestLabel string,
	exactCapabilities, metadataCapabilities []string,
) {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := i.versionKey(id, version, pluginRuntimeFullSetDigest(digestLabel))
	exact := i.versions[key]
	exact.Manifest.Capabilities = append([]string(nil), exactCapabilities...)
	i.versions[key] = exact
	metadata := i.extensions[id]
	metadata.Manifest.Capabilities = append([]string(nil), metadataCapabilities...)
	i.extensions[id] = metadata
}

func (i *pluginRuntimeFullSetTestInventory) publication(
	reason extensions.PluginRuntimePublicationReason,
	members ...extensions.PluginRuntimeMember,
) extensions.PluginRuntimePublication {
	cloned := append([]extensions.PluginRuntimeMember(nil), members...)
	sort.Slice(cloned, func(a, b int) bool { return cloned[a].ExtensionID > cloned[b].ExtensionID })
	digest, err := extensions.PluginRuntimeMembersDigest(cloned)
	if err != nil {
		panic(err)
	}
	publication := extensions.PluginRuntimePublication{
		Revision:      1,
		MemberCount:   len(cloned),
		MembersDigest: digest,
		Members:       cloned,
		Reason:        reason,
		CreatedAt:     time.Unix(1_700_000_000, 0).UTC(),
	}
	i.setLatest(publication)
	return publication
}

func (i *pluginRuntimeFullSetTestInventory) setLatest(publication extensions.PluginRuntimePublication) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.latest = publication
	i.latest.Members = append([]extensions.PluginRuntimeMember(nil), publication.Members...)
}

func (i *pluginRuntimeFullSetTestInventory) setReadHooks(afterGetVersion, onLatest func()) {
	i.mu.Lock()
	i.afterGetVersion = afterGetVersion
	i.onLatest = onLatest
	i.mu.Unlock()
}

func (i *pluginRuntimeFullSetTestInventory) LatestPluginRuntimePublication(
	_ context.Context,
) (extensions.PluginRuntimePublication, error) {
	i.mu.Lock()
	onLatest := i.onLatest
	if i.latest.Revision <= 0 {
		i.mu.Unlock()
		return extensions.PluginRuntimePublication{}, extensions.ErrPluginRuntimePublicationNotFound
	}
	latest := i.latest
	latest.Members = append([]extensions.PluginRuntimeMember(nil), i.latest.Members...)
	i.mu.Unlock()
	if onLatest != nil {
		onLatest()
	}
	return latest, nil
}

func (i *pluginRuntimeFullSetTestInventory) Get(_ context.Context, extensionID string) (extensions.Extension, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	extension, ok := i.extensions[extensionID]
	if !ok {
		return extensions.Extension{}, extensions.ErrExtensionNotFound
	}
	return extension, nil
}

func (i *pluginRuntimeFullSetTestInventory) GetExtensionVersion(
	_ context.Context,
	input extensions.ExactExtensionVersionInput,
) (extensions.ExtensionVersion, error) {
	i.mu.Lock()
	version, ok := i.versions[i.versionKey(input.ExtensionID, input.Version, input.PackageDigest)]
	afterGetVersion := i.afterGetVersion
	i.mu.Unlock()
	if !ok {
		return extensions.ExtensionVersion{}, extensions.ErrExtensionVersionNotFound
	}
	if afterGetVersion != nil {
		afterGetVersion()
	}
	return version, nil
}
