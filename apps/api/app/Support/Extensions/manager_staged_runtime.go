package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// StageRuntimeInstance starts one inert Protocol V2 process and retains its
// Manager admission gate without changing the active runtime pointer.
func (m *InstanceAdmission) StageRuntimeInstance(ctx context.Context, extension extensions.Extension) (RuntimeInstanceSnapshot, error) {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	defer unlock()
	return m.stageRuntimeInstanceRuntimeSetLocked(ctx, extension)
}

func (m *managerCore) stageRuntimeInstanceRuntimeSetLocked(ctx context.Context, extension extensions.Extension) (RuntimeInstanceSnapshot, error) {
	if m == nil || ctx == nil {
		return RuntimeInstanceSnapshot{}, ErrRuntimeAdmissionInvalid
	}
	if err := ctx.Err(); err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	frozen, err := cloneManagedRuntimeExtension(extension)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	if err := validateManagedStagedExtension(frozen); err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	starter, ok := m.starter.(StagedRuntimeStarter)
	if !ok {
		return RuntimeInstanceSnapshot{}, ErrProtocolInstanceUnsupported
	}

	unlock, err := m.lockRuntimeLifecycleContext(ctx, frozen.ID)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	defer unlock()
	if err := m.prepareRuntimeStart(ctx, frozen); err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	target, err := starter.StartInstance(ctx, frozen)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	identity, err := normalizeRuntimeInstanceIdentity(RuntimeInstanceIdentity{
		ExtensionID: frozen.ID,
		InstanceID:  target.InstanceID,
	})
	if err != nil {
		return RuntimeInstanceSnapshot{}, discardStagedRuntimeAfterFailure(starter, identity, err)
	}
	protocolSnapshot, err := starter.InspectInstance(identity)
	if err != nil {
		return RuntimeInstanceSnapshot{}, discardStagedRuntimeAfterFailure(starter, identity, err)
	}
	if err := validateManagedProtocolSnapshot(protocolSnapshot, frozen, identity, ProtocolRuntimeStaged); err != nil {
		return RuntimeInstanceSnapshot{}, discardStagedRuntimeAfterFailure(starter, identity, err)
	}

	gate, err := NewRuntimeAdmissionGate(identity)
	if err != nil {
		return RuntimeInstanceSnapshot{}, discardStagedRuntimeAfterFailure(starter, identity, err)
	}
	m.mu.Lock()
	instances := m.runtimeInstances[frozen.ID]
	if instances == nil {
		instances = make(map[string]*managedRuntimeInstance)
		m.runtimeInstances[frozen.ID] = instances
	}
	if instances[identity.InstanceID] != nil {
		m.mu.Unlock()
		return RuntimeInstanceSnapshot{}, discardStagedRuntimeAfterFailure(
			starter, identity, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceConflict, identity.ExtensionID, identity.InstanceID),
		)
	}
	target.InstanceID = identity.InstanceID
	instance := &managedRuntimeInstance{
		extension: frozen, extensionVersion: frozen.Version, artifactDigest: frozen.PackageDigest,
		target: target, gate: gate,
	}
	instances[identity.InstanceID] = instance
	if m.activeInstances[frozen.ID] == "" {
		m.statuses[frozen.ID] = managedRuntimeStatus(frozen, extensions.RuntimeStarting, nil)
	}
	snapshot := m.runtimeInstanceSnapshotLocked(identity, instance)
	m.mu.Unlock()
	return snapshot, nil
}

// HealthRuntimeInstance performs health and readiness against one exact
// staged/published/retained process and verifies the frozen artifact again.
func (m *InstanceAdmission) HealthRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error) {
	if m == nil || ctx == nil {
		return ProtocolRuntimeInstanceSnapshot{}, ErrRuntimeAdmissionInvalid
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	starter, ok := m.starter.(StagedRuntimeStarter)
	if !ok {
		return ProtocolRuntimeInstanceSnapshot{}, ErrProtocolInstanceUnsupported
	}
	unlock, err := m.lockRuntimeLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	defer unlock()
	extension, err := m.managedRuntimeExtension(identity)
	if err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	if _, err := starter.HealthInstance(ctx, identity); err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	snapshot, err := starter.InspectInstance(identity)
	if err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	if err := validateManagedProtocolSnapshot(snapshot, extension, identity, ""); err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	if !snapshot.Healthy || !snapshot.Ready || !snapshot.ReadinessChecked {
		return ProtocolRuntimeInstanceSnapshot{}, ErrProtocolInstanceNotReady
	}
	return snapshot, nil
}

// PublishRuntimeInstance switches ProtocolStarter first while the old Manager
// gate is closed, then publishes the same exact identity to Manager callers.
// The tiny cross-registry window is fail-closed because service/job admission
// still resolves the drained old Manager pointer until this method commits.
func (m *InstanceAdmission) PublishRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error) {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	defer unlock()
	return m.publishRuntimeInstanceRuntimeSetLocked(ctx, identity, false)
}

