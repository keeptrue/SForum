package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

func (b *PostgresLifecycleBoundaryRegistries) QueryRegistry() *queryregistry.Registry {
	if b == nil {
		return nil
	}
	return b.queries
}

// BuildLifecycleQueryPublication constructs the exact lifecycle Query Registry
// publication used by enable/restore paths. Integration tests call this to prove
// the same Schema/filter metadata path without driving the full coordinator.
func BuildLifecycleQueryPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (*queryregistry.Publication, error) {
	return buildLifecycleQueryPublication(extension, binding)
}

func buildLifecycleQueryPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (*queryregistry.Publication, error) {
	if !hasQueryRegistryPublication(extension.Manifest) {
		return nil, nil
	}
	if extension.Type != extensions.TypePlugin ||
		validateExactQueryPublicationArtifact(extension) != nil ||
		validateExactCoordinatorBinding("query registry", binding, extension, true) != nil {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	publication := &queryregistry.Publication{Artifact: queryregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: binding.VersionID,
		RuntimeInstanceID: binding.RuntimeInstanceID,
	}}
	publication.Queries = make([]queryregistry.QueryDeclaration, 0, len(extension.Manifest.Queries))
	for _, query := range extension.Manifest.Queries {
		declaration := queryregistry.QueryDeclaration{
			ID: query.ID, ContractVersion: query.ContractVersion, Entity: query.Entity,
			PlanVersion: query.PlanVersion, Fields: append([]string(nil), query.Fields...),
			Relations: append([]string(nil), query.Relations...),
			Filters:   append([]string(nil), query.Filters...), Sort: append([]string(nil), query.Sort...),
			Pagination: query.Pagination, ResultSchema: query.ResultSchema,
			PermissionPolicy: query.PermissionPolicy, CacheTags: append([]string(nil), query.CacheTags...),
			// Handler/Identity/DefaultSort 是 Manifest 可选 executable 元数据；
			// 无私有 provider material 时仍可 inspect/plan，执行由后续 runtime 绑定。
			Handler:        strings.TrimSpace(query.Handler),
			IdentityFields: append([]string(nil), query.IdentityFields...),
		}
		if len(query.DefaultSort) > 0 {
			declaration.DefaultSort = make([]queryregistry.SortValue, 0, len(query.DefaultSort))
			for _, item := range query.DefaultSort {
				declaration.DefaultSort = append(declaration.DefaultSort, queryregistry.SortValue{
					Field: item.Field, Descending: item.Descending,
				})
			}
		}
		publication.Queries = append(publication.Queries, declaration)
	}
	if len(extension.Manifest.QueryResultFilters) > 0 {
		// Host 从目标 query owner 复制 identity，防止 filter 自选装饰字段。
		queryByID := make(map[string]queryregistry.QueryDeclaration, len(publication.Queries))
		for _, declaration := range publication.Queries {
			queryByID[declaration.ID] = declaration
		}
		publication.ResultFilters = make([]queryregistry.ResultFilterDeclaration, 0, len(extension.Manifest.QueryResultFilters))
		for _, filter := range extension.Manifest.QueryResultFilters {
			item := queryregistry.ResultFilterDeclaration{
				ID: filter.ID, ContractVersion: filter.ContractVersion,
				QueryID: filter.QueryID, QueryContractVersion: filter.QueryContractVersion,
				QueryPlanVersion: filter.QueryPlanVersion, Handler: strings.TrimSpace(filter.Handler),
				Priority: filter.Priority, FailurePolicy: filter.FailurePolicy, TimeoutMS: filter.TimeoutMS,
			}
			if filter.Dependency != nil {
				item.Dependency = &queryregistry.ResultFilterDependency{
					ExtensionID:       filter.Dependency.ExtensionID,
					VersionConstraint: filter.Dependency.VersionConstraint,
				}
			}
			if target, ok := queryByID[filter.QueryID]; ok {
				item.IdentityFields = append([]string(nil), target.IdentityFields...)
			}
			publication.ResultFilters = append(publication.ResultFilters, item)
		}
	}
	bound, err := bindLifecycleQuerySchemas(extension, *publication)
	if err != nil {
		return nil, err
	}
	return &bound, nil
}

