package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
)

func (b *PostgresLifecycleBoundaryRegistries) MediaRegistry() *mediaregistry.Registry {
	if b == nil {
		return nil
	}
	return b.media
}

// buildLifecycleMediaPublication freezes Manifest.media into one exact Media
// Pipeline Registry publication. Manifest pipeline entries become transform-stage
// processors plus optional variants; each entry also contributes a MIME policy
// scoped to its declared MIMEs so upload planning has an exact owner.
func buildLifecycleMediaPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
	impactDigest string,
) (*mediaregistry.Publication, error) {
	if len(extension.Manifest.Media) == 0 {
		return nil, nil
	}
	if validateExactMediaPublicationArtifact(extension, binding, impactDigest) != nil {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	publication := mediaregistry.Publication{Artifact: mediaregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: impactDigest,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: binding.RuntimeInstanceID,
	}}
	for _, pipeline := range extension.Manifest.Media {
		permission := strings.TrimSpace(pipeline.Permission)
		if permission == "" {
			// Host 推荐默认：未声明权限时仍绑定附件上传能力，供 plan 授权点使用。
			permission = "attachment.upload"
		}
		policyID := pipeline.ID + ".mime-policy"
		publication.Policies = append(publication.Policies, mediaregistry.MIMEPolicyDeclaration{
			ID: policyID, ContractVersion: policyID + "@1", Purpose: "general",
			Priority: pipeline.Priority, RequiredPermission: permission,
			AllowedMIMEs:       append([]string(nil), pipeline.MIMEs...),
			StrictDeclaredMIME: true, Budget: mediaregistry.DefaultBudget(),
		})
		// Manifest media 是可组合的 transform/provider 声明；生命周期冻结为
		// transform 阶段 compose 处理器，variant 绑定 exact 本包 processor。
		execution := mediaregistry.ExecutionSync
		if len(pipeline.Transforms) > 0 {
			execution = mediaregistry.ExecutionBackground
		}
		publication.Processors = append(publication.Processors, mediaregistry.ProcessorDeclaration{
			ID: pipeline.ID, ContractVersion: pipeline.ContractVersion,
			Stage: mediaregistry.StageTransform, Purpose: "general",
			MIMEs: append([]string(nil), pipeline.MIMEs...), Handler: pipeline.Handler,
			Priority: pipeline.Priority, Mode: mediaregistry.ProcessorCompose,
			Execution: execution, FailureMode: mediaregistry.FailureFallbackOriginal,
			RequiredPermission: permission,
			Retry:              mediaregistry.RetryPolicy{MaxAttempts: 3, BaseDelaySeconds: 2, MaxDelaySeconds: 30},
		})
		for _, transform := range pipeline.Transforms {
			variantID := pipeline.ID + "." + transform.ID
			publication.Variants = append(publication.Variants, mediaregistry.VariantDeclaration{
				ID: variantID, ContractVersion: variantID + "@1", Purpose: "general",
				Name: transform.Variant, ProcessorID: pipeline.ID,
				ProcessorContractVersion:  pipeline.ContractVersion,
				ProcessorOwnerExtensionID: extension.ID,
				ProcessorPackageDigest:    extension.PackageDigest,
				OutputMIME:                lifecycleMediaOutputMIME(transform.Format),
				Priority:                  pipeline.Priority,
			})
		}
	}
	// 探针 Registry 规范化顺序/边界后冻结进 durable lifecycle digest。
	probe := mediaregistry.New()
	if _, err := probe.ReplaceAllIfRevision(0, []mediaregistry.Publication{publication}, false); err != nil {
		return nil, fmt.Errorf("build media registry publication: %w", err)
	}
	frozen, found := probe.SnapshotPublication(extension.ID)
	if !found {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	return &frozen, nil
}

func lifecycleMediaOutputMIME(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case "", "webp":
		return "image/webp"
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "avif":
		return "image/avif"
	default:
		if strings.Contains(format, "/") {
			return format
		}
		return "image/" + format
	}
}