// PublishRuntimeInstanceFrom atomically proves the exact drained source is
// still authoritative before a staged replacement can become active.
func (m *InstanceAdmission) PublishRuntimeInstanceFrom(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	source RuntimeInstanceArtifactIdentity,
) (RuntimeInstanceSnapshot, error) {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	defer unlock()
	return m.publishRuntimeInstanceRuntimeSetLockedWithExpectation(ctx, identity, false, runtimePublicationExpectation{
		source: &source,
	})
}

// PublishRuntimeInstanceIfNoActive restores a retained runtime only while no
// concurrent replacement owns the active pointer.
func (m *InstanceAdmission) PublishRuntimeInstanceIfNoActive(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
) (RuntimeInstanceSnapshot, error) {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	defer unlock()
	return m.publishRuntimeInstanceRuntimeSetLockedWithExpectation(ctx, identity, false, runtimePublicationExpectation{
		requireNoActive: true,
	})
}

// PublishDrainedRuntimeInstance selects the exact process in ProtocolStarter
// and Manager while keeping its ordinary admission gate closed. Composed
// lifecycle publication opens it only after durable state and every registry
// snapshot have committed.
func (m *InstanceAdmission) PublishDrainedRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error) {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	defer unlock()
	return m.publishRuntimeInstanceRuntimeSetLocked(ctx, identity, true)
}

func (m *managerCore) publishRuntimeInstanceRuntimeSetLocked(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	keepDraining bool,
) (RuntimeInstanceSnapshot, error) {
	return m.publishRuntimeInstanceRuntimeSetLockedWithExpectation(ctx, identity, keepDraining, runtimePublicationExpectation{})
}

type runtimePublicationExpectation struct {
	source          *RuntimeInstanceArtifactIdentity
	requireNoActive bool
}

