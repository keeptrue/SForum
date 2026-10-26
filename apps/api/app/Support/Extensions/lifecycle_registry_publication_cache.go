package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func (b *PostgresLifecycleBoundaryRegistries) CacheRegistry() *cacheregistry.Registry {
	if b == nil {
		return nil
	}
	return b.caches
}

func buildLifecycleCachePublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (*cacheregistry.Publication, error) {
	if len(extension.Manifest.Cache) == 0 {
		return nil, nil
	}
	if validateExactCachePublicationArtifact(extension) != nil ||
		validateExactCoordinatorBinding("cache registry", binding, extension, true) != nil {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	publication := cacheregistry.Publication{Artifact: cacheregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: binding.VersionID,
		RuntimeInstanceID: binding.RuntimeInstanceID,
	}}
	publication.Caches = make([]cacheregistry.Declaration, 0, len(extension.Manifest.Cache))
	for _, declaration := range extension.Manifest.Cache {
		publication.Caches = append(publication.Caches, cacheregistry.Declaration{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Namespace: declaration.Namespace, Policy: declaration.Policy,
			Tags: append([]string(nil), declaration.Tags...), Provider: declaration.Provider,
			Invalidators: append([]string(nil), declaration.Invalidators...),
		})
	}
	// Freeze the Registry's canonical order into the durable lifecycle digest.
	probe := cacheregistry.New()
	if _, err := probe.ReplaceAllIfRevision(0, []cacheregistry.Publication{publication}, false); err != nil {
		return nil, fmt.Errorf("build cache registry publication: %w", err)
	}
	frozen, found := probe.SnapshotPublication(extension.ID)
	if !found {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	return &frozen, nil
}

// Cache declarations are executable Host API contracts. They require one
// exact Protocol V2 runtime even when the plugin declares no lifecycle hook.
func validateExactCachePublicationArtifact(extension extensions.Extension) error {
	if extension.ID == "" || extension.Version == "" || extension.ActiveVersionID <= 0 ||
		extension.ID != strings.TrimSpace(extension.ID) || extension.Version != strings.TrimSpace(extension.Version) ||
		!validLifecycleCleanupDigest(extension.PackageDigest) || extension.Type != extensions.TypePlugin ||
		extension.Manifest.ID != extension.ID || extension.Manifest.Version != extension.Version ||
		extension.Manifest.Type != extensions.TypePlugin || extension.Manifest.Backend.ProtocolVersion != 2 ||
		strings.TrimSpace(extension.Manifest.Backend.Entry) == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) restoreCachePublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || b.caches == nil || b.manager == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := b.caches.Snapshot()
	publications := coreLifecycleCachePublications(snapshot.Publications)
	if safeMode {
		if _, err := b.caches.ReplaceAllIfRevision(snapshot.Revision, publications, true); err != nil {
			return wrapLifecycleCacheError("restore cache registry safe mode", err)
		}
		return nil
	}
	if capacity := len(publications) + len(items); capacity > cap(publications) {
		grown := make([]cacheregistry.Publication, len(publications), capacity)
		copy(grown, publications)
		publications = grown
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled || len(item.Manifest.Cache) == 0 {
			continue
		}
		runtime, err := b.manager.ActiveRuntimeInstance(item.ID)
		if err != nil {
			// Boot-loop suppression keeps the declaration closed; stale package
			// material is never republished without its exact live instance.
			continue
		}
		if !runtimeInstanceMatchesExtension(runtime, item) || !b.manager.RuntimeInstanceAvailable(runtime.Identity) {
			return fmt.Errorf("%w: startup cache runtime for %s is not exact and available",
				ErrLifecycleRegistryPublicationConflict, item.ID)
		}
		publication, err := buildLifecycleCachePublication(item, extensions.LifecycleRuntimeBinding{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			VersionID: item.ActiveVersionID, RuntimeInstanceID: runtime.Identity.InstanceID,
		})
		if err != nil {
			return fmt.Errorf("restore cache registry for %s: %w", item.ID, err)
		}
		if publication != nil {
			publications = append(publications, *publication)
		}
	}
	if _, err := b.caches.ReplaceAllIfRevision(snapshot.Revision, publications, false); err != nil {
		return wrapLifecycleCacheError("restore cache registry publication", err)
	}
	return nil
}

func coreLifecycleCachePublications(input []cacheregistry.Publication) []cacheregistry.Publication {
	result := make([]cacheregistry.Publication, 0, len(input))
	for _, publication := range input {
		if publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	return result
}

func (b *PostgresLifecycleBoundaryRegistries) validateCacheTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	if b == nil || b.caches == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	snapshot := b.caches.Snapshot()
	for _, desired := range []*lifecycleRegistryMaterial{source, target} {
		var publication *cacheregistry.Publication
		if desired != nil {
			publication = desired.cachePublication
		}
		graph, err := lifecycleCacheGraph(snapshot, extensionID, publication, source, target)
		if err != nil {
			return err
		}
		if _, err := cacheregistry.New().ReplaceAllIfRevision(0, graph, snapshot.SafeMode); err != nil {
			return wrapLifecycleCacheError("validate cache registry publication", err)
		}
	}
	return nil
}

func lifecycleCacheGraph(
	snapshot cacheregistry.Snapshot,
	extensionID string,
	desired *cacheregistry.Publication,
	allowedMaterials ...*lifecycleRegistryMaterial,
) ([]cacheregistry.Publication, error) {
	allowed := make(map[cacheregistry.Artifact]cacheregistry.Publication, len(allowedMaterials))
	for _, material := range allowedMaterials {
		if material != nil && material.cachePublication != nil {
			artifact := material.cachePublication.Artifact
			if existing, found := allowed[artifact]; found && !reflect.DeepEqual(existing, *material.cachePublication) {
				return nil, ErrLifecycleRegistryPublicationConflict
			}
			allowed[artifact] = *material.cachePublication
		}
	}
	publications := make([]cacheregistry.Publication, 0, len(snapshot.Publications)+1)
	for _, publication := range snapshot.Publications {
		if publication.Artifact.ExtensionID != extensionID {
			publications = append(publications, publication)
			continue
		}
		frozen, ok := allowed[publication.Artifact]
		if !ok || !reflect.DeepEqual(frozen, publication) {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
	}
	if desired != nil {
		if snapshot.SafeMode {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
		publications = append(publications, *desired)
	}
	return publications, nil
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileCaches(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	if b == nil || b.caches == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	var desiredPublication *cacheregistry.Publication
	if desired != nil {
		desiredPublication = desired.cachePublication
	}
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := b.caches.Snapshot()
		graph, err := lifecycleCacheGraph(snapshot, extensionID, desiredPublication, source, target)
		if err != nil {
			return err
		}
		if _, err := b.caches.ReplaceAllIfRevision(snapshot.Revision, graph, snapshot.SafeMode); err == nil {
			return nil
		} else if !errors.Is(err, cacheregistry.ErrRevisionConflict) {
			return wrapLifecycleCacheError("publish cache registry graph", err)
		}
	}
	return cacheregistry.ErrRevisionConflict
}

func wrapLifecycleCacheError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, cacheregistry.ErrArtifactConflict) || errors.Is(err, cacheregistry.ErrSafeMode) {
		return fmt.Errorf("%w: %s: %v", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}
