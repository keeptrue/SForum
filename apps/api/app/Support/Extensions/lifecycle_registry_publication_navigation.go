package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

func (b *PostgresLifecycleBoundaryRegistries) NavigationRegistry() *navigationregistry.Registry {
	if b == nil {
		return nil
	}
	return b.navigation
}

func buildLifecycleNavigationPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
	impactDigest string,
) (*navigationregistry.Publication, error) {
	if !manifestPublishesNavigation(extension.Manifest) {
		return nil, nil
	}
	if validateExactNavigationPublicationArtifact(extension, binding, impactDigest) != nil {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	publication := navigationregistry.Publication{Artifact: navigationregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: impactDigest,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: binding.RuntimeInstanceID,
	}}
	publication.Navigation = make([]navigationregistry.NavigationDeclaration, 0, len(extension.Manifest.Navigation))
	for _, declaration := range extension.Manifest.Navigation {
		// Manifest V3 只有单一 Label；Registry 允许 Labels 缺省，Visibility 默认 public。
		item := navigationregistry.NavigationDeclaration{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Kind: declaration.Kind, Action: declaration.Action, TargetID: declaration.TargetID,
			Label: declaration.Label, Href: declaration.Href, Permission: declaration.Permission,
			OwnerResource: declaration.OwnerResource, Order: declaration.Order,
			Visibility: declaration.Visibility,
		}
		if item.Visibility == "" {
			item.Visibility = navigationregistry.VisibilityPublic
		}
		if item.Kind == navigationregistry.NavigationKindAccountSettings && item.Visibility == navigationregistry.VisibilityPublic {
			item.Visibility = navigationregistry.VisibilityAuthenticated
		}
		if declaration.Label != "" {
			item.Labels = map[string]string{"zh-CN": declaration.Label}
		}
		publication.Navigation = append(publication.Navigation, item)
	}
	publication.Regions = make([]navigationregistry.RegionDeclaration, 0, len(extension.Manifest.Regions))
	for _, declaration := range extension.Manifest.Regions {
		item := navigationregistry.RegionDeclaration{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Action: declaration.Action, TargetID: declaration.TargetID, Kind: declaration.Kind,
			Label: declaration.Label, Multiple: declaration.Multiple,
			Visibility: navigationregistry.VisibilityPublic,
		}
		if declaration.Label != "" {
			item.Labels = map[string]string{"zh-CN": declaration.Label}
		}
		publication.Regions = append(publication.Regions, item)
	}
	// 用 Registry 规范化顺序与默认值冻结进 durable digest，避免后续读到可变 Manifest。
	probe := navigationregistry.New()
	if _, err := probe.ReplaceAllIfRevision(0, []navigationregistry.Publication{publication}); err != nil {
		return nil, fmt.Errorf("build navigation registry publication: %w", err)
	}
	frozen, found := probe.SnapshotPublication(extension.ID)
	if !found {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	return &frozen, nil
}

func manifestPublishesNavigation(manifest extensions.Manifest) bool {
	return len(manifest.Navigation) > 0 || len(manifest.Regions) > 0
}

// Navigation Registry 需要 exact artifact + RuntimeInstanceID（Registry normalize 强制）。
// Manifest 当前无 Handler 字段，但仍走 Protocol V2 插件运行时绑定，与 SEO 一致。
func validateExactNavigationPublicationArtifact(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
	impactDigest string,
) error {
	if extension.ID == "" || extension.Version == "" || extension.ActiveVersionID <= 0 ||
		extension.ID != strings.TrimSpace(extension.ID) || extension.Version != strings.TrimSpace(extension.Version) ||
		!validLifecycleCleanupDigest(extension.PackageDigest) || !validLifecycleCleanupDigest(impactDigest) ||
		impactDigest != strings.ToLower(strings.TrimSpace(impactDigest)) ||
		extension.Type != extensions.TypePlugin || extension.Manifest.ID != extension.ID ||
		extension.Manifest.Version != extension.Version || extension.Manifest.Type != extensions.TypePlugin ||
		extension.Manifest.Backend.ProtocolVersion != 2 || strings.TrimSpace(extension.Manifest.Backend.Entry) == "" ||
		validateExactCoordinatorBinding("navigation registry", binding, extension, true) != nil {
		return ErrLifecycleRegistryPublicationInvalid
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeNavigationMaterials(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	source, target *lifecycleRegistryMaterial,
) error {
	hasSource := source != nil && manifestPublishesNavigation(source.extension.Manifest)
	hasTarget := target != nil && manifestPublishesNavigation(target.extension.Manifest)
	if !hasSource && !hasTarget {
		return nil
	}
	if b == nil || b.navigation == nil || b.assetAuthority == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if hasSource {
		impact, err := b.assetAuthority.RestoreImpactDigest(ctx, source.extension)
		if err != nil {
			return fmt.Errorf("freeze source navigation authority: %w", err)
		}
		if err := b.freezeNavigationMaterial(source, impact); err != nil {
			return err
		}
	}
	if hasTarget {
		impact, err := b.assetAuthority.OperationImpactDigest(ctx, request.OperationID, target.extension)
		if err != nil {
			return fmt.Errorf("freeze target navigation authority: %w", err)
		}
		if err := b.freezeNavigationMaterial(target, impact); err != nil {
			return err
		}
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeNavigationMaterial(
	material *lifecycleRegistryMaterial,
	impactDigest string,
) error {
	publication, err := buildLifecycleNavigationPublication(material.extension, material.binding, impactDigest)
	if err != nil {
		return err
	}
	material.navigationPublication = publication
	return refreshLifecycleRegistryMaterialDigest(material)
}

func (b *PostgresLifecycleBoundaryRegistries) restoreNavigationPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || b.navigation == nil || b.manager == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := b.navigation.Snapshot()
	publications := coreLifecycleNavigationPublications(snapshot.Publications)
	if safeMode {
		// Safe Mode 仅保留 Host Core；第三方 navigation/regions 一律剔除。
		if _, err := b.navigation.ReplaceAllWithSafeModeIfRevision(snapshot.Revision, publications, true); err != nil {
			return wrapLifecycleNavigationError("restore navigation registry safe mode", err)
		}
		return nil
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled ||
			!manifestPublishesNavigation(item.Manifest) {
			continue
		}
		if b.assetAuthority == nil {
			return ErrLifecycleRegistryPublicationUnavailable
		}
		impact, err := b.assetAuthority.RestoreImpactDigest(ctx, item)
		if errors.Is(err, extensions.ErrTrustGrantNotFound) || errors.Is(err, extensions.ErrLifecycleAuthorityNotFound) {
			// 未确认或已撤销的可执行包在启动时保持关闭。
			continue
		}
		if err != nil {
			return fmt.Errorf("restore navigation authority for %s: %w", item.ID, err)
		}
		runtime, err := b.manager.ActiveRuntimeInstance(item.ID)
		if err != nil {
			// 失败或 boot-loop 抑制的进程不能重新获得声明。
			continue
		}
		if !runtimeInstanceMatchesExtension(runtime, item) || !b.manager.RuntimeInstanceAvailable(runtime.Identity) {
			return fmt.Errorf("%w: startup navigation runtime for %s is not exact and available",
				ErrLifecycleRegistryPublicationConflict, item.ID)
		}
		publication, err := buildLifecycleNavigationPublication(item, extensions.LifecycleRuntimeBinding{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			VersionID: item.ActiveVersionID, RuntimeInstanceID: runtime.Identity.InstanceID,
		}, impact)
		if err != nil {
			return fmt.Errorf("restore navigation registry for %s: %w", item.ID, err)
		}
		if publication != nil {
			publications = append(publications, *publication)
		}
	}
	if _, err := b.navigation.ReplaceAllWithSafeModeIfRevision(snapshot.Revision, publications, false); err != nil {
		return wrapLifecycleNavigationError("restore navigation registry publication", err)
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) RestoreNavigationPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	return b.restoreNavigationPublications(ctx, items, safeMode)
}

func coreLifecycleNavigationPublications(input []navigationregistry.Publication) []navigationregistry.Publication {
	result := make([]navigationregistry.Publication, 0, len(input))
	for _, publication := range input {
		if publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	return result
}

func (b *PostgresLifecycleBoundaryRegistries) validateNavigationTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	if b == nil || b.navigation == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	snapshot := b.navigation.Snapshot()
	for _, desired := range []*lifecycleRegistryMaterial{source, target} {
		var publication *navigationregistry.Publication
		if desired != nil {
			publication = desired.navigationPublication
		}
		graph, err := lifecycleNavigationGraph(snapshot, extensionID, publication, source, target)
		if err != nil {
			return err
		}
		if _, err := navigationregistry.New().ReplaceAllWithSafeModeIfRevision(0, graph, snapshot.SafeMode); err != nil {
			return wrapLifecycleNavigationError("validate navigation registry publication", err)
		}
	}
	return nil
}

func lifecycleNavigationGraph(
	snapshot navigationregistry.Snapshot,
	extensionID string,
	desired *navigationregistry.Publication,
	allowedMaterials ...*lifecycleRegistryMaterial,
) ([]navigationregistry.Publication, error) {
	allowed := make(map[navigationregistry.Artifact]navigationregistry.Publication, len(allowedMaterials))
	for _, material := range allowedMaterials {
		if material == nil || material.navigationPublication == nil {
			continue
		}
		artifact := material.navigationPublication.Artifact
		if existing, found := allowed[artifact]; found && !reflect.DeepEqual(existing, *material.navigationPublication) {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
		allowed[artifact] = *material.navigationPublication
	}
	publications := make([]navigationregistry.Publication, 0, len(snapshot.Publications)+1)
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

func (b *PostgresLifecycleBoundaryRegistries) reconcileNavigation(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	if b == nil || b.navigation == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	var desiredPublication *navigationregistry.Publication
	if desired != nil {
		desiredPublication = desired.navigationPublication
	}
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := b.navigation.Snapshot()
		graph, err := lifecycleNavigationGraph(snapshot, extensionID, desiredPublication, source, target)
		if err != nil {
			return err
		}
		if _, err := b.navigation.ReplaceAllWithSafeModeIfRevision(snapshot.Revision, graph, snapshot.SafeMode); err == nil {
			return nil
		} else if !errors.Is(err, navigationregistry.ErrRevisionConflict) {
			return wrapLifecycleNavigationError("publish navigation registry graph", err)
		}
	}
	return navigationregistry.ErrRevisionConflict
}

func wrapLifecycleNavigationError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, navigationregistry.ErrArtifactConflict) || errors.Is(err, navigationregistry.ErrSafeMode) {
		return fmt.Errorf("%w: %s: %v", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}