func (m *managerCore) publishRuntimeInstanceRuntimeSetLockedWithExpectation(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	keepDraining bool,
	expectation runtimePublicationExpectation,
) (RuntimeInstanceSnapshot, error) {
	if m == nil || ctx == nil {
		return RuntimeInstanceSnapshot{}, ErrRuntimeAdmissionInvalid
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	starter, ok := m.starter.(StagedRuntimeStarter)
	if !ok {
		return RuntimeInstanceSnapshot{}, ErrProtocolInstanceUnsupported
	}
	unlock, err := m.lockRuntimeLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return RuntimeInstanceSnapshot{}, err
	}
	defer unlock()

	m.mu.Lock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		m.mu.Unlock()
		return RuntimeInstanceSnapshot{}, err
	}
	if instance.transitioning {
		m.mu.Unlock()
		return RuntimeInstanceSnapshot{}, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceBusy, identity.ExtensionID, identity.InstanceID)
	}
	activeID := m.activeInstances[identity.ExtensionID]
	if err := m.validateRuntimePublicationExpectationLocked(identity.ExtensionID, activeID, expectation, false); err != nil {
		m.mu.Unlock()
		return RuntimeInstanceSnapshot{}, err
	}
	var activeInstance *managedRuntimeInstance
	if activeID == identity.InstanceID {
		admission := instance.gate.Snapshot()
		if keepDraining && !admission.Draining {
			admission = instance.gate.BeginDrain()
		}
		if keepDraining && admission.ActiveTotal != 0 {
			m.mu.Unlock()
			return RuntimeInstanceSnapshot{}, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceBusy, identity.ExtensionID, identity.InstanceID)
		}
		if keepDraining {
			instance.transitioning = true
		}
		extension := instance.extension
		m.mu.Unlock()
		protocolSnapshot, err := starter.PublishInstance(ctx, identity)
		if err != nil {
			if keepDraining {
				m.mu.Lock()
				instance.transitioning = false
				m.mu.Unlock()
			}
			return RuntimeInstanceSnapshot{}, err
		}
		if err := validateManagedProtocolSnapshot(protocolSnapshot, extension, identity, ProtocolRuntimePublished); err != nil {
			if keepDraining {
				m.mu.Lock()
				instance.transitioning = false
				m.mu.Unlock()
			}
			return RuntimeInstanceSnapshot{}, err
		}
		m.mu.Lock()
		if keepDraining {
			instance.transitioning = false
		}
		snapshot := m.runtimeInstanceSnapshotLocked(identity, instance)
		m.mu.Unlock()
		return snapshot, nil
	}
	if activeID != "" {
		activeInstance = m.runtimeInstances[identity.ExtensionID][activeID]
		if activeInstance == nil {
			m.mu.Unlock()
			return RuntimeInstanceSnapshot{}, fmt.Errorf("%w: active runtime pointer is missing", ErrRuntimeAdmissionInvalid)
		}
		activeAdmission := activeInstance.gate.Snapshot()
		if !activeAdmission.Draining || activeAdmission.ActiveTotal != 0 {
			m.mu.Unlock()
			return RuntimeInstanceSnapshot{}, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceNotDrained, identity.ExtensionID, activeID)
		}
	}
	candidateAdmission := instance.gate.Snapshot()
	candidateWasDraining := candidateAdmission.Draining
	if candidateAdmission.ActiveTotal != 0 || candidateAdmission.Quarantined || candidateAdmission.Forced {
		m.mu.Unlock()
		return RuntimeInstanceSnapshot{}, fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceBusy, identity.ExtensionID, identity.InstanceID)
	}
	if keepDraining && !candidateAdmission.Draining {
		instance.gate.BeginDrain()
	} else if !keepDraining && candidateAdmission.Draining {
		if _, err := instance.gate.Resume(); err != nil {
			m.mu.Unlock()
			return RuntimeInstanceSnapshot{}, err
		}
	}
	instance.transitioning = true
	if activeInstance != nil {
		activeInstance.transitioning = true
	}
	extension := instance.extension
	m.mu.Unlock()

	previousHooks, hadPreviousHooks := m.hooks.RuntimeSnapshot(identity.ExtensionID)
	publishedVersionedHooks := publishesVersionedHookSnapshot(extension, identity.InstanceID)
	if publishedVersionedHooks {
		if err := m.hooks.RegisterRuntime(extension, identity.InstanceID); err != nil {
			m.resetRuntimePublicationTransition(identity, instance, activeID, activeInstance, candidateWasDraining, keepDraining)
			return RuntimeInstanceSnapshot{}, err
		}
	}
	protocolSnapshot, publishErr := starter.PublishInstance(ctx, identity)
	if publishErr == nil {
		publishErr = validateManagedProtocolSnapshot(protocolSnapshot, extension, identity, ProtocolRuntimePublished)
	}
	if publishErr != nil {
		var hookErr error
		if publishedVersionedHooks {
			hookErr = m.hooks.restoreRuntime(identity.ExtensionID, identity.InstanceID, previousHooks, hadPreviousHooks)
		}
		m.resetRuntimePublicationTransition(identity, instance, activeID, activeInstance, candidateWasDraining, keepDraining)
		return RuntimeInstanceSnapshot{}, errors.Join(publishErr, hookErr)
	}

	now := time.Now().UTC()
	m.mu.Lock()
	current, currentErr := m.runtimeInstanceLocked(identity)
	expectationErr := m.validateRuntimePublicationExpectationLocked(identity.ExtensionID, activeID, expectation, true)
	currentAdmissionInvalid := currentErr == nil && (current.gate.Snapshot().Quarantined || current.gate.Snapshot().Forced)
	if currentErr != nil || current != instance || m.activeInstances[identity.ExtensionID] != activeID ||
		expectationErr != nil || currentAdmissionInvalid {
		if current == instance {
			current.transitioning = false
		}
		if activeInstance != nil {
			activeInstance.transitioning = false
		}
		m.mu.Unlock()
		var rollbackErr error
		if publishedVersionedHooks {
			rollbackErr = m.hooks.restoreRuntime(identity.ExtensionID, identity.InstanceID, previousHooks, hadPreviousHooks)
		}
		if activeID != "" {
			compensationCtx, cancel := lifecycleBoundaryCompensationContext(ctx)
			_, protocolErr := starter.PublishInstance(compensationCtx, RuntimeInstanceIdentity{ExtensionID: identity.ExtensionID, InstanceID: activeID})
			cancel()
			var quarantineErr error
			if protocolErr != nil && activeInstance != nil {
				_, quarantineErr = m.host.QuarantineRuntimeInstance(RuntimeInstanceArtifactIdentity{
					RuntimeInstanceIdentity: RuntimeInstanceIdentity{ExtensionID: identity.ExtensionID, InstanceID: activeID},
					ExtensionVersion:        activeInstance.extensionVersion,
					ArtifactDigest:          activeInstance.artifactDigest,
				}, errors.Join(ErrRuntimeInstanceConflict, fmt.Errorf("restore protocol source: %w", protocolErr)))
			}
			rollbackErr = errors.Join(rollbackErr, protocolErr, quarantineErr)
		}
		if currentErr != nil || expectationErr != nil {
			return RuntimeInstanceSnapshot{}, errors.Join(currentErr, expectationErr, rollbackErr)
		}
		return RuntimeInstanceSnapshot{}, errors.Join(
			fmt.Errorf("%w: runtime publication changed concurrently", ErrRuntimeInstanceConflict), rollbackErr,
		)
	}
	instance.transitioning = false
	if activeInstance != nil {
		activeInstance.transitioning = false
	}
	m.activeInstances[identity.ExtensionID] = identity.InstanceID
	m.targets[identity.ExtensionID] = instance.target
	m.running[identity.ExtensionID] = extension
	m.statuses[identity.ExtensionID] = managedRuntimeStatus(extension, extensions.RuntimeRunning, &now)
	snapshot := m.runtimeInstanceSnapshotLocked(identity, instance)
	m.mu.Unlock()
	if !publishedVersionedHooks {
		if err := m.hooks.RegisterRuntime(extension, identity.InstanceID); err != nil {
			return RuntimeInstanceSnapshot{}, err
		}
	}
	return snapshot, nil
}

