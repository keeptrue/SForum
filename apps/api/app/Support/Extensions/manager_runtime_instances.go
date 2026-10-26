package extensionsruntime

import (
	"context"
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

type managedRuntimeInstance struct {
	extension        extensions.Extension
	extensionVersion string
	artifactDigest   string
	target           RouteTarget
	gate             *RuntimeAdmissionGate
	transitioning    bool
}

// AcquireActiveRuntimeCall 在线性化边界内捕获活动实例并取得普通调用 lease。
// 返回的 target 与 lease 始终属于同一个 exact instance，调用方必须 Release。
func (m *InstanceAdmission) AcquireActiveRuntimeCall(ctx context.Context, extensionID string, class RuntimeCallClass) (RuntimeInstanceSnapshot, *RuntimeAdmissionLease, error) {
	if ctx == nil || strings.TrimSpace(string(class)) == "" {
		return RuntimeInstanceSnapshot{}, nil, ErrRuntimeAdmissionInvalid
	}
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return RuntimeInstanceSnapshot{}, nil, ErrRuntimeAdmissionInvalid
	}

	m.mu.RLock()
	instanceID := m.activeInstances[extensionID]
	if instanceID == "" {
		m.mu.RUnlock()
		return RuntimeInstanceSnapshot{}, nil, fmt.Errorf("%w: %s", ErrRuntimeInstanceNotFound, extensionID)
	}
	identity := RuntimeInstanceIdentity{ExtensionID: extensionID, InstanceID: instanceID}
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		m.mu.RUnlock()
		return RuntimeInstanceSnapshot{}, nil, err
	}
	if instance.transitioning {
		m.mu.RUnlock()
		return RuntimeInstanceSnapshot{}, nil, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceBusy, identity.ExtensionID, identity.InstanceID)
	}
	lease, err := instance.gate.Acquire(ctx, class)
	if err != nil {
		m.mu.RUnlock()
		return RuntimeInstanceSnapshot{}, nil, err
	}
	snapshot := m.runtimeInstanceSnapshotLocked(identity, instance)
	m.mu.RUnlock()
	return snapshot, lease, nil
}

// AcquireRuntimeCall 只允许普通调用进入活动实例；cleanup 可显式进入保留的 draining 实例。
func (m *InstanceAdmission) AcquireRuntimeCall(ctx context.Context, identity RuntimeInstanceIdentity, class RuntimeCallClass) (*RuntimeAdmissionLease, error) {
	if ctx == nil || strings.TrimSpace(string(class)) == "" {
		return nil, ErrRuntimeAdmissionInvalid
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		m.mu.RUnlock()
		return nil, err
	}
	if class != RuntimeCallLifecycleCleanup && m.activeInstances[identity.ExtensionID] != identity.InstanceID {
		m.mu.RUnlock()
		return nil, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceNotActive, identity.ExtensionID, identity.InstanceID)
	}
	if instance.transitioning {
		m.mu.RUnlock()
		return nil, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceBusy, identity.ExtensionID, identity.InstanceID)
	}
	// 保持 Manager 读锁直至 gate 完成 admission，使 Start 的 drain+switch 具有单一线性化边界。
	lease, err := instance.gate.Acquire(ctx, class)
	m.mu.RUnlock()
	return lease, err
}

func (m *InstanceAdmission) BeginDrain(identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	return m.BeginDrainContext(context.Background(), identity)
}

// BeginDrainContext preserves the legacy BeginDrain surface while allowing
// multi-step transactions to bound runtime-set barrier contention.
func (m *InstanceAdmission) BeginDrainContext(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	defer unlock()
	return m.beginDrainRuntimeSetLocked(ctx, identity)
}

