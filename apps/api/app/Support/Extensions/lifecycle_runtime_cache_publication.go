package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

type runtimeCachePublishMutation struct {
	mu       sync.Mutex
	registry *cacheregistry.Registry
	before   *cacheregistry.Publication
	after    cacheregistry.Publication
	changed  bool
	done     bool
}

func (m *runtimeCachePublishMutation) Rollback() error {
	if m == nil {
		return extensions.ErrRuntimeCachePublicationUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done || !m.changed {
		m.done = true
		return nil
	}
	if _, err := restoreRuntimeCachePublication(m.registry, &m.after.Artifact, m.before); err != nil {
		return err
	}
	m.done = true
	return nil
}

type runtimeCacheQuarantineMutation struct {
	mu             sync.Mutex
	registry       *cacheregistry.Registry
	manager        *Manager
	before         *cacheregistry.Publication
	resumeIdentity RuntimeInstanceIdentity
	resumeOwned    bool
	done           bool
}

func (m *runtimeCacheQuarantineMutation) Rollback() error {
	if m == nil {
		return extensions.ErrRuntimeCachePublicationUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return nil
	}
	restored := false
	if m.before != nil {
		var err error
		restored, err = restoreRuntimeCachePublication(m.registry, nil, m.before)
		if err != nil {
			return err
		}
	}
	if m.resumeOwned {
		if _, err := m.manager.ResumeRuntimeInstance(m.resumeIdentity); err != nil {
			if restored && m.before != nil {
				_, _, removeErr := m.registry.Remove(m.before.Artifact)
				return errors.Join(err, removeErr)
			}
			return err
		}
	}
	m.done = true
	return nil
}

// PublishRuntimeCaches production-binds a cache-bearing plugin that uses the
// legacy Service Enable path because it declares no Lifecycle V2 hook contract.
func (b *PostgresLifecycleBoundaryRegistries) PublishRuntimeCaches(
	ctx context.Context,
	extension extensions.Extension,
) (extensions.RuntimeCachePublicationMutation, error) {
	if b == nil || b.manager == nil || b.caches == nil || ctx == nil || len(extension.Manifest.Cache) == 0 {
		return nil, extensions.ErrRuntimeCachePublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime, err := b.manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || !runtimeCacheInstanceMatches(runtime, extension) ||
		!b.manager.RuntimeInstanceAvailable(runtime.Identity) {
		return nil, errors.Join(extensions.ErrRuntimeCachePublicationUnavailable, err)
	}
	desired, err := buildLifecycleCachePublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	})
	if err != nil || desired == nil {
		return nil, errors.Join(extensions.ErrRuntimeCachePublicationUnavailable, err)
	}

	mutation := &runtimeCachePublishMutation{registry: b.caches, after: *desired}
	current, found := b.caches.SnapshotPublication(extension.ID)
	if found {
		if !runtimeCacheArtifactCanMoveForward(current.Artifact, desired.Artifact) {
			return nil, fmt.Errorf("%w: newer cache artifact is already active", ErrLifecycleRegistryPublicationConflict)
		}
		previous := current
		mutation.before = &previous
		if current.Artifact == desired.Artifact {
			// A failed Service enable stops this exact runtime, so compensation
			// must remove even an idempotently pre-existing publication.
			mutation.before = nil
			mutation.changed = true
			if _, err := b.caches.Publish(*desired); err != nil {
				return mutation, fmt.Errorf("publish exact runtime caches: %w", err)
			}
		} else {
			if _, err := b.caches.PublishIfArtifact(current.Artifact, *desired); err != nil {
				return nil, fmt.Errorf("replace exact runtime caches: %w", err)
			}
			mutation.changed = true
		}
	} else {
		if _, err := b.caches.Publish(*desired); err != nil {
			return nil, fmt.Errorf("publish exact runtime caches: %w", err)
		}
		mutation.changed = true
	}
	if !b.runtimeCacheArtifactAvailable(desired.Artifact) {
		rollbackErr := mutation.Rollback()
		return nil, errors.Join(extensions.ErrRuntimeCachePublicationUnavailable, rollbackErr)
	}
	return mutation, nil
}