func (m *managerCore) validateRuntimePublicationExpectationLocked(
	extensionID string,
	activeID string,
	expectation runtimePublicationExpectation,
	allowTransitioning bool,
) error {
	if expectation.requireNoActive {
		if expectation.source != nil || activeID != "" {
			return fmt.Errorf("%w: active runtime changed before publication", ErrRuntimeInstanceConflict)
		}
		return nil
	}
	if expectation.source == nil {
		return nil
	}
	source := *expectation.source
	identity, err := normalizeRuntimeInstanceIdentity(source.RuntimeInstanceIdentity)
	if err != nil || identity.ExtensionID != extensionID || activeID != identity.InstanceID ||
		strings.TrimSpace(source.ExtensionVersion) == "" || strings.TrimSpace(source.ArtifactDigest) == "" {
		return fmt.Errorf("%w: expected source runtime changed", ErrRuntimeInstanceConflict)
	}
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil || instance.extensionVersion != source.ExtensionVersion || instance.artifactDigest != source.ArtifactDigest {
		return fmt.Errorf("%w: expected source artifact changed", ErrRuntimeInstanceConflict)
	}
	admission := instance.gate.Snapshot()
	if (!allowTransitioning && instance.transitioning) || !admission.Draining || admission.ActiveTotal != 0 ||
		admission.Quarantined || admission.Forced {
		return fmt.Errorf("%w: expected source runtime is not safely drained", ErrRuntimeInstanceConflict)
	}
	return nil
}

func (m *managerCore) resetRuntimePublicationTransition(
	identity RuntimeInstanceIdentity,
	instance *managedRuntimeInstance,
	activeID string,
	activeInstance *managedRuntimeInstance,
	candidateWasDraining, keepDraining bool,
) {
	m.mu.Lock()
	if current := m.runtimeInstances[identity.ExtensionID][identity.InstanceID]; current == instance {
		current.transitioning = false
		if candidateWasDraining || keepDraining {
			current.gate.BeginDrain()
		}
	}
	if current := m.runtimeInstances[identity.ExtensionID][activeID]; current == activeInstance && current != nil {
		current.transitioning = false
	}
	m.mu.Unlock()
}

// StopRuntimeInstance stops one exact V2 process after its gate is drained and
// idle. Stopping a retained instance cannot clear the active replacement.
func (m *InstanceAdmission) StopRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return m.removeManagedProtocolRuntimeSetLocked(ctx, identity, false)
}