// QuarantineRuntimeInstance deliberately bypasses runtime-set and lifecycle
// transition locks. Its only lock order is Manager -> exact gate, so an
// incident can close admission promptly even while publication is blocked.
func (m *InstanceAdmission) QuarantineRuntimeInstance(
	exact RuntimeInstanceArtifactIdentity,
	cause error,
) (RuntimeAdmissionSnapshot, error) {
	if m == nil {
		return RuntimeAdmissionSnapshot{}, ErrRuntimeAdmissionInvalid
	}
	identity, err := normalizeRuntimeInstanceIdentity(exact.RuntimeInstanceIdentity)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	version := strings.TrimSpace(exact.ExtensionVersion)
	digest := strings.TrimSpace(exact.ArtifactDigest)
	if version == "" || digest == "" {
		return RuntimeAdmissionSnapshot{}, ErrRuntimeAdmissionInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	if instance.extensionVersion != version || instance.artifactDigest != digest {
		return instance.gate.Snapshot(), fmt.Errorf("%w: %s/%s artifact drifted", ErrRuntimeInstanceConflict, identity.ExtensionID, identity.InstanceID)
	}
	return instance.gate.Quarantine(cause), nil
}

// QuarantineRuntimeArtifact closes every retained process for one exact
// package. Trust is artifact-scoped, so a staged settings replacement and its
// source must not be able to reopen each other after revocation.
func (m *InstanceAdmission) QuarantineRuntimeArtifact(
	exact RuntimeInstanceArtifactIdentity,
	cause error,
) ([]RuntimeAdmissionSnapshot, error) {
	if m == nil {
		return nil, ErrRuntimeAdmissionInvalid
	}
	identity, err := normalizeRuntimeInstanceIdentity(exact.RuntimeInstanceIdentity)
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(exact.ExtensionVersion)
	digest := strings.TrimSpace(exact.ArtifactDigest)
	if version == "" || digest == "" {
		return nil, ErrRuntimeAdmissionInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	anchor, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		return nil, err
	}
	if anchor.extensionVersion != version || anchor.artifactDigest != digest {
		return nil, fmt.Errorf("%w: %s/%s artifact drifted", ErrRuntimeInstanceConflict, identity.ExtensionID, identity.InstanceID)
	}
	instances := m.runtimeInstances[identity.ExtensionID]
	snapshots := make([]RuntimeAdmissionSnapshot, 0, len(instances))
	for _, instance := range instances {
		if instance.extensionVersion == version && instance.artifactDigest == digest {
			snapshots = append(snapshots, instance.gate.Quarantine(cause))
		}
	}
	return snapshots, nil
}

func (m *managerCore) beginDrainRuntimeSetLocked(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	unlock, err := m.lockRuntimeLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	defer unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	return instance.gate.BeginDrain(), nil
}

// ResumeRuntimeInstance 只重开仍为活动指针的 exact instance，候选发布失败时可恢复旧版本。
func (m *InstanceAdmission) ResumeRuntimeInstance(identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	return m.ResumeRuntimeInstanceContext(context.Background(), identity)
}

// ResumeRuntimeInstanceContext bounds compensation waits on the Manager-wide
// transition barrier. It retains the exact-active-only resume fence.
func (m *InstanceAdmission) ResumeRuntimeInstanceContext(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	defer unlock()
	return m.resumeRuntimeInstanceRuntimeSetLocked(ctx, identity)
}

func (m *managerCore) resumeRuntimeInstanceRuntimeSetLocked(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	unlock, err := m.lockRuntimeLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	defer unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	if m.activeInstances[identity.ExtensionID] != identity.InstanceID {
		return instance.gate.Snapshot(), fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceNotActive, identity.ExtensionID, identity.InstanceID)
	}
	return instance.gate.Resume()
}

func (m *InstanceAdmission) WaitDrain(ctx context.Context, identity RuntimeInstanceIdentity) error {
	if ctx == nil {
		return ErrRuntimeAdmissionInvalid
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return err
	}
	m.mu.RLock()
	instance, err := m.runtimeInstanceLocked(identity)
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	return instance.gate.Wait(ctx)
}

func (m *InstanceAdmission) ForceDrain(identity RuntimeInstanceIdentity, cause error) (RuntimeAdmissionSnapshot, error) {
	unlock, err := m.lockRuntimeSetTransition(context.Background())
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	defer unlock()
	return m.forceDrainRuntimeSetLocked(identity, cause)
}

func (m *managerCore) forceDrainRuntimeSetLocked(identity RuntimeInstanceIdentity, cause error) (RuntimeAdmissionSnapshot, error) {
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	unlock := m.lockRuntimeLifecycle(identity.ExtensionID)
	defer unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		return RuntimeAdmissionSnapshot{}, err
	}
	return instance.gate.ForceCancel(cause), nil
}

