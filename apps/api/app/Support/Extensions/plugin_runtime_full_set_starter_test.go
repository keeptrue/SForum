package extensionsruntime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// pluginRuntimeFullSetStarter reuses managerStagedStarter and adds deterministic
// health, publish, stop and concurrency fault injection for full-set tests.
type pluginRuntimeFullSetStarter struct {
	inner              *managerStagedStarter
	mu                 sync.Mutex
	healthErrors       map[RuntimeInstanceIdentity]error
	failNextHealth     error
	failPublishAfter   int
	publishCount       int
	stopErrors         map[RuntimeInstanceIdentity]error
	acquireSetError    error
	revalidateSetError error
	revalidateSetHook  func()
	finalFenceError    error
	finalFenceHook     func()
	startDelay         time.Duration
	inFlightStarts     atomic.Int32
	maxInFlightStarts  atomic.Int32
	startedExtensions  map[RuntimeInstanceIdentity]extensions.Extension
	// stagedStartCount 统计 Protocol V2 StartInstance 调用次数（用于证明真正进入 V2 stage）。
	stagedStartCount atomic.Int32
}

func newPluginRuntimeFullSetStarter() *pluginRuntimeFullSetStarter {
	return &pluginRuntimeFullSetStarter{
		inner:             newManagerStagedStarter(),
		healthErrors:      make(map[RuntimeInstanceIdentity]error),
		stopErrors:        make(map[RuntimeInstanceIdentity]error),
		startedExtensions: make(map[RuntimeInstanceIdentity]extensions.Extension),
	}
}

func (s *pluginRuntimeFullSetStarter) Start(ctx context.Context, extension extensions.Extension) (RouteTarget, error) {
	target, err := s.inner.Start(ctx, extension)
	if err != nil {
		return target, err
	}
	identity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: target.InstanceID}
	s.mu.Lock()
	s.startedExtensions[identity] = extension
	s.mu.Unlock()
	return target, nil
}

func (s *pluginRuntimeFullSetStarter) Stop(ctx context.Context, extension extensions.Extension) error {
	return s.inner.Stop(ctx, extension)
}

func (s *pluginRuntimeFullSetStarter) StartInstance(ctx context.Context, extension extensions.Extension) (RouteTarget, error) {
	cur := s.inFlightStarts.Add(1)
	for {
		prev := s.maxInFlightStarts.Load()
		if cur <= prev || s.maxInFlightStarts.CompareAndSwap(prev, cur) {
			break
		}
	}
	defer s.inFlightStarts.Add(-1)
	if s.startDelay > 0 {
		select {
		case <-ctx.Done():
			return RouteTarget{}, ctx.Err()
		case <-time.After(s.startDelay):
		}
	}
	target, err := s.inner.StartInstance(ctx, extension)
	if err != nil {
		return RouteTarget{}, err
	}
	s.stagedStartCount.Add(1)
	s.mu.Lock()
	s.startedExtensions[RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: target.InstanceID}] = extension
	s.mu.Unlock()
	return target, nil
}

func (s *pluginRuntimeFullSetStarter) InspectInstance(identity RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error) {
	return s.inner.InspectInstance(identity)
}

func (s *pluginRuntimeFullSetStarter) HealthInstance(ctx context.Context, identity RuntimeInstanceIdentity) (PluginHealth, error) {
	snapshot, _ := s.inner.InspectInstance(identity)
	s.mu.Lock()
	if snapshot.State == ProtocolRuntimePublished && s.revalidateSetError != nil {
		err := s.revalidateSetError
		hook := s.revalidateSetHook
		s.revalidateSetError = nil
		s.revalidateSetHook = nil
		s.mu.Unlock()
		if hook != nil {
			hook()
		}
		return PluginHealth{}, err
	}
	if err := s.failNextHealth; err != nil {
		s.failNextHealth = nil
		s.mu.Unlock()
		return PluginHealth{}, err
	}
	if err := s.healthErrors[identity]; err != nil {
		s.mu.Unlock()
		return PluginHealth{}, err
	}
	s.mu.Unlock()
	return s.inner.HealthInstance(ctx, identity)
}