// DiscardRuntimeInstance destroys only an unpublished inactive candidate.
func (m *InstanceAdmission) DiscardRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	unlock, err := m.lockRuntimeSetTransition(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return m.removeManagedProtocolRuntimeSetLocked(ctx, identity, true)
}

func (m *managerCore) removeManagedProtocolRuntimeSetLocked(ctx context.Context, identity RuntimeInstanceIdentity, discard bool) error {
	if m == nil || ctx == nil {
		return ErrRuntimeAdmissionInvalid
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return err
	}
	starter, ok := m.starter.(StagedRuntimeStarter)
	if !ok {
		return ErrProtocolInstanceUnsupported
	}
	unlock, err := m.lockRuntimeLifecycleContext(ctx, identity.ExtensionID)
	if err != nil {
		return err
	}
	defer unlock()

	m.mu.Lock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	active := m.activeInstances[identity.ExtensionID] == identity.InstanceID
	admission := instance.gate.Snapshot()
	if instance.transitioning || admission.ActiveTotal != 0 {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceBusy, identity.ExtensionID, identity.InstanceID)
	}
	if discard && active {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceActive, identity.ExtensionID, identity.InstanceID)
	}
	if !discard && (!admission.Draining || admission.ActiveTotal != 0) {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceNotDrained, identity.ExtensionID, identity.InstanceID)
	}
	instance.transitioning = true
	extension := instance.extension
	m.mu.Unlock()

	if active {
		if err := m.hooks.validateUnregisterRuntime(extension, identity.InstanceID); err != nil {
			m.mu.Lock()
			if current := m.runtimeInstances[identity.ExtensionID][identity.InstanceID]; current == instance {
				current.transitioning = false
			}
			m.mu.Unlock()
			return err
		}
	}
	if discard {
		err = starter.DiscardInstance(ctx, identity)
	} else {
		err = starter.StopInstance(ctx, identity)
	}
	if err != nil && !errors.Is(err, ErrRuntimeInstanceNotFound) {
		m.mu.Lock()
		if current := m.runtimeInstances[identity.ExtensionID][identity.InstanceID]; current == instance {
			current.transitioning = false
		}
		m.mu.Unlock()
		return err
	}

	m.mu.Lock()
	if current := m.runtimeInstances[identity.ExtensionID][identity.InstanceID]; current == instance {
		delete(m.runtimeInstances[identity.ExtensionID], identity.InstanceID)
		if len(m.runtimeInstances[identity.ExtensionID]) == 0 {
			delete(m.runtimeInstances, identity.ExtensionID)
		}
		if active && m.activeInstances[identity.ExtensionID] == identity.InstanceID {
			delete(m.activeInstances, identity.ExtensionID)
			delete(m.targets, identity.ExtensionID)
			delete(m.running, identity.ExtensionID)
			m.statuses[identity.ExtensionID] = managedRuntimeStatus(extension, extensions.RuntimeStopped, nil)
		} else if m.activeInstances[identity.ExtensionID] == "" && len(m.runtimeInstances[identity.ExtensionID]) == 0 {
			m.statuses[identity.ExtensionID] = managedRuntimeStatus(extension, extensions.RuntimeStopped, nil)
		}
	}
	m.mu.Unlock()
	var hookErr error
	if active {
		_, hookErr = m.hooks.unregisterRuntime(identity.ExtensionID, identity.InstanceID)
		if m.resilience != nil {
			m.resilience.remove(identity.ExtensionID)
		}
	}
	return hookErr
}

func (m *managerCore) managedRuntimeExtension(identity RuntimeInstanceIdentity) (extensions.Extension, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	instance, err := m.runtimeInstanceLocked(identity)
	if err != nil {
		return extensions.Extension{}, err
	}
	return instance.extension, nil
}

func validateManagedProtocolSnapshot(
	snapshot ProtocolRuntimeInstanceSnapshot,
	extension extensions.Extension,
	identity RuntimeInstanceIdentity,
	wantState ProtocolRuntimeInstanceState,
) error {
	manifestDigest, err := protocolRuntimeManifestDigest(extension.Manifest)
	if err != nil {
		return fmt.Errorf("%w: encode frozen runtime manifest: %v", ErrRuntimeInstanceConflict, err)
	}
	if snapshot.Identity != identity || snapshot.Target.InstanceID != identity.InstanceID ||
		snapshot.ExtensionVersion != extension.Version || snapshot.ArtifactDigest != extension.PackageDigest ||
		snapshot.ManifestDigest != manifestDigest ||
		snapshot.ProtocolVersion != 2 || (wantState != "" && snapshot.State != wantState) {
		return fmt.Errorf("%w: protocol snapshot does not match exact artifact", ErrRuntimeInstanceConflict)
	}
	return nil
}

