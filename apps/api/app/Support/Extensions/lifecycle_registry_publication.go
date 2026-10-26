package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

var (
	ErrLifecycleRegistryPublicationInvalid     = errors.New("extension lifecycle registry publication input is invalid")
	ErrLifecycleRegistryPublicationConflict    = errors.New("extension lifecycle registry publication exact fence conflict")
	ErrLifecycleRegistryPublicationNotPrepared = errors.New("extension lifecycle registry publication is not prepared")
	ErrLifecycleRegistryPublicationCommitted   = errors.New("extension lifecycle registry publication marker is committed")
	ErrLifecycleRegistryPublicationUnavailable = errors.New("extension lifecycle registry publication dependency is unavailable")
)

type LifecycleRegistryPublicationPhase string

const (
	LifecycleRegistryPublicationSource LifecycleRegistryPublicationPhase = "source"
	LifecycleRegistryPublicationTarget LifecycleRegistryPublicationPhase = "target"
)

type LifecycleRegistryPublicationRef struct {
	OperationID int64
	StepID      string
	Mode        LifecycleBoundaryPublicationMode
	Attempt     int
}

type PrepareLifecycleRegistryPublicationInput struct {
	Fence                   lifecyclePublicationFence
	SourceDigest            string
	TargetDigest            string
	CompatibleSourceDigests []string
	CompatibleTargetDigests []string
}

// LifecycleRegistryPublicationRepository owns the durable aggregate phase.
// Move keeps the PostgreSQL fence locked while one node reconciles its local
// immutable snapshots, so an opposite or stale attempt cannot commit midway.
type LifecycleRegistryPublicationRepository interface {
	PrepareLifecycleRegistryPublication(context.Context, PrepareLifecycleRegistryPublicationInput) (LifecycleRegistryPublicationRef, error)
	InspectLifecycleRegistryPublication(context.Context, LifecycleRegistryPublicationRef) (LifecycleRegistryPublicationPhase, error)
	MoveLifecycleRegistryPublication(
		context.Context,
		LifecycleRegistryPublicationRef,
		LifecycleRegistryPublicationPhase,
		func() error,
	) error
}

type transactionalLifecycleRegistryPublicationRepository interface {
	MoveLifecycleRegistryPublicationTx(
		context.Context,
		LifecycleRegistryPublicationRef,
		LifecycleRegistryPublicationPhase,
		func(pgx.Tx) error,
	) error
}

// LifecycleIdentityStartupRecovery repairs only an exact source publication
// whose failed deactivation never committed and whose aggregate registry and
// extension-state transactions both durably returned to source.
type LifecycleIdentityStartupRecovery interface {
	RecoverStaleIdentityPublication(
		context.Context,
		identityregistry.Publication,
	) (identityregistry.DurableState, bool, error)
}

type LifecycleRegistryBoundaryConfig struct {
	Repository   LifecycleRegistryPublicationRepository
	Manager      *Manager
	Pages        *pages.Registry
	ThemeRuntime *pages.ThemeRuntimeRegistry
	PageSiteName string
	PageLocales  []string
	Routes       *routes.Registry
	RouteSchemas *extensionopenapi.RouteSchemaPublication
	Services     *hostapi.ServiceRegistry
	Components   *ComponentRegistry
	// ComponentComposition 可选：组件发布成功后同步编译包本地 SSR。
	ComponentComposition *ProductionComponentComposition
	Assets               *assetregistry.Registry
	Caches               *cacheregistry.Registry
	Queries              *queryregistry.Registry
	SEO                  *seoregistry.Registry
	Identity             *identityregistry.Registry
	IdentityStore        identityregistry.PublicationStore
	IdentityRecovery     LifecycleIdentityStartupRecovery
	Navigation           *navigationregistry.Registry
	Notifications        *notifications.Registry
	// Content is the P10 Content Registry (block/shortcode/embed/node/mark/…).
	Content *contentregistry.Registry
	// Media is the P10 Media Pipeline Registry (MIME policy/processors/variants).
	Media *mediaregistry.Registry
	// Editor is the P10 Tiptap node/mark/command/toolbar registry.
	Editor *editorregistry.Registry
	// Entity is the P10 Entity Type / Taxonomy / Field Schema registry.
	Entity         *entityregistry.Registry
	AssetAuthority LifecycleAssetAuthority
	AssetAdmission LifecycleAssetAdmission
}