func (m *InstanceAdmission) InspectRuntimeInstance(identity RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error) {
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	m.mu.RLock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		m.mu.RUnlock()
		return RuntimeInstanceSnapshot{}, err
	}
	snapshot := m.runtimeInstanceSnapshotLocked(identity, instance)
	extension := instance.extension
	m.mu.RUnlock()
	if err := ensureManagedProtocolRuntimePresent(m.starter, identity, extension); err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	return snapshot, nil
}

// RuntimeInstanceAvailable is the read-side visibility predicate used by
// in-process registries. It never opens admission: execution must still acquire
// a lease, but a staged or drained target is hidden before the durable marker.
func (m *InstanceAdmission) RuntimeInstanceAvailable(identity RuntimeInstanceIdentity) bool {
	if m == nil {
		return false
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return false
	}
	m.mu.RLock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil || instance.transitioning || m.activeInstances[identity.ExtensionID] != identity.InstanceID {
		m.mu.RUnlock()
		return false
	}
	extension := instance.extension
	available := !instance.gate.Snapshot().Draining
	m.mu.RUnlock()
	return available && ensureManagedProtocolRuntimePresent(m.starter, identity, extension) == nil
}

func (m *InstanceAdmission) ActiveRuntimeInstance(extensionID string) (RuntimeInstanceSnapshot, error) {
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return RuntimeInstanceSnapshot{}, ErrRuntimeAdmissionInvalid
	}
	m.mu.RLock()
	instanceID := m.activeInstances[extensionID]
	if instanceID == "" {
		m.mu.RUnlock()
		return RuntimeInstanceSnapshot{}, fmt.Errorf("%w: %s", ErrRuntimeInstanceNotFound, extensionID)
	}
	identity := RuntimeInstanceIdentity{ExtensionID: extensionID, InstanceID: instanceID}
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		m.mu.RUnlock()
		return RuntimeInstanceSnapshot{}, err
	}
	snapshot := m.runtimeInstanceSnapshotLocked(identity, instance)
	extension := instance.extension
	m.mu.RUnlock()
	if err := ensureManagedProtocolRuntimePresent(m.starter, identity, extension); err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	return snapshot, nil
}

func ensureManagedProtocolRuntimePresent(starter Starter, identity RuntimeInstanceIdentity, extension extensions.Extension) error {
	if extension.Manifest.Backend.ProtocolVersion != 2 {
		return nil
	}
	staged, ok := starter.(StagedRuntimeStarter)
	if !ok {
		return nil
	}
	_, err := staged.InspectInstance(identity)
	return err
}

// RemoveRuntimeInstance 只删除已停用且完全 idle 的精确实例；不会回退到当前活动实例。
func (m *InstanceAdmission) RemoveRuntimeInstance(identity RuntimeInstanceIdentity) error {
	unlock, err := m.lockRuntimeSetTransition(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	return m.removeRuntimeInstanceRuntimeSetLocked(identity)
}

func (m *managerCore) removeRuntimeInstanceRuntimeSetLocked(identity RuntimeInstanceIdentity) error {
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return err
	}
	unlock := m.lockRuntimeLifecycle(identity.ExtensionID)
	defer unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		return err
	}
	if m.activeInstances[identity.ExtensionID] == identity.InstanceID {
		return fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceActive, identity.ExtensionID, identity.InstanceID)
	}
	if snapshot := instance.gate.Snapshot(); snapshot.ActiveTotal != 0 {
		return fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceBusy, identity.ExtensionID, identity.InstanceID)
	}
	delete(m.runtimeInstances[identity.ExtensionID], identity.InstanceID)
	if len(m.runtimeInstances[identity.ExtensionID]) == 0 {
		delete(m.runtimeInstances, identity.ExtensionID)
	}
	return nil
}