func (s *pluginRuntimeFullSetStarter) PublishInstance(ctx context.Context, identity RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error) {
	s.mu.Lock()
	s.publishCount++
	count := s.publishCount
	if s.failPublishAfter > 0 && count == s.failPublishAfter+1 {
		s.failPublishAfter = 0
		s.mu.Unlock()
		return ProtocolRuntimeInstanceSnapshot{}, fmt.Errorf("forced publish failure on call %d", count)
	}
	s.mu.Unlock()
	return s.inner.PublishInstance(ctx, identity)
}

func (s *pluginRuntimeFullSetStarter) PublishInstanceSet(
	ctx context.Context,
	identities []RuntimeInstanceIdentity,
) ([]ProtocolRuntimeInstanceSnapshot, *ProtocolRuntimeSetLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	desired, err := normalizeProtocolRuntimeInstanceSet(identities)
	if err != nil {
		return nil, nil, err
	}

	// Run every deterministic fault/barrier before mutating the fake's active
	// map, matching the production complete-set failure guarantee.
	for _, identity := range desired {
		if s.inner.activeIdentity(identity.ExtensionID) == identity {
			continue
		}
		if s.inner.publishStarted != nil {
			s.inner.publishStarted <- struct{}{}
			<-s.inner.publishContinue
		}
		s.mu.Lock()
		s.publishCount++
		count := s.publishCount
		if s.failPublishAfter > 0 && count == s.failPublishAfter+1 {
			s.failPublishAfter = 0
			s.mu.Unlock()
			return nil, nil, fmt.Errorf("forced publish failure on call %d", count)
		}
		s.mu.Unlock()
	}

	s.inner.mu.Lock()
	defer s.inner.mu.Unlock()
	for _, identity := range desired {
		if err := s.inner.publishErrors[identity]; err != nil {
			return nil, nil, err
		}
		snapshot, ok := s.inner.instances[identity]
		if !ok {
			return nil, nil, ErrRuntimeInstanceNotFound
		}
		if !snapshot.Ready {
			return nil, nil, ErrProtocolInstanceNotReady
		}
	}
	desiredSet := make(map[string]RuntimeInstanceIdentity, len(desired))
	for _, identity := range desired {
		desiredSet[identity.ExtensionID] = identity
	}
	for extensionID, previous := range s.inner.active {
		if desiredSet[extensionID] == previous {
			continue
		}
		snapshot := s.inner.instances[previous]
		snapshot.State = ProtocolRuntimeRetained
		s.inner.instances[previous] = snapshot
	}
	nextActive := make(map[string]RuntimeInstanceIdentity, len(desired))
	result := make([]ProtocolRuntimeInstanceSnapshot, 0, len(desired))
	for _, identity := range desired {
		if previous := s.inner.active[identity.ExtensionID]; previous.InstanceID != "" && previous != identity {
			snapshot := s.inner.instances[previous]
			snapshot.State = ProtocolRuntimeRetained
			s.inner.instances[previous] = snapshot
		}
		snapshot := s.inner.instances[identity]
		snapshot.State = ProtocolRuntimePublished
		s.inner.instances[identity] = snapshot
		nextActive[identity.ExtensionID] = identity
		result = append(result, snapshot)
	}
	s.inner.active = nextActive
	s.mu.Lock()
	s.revalidateSetError = s.acquireSetError
	s.acquireSetError = nil
	s.mu.Unlock()
	lease := &ProtocolRuntimeSetLease{release: func() {}}
	lease.restore = func(restoreCtx context.Context, restoreIdentities []RuntimeInstanceIdentity) ([]ProtocolRuntimeInstanceSnapshot, error) {
		restored, nested, restoreErr := s.PublishInstanceSet(restoreCtx, restoreIdentities)
		if nested != nil {
			nested.Release()
		}
		return restored, restoreErr
	}
	lease.validate = func(validateCtx context.Context, validateIdentities []RuntimeInstanceIdentity) error {
		s.mu.Lock()
		err := s.finalFenceError
		hook := s.finalFenceHook
		s.finalFenceError = nil
		s.finalFenceHook = nil
		s.mu.Unlock()
		if hook != nil {
			hook()
		}
		if err != nil {
			return err
		}
		if !s.RuntimeInstanceSetVisible(validateCtx, validateIdentities) {
			return ErrProtocolInstanceNotReady
		}
		return nil
	}
	return result, lease, nil
}