// PostgresLifecycleBoundaryRegistries composes the production HookBus,
// Protocol-v2 Service Registry, Page, Route, Component, and Asset registries,
// plus the exact package schema publication consumed by the Route dispatcher.
type PostgresLifecycleBoundaryRegistries struct {
	publicationMu    sync.Mutex
	repository       LifecycleRegistryPublicationRepository
	manager          *Manager
	hooks            *HookBus
	pages            *pages.Registry
	themeRuntime     *pages.ThemeRuntimeRegistry
	pageSiteName     string
	pageLocales      []string
	routes           *routes.Registry
	routePublisher   lifecycleRoutePublicationCAS
	routeSchemas     *extensionopenapi.RouteSchemaPublication
	services         *hostapi.ServiceRegistry
	components       *ComponentRegistry
	componentSSR     *ProductionComponentComposition
	assets           *assetregistry.Registry
	caches           *cacheregistry.Registry
	queries          *queryregistry.Registry
	seo              *seoregistry.Registry
	identity         *identityregistry.Registry
	identityStore    identityregistry.PublicationStore
	identityRecovery LifecycleIdentityStartupRecovery
	identitySet      bool
	navigation       *navigationregistry.Registry
	notifications    *notifications.Registry
	content          *contentregistry.Registry
	media            *mediaregistry.Registry
	editor           *editorregistry.Registry
	entity           *entityregistry.Registry
	assetAuthority   LifecycleAssetAuthority
	assetAdmission   LifecycleAssetAdmission
	// identitySuppressed lists plugins whose durable Identity publication could
	// not be proven exact during this boot. They stay unpublished (identity,
	// routes, assets, pages, …) until a lifecycle operation republishes them, so
	// one stale publication cannot keep the whole Host from starting.
	identitySuppressed []string
}

func NewPostgresLifecycleBoundaryRegistries(config LifecycleRegistryBoundaryConfig) *PostgresLifecycleBoundaryRegistries {
	components := config.Components
	if components == nil {
		components = NewComponentRegistry()
	}
	notificationRegistry := config.Notifications
	if notificationRegistry == nil {
		notificationRegistry = notifications.NewRegistry()
	}
	boundary := &PostgresLifecycleBoundaryRegistries{
		repository:       config.Repository,
		manager:          config.Manager,
		pages:            config.Pages,
		themeRuntime:     config.ThemeRuntime,
		pageSiteName:     config.PageSiteName,
		pageLocales:      append([]string(nil), config.PageLocales...),
		routes:           config.Routes,
		routePublisher:   config.Routes,
		routeSchemas:     config.RouteSchemas,
		services:         config.Services,
		components:       components,
		componentSSR:     config.ComponentComposition,
		assets:           config.Assets,
		caches:           config.Caches,
		queries:          config.Queries,
		seo:              config.SEO,
		identity:         config.Identity,
		identityStore:    config.IdentityStore,
		identityRecovery: config.IdentityRecovery,
		identitySet:      config.Identity != nil || config.IdentityStore != nil,
		navigation:       config.Navigation,
		notifications:    notificationRegistry,
		content:          config.Content,
		media:            config.Media,
		editor:           config.Editor,
		entity:           config.Entity,
		assetAuthority:   config.AssetAuthority,
		assetAdmission:   config.AssetAdmission,
	}
	if boundary.queries == nil {
		boundary.queries = queryregistry.New()
	}
	if boundary.caches == nil {
		boundary.caches = cacheregistry.New()
	}
	if boundary.seo == nil {
		boundary.seo = seoregistry.New()
	}
	if boundary.navigation == nil {
		boundary.navigation = navigationregistry.New()
	}
	if config.Manager != nil {
		boundary.hooks = config.Manager.HookBus()
	}
	if boundary.pages != nil && boundary.manager != nil {
		boundary.pages.WithRuntimeAdmission(func(artifact pages.RuntimeArtifact) bool {
			identity := RuntimeInstanceIdentity{ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID}
			snapshot, err := boundary.manager.InspectRuntimeInstance(identity)
			return err == nil && snapshot.ExtensionVersion == artifact.ExtensionVersion &&
				snapshot.ArtifactDigest == artifact.PackageDigest && boundary.manager.RuntimeInstanceAvailable(identity)
		})
	}
	if boundary.routes != nil && boundary.manager != nil {
		boundary.routes.WithPluginAdmission(func(artifact routes.PluginArtifact) bool {
			identity := RuntimeInstanceIdentity{ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID}
			snapshot, err := boundary.manager.InspectRuntimeInstance(identity)
			return err == nil && snapshot.ExtensionVersion == artifact.ExtensionVersion &&
				snapshot.ArtifactDigest == artifact.PackageDigest && boundary.manager.RuntimeInstanceAvailable(identity)
		})
	}
	if boundary.queries != nil && boundary.manager != nil {
		boundary.queries.WithPluginAdmission(func(artifact queryregistry.Artifact) bool {
			identity := RuntimeInstanceIdentity{ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID}
			snapshot, err := boundary.manager.InspectRuntimeInstance(identity)
			return err == nil && snapshot.ExtensionVersion == artifact.ExtensionVersion &&
				snapshot.ArtifactDigest == artifact.PackageDigest && snapshot.VersionID == artifact.VersionID &&
				boundary.manager.RuntimeInstanceAvailable(identity)
		})
	}
	if boundary.caches != nil && boundary.manager != nil {
		boundary.caches.WithPluginAdmission(func(artifact cacheregistry.Artifact) bool {
			identity := RuntimeInstanceIdentity{ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID}
			snapshot, err := boundary.manager.InspectRuntimeInstance(identity)
			return err == nil && snapshot.ExtensionVersion == artifact.ExtensionVersion &&
				snapshot.ArtifactDigest == artifact.PackageDigest && boundary.manager.RuntimeInstanceAvailable(identity)
		})
	}
	return boundary
}