// Query declarations require an exact Protocol V2 runtime, but the Manifest
// lifecycle hook contract is optional. Lifecycle orchestration and Query
// publication are independent extension surfaces.
func validateExactQueryPublicationArtifact(extension extensions.Extension) error {
	if extension.ID == "" || extension.Version == "" || extension.PackageDigest == "" ||
		extension.ID != strings.TrimSpace(extension.ID) || extension.Version != strings.TrimSpace(extension.Version) ||
		extension.PackageDigest != strings.TrimSpace(extension.PackageDigest) || extension.Type != extensions.TypePlugin ||
		extension.Manifest.ID != extension.ID || extension.Manifest.Version != extension.Version ||
		extension.Manifest.Type != extensions.TypePlugin || extension.Manifest.Backend.ProtocolVersion != 2 {
		return ErrLifecycleRegistryPublicationInvalid
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) restoreQueryPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || b.queries == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := b.queries.Snapshot()
	publications := coreLifecycleQueryPublications(snapshot.Publications)
	if safeMode {
		if _, err := b.queries.ReplaceAllIfRevision(snapshot.Revision, publications, true); err != nil {
			return fmt.Errorf("restore query registry safe mode: %w", err)
		}
		return nil
	}
	if capacity := len(publications) + len(items); capacity > cap(publications) {
		grown := make([]queryregistry.Publication, len(publications), capacity)
		copy(grown, publications)
		publications = grown
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled ||
			!hasQueryRegistryPublication(item.Manifest) {
			continue
		}
		runtime, err := b.manager.ActiveRuntimeInstance(item.ID)
		if err != nil {
			// 与 Route 恢复保持一致：启动失败或 boot-loop 抑制的插件保持关闭，
			// 但不得把一个已存在的错误 exact runtime 当作普通缺席。
			continue
		}
		if !runtimeInstanceMatchesExtension(runtime, item) || !b.manager.RuntimeInstanceAvailable(runtime.Identity) {
			return fmt.Errorf("%w: startup query runtime for %s is not exact and available", ErrLifecycleRegistryPublicationConflict, item.ID)
		}
		publication, err := buildLifecycleQueryPublication(item, extensions.LifecycleRuntimeBinding{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			VersionID: item.ActiveVersionID, RuntimeInstanceID: runtime.Identity.InstanceID,
		})
		if err != nil {
			return fmt.Errorf("restore query registry for %s: %w", item.ID, err)
		}
		if publication != nil {
			publications = append(publications, *publication)
		}
	}
	if _, err := b.queries.ReplaceAllIfRevision(snapshot.Revision, publications, false); err != nil {
		return fmt.Errorf("restore query registry publication: %w", err)
	}
	return nil
}

func hasQueryRegistryPublication(manifest extensions.Manifest) bool {
	return len(manifest.Queries) > 0 || len(manifest.QueryResultFilters) > 0
}

func coreLifecycleQueryPublications(input []queryregistry.Publication) []queryregistry.Publication {
	result := make([]queryregistry.Publication, 0, len(input))
	for _, publication := range input {
		if publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	return result
}

func (b *PostgresLifecycleBoundaryRegistries) validateQueryTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	if b == nil || b.queries == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	snapshot := b.queries.Snapshot()
	for _, desired := range []*lifecycleRegistryMaterial{source, target} {
		var publication *queryregistry.Publication
		if desired != nil {
			publication = desired.queryPublication
		}
		graph, err := lifecycleQueryGraph(snapshot, extensionID, publication, source, target)
		if err != nil {
			return err
		}
		if _, err := queryregistry.New().ReplaceAll(graph, snapshot.SafeMode); err != nil {
			return fmt.Errorf("validate query registry publication: %w", err)
		}
	}
	return nil
}

func lifecycleQueryGraph(
	snapshot queryregistry.Snapshot,
	extensionID string,
	desired *queryregistry.Publication,
	allowedMaterials ...*lifecycleRegistryMaterial,
) ([]queryregistry.Publication, error) {
	allowed := make(map[queryregistry.Artifact]struct{}, len(allowedMaterials))
	for _, material := range allowedMaterials {
		if material != nil && material.queryPublication != nil {
			allowed[material.queryPublication.Artifact] = struct{}{}
		}
	}
	publications := make([]queryregistry.Publication, 0, len(snapshot.Publications)+1)
	for _, publication := range snapshot.Publications {
		if publication.Artifact.ExtensionID != extensionID {
			publications = append(publications, publication)
			continue
		}
		if _, ok := allowed[publication.Artifact]; !ok {
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

func (b *PostgresLifecycleBoundaryRegistries) reconcileQueries(
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	if b == nil || b.queries == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	var desiredPublication *queryregistry.Publication
	if desired != nil {
		desiredPublication = desired.queryPublication
	}
	current, found := b.queries.SnapshotPublication(extensionID)
	if found && !lifecycleQueryArtifactAllowed(current.Artifact, source, target) {
		return ErrLifecycleRegistryPublicationConflict
	}
	if desiredPublication == nil {
		if !found {
			return nil
		}
		_, _, err := b.queries.Remove(current.Artifact)
		return wrapLifecycleQueryError("remove query registry publication", err)
	}
	if b.queries.Snapshot().SafeMode {
		return ErrLifecycleRegistryPublicationConflict
	}
	var err error
	if !found {
		_, err = b.queries.Publish(*desiredPublication)
	} else if current.Artifact == desiredPublication.Artifact {
		_, err = b.queries.Publish(*desiredPublication)
	} else {
		_, err = b.queries.PublishIfArtifact(current.Artifact, *desiredPublication)
	}
	return wrapLifecycleQueryError("publish query registry publication", err)
}

func lifecycleQueryArtifactAllowed(
	artifact queryregistry.Artifact,
	materials ...*lifecycleRegistryMaterial,
) bool {
	for _, material := range materials {
		if material != nil && material.queryPublication != nil && material.queryPublication.Artifact == artifact {
			return true
		}
	}
	return false
}

func wrapLifecycleQueryError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, queryregistry.ErrArtifactConflict) || errors.Is(err, queryregistry.ErrSafeMode) {
		return fmt.Errorf("%w: %s: %v", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}
