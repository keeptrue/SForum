package extensionsruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func (b *PostgresLifecycleBoundaryRegistries) ContentRegistry() *contentregistry.Registry {
	if b == nil {
		return nil
	}
	return b.content
}

// buildLifecycleContentPublication freezes Manifest.content into one exact
// Content Registry publication. Impact digest is verified by freeze callers for
// trust authority; the Content artifact itself does not carry ImpactDigest.
func buildLifecycleContentPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (*contentregistry.Publication, error) {
	if len(extension.Manifest.Content) == 0 {
		return nil, nil
	}
	if validateExactContentPublicationArtifact(extension, binding) != nil {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	publication := contentregistry.Publication{Artifact: contentregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: binding.RuntimeInstanceID,
	}}
	publication.Content = make([]contentregistry.Declaration, 0, len(extension.Manifest.Content))
	for _, declaration := range extension.Manifest.Content {
		publication.Content = append(publication.Content, contentregistry.Declaration{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Kind: declaration.Kind, Handler: declaration.Handler,
			Schema: declaration.Schema, Renderer: declaration.Renderer,
			Migration: declaration.Migration,
		})
	}
	// 用探针 Registry 规范化顺序与边界，再冻结进 durable lifecycle digest。
	probe := contentregistry.New()
	if _, err := probe.ReplaceAllIfRevision(0, []contentregistry.Publication{publication}, false); err != nil {
		return nil, fmt.Errorf("build content registry publication: %w", err)
	}
	frozen, found := probe.SnapshotPublication(extension.ID)
	if !found {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	return &frozen, nil
}

func validateExactContentPublicationArtifact(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) error {
	// Content 声明可带 handler 或纯 renderer；带 handler 时要求 exact runtime。
	requiresRuntime := false
	for _, declaration := range extension.Manifest.Content {
		if strings.TrimSpace(declaration.Handler) != "" {
			requiresRuntime = true
			break
		}
	}
	if extension.ID == "" || extension.Version == "" || extension.ActiveVersionID <= 0 ||
		extension.ID != strings.TrimSpace(extension.ID) || extension.Version != strings.TrimSpace(extension.Version) ||
		!validLifecycleCleanupDigest(extension.PackageDigest) ||
		extension.Type != extensions.TypePlugin || extension.Manifest.ID != extension.ID ||
		extension.Manifest.Version != extension.Version || extension.Manifest.Type != extensions.TypePlugin ||
		validateExactCoordinatorBinding("content registry", binding, extension, requiresRuntime) != nil {
		return ErrLifecycleRegistryPublicationInvalid
	}
	if requiresRuntime {
		if extension.Manifest.Backend.ProtocolVersion != 2 || strings.TrimSpace(extension.Manifest.Backend.Entry) == "" {
			return ErrLifecycleRegistryPublicationInvalid
		}
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeContentMaterials(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	source, target *lifecycleRegistryMaterial,
) error {
	hasSource := source != nil && len(source.extension.Manifest.Content) > 0
	hasTarget := target != nil && len(target.extension.Manifest.Content) > 0
	if !hasSource && !hasTarget {
		return nil
	}
	if b == nil || b.content == nil || b.assetAuthority == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	// Impact 校验与 SEO/Navigation 一致：source 用 restore，target 用 operation。
	// 不写入 Content Artifact（该 Registry 无 ImpactDigest 字段）。
	if hasSource {
		if _, err := b.assetAuthority.RestoreImpactDigest(ctx, source.extension); err != nil {
			return fmt.Errorf("freeze source content authority: %w", err)
		}
		if err := b.freezeContentMaterial(source); err != nil {
			return err
		}
	}
	if hasTarget {
		if _, err := b.assetAuthority.OperationImpactDigest(ctx, request.OperationID, target.extension); err != nil {
			return fmt.Errorf("freeze target content authority: %w", err)
		}
		if err := b.freezeContentMaterial(target); err != nil {
			return err
		}
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeContentMaterial(
	material *lifecycleRegistryMaterial,
) error {
	publication, err := buildLifecycleContentPublication(material.extension, material.binding)
	if err != nil {
		return err
	}
	material.contentPublication = publication
	return refreshLifecycleRegistryMaterialDigest(material)
}

func (b *PostgresLifecycleBoundaryRegistries) restoreContentPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	// Content Registry 未接线时跳过：兼容尚未注入 P10 Content 的旧边界测试。
	if b.content == nil {
		return nil
	}
	if b.manager == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := b.content.Snapshot()
	publications := coreLifecycleContentPublications(snapshot.Publications)
	if safeMode {
		if _, err := b.content.ReplaceAllIfRevision(snapshot.Revision, publications, true); err != nil {
			return wrapLifecycleContentError("restore content registry safe mode", err)
		}
		return nil
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled || len(item.Manifest.Content) == 0 {
			continue
		}
		if b.assetAuthority == nil {
			return ErrLifecycleRegistryPublicationUnavailable
		}
		if _, err := b.assetAuthority.RestoreImpactDigest(ctx, item); errors.Is(err, extensions.ErrTrustGrantNotFound) ||
			errors.Is(err, extensions.ErrLifecycleAuthorityNotFound) {
			// 未确认/已撤销可执行包在启动路径保持关闭。
			continue
		} else if err != nil {
			return fmt.Errorf("restore content authority for %s: %w", item.ID, err)
		}
		runtime, err := b.manager.ActiveRuntimeInstance(item.ID)
		if err != nil {
			// 失败或 boot-loop 抑制的进程不得恢复声明。
			// 纯 renderer 内容仍要求进程可用时走同一路径（与 SEO 对齐）。
			continue
		}
		if !runtimeInstanceMatchesExtension(runtime, item) || !b.manager.RuntimeInstanceAvailable(runtime.Identity) {
			return fmt.Errorf("%w: startup content runtime for %s is not exact and available",
				ErrLifecycleRegistryPublicationConflict, item.ID)
		}
		publication, err := buildLifecycleContentPublication(item, extensions.LifecycleRuntimeBinding{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			VersionID: item.ActiveVersionID, RuntimeInstanceID: runtime.Identity.InstanceID,
		})
		if err != nil {
			return fmt.Errorf("restore content registry for %s: %w", item.ID, err)
		}
		if publication != nil {
			publications = append(publications, *publication)
		}
	}
	if _, err := b.content.ReplaceAllIfRevision(snapshot.Revision, publications, false); err != nil {
		return wrapLifecycleContentError("restore content registry publication", err)
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) RestoreContentPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	return b.restoreContentPublications(ctx, items, safeMode)
}

func coreLifecycleContentPublications(input []contentregistry.Publication) []contentregistry.Publication {
	result := make([]contentregistry.Publication, 0, len(input))
	for _, publication := range input {
		if publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	return result
}

func (b *PostgresLifecycleBoundaryRegistries) validateContentTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	hasContent := (source != nil && source.contentPublication != nil) ||
		(target != nil && target.contentPublication != nil)
	if !hasContent {
		return nil
	}
	if b == nil || b.content == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	snapshot := b.content.Snapshot()
	for _, desired := range []*lifecycleRegistryMaterial{source, target} {
		var publication *contentregistry.Publication
		if desired != nil {
			publication = desired.contentPublication
		}
		graph, err := lifecycleContentGraph(snapshot, extensionID, publication, source, target)
		if err != nil {
			return err
		}
		if _, err := contentregistry.New().ReplaceAllIfRevision(0, graph, snapshot.SafeMode); err != nil {
			return wrapLifecycleContentError("validate content registry publication", err)
		}
	}
	return nil
}

func lifecycleContentGraph(
	snapshot contentregistry.Snapshot,
	extensionID string,
	desired *contentregistry.Publication,
	allowedMaterials ...*lifecycleRegistryMaterial,
) ([]contentregistry.Publication, error) {
	allowed := make(map[contentregistry.Artifact]contentregistry.Publication, len(allowedMaterials))
	for _, material := range allowedMaterials {
		if material == nil || material.contentPublication == nil {
			continue
		}
		artifact := material.contentPublication.Artifact
		if existing, found := allowed[artifact]; found && !reflect.DeepEqual(existing, *material.contentPublication) {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
		allowed[artifact] = *material.contentPublication
	}
	publications := make([]contentregistry.Publication, 0, len(snapshot.Publications)+1)
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

func (b *PostgresLifecycleBoundaryRegistries) reconcileContent(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	hasContent := (source != nil && source.contentPublication != nil) ||
		(target != nil && target.contentPublication != nil) ||
		(desired != nil && desired.contentPublication != nil)
	if !hasContent {
		return nil
	}
	if b == nil || b.content == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	var desiredPublication *contentregistry.Publication
	if desired != nil {
		desiredPublication = desired.contentPublication
	}
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := b.content.Snapshot()
		graph, err := lifecycleContentGraph(snapshot, extensionID, desiredPublication, source, target)
		if err != nil {
			return err
		}
		if _, err := b.content.ReplaceAllIfRevision(snapshot.Revision, graph, snapshot.SafeMode); err == nil {
			return nil
		} else if !errors.Is(err, contentregistry.ErrRevisionConflict) {
			return wrapLifecycleContentError("publish content registry graph", err)
		}
	}
	return contentregistry.ErrRevisionConflict
}

func wrapLifecycleContentError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, contentregistry.ErrArtifactConflict) || errors.Is(err, contentregistry.ErrSafeMode) {
		return fmt.Errorf("%w: %s: %v", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

type runtimeContentMutation struct {
	mu             sync.Mutex
	registry       *contentregistry.Registry
	manager        *Manager
	before         *contentregistry.Publication
	after          *contentregistry.Publication
	resumeIdentity RuntimeInstanceIdentity
	resumeOwned    bool
	done           bool
}

func (m *runtimeContentMutation) Rollback() error {
	if m == nil || m.registry == nil {
		return extensions.ErrRuntimeContentPublicationUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return nil
	}
	var after *contentregistry.Artifact
	if m.after != nil {
		after = &m.after.Artifact
	}
	if _, err := restoreRuntimeContentPublication(m.registry, after, m.before); err != nil {
		return err
	}
	if m.resumeOwned {
		if _, err := m.manager.ResumeRuntimeInstance(m.resumeIdentity); err != nil {
			return err
		}
	}
	m.done = true
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) PublishRuntimeContent(
	ctx context.Context,
	extension extensions.Extension,
) (extensions.RuntimeContentPublicationMutation, error) {
	if b == nil || b.manager == nil || b.content == nil || ctx == nil || len(extension.Manifest.Content) == 0 {
		return nil, extensions.ErrRuntimeContentPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime, err := b.manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || !runtimeInstanceMatchesExtension(runtime, extension) ||
		!b.manager.RuntimeInstanceAvailable(runtime.Identity) {
		return nil, errors.Join(extensions.ErrRuntimeContentPublicationUnavailable, err)
	}
	desired, err := buildLifecycleContentPublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	})
	if err != nil || desired == nil {
		return nil, errors.Join(extensions.ErrRuntimeContentPublicationUnavailable, err)
	}
	mutation := &runtimeContentMutation{registry: b.content, manager: b.manager, after: desired}
	current, found := b.content.SnapshotPublication(extension.ID)
	if found {
		if !runtimeContentArtifactCanMoveForward(current.Artifact, desired.Artifact) {
			return nil, fmt.Errorf("%w: newer content artifact is already active", ErrLifecycleRegistryPublicationConflict)
		}
		previous := current
		mutation.before = &previous
		if current.Artifact == desired.Artifact {
			mutation.before = nil
			_, err = b.content.Publish(*desired)
		} else {
			_, err = b.content.PublishIfArtifact(current.Artifact, *desired)
		}
	} else {
		_, err = b.content.Publish(*desired)
	}
	if err != nil {
		return nil, fmt.Errorf("publish exact runtime content: %w", err)
	}
	return mutation, nil
}

func (b *PostgresLifecycleBoundaryRegistries) QuarantineRuntimeContent(
	ctx context.Context,
	extension extensions.Extension,
) (extensions.RuntimeContentPublicationMutation, error) {
	if b == nil || b.manager == nil || b.content == nil || ctx == nil || len(extension.Manifest.Content) == 0 {
		return nil, extensions.ErrRuntimeContentPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mutation := &runtimeContentMutation{registry: b.content, manager: b.manager}
	current, found := b.content.SnapshotPublication(extension.ID)
	if found {
		if !runtimeContentArtifactBelongsToExtension(current.Artifact, extension) {
			return nil, fmt.Errorf("%w: stale disable cannot remove active content artifact", ErrLifecycleRegistryPublicationConflict)
		}
		previous := current
		mutation.before = &previous
	}
	runtime, runtimeErr := b.manager.ActiveRuntimeInstance(extension.ID)
	if runtimeErr == nil {
		if !runtimeInstanceMatchesExtension(runtime, extension) {
			return nil, fmt.Errorf("%w: active runtime does not match content artifact", ErrLifecycleRegistryPublicationConflict)
		}
		mutation.resumeIdentity = runtime.Identity
		if !runtime.Admission.Draining {
			if _, err := b.manager.BeginDrain(runtime.Identity); err != nil {
				return nil, err
			}
			mutation.resumeOwned = true
		}
	} else if !errors.Is(runtimeErr, ErrRuntimeInstanceNotFound) {
		return nil, runtimeErr
	}
	if mutation.before != nil {
		if _, removed, err := b.content.Remove(mutation.before.Artifact); err != nil || !removed {
			_ = mutation.Rollback()
			return nil, errors.Join(extensions.ErrRuntimeContentPublicationUnavailable, err)
		}
	}
	return mutation, nil
}

func runtimeContentArtifactCanMoveForward(current, desired contentregistry.Artifact) bool {
	if current.ExtensionID != desired.ExtensionID || current.Core || desired.Core || current.VersionID > desired.VersionID {
		return false
	}
	return current.VersionID < desired.VersionID ||
		(current.ExtensionVersion == desired.ExtensionVersion && current.PackageDigest == desired.PackageDigest)
}

func runtimeContentArtifactBelongsToExtension(artifact contentregistry.Artifact, extension extensions.Extension) bool {
	if artifact.Core || artifact.ExtensionID != extension.ID || artifact.VersionID > extension.ActiveVersionID {
		return false
	}
	return artifact.VersionID < extension.ActiveVersionID ||
		(artifact.ExtensionVersion == extension.Version && artifact.PackageDigest == extension.PackageDigest)
}

func restoreRuntimeContentPublication(
	registry *contentregistry.Registry,
	after *contentregistry.Artifact,
	before *contentregistry.Publication,
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
	return false, contentregistry.ErrArtifactConflict
}

const defaultContentRuntimeTimeout = 2 * time.Second

const (
	ProtocolV2ContentProviderSlot   = "sforum.content"
	ProtocolV2ContentRequestSchema  = "sforum.content.call.request@1"
	ProtocolV2ContentResponseSchema = "sforum.content.call.response@1"
)

type VersionedContentRequest struct {
	DeclarationID   string
	ContractVersion string
	Kind            string
	Handler         string
	Schema          string
	Renderer        string
	Migration       string
	Operation       string
	Timeout         time.Duration
	Input           map[string]any
	// ActorUserID is Host-internal delegation input. It is never serialized in
	// the content call document or RequestContext actor field. Zero is anonymous.
	ActorUserID int64
	// SuppressHostDelegations is Host-internal and never enters the content
	// document. Protected rendering sets it after Host authorization.
	SuppressHostDelegations bool
}

type VersionedContentResponse struct {
	Output map[string]any
}

type exactContentInstanceInvoker interface {
	InvokeContentInstance(
		context.Context,
		RuntimeInstanceIdentity,
		extensions.Extension,
		VersionedContentRequest,
	) (VersionedContentResponse, error)
}

// ContentRegistryProtocolRuntime is the single production adapter for HostAPI
// admission and Content Registry provider callbacks. It captures one Manager;
// callers cannot substitute a different runtime authority per invocation.
type ContentRegistryProtocolRuntime struct {
	manager *Manager
}

func NewContentRegistryProtocolRuntime(manager *Manager) (*ContentRegistryProtocolRuntime, error) {
	if manager == nil || manager.managerCore == nil {
		return nil, contentregistry.ErrExecutionInvalid
	}
	return &ContentRegistryProtocolRuntime{manager: manager}, nil
}

func (r *ContentRegistryProtocolRuntime) AcquireContentRegistryRuntime(
	ctx context.Context,
	claim hostapi.ContentRegistryRuntimeIdentity,
) (hostapi.ContentRegistryRuntimeLease, error) {
	if r == nil || r.manager == nil || ctx == nil || !validContentRuntimeClaim(claim) {
		return nil, contentregistry.ErrRuntimeUnavailable
	}
	identity := RuntimeInstanceIdentity{ExtensionID: claim.ExtensionID, InstanceID: claim.RuntimeInstanceID}
	lease, err := r.manager.AcquireRuntimeCall(ctx, identity, RuntimeCallProvider)
	if err != nil {
		return nil, errors.Join(contentregistry.ErrRuntimeUnavailable, err)
	}
	failed := true
	defer func() {
		if failed {
			lease.Release()
		}
	}()
	extension, err := exactContentManagedRuntime(r.manager.managerCore, identity, claim)
	if err != nil {
		return nil, err
	}
	if !exactManifestContentDeclaration(extension, claim) {
		return nil, contentregistry.ErrContractStale
	}
	failed = false
	return &contentRegistryRuntimeLease{lease: lease}, nil
}

func (r *ContentRegistryProtocolRuntime) ResolveContentProviders(
	contribution contentregistry.Contribution,
) (contentregistry.ProviderSet, error) {
	if r == nil || r.manager == nil || contribution.Artifact.Core ||
		strings.TrimSpace(contribution.Handler) == "" || strings.TrimSpace(contribution.Artifact.RuntimeInstanceID) == "" {
		return contentregistry.ProviderSet{}, contentregistry.ErrRuntimeUnavailable
	}
	provider := &protocolV2ContentProvider{runtime: r, contribution: contribution}
	return contentregistry.ProviderSet{
		Editor: provider, Validator: provider, Serializer: provider, Renderer: provider, Filter: provider,
	}, nil
}

type contentRegistryRuntimeLease struct {
	lease *RuntimeAdmissionLease
}

func (l *contentRegistryRuntimeLease) Context() context.Context {
	if l == nil || l.lease == nil {
		return nil
	}
	return l.lease.Context
}

func (l *contentRegistryRuntimeLease) Release() {
	if l != nil && l.lease != nil {
		l.lease.Release()
	}
}

type protocolV2ContentProvider struct {
	runtime      *ContentRegistryProtocolRuntime
	contribution contentregistry.Contribution
}

func (p *protocolV2ContentProvider) PrepareEditorDocument(
	ctx context.Context,
	request contentregistry.EditorProviderRequest,
) (contentregistry.EditorDocument, error) {
	result, err := p.invoke(ctx, contentregistry.OperationEditor, contentCallWireRequest{
		Target: contentDeclarationWire(request.Target), Provider: contentDeclarationWire(request.Provider),
		Action: request.Action, Operation: contentregistry.OperationEditor,
		Document:   contentEditorDocumentToWire(request.Document),
		ResourceID: request.ResourceID, Locale: request.Locale, Scope: request.Scope,
	})
	if err != nil || result.Document == nil {
		return contentregistry.EditorDocument{}, errors.Join(contentregistry.ErrProviderFailed, err)
	}
	return contentEditorDocumentFromWire(*result.Document), nil
}

func (p *protocolV2ContentProvider) ValidateEditorDocument(
	ctx context.Context,
	request contentregistry.ValidatorProviderRequest,
) error {
	result, err := p.invoke(ctx, contentregistry.OperationValidator, contentCallWireRequest{
		Target: contentDeclarationWire(request.Target), Provider: contentDeclarationWire(request.Provider),
		Action: request.Action, Operation: contentregistry.OperationValidator,
		Document:   contentEditorDocumentToWire(request.Document),
		ResourceID: request.ResourceID, Locale: request.Locale, Scope: request.Scope,
	})
	if err != nil || result.Document != nil || result.Serialized != nil || result.Render != nil {
		return errors.Join(contentregistry.ErrProviderFailed, err)
	}
	return nil
}

func (p *protocolV2ContentProvider) SerializeEditorDocument(
	ctx context.Context,
	request contentregistry.SerializerProviderRequest,
) (contentregistry.SerializedContent, error) {
	result, err := p.invoke(ctx, contentregistry.OperationSerializer, contentCallWireRequest{
		Target: contentDeclarationWire(request.Target), Provider: contentDeclarationWire(request.Provider),
		Action: request.Action, Operation: contentregistry.OperationSerializer,
		Document:   contentEditorDocumentToWire(request.Document),
		ResourceID: request.ResourceID, Locale: request.Locale, Scope: request.Scope,
	})
	if err != nil || result.Serialized == nil {
		return contentregistry.SerializedContent{}, errors.Join(contentregistry.ErrProviderFailed, err)
	}
	return contentSerializedDocumentFromWire(*result.Serialized), nil
}

func (p *protocolV2ContentProvider) RenderContent(
	ctx context.Context,
	request contentregistry.RendererProviderRequest,
) (contentregistry.RenderSegments, error) {
	result, err := p.invoke(ctx, contentregistry.OperationRenderer, contentCallWireRequest{
		Target: contentDeclarationWire(request.Target), Provider: contentDeclarationWire(request.Provider),
		Action: request.Action, Operation: contentregistry.OperationRenderer,
		Document:   contentEditorDocumentToWire(request.Document),
		Serialized: contentSerializedDocumentToWire(request.Serialized), Render: contentRenderSegmentsToWire(request.Inner),
		ResourceID: request.ResourceID, Locale: request.Locale, Scope: request.Scope,
		SuppressHostDelegations: request.SuppressHostDelegations,
	})
	if err != nil || result.Render == nil {
		return contentregistry.RenderSegments{}, errors.Join(contentregistry.ErrProviderFailed, err)
	}
	return contentRenderSegmentsFromWire(*result.Render), nil
}

func (p *protocolV2ContentProvider) FilterRenderedContent(
	ctx context.Context,
	request contentregistry.FilterProviderRequest,
) (contentregistry.RenderSegments, error) {
	result, err := p.invoke(ctx, contentregistry.OperationFilter, contentCallWireRequest{
		Target: contentDeclarationWire(request.Target), Provider: contentDeclarationWire(request.Provider),
		Action: request.Action, Operation: contentregistry.OperationFilter,
		Document:   contentEditorDocumentToWire(request.Document),
		Serialized: contentSerializedDocumentToWire(request.Serialized), Render: contentRenderSegmentsToWire(request.Render),
		ResourceID: request.ResourceID, Locale: request.Locale, Scope: request.Scope,
	})
	if err != nil || result.Render == nil {
		return contentregistry.RenderSegments{}, errors.Join(contentregistry.ErrProviderFailed, err)
	}
	return contentRenderSegmentsFromWire(*result.Render), nil
}

func (p *protocolV2ContentProvider) invoke(
	ctx context.Context,
	operation string,
	input contentCallWireRequest,
) (pluginv2sdk.ContentResult, error) {
	if p == nil || p.runtime == nil || p.runtime.manager == nil || ctx == nil ||
		p.contribution.Artifact.Core || p.contribution.Handler == "" {
		return pluginv2sdk.ContentResult{}, contentregistry.ErrRuntimeUnavailable
	}
	identity := RuntimeInstanceIdentity{
		ExtensionID: p.contribution.Artifact.ExtensionID,
		InstanceID:  p.contribution.Artifact.RuntimeInstanceID,
	}
	claim := contentRuntimeIdentityFor(p.contribution, input.Target, input.Action, operation)
	extension, err := exactContentManagedRuntime(p.runtime.manager.managerCore, identity, claim)
	if err != nil || !exactManifestContentDeclaration(extension, claim) {
		return pluginv2sdk.ContentResult{}, errors.Join(contentregistry.ErrContractStale, err)
	}
	invoker, ok := p.runtime.manager.starter.(exactContentInstanceInvoker)
	if !ok {
		return pluginv2sdk.ContentResult{}, contentregistry.ErrRuntimeUnavailable
	}
	values, err := encodeContentWire(input)
	if err != nil {
		return pluginv2sdk.ContentResult{}, contentregistry.ErrExecutionInvalid
	}
	response, err := invoker.InvokeContentInstance(ctx, identity, extension, VersionedContentRequest{
		DeclarationID: p.contribution.ID, ContractVersion: p.contribution.ContractVersion,
		Kind: p.contribution.Kind, Handler: p.contribution.Handler, Schema: p.contribution.Schema,
		Renderer: p.contribution.Renderer, Migration: p.contribution.Migration,
		Operation: operation, Timeout: defaultContentRuntimeTimeout, Input: values,
		SuppressHostDelegations: input.SuppressHostDelegations,
	})
	if err != nil {
		return pluginv2sdk.ContentResult{}, err
	}
	result := pluginv2sdk.ContentResult{}
	if err := decodeContentWire(response.Output, &result); err != nil {
		return pluginv2sdk.ContentResult{}, contentregistry.ErrExecutionInvalid
	}
	return result, nil
}

type contentCallWireRequest struct {
	Target                  pluginv2sdk.ContentDeclaration        `json:"target"`
	Provider                pluginv2sdk.ContentDeclaration        `json:"provider"`
	Action                  string                                `json:"action"`
	Operation               string                                `json:"operation"`
	Document                pluginv2sdk.ContentEditorDocument     `json:"document"`
	Serialized              pluginv2sdk.ContentSerializedDocument `json:"serialized"`
	Render                  pluginv2sdk.ContentRenderSegments     `json:"render"`
	ResourceID              string                                `json:"resourceId"`
	Locale                  string                                `json:"locale"`
	Scope                   string                                `json:"scope"`
	SuppressHostDelegations bool                                  `json:"-"`
}

func contentEditorDocumentToWire(input contentregistry.EditorDocument) pluginv2sdk.ContentEditorDocument {
	return pluginv2sdk.ContentEditorDocument{
		SchemaVersion: input.SchemaVersion, ContentID: input.ContentID,
		ContractVersion: input.ContractVersion, Schema: input.Schema,
		StorageVersion: input.StorageVersion, Value: append(json.RawMessage(nil), input.Value...),
	}
}

func contentEditorDocumentFromWire(input pluginv2sdk.ContentEditorDocument) contentregistry.EditorDocument {
	return contentregistry.EditorDocument{
		SchemaVersion: input.SchemaVersion, ContentID: input.ContentID,
		ContractVersion: input.ContractVersion, Schema: input.Schema,
		StorageVersion: input.StorageVersion, Value: append(json.RawMessage(nil), input.Value...),
	}
}

func contentSerializedDocumentToWire(input contentregistry.SerializedContent) pluginv2sdk.ContentSerializedDocument {
	return pluginv2sdk.ContentSerializedDocument{
		SchemaVersion: input.SchemaVersion, ContentID: input.ContentID,
		ContractVersion: input.ContractVersion, StorageVersion: input.StorageVersion,
		MediaType: input.MediaType, Data: append([]byte(nil), input.Data...),
	}
}

func contentSerializedDocumentFromWire(input pluginv2sdk.ContentSerializedDocument) contentregistry.SerializedContent {
	return contentregistry.SerializedContent{
		SchemaVersion: input.SchemaVersion, ContentID: input.ContentID,
		ContractVersion: input.ContractVersion, StorageVersion: input.StorageVersion,
		MediaType: input.MediaType, Data: append([]byte(nil), input.Data...),
	}
}

func contentRenderSegmentsToWire(input contentregistry.RenderSegments) pluginv2sdk.ContentRenderSegments {
	segments := make([]pluginv2sdk.ContentRenderSegment, len(input.Segments))
	for index, segment := range input.Segments {
		segments[index] = pluginv2sdk.ContentRenderSegment{Kind: segment.Kind, HTML: segment.HTML, Text: segment.Text}
	}
	return pluginv2sdk.ContentRenderSegments{
		SchemaVersion: input.SchemaVersion, ContentID: input.ContentID,
		ContractVersion: input.ContractVersion, TextEncoding: input.TextEncoding,
		Segments: segments, PlainText: input.PlainText,
	}
}

func contentRenderSegmentsFromWire(input pluginv2sdk.ContentRenderSegments) contentregistry.RenderSegments {
	segments := make([]contentregistry.RenderSegment, len(input.Segments))
	for index, segment := range input.Segments {
		segments[index] = contentregistry.RenderSegment{Kind: segment.Kind, HTML: segment.HTML, Text: segment.Text}
	}
	return contentregistry.RenderSegments{
		SchemaVersion: input.SchemaVersion, ContentID: input.ContentID,
		ContractVersion: input.ContractVersion, TextEncoding: input.TextEncoding,
		Segments: segments, PlainText: input.PlainText,
	}
}

func contentDeclarationWire(input contentregistry.Contribution) pluginv2sdk.ContentDeclaration {
	return pluginv2sdk.ContentDeclaration{
		ID: input.ID, ContractVersion: input.ContractVersion, Kind: input.Kind,
		Handler: input.Handler, Schema: input.Schema, Renderer: input.Renderer, Migration: input.Migration,
	}
}

func contentRuntimeIdentityFor(
	provider contentregistry.Contribution,
	target pluginv2sdk.ContentDeclaration,
	action, operation string,
) hostapi.ContentRegistryRuntimeIdentity {
	artifact := provider.Artifact
	return hostapi.ContentRegistryRuntimeIdentity{
		TargetID: target.ID, TargetContractVersion: target.ContractVersion, TargetSchema: target.Schema,
		ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.ExtensionVersion,
		PackageDigest: artifact.PackageDigest, VersionID: artifact.VersionID,
		RuntimeInstanceID: artifact.RuntimeInstanceID, ContentID: provider.ID,
		ContractVersion: provider.ContractVersion, Kind: provider.Kind, Schema: provider.Schema,
		HandlerReference: provider.Handler, RendererReference: provider.Renderer,
		MigrationReference: provider.Migration, Action: action, Operation: operation,
	}
}

func validContentRuntimeClaim(claim hostapi.ContentRegistryRuntimeIdentity) bool {
	return claim.ExtensionID != "" && claim.ExtensionVersion != "" && claim.PackageDigest != "" &&
		claim.VersionID > 0 && claim.RuntimeInstanceID != "" && claim.ContentID != "" &&
		claim.ContractVersion != "" && claim.Kind != "" && claim.Schema != "" &&
		claim.HandlerReference != "" && claim.Operation != ""
}

func exactContentManagedRuntime(
	m *managerCore,
	identity RuntimeInstanceIdentity,
	claim hostapi.ContentRegistryRuntimeIdentity,
) (extensions.Extension, error) {
	if m == nil || m.host == nil {
		return extensions.Extension{}, contentregistry.ErrRuntimeUnavailable
	}
	snapshot, err := m.host.InspectRuntimeInstance(identity)
	if err != nil {
		return extensions.Extension{}, errors.Join(contentregistry.ErrRuntimeUnavailable, err)
	}
	extension, available := m.runningExtension(identity.ExtensionID)
	if !available || !snapshot.Active || snapshot.Admission.Draining || snapshot.Admission.Quarantined ||
		snapshot.Admission.Forced || snapshot.Identity.ExtensionID != claim.ExtensionID ||
		snapshot.Identity.InstanceID != claim.RuntimeInstanceID || snapshot.ExtensionVersion != claim.ExtensionVersion ||
		snapshot.ArtifactDigest != claim.PackageDigest || snapshot.VersionID != claim.VersionID ||
		extension.ID != claim.ExtensionID || extension.Version != claim.ExtensionVersion ||
		extension.PackageDigest != claim.PackageDigest || extension.ActiveVersionID != claim.VersionID ||
		extension.Type != extensions.TypePlugin || extension.Manifest.ID != claim.ExtensionID ||
		extension.Manifest.Version != claim.ExtensionVersion || extension.Manifest.Type != extensions.TypePlugin ||
		extension.Manifest.Backend.ProtocolVersion != 2 {
		return extensions.Extension{}, contentregistry.ErrContractStale
	}
	return extension, nil
}

func exactManifestContentDeclaration(extension extensions.Extension, claim hostapi.ContentRegistryRuntimeIdentity) bool {
	for _, declaration := range extension.Manifest.Content {
		if declaration.ID == claim.ContentID && declaration.ContractVersion == claim.ContractVersion &&
			declaration.Kind == claim.Kind && declaration.Schema == claim.Schema &&
			declaration.Handler == claim.HandlerReference && declaration.Renderer == claim.RendererReference &&
			declaration.Migration == claim.MigrationReference {
			return true
		}
	}
	return false
}

func (s *ProtocolStarter) InvokeContentInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	request VersionedContentRequest,
) (VersionedContentResponse, error) {
	client, err := s.exactContentRuntimeClient(ctx, identity, extension)
	if err != nil {
		return VersionedContentResponse{}, err
	}
	return client.InvokeContent(ctx, request)
}

func (s *ProtocolStarter) exactContentRuntimeClient(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
) (*protocolV2Client, error) {
	if s == nil || ctx == nil {
		return nil, contentregistry.ErrRuntimeUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return nil, errors.Join(contentregistry.ErrRuntimeUnavailable, err)
	}
	var client *protocolV2Client
	if err := func() error {
		unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
		if err != nil {
			return err
		}
		defer unlock()
		instance := s.protocolInstance(identity)
		if instance == nil {
			return protocolInstanceNotFound(identity)
		}
		var ok bool
		client, ok = instance.protocol.(*protocolV2Client)
		if !ok || client.identity == nil || client.identity.GetExtensionId() != identity.ExtensionID ||
			client.identity.GetInstanceId() != identity.InstanceID ||
			client.identity.GetExtensionVersion() != instance.extensionVersion ||
			client.identity.GetArtifactDigest() != instance.artifactDigest ||
			extension.ID != identity.ExtensionID || extension.Version != instance.extensionVersion ||
			extension.PackageDigest != instance.artifactDigest {
			return contentregistry.ErrContractStale
		}
		s.mu.Lock()
		s.recordProtocolCallLocked(identity.ExtensionID)
		s.mu.Unlock()
		return nil
	}(); err != nil {
		return nil, err
	}
	return client, nil
}

func encodeContentWire(input any) (map[string]any, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeContentWire(input map[string]any, target any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

var _ hostapi.ContentRegistryAdmissionBackend = (*ContentRegistryProtocolRuntime)(nil)
var _ contentregistry.ContentProviderResolver = (*ContentRegistryProtocolRuntime)(nil)
var _ contentregistry.EditorProvider = (*protocolV2ContentProvider)(nil)
var _ contentregistry.ValidatorProvider = (*protocolV2ContentProvider)(nil)
var _ contentregistry.SerializerProvider = (*protocolV2ContentProvider)(nil)
var _ contentregistry.RendererProvider = (*protocolV2ContentProvider)(nil)
var _ contentregistry.FilterProvider = (*protocolV2ContentProvider)(nil)
var _ exactContentInstanceInvoker = (*ProtocolStarter)(nil)
var _ extensions.RuntimeContentPublicationBoundary = (*PostgresLifecycleBoundaryRegistries)(nil)
var _ extensions.RuntimeContentPublicationMutation = (*runtimeContentMutation)(nil)