func (b *PostgresLifecycleBoundaryRegistries) ComponentRegistry() *ComponentRegistry {
	if b == nil {
		return nil
	}
	return b.components
}

func (b *PostgresLifecycleBoundaryRegistries) IdentityRegistry() *identityregistry.Registry {
	if b == nil {
		return nil
	}
	return b.identity
}

// RestoreRoutePublications rebuilds process-local immutable catalogs from the
// exact runtime instances that survived startup reconciliation. Safe Mode owns
// deliberately empty extension schema/component/asset catalogs and core-only
// Route and Component registries.
func (b *PostgresLifecycleBoundaryRegistries) restoreExactPluginPagePublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || b.pages == nil || b.manager == nil || safeMode {
		return nil
	}
	for _, item := range items {
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled ||
			strings.TrimSpace(item.Manifest.Backend.Entry) == "" {
			continue
		}
		// 先加载 inert 包并派生页面贡献：无 theme.json / 无 pages 的后端插件
		// （如 sforum.content-policy）不得因 page-only exact fence 阻断 Host 启动。
		pkg, err := pages.LoadThemePackage(extensions.PackageContentRoot(item))
		if err != nil {
			return fmt.Errorf("restore plugin page package for %s: %w", item.ID, err)
		}
		contributions := pages.ContributionsFromTheme(item.ID, item.Version, item.PackageDigest, pkg)
		if len(contributions) == 0 {
			continue
		}
		// 仅当存在真实页面贡献时才要求 exact artifact + RuntimeInstanceAvailable。
		runtime, err := b.manager.ActiveRuntimeInstance(item.ID)
		if err != nil {
			continue
		}
		if !runtimeInstanceMatchesExtension(runtime, item) || !b.manager.RuntimeInstanceAvailable(runtime.Identity) {
			return fmt.Errorf("%w: startup page runtime for %s is not exact and available", ErrLifecycleRegistryPublicationConflict, item.ID)
		}
		binding := extensions.LifecycleRuntimeBinding{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			RuntimeInstanceID: runtime.Identity.InstanceID, VersionID: item.ActiveVersionID,
		}
		for index := range contributions {
			contributions[index].RuntimeInstanceID = binding.RuntimeInstanceID
		}
		if b.themeRuntime != nil {
			snapshot, buildErr := buildExactPluginPageRuntime(item, binding, contributions, b.pageSiteName, b.pageLocales)
			if buildErr != nil {
				return fmt.Errorf("restore exact plugin page runtime for %s: %w", item.ID, buildErr)
			}
			if snapshot != nil {
				if _, _, stageErr := b.themeRuntime.Stage(snapshot); stageErr != nil {
					return fmt.Errorf("stage exact plugin page runtime for %s: %w", item.ID, stageErr)
				}
			}
		}
		artifact := pages.RuntimeArtifact{
			ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
			RuntimeInstanceID: binding.RuntimeInstanceID,
		}
		published := false
		for attempts := 0; attempts < 16; attempts++ {
			existing, existed := b.pages.ExtensionSnapshot(item.ID)
			if _, publishErr := b.pages.PublishExtensionIfRevision(artifact, contributions, existing.Revision); publishErr == nil {
				if existed && existing.Artifact != artifact && b.themeRuntime != nil {
					if _, removeErr := b.themeRuntime.RemoveExact(existing.Artifact); removeErr != nil {
						return fmt.Errorf("remove superseded plugin page runtime for %s: %w", item.ID, removeErr)
					}
				}
				published = true
				break
			} else if !errors.Is(publishErr, pages.ErrRevisionConflict) {
				return fmt.Errorf("restore exact plugin page publication for %s: %w", item.ID, publishErr)
			}
		}
		if !published {
			return pages.ErrRevisionConflict
		}
	}
	return nil
}

// buildStartupRoutePublicationMaterial is deliberately independent from the
// Lifecycle V2 coordinator. Route/OpenAPI declarations may belong to an exact
// enabled runtime without declaring executable lifecycle hooks, while legacy
// provider-only plugins have no route material and are skipped by the caller.
func buildStartupRoutePublicationMaterial(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (lifecycleRegistryMaterial, error) {
	if extension.ID == "" || extension.Version == "" || extension.ActiveVersionID <= 0 ||
		!validLifecycleCleanupDigest(extension.PackageDigest) || extension.Type != extensions.TypePlugin ||
		extension.Manifest.ID != extension.ID || extension.Manifest.Version != extension.Version ||
		extension.Manifest.Type != extensions.TypePlugin ||
		binding.ExtensionID != extension.ID || binding.ExtensionVersion != extension.Version ||
		binding.PackageDigest != extension.PackageDigest || binding.VersionID != extension.ActiveVersionID ||
		binding.RuntimeInstanceID == "" || binding.RuntimeInstanceID != strings.TrimSpace(binding.RuntimeInstanceID) {
		return lifecycleRegistryMaterial{}, ErrLifecycleRegistryPublicationInvalid
	}
	return lifecycleRegistryMaterial{
		extension: extension,
		binding:   binding,
		routes: routes.PluginRouteSet{Artifact: routes.PluginArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, RuntimeInstanceID: binding.RuntimeInstanceID,
		}, Routes: append([]extensions.ManifestRoute(nil), extension.Manifest.Routes...),
			Guards: append([]extensions.ManifestGuard(nil), extension.Manifest.Guards...)},
		routeSchema: extensionopenapi.Artifact{
			Root: extensions.PackageContentRoot(extension), ExtensionID: extension.ID,
			Version: extension.Version, PackageDigest: extension.PackageDigest, Manifest: extension.Manifest,
		},
	}, nil
}

