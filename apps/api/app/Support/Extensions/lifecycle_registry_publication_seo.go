package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

func (b *PostgresLifecycleBoundaryRegistries) SEORegistry() *seoregistry.Registry {
	if b == nil {
		return nil
	}
	return b.seo
}

func buildLifecycleSEOPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
	impactDigest string,
) (*seoregistry.Publication, error) {
	if len(extension.Manifest.SEO) == 0 {
		return nil, nil
	}
	if validateExactSEOPublicationArtifact(extension, binding, impactDigest) != nil {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	publication := seoregistry.Publication{Artifact: seoregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: impactDigest,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: binding.RuntimeInstanceID,
	}}
	publication.Contributions = make([]seoregistry.Declaration, 0, len(extension.Manifest.SEO))
	for _, declaration := range extension.Manifest.SEO {
		publication.Contributions = append(publication.Contributions, seoregistry.Declaration{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Scope: declaration.Scope, Kind: declaration.Kind, Action: declaration.Action,
			Handler: declaration.Handler, Priority: declaration.Priority,
			FailurePolicy: declaration.FailurePolicy,
			Timeout:       time.Duration(declaration.TimeoutMS) * time.Millisecond,
		})
	}
	// Freeze the canonical declaration order and defaulted timeout into the
	// durable lifecycle digest instead of trusting caller-owned Manifest slices.
	probe := seoregistry.New()
	if _, err := probe.ReplaceAllIfRevision(0, []seoregistry.Publication{publication}, false); err != nil {
		return nil, fmt.Errorf("build SEO registry publication: %w", err)
	}
	frozen, found := probe.SnapshotPublication(extension.ID)
	if !found {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	return &frozen, nil
}

func validateExactSEOPublicationArtifact(
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
		validateExactCoordinatorBinding("seo registry", binding, extension, true) != nil {
		return ErrLifecycleRegistryPublicationInvalid
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeSEOMaterials(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	source, target *lifecycleRegistryMaterial,
) error {
	hasSource := source != nil && len(source.extension.Manifest.SEO) > 0
	hasTarget := target != nil && len(target.extension.Manifest.SEO) > 0
	if !hasSource && !hasTarget {
		return nil
	}
	if b == nil || b.seo == nil || b.assetAuthority == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if hasSource {
		impact, err := b.assetAuthority.RestoreImpactDigest(ctx, source.extension)
		if err != nil {
			return fmt.Errorf("freeze source SEO authority: %w", err)
		}
		if err := b.freezeSEOMaterial(source, impact); err != nil {
			return err
		}
	}
	if hasTarget {
		impact, err := b.assetAuthority.OperationImpactDigest(ctx, request.OperationID, target.extension)
		if err != nil {
			return fmt.Errorf("freeze target SEO authority: %w", err)
		}
		if err := b.freezeSEOMaterial(target, impact); err != nil {
			return err
		}
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeSEOMaterial(
	material *lifecycleRegistryMaterial,
	impactDigest string,
) error {
	publication, err := buildLifecycleSEOPublication(material.extension, material.binding, impactDigest)
	if err != nil {
		return err
	}
	material.seoPublication = publication
	return refreshLifecycleRegistryMaterialDigest(material)
}

func (b *PostgresLifecycleBoundaryRegistries) restoreSEOPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || b.seo == nil || b.manager == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := b.seo.Snapshot()
	publications := coreLifecycleSEOPublications(snapshot.Publications)
	if safeMode {
		if _, err := b.seo.ReplaceAllIfRevision(snapshot.Revision, publications, true); err != nil {
			return wrapLifecycleSEOError("restore SEO registry safe mode", err)
		}
		return nil
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled || len(item.Manifest.SEO) == 0 {
			continue
		}
		if b.assetAuthority == nil {
			return ErrLifecycleRegistryPublicationUnavailable
		}
		impact, err := b.assetAuthority.RestoreImpactDigest(ctx, item)
		if errors.Is(err, extensions.ErrTrustGrantNotFound) || errors.Is(err, extensions.ErrLifecycleAuthorityNotFound) {
			// A revoked or never-confirmed executable package stays closed on boot.
			continue
		}
		if err != nil {
			return fmt.Errorf("restore SEO authority for %s: %w", item.ID, err)
		}
		runtime, err := b.manager.ActiveRuntimeInstance(item.ID)
		if err != nil {
			// Failed and boot-loop-suppressed processes cannot regain declarations.
			continue
		}
		if !runtimeInstanceMatchesExtension(runtime, item) || !b.manager.RuntimeInstanceAvailable(runtime.Identity) {
			return fmt.Errorf("%w: startup SEO runtime for %s is not exact and available",
				ErrLifecycleRegistryPublicationConflict, item.ID)
		}
		publication, err := buildLifecycleSEOPublication(item, extensions.LifecycleRuntimeBinding{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			VersionID: item.ActiveVersionID, RuntimeInstanceID: runtime.Identity.InstanceID,
		}, impact)
		if err != nil {
			return fmt.Errorf("restore SEO registry for %s: %w", item.ID, err)
		}
		if publication != nil {
			publications = append(publications, *publication)
		}
	}
	if _, err := b.seo.ReplaceAllIfRevision(snapshot.Revision, publications, false); err != nil {
		return wrapLifecycleSEOError("restore SEO registry publication", err)
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) RestoreSEOPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	return b.restoreSEOPublications(ctx, items, safeMode)
}

func coreLifecycleSEOPublications(input []seoregistry.Publication) []seoregistry.Publication {
	result := make([]seoregistry.Publication, 0, len(input))
	for _, publication := range input {
		if publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	return result
}

func (b *PostgresLifecycleBoundaryRegistries) validateSEOTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	if b == nil || b.seo == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	snapshot := b.seo.Snapshot()
	for _, desired := range []*lifecycleRegistryMaterial{source, target} {
		var publication *seoregistry.Publication
		if desired != nil {
			publication = desired.seoPublication
		}
		graph, err := lifecycleSEOGraph(snapshot, extensionID, publication, source, target)
		if err != nil {
			return err
		}
		if _, err := seoregistry.New().ReplaceAllIfRevision(0, graph, snapshot.SafeMode); err != nil {
			return wrapLifecycleSEOError("validate SEO registry publication", err)
		}
	}
	return nil
}

func lifecycleSEOGraph(
	snapshot seoregistry.Snapshot,
	extensionID string,
	desired *seoregistry.Publication,
	allowedMaterials ...*lifecycleRegistryMaterial,
) ([]seoregistry.Publication, error) {
	allowed := make(map[seoregistry.Artifact]seoregistry.Publication, len(allowedMaterials))
	for _, material := range allowedMaterials {
		if material == nil || material.seoPublication == nil {
			continue
		}
		artifact := material.seoPublication.Artifact
		if existing, found := allowed[artifact]; found && !reflect.DeepEqual(existing, *material.seoPublication) {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
		allowed[artifact] = *material.seoPublication
	}
	publications := make([]seoregistry.Publication, 0, len(snapshot.Publications)+1)
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

func (b *PostgresLifecycleBoundaryRegistries) reconcileSEO(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	if b == nil || b.seo == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	var desiredPublication *seoregistry.Publication
	if desired != nil {
		desiredPublication = desired.seoPublication
	}
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := b.seo.Snapshot()
		graph, err := lifecycleSEOGraph(snapshot, extensionID, desiredPublication, source, target)
		if err != nil {
			return err
		}
		if _, err := b.seo.ReplaceAllIfRevision(snapshot.Revision, graph, snapshot.SafeMode); err == nil {
			return nil
		} else if !errors.Is(err, seoregistry.ErrRevisionConflict) {
			return wrapLifecycleSEOError("publish SEO registry graph", err)
		}
	}
	return seoregistry.ErrRevisionConflict
}

func wrapLifecycleSEOError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, seoregistry.ErrArtifactConflict) || errors.Is(err, seoregistry.ErrSafeMode) {
		return fmt.Errorf("%w: %s: %v", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}
