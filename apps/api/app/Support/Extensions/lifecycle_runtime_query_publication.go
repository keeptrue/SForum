package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

type runtimeQueryPublishMutation struct {
	mu       sync.Mutex
	registry *queryregistry.Registry
	before   *queryregistry.Publication
	after    queryregistry.Publication
	changed  bool
	done     bool
}

func (m *runtimeQueryPublishMutation) Rollback() error {
	if m == nil {
		return extensions.ErrRuntimeQueryPublicationUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done || !m.changed {
		m.done = true
		return nil
	}
	if _, err := restoreRuntimeQueryPublication(m.registry, &m.after.Artifact, m.before); err != nil {
		return err
	}
	m.done = true
	return nil
}

type runtimeQueryQuarantineMutation struct {
	mu             sync.Mutex
	registry       *queryregistry.Registry
	manager        *Manager
	before         *queryregistry.Publication
	resumeIdentity RuntimeInstanceIdentity
	resumeOwned    bool
	done           bool
}

func (m *runtimeQueryQuarantineMutation) Rollback() error {
	if m == nil {
		return extensions.ErrRuntimeQueryPublicationUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return nil
	}
	restored := false
	if m.before != nil {
		var err error
		restored, err = restoreRuntimeQueryPublication(m.registry, nil, m.before)
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

// PublishRuntimeQueries production-binds a Query Registry plugin that uses the
// legacy Service Enable path because it declares no Lifecycle V2 hook contract.
func (b *PostgresLifecycleBoundaryRegistries) PublishRuntimeQueries(
	ctx context.Context,
	extension extensions.Extension,
) (extensions.RuntimeQueryPublicationMutation, error) {
	if b == nil || b.manager == nil || b.queries == nil || ctx == nil ||
		!hasQueryRegistryPublication(extension.Manifest) {
		return nil, extensions.ErrRuntimeQueryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime, err := b.manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || !runtimeQueryInstanceMatches(runtime, extension) ||
		!b.manager.RuntimeInstanceAvailable(runtime.Identity) {
		return nil, errors.Join(extensions.ErrRuntimeQueryPublicationUnavailable, err)
	}
	desired, err := buildLifecycleQueryPublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	})
	if err != nil || desired == nil {
		return nil, errors.Join(extensions.ErrRuntimeQueryPublicationUnavailable, err)
	}

	mutation := &runtimeQueryPublishMutation{registry: b.queries, after: *desired}
	current, found := b.queries.SnapshotPublication(extension.ID)
	if found {
		if !runtimeQueryArtifactCanMoveForward(current.Artifact, desired.Artifact) {
			return nil, fmt.Errorf("%w: newer query artifact is already active", ErrLifecycleRegistryPublicationConflict)
		}
		previous := current
		mutation.before = &previous
		if current.Artifact == desired.Artifact {
			// The Service compensation target is disabled/stopped, so even an
			// idempotently pre-existing publication must be removed on rollback.
			mutation.before = nil
			mutation.changed = true
			if _, err := b.queries.Publish(*desired); err != nil {
				return mutation, fmt.Errorf("publish exact runtime queries: %w", err)
			}
		} else {
			if _, err := b.queries.PublishIfArtifact(current.Artifact, *desired); err != nil {
				return nil, fmt.Errorf("replace exact runtime queries: %w", err)
			}
			mutation.changed = true
		}
	} else {
		if _, err := b.queries.Publish(*desired); err != nil {
			return nil, fmt.Errorf("publish exact runtime queries: %w", err)
		}
		mutation.changed = true
	}
	if !b.runtimeQueryArtifactAvailable(desired.Artifact) {
		rollbackErr := mutation.Rollback()
		return nil, errors.Join(extensions.ErrRuntimeQueryPublicationUnavailable, rollbackErr)
	}
	return mutation, nil
}

// QuarantineRuntimeQueries closes exact runtime admission and removes only the
// observed publication. The returned mutation retains enough Host state to
// restore publication-before-resume if Store.Disable fails.
func (b *PostgresLifecycleBoundaryRegistries) QuarantineRuntimeQueries(
	ctx context.Context,
	extension extensions.Extension,
) (extensions.RuntimeQueryPublicationMutation, error) {
	if b == nil || b.manager == nil || b.queries == nil || ctx == nil ||
		!hasQueryRegistryPublication(extension.Manifest) {
		return nil, extensions.ErrRuntimeQueryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mutation := &runtimeQueryQuarantineMutation{registry: b.queries, manager: b.manager}
	current, found := b.queries.SnapshotPublication(extension.ID)
	if found {
		if !runtimeQueryArtifactBelongsToExtension(current.Artifact, extension) {
			return nil, fmt.Errorf("%w: stale disable cannot remove active query artifact", ErrLifecycleRegistryPublicationConflict)
		}
		previous := current
		mutation.before = &previous
	}

	runtime, runtimeErr := b.manager.ActiveRuntimeInstance(extension.ID)
	if runtimeErr == nil {
		if !runtimeQueryInstanceMatches(runtime, extension) {
			return nil, fmt.Errorf("%w: active runtime does not match disabled query artifact", ErrLifecycleRegistryPublicationConflict)
		}
		if mutation.before != nil && !runtimeQueryArtifactMatchesInstance(mutation.before.Artifact, extension, runtime) {
			return nil, fmt.Errorf("%w: query publication does not match the active runtime instance", ErrLifecycleRegistryPublicationConflict)
		}
		mutation.resumeIdentity = runtime.Identity
		if !runtime.Admission.Draining {
			if _, err := b.manager.BeginDrain(runtime.Identity); err != nil {
				return nil, fmt.Errorf("begin exact query runtime drain: %w", err)
			}
			mutation.resumeOwned = true
		}
	} else if !errors.Is(runtimeErr, ErrRuntimeInstanceNotFound) {
		return nil, runtimeErr
	}

	if mutation.before != nil {
		_, removed, err := b.queries.Remove(mutation.before.Artifact)
		if err != nil || !removed {
			if mutation.resumeOwned {
				_, _ = b.manager.ResumeRuntimeInstance(mutation.resumeIdentity)
			}
			if err == nil {
				err = queryregistry.ErrArtifactConflict
			}
			return nil, fmt.Errorf("remove exact runtime queries: %w", err)
		}
	}
	return mutation, nil
}

func (b *PostgresLifecycleBoundaryRegistries) runtimeQueryArtifactAvailable(artifact queryregistry.Artifact) bool {
	if b == nil || b.manager == nil {
		return false
	}
	identity := RuntimeInstanceIdentity{ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID}
	runtime, err := b.manager.InspectRuntimeInstance(identity)
	return err == nil && runtime.ExtensionVersion == artifact.ExtensionVersion &&
		runtime.ArtifactDigest == artifact.PackageDigest && runtime.VersionID == artifact.VersionID &&
		b.manager.RuntimeInstanceAvailable(identity)
}

func runtimeQueryInstanceMatches(runtime RuntimeInstanceSnapshot, extension extensions.Extension) bool {
	return runtime.Active && runtime.Identity.ExtensionID == extension.ID &&
		runtime.ExtensionVersion == extension.Version && runtime.ArtifactDigest == extension.PackageDigest &&
		runtime.VersionID == extension.ActiveVersionID
}

func runtimeQueryArtifactMatchesInstance(
	artifact queryregistry.Artifact,
	extension extensions.Extension,
	runtime RuntimeInstanceSnapshot,
) bool {
	return !artifact.Core && artifact.ExtensionID == extension.ID &&
		artifact.ExtensionVersion == extension.Version && artifact.PackageDigest == extension.PackageDigest &&
		artifact.VersionID == extension.ActiveVersionID && artifact.VersionID == runtime.VersionID &&
		artifact.RuntimeInstanceID == runtime.Identity.InstanceID
}

func runtimeQueryArtifactCanMoveForward(current, desired queryregistry.Artifact) bool {
	if current.ExtensionID != desired.ExtensionID || current.Core || desired.Core || current.VersionID > desired.VersionID {
		return false
	}
	if current.VersionID == desired.VersionID {
		return current.ExtensionVersion == desired.ExtensionVersion && current.PackageDigest == desired.PackageDigest
	}
	return true
}

func runtimeQueryArtifactBelongsToExtension(artifact queryregistry.Artifact, extension extensions.Extension) bool {
	if artifact.Core || artifact.ExtensionID != extension.ID || artifact.VersionID > extension.ActiveVersionID {
		return false
	}
	if artifact.VersionID == extension.ActiveVersionID {
		return artifact.ExtensionVersion == extension.Version && artifact.PackageDigest == extension.PackageDigest
	}
	return true
}

// restoreRuntimeQueryPublication returns whether it inserted/replaced material.
func restoreRuntimeQueryPublication(
	registry *queryregistry.Registry,
	after *queryregistry.Artifact,
	before *queryregistry.Publication,
) (bool, error) {
	if registry == nil {
		return false, extensions.ErrRuntimeQueryPublicationUnavailable
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
	return false, queryregistry.ErrArtifactConflict
}

var _ extensions.RuntimeQueryPublicationBoundary = (*PostgresLifecycleBoundaryRegistries)(nil)
var _ extensions.RuntimeQueryPublicationMutation = (*runtimeQueryPublishMutation)(nil)
var _ extensions.RuntimeQueryPublicationMutation = (*runtimeQueryQuarantineMutation)(nil)