func (b *PostgresLifecycleBoundaryRegistries) ValidateLifecycleRegistries(
	ctx context.Context,
	request LifecycleBoundaryRequest,
) error {
	if err := b.validateDependencies(ctx); err != nil {
		return err
	}
	_, source, target, err := b.prepareMaterial(ctx, request)
	if err != nil {
		return err
	}
	if _, err := b.prepareAssetPlan(source, target); err != nil {
		return err
	}
	return b.validatePreparedLifecycleRegistries(ctx, source, target, request.artifactLoader)
}

func (b *PostgresLifecycleBoundaryRegistries) validatePreparedLifecycleRegistries(
	ctx context.Context,
	source, target *lifecycleRegistryMaterial,
	loader *extensionopenapi.ArtifactLoader,
) error {
	if err := b.validateNotificationTransition(target); err != nil {
		return err
	}
	if err := b.validateComponentTransition(source, target); err != nil {
		return err
	}
	if err := b.validateQueryTransition(source, target); err != nil {
		return err
	}
	if err := b.validateCacheTransition(source, target); err != nil {
		return err
	}
	if err := b.validateSEOTransition(source, target); err != nil {
		return err
	}
	if err := b.validateNavigationTransition(source, target); err != nil {
		return err
	}
	if err := b.validateContentTransition(source, target); err != nil {
		return err
	}
	if err := b.validateMediaTransition(source, target); err != nil {
		return err
	}
	if err := b.validateEditorTransition(source, target); err != nil {
		return err
	}
	if err := b.validateEntityTransition(source, target); err != nil {
		return err
	}
	if err := b.validateIdentityTransition(source, target); err != nil {
		return err
	}
	for _, material := range []*lifecycleRegistryMaterial{source, target} {
		if material == nil {
			continue
		}
		if err := b.validateRuntimeMaterial(*material); err != nil {
			return err
		}
		if publishesVersionedHookSnapshot(material.extension, material.binding.RuntimeInstanceID) {
			if err := b.hooks.registry.ValidateReplaceRuntime(material.extension, material.binding.RuntimeInstanceID); err != nil {
				return fmt.Errorf("validate hook registry: %w", err)
			}
			if err := b.hooks.providerSlots.ValidateReplaceRuntime(material.extension, material.binding.RuntimeInstanceID); err != nil {
				return fmt.Errorf("validate provider slot registry: %w", err)
			}
			if err := b.hooks.commands.ValidateReplaceRuntime(material.extension, material.binding.RuntimeInstanceID); err != nil {
				return fmt.Errorf("validate plugin command registry: %w", err)
			}
			if err := b.hooks.adminSurfaces.ValidateReplaceRuntime(material.extension, material.binding.RuntimeInstanceID); err != nil {
				return fmt.Errorf("validate admin surface registry: %w", err)
			}
		}
		if err := b.pages.PreflightContributionsReplacing(material.extension.ID, material.pages, material.extension.ID); err != nil {
			return fmt.Errorf("validate page registry: %w", err)
		}
		if b.themeRuntime != nil {
			if _, err := buildExactPluginPageRuntime(
				material.extension, material.binding, material.pages, b.pageSiteName, b.pageLocales,
			); err != nil {
				return fmt.Errorf("validate plugin page runtime: %w", err)
			}
		}
	}
	return b.validateLifecycleRoutePolicyStates(ctx, source, target, loader)
}