// activateRuntimeInstanceLocked 在同一 Manager 临界区内关闭旧入口并发布新活动指针。
// Caller holds m.mu.
func (m *managerCore) activateRuntimeInstanceLocked(extension extensions.Extension, target RouteTarget) (RouteTarget, error) {
	extensionID := strings.TrimSpace(extension.ID)
	if extensionID == "" {
		return RouteTarget{}, ErrRuntimeAdmissionInvalid
	}
	instanceID := strings.TrimSpace(target.InstanceID)
	if instanceID == "" {
		instanceID = m.newLegacyRuntimeInstanceIDLocked(extensionID)
	}
	identity := RuntimeInstanceIdentity{ExtensionID: extensionID, InstanceID: instanceID}
	gate, err := NewRuntimeAdmissionGate(identity)
	if err != nil {
		return RouteTarget{}, err
	}
	instances := m.runtimeInstances[extensionID]
	if instances == nil {
		instances = make(map[string]*managedRuntimeInstance)
		m.runtimeInstances[extensionID] = instances
	}
	if _, exists := instances[instanceID]; exists {
		return RouteTarget{}, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceConflict, extensionID, instanceID)
	}
	if activeID := m.activeInstances[extensionID]; activeID != "" {
		if previous := instances[activeID]; previous != nil {
			previous.gate.BeginDrain()
		}
	}
	target.InstanceID = instanceID
	version := extension.Version
	if version == "" {
		version = extension.Manifest.Version
	}
	instances[instanceID] = &managedRuntimeInstance{
		extension:        extension,
		extensionVersion: version,
		artifactDigest:   extension.PackageDigest,
		target:           target,
		gate:             gate,
	}
	m.activeInstances[extensionID] = instanceID
	return target, nil
}

// deactivateRuntimeInstanceLocked 只停用捕获到的实例，陈旧调用不能清除替换实例。
// Caller holds m.mu.
func (m *managerCore) deactivateRuntimeInstanceLocked(identity RuntimeInstanceIdentity) bool {
	if identity.ExtensionID == "" || identity.InstanceID == "" {
		return false
	}
	instances := m.runtimeInstances[identity.ExtensionID]
	instance := instances[identity.InstanceID]
	if instance == nil {
		return false
	}
	instance.gate.BeginDrain()
	if m.activeInstances[identity.ExtensionID] != identity.InstanceID {
		return false
	}
	delete(m.activeInstances, identity.ExtensionID)
	return true
}

func (m *managerCore) runtimeInstanceLocked(identity RuntimeInstanceIdentity) (*managedRuntimeInstance, error) {
	instance := m.runtimeInstances[identity.ExtensionID][identity.InstanceID]
	if instance == nil {
		return nil, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceNotFound, identity.ExtensionID, identity.InstanceID)
	}
	return instance, nil
}

func (m *managerCore) runtimeInstanceSnapshotLocked(identity RuntimeInstanceIdentity, instance *managedRuntimeInstance) RuntimeInstanceSnapshot {
	return RuntimeInstanceSnapshot{
		Identity:         identity,
		ExtensionVersion: instance.extensionVersion,
		ArtifactDigest:   instance.artifactDigest,
		VersionID:        instance.extension.ActiveVersionID,
		Target:           instance.target,
		Active:           m.activeInstances[identity.ExtensionID] == identity.InstanceID,
		Admission:        instance.gate.Snapshot(),
	}
}

func (m *managerCore) newLegacyRuntimeInstanceIDLocked(extensionID string) string {
	for {
		instanceID := "legacy-" + appevents.NewID()
		if m.runtimeInstances[extensionID][instanceID] == nil {
			return instanceID
		}
	}
}

func (m *managerCore) recordRuntimeStartFailure(
	extension extensions.Extension,
	previousInstanceID string,
	previousStatus extensions.RuntimeStatus,
	hadPreviousStatus bool,
	startErr error,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if previousInstanceID != "" && m.activeInstances[extension.ID] == previousInstanceID && hadPreviousStatus {
		previousStatus.LastError = startErr.Error()
		m.statuses[extension.ID] = previousStatus
		return
	}
	m.statuses[extension.ID] = extensions.RuntimeStatus{
		State:         extensions.RuntimeFailed,
		LastError:     startErr.Error(),
		RouteCount:    len(extension.Manifest.Routes),
		HookCount:     len(extension.Manifest.Hooks),
		EventCount:    len(extension.Manifest.Events),
		ProviderCount: len(extension.Manifest.Providers),
	}
}

func (m *managerCore) lockRuntimeLifecycle(extensionID string) func() {
	unlock, _ := m.lockRuntimeLifecycleContext(context.Background(), extensionID)
	return unlock
}

