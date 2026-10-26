package extensionsruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestExecutableTrustRevocationFenceClosesExactRuntimeAndPolicy(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "trust.fence")
	active, lease, err := manager.AcquireActiveRuntimeCall(t.Context(), "trust.fence", RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	policy := newRecordingExecutableTrustPolicy(active)
	fence := NewExecutableTrustRevocationFence(manager, policy)
	durableCalls := 0
	if err := fence.RevokeExecutableTrust(t.Context(), "trust.fence", "operator_revoked", func(context.Context) error {
		durableCalls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if durableCalls != 1 || policy.invalidateCalls() != 1 {
		t.Fatalf("durable=%d policy=%d", durableCalls, policy.invalidateCalls())
	}
	if manager.RuntimeInstanceAvailable(active.Identity) {
		t.Fatal("revoked runtime remained available")
	}
	select {
	case <-lease.Context.Done():
		t.Fatal("trust revoke cancelled an existing call")
	default:
	}
	if _, _, err := manager.AcquireActiveRuntimeCall(t.Context(), "trust.fence", RuntimeCallRoute); !errors.Is(err, ErrRuntimeAdmissionQuarantined) || !errors.Is(err, ErrRuntimeTrustRevoked) {
		t.Fatalf("admission error=%v", err)
	}
}

func TestExecutableTrustRevocationFenceClosesEveryRetainedExactArtifactInstance(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerStagedExtension("trust.retained-artifact", "1.0.0", "digest-1")
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	source, _ := manager.ActiveRuntimeInstance(extension.ID)
	target, err := manager.StageRuntimeInstance(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(t.Context(), target.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(source.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishRuntimeInstanceFrom(t.Context(), target.Identity, RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: source.Identity,
		ExtensionVersion:        source.ExtensionVersion,
		ArtifactDigest:          source.ArtifactDigest,
	}); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || active.Identity != target.Identity {
		t.Fatalf("active exact target = %#v, %v", active, err)
	}
	policy := newRecordingExecutableTrustPolicy(active)
	fence := NewExecutableTrustRevocationFence(manager, policy)
	if err := fence.RevokeExecutableTrust(t.Context(), extension.ID, "operator_revoked", func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []RuntimeInstanceIdentity{source.Identity, target.Identity} {
		snapshot, err := manager.InspectRuntimeInstance(identity)
		if err != nil || !snapshot.Admission.Quarantined || !errors.Is(snapshot.Admission.QuarantineCause, ErrRuntimeTrustRevoked) {
			t.Fatalf("retained exact instance %s = %#v, %v", identity.InstanceID, snapshot, err)
		}
	}
}

func TestExecutableTrustRevocationFenceKeepsLocalStateWhenDurableFails(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "trust.failed")
	active, err := manager.ActiveRuntimeInstance("trust.failed")
	if err != nil {
		t.Fatal(err)
	}
	policy := newRecordingExecutableTrustPolicy(active)
	fence := NewExecutableTrustRevocationFence(manager, policy)
	durableErr := errors.New("durable revoke failed")
	err = fence.RevokeExecutableTrust(t.Context(), "trust.failed", "operator_revoked", func(context.Context) error {
		if _, lease, admissionErr := manager.AcquireActiveRuntimeCall(t.Context(), "trust.failed", RuntimeCallRoute); !errors.Is(admissionErr, ErrRuntimeAdmissionDraining) {
			if lease != nil {
				lease.Release()
			}
			t.Fatalf("new admission crossed pre-commit drain: %v", admissionErr)
		}
		return durableErr
	})
	if !errors.Is(err, durableErr) {
		t.Fatalf("durable error=%v", err)
	}
	if !manager.RuntimeInstanceAvailable(active.Identity) || policy.invalidateCalls() != 0 || policy.releaseCalls() != 1 {
		t.Fatalf("failed durable revoke changed local state: available=%t invalidations=%d releases=%d",
			manager.RuntimeInstanceAvailable(active.Identity), policy.invalidateCalls(), policy.releaseCalls())
	}
	_, lease, err := manager.AcquireActiveRuntimeCall(t.Context(), "trust.failed", RuntimeCallRoute)
	if err != nil {
		t.Fatalf("definite failure did not resume exact gate: %v", err)
	}
	lease.Release()
}

func TestExecutableTrustRevocationFenceResumesAfterRequestCancellation(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "trust.cancelled")
	active, err := manager.ActiveRuntimeInstance("trust.cancelled")
	if err != nil {
		t.Fatal(err)
	}
	policy := newRecordingExecutableTrustPolicy(active)
	fence := NewExecutableTrustRevocationFence(manager, policy)
	ctx, cancel := context.WithCancel(t.Context())
	err = fence.RevokeExecutableTrust(ctx, "trust.cancelled", "operator_revoked", func(context.Context) error {
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled durable error=%v", err)
	}
	if !manager.RuntimeInstanceAvailable(active.Identity) || policy.invalidateCalls() != 0 || policy.releaseCalls() != 1 {
		t.Fatalf("cancelled durable revoke changed local state: available=%t invalidations=%d releases=%d",
			manager.RuntimeInstanceAvailable(active.Identity), policy.invalidateCalls(), policy.releaseCalls())
	}
}

func TestExecutableTrustRevocationFenceFailsClosedOnUnknownCommit(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "trust.unknown")
	active, err := manager.ActiveRuntimeInstance("trust.unknown")
	if err != nil {
		t.Fatal(err)
	}
	policy := newRecordingExecutableTrustPolicy(active)
	fence := NewExecutableTrustRevocationFence(manager, policy)
	commitErr := errors.New("commit response lost")
	err = fence.RevokeExecutableTrust(t.Context(), "trust.unknown", "operator_revoked", func(context.Context) error {
		return errors.Join(extensions.ErrTrustRevocationCommitUnknown, commitErr)
	})
	if !errors.Is(err, extensions.ErrTrustRevocationCommitUnknown) || !errors.Is(err, commitErr) {
		t.Fatalf("unknown commit error=%v", err)
	}
	if manager.RuntimeInstanceAvailable(active.Identity) || policy.invalidateCalls() != 1 {
		t.Fatalf("unknown commit reopened local state: available=%t invalidations=%d",
			manager.RuntimeInstanceAvailable(active.Identity), policy.invalidateCalls())
	}
	if _, _, admissionErr := manager.AcquireActiveRuntimeCall(t.Context(), "trust.unknown", RuntimeCallRoute); !errors.Is(admissionErr, ErrRuntimeAdmissionQuarantined) || !errors.Is(admissionErr, ErrRuntimeTrustRevoked) {
		t.Fatalf("unknown commit admission error=%v", admissionErr)
	}
}

func TestExecutableTrustRevocationFenceReplayNeverReopensQuarantinedRuntime(t *testing.T) {
	const extensionID = "trust.replay"
	manager := newTwoInstanceRuntimeManager(t, extensionID)
	active, lease, err := manager.AcquireActiveRuntimeCall(t.Context(), extensionID, RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	policy := newRecordingExecutableTrustPolicy(active)
	fence := NewExecutableTrustRevocationFence(manager, policy)
	durableCalls := 0
	for attempt := 0; attempt < 2; attempt++ {
		if err := fence.RevokeExecutableTrust(t.Context(), extensionID, "operator_revoked", func(context.Context) error {
			durableCalls++
			return nil
		}); err != nil {
			t.Fatalf("successful replay %d: %v", attempt, err)
		}
	}
	durableErr := errors.New("replayed durable revoke failed")
	err = fence.RevokeExecutableTrust(t.Context(), extensionID, "operator_revoked", func(context.Context) error {
		durableCalls++
		return durableErr
	})
	if !errors.Is(err, durableErr) {
		t.Fatalf("failed replay error=%v", err)
	}
	if durableCalls != 3 || policy.invalidateCalls() != 2 || policy.releaseCalls() != 1 {
		t.Fatalf(
			"durable=%d invalidations=%d releases=%d",
			durableCalls, policy.invalidateCalls(), policy.releaseCalls(),
		)
	}
	if manager.RuntimeInstanceAvailable(active.Identity) {
		t.Fatal("failed replay reopened the already quarantined runtime")
	}
	if _, _, admissionErr := manager.AcquireActiveRuntimeCall(
		t.Context(), extensionID, RuntimeCallRoute,
	); !errors.Is(admissionErr, ErrRuntimeAdmissionQuarantined) ||
		!errors.Is(admissionErr, ErrRuntimeTrustRevoked) {
		t.Fatalf("replay admission error=%v", admissionErr)
	}
	select {
	case <-lease.Context.Done():
		t.Fatal("replayed revoke cancelled an existing lease")
	default:
	}
}

func TestExecutableTrustRevocationFenceInvalidatesPolicyWithoutRuntime(t *testing.T) {
	manager := newTwoInstanceRuntimeManager(t, "trust.absent")
	policy := &recordingExecutableTrustPolicy{lookup: extensions.GuardPolicyLookup{
		Found: true,
		Entry: extensions.GuardPolicyEntry{
			ExtensionID: "missing.plugin", Version: "1.0.0", PackageDigest: "missing-digest",
			CurrentTrustRequired: true, CurrentArtifactTrusted: true,
		},
	}}
	fence := NewExecutableTrustRevocationFence(manager, policy)
	durableCalls := 0
	if err := fence.RevokeExecutableTrust(t.Context(), "missing.plugin", "operator_revoked", func(context.Context) error {
		durableCalls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if durableCalls != 1 || policy.invalidateCalls() != 1 {
		t.Fatalf("durable=%d policy=%d", durableCalls, policy.invalidateCalls())
	}
}

func TestExecutableTrustRevocationFenceUsesActiveRuntimeWhenPolicyIsMissing(t *testing.T) {
	const extensionID = "trust.policy-missing"
	manager := newTwoInstanceRuntimeManager(t, extensionID)
	active, err := manager.ActiveRuntimeInstance(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	policy := &recordingExecutableTrustPolicy{}
	fence := NewExecutableTrustRevocationFence(manager, policy)
	commitErr := errors.New("commit response lost")
	err = fence.RevokeExecutableTrust(t.Context(), extensionID, "operator_revoked", func(context.Context) error {
		return errors.Join(extensions.ErrTrustRevocationCommitUnknown, commitErr)
	})
	if !errors.Is(err, extensions.ErrTrustRevocationCommitUnknown) || !errors.Is(err, commitErr) {
		t.Fatalf("unknown commit error=%v", err)
	}
	invalidated := policy.invalidatedEntries()
	if len(invalidated) != 1 || invalidated[0].ExtensionID != extensionID ||
		invalidated[0].Version != active.ExtensionVersion ||
		invalidated[0].PackageDigest != active.ArtifactDigest ||
		!invalidated[0].CurrentTrustRequired || !invalidated[0].CurrentArtifactTrusted {
		t.Fatalf("runtime fallback invalidation=%#v", invalidated)
	}
}

func TestExecutableTrustRevocationFenceBlocksReplacementUntilExactClosure(t *testing.T) {
	const extensionID = "trust.concurrent"
	starter := &managerRuntimeStarter{results: []managerRuntimeStartResult{
		{target: RouteTarget{InstanceID: "old-instance"}},
		{target: RouteTarget{InstanceID: "new-instance"}},
	}}
	manager := NewManager(ManagerConfig{Starter: starter})
	oldExtension := managerRuntimeExtension(extensionID, "1.0.0", "old-digest")
	if err := manager.Start(t.Context(), oldExtension); err != nil {
		t.Fatal(err)
	}
	old, lease, err := manager.AcquireActiveRuntimeCall(t.Context(), extensionID, RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	policy := newRecordingExecutableTrustPolicy(old)
	fence := NewExecutableTrustRevocationFence(manager, policy)
	durableStarted := make(chan struct{})
	releaseDurable := make(chan struct{})
	revokeDone := make(chan error, 1)
	go func() {
		revokeDone <- fence.RevokeExecutableTrust(t.Context(), extensionID, "operator_revoked", func(ctx context.Context) error {
			close(durableStarted)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseDurable:
				return nil
			}
		})
	}()
	<-durableStarted
	barrierWait := &runtimeSetBarrierWaitContext{
		Context: t.Context(), entered: make(chan struct{}),
	}
	startDone := make(chan error, 1)
	go func() {
		startDone <- manager.Start(barrierWait, managerRuntimeExtension(extensionID, "2.0.0", "new-digest"))
	}()
	select {
	case <-barrierWait.entered:
	case <-time.After(time.Second):
		t.Fatal("replacement did not enter the Manager barrier wait")
	}
	select {
	case err := <-startDone:
		t.Fatalf("replacement crossed revoke barrier: %v", err)
	default:
	}
	close(releaseDurable)
	if err := <-revokeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extensionID)
	if err != nil || active.ExtensionVersion != "2.0.0" || active.ArtifactDigest != "new-digest" ||
		!manager.RuntimeInstanceAvailable(active.Identity) {
		t.Fatalf("replacement=%#v err=%v", active, err)
	}
	oldAfter, err := manager.InspectRuntimeInstance(old.Identity)
	if err != nil || !oldAfter.Admission.Quarantined {
		t.Fatalf("old exact runtime=%#v err=%v", oldAfter, err)
	}
	select {
	case <-lease.Context.Done():
		t.Fatal("revoke cancelled the existing old-runtime lease")
	default:
	}
}

func TestExecutableTrustRevocationFenceBlocksSupersededFullSetReopen(t *testing.T) {
	const extensionID = "trust.superseded"
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	extension := inventory.seed(t, extensionID, 1, "1.0.0", "trust-old")
	member := inventory.member(extensionID, 1, "1.0.0", "trust-old")
	oldPublication := inventory.publication(extensions.PluginRuntimePublicationEnable, member)
	oldPublication.Revision = 1
	inventory.setLatest(oldPublication)
	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	if _, err := applier.ApplyPluginRuntimeFullSet(t.Context(), oldPublication); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy := newRecordingExecutableTrustPolicy(active)
	fence := NewExecutableTrustRevocationFence(manager, policy)
	staleDone := make(chan error, 1)
	removal := inventory.publication(extensions.PluginRuntimePublicationRecovery)
	removal.Revision = 2
	inventory.setLatest(oldPublication)
	latestRead := make(chan struct{})
	var latestOnce sync.Once
	inventory.setReadHooks(
		nil,
		func() { latestOnce.Do(func() { close(latestRead) }) },
	)
	barrierWait := &runtimeSetBarrierWaitContext{
		Context: t.Context(), entered: make(chan struct{}),
	}

	err = fence.RevokeExecutableTrust(t.Context(), extensionID, "operator_revoked", func(context.Context) error {
		go func() {
			_, staleErr := applier.ApplyPluginRuntimeFullSet(barrierWait, oldPublication)
			staleDone <- staleErr
		}()
		select {
		case <-barrierWait.entered:
		case <-time.After(time.Second):
			t.Fatal("stale apply did not enter the Manager barrier wait")
		}
		// Done is first evaluated by lockRuntimeSetTransition's select, after exact
		// inventory resolution. Because this revoke still owns the empty token
		// channel, the stale goroutine cannot cross the barrier after this signal.
		select {
		case <-latestRead:
			t.Fatal("stale apply rechecked Latest before the Manager barrier")
		default:
		}
		// This models the atomic PostgreSQL revoke + R+1 desired publication.
		inventory.setLatest(removal)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if staleErr := <-staleDone; !errors.Is(staleErr, extensions.ErrPluginRuntimePublicationSuperseded) {
		t.Fatalf("stale publication error=%v", staleErr)
	}
	if manager.RuntimeInstanceAvailable(active.Identity) {
		t.Fatal("superseded publication reopened the revoked exact runtime")
	}

	// A full-set coordinator may skip the intermediate removal and converge
	// directly to a later exact reauthorization. The quarantined R1 process must
	// be replaced by R3 rather than reused or allowed to block convergence.
	inventory.seed(t, extensionID, 2, "2.0.0", "trust-new")
	reauthorized := inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(extensionID, 2, "2.0.0", "trust-new"),
	)
	reauthorized.Revision = 3
	inventory.setLatest(reauthorized)
	if _, err := applier.ApplyPluginRuntimeFullSet(t.Context(), reauthorized); err != nil {
		t.Fatalf("apply reauthorized artifact: %v", err)
	}
	restarted, err := manager.ActiveRuntimeInstance(extensionID)
	if err != nil || restarted.ExtensionVersion != "2.0.0" ||
		restarted.ArtifactDigest != inventory.member(extensionID, 2, "2.0.0", "trust-new").PackageDigest ||
		!manager.RuntimeInstanceAvailable(restarted.Identity) {
		t.Fatalf("reauthorized runtime=%#v err=%v", restarted, err)
	}
	if generation := manager.HookBus().RuntimeRegistryGeneration(); generation.PublicationRevision != reauthorized.Revision {
		t.Fatalf("reauthorized publication revision=%#v", generation)
	}
	assertActiveCallable(t, manager, extensionID, restarted.Identity)
}

// runtimeSetBarrierWaitContext exposes the exact point where
// lockRuntimeSetTransition evaluates ctx.Done before blocking on the Manager
// token. Callers use only Err before that point, so entered is a deterministic
// post-resolve/pre-acquire observation rather than a scheduler delay heuristic.
type runtimeSetBarrierWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *runtimeSetBarrierWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

type recordingExecutableTrustPolicy struct {
	mu          sync.Mutex
	lookup      extensions.GuardPolicyLookup
	pending     *extensions.GuardPolicyEntry
	releases    int
	invalidated []extensions.GuardPolicyEntry
}

func newRecordingExecutableTrustPolicy(active RuntimeInstanceSnapshot) *recordingExecutableTrustPolicy {
	return &recordingExecutableTrustPolicy{lookup: extensions.GuardPolicyLookup{
		Found: true,
		Entry: extensions.GuardPolicyEntry{
			ExtensionID: active.Identity.ExtensionID,
			Version:     active.ExtensionVersion, PackageDigest: active.ArtifactDigest,
			CurrentTrustRequired: true, CurrentArtifactTrusted: true,
		},
	}}
}

func (p *recordingExecutableTrustPolicy) CaptureExecutableTrustExactWithFallback(
	extensionID string,
	fallback extensions.GuardPolicyEntry,
) (extensions.GuardPolicyEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.lookup.Entry
	if !p.lookup.Found || entry.ExtensionID != extensionID {
		entry = fallback
		p.pending = &entry
		return entry, false
	}
	p.pending = &entry
	return entry, true
}

func (p *recordingExecutableTrustPolicy) invalidatedEntries() []extensions.GuardPolicyEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]extensions.GuardPolicyEntry(nil), p.invalidated...)
}

func (p *recordingExecutableTrustPolicy) ReleaseExecutableTrustCaptureExact(
	extensionID string,
	captured extensions.GuardPolicyEntry,
) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil || captured.ExtensionID != extensionID || *p.pending != captured {
		return false
	}
	p.pending = nil
	p.releases++
	return true
}

func (p *recordingExecutableTrustPolicy) InvalidateExecutableTrustExact(
	extensionID string,
	captured extensions.GuardPolicyEntry,
) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if captured.ExtensionID != extensionID {
		return false
	}
	if p.pending != nil && *p.pending == captured {
		p.pending = nil
	}
	p.invalidated = append(p.invalidated, captured)
	return true
}

func (p *recordingExecutableTrustPolicy) invalidateCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.invalidated)
}

func (p *recordingExecutableTrustPolicy) releaseCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.releases
}