func (s *pluginRuntimeFullSetStarter) RuntimeInstanceSetVisible(ctx context.Context, identities []RuntimeInstanceIdentity) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	desired, err := normalizeProtocolRuntimeInstanceSet(identities)
	if err != nil {
		return false
	}
	s.inner.mu.Lock()
	defer s.inner.mu.Unlock()
	if len(s.inner.active) != len(desired) {
		return false
	}
	for _, identity := range desired {
		if s.inner.active[identity.ExtensionID] != identity {
			return false
		}
		snapshot, ok := s.inner.instances[identity]
		if !ok || snapshot.State != ProtocolRuntimePublished {
			return false
		}
	}
	return true
}

func (s *pluginRuntimeFullSetStarter) RunLifecycleInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	invocation LifecycleInvocation,
) (LifecycleRunResult, error) {
	return s.inner.RunLifecycleInstance(ctx, identity, extension, invocation)
}

func (s *pluginRuntimeFullSetStarter) StopInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	s.mu.Lock()
	err := s.stopErrors[identity]
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.inner.StopInstance(ctx, identity)
}

func (s *pluginRuntimeFullSetStarter) DiscardInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	return s.inner.DiscardInstance(ctx, identity)
}

func (s *pluginRuntimeFullSetStarter) failStop(identity RuntimeInstanceIdentity, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopErrors[identity] = err
}

func (s *pluginRuntimeFullSetStarter) resetPublishFault(failAfter int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishCount = 0
	s.failPublishAfter = failAfter
}

func (s *pluginRuntimeFullSetStarter) startCount() int {
	s.inner.mu.Lock()
	defer s.inner.mu.Unlock()
	return s.inner.next
}

func (s *pluginRuntimeFullSetStarter) startedExtension(identity RuntimeInstanceIdentity) (extensions.Extension, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	extension, ok := s.startedExtensions[identity]
	return extension, ok
}

// Full-set apply must accept exact Protocol V2 process members that omit optional
// Manifest.lifecycle (for example genesis admin-surface-reference plugins).
func TestPluginRuntimeFullSetAppliesProtocolV2WithoutLifecycle(t *testing.T) {
	starter := newPluginRuntimeFullSetStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	inventory := newPluginRuntimeFullSetTestInventory()
	plugin := inventory.seed(t, "sforum.admin-surface-reference", 7, "1.0.0", "admin-surface-no-lifecycle")

	inventory.mu.Lock()
	key := inventory.versionKey(plugin.ID, plugin.Version, plugin.PackageDigest)
	version := inventory.versions[key]
	version.Manifest.Lifecycle = nil
	inventory.versions[key] = version
	metadata := inventory.extensions[plugin.ID]
	metadata.Manifest.Lifecycle = nil
	inventory.extensions[plugin.ID] = metadata
	inventory.mu.Unlock()

	if err := validateManagedStagedExtension(pluginRuntimeExactExtension(
		inventory.extensions[plugin.ID], version, inventory.member(plugin.ID, 7, "1.0.0", "admin-surface-no-lifecycle"),
	)); err != nil {
		t.Fatalf("desired Protocol V2 without lifecycle: %v", err)
	}

	applier := mustNewPluginRuntimeFullSetApplier(t, manager, inventory)
	applied, err := applier.ApplyPluginRuntimeFullSet(context.Background(), inventory.publication(
		extensions.PluginRuntimePublicationEnable,
		inventory.member(plugin.ID, 7, "1.0.0", "admin-surface-no-lifecycle"),
	))
	if err != nil {
		t.Fatalf("full-set apply Protocol V2 without lifecycle: %v", err)
	}
	if len(applied) != 1 || applied[0].ExtensionID != plugin.ID || applied[0].ExtensionVersion != "1.0.0" {
		t.Fatalf("applied = %#v", applied)
	}
	active, err := manager.ActiveRuntimeInstance(plugin.ID)
	if err != nil || active.ExtensionVersion != "1.0.0" || active.ArtifactDigest != plugin.PackageDigest {
		t.Fatalf("active without lifecycle = %#v, %v", active, err)
	}
}

var _ StagedRuntimeStarter = (*pluginRuntimeFullSetStarter)(nil)
var _ StagedRuntimeSetStarter = (*pluginRuntimeFullSetStarter)(nil)