func (m *managerCore) lockRuntimeLifecycleContext(ctx context.Context, extensionID string) (func(), error) {
	if m == nil || ctx == nil {
		return nil, ErrRuntimeAdmissionInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.runtimeLifecycleMu.Lock()
	lock := m.runtimeLifecycle[extensionID]
	if lock == nil {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		m.runtimeLifecycle[extensionID] = lock
	}
	m.runtimeLifecycleMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-lock:
		if err := ctx.Err(); err != nil {
			lock <- struct{}{}
			return nil, err
		}
		return func() { lock <- struct{}{} }, nil
	}
}

func (m *managerCore) lockRuntimeSetTransition(ctx context.Context) (func(), error) {
	if m == nil || ctx == nil || m.runtimeSetTransition == nil {
		return nil, ErrRuntimeAdmissionInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.runtimeSetTransition:
		if err := ctx.Err(); err != nil {
			m.runtimeSetTransition <- struct{}{}
			return nil, err
		}
		return func() { m.runtimeSetTransition <- struct{}{} }, nil
	}
}

func normalizeRuntimeInstanceIdentity(identity RuntimeInstanceIdentity) (RuntimeInstanceIdentity, error) {
	identity.ExtensionID = strings.TrimSpace(identity.ExtensionID)
	identity.InstanceID = strings.TrimSpace(identity.InstanceID)
	if identity.ExtensionID == "" || identity.InstanceID == "" {
		return RuntimeInstanceIdentity{}, ErrRuntimeAdmissionInvalid
	}
	return identity, nil
}

func runtimeInstanceMatchesExtension(instance RuntimeInstanceSnapshot, extension extensions.Extension) bool {
	version := extension.Version
	if version == "" {
		version = extension.Manifest.Version
	}
	return instance.Identity.ExtensionID == extension.ID &&
		instance.ExtensionVersion == version &&
		instance.ArtifactDigest == extension.PackageDigest &&
		instance.VersionID == extension.ActiveVersionID
}

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) AcquireActiveRuntimeCall(ctx context.Context, extensionID string, class RuntimeCallClass) (RuntimeInstanceSnapshot, *RuntimeAdmissionLease, error) {
	return m.admission.AcquireActiveRuntimeCall(ctx, extensionID, class)
}

func (m *Manager) AcquireRuntimeCall(ctx context.Context, identity RuntimeInstanceIdentity, class RuntimeCallClass) (*RuntimeAdmissionLease, error) {
	return m.admission.AcquireRuntimeCall(ctx, identity, class)
}

func (m *Manager) BeginDrain(identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	return m.admission.BeginDrain(identity)
}

func (m *Manager) BeginDrainContext(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	return m.admission.BeginDrainContext(ctx, identity)
}

func (m *Manager) QuarantineRuntimeInstance(
	exact RuntimeInstanceArtifactIdentity,
	cause error,
) (RuntimeAdmissionSnapshot, error) {
	return m.admission.QuarantineRuntimeInstance(exact, cause)
}

func (m *Manager) QuarantineRuntimeArtifact(
	exact RuntimeInstanceArtifactIdentity,
	cause error,
) ([]RuntimeAdmissionSnapshot, error) {
	return m.admission.QuarantineRuntimeArtifact(exact, cause)
}

func (m *Manager) ResumeRuntimeInstance(identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	return m.admission.ResumeRuntimeInstance(identity)
}

func (m *Manager) ResumeRuntimeInstanceContext(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeAdmissionSnapshot, error) {
	return m.admission.ResumeRuntimeInstanceContext(ctx, identity)
}

func (m *Manager) WaitDrain(ctx context.Context, identity RuntimeInstanceIdentity) error {
	return m.admission.WaitDrain(ctx, identity)
}

func (m *Manager) ForceDrain(identity RuntimeInstanceIdentity, cause error) (RuntimeAdmissionSnapshot, error) {
	return m.admission.ForceDrain(identity, cause)
}

func (m *Manager) InspectRuntimeInstance(identity RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error) {
	return m.admission.InspectRuntimeInstance(identity)
}

func (m *Manager) RuntimeInstanceAvailable(identity RuntimeInstanceIdentity) bool {
	return m.admission.RuntimeInstanceAvailable(identity)
}

func (m *Manager) ActiveRuntimeInstance(extensionID string) (RuntimeInstanceSnapshot, error) {
	return m.admission.ActiveRuntimeInstance(extensionID)
}

func (m *Manager) RemoveRuntimeInstance(identity RuntimeInstanceIdentity) error {
	return m.admission.RemoveRuntimeInstance(identity)
}
