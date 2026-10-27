package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	health "github.com/keeptrue/sforum/apps/api/app/Support/Health"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	"github.com/keeptrue/sforum/apps/api/config"
)

type apiExtensionPlatform struct {
	infrastructure           *apiInfrastructure
	executableTrustService   *extensions.ExecutableTrustService
	frontendService          *extensions.FrontendService
	pageRegistry             *pages.Registry
	themeRuntime             *pages.ThemeRuntimeRegistry
	extensionService         *extensions.Service
	hostAPIGateway           *hostapi.Gateway
	extensionRuntime         extensionRuntime
	lifecycleRuntime         *extensionsruntime.Manager
	hostCacheRuntime         *productionHostCache
	pluginSessionState       *productionPluginSessionState
	lifecycleStack           *productionLifecycleStack
	productionSEO            *productionSEORegistry
	identityReviewStore      identityregistry.Store
	hostPlatform             *productionHostPlatform
	pluginRuntimeCoordinator *pluginRuntimeCoordinatorRuntime
	pluginRuntimeRecovery    *health.RecoveryRequirement
	// aiToolRefresh 是「插件运行时期望集已应用」到「AI 工具登记表重建」的槽位：
	// 本阶段创建并装进协调器，领域阶段拿到 AI 登记表之后 Arm 它。
	aiToolRefresh *aiToolRefreshHook
	// commandDeferred 收集目录冻结时尚未就绪的宿主目标（事件总线、AI 网关）。
	// 扩展平台阶段回填事件总线；AI 网关由领域阶段在拿到 Secret Store 后回填。
	commandDeferred              *protocolV2CommandRuntimeDeferred
	pluginRuntimeStopTimeout     time.Duration
	stopPluginRuntimeCoordinator func()
	closePluginRuntime           func()
}

