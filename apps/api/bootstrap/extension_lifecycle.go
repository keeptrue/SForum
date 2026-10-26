package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
	extensioncomposition "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionComposition"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	lifecyclerecovery "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/LifecycleRecovery"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

var errProductionLifecycleDependency = errors.New("bootstrap: production extension lifecycle dependency unavailable")

type productionLifecycleStackConfig struct {
	Pool         *pgxpool.Pool
	Store        *extensions.PostgresStore
	Features     extensions.FeatureFlagSource
	Trust        *extensions.ExecutableTrustService
	Runtime      *extensionsruntime.Manager
	Pages        *pages.Registry
	ThemeRuntime *pages.ThemeRuntimeRegistry
	PageSiteName string
	PageLocales  []string
	Services     *hostapi.ServiceRegistry
	Caches       *cacheregistry.Registry
	// IdentityStore is a test seam. Production leaves it nil and constructs a
	// PostgreSQL store with extensions.ValidateStoredTrustImpact so legacy
	// adoption has an explicit instance-scoped integrity dependency.
	IdentityStore identityregistry.PublicationStore
	// NotificationRegistry is a test seam. Production leaves it nil so the
	// lifecycle stack binds durable descriptor persistence to Pool.
	NotificationRegistry *notifications.Registry
	River                hostapi.PluginJobLifecycleRiverClient
	ExtensionRoot        string
	QueryCursorSecret    []byte
	MigrationEngine      extensionsruntime.LifecycleMigrationEngine
	Database             extensionsruntime.ExtensionDatabaseDisposition
	SafeMode             bool
}

// productionLifecycleStack 保留组装后的具体实例，避免 lifecycle 的不同边界
// 意外各自创建 Manager、journal 或 schedule admission registry。
type productionLifecycleStack struct {
	Repository           *extensions.PostgresLifecycleRepository
	RuntimeManager       *extensionsruntime.Manager
	Runtime              *extensionsruntime.ExactLifecycleCoordinatorRuntimeAdapter
	Preflight            *extensionsruntime.ProductionLifecycleBoundaryPreflight
	StaticPreflight      extensions.LifecycleStaticPreflight
	MigrationEngine      extensionsruntime.LifecycleMigrationEngine
	Migrations           *extensionsruntime.ProductionLifecycleBoundaryMigrations
	Schedules            *supportjobs.PluginScheduleAdmissionRegistry
	JobStore             *hostapi.PostgresPluginJobLifecycleStore
	JobCoordinator       *hostapi.PluginJobLifecycleCoordinator
	Jobs                 *extensionsruntime.PostgresLifecycleBoundaryJobs
	RouteRegistry        *routes.Registry
	RouteSchemas         *extensionopenapi.RouteSchemaPublication
	ComponentRegistry    *extensionsruntime.ComponentRegistry
	ComponentComposition *extensionsruntime.ProductionComponentComposition
	ComponentInspector   extensioncomposition.Inspector
	AssetRegistry        *assetregistry.Registry
	CacheRegistry        *cacheregistry.Registry
	IdentityRegistry     *identityregistry.Registry
	IdentityStore        identityregistry.PublicationStore
	SessionPolicyStore   *identity.PostgresIdentitySessionPolicyStore
	// QueryRegistry 与 QueryCoreCatalog 在进程启动时一次性构造；不得每请求重建，
	// 也不得从 mutable Store 再生成 Core publication。
	QueryRegistry        *queryregistry.Registry
	QueryCoreCatalog     *hostapi.QueryRegistryCoreCatalog
	SEORegistry          *seoregistry.Registry
	NavigationRegistry   *navigationregistry.Registry
	NotificationRegistry *notifications.Registry
	// ContentRegistry is the P10 block/shortcode/embed/node/mark graph.
	ContentRegistry *contentregistry.Registry
	// MediaRegistry is the P10 MIME/processor/variant pipeline graph.
	MediaRegistry *mediaregistry.Registry
	// EditorRegistry is the P10 Tiptap node/mark/command/toolbar graph.
	EditorRegistry *editorregistry.Registry
	// EntityRegistry is the P10 Entity Type / Taxonomy / Field Schema graph.
	EntityRegistry     *entityregistry.Registry
	RouteProviders     *routes.ProviderSelectionAPI
	ProviderSlots      *extensionsruntime.ProviderSlotSelectionAPI
	RegistryRepository *extensionsruntime.PostgresLifecycleRegistryPublicationRepository
	Registries         *extensionsruntime.PostgresLifecycleBoundaryRegistries
	State              *extensionsruntime.PostgresLifecycleBoundaryState
	PublicationJournal *extensionsruntime.PostgresLifecycleBoundaryPublicationJournal
	Cleanup            *extensionsruntime.PostgresLifecycleBoundaryCleanup
	Database           extensionsruntime.ExtensionDatabaseDisposition
	CleanupPurger      extensionsruntime.LifecycleBoundaryCleanupPurger
	CleanupFinalizer   *extensionsruntime.PostgresLifecycleBoundaryCleanupFinalizer
	Boundary           *extensionsruntime.ComposedLifecycleHostBoundary
	Host               *extensionsruntime.ExactLifecycleCoordinatorHost
	Coordinator        *extensions.LifecycleCoordinator
	// Trust 是 executable trust 服务引用，供开发期信任会话（仅 development）注入。
	Trust *extensions.ExecutableTrustService
}