func (b *PostgresLifecycleBoundaryRegistries) PrepareLifecycleRegistryPublication(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) (LifecycleBoundaryTransaction, error) {
	if err := b.validateDependencies(ctx); err != nil {
		return nil, err
	}
	fence, source, target, err := b.prepareMaterial(ctx, request)
	if err != nil {
		return nil, err
	}
	if fence.Mode != mode {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	assetPlan, err := b.prepareAssetPlan(source, target)
	if err != nil {
		return nil, err
	}
	if err := b.validatePreparedLifecycleRegistries(ctx, source, target, request.artifactLoader); err != nil {
		return nil, err
	}
	ref, err := b.repository.PrepareLifecycleRegistryPublication(ctx, PrepareLifecycleRegistryPublicationInput{
		Fence: fence, SourceDigest: registryMaterialDigest(source, request.TargetExtension.ID),
		TargetDigest:            registryMaterialDigest(target, request.TargetExtension.ID),
		CompatibleSourceDigests: registryMaterialCompatibleDigests(source),
		CompatibleTargetDigests: registryMaterialCompatibleDigests(target),
	})
	if err != nil {
		return nil, err
	}
	return &postgresLifecycleRegistryTransaction{
		boundary: b, ref: ref, request: cloneLifecycleBoundaryRequest(request), source: source, target: target,
		assetPlan: assetPlan,
	}, nil
}

func (b *PostgresLifecycleBoundaryRegistries) validateDependencies(ctx context.Context) error {
	if b == nil || ctx == nil || b.repository == nil || b.manager == nil || b.hooks == nil ||
		b.pages == nil || b.routes == nil || b.routePublisher == nil || b.routeSchemas == nil || b.services == nil || b.components == nil ||
		b.queries == nil || b.caches == nil || b.seo == nil || b.navigation == nil || b.notifications == nil ||
		(b.identitySet && (b.identity == nil || b.identityStore == nil)) ||
		(b.assets != nil && (b.assetAuthority == nil || b.assetAdmission == nil)) {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	return ctx.Err()
}

type lifecycleRegistryMaterial struct {
	extension             extensions.Extension
	binding               extensions.LifecycleRuntimeBinding
	pages                 []pages.PageContribution
	routes                routes.PluginRouteSet
	routeSchema           extensionopenapi.Artifact
	assetPublication      *assetregistry.Publication
	cachePublication      *cacheregistry.Publication
	queryPublication      *queryregistry.Publication
	seoPublication        *seoregistry.Publication
	identityPublication   *identityregistry.Publication
	navigationPublication *navigationregistry.Publication
	contentPublication    *contentregistry.Publication
	mediaPublication      *mediaregistry.Publication
	editorPublication     *editorregistry.Publication
	entityPublication     *entityregistry.Publication
	assetAdmitted         bool
	digest                string
	legacyDigest          string
	compatibleDigests     []string
}

func (b *PostgresLifecycleBoundaryRegistries) prepareMaterial(
	ctx context.Context,
	request LifecycleBoundaryRequest,
) (lifecyclePublicationFence, *lifecycleRegistryMaterial, *lifecycleRegistryMaterial, error) {
	mode := LifecycleBoundaryActivate
	if request.Operation == extensions.LifecycleMachineDisable || request.Operation == extensions.LifecycleMachineUninstall {
		mode = LifecycleBoundaryDeactivate
	}
	fence, err := lifecyclePublicationFenceFor(request, mode)
	if err != nil {
		return lifecyclePublicationFence{}, nil, nil, err
	}
	var source *lifecycleRegistryMaterial
	if request.Operation != extensions.LifecycleMachineInstall && request.Operation != extensions.LifecycleMachineEnable {
		if request.SourceExtension == nil {
			return lifecyclePublicationFence{}, nil, nil, ErrLifecycleRegistryPublicationInvalid
		}
		value, materialErr := buildLifecycleRegistryMaterial(*request.SourceExtension, request.SourceBinding)
		if materialErr != nil {
			return lifecyclePublicationFence{}, nil, nil, materialErr
		}
		source = &value
	}
	var target *lifecycleRegistryMaterial
	if mode == LifecycleBoundaryActivate {
		value, materialErr := buildLifecycleRegistryMaterial(request.TargetExtension, request.TargetBinding)
		if materialErr != nil {
			return lifecyclePublicationFence{}, nil, nil, materialErr
		}
		target = &value
	}
	if err := b.freezeAssetMaterials(ctx, request, source, target); err != nil {
		return lifecyclePublicationFence{}, nil, nil, err
	}
	if err := b.freezeSEOMaterials(ctx, request, source, target); err != nil {
		return lifecyclePublicationFence{}, nil, nil, err
	}
	// Identity 已在 buildLifecycleRegistryMaterial 中冻结；Navigation 随后冻结。
	if err := b.freezeNavigationMaterials(ctx, request, source, target); err != nil {
		return lifecyclePublicationFence{}, nil, nil, err
	}
	if err := b.freezeContentMaterials(ctx, request, source, target); err != nil {
		return lifecyclePublicationFence{}, nil, nil, err
	}
	if err := b.freezeMediaMaterials(ctx, request, source, target); err != nil {
		return lifecyclePublicationFence{}, nil, nil, err
	}
	if err := b.freezeEditorMaterials(ctx, request, source, target); err != nil {
		return lifecyclePublicationFence{}, nil, nil, err
	}
	if err := b.freezeEntityMaterials(ctx, request, source, target); err != nil {
		return lifecyclePublicationFence{}, nil, nil, err
	}
	return fence, source, target, nil
}

func buildLifecycleRegistryMaterial(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (lifecycleRegistryMaterial, error) {
	if err := validateExactCoordinatorArtifact("registry", extension); err != nil ||
		validateExactCoordinatorBinding("registry", binding, extension, true) != nil {
		return lifecycleRegistryMaterial{}, ErrLifecycleRegistryPublicationInvalid
	}
	pkg, err := pages.LoadThemePackage(extensions.PackageContentRoot(extension))
	if err != nil {
		return lifecycleRegistryMaterial{}, fmt.Errorf("load plugin page package: %w", err)
	}
	pageContributions := pages.ContributionsFromTheme(extension.ID, extension.Version, extension.PackageDigest, pkg)
	for index := range pageContributions {
		pageContributions[index].RuntimeInstanceID = binding.RuntimeInstanceID
		if template := strings.TrimSpace(pageContributions[index].Template); template != "" {
			if _, err := pages.LoadTemplate(extensions.PackageContentRoot(extension), template); err != nil {
				return lifecycleRegistryMaterial{}, fmt.Errorf("validate plugin page template %s: %w", template, err)
			}
		}
	}
	material := lifecycleRegistryMaterial{
		extension: extension, binding: binding, pages: pageContributions,
		routes: routes.PluginRouteSet{Artifact: routes.PluginArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, RuntimeInstanceID: binding.RuntimeInstanceID,
		}, Routes: append([]extensions.ManifestRoute(nil), extension.Manifest.Routes...),
			// 守卫声明必须与路由一起冻结：Routes 侧只为「本包声明的守卫」建立绑定，
			// 漏掉声明会让引用它（含 R1 之后唯一合法的 kind=raw_request 守卫）的路由在
			// 启用期被 fail-closed 拒绝。启动恢复路径 buildStartupRoutePublicationMaterial
			// 已携带守卫，此处补齐同一条契约。
			Guards: append([]extensions.ManifestGuard(nil), extension.Manifest.Guards...)},
		routeSchema: extensionopenapi.Artifact{
			Root: extensions.PackageContentRoot(extension), ExtensionID: extension.ID,
			Version: extension.Version, PackageDigest: extension.PackageDigest, Manifest: extension.Manifest,
		},
	}
	queryPublication, err := buildLifecycleQueryPublication(extension, binding)
	if err != nil {
		return lifecycleRegistryMaterial{}, err
	}
	material.queryPublication = queryPublication
	cachePublication, err := buildLifecycleCachePublication(extension, binding)
	if err != nil {
		return lifecycleRegistryMaterial{}, err
	}
	material.cachePublication = cachePublication
	identityPublication, err := buildLifecycleIdentityPublication(extension, binding)
	if err != nil {
		return lifecycleRegistryMaterial{}, err
	}
	material.identityPublication = identityPublication
	if err := refreshLifecycleRegistryMaterialDigest(&material); err != nil {
		return lifecycleRegistryMaterial{}, err
	}
	return material, nil
}

func (b *PostgresLifecycleBoundaryRegistries) validateRuntimeMaterial(material lifecycleRegistryMaterial) error {
	identity := RuntimeInstanceIdentity{
		ExtensionID: material.binding.ExtensionID, InstanceID: material.binding.RuntimeInstanceID,
	}
	snapshot, err := b.manager.InspectRuntimeInstance(identity)
	if err != nil || !runtimeInstanceMatchesExtension(snapshot, material.extension) {
		return fmt.Errorf("%w: exact registry runtime is unavailable", ErrLifecycleRegistryPublicationConflict)
	}
	return nil
}

type postgresLifecycleRegistryTransaction struct {
	boundary  *PostgresLifecycleBoundaryRegistries
	ref       LifecycleRegistryPublicationRef
	request   LifecycleBoundaryRequest
	source    *lifecycleRegistryMaterial
	target    *lifecycleRegistryMaterial
	assetPlan *lifecycleAssetPlan
}

func (t *postgresLifecycleRegistryTransaction) Inspect(ctx context.Context) (LifecycleBoundaryTransactionState, error) {
	if t == nil || t.boundary == nil || ctx == nil {
		return "", ErrLifecycleRegistryPublicationUnavailable
	}
	phase, err := t.boundary.repository.InspectLifecycleRegistryPublication(ctx, t.ref)
	if err != nil {
		return "", err
	}
	switch phase {
	case LifecycleRegistryPublicationSource:
		return LifecycleBoundaryTransactionSource, nil
	case LifecycleRegistryPublicationTarget:
		return LifecycleBoundaryTransactionTarget, nil
	default:
		return "", ErrLifecycleRegistryPublicationConflict
	}
}

func (t *postgresLifecycleRegistryTransaction) Publish(ctx context.Context) error {
	return t.move(ctx, LifecycleRegistryPublicationTarget, t.target)
}

func (t *postgresLifecycleRegistryTransaction) Restore(ctx context.Context) error {
	return t.move(ctx, LifecycleRegistryPublicationSource, t.source)
}

func (t *postgresLifecycleRegistryTransaction) move(
	ctx context.Context,
	phase LifecycleRegistryPublicationPhase,
	desired *lifecycleRegistryMaterial,
) error {
	if t == nil || t.boundary == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	repository, repositoryTx := t.boundary.repository.(transactionalLifecycleRegistryPublicationRepository)
	_, identityTx := t.boundary.identityStore.(identityregistry.TransactionalPublicationStore)
	if lifecycleMaterialsPublishIdentity(t.source, t.target) && (!repositoryTx || !identityTx) {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if repositoryTx {
		return repository.MoveLifecycleRegistryPublicationTx(ctx, t.ref, phase, func(tx pgx.Tx) error {
			return t.boundary.reconcileLocalRegistries(
				ctx, tx, t.request, t.source, t.target, desired, t.assetPlan, phase,
			)
		})
	}
	return t.boundary.repository.MoveLifecycleRegistryPublication(ctx, t.ref, phase, func() error {
		return t.boundary.reconcileLocalRegistries(
			ctx, nil, t.request, t.source, t.target, desired, t.assetPlan, phase,
		)
	})
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileLocalRegistries(
	ctx context.Context,
	tx pgx.Tx,
	request LifecycleBoundaryRequest,
	source, target, desired *lifecycleRegistryMaterial,
	assetPlan *lifecycleAssetPlan,
	phase LifecycleRegistryPublicationPhase,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.publicationMu.Lock()
	defer b.publicationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.validateLifecycleRoutePolicyState(request.TargetExtension.ID, desired, source, target, request.artifactLoader); err != nil {
		return fmt.Errorf("route policy registry: %w", err)
	}
	if phase == LifecycleRegistryPublicationTarget && desired != nil {
		identity := RuntimeInstanceIdentity{ExtensionID: desired.extension.ID, InstanceID: desired.binding.RuntimeInstanceID}
		snapshot, err := b.manager.ActiveRuntimeInstance(desired.extension.ID)
		if err != nil || snapshot.Identity != identity || !runtimeInstanceMatchesExtension(snapshot, desired.extension) ||
			!snapshot.Admission.Draining || snapshot.Admission.ActiveTotal != 0 {
			return fmt.Errorf("runtime registry: %w: target runtime is not published and drained", ErrLifecycleRegistryPublicationConflict)
		}
	}
	identitySource, identityTarget := lifecycleIdentityMaterialsForPhase(phase, source, target)
	if err := b.reconcileIdentityTx(ctx, tx, request, identitySource, identityTarget, desired); err != nil {
		return fmt.Errorf("identity registry: %w", err)
	}
	if err := b.reconcileNotifications(ctx, request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("notification registry: %w", err)
	}
	if err := b.reconcileComponents(request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("component registry: %w", err)
	}
	if err := b.reconcileQueries(request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("query registry: %w", err)
	}
	if err := b.reconcileCaches(ctx, request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("cache registry: %w", err)
	}
	if err := b.reconcileSEO(ctx, request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("SEO registry: %w", err)
	}
	if err := b.reconcileNavigation(ctx, request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("navigation registry: %w", err)
	}
	if err := b.reconcileContent(ctx, request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("content registry: %w", err)
	}
	if err := b.reconcileMedia(ctx, request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("media registry: %w", err)
	}
	if err := b.reconcileEditor(ctx, request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("editor registry: %w", err)
	}
	if err := b.reconcileEntity(ctx, request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("entity registry: %w", err)
	}
	if err := b.applyAssetPlanRetrying(ctx, assetPlan, phase, source, target); err != nil {
		return fmt.Errorf("asset registry: %w", err)
	}
	if err := b.reconcileServices(request.TargetExtension.ID, source, target, desired, phase); err != nil {
		return fmt.Errorf("service registry: %w", err)
	}
	if err := b.reconcileRoutePolicyPublications(
		ctx, request.TargetExtension.ID, source, target, desired,
	); err != nil {
		return fmt.Errorf("route publication registry: %w", err)
	}
	if err := b.reconcilePages(ctx, request.TargetExtension.ID, source, target, desired); err != nil {
		return fmt.Errorf("page registry: %w", err)
	}
	if desired == nil {
		if err := b.unregisterAllowedHookRuntime(request.TargetExtension.ID, source, target); err != nil {
			return err
		}
	} else {
		if err := b.hooks.RegisterRuntime(desired.extension, desired.binding.RuntimeInstanceID); err != nil {
			return fmt.Errorf("publish hook registry: %w", err)
		}
	}
	return nil
}

func lifecycleIdentityMaterialsForPhase(
	phase LifecycleRegistryPublicationPhase,
	source, target *lifecycleRegistryMaterial,
) (*lifecycleRegistryMaterial, *lifecycleRegistryMaterial) {
	if phase == LifecycleRegistryPublicationSource {
		return target, source
	}
	return source, target
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileRouteSchemas(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) (lifecycleRouteSchemaChange, error) {
	allowed := lifecycleRouteSchemaAllowedArtifacts(source, target)
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return lifecycleRouteSchemaChange{}, err
		}
		snapshot := b.routeSchemas.PublicationSnapshot()
		var desiredArtifact *extensionopenapi.Artifact
		if desired != nil {
			value := desired.routeSchema
			desiredArtifact = &value
			if routeSchemaSnapshotHasExactArtifact(snapshot, value) {
				return lifecycleRouteSchemaChange{}, nil
			}
		} else if !routeSchemaSnapshotHasExtension(snapshot, extensionID) {
			return lifecycleRouteSchemaChange{}, nil
		}
		if published, err := b.routeSchemas.ReplaceExtensionIfRevision(
			extensionID, desiredArtifact, allowed, snapshot.Revision,
		); err == nil {
			return lifecycleRouteSchemaChange{before: snapshot, published: published, changed: true}, nil
		} else if !errors.Is(err, extensionopenapi.ErrRouteSchemaRevisionConflict) {
			if errors.Is(err, extensionopenapi.ErrRouteSchemaArtifactConflict) {
				return lifecycleRouteSchemaChange{}, fmt.Errorf(
					"%w: route schema exact artifact", ErrLifecycleRegistryPublicationConflict,
				)
			}
			return lifecycleRouteSchemaChange{}, err
		}
	}
	return lifecycleRouteSchemaChange{}, extensionopenapi.ErrRouteSchemaRevisionConflict
}

func lifecycleRouteSchemaAllowedArtifacts(
	materials ...*lifecycleRegistryMaterial,
) []extensionopenapi.PublishedRouteSchemaArtifact {
	result := make([]extensionopenapi.PublishedRouteSchemaArtifact, 0, len(materials))
	for _, material := range materials {
		if material == nil {
			continue
		}
		result = append(result, extensionopenapi.PublishedRouteSchemaArtifact{
			ExtensionID: material.extension.ID, ExtensionVersion: material.extension.Version,
			PackageDigest: material.extension.PackageDigest,
		})
	}
	return result
}

func routeSchemaSnapshotHasExtension(
	snapshot extensionopenapi.RouteSchemaPublicationSnapshot,
	extensionID string,
) bool {
	for _, artifact := range snapshot.Artifacts {
		if artifact.ExtensionID == extensionID {
			return true
		}
	}
	return false
}

func routeSchemaSnapshotHasExactArtifact(
	snapshot extensionopenapi.RouteSchemaPublicationSnapshot,
	artifact extensionopenapi.Artifact,
) bool {
	for _, published := range snapshot.Artifacts {
		if published.ExtensionID == artifact.ExtensionID && published.ExtensionVersion == artifact.Version &&
			published.PackageDigest == artifact.PackageDigest {
			return true
		}
	}
	return false
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileServices(
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
	phase LifecycleRegistryPublicationPhase,
) error {
	snapshot, exists, err := b.services.ExtensionSnapshot(extensionID)
	if err != nil {
		return err
	}
	if desired == nil {
		if !exists {
			return nil
		}
		if !registryInstanceAllowed(snapshot.InstanceID, source, target) ||
			!b.services.UnregisterProtocolV2ServiceInstance(extensionID, snapshot.InstanceID) {
			return ErrLifecycleRegistryPublicationConflict
		}
		return nil
	}
	if len(desired.extension.Manifest.Services) == 0 {
		if exists && snapshot.InstanceID != desired.binding.RuntimeInstanceID {
			return ErrLifecycleRegistryPublicationConflict
		}
		return nil
	}
	if exists && snapshot.InstanceID == desired.binding.RuntimeInstanceID &&
		len(snapshot.Registrations) == len(desired.extension.Manifest.Services) {
		return nil
	}
	// Restore runs before Manager republishes a retained source. An empty
	// Service Registry is a deliberate closed state; PublishInstance rebuilds
	// the frozen service set before admission can reopen.
	if phase == LifecycleRegistryPublicationSource {
		if !exists {
			return nil
		}
		if registryInstanceAllowed(snapshot.InstanceID, source, target) &&
			b.services.UnregisterProtocolV2ServiceInstance(extensionID, snapshot.InstanceID) {
			return nil
		}
	}
	return fmt.Errorf("%w: exact service set is unavailable", ErrLifecycleRegistryPublicationConflict)
}

func (b *PostgresLifecycleBoundaryRegistries) unregisterAllowedHookRuntime(
	extensionID string,
	source, target *lifecycleRegistryMaterial,
) error {
	current, ok := b.hooks.RuntimeSnapshot(extensionID)
	if !ok {
		return nil
	}
	if registryInstanceAllowed(current.InstanceID, source, target) {
		removed, err := b.hooks.unregisterRuntime(extensionID, current.InstanceID)
		if err != nil {
			return fmt.Errorf("unpublish hook registry: %w", err)
		}
		if removed {
			return nil
		}
	}
	return fmt.Errorf("%w: exact hook set is unavailable", ErrLifecycleRegistryPublicationConflict)
}

func registryInstanceAllowed(instanceID string, materials ...*lifecycleRegistryMaterial) bool {
	for _, material := range materials {
		if material != nil && material.binding.RuntimeInstanceID == instanceID {
			return true
		}
	}
	return false
}

var _ LifecycleBoundaryRegistries = (*PostgresLifecycleBoundaryRegistries)(nil)
var _ LifecycleBoundaryTransaction = (*postgresLifecycleRegistryTransaction)(nil)