func validateExactMediaPublicationArtifact(
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
		validateExactCoordinatorBinding("media registry", binding, extension, true) != nil {
		return ErrLifecycleRegistryPublicationInvalid
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeMediaMaterials(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	source, target *lifecycleRegistryMaterial,
) error {
	hasSource := source != nil && len(source.extension.Manifest.Media) > 0
	hasTarget := target != nil && len(target.extension.Manifest.Media) > 0
	if !hasSource && !hasTarget {
		return nil
	}
	if b == nil || b.media == nil || b.assetAuthority == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if hasSource {
		impact, err := b.assetAuthority.RestoreImpactDigest(ctx, source.extension)
		if err != nil {
			return fmt.Errorf("freeze source media authority: %w", err)
		}
		if err := b.freezeMediaMaterial(source, impact); err != nil {
			return err
		}
	}
	if hasTarget {
		impact, err := b.assetAuthority.OperationImpactDigest(ctx, request.OperationID, target.extension)
		if err != nil {
			return fmt.Errorf("freeze target media authority: %w", err)
		}
		if err := b.freezeMediaMaterial(target, impact); err != nil {
			return err
		}
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeMediaMaterial(
	material *lifecycleRegistryMaterial,
	impactDigest string,
) error {
	publication, err := buildLifecycleMediaPublication(material.extension, material.binding, impactDigest)
	if err != nil {
		return err
	}
	material.mediaPublication = publication
	return refreshLifecycleRegistryMaterialDigest(material)
}

func (b *PostgresLifecycleBoundaryRegistries) restoreMediaPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	// Media Registry 未接线时跳过：兼容尚未注入 P10 Media 的旧边界测试。
	if b.media == nil {
		return nil
	}
	if b.manager == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := b.media.Snapshot()
	publications := coreLifecycleMediaPublications(snapshot.Publications)
	if safeMode {
		if _, err := b.media.ReplaceAllIfRevision(snapshot.Revision, publications, true); err != nil {
			return wrapLifecycleMediaError("restore media registry safe mode", err)
		}
		return nil
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled || len(item.Manifest.Media) == 0 {
			continue
		}
		if b.assetAuthority == nil {
			return ErrLifecycleRegistryPublicationUnavailable
		}
		impact, err := b.assetAuthority.RestoreImpactDigest(ctx, item)
		if errors.Is(err, extensions.ErrTrustGrantNotFound) || errors.Is(err, extensions.ErrLifecycleAuthorityNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("restore media authority for %s: %w", item.ID, err)
		}
		runtime, err := b.manager.ActiveRuntimeInstance(item.ID)
		if err != nil {
			continue
		}
		if !runtimeInstanceMatchesExtension(runtime, item) || !b.manager.RuntimeInstanceAvailable(runtime.Identity) {
			return fmt.Errorf("%w: startup media runtime for %s is not exact and available",
				ErrLifecycleRegistryPublicationConflict, item.ID)
		}
		publication, err := buildLifecycleMediaPublication(item, extensions.LifecycleRuntimeBinding{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			VersionID: item.ActiveVersionID, RuntimeInstanceID: runtime.Identity.InstanceID,
		}, impact)
		if err != nil {
			return fmt.Errorf("restore media registry for %s: %w", item.ID, err)
		}
		if publication != nil {
			publications = append(publications, *publication)
		}
	}
	if _, err := b.media.ReplaceAllIfRevision(snapshot.Revision, publications, false); err != nil {
		return wrapLifecycleMediaError("restore media registry publication", err)
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) RestoreMediaPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	return b.restoreMediaPublications(ctx, items, safeMode)
}

func coreLifecycleMediaPublications(input []mediaregistry.Publication) []mediaregistry.Publication {
	result := make([]mediaregistry.Publication, 0, len(input))
	for _, publication := range input {
		if publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	return result
}

func (b *PostgresLifecycleBoundaryRegistries) validateMediaTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	hasMedia := (source != nil && source.mediaPublication != nil) ||
		(target != nil && target.mediaPublication != nil)
	if !hasMedia {
		return nil
	}
	if b == nil || b.media == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	snapshot := b.media.Snapshot()
	for _, desired := range []*lifecycleRegistryMaterial{source, target} {
		var publication *mediaregistry.Publication
		if desired != nil {
			publication = desired.mediaPublication
		}
		graph, err := lifecycleMediaGraph(snapshot, extensionID, publication, source, target)
		if err != nil {
			return err
		}
		if _, err := mediaregistry.New().ReplaceAllIfRevision(0, graph, snapshot.SafeMode); err != nil {
			return wrapLifecycleMediaError("validate media registry publication", err)
		}
	}
	return nil
}

func lifecycleMediaGraph(
	snapshot mediaregistry.Snapshot,
	extensionID string,
	desired *mediaregistry.Publication,
	allowedMaterials ...*lifecycleRegistryMaterial,
) ([]mediaregistry.Publication, error) {
	allowed := make(map[mediaregistry.Artifact]mediaregistry.Publication, len(allowedMaterials))
	for _, material := range allowedMaterials {
		if material == nil || material.mediaPublication == nil {
			continue
		}
		artifact := material.mediaPublication.Artifact
		if existing, found := allowed[artifact]; found && !reflect.DeepEqual(existing, *material.mediaPublication) {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
		allowed[artifact] = *material.mediaPublication
	}
	publications := make([]mediaregistry.Publication, 0, len(snapshot.Publications)+1)
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

func (b *PostgresLifecycleBoundaryRegistries) reconcileMedia(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	hasMedia := (source != nil && source.mediaPublication != nil) ||
		(target != nil && target.mediaPublication != nil) ||
		(desired != nil && desired.mediaPublication != nil)
	if !hasMedia {
		return nil
	}
	if b == nil || b.media == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	var desiredPublication *mediaregistry.Publication
	if desired != nil {
		desiredPublication = desired.mediaPublication
	}
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := b.media.Snapshot()
		graph, err := lifecycleMediaGraph(snapshot, extensionID, desiredPublication, source, target)
		if err != nil {
			return err
		}
		if _, err := b.media.ReplaceAllIfRevision(snapshot.Revision, graph, snapshot.SafeMode); err == nil {
			return nil
		} else if !errors.Is(err, mediaregistry.ErrRevisionConflict) {
			return wrapLifecycleMediaError("publish media registry graph", err)
		}
	}
	return mediaregistry.ErrRevisionConflict
}

func wrapLifecycleMediaError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mediaregistry.ErrArtifactConflict) || errors.Is(err, mediaregistry.ErrSafeMode) {
		return fmt.Errorf("%w: %s: %v", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}