func requireProductionExtensionRuntime(runtime extensionRuntime) (*extensionsruntime.Manager, error) {
	manager, ok := runtime.(*extensionsruntime.Manager)
	if !ok || manager == nil {
		return nil, fmt.Errorf("%w: exact runtime manager", errProductionLifecycleDependency)
	}
	return manager, nil
}

func newProductionLifecycleStack(config productionLifecycleStackConfig) (*productionLifecycleStack, error) {
	if config.Pool == nil || config.Store == nil || config.Features == nil || config.Trust == nil ||
		config.Runtime == nil || config.Pages == nil || config.Services == nil || config.Caches == nil || config.River == nil ||
		strings.TrimSpace(config.ExtensionRoot) == "" || len(config.QueryCursorSecret) != 32 ||
		config.MigrationEngine == nil || config.Database == nil {
		return nil, errProductionLifecycleDependency
	}

	runtime, err := config.Runtime.NewExactLifecycleCoordinatorRuntimeAdapter()
	if err != nil {
		return nil, fmt.Errorf("%w: exact coordinator runtime: %v", errProductionLifecycleDependency, err)
	}
	cleanupPurger, err := newProductionLifecycleCleanupPurger(
		config.Pool,
		config.Runtime,
		config.ExtensionRoot,
		config.Database,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: cleanup purger: %v", errProductionLifecycleDependency, err)
	}
	repository := extensions.NewPostgresLifecycleRepository(config.Pool)
	migrations := extensionsruntime.NewProductionLifecycleBoundaryMigrations(config.Pool, config.MigrationEngine)
	preflight := extensionsruntime.NewProductionLifecycleBoundaryPreflight(
		extensionsruntime.ProductionLifecycleBoundaryPreflightConfig{
			Pool: config.Pool, Inventory: config.Store, Features: config.Features,
			Compatibility: config.Runtime, Trust: config.Trust, Migrations: migrations,
		},
	)
	staticPreflight := func(
		ctx context.Context,
		operation extensions.LifecycleMachineOperation,
		source *extensions.Extension,
		target extensions.Extension,
	) error {
		return preflight.CheckLifecycleStaticPreflight(ctx, extensionsruntime.LifecycleStaticPreflightRequest{
			Operation: operation, SourceExtension: source, TargetExtension: target,
		})
	}

	journal := extensionsruntime.NewPostgresLifecycleBoundaryPublicationJournal(config.Pool)
	// 当前只存在 admission owner，不虚构尚未实现的 production schedule trigger。
	schedules := supportjobs.NewPluginScheduleAdmissionRegistry()
	jobStore := hostapi.NewPostgresPluginJobLifecycleStore(config.Pool, config.River)
	jobCoordinator := &hostapi.PluginJobLifecycleCoordinator{Store: jobStore}
	jobs := extensionsruntime.NewPostgresLifecycleBoundaryJobs(
		extensionsruntime.PostgresLifecycleBoundaryJobsConfig{
			Pool: config.Pool, Runtime: config.Runtime, Schedules: schedules,
			Coordinator: jobCoordinator, Trust: config.Trust, Journal: journal,
		},
	)

	// Core 路由是每个进程第一个不可变快照。Safe Mode 仍使用这份 Host
	// catalog，但后续 lifecycle 发布即使携带第三方路由也会被 Registry 过滤。
	routeRegistry := routes.NewRegistry()
	if _, err := routeRegistry.Publish(routes.Publication{
		Core: routes.CoreRouteCatalog(), SafeMode: config.SafeMode,
	}); err != nil {
		return nil, fmt.Errorf("%w: publish core route catalog: %v", errProductionLifecycleDependency, err)
	}
	routeSchemas, err := extensionopenapi.NewRouteSchemaContractPublication(
		extensionopenapi.CoreOperations(routes.CoreRouteCatalog()),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: create route schema publication: %v", errProductionLifecycleDependency, err)
	}
	componentRegistry := extensionsruntime.NewComponentRegistry()
	// 生产 Component Composition：Core fallback + Manager admission，挂在 lifecycle stack 上。
	componentComposition, err := extensionsruntime.NewProductionComponentComposition(
		extensionsruntime.ProductionComponentCompositionConfig{
			Registry: componentRegistry,
			Manager:  config.Runtime,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("%w: create component composition: %v", errProductionLifecycleDependency, err)
	}
	componentInspector := extensionsruntime.NewComponentCompositionInspector(componentRegistry, componentComposition)
	if config.ThemeRuntime != nil {
		// Theme L1 渲染后 preface 插件组件 HTML；无贡献时 ComposePageHTML 返回空切片。
		config.ThemeRuntime.WithPageComposition(componentComposition)
	}
	// 全进程唯一 Asset Registry：生命周期恢复/发布与 FrontendService 请求读取共享。
	assetRegistry := assetregistry.New()
	cacheRegistry := config.Caches
	if config.SafeMode {
		snapshot := cacheRegistry.Snapshot()
		if _, err := cacheRegistry.ReplaceAllIfRevision(snapshot.Revision, snapshot.Publications, true); err != nil {
			return nil, fmt.Errorf("%w: enter cache registry safe mode: %v", errProductionLifecycleDependency, err)
		}
	}
	// Query Core catalog/cost policy 在进程启动首个 snapshot 密封发布：推荐
	// cost max 500、hard max 2000；cursor key 来自安装级 Host secret 派生，不由插件声明。
	// 生命周期 restore/Safe Mode 通过 coreLifecycleQueryPublications 保留这份 Core。
	queryRegistry, queryCoreCatalog, err := hostapi.NewQueryRegistryCoreRegistry(hostapi.QueryRegistryCoreOptions{
		CursorSecret: append([]byte(nil), config.QueryCursorSecret...),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: create core query registry: %v", errProductionLifecycleDependency, err)
	}
	if config.SafeMode {
		// Safe Mode 从首个 snapshot 起过滤第三方，但不得删除或改写已密封 Core artifact。
		snapshot := queryRegistry.Snapshot()
		if _, err := queryRegistry.ReplaceAllIfRevision(snapshot.Revision, snapshot.Publications, true); err != nil {
			return nil, fmt.Errorf("%w: enter query registry safe mode: %v", errProductionLifecycleDependency, err)
		}
	}
	seoRegistry := seoregistry.New()
	if config.SafeMode {
		snapshot := seoRegistry.Snapshot()
		if _, err := seoRegistry.ReplaceAllIfRevision(snapshot.Revision, snapshot.Publications, true); err != nil {
			return nil, fmt.Errorf("%w: enter SEO registry safe mode: %v", errProductionLifecycleDependency, err)
		}
	}
	// Navigation/Region：进程启动先发布 Host Core 目标；Safe Mode 从首个 snapshot 起
	// 过滤第三方，但不得删除 Core 结构身份。
	navigationRegistry := navigationregistry.New()
	if _, err := navigationRegistry.Publish(navigationregistry.CorePublication()); err != nil {
		return nil, fmt.Errorf("%w: publish core navigation catalog: %v", errProductionLifecycleDependency, err)
	}
	if config.SafeMode {
		snapshot := navigationRegistry.Snapshot()
		if _, err := navigationRegistry.ReplaceAllWithSafeModeIfRevision(snapshot.Revision, snapshot.Publications, true); err != nil {
			return nil, fmt.Errorf("%w: enter navigation registry safe mode: %v", errProductionLifecycleDependency, err)
		}
	}
	// 构造阶段只建立进程内 Safe Mode 快照；durable descriptor 退役由启动
	// lifecycle restore 在拿到完整 enabled inventory 后统一提交。
	notificationRegistry := config.NotificationRegistry
	if notificationRegistry == nil {
		notificationRegistry = notifications.NewPersistentRegistry(config.Pool, config.SafeMode)
	} else if config.SafeMode {
		if _, err := notificationRegistry.SetSafeMode(context.Background(), true); err != nil {
			return nil, fmt.Errorf("%w: enter notification registry safe mode: %v", errProductionLifecycleDependency, err)
		}
	}
	// Content Registry：P10 声明图；Safe Mode 从首个 snapshot 起拒绝第三方。
	contentRegistry := contentregistry.New()
	forumPostBodyCore, err := contentregistry.ForumPostBodyCorePublication()
	if err != nil {
		return nil, fmt.Errorf("%w: create Forum post-body content target: %v", errProductionLifecycleDependency, err)
	}
	if _, err := contentRegistry.Publish(forumPostBodyCore); err != nil {
		return nil, fmt.Errorf("%w: publish Forum post-body content target: %v", errProductionLifecycleDependency, err)
	}
	if config.SafeMode {
		snapshot := contentRegistry.Snapshot()
		if _, err := contentRegistry.ReplaceAllIfRevision(snapshot.Revision, snapshot.Publications, true); err != nil {
			return nil, fmt.Errorf("%w: enter content registry safe mode: %v", errProductionLifecycleDependency, err)
		}
	}
	// Media Pipeline Registry：P10 声明图；Safe Mode 从首个 snapshot 起拒绝第三方。
	mediaRegistry := mediaregistry.New()
	if config.SafeMode {
		snapshot := mediaRegistry.Snapshot()
		if _, err := mediaRegistry.ReplaceAllIfRevision(snapshot.Revision, snapshot.Publications, true); err != nil {
			return nil, fmt.Errorf("%w: enter media registry safe mode: %v", errProductionLifecycleDependency, err)
		}
	}
	// Editor Registry：P10 Tiptap trusted L2；Safe Mode 从首个 snapshot 起拒绝第三方。
	editorRegistry := editorregistry.New()
	if config.SafeMode {
		snapshot := editorRegistry.Snapshot()
		if _, err := editorRegistry.ReplaceAllIfRevision(snapshot.Revision, snapshot.Publications, true); err != nil {
			return nil, fmt.Errorf("%w: enter editor registry safe mode: %v", errProductionLifecycleDependency, err)
		}
	}
	// Entity Registry：P10 Entity/Taxonomy/Field；Safe Mode 从首个 snapshot 起拒绝第三方。
	entityRegistry := entityregistry.New()
	if config.SafeMode {
		snapshot := entityRegistry.Snapshot()
		if _, err := entityRegistry.ReplaceAllIfRevision(snapshot.Revision, snapshot.Publications, true); err != nil {
			return nil, fmt.Errorf("%w: enter entity registry safe mode: %v", errProductionLifecycleDependency, err)
		}
	}
	// Identity root policy and permanent leaf ownership must converge from the
	// durable PostgreSQL ledger before the process-local graph becomes visible.
	// Default PostgreSQL store binds the production TrustImpact digest verifier
	// so legacy adoption cannot run under a package-global or unset integrity path.
	// Test seams that supply IdentityStore keep their injected double unchanged.
	identityRegistry := identityregistry.New()
	identityStore := config.IdentityStore
	var sessionPolicyStore *identity.PostgresIdentitySessionPolicyStore
	if identityStore == nil {
		sessionPolicyStore, err = identity.NewPostgresIdentitySessionPolicyStore(config.Pool, identityRegistry)
		if err != nil {
			return nil, fmt.Errorf("%w: create session policy store: %v", errProductionLifecycleDependency, err)
		}
		identityStore = identityregistry.NewPostgresStoreWithDependencies(
			config.Pool,
			identityregistry.PostgresStoreDependencies{
				StoredTrustImpactValidator: extensions.ValidateStoredTrustImpact,
				SessionPolicyInvalidator:   sessionPolicyStore,
			},
		)
	}
	routeProviders := routes.NewProviderSelectionAPI(
		routeRegistry,
		routes.NewPostgresProviderSelectionStore(config.Pool),
	)
	providerSlotStore := extensionsruntime.NewPostgresProviderSlotSelectionStore(config.Pool)
	config.Runtime.BindProviderSlotSelections(providerSlotStore)
	providerSlots := config.Runtime.ProviderSlotSelections()
	if providerSlots == nil {
		return nil, fmt.Errorf("%w: provider slot selections", errProductionLifecycleDependency)
	}
	registryRepository := extensionsruntime.NewPostgresLifecycleRegistryPublicationRepository(config.Pool)
	identityRecovery := lifecyclerecovery.NewPostgresIdentityStartupRecovery(config.Pool, identityStore)
	assetAuthority := extensionsruntime.NewPostgresLifecycleAssetAuthority(config.Pool, repository)
	registries := extensionsruntime.NewPostgresLifecycleBoundaryRegistries(
		extensionsruntime.LifecycleRegistryBoundaryConfig{
			Repository: registryRepository, Manager: config.Runtime, Pages: config.Pages,
			ThemeRuntime: config.ThemeRuntime, PageSiteName: config.PageSiteName, PageLocales: config.PageLocales,
			Routes: routeRegistry, RouteSchemas: routeSchemas, Services: config.Services,
			Components: componentRegistry, ComponentComposition: componentComposition,
			Assets: assetRegistry, Caches: cacheRegistry, Queries: queryRegistry,
			SEO: seoRegistry, Identity: identityRegistry, IdentityStore: identityStore,
			IdentityRecovery: identityRecovery,
			Navigation:       navigationRegistry, Notifications: notificationRegistry, Content: contentRegistry, Media: mediaRegistry, Editor: editorRegistry,
			Entity: entityRegistry, AssetAuthority: assetAuthority, AssetAdmission: config.Trust,
		},
	)
	state := extensionsruntime.NewPostgresLifecycleBoundaryState(config.Store)
	cleanup := extensionsruntime.NewPostgresLifecycleBoundaryCleanup(config.Pool)
	cleanupFinalizer := extensionsruntime.NewPostgresLifecycleBoundaryCleanupFinalizer(
		config.Pool,
		cleanupPurger,
	)
	boundary := extensionsruntime.NewComposedLifecycleHostBoundary(
		extensionsruntime.ComposedLifecycleHostBoundaryDependencies{
			Runtime: config.Runtime, Preflight: preflight, Migrations: migrations,
			Jobs: jobs, Registries: registries, State: state, Journal: journal, Cleanup: cleanup,
		},
	)
	host := extensionsruntime.NewExactLifecycleCoordinatorHost(config.Runtime, boundary)
	coordinator := extensions.NewLifecycleCoordinator(repository, runtime, host)

	return &productionLifecycleStack{
		Repository: repository, RuntimeManager: config.Runtime, Runtime: runtime,
		Trust:     config.Trust,
		Preflight: preflight, StaticPreflight: staticPreflight,
		MigrationEngine: config.MigrationEngine, Migrations: migrations,
		Schedules: schedules, JobStore: jobStore, JobCoordinator: jobCoordinator, Jobs: jobs,
		RouteRegistry: routeRegistry, RouteSchemas: routeSchemas, ComponentRegistry: componentRegistry,
		ComponentComposition: componentComposition, ComponentInspector: componentInspector,
		AssetRegistry: assetRegistry, CacheRegistry: cacheRegistry,
		IdentityRegistry: identityRegistry, IdentityStore: identityStore,
		SessionPolicyStore: sessionPolicyStore,
		QueryRegistry:      queryRegistry, QueryCoreCatalog: queryCoreCatalog,
		SEORegistry: seoRegistry, NavigationRegistry: navigationRegistry, NotificationRegistry: notificationRegistry,
		ContentRegistry: contentRegistry, MediaRegistry: mediaRegistry, EditorRegistry: editorRegistry,
		EntityRegistry: entityRegistry, RouteProviders: routeProviders,
		ProviderSlots:      providerSlots,
		RegistryRepository: registryRepository, Registries: registries,
		State: state, PublicationJournal: journal, Cleanup: cleanup,
		Database: config.Database, CleanupPurger: cleanupPurger,
		CleanupFinalizer: cleanupFinalizer, Boundary: boundary,
		Host: host, Coordinator: coordinator,
	}, nil
}

func (s *productionLifecycleStack) bindService(service *extensions.Service) error {
	if s == nil || service == nil || s.Coordinator == nil || s.StaticPreflight == nil ||
		s.Repository == nil || s.CleanupFinalizer == nil || s.RouteProviders == nil || s.ProviderSlots == nil ||
		s.ComponentRegistry == nil || s.AssetRegistry == nil || s.CacheRegistry == nil || s.QueryRegistry == nil ||
		s.QueryCoreCatalog == nil || s.SEORegistry == nil || s.NavigationRegistry == nil ||
		s.ContentRegistry == nil || s.MediaRegistry == nil || s.EditorRegistry == nil || s.EntityRegistry == nil ||
		s.IdentityRegistry == nil || s.IdentityStore == nil || s.Registries == nil {
		return errProductionLifecycleDependency
	}
	extensions.WithLifecycleCoordinator(s.Coordinator, s.StaticPreflight, s.Repository)(service)
	extensions.WithLifecycleInspectionRepository(s.Repository)(service)
	extensions.WithLifecycleCleanupFinalizer(adaptLifecycleCleanupFinalizer(s.CleanupFinalizer))(service)
	// 卸载清理收敛：周期 sweep 与免键收尾端点共用同一 pending tombstone 读取边界。
	extensions.WithLifecycleCleanupSweepSource(s.Repository)(service)
	extensions.WithRouteProviderSelectionInvalidator(s.RouteProviders)(service)
	extensions.WithProviderSlotSelectionInvalidator(s.ProviderSlots)(service)
	extensions.WithComponentRegistry(s.ComponentRegistry)(service)
	service.BindRuntimeQueryPublications(s.Registries)
	extensions.WithRuntimeCachePublications(s.Registries)(service)
	extensions.WithRuntimeContentPublications(s.Registries)(service)
	extensions.WithRuntimeEditorPublications(s.Registries)(service)
	service.BindRuntimeIdentityPublications(s.Registries)
	return nil
}

// bindAssetRegistryConsumers runs only after authoritative lifecycle startup
// restore succeeds. Theme/Page restoration must not publish a second Asset graph
// from the unreconciled Store view.
func (s *productionLifecycleStack) bindAssetRegistryConsumers(
	service *extensions.Service,
	frontend *extensions.FrontendService,
	trust *extensions.ExecutableTrustService,
) error {
	if s == nil || s.AssetRegistry == nil || service == nil || frontend == nil || trust == nil {
		return errProductionLifecycleDependency
	}
	service.BindAssetRegistry(s.AssetRegistry)
	frontend.WithPublicAssetRegistry(s.AssetRegistry)
	trust.WithPublicAssetRegistry(s.AssetRegistry)
	return nil
}

func adaptLifecycleCleanupFinalizer(
	finalizer extensionsruntime.LifecycleBoundaryCleanupFinalizer,
) extensions.LifecycleCleanupFinalizer {
	return func(ctx context.Context, operationID int64) (extensions.LifecycleCleanupFinalization, error) {
		if finalizer == nil {
			return extensions.LifecycleCleanupFinalization{}, errProductionLifecycleDependency
		}
		result, err := finalizer.FinalizeLifecycleHostCleanup(ctx, operationID)
		if err != nil {
			return extensions.LifecycleCleanupFinalization{}, err
		}
		return extensions.LifecycleCleanupFinalization{
			OperationID: result.OperationID, Status: result.Status,
			PhysicalPurgeComplete: result.PhysicalPurgeCompleted,
		}, nil
	}
}

type extensionRuntimeReconciler interface {
	Reconcile(context.Context, []extensions.Extension)
}

func reconcileAPIExtensionRuntime(
	ctx context.Context,
	safeMode bool,
	store extensions.Store,
	runtime extensionRuntimeReconciler,
) ([]extensions.Extension, error) {
	if store == nil || runtime == nil {
		return nil, fmt.Errorf("%w: runtime reconciliation", errProductionLifecycleDependency)
	}
	items, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	if safeMode {
		items = nil
	}
	runtime.Reconcile(ctx, items)
	return items, nil
}