// validateManagedStagedExtension admits an exact Protocol V2 process artifact
// for staged runtime / full-set apply. Lifecycle V2 is optional here; lifecycle
// coordinators and protocol lifecycle invokers enforce declarations separately.
func validateManagedStagedExtension(extension extensions.Extension) error {
	if extension.ID == "" || extension.ID != strings.TrimSpace(extension.ID) ||
		extension.Version == "" || extension.Version != strings.TrimSpace(extension.Version) ||
		extension.PackageDigest == "" || extension.PackageDigest != strings.TrimSpace(extension.PackageDigest) ||
		extension.Type != extensions.TypePlugin || extension.Manifest.ID != extension.ID ||
		extension.Manifest.Version != extension.Version || extension.Manifest.Type != extensions.TypePlugin ||
		extension.Manifest.Backend.ProtocolVersion != 2 {
		return fmt.Errorf("%w: exact Protocol V2 process artifact is required", ErrRuntimeAdmissionInvalid)
	}
	return nil
}

func cloneManagedRuntimeExtension(extension extensions.Extension) (extensions.Extension, error) {
	document, err := json.Marshal(extension.Manifest)
	if err != nil {
		return extensions.Extension{}, fmt.Errorf("%w: encode runtime manifest: %v", ErrRuntimeAdmissionInvalid, err)
	}
	var manifest extensions.Manifest
	if err := json.Unmarshal(document, &manifest); err != nil {
		return extensions.Extension{}, fmt.Errorf("%w: clone runtime manifest: %v", ErrRuntimeAdmissionInvalid, err)
	}
	clone := extension
	clone.Manifest = manifest
	clone.CapabilityGrants = append([]extensions.CapabilityGrant(nil), extension.CapabilityGrants...)
	clone.StagedVersion = nil
	if extension.Runtime != nil {
		status := *extension.Runtime
		clone.Runtime = &status
	}
	return clone, nil
}

func managedRuntimeStatus(extension extensions.Extension, state string, startedAt *time.Time) extensions.RuntimeStatus {
	hookCount, eventCount := runtimeManifestCounts(extension.Manifest)
	return extensions.RuntimeStatus{
		State: state, StartedAt: startedAt,
		RouteCount: len(extension.Manifest.Routes), HookCount: hookCount,
		EventCount: eventCount, ProviderCount: len(extension.Manifest.Providers),
	}
}

func discardStagedRuntimeAfterFailure(starter StagedRuntimeStarter, identity RuntimeInstanceIdentity, cause error) error {
	if identity.ExtensionID == "" || identity.InstanceID == "" {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := starter.DiscardInstance(ctx, identity); err != nil && !errors.Is(err, ErrRuntimeInstanceNotFound) {
		return errors.Join(cause, fmt.Errorf("discard failed staged runtime: %w", err))
	}
	return cause
}

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) StageRuntimeInstance(ctx context.Context, extension extensions.Extension) (RuntimeInstanceSnapshot, error) {
	return m.admission.StageRuntimeInstance(ctx, extension)
}

func (m *Manager) HealthRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error) {
	return m.admission.HealthRuntimeInstance(ctx, identity)
}

func (m *Manager) PublishRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error) {
	return m.admission.PublishRuntimeInstance(ctx, identity)
}

func (m *Manager) PublishRuntimeInstanceFrom(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	source RuntimeInstanceArtifactIdentity,
) (RuntimeInstanceSnapshot, error) {
	return m.admission.PublishRuntimeInstanceFrom(ctx, identity, source)
}

func (m *Manager) PublishRuntimeInstanceIfNoActive(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
) (RuntimeInstanceSnapshot, error) {
	return m.admission.PublishRuntimeInstanceIfNoActive(ctx, identity)
}

func (m *Manager) PublishDrainedRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) (RuntimeInstanceSnapshot, error) {
	return m.admission.PublishDrainedRuntimeInstance(ctx, identity)
}

func (m *Manager) StopRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	return m.admission.StopRuntimeInstance(ctx, identity)
}

func (m *Manager) DiscardRuntimeInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	return m.admission.DiscardRuntimeInstance(ctx, identity)
}
