package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
)

func (b *PostgresLifecycleBoundaryRegistries) EntityRegistry() *entityregistry.Registry {
	if b == nil {
		return nil
	}
	return b.entity
}

// buildLifecycleEntityPublication freezes Manifest.entities into one exact Entity
// Registry publication. Durable entity rows remain Host-owned outside this graph.
func buildLifecycleEntityPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (*entityregistry.Publication, error) {
	if len(extension.Manifest.Entities) == 0 {
		return nil, nil
	}
	if validateExactEntityPublicationArtifact(extension, binding) != nil {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	publication := entityregistry.Publication{Artifact: entityregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: binding.RuntimeInstanceID,
	}}
	publication.Entities = make([]entityregistry.Declaration, 0, len(extension.Manifest.Entities))
	for _, declaration := range extension.Manifest.Entities {
		publication.Entities = append(publication.Entities, entityregistry.Declaration{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Kind: declaration.Kind, Label: declaration.Label, StorageKey: declaration.StorageKey,
			PermissionCreate: declaration.PermissionCreate, PermissionRead: declaration.PermissionRead,
			PermissionUpdate: declaration.PermissionUpdate, PermissionDelete: declaration.PermissionDelete,
			PermissionImport: declaration.PermissionImport, PermissionExport: declaration.PermissionExport,
			ImportExportPolicy: declaration.ImportExportPolicy, DeletionPolicy: declaration.DeletionPolicy,
			TaxonomyIDs:      append([]string(nil), declaration.TaxonomyIDs...),
			Hierarchical:     declaration.Hierarchical,
			EntityIDs:        append([]string(nil), declaration.EntityIDs...),
			PermissionManage: declaration.PermissionManage, PermissionAssign: declaration.PermissionAssign,
			EntityID: declaration.EntityID, Schema: declaration.Schema,
			UIComponent: declaration.UIComponent, UIModule: declaration.UIModule,
			UIDigest: declaration.UIDigest, Required: declaration.Required,
			Indexed: declaration.Indexed, IndexKind: declaration.IndexKind,
			PermissionFieldRead:  declaration.PermissionFieldRead,
			PermissionFieldWrite: declaration.PermissionFieldWrite,
			Validation:           declaration.Validation, Order: declaration.Order, Priority: declaration.Priority,
		})
	}
	probe := entityregistry.New()
	if _, err := probe.ReplaceAllIfRevision(0, []entityregistry.Publication{publication}, false); err != nil {
		return nil, fmt.Errorf("build entity registry publication: %w", err)
	}
	frozen, found := probe.SnapshotPublication(extension.ID)
	if !found {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	return &frozen, nil
}

func validateExactEntityPublicationArtifact(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) error {
	// Entity surfaces are Host-owned declaration graphs; no backend runtime is
	// required to freeze or restore the graph.
	if extension.ID == "" || extension.Version == "" || extension.ActiveVersionID <= 0 ||
		extension.ID != strings.TrimSpace(extension.ID) || extension.Version != strings.TrimSpace(extension.Version) ||
		!validLifecycleCleanupDigest(extension.PackageDigest) ||
		extension.Type != extensions.TypePlugin || extension.Manifest.ID != extension.ID ||
		extension.Manifest.Version != extension.Version || extension.Manifest.Type != extensions.TypePlugin ||
		validateExactCoordinatorBinding("entity registry", binding, extension, false) != nil {
		return ErrLifecycleRegistryPublicationInvalid
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeEntityMaterials(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	source, target *lifecycleRegistryMaterial,
) error {
	hasSource := source != nil && len(source.extension.Manifest.Entities) > 0
	hasTarget := target != nil && len(target.extension.Manifest.Entities) > 0
	if !hasSource && !hasTarget {
		return nil
	}
	if b == nil || b.entity == nil || b.assetAuthority == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if hasSource {
		if _, err := b.assetAuthority.RestoreImpactDigest(ctx, source.extension); err != nil {
			return fmt.Errorf("freeze source entity authority: %w", err)
		}
		if err := b.freezeEntityMaterial(source); err != nil {
			return err
		}
	}
	if hasTarget {
		if _, err := b.assetAuthority.OperationImpactDigest(ctx, request.OperationID, target.extension); err != nil {
			return fmt.Errorf("freeze target entity authority: %w", err)
		}
		if err := b.freezeEntityMaterial(target); err != nil {
			return err
		}
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeEntityMaterial(
	material *lifecycleRegistryMaterial,
) error {
	publication, err := buildLifecycleEntityPublication(material.extension, material.binding)
	if err != nil {
		return err
	}
	material.entityPublication = publication
	return refreshLifecycleRegistryMaterialDigest(material)
}

func (b *PostgresLifecycleBoundaryRegistries) restoreEntityPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	// Entity Registry 未接线时跳过：兼容尚未注入 P10 Entity 的旧边界测试。
	if b.entity == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := b.entity.Snapshot()
	publications := coreLifecycleEntityPublications(snapshot.Publications)
	if safeMode {
		if _, err := b.entity.ReplaceAllIfRevision(snapshot.Revision, publications, true); err != nil {
			return wrapLifecycleEntityError("restore entity registry safe mode", err)
		}
		return nil
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled || len(item.Manifest.Entities) == 0 {
			continue
		}
		if b.assetAuthority == nil {
			return ErrLifecycleRegistryPublicationUnavailable
		}
		if _, err := b.assetAuthority.RestoreImpactDigest(ctx, item); errors.Is(err, extensions.ErrTrustGrantNotFound) ||
			errors.Is(err, extensions.ErrLifecycleAuthorityNotFound) {
			continue
		} else if err != nil {
			return fmt.Errorf("restore entity authority for %s: %w", item.ID, err)
		}
		publication, err := buildLifecycleEntityPublication(item, extensions.LifecycleRuntimeBinding{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			VersionID: item.ActiveVersionID,
		})
		if err != nil {
			return fmt.Errorf("restore entity registry for %s: %w", item.ID, err)
		}
		if publication != nil {
			publications = append(publications, *publication)
		}
	}
	if _, err := b.entity.ReplaceAllIfRevision(snapshot.Revision, publications, false); err != nil {
		return wrapLifecycleEntityError("restore entity registry publication", err)
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) RestoreEntityPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	return b.restoreEntityPublications(ctx, items, safeMode)
}

func coreLifecycleEntityPublications(input []entityregistry.Publication) []entityregistry.Publication {
	result := make([]entityregistry.Publication, 0, len(input))
	for _, publication := range input {
		if publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	return result
}

func (b *PostgresLifecycleBoundaryRegistries) validateEntityTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	hasEntity := (source != nil && source.entityPublication != nil) ||
		(target != nil && target.entityPublication != nil)
	if !hasEntity {
		return nil
	}
	if b == nil || b.entity == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	snapshot := b.entity.Snapshot()
	for _, desired := range []*lifecycleRegistryMaterial{source, target} {
		var publication *entityregistry.Publication
		if desired != nil {
			publication = desired.entityPublication
		}
		graph, err := lifecycleEntityGraph(snapshot, extensionID, publication, source, target)
		if err != nil {
			return err
		}
		if _, err := entityregistry.New().ReplaceAllIfRevision(0, graph, snapshot.SafeMode); err != nil {
			return wrapLifecycleEntityError("validate entity registry publication", err)
		}
	}
	return nil
}

func lifecycleEntityGraph(
	snapshot entityregistry.Snapshot,
	extensionID string,
	desired *entityregistry.Publication,
	allowedMaterials ...*lifecycleRegistryMaterial,
) ([]entityregistry.Publication, error) {
	allowed := make(map[entityregistry.Artifact]entityregistry.Publication, len(allowedMaterials))
	for _, material := range allowedMaterials {
		if material == nil || material.entityPublication == nil {
			continue
		}
		artifact := material.entityPublication.Artifact
		if existing, found := allowed[artifact]; found && !reflect.DeepEqual(existing, *material.entityPublication) {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
		allowed[artifact] = *material.entityPublication
	}
	publications := make([]entityregistry.Publication, 0, len(snapshot.Publications)+1)
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

func (b *PostgresLifecycleBoundaryRegistries) reconcileEntity(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	hasEntity := (source != nil && source.entityPublication != nil) ||
		(target != nil && target.entityPublication != nil) ||
		(desired != nil && desired.entityPublication != nil)
	if !hasEntity {
		return nil
	}
	if b == nil || b.entity == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	var desiredPublication *entityregistry.Publication
	if desired != nil {
		desiredPublication = desired.entityPublication
	}
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := b.entity.Snapshot()
		graph, err := lifecycleEntityGraph(snapshot, extensionID, desiredPublication, source, target)
		if err != nil {
			return err
		}
		if _, err := b.entity.ReplaceAllIfRevision(snapshot.Revision, graph, snapshot.SafeMode); err == nil {
			return nil
		} else if !errors.Is(err, entityregistry.ErrRevisionConflict) {
			return wrapLifecycleEntityError("publish entity registry graph", err)
		}
	}
	return entityregistry.ErrRevisionConflict
}

func wrapLifecycleEntityError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, entityregistry.ErrArtifactConflict) || errors.Is(err, entityregistry.ErrSafeMode) {
		return fmt.Errorf("%w: %s: %v", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}