// QuarantineRuntimeCaches closes exact runtime admission and removes only the
// observed complete publication. Rollback restores publication before admission.
func (b *PostgresLifecycleBoundaryRegistries) QuarantineRuntimeCaches(
	ctx context.Context,
	extension extensions.Extension,
) (extensions.RuntimeCachePublicationMutation, error) {
	if b == nil || b.manager == nil || b.caches == nil || ctx == nil || len(extension.Manifest.Cache) == 0 {
		return nil, extensions.ErrRuntimeCachePublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mutation := &runtimeCacheQuarantineMutation{registry: b.caches, manager: b.manager}
	current, found := b.caches.SnapshotPublication(extension.ID)
	if found {
		if !runtimeCacheArtifactBelongsToExtension(current.Artifact, extension) {
			return nil, fmt.Errorf("%w: stale disable cannot remove active cache artifact", ErrLifecycleRegistryPublicationConflict)
		}
		previous := current
		mutation.before = &previous
	}

	runtime, runtimeErr := b.manager.ActiveRuntimeInstance(extension.ID)
	if runtimeErr == nil {
		if !runtimeCacheInstanceMatches(runtime, extension) {
			return nil, fmt.Errorf("%w: active runtime does not match disabled cache artifact", ErrLifecycleRegistryPublicationConflict)
		}
		if mutation.before != nil && !runtimeCacheArtifactMatchesInstance(mutation.before.Artifact, extension, runtime) {
			return nil, fmt.Errorf("%w: cache publication does not match the active runtime instance", ErrLifecycleRegistryPublicationConflict)
		}
		mutation.resumeIdentity = runtime.Identity
		if !runtime.Admission.Draining {
			if _, err := b.manager.BeginDrain(runtime.Identity); err != nil {
				return nil, fmt.Errorf("begin exact cache runtime drain: %w", err)
			}
			mutation.resumeOwned = true
		}
	} else if !errors.Is(runtimeErr, ErrRuntimeInstanceNotFound) {
		return nil, runtimeErr
	}

	if mutation.before != nil {
		_, removed, err := b.caches.Remove(mutation.before.Artifact)
		if err != nil || !removed {
			if mutation.resumeOwned {
				_, _ = b.manager.ResumeRuntimeInstance(mutation.resumeIdentity)
			}
			if err == nil {
				err = cacheregistry.ErrArtifactConflict
			}
			return nil, fmt.Errorf("remove exact runtime caches: %w", err)
		}
	}
	return mutation, nil
}

func (b *PostgresLifecycleBoundaryRegistries) runtimeCacheArtifactAvailable(artifact cacheregistry.Artifact) bool {
	if b == nil || b.manager == nil {
		return false
	}
	identity := RuntimeInstanceIdentity{ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID}
	runtime, err := b.manager.InspectRuntimeInstance(identity)
	return err == nil && runtime.ExtensionVersion == artifact.ExtensionVersion &&
		runtime.ArtifactDigest == artifact.PackageDigest && b.manager.RuntimeInstanceAvailable(identity)
}

func runtimeCacheInstanceMatches(runtime RuntimeInstanceSnapshot, extension extensions.Extension) bool {
	return runtime.Active && runtime.Identity.ExtensionID == extension.ID &&
		runtime.ExtensionVersion == extension.Version && runtime.ArtifactDigest == extension.PackageDigest
}

func runtimeCacheArtifactMatchesInstance(
	artifact cacheregistry.Artifact,
	extension extensions.Extension,
	runtime RuntimeInstanceSnapshot,
) bool {
	return !artifact.Core && artifact.ExtensionID == extension.ID &&
		artifact.ExtensionVersion == extension.Version && artifact.PackageDigest == extension.PackageDigest &&
		artifact.VersionID == extension.ActiveVersionID && artifact.RuntimeInstanceID == runtime.Identity.InstanceID
}

func runtimeCacheArtifactCanMoveForward(current, desired cacheregistry.Artifact) bool {
	if current.ExtensionID != desired.ExtensionID || current.Core || desired.Core || current.VersionID > desired.VersionID {
		return false
	}
	if current.VersionID == desired.VersionID {
		return current.ExtensionVersion == desired.ExtensionVersion && current.PackageDigest == desired.PackageDigest
	}
	return true
}

func runtimeCacheArtifactBelongsToExtension(artifact cacheregistry.Artifact, extension extensions.Extension) bool {
	if artifact.Core || artifact.ExtensionID != extension.ID || artifact.VersionID > extension.ActiveVersionID {
		return false
	}
	if artifact.VersionID == extension.ActiveVersionID {
		return artifact.ExtensionVersion == extension.Version && artifact.PackageDigest == extension.PackageDigest
	}
	return true
}

// restoreRuntimeCachePublication returns whether it inserted/replaced material.
func restoreRuntimeCachePublication(
	registry *cacheregistry.Registry,
	after *cacheregistry.Artifact,
	before *cacheregistry.Publication,
) (bool, error) {
	if registry == nil {
		return false, extensions.ErrRuntimeCachePublicationUnavailable
	}
	if before == nil {
		if after == nil {
			return false, nil
		}
		_, removed, err := registry.Remove(*after)
		return removed, err
	}
	current, found := registry.SnapshotPublication(before.Artifact.ExtensionID)
	if !found {
		_, err := registry.Publish(*before)
		return err == nil, err
	}
	if current.Artifact == before.Artifact {
		_, err := registry.Publish(*before)
		return false, err
	}
	if after != nil && current.Artifact == *after {
		_, err := registry.PublishIfArtifact(*after, *before)
		return err == nil, err
	}
	return false, cacheregistry.ErrArtifactConflict
}

var _ extensions.RuntimeCachePublicationBoundary = (*PostgresLifecycleBoundaryRegistries)(nil)
var _ extensions.RuntimeCachePublicationMutation = (*runtimeCachePublishMutation)(nil)
var _ extensions.RuntimeCachePublicationMutation = (*runtimeCacheQuarantineMutation)(nil)