func wireAPIExtensionPlatform(ctx context.Context, cfg config.Config, logger *slog.Logger, infrastructure *apiInfrastructure) (*apiExtensionPlatform, error) {
	queryCacheOwnership := newQueryResultCacheStageHandoff(infrastructure.queryResultCache, logger)
	defer queryCacheOwnership.CloseUnlessHandedOff()
	pool := infrastructure.pool
	databaseLeaseRegistry := infrastructure.databaseLeaseRegistry
	hostInstallationID := infrastructure.hostInstallationID
	queryCursorSecret := infrastructure.queryCursorSecret
	redisStorage := infrastructure.redisStorage
	sharedRedisClient := infrastructure.sharedRedisClient
	queryResultCache := infrastructure.queryResultCache
	optionCipher := infrastructure.optionCipher
	auditWriter := infrastructure.auditWriter
	optionsService := infrastructure.optionsService
	seoHostPolicy := infrastructure.seoHostPolicy
	identityStore := infrastructure.identityStore
	identityAuthorityGate := infrastructure.identityAuthorityGate
	moderationStore := infrastructure.moderationStore
	attachmentStore := infrastructure.attachmentStore
	attachmentService := infrastructure.attachmentService
	// 扩展附件引用（公开图标等）的写入口：Host Command 与卸载收口共用同一个实例。
	extensionReferences := extensionreference.NewStore(pool)
	extensionStore := infrastructure.extensionStore
	frontendTrustStore := infrastructure.frontendTrustStore
	executableTrustStore := infrastructure.executableTrustStore
	activationCoordinator := infrastructure.activationCoordinator
	jobClient := infrastructure.jobClient
	jobDispatcher := infrastructure.jobDispatcher

	executableTrustService := extensions.NewExecutableTrustService(extensionStore, executableTrustStore).
		WithAuditor(auditWriter).
		WithTTL(cfg.TrustChallengeTTL)
	frontendService := extensions.NewFrontendService(extensionStore, frontendTrustStore).
		WithAuditor(auditWriter).
		WithSafeMode(cfg.SafeMode).
		WithExecutableTrust(executableTrustService, cfg.V3TrustChallenges)
	// Page Registry：运行时主题 L0/L1，主题激活不重建 Nuxt、不写 current.json。
	pageRegistryStore := pages.NewPostgresStore(pool)
	pageRegistry := pages.NewRegistry(pageRegistryStore)
	if err := pageRegistry.RestoreBindings(ctx); err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("restore page provider bindings failed: %w", err)
	}
	themeRuntime := pages.NewThemeRuntimeRegistry()
	themeSiteName, _ := optionsService.SiteName(ctx)
	if strings.TrimSpace(themeSiteName) == "" {
		themeSiteName = cfg.AppName
	}
	pageRegistryAdapter := extensions.NewPageRegistryAdapter(pageRegistry).
		WithThemeRuntime(themeRuntime, themeSiteName, cfg.SupportedLocales)
	// S1-S9 插件会话状态：存储与签发权威只需要共享 Redis 客户端与持久安装身份，
	// 因此在扩展 Service 之前构造，好让禁用/卸载的擦除钩子能在构造期注入。
	pluginSessionState, err := newProductionPluginSessionState(hostInstallationID, sharedRedisClient)
	if err != nil {
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, err
	}
	// 先构造带 Cipher 的 Service，插件启动与 Host API 才能共享同一个解密设置源。
	// 主题激活走 Page Registry 同步路径；不再注入 theme_activate dispatcher。
	extensionService := extensions.NewServiceWithOptions(
		extensionStore, cfg.ExtensionRoot, cfg.BuiltinExtensionRoot,
		nil,
		extensions.WithAuditor(auditWriter),
		extensions.WithExternalExtensionRoots(cfg.ExternalExtensionRoots),
		extensions.WithExecutableTrust(executableTrustService, cfg.V3TrustChallenges),
		extensions.WithSafeMode(cfg.SafeMode),
		extensions.WithActivationCoordinator(activationCoordinator),
		// F2.4：同 id 升级且 digest 变化时吊销该扩展前端信任，要求重新授权。
		extensions.WithTrustRevoker(frontendService),
		// F4.5：启用时校验 manifest requiresFeatures。
		extensions.WithFeatureFlags(optionsService),
		// 扩展设置影响公开贡献时 bump site.public_surface_revision（Nuxt /t/** SWR varies）。
		extensions.WithPublicSurfaceRevisionBumper(optionsService),
		// 扩展 secret 设置与 web_options 共用 AES-GCM 密钥。
		extensions.WithCipher(optionCipher),
		// E6.1：禁用声明存储槽位的插件时，attachment.provider 从 plugin:<id> 回落 local。
		extensions.WithStorageSelectionClearer(attachmentService),
		// 卸载收口释放该插件拥有的附件引用（公开图标等），否则附件永不回收。
		extensions.WithExtensionAttachmentReferenceReaper(extensionReferences),
		// 运行时 Page Registry（主题激活同步注册页面贡献）。
		extensions.WithPageRegistry(pageRegistryAdapter),
		// S4/S5：扩展禁用/卸载后擦除它的会话状态（提交后 best-effort）。
		extensions.WithPluginSessionStateInvalidator(pluginSessionState),
	)
	// F2.2 Host API 与 ProtocolStarter 共用 extensionService；错误密钥不得降级读取 store。
	hostAPIService := hostapi.New(hostapi.Config{
		Settings: extensionService,
		Jobs:     &hostapi.RiverJobEnqueuer{Dispatcher: jobDispatcher, Contracts: extensionService},
		Auditor:  auditWriter,
	})
	hostAPIGateway := hostapi.NewGateway(hostAPIService)
	databaseCatalogBinder := postgresProtocolV2DatabaseCatalogBinder{
		pool: pool, gateway: hostAPIGateway,
		options: []hostapi.ProtocolV2DatabaseRuntimeOption{
			hostapi.WithProtocolV2DatabaseTraceSink(hostapi.NewSlogDatabaseTraceSink(logger)),
			hostapi.WithProtocolV2DatabaseQueryInvalidationJobs(jobDispatcher),
		},
	}
	queryTraceSink := hostapi.NewSlogQueryTraceSink(logger)
	queryAuthority := hostapi.NewPostgresProtocolV2QueryAuthorityResolver(pool)
	queryRuntime, err := hostapi.NewPostgresProtocolV2QueryRuntime(
		pool, queryAuthority,
		hostapi.WithProtocolV2QueryTraceSink(queryTraceSink),
	)
	if err == nil {
		err = hostAPIGateway.BindProtocolV2QueryRuntime(queryRuntime)
	}
	if err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("Host Query runtime setup failed: %w", err)
	}
	// 六域 Host Command 必须在首个插件 broker 注册前冻结；否则同一启动中
	// 已运行插件会看到缺失目录，而后启动插件看到另一份能力集合。
	commandDeferred, err := bindPostgresProtocolV2CommandRuntime(
		hostAPIGateway, pool, jobDispatcher, moderationStore,
		attachmentStore, attachmentService, extensionReferences, identityAuthorityGate,
	)
	if err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("Host Command runtime setup failed: %w", err)
	}
	extensionRuntime := bindAPIExtensionRuntime(
		extensionStore, hostAPIGateway, extensionService, executableTrustService, databaseLeaseRegistry,
	)
	if runtime, ok := extensionRuntime.(interface {
		SetStartPreparer(func(context.Context, extensions.Extension) error)
	}); ok {
		runtime.SetStartPreparer(protocolV2DatabaseStartPreparer(extensionStore, databaseCatalogBinder, cfg.SafeMode))
	}
	// 启用期把活动 runtime instance 绑到插件页面贡献：此前只有启动恢复路径写它，
	// 导致「启用后不重启 API」时插件页数据取不到（pages: exact plugin runtime unavailable）。
	if source, ok := extensionRuntime.(activeWorkerRuntimeSource); ok {
		pageRegistryAdapter.WithRuntimeInstances(pageRuntimeInstanceResolver{source: source})
	}
	hostAPIService.BindPluginJobAdmission(newPluginJobEnqueueAdmission(extensionRuntime))
	serviceAdmission := newPluginServiceProviderAdmission(extensionRuntime)
	hostAPIService.BindServiceProviderAdmission(serviceAdmission)
	// S9：会话状态服务必须在任何插件 broker 注册之前绑定，且与 Cache 服务同一形状。
	if err := pluginSessionState.BindGateway(cfg.SafeMode, serviceAdmission, hostAPIGateway); err != nil {
		extensionRuntime.Close(ctx)
		_ = hostAPIGateway.Close()
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("bind plugin session state runtime: %w", err)
	}
	// 登出/登录替换与撤销/设备数驱逐/改密的擦除钩子（S4/S5）。
	infrastructure.authSessions.WithPluginSessionStateInvalidator(pluginSessionState)
	infrastructure.identityStore.WithPluginSessionStateInvalidator(pluginSessionState)
	lifecycleRuntime, err := requireProductionExtensionRuntime(extensionRuntime)
	if err == nil && commandDeferred.Events != nil {
		// 事件总线随 Manager 就绪：命令目录已冻结，这里只回填发布目标。
		commandDeferred.Events.Set(extensionsruntime.NewExtensionEventPublisher(lifecycleRuntime))
	}
	if err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, err
	}
	hostCacheRuntime, err := bindProductionHostCache(
		hostInstallationID, logger, sharedRedisClient, cacheregistry.New(), serviceAdmission, hostAPIGateway,
	)
	if err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		_ = hostAPIGateway.Close()
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("Host Cache runtime setup failed: %w", err)
	}
	// P11 平台服务：SecretStore / HostHTTP / PluginFiles / SettingsLifecycle → Protocol V2。
	hostPlatform, err := bindProductionHostPlatform(cfg, pool, optionCipher, extensionStore, logger, hostAPIGateway)
	if err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		_ = hostAPIGateway.Close()
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("Host platform services setup failed: %w", err)
	}
	// 生产接线：后台设置保存/重置/导入/升级必须走 SettingsLifecycle。
	if hostPlatform != nil && hostPlatform.Settings != nil {
		extensionService.BindSettingsLifecycle(hostPlatform.Settings)
	}
	if hostPlatform != nil {
		attachmentService.WithSecretStore(hostPlatform.Secrets)
	}
	// P12 ops：RuntimeRollout / SystemTier / Privacy 绑定 PostgreSQL。
	p12Ops, err := bindProductionP12Ops(cfg, pool, logger)
	if err == nil && p12Ops != nil && p12Ops.Privacy != nil {
		// S5：隐私擦除消费者。Support/Privacy 此前没有任何消费者，插件会话状态也就
		// 永远不会随用户擦除而消失；这里注册一个宿主自有 contribution，其 erase hook
		// 遍历该用户的会话并逐个失效（报告只含计数，不含任何值）。
		if err := registerPluginSessionStatePrivacyContribution(p12Ops.Privacy, pluginSessionState, identityStore); err != nil {
			sharedRedisClient.Close()
			if closeErr := redisStorage.Close(); closeErr != nil {
				logger.Warn("redis session storage close failed", "error", closeErr)
			}
			pool.Close()
			return nil, fmt.Errorf("register plugin session state privacy contribution: %w", err)
		}
	}
	if err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		_ = hostAPIGateway.Close()
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("P12 ops services setup failed: %w", err)
	}
	// SystemTier：在任何 system extension 代码启动前决定加载顺序；Safe Mode 直接绕过。
	var systemTierOrder []string
	if p12Ops != nil && p12Ops.SystemTier != nil {
		if order, tierErr := p12Ops.SystemTier.LoadOrder(ctx, cfg.SafeMode); tierErr != nil {
			if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
				logger.Warn("job dispatcher stop failed", "error", stopErr)
			}
			extensionRuntime.Close(ctx)
			_ = hostAPIGateway.Close()
			sharedRedisClient.Close()
			if closeErr := redisStorage.Close(); closeErr != nil {
				logger.Warn("redis session storage close failed", "error", closeErr)
			}
			pool.Close()
			return nil, fmt.Errorf("system tier load order: %w", tierErr)
		} else if cfg.SafeMode && order != nil {
			if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
				logger.Warn("job dispatcher stop failed", "error", stopErr)
			}
			extensionRuntime.Close(ctx)
			_ = hostAPIGateway.Close()
			sharedRedisClient.Close()
			if closeErr := redisStorage.Close(); closeErr != nil {
				logger.Warn("redis session storage close failed", "error", closeErr)
			}
			pool.Close()
			return nil, fmt.Errorf("bootstrap: safe mode must bypass system tier (got %d members)", len(order))
		} else if !cfg.SafeMode && len(order) > 0 {
			logger.Info("system tier load order resolved before system extension start",
				"members", len(order))
			systemTierOrder = make([]string, 0, len(order))
			for _, member := range order {
				systemTierOrder = append(systemTierOrder, member.ExtensionID)
			}
		}
		// RuntimeRollout remains deliberately unbound from lifecycle completion.
		// The current lifecycle coordinator reaches terminal success before this
		// P12 service can collect node-bound health evidence; binding it here would
		// falsely present a post-hoc status record as an atomic canary gate.
	}
	if err := bindProtocolV2ProviderBroker(hostAPIGateway, lifecycleRuntime); err != nil {
		extensionRuntime.Close(ctx)
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, err
	}
	lifecycleRuntime.WithActivation(activationCoordinator, extensions.NewActivationBootID())
	lifecycleMigrationEngine := extensionsruntime.NewPostgresLifecycleMigrationEngine(pool, nil)
	lifecycleDatabaseDisposition := extensionsruntime.NewPostgresExtensionDatabaseDisposition(pool)
	lifecycleStack, err := newProductionLifecycleStack(productionLifecycleStackConfig{
		Pool: pool, Store: extensionStore, Features: optionsService,
		Trust: executableTrustService, Runtime: lifecycleRuntime, Pages: pageRegistry,
		ThemeRuntime: themeRuntime, PageSiteName: themeSiteName, PageLocales: cfg.SupportedLocales,
		Services: hostAPIGateway.ProtocolV2ServiceRegistry(), Caches: hostCacheRuntime.Registry, River: jobClient,
		ExtensionRoot: cfg.ExtensionRoot, QueryCursorSecret: queryCursorSecret, MigrationEngine: lifecycleMigrationEngine,
		Database: lifecycleDatabaseDisposition, SafeMode: cfg.SafeMode,
	})
	if err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("extension lifecycle setup failed: %w", err)
	}
	identityReviewStore, ok := lifecycleStack.IdentityStore.(identityregistry.Store)
	if !ok {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		_ = hostAPIGateway.Close()
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("extension lifecycle identity store does not expose Host review operations")
	}
	frontendService.WithPublicComponentAdmission(lifecycleStack.ComponentRegistry)
	if err := lifecycleStack.bindService(extensionService); err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("bind extension lifecycle service failed: %w", err)
	}
	// Query Registry 必须在首个插件 broker 注册前绑定。它复用 lifecycle
	// stack 的不可变 Core snapshot，并让 caller/provider 都经过 exact-runtime gate。
	if _, err := bindProductionQueryRegistryWithCache(
		lifecycleStack.QueryRegistry,
		lifecycleStack.QueryCoreCatalog,
		queryRuntime,
		identityStore,
		lifecycleRuntime,
		hostAPIGateway,
		queryTraceSink,
		queryResultCache.Cache(),
		withProductionQueryRegistryConsentGate(
			newProductionQueryConsentGate(queryAuthority, auditWriter, sharedRedisClient),
		),
	); err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		_ = hostAPIGateway.Close()
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("Query Registry production setup failed: %w", err)
	}
	productionSEO, err := bindProductionSEORegistry(
		lifecycleStack.SEORegistry,
		lifecycleRuntime,
		seoHostPolicy,
	)
	if err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		_ = hostAPIGateway.Close()
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("SEO Registry production setup failed: %w", err)
	}
	// 把已构造的 extensionService 接到 Host API 能力/权限解析（避免循环构造）。
	hostAPIService.BindCapabilitySource(extensionService)
	hostAPIService.BindExtensionInventory(extensions.HostInventoryAdapter{Service: extensionService})
	hostAPIGateway.BindCommandCapabilitySource(extensionService)
	// SecretService.Put/Clear 与 settings.own 同一能力源；平台服务先于 extensionService
	// 构造，所以写半边在这里补绑，未绑时写入 fail closed。
	hostPlatform.SecretV2.BindCapabilitySource(extensionService)
	hostAPIService.BindPermissions(identityPermissionAdapter{store: identityStore})
	// Identity user-field digests are installation-bound. GetUserSafe resolves
	// extension declared_fields through the Host-owned store with live actor
	// permission checks; core safe fields remain available without the store.
	userFieldDigestKey, err := deriveIdentityUserFieldDigestKey(cfg.SessionHashSecret, hostInstallationID)
	if err != nil {
		return nil, fmt.Errorf("derive identity user-field digest key: %w", err)
	}
	userFieldValueStore, err := identity.NewPostgresIdentityUserFieldValueStore(
		pool, lifecycleStack.IdentityRegistry, userFieldDigestKey,
	)
	if err != nil {
		return nil, fmt.Errorf("create identity user-field value store: %w", err)
	}
	hostAPIService.BindUsers(identityUserAdapter{store: identityStore, fields: userFieldValueStore})
	aiToolRefresh := &aiToolRefreshHook{}
	runtimeState, err := restoreAPIExtensionPlatform(ctx, cfg, logger, apiExtensionRestoreInput{
		infrastructure:         infrastructure,
		extensionService:       extensionService,
		frontendService:        frontendService,
		executableTrustService: executableTrustService,
		databaseCatalogBinder:  databaseCatalogBinder,
		extensionRuntime:       extensionRuntime,
		lifecycleRuntime:       lifecycleRuntime,
		lifecycleStack:         lifecycleStack,
		systemTierOrder:        systemTierOrder,
		aiToolRefresh:          aiToolRefresh,
	})
	if err != nil {
		return nil, err
	}
	_ = queryCacheOwnership.HandOff()
	return &apiExtensionPlatform{
		infrastructure:               infrastructure,
		executableTrustService:       executableTrustService,
		frontendService:              frontendService,
		pageRegistry:                 pageRegistry,
		themeRuntime:                 themeRuntime,
		extensionService:             extensionService,
		hostAPIGateway:               hostAPIGateway,
		commandDeferred:              &commandDeferred,
		extensionRuntime:             extensionRuntime,
		lifecycleRuntime:             lifecycleRuntime,
		hostCacheRuntime:             hostCacheRuntime,
		pluginSessionState:           pluginSessionState,
		lifecycleStack:               lifecycleStack,
		productionSEO:                productionSEO,
		identityReviewStore:          identityReviewStore,
		hostPlatform:                 hostPlatform,
		pluginRuntimeCoordinator:     runtimeState.pluginRuntimeCoordinator,
		pluginRuntimeRecovery:        runtimeState.pluginRuntimeRecovery,
		pluginRuntimeStopTimeout:     runtimeState.pluginRuntimeStopTimeout,
		stopPluginRuntimeCoordinator: runtimeState.stopPluginRuntimeCoordinator,
		closePluginRuntime:           runtimeState.closePluginRuntime,
		aiToolRefresh:                runtimeState.aiToolRefresh,
	}, nil
}

// pageRuntimeInstanceResolver 把活动 runtime instance 解析成页面贡献需要的 id。
// 只在活动实例与扩展的版本/摘要**精确匹配**时返回 id；否则返回 false，
// 让贡献保持空 runtime instance（请求期由注册表 admission 拒绝，不伪造身份）。
type pageRuntimeInstanceResolver struct {
	source activeWorkerRuntimeSource
}

func (r pageRuntimeInstanceResolver) ActiveRuntimeInstanceID(extensionID, version, packageDigest string) (string, bool) {
	if r.source == nil || extensionID == "" {
		return "", false
	}
	snapshot, err := r.source.ActiveRuntimeInstance(extensionID)
	if err != nil || !snapshot.Active || snapshot.Identity.InstanceID == "" {
		return "", false
	}
	if version != "" && snapshot.ExtensionVersion != version {
		return "", false
	}
	if packageDigest != "" && snapshot.ArtifactDigest != packageDigest {
		return "", false
	}
	return snapshot.Identity.InstanceID, true
}
