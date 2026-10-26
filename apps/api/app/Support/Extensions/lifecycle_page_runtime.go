package extensionsruntime

import (
	"context"
	"errors"
	"fmt"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

func buildExactPluginPageRuntime(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
	contributions []pages.PageContribution,
	siteName string,
	locales []string,
) (*pages.ThemeRuntimeSnapshot, error) {
	templates := make([]pages.RuntimeTemplateDeclaration, len(extension.Manifest.Templates))
	for index, item := range extension.Manifest.Templates {
		templates[index] = pages.RuntimeTemplateDeclaration{
			ID: item.ID, ContractVersion: item.ContractVersion, Action: item.Action,
			TargetID: item.TargetID, Path: item.Path, Digest: item.Digest,
			ViewModelSchema: item.ViewModelSchema, ThemeOverrideKey: item.ThemeOverrideKey,
		}
	}
	schemas := make([]pages.RuntimeDataSchemaDeclaration, 0, len(extension.Manifest.PackageFiles))
	for _, file := range extension.Manifest.PackageFiles {
		if file.Kind != "schema" {
			continue
		}
		schemas = append(schemas, pages.RuntimeDataSchemaDeclaration{
			ID: file.ID, Version: file.Version, Path: file.Path, Digest: file.Digest,
		})
	}
	snapshot, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: pages.RuntimeArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, RuntimeInstanceID: binding.RuntimeInstanceID,
		},
		PackageRoot: extensions.PackageContentRoot(extension), Contributions: contributions,
		Templates: templates, DataSchemas: schemas, PackageKind: pages.RuntimeTemplatePlugin,
		RequireDeclaredTemplates: extensionmanifest.EffectiveManifestVersion(extension.Manifest) >= extensionmanifest.ManifestVersionV3,
		SiteName:                 siteName, Locales: append([]string(nil), locales...),
	})
	if errors.Is(err, pages.ErrThemeRuntimeMissing) {
		return nil, nil
	}
	return snapshot, err
}

func (b *PostgresLifecycleBoundaryRegistries) removeSupersededPageRuntimes(
	desired *lifecycleRegistryMaterial,
	materials ...*lifecycleRegistryMaterial,
) error {
	if b == nil || b.themeRuntime == nil {
		return nil
	}
	var keep pages.RuntimeArtifact
	if desired != nil {
		keep = pages.RuntimeArtifact{
			ExtensionID: desired.extension.ID, ExtensionVersion: desired.extension.Version,
			PackageDigest: desired.extension.PackageDigest, RuntimeInstanceID: desired.binding.RuntimeInstanceID,
		}
	}
	for _, material := range materials {
		if material == nil {
			continue
		}
		artifact := pages.RuntimeArtifact{
			ExtensionID: material.extension.ID, ExtensionVersion: material.extension.Version,
			PackageDigest: material.extension.PackageDigest, RuntimeInstanceID: material.binding.RuntimeInstanceID,
		}
		if artifact == keep {
			continue
		}
		if _, err := b.themeRuntime.RemoveExact(artifact); err != nil && !errors.Is(err, pages.ErrThemeRuntimeConflict) {
			return err
		}
	}
	return nil
}

func pageArtifactAllowed(artifact pages.RuntimeArtifact, materials ...*lifecycleRegistryMaterial) bool {
	for _, material := range materials {
		if material == nil {
			continue
		}
		if artifact.ExtensionID == material.extension.ID &&
			artifact.ExtensionVersion == material.extension.Version &&
			artifact.PackageDigest == material.extension.PackageDigest &&
			artifact.RuntimeInstanceID == material.binding.RuntimeInstanceID {
			return true
		}
	}
	return false
}

func (b *PostgresLifecycleBoundaryRegistries) reconcilePages(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	var stagedRuntime *pages.ThemeRuntimeSnapshot
	staged := false
	if desired != nil && b.themeRuntime != nil {
		runtimeSnapshot, err := buildExactPluginPageRuntime(
			desired.extension, desired.binding, desired.pages, b.pageSiteName, b.pageLocales,
		)
		if err != nil {
			return fmt.Errorf("build exact plugin page runtime: %w", err)
		}
		if runtimeSnapshot != nil {
			if _, staged, err = b.themeRuntime.Stage(runtimeSnapshot); err != nil {
				return fmt.Errorf("stage exact plugin page runtime: %w", err)
			}
			stagedRuntime = runtimeSnapshot
		}
	}
	rollbackStaged := func(cause error) error {
		if !staged || stagedRuntime == nil || b.themeRuntime == nil {
			return cause
		}
		_, removeErr := b.themeRuntime.RemoveExact(stagedRuntime.Artifact())
		return errors.Join(cause, removeErr)
	}
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return rollbackStaged(err)
		}
		snapshot, exists := b.pages.ExtensionSnapshot(extensionID)
		if exists && !pageArtifactAllowed(snapshot.Artifact, source, target) {
			return rollbackStaged(fmt.Errorf(
				"%w: active page artifact %s@%s digest=%s runtime=%s is outside the lifecycle source/target fence",
				ErrLifecycleRegistryPublicationConflict,
				snapshot.Artifact.ExtensionID,
				snapshot.Artifact.ExtensionVersion,
				snapshot.Artifact.PackageDigest,
				snapshot.Artifact.RuntimeInstanceID,
			))
		}
		if desired != nil {
			artifact := pages.RuntimeArtifact{
				ExtensionID: desired.extension.ID, ExtensionVersion: desired.extension.Version,
				PackageDigest: desired.extension.PackageDigest, RuntimeInstanceID: desired.binding.RuntimeInstanceID,
			}
			if _, err := b.pages.PublishExtensionIfRevision(artifact, desired.pages, snapshot.Revision); err == nil {
				return b.removeSupersededPageRuntimes(desired, source, target)
			} else if !errors.Is(err, pages.ErrRevisionConflict) {
				return rollbackStaged(err)
			}
			continue
		}
		if !exists {
			return b.removeSupersededPageRuntimes(nil, source, target)
		}
		if _, err := b.pages.RemoveExtensionIfRevision(extensionID, snapshot.Artifact, snapshot.Revision); err == nil {
			return b.removeSupersededPageRuntimes(nil, source, target)
		} else if !errors.Is(err, pages.ErrRevisionConflict) {
			return rollbackStaged(err)
		}
	}
	return rollbackStaged(pages.ErrRevisionConflict)
}
