package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
)

func (b *PostgresLifecycleBoundaryRegistries) EditorRegistry() *editorregistry.Registry {
	if b == nil {
		return nil
	}
	return b.editor
}

// buildLifecycleEditorPublication freezes Manifest.editor into one exact Editor
// Registry publication. Prebuilt L2 modules remain package-digest-bound; the
// Editor Registry never stores document bodies.
func buildLifecycleEditorPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (*editorregistry.Publication, error) {
	if len(extension.Manifest.Editor) == 0 {
		return nil, nil
	}
	if validateExactEditorPublicationArtifact(extension, binding) != nil {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	publication := editorregistry.Publication{Artifact: editorregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: binding.RuntimeInstanceID,
	}}
	publication.Editor = make([]editorregistry.Declaration, 0, len(extension.Manifest.Editor))
	for _, declaration := range extension.Manifest.Editor {
		publication.Editor = append(publication.Editor, editorregistry.Declaration{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Kind: declaration.Kind, Schema: declaration.Schema,
			ExtensionName: declaration.ExtensionName, L2Module: declaration.L2Module,
			L2Digest: declaration.L2Digest, CommandKey: declaration.CommandKey,
			CommandID: declaration.CommandID, Label: declaration.Label,
			Icon: declaration.Icon, Group: declaration.Group, Order: declaration.Order,
			Priority: declaration.Priority, Permission: declaration.Permission,
		})
	}
	probe := editorregistry.New()
	if _, err := probe.ReplaceAllIfRevision(0, []editorregistry.Publication{publication}, false); err != nil {
		return nil, fmt.Errorf("build editor registry publication: %w", err)
	}
	frozen, found := probe.SnapshotPublication(extension.ID)
	if !found {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	return &frozen, nil
}

func validateExactEditorPublicationArtifact(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) error {
	// Editor surfaces are package-local frontend modules; no backend runtime is
	// required to freeze or restore the declaration graph.
	if extension.ID == "" || extension.Version == "" || extension.ActiveVersionID <= 0 ||
		extension.ID != strings.TrimSpace(extension.ID) || extension.Version != strings.TrimSpace(extension.Version) ||
		!validLifecycleCleanupDigest(extension.PackageDigest) ||
		extension.Type != extensions.TypePlugin || extension.Manifest.ID != extension.ID ||
		extension.Manifest.Version != extension.Version || extension.Manifest.Type != extensions.TypePlugin ||
		validateExactCoordinatorBinding("editor registry", binding, extension, false) != nil {
		return ErrLifecycleRegistryPublicationInvalid
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeEditorMaterials(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	source, target *lifecycleRegistryMaterial,
) error {
	hasSource := source != nil && len(source.extension.Manifest.Editor) > 0
	hasTarget := target != nil && len(target.extension.Manifest.Editor) > 0
	if !hasSource && !hasTarget {
		return nil
	}
	if b == nil || b.editor == nil || b.assetAuthority == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if hasSource {
		if _, err := b.assetAuthority.RestoreImpactDigest(ctx, source.extension); err != nil {
			return fmt.Errorf("freeze source editor authority: %w", err)
		}
		if err := b.freezeEditorMaterial(source); err != nil {
			return err
		}
	}
	if hasTarget {
		if _, err := b.assetAuthority.OperationImpactDigest(ctx, request.OperationID, target.extension); err != nil {
			return fmt.Errorf("freeze target editor authority: %w", err)
		}
		if err := b.freezeEditorMaterial(target); err != nil {
			return err
		}
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeEditorMaterial(
	material *lifecycleRegistryMaterial,
) error {
	publication, err := buildLifecycleEditorPublication(material.extension, material.binding)
	if err != nil {
		return err
	}
	material.editorPublication = publication
	return refreshLifecycleRegistryMaterialDigest(material)
}

func (b *PostgresLifecycleBoundaryRegistries) restoreEditorPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	// Editor Registry 未接线时跳过：兼容尚未注入 P10 Editor 的旧边界测试。
	if b.editor == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := b.editor.Snapshot()
	publications := coreLifecycleEditorPublications(snapshot.Publications)
	if safeMode {
		if _, err := b.editor.ReplaceAllIfRevision(snapshot.Revision, publications, true); err != nil {
			return wrapLifecycleEditorError("restore editor registry safe mode", err)
		}
		return nil
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled || len(item.Manifest.Editor) == 0 {
			continue
		}
		if b.assetAuthority == nil {
			return ErrLifecycleRegistryPublicationUnavailable
		}
		if _, err := b.assetAuthority.RestoreImpactDigest(ctx, item); errors.Is(err, extensions.ErrTrustGrantNotFound) ||
			errors.Is(err, extensions.ErrLifecycleAuthorityNotFound) {
			continue
		} else if err != nil {
			return fmt.Errorf("restore editor authority for %s: %w", item.ID, err)
		}
		// Editor L2 is package-served; RuntimeInstanceID stays empty unless a
		// coordinator binding supplies one for mixed packages.
		publication, err := buildLifecycleEditorPublication(item, extensions.LifecycleRuntimeBinding{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			VersionID: item.ActiveVersionID,
		})
		if err != nil {
			return fmt.Errorf("restore editor registry for %s: %w", item.ID, err)
		}
		if publication != nil {
			publications = append(publications, *publication)
		}
	}
	if _, err := b.editor.ReplaceAllIfRevision(snapshot.Revision, publications, false); err != nil {
		return wrapLifecycleEditorError("restore editor registry publication", err)
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) RestoreEditorPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	return b.restoreEditorPublications(ctx, items, safeMode)
}

func coreLifecycleEditorPublications(input []editorregistry.Publication) []editorregistry.Publication {
	result := make([]editorregistry.Publication, 0, len(input))
	for _, publication := range input {
		if publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	return result
}

func (b *PostgresLifecycleBoundaryRegistries) validateEditorTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	hasEditor := (source != nil && source.editorPublication != nil) ||
		(target != nil && target.editorPublication != nil)
	if !hasEditor {
		return nil
	}
	if b == nil || b.editor == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	snapshot := b.editor.Snapshot()
	for _, desired := range []*lifecycleRegistryMaterial{source, target} {
		var publication *editorregistry.Publication
		if desired != nil {
			publication = desired.editorPublication
		}
		graph, err := lifecycleEditorGraph(snapshot, extensionID, publication, source, target)
		if err != nil {
			return err
		}
		if _, err := editorregistry.New().ReplaceAllIfRevision(0, graph, snapshot.SafeMode); err != nil {
			return wrapLifecycleEditorError("validate editor registry publication", err)
		}
	}
	return nil
}

func lifecycleEditorGraph(
	snapshot editorregistry.Snapshot,
	extensionID string,
	desired *editorregistry.Publication,
	allowedMaterials ...*lifecycleRegistryMaterial,
) ([]editorregistry.Publication, error) {
	allowed := make(map[editorregistry.Artifact]editorregistry.Publication, len(allowedMaterials))
	for _, material := range allowedMaterials {
		if material == nil || material.editorPublication == nil {
			continue
		}
		artifact := material.editorPublication.Artifact
		if existing, found := allowed[artifact]; found && !reflect.DeepEqual(existing, *material.editorPublication) {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
		allowed[artifact] = *material.editorPublication
	}
	publications := make([]editorregistry.Publication, 0, len(snapshot.Publications)+1)
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

func (b *PostgresLifecycleBoundaryRegistries) reconcileEditor(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	hasEditor := (source != nil && source.editorPublication != nil) ||
		(target != nil && target.editorPublication != nil) ||
		(desired != nil && desired.editorPublication != nil)
	if !hasEditor {
		return nil
	}
	if b == nil || b.editor == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	var desiredPublication *editorregistry.Publication
	if desired != nil {
		desiredPublication = desired.editorPublication
	}
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := b.editor.Snapshot()
		graph, err := lifecycleEditorGraph(snapshot, extensionID, desiredPublication, source, target)
		if err != nil {
			return err
		}
		if _, err := b.editor.ReplaceAllIfRevision(snapshot.Revision, graph, snapshot.SafeMode); err == nil {
			return nil
		} else if !errors.Is(err, editorregistry.ErrRevisionConflict) {
			return wrapLifecycleEditorError("publish editor registry graph", err)
		}
	}
	return editorregistry.ErrRevisionConflict
}

func wrapLifecycleEditorError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, editorregistry.ErrArtifactConflict) || errors.Is(err, editorregistry.ErrSafeMode) {
		return fmt.Errorf("%w: %s: %v", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

type runtimeEditorMutation struct {
	mu       sync.Mutex
	registry *editorregistry.Registry
	before   *editorregistry.Publication
	after    *editorregistry.Publication
	done     bool
}

func (m *runtimeEditorMutation) Rollback() error {
	if m == nil || m.registry == nil {
		return extensions.ErrRuntimeEditorPublicationUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return nil
	}
	var after *editorregistry.Artifact
	if m.after != nil {
		after = &m.after.Artifact
	}
	if _, err := restoreRuntimeEditorPublication(m.registry, after, m.before); err != nil {
		return err
	}
	m.done = true
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) PublishRuntimeEditor(
	ctx context.Context,
	extension extensions.Extension,
) (extensions.RuntimeEditorPublicationMutation, error) {
	if b == nil || b.editor == nil || ctx == nil || len(extension.Manifest.Editor) == 0 {
		return nil, extensions.ErrRuntimeEditorPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	desired, err := buildLifecycleEditorPublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
	})
	if err != nil || desired == nil {
		return nil, errors.Join(extensions.ErrRuntimeEditorPublicationUnavailable, err)
	}
	mutation := &runtimeEditorMutation{registry: b.editor, after: desired}
	current, found := b.editor.SnapshotPublication(extension.ID)
	if found {
		if !runtimeEditorArtifactCanMoveForward(current.Artifact, desired.Artifact) {
			return nil, fmt.Errorf("%w: newer editor artifact is already active", ErrLifecycleRegistryPublicationConflict)
		}
		previous := current
		mutation.before = &previous
		if current.Artifact == desired.Artifact {
			mutation.before = nil
			_, err = b.editor.Publish(*desired)
		} else {
			_, err = b.editor.PublishIfArtifact(current.Artifact, *desired)
		}
	} else {
		_, err = b.editor.Publish(*desired)
	}
	if err != nil {
		return nil, fmt.Errorf("publish exact runtime editor: %w", err)
	}
	return mutation, nil
}

func (b *PostgresLifecycleBoundaryRegistries) QuarantineRuntimeEditor(
	ctx context.Context,
	extension extensions.Extension,
) (extensions.RuntimeEditorPublicationMutation, error) {
	if b == nil || b.editor == nil || ctx == nil || len(extension.Manifest.Editor) == 0 {
		return nil, extensions.ErrRuntimeEditorPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mutation := &runtimeEditorMutation{registry: b.editor}
	current, found := b.editor.SnapshotPublication(extension.ID)
	if !found {
		return mutation, nil
	}
	if !runtimeEditorArtifactBelongsToExtension(current.Artifact, extension) {
		return nil, fmt.Errorf("%w: stale disable cannot remove active editor artifact", ErrLifecycleRegistryPublicationConflict)
	}
	previous := current
	mutation.before = &previous
	if _, removed, err := b.editor.Remove(current.Artifact); err != nil || !removed {
		_ = mutation.Rollback()
		return nil, errors.Join(extensions.ErrRuntimeEditorPublicationUnavailable, err)
	}
	return mutation, nil
}

func runtimeEditorArtifactCanMoveForward(current, desired editorregistry.Artifact) bool {
	if current.ExtensionID != desired.ExtensionID || current.Core || desired.Core || current.VersionID > desired.VersionID {
		return false
	}
	return current.VersionID < desired.VersionID ||
		(current.ExtensionVersion == desired.ExtensionVersion && current.PackageDigest == desired.PackageDigest)
}

func runtimeEditorArtifactBelongsToExtension(artifact editorregistry.Artifact, extension extensions.Extension) bool {
	if artifact.Core || artifact.ExtensionID != extension.ID || artifact.VersionID > extension.ActiveVersionID {
		return false
	}
	return artifact.VersionID < extension.ActiveVersionID ||
		(artifact.ExtensionVersion == extension.Version && artifact.PackageDigest == extension.PackageDigest)
}

func restoreRuntimeEditorPublication(
	registry *editorregistry.Registry,
	after *editorregistry.Artifact,
	before *editorregistry.Publication,
) (bool, error) {
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
	return false, editorregistry.ErrArtifactConflict
}
