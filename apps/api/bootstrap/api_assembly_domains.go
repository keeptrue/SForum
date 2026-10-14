package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	httpserver "github.com/zhuchunshu/sforum/apps/api/app/Http"
	notificationscontroller "github.com/zhuchunshu/sforum/apps/api/app/Http/Controllers/Notifications"
	aireplyjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/AIReply"
	attachmentjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/Attachments"
	aireply "github.com/zhuchunshu/sforum/apps/api/app/Models/AIReply"
	aitools "github.com/zhuchunshu/sforum/apps/api/app/Models/AITools"
	apitokens "github.com/zhuchunshu/sforum/apps/api/app/Models/APITokens"
	adminoverview "github.com/zhuchunshu/sforum/apps/api/app/Models/AdminOverview"
	attachments "github.com/zhuchunshu/sforum/apps/api/app/Models/Attachments"
	entitymeta "github.com/zhuchunshu/sforum/apps/api/app/Models/EntityMeta"
	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	forum "github.com/zhuchunshu/sforum/apps/api/app/Models/Forum"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	moderation "github.com/zhuchunshu/sforum/apps/api/app/Models/Moderation"
	notifications "github.com/zhuchunshu/sforum/apps/api/app/Models/Notifications"
	options "github.com/zhuchunshu/sforum/apps/api/app/Models/Options"
	pageviewmodels "github.com/zhuchunshu/sforum/apps/api/app/Models/PageViewModels"
	profile "github.com/zhuchunshu/sforum/apps/api/app/Models/Profile"
	sitechrome "github.com/zhuchunshu/sforum/apps/api/app/Models/SiteChrome"
	systemupdates "github.com/zhuchunshu/sforum/apps/api/app/Models/SystemUpdates"
	webhooks "github.com/zhuchunshu/sforum/apps/api/app/Models/Webhooks"
	providers "github.com/zhuchunshu/sforum/apps/api/app/Providers"
	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
	authsupport "github.com/zhuchunshu/sforum/apps/api/app/Support/Auth"
	cache "github.com/zhuchunshu/sforum/apps/api/app/Support/Cache"
	contentregistry "github.com/zhuchunshu/sforum/apps/api/app/Support/ContentRegistry"
	appevents "github.com/zhuchunshu/sforum/apps/api/app/Support/Events"
	extensionsruntime "github.com/zhuchunshu/sforum/apps/api/app/Support/Extensions"
	health "github.com/zhuchunshu/sforum/apps/api/app/Support/Health"
	hostapi "github.com/zhuchunshu/sforum/apps/api/app/Support/HostAPI"
	idempotency "github.com/zhuchunshu/sforum/apps/api/app/Support/Idempotency"
	supportjobs "github.com/zhuchunshu/sforum/apps/api/app/Support/Jobs"
	pages "github.com/zhuchunshu/sforum/apps/api/app/Support/Pages"
	routes "github.com/zhuchunshu/sforum/apps/api/app/Support/Routes"
	search "github.com/zhuchunshu/sforum/apps/api/app/Support/Search"
	secretstore "github.com/zhuchunshu/sforum/apps/api/app/Support/SecretStore"
	"github.com/zhuchunshu/sforum/apps/api/config"
)

type apiNotificationChannelRuntimeAdapter struct{ runtime *extensionsruntime.Manager }

func (a apiNotificationChannelRuntimeAdapter) NotificationChannelInspection(ctx context.Context) (extensions.ProviderSlotInspection, error) {
	return a.runtime.ProviderSlotInspection(ctx)
}

func (a apiNotificationChannelRuntimeAdapter) SelectNotificationChannel(ctx context.Context, slot, candidate string, revision, actorID, auditID int64) (any, error) {
	if a.runtime == nil || a.runtime.ProviderSlotSelections() == nil {
		return nil, notificationscontroller.ErrChannelRuntimeUnavailable
	}
	return a.runtime.ProviderSlotSelections().Select(ctx, slot, candidate, revision, actorID, auditID)
}

func (a apiNotificationChannelRuntimeAdapter) ResetNotificationChannel(ctx context.Context, slot string, revision, actorID, auditID int64) error {
	if a.runtime == nil || a.runtime.ProviderSlotSelections() == nil {
		return notificationscontroller.ErrChannelRuntimeUnavailable
	}
	return a.runtime.ProviderSlotSelections().Reset(ctx, extensionsruntime.ResetProviderSlotRequest{
		ContractID: slot, ExpectedRevision: revision, ActorUserID: actorID,
		AuditEventID: auditID, ReasonCode: "operator_reset",
	})
}

func (a apiNotificationChannelRuntimeAdapter) ProbeNotificationChannel(ctx context.Context, slot, contractVersion, inputSchema string) (map[string]any, error) {
	if a.runtime == nil {
		return nil, notificationscontroller.ErrChannelRuntimeUnavailable
	}
	result, err := a.runtime.InvokeVersionedProvider(ctx, extensionsruntime.VersionedProviderInvocation{
		SlotID: slot, ContractVersion: contractVersion,
		Operation: extensionsruntime.VersionedProviderOperationInvoke, InputSchema: inputSchema,
		Input: map[string]any{"operation": "probe"}, Revalidate: extensionsruntime.BoundedProviderDocumentRevalidator,
	})
	return result.Output, err
}

var _ notificationscontroller.ChannelRuntime = apiNotificationChannelRuntimeAdapter{}

func wireAPIDomainServices(ctx context.Context, cfg config.Config, logger *slog.Logger, infrastructure *apiInfrastructure, extensionPlatform *apiExtensionPlatform) (*apiCoreStack, error) {
	queryCacheOwnership := newQueryResultCacheStageHandoff(infrastructure.queryResultCache, logger)
	defer queryCacheOwnership.CloseUnlessHandedOff()
	pool := infrastructure.pool
	databaseLeaseRegistry := infrastructure.databaseLeaseRegistry
	hostInstallationID := infrastructure.hostInstallationID
	redisStorage := infrastructure.redisStorage
	sharedRedisClient := infrastructure.sharedRedisClient
	optionCipher := infrastructure.optionCipher
	requiredReplayCipher := infrastructure.requiredReplayCipher
	auditWriter := infrastructure.auditWriter
	optionsService := infrastructure.optionsService
	humanVerifier := infrastructure.humanVerifier
	authSessions := infrastructure.authSessions
	identityStore := infrastructure.identityStore
	sessionPolicyRenewal := infrastructure.sessionPolicyRenewal
	identityAuthorityGate := infrastructure.identityAuthorityGate
	adminOverviewStore := infrastructure.adminOverviewStore
	forumStore := infrastructure.forumStore
	forumCachedStore := infrastructure.forumCachedStore
	profileStore := infrastructure.profileStore
	moderationStore := infrastructure.moderationStore
	attachmentStore := infrastructure.attachmentStore
	attachmentService := infrastructure.attachmentService
	databaseStore := infrastructure.databaseStore
	extensionStore := infrastructure.extensionStore
	frontendTrustStore := infrastructure.frontendTrustStore
	jobClient := infrastructure.jobClient
	jobDispatcher := infrastructure.jobDispatcher

	executableTrustService := extensionPlatform.executableTrustService
	frontendService := extensionPlatform.frontendService
	pageRegistry := extensionPlatform.pageRegistry
	themeRuntime := extensionPlatform.themeRuntime
	extensionService := extensionPlatform.extensionService
	hostAPIGateway := extensionPlatform.hostAPIGateway
	extensionRuntime := extensionPlatform.extensionRuntime
	lifecycleRuntime := extensionPlatform.lifecycleRuntime
	hostCacheRuntime := extensionPlatform.hostCacheRuntime
	lifecycleStack := extensionPlatform.lifecycleStack
	productionSEO := extensionPlatform.productionSEO
	identityReviewStore := extensionPlatform.identityReviewStore
	pluginRuntimeCoordinator := extensionPlatform.pluginRuntimeCoordinator
	pluginRuntimeRecovery := extensionPlatform.pluginRuntimeRecovery
	pluginRuntimeStopTimeout := extensionPlatform.pluginRuntimeStopTimeout
	stopPluginRuntimeCoordinator := extensionPlatform.stopPluginRuntimeCoordinator
	closePluginRuntime := extensionPlatform.closePluginRuntime
	notificationStore := notifications.NewPostgresStoreWithAvatar(pool, avatarOptionsAdapter{options: infrastructure.optionsService}).
		WithPushDeviceCipher(infrastructure.optionCipher).
		WithRevisionWakeups(ctx)
	closeNotificationStore := notificationStore.Close
	mailOutbox := notifications.NewOutbox(pool, notificationStore, jobDispatcher, options.NewMailSettings(optionsService)).
		WithDeliveryPolicyResolver(notificationStore).
		WithPermissionRecipients(identityStore)
	forumStore.WithCommentNotifications(forumNotificationAdapter{outbox: mailOutbox})
	moderationStore.WithDecisionNotifications(moderationNotificationAdapter{outbox: mailOutbox})
	siteName, _ := optionsService.SiteName(ctx)
	siteURL, _ := optionsService.WebOption(ctx, "site.url")
	mailResendPolicyResolver := options.NewMailResendPolicyResolver(optionsService)
	passwordResetService := identity.NewPasswordResetServiceWithPasswordPolicy(identityStore, passwordResetOutbox{outbox: mailOutbox}, identity.PasswordResetConfig{
		SiteName: siteName,
		SiteURL:  siteURL,
	}, optionsService).WithLocaleResolver(options.NewMailSettings(optionsService)).WithBrandResolver(options.NewMailSettings(optionsService)).
		WithRateLimiter(authsupport.NewPasswordResetLimiter(sharedRedisClient)).WithMailResendPolicyResolver(mailResendPolicyResolver)
	mailSettings := options.NewMailSettings(optionsService)
	emailVerificationService := identity.NewEmailVerificationService(
		identityStore,
		emailVerificationOutbox{outbox: mailOutbox},
		options.NewEmailVerificationPolicyResolver(optionsService),
		identity.EmailVerificationConfig{},
	).WithMailResolvers(mailSettings, mailSettings).
		WithRateLimiter(authsupport.NewPasswordResetLimiter(sharedRedisClient)).WithMailResendPolicyResolver(mailResendPolicyResolver)
	loginLockout := authsupport.NewLoginLockout(sharedRedisClient)
	// F3.3：出站 webhook 扇出包装在事件发布路径上（observe 成功后异步入队）。
	webhookStore := webhooks.NewPostgresStore(pool)
	// 生产默认仅 https；非生产允许 http 便于本地联调（连接时仍禁止私网 SSRF）。
	webhookService := webhooks.NewService(webhookStore, pool, jobDispatcher).
		WithAllowHTTP(!strings.EqualFold(cfg.AppEnv, "production")).
		WithCipher(optionCipher)
	// 发布者声明为接口：AI 回复要在这里包装它，好订阅 comment.created。
	var eventPublisher appevents.Publisher = webhooks.BridgePublisher{Inner: extensionRuntime, Fanout: webhookService}
	// AI 回复的触发来自事件而非论坛服务内部。包装必须发生在论坛服务创建之前，
	// 否则论坛拿到的是未包装的发布者，事件不会到达判定。
	aiReplyReader := aireply.NewPostgresReader(pool)
	// 触发模式（mention / autonomous / off）在 aiProvider 就绪后补挂：见下方
	// aiProvider 创建处的 WithTriggerModes 调用。
	aiReplyTrigger := aireply.NewTrigger(providers.BotAccountAdapter{Store: identityStore}, aiReplyReader, aireplyjobs.NewEnqueuer(jobDispatcher))
	eventPublisher = aireply.NewSubscriber(eventPublisher, aiReplyTrigger)
	// 与 extensionService 共享同一 attachmentService 实例（禁用回落 + 候选目录 + 事件 + 存储 RPC）。
	// MediaRegistry MIME 策略在已发布时叠加；无策略时 no-op。
	_ = attachmentService.WithEvents(eventPublisher).
		WithStorageProviderCatalog(extensions.NewAttachmentStorageProviderCatalog(extensionService)).
		WithStoragePluginRuntime(extensionsruntime.NewPluginStorageAdapterFactory(extensionRuntime, 0)).
		WithMediaRegistry(lifecycleStack.MediaRegistry)

	// F3.4：个人访问令牌；管理走 cookie，调用走 Bearer。
	apiTokenStore := apitokens.NewPostgresStore(pool)
	apiTokenService := apitokens.NewService(apiTokenStore, identityStore).WithAuditor(auditWriter)

	sessionPolicyStepUpStore, err := identity.NewPostgresSessionPolicyStepUpStore(pool)
	if err != nil {
		return nil, fmt.Errorf("create session policy step-up store: %w", err)
	}
	sessionPolicyEvaluator, err := newSessionPolicyEvaluator(
		lifecycleStack.RuntimeManager,
		lifecycleStack.IdentityRegistry,
		lifecycleStack.SessionPolicyStore,
		sessionPolicyStepUpStore,
	)
	if err != nil {
		return nil, err
	}
	riskEvaluator, err := newRiskEvaluator(lifecycleStack.RuntimeManager, lifecycleStack.IdentityRegistry)
	if err != nil {
		return nil, err
	}
	externalLinkStore := identity.NewPostgresExternalIdentityLinkStore(pool)
	// 外部认证 Host 栈（callback 状态/注册票据/激活目录/编排服务）。
	externalAuthStack := identity.NewExternalAuthStack(pool, sharedRedisClient, lifecycleStack.IdentityRegistry, func(ctx context.Context) (bool, error) {
		return optionsService.RegistrationEnabled(ctx)
	})
	// 外部注册必须在 user/link/audit 同一 pgx 事务中读取权威运营策略，不能复用
	// Options 的独立 pool/cache 快速读取。
	externalAuthStack.Service.WithRegistrationPolicyTx(optionsService.RegistrationEnabledTx)
	// T1D：权威 CurrentUser 路径（token version/roles/permissions/avatar/status）。
	// 注册校验在 Controller.WithExternalAuthService 中由 identity.Service 注入。
	// T8A：成功 external registration 后发出与密码注册同形的 user.registered observe。
	externalAuthStack.Service.WithCurrentUserLoader(identityStore.GetCurrentUser)
	externalAuthStack.Service.WithEvents(eventPublisher)
	// M3：必需配置门控（client_id/secret 等）来自扩展设置；未齐则公开 catalog 不暴露。
	identityRegistry := lifecycleStack.IdentityRegistry
	externalAuthStack.Service.WithProviderConfiguredChecker(func(ctx context.Context, providerID string) (bool, error) {
		if identityRegistry == nil || extensionService == nil {
			return false, nil
		}
		contrib, err := identityRegistry.ResolveProvider(providerID)
		if err != nil {
			return false, nil
		}
		return extensionService.AuthProviderSettingsConfigured(ctx, contrib.Artifact.ExtensionID)
	})
	authProviderFlow, err := newAuthProviderFlow(
		lifecycleStack.RuntimeManager, lifecycleStack.IdentityRegistry, externalLinkStore,
	)
	if err != nil {
		return nil, err
	}
	profileProviderComposer, err := newProfileProviderComposer(
		lifecycleStack.RuntimeManager, lifecycleStack.IdentityRegistry,
	)
	if err != nil {
		return nil, err
	}
	recoveryProviderFlow, err := newRecoveryProviderFlow(
		lifecycleStack.RuntimeManager, lifecycleStack.IdentityRegistry,
	)
	if err != nil {
		return nil, err
	}
	identityStore.WithAuthorityMutationGate(lifecycleStack.SessionPolicyStore)
	identityAuthorityGate.Set(lifecycleStack.SessionPolicyStore)
	sessionPolicyRenewal.Set(sessionPolicyEvaluator)
	// M5：外部认证 start/callback 专用限流（Redis；无 Redis 时内存）。
	var externalAuthRateLimiter identity.ExternalAuthRateLimiter
	if sharedRedisClient != nil {
		externalAuthRateLimiter = identity.NewRedisExternalAuthRateLimiter(sharedRedisClient)
	} else {
		externalAuthRateLimiter = identity.NewMemoryExternalAuthRateLimiter()
	}
	// T8B：admin Login Methods discovery = 扩展包目录 + live Registry；trust/enable 权威不变。
	authProviderPackageCatalog := extensions.NewAuthProviderPackageCatalog(extensionService, executableTrustService)
	identityProvider := providers.NewIdentityProviderWithPasswordResetAndLockout(identityStore, authSessions, humanVerifier, eventPublisher, passwordResetService, mailOutbox, optionsService, loginLockout).
		WithEmailVerification(emailVerificationService).
		WithIdentityRegistryStore(identityReviewStore).
		WithSessionPolicyEvaluator(sessionPolicyEvaluator).
		WithRiskEvaluator(riskEvaluator).
		WithAuthProviderFlow(authProviderFlow).
		WithProfileProviderComposer(profileProviderComposer).
		WithRecoveryProviderFlow(recoveryProviderFlow).
		WithIdentityProviderCatalog(lifecycleStack.IdentityRegistry).
		WithAuthProviderPackageCatalog(authProviderPackageCatalog).
		WithExternalAuthStack(externalAuthStack).
		WithExternalAuthRateLimiter(externalAuthRateLimiter).
		WithPublicAppURL(cfg.AppURL, cfg.AppEnv).
		WithAPITokens(apiTokenService)
	notificationTargets := notificationTargetVisibilityAdapter{forum: forumStore, identities: identityStore}
	notificationsProvider := providers.NewNotificationsProvider(notificationStore, identityStore, authSessions, auditWriter).
		WithTargetVisibility(notificationTargets).
		WithTargetPreview(notificationTargetPreviewAdapter{store: forumStore}).
		WithChannels(apiNotificationChannelRuntimeAdapter{runtime: lifecycleStack.RuntimeManager}, auditWriter, mailOutbox)
	mailProvider := providers.NewMailProvider(extensionStore, notificationStore, extensionsruntime.NewMailProviderRegistry(extensionStore), identityStore, authSessions, optionsService, notificationStore)
	// Worker 心跳 store 尽早创建，供 overview 与嵌入 worker 共用。
	heartbeatStore := health.NewRedisHeartbeatStore(sharedRedisClient)
	adminOverviewProvider := providers.NewAdminOverviewProviderWithWidgets(
		adminOverviewStore,
		adminoverview.NewRuntimeCollector(time.Now().UTC(), pool).
			WithHeartbeat(heartbeatStore).
			WithQueueLag(pool).
			WithWorkerRuntime(
				!pluginRuntimeRecovery.Active(),
				config.JobQueueWorkerTotal(cfg),
			),
		identityStore,
		authSessions,
		providers.NewExtensionDashboardWidgetProvider(extensionService),
	)
	// 搜索：Host 框架 + 默认站内引擎（PG FTS）；外部引擎经 search.provider 插件。
	searchProviders := extensionsruntime.NewSearchProviderRegistry(extensionStore)
	siteEngine := search.NewPostgresSiteEngine(pool)
	searchEngine := extensionsruntime.NewResolvingSearchEngine(searchProviders, extensionRuntime, siteEngine)
	searchIndexer := search.NewIndexer(searchEngine, nil, jobDispatcher)
	forumSettingsResolver := providers.NewForumSettingsResolver(optionsService)
	// LiveTopicSource：引擎命中后对照 topics 表剔除幽灵文档（Meili 脏索引 / 删库未清引擎）。
	searchService := search.NewService(searchEngine, forumSettingsResolver).
		WithLiveSource(forumLiveSearchSource{store: forumStore})
	// 搜索索引重建：forumStore 提供 ListAllTopicIDs（TopicIDSource），
	// reindexStore 记录运行状态，dispatcher 批量入队 IndexTopicArgs。
	reindexManager := search.NewReindexManager(forumStore, search.NewPostgresReindexStore(pool), jobDispatcher)

	// F3.2：发帖/评论写路径可选 Idempotency-Key；存储复用 shared Redis。
	idempotencyStore := idempotency.NewStore(
		idempotency.NewRedisBackend(sharedRedisClient), idempotency.DefaultTTL,
	).WithRequiredReplayCipher(requiredReplayCipher)
	// D3：公开详情浏览计数（与 worker 刷盘共用同一 Redis）。
	topicViewCounter := forum.NewRedisTopicViewCounter(sharedRedisClient).WithLogger(logger)
	contentProtocolRuntime, err := extensionsruntime.NewContentRegistryProtocolRuntime(lifecycleStack.RuntimeManager)
	if err != nil {
		return nil, fmt.Errorf("create Content Registry Protocol V2 runtime: %w", err)
	}
	forumContentFilter, err := contentregistry.NewProductionForumPostFilter(contentregistry.ForumPostFilterConfig{
		Registry:  lifecycleStack.ContentRegistry,
		Admission: hostapi.NewContentRegistryAdmission(contentProtocolRuntime),
		Providers: contentProtocolRuntime,
	})
	if err != nil {
		return nil, fmt.Errorf("create Forum Content Registry filter: %w", err)
	}
	forumShortcodes, err := contentregistry.NewForumShortcodeDispatcher(contentregistry.ForumShortcodeDispatcherConfig{
		Registry: lifecycleStack.ContentRegistry, Admission: hostapi.NewContentRegistryAdmission(contentProtocolRuntime),
		Providers: contentProtocolRuntime, Cache: cache.NewRedisCache(sharedRedisClient),
	})
	if err != nil {
		return nil, fmt.Errorf("create Forum shortcode dispatcher: %w", err)
	}
	forumCachedStore = forum.WithReferenceRenderCacheInvalidator(forumCachedStore, forumShortcodes)
	// 评论区实时信号：修订号由 comments 触发器维护在 topics.comment_revision 上，
	// 唤醒来源是本进程的 LISTEN hub。hub 随 ctx 结束释放专用连接，无需额外关停顺序。
	commentLive := forum.NewCommentLiveService(forumCachedStore).
		WithWakeHub(forum.NewCommentRevisionHub(ctx, pool))
	forumProvider := providers.NewForumProviderWithPublicContributions(
		forumCachedStore,
		optionsService,
		identityStore,
		authSessions,
		eventPublisher,
		searchIndexer,
		searchServiceAdapter{inner: searchService, store: forumStore},
		reindexServiceAdapter{inner: reindexManager},
		providers.NewExtensionTopicActionProvider(extensionService),
		providers.NewExtensionCommentActionProvider(extensionService),
		providers.NewExtensionTopicSurfaceProvider(extensionService),
		providers.NewExtensionComposerToolbarProvider(extensionService),
		providers.NewModerationPublicationPolicy(moderationStore, optionsService),
	).WithIdempotency(idempotencyStore).WithContentPostFilter(forum.ContentRegistryBridge{
		Inner: forumContentFilter,
	}).WithPublicShortcodeDispatcher(forumShortcodes).
		WithProtectedShortcodes(
			forum.NewProtectedShortcodePolicy(identityStore, hostapi.NewPostgresShortcodePolicyProjection(pool)),
			forumShortcodes,
		).
		WithEditorDocumentSchema(forum.EditorRegistrySchemaBridge{
			Registry: lifecycleStack.EditorRegistry,
		}).WithSearchProviderAdmin(searchProviderAdminAdapter{registry: searchProviders}).
		WithEmailVerificationGate(emailVerificationService).
		WithViewRecorder(topicViewCounter).
		WithCommentLive(commentLive)
	// 头像与附件管理共用带存储候选目录的服务实例。
	avatarAttachmentService := attachmentService
	profileProvider := providers.NewProfileProviderWithAvatarAndTabs(
		profileStore,
		identityStore,
		authSessions,
		avatarAttachmentService,
		optionsService,
		providers.NewExtensionProfileTabProvider(extensionService),
	)
	moderationReadModels, _ := forumCachedStore.(moderation.DecisionReadModelInvalidator)
	moderationProvider := providers.NewModerationWorkbenchProviderWithIndexer(moderationStore, forumStore, identityStore, authSessions, searchIndexer, moderationReadModels)
	// AI 网关：默认关闭，未启用前不产生任何出站调用。密钥只以 Secret Store
	// 引用形式进入配置，因此这里传入的是 Host 的 Secret Store 服务。
	var aiSecrets *secretstore.Service
	if extensionPlatform.hostPlatform != nil {
		aiSecrets = extensionPlatform.hostPlatform.Secrets
	}
	// 出站策略与其它路径一致：非生产放行 http（生产只允许 https）。私网与回环地址
	// 默认拒绝，只有显式配置 SFORUM_AI_ALLOW_PRIVATE_ENDPOINT 且非生产时才放行。
	aiProvider := providers.NewAIProvider(pool, aiSecrets, identityStore, authSessions, providers.AIOutboundOptions{
		AllowHTTP:    !strings.EqualFold(cfg.AppEnv, "production"),
		AllowPrivate: config.AllowPrivateAIEndpoint(cfg),
	}, cache.NewRedisCache(sharedRedisClient))
	// AI 网关在这里才可用（它需要 Secret Store，后者由扩展平台阶段产出），因此
	// 命令目录冻结时拿到的是一个占位目标，此刻回填真实网关。
	if extensionPlatform.commandDeferred != nil {
		extensionPlatform.commandDeferred.AI.Set(aiProvider.Gateway())
	}
	// AI 回复触发模式来自 AI 设置（mention / autonomous / off）。事件订阅者持
	// 有的是同一个触发器指针，这里补挂读法后控制台改配置立即生效；读失败时
	// 触发器自回退到 mention（默认），装配层不做兜底解释。
	aiReplyTrigger.WithTriggerModes(aiProvider.Service())
	optionsProvider := providers.NewOptionsProviderWithService(optionsService, identityStore, authSessions)
	systemUpdatesProvider := providers.NewSystemUpdatesProvider(
		systemupdates.NewService(options.NewSystemUpdatesSource(optionsService), systemupdates.WithLogger(logger)),
		identityStore,
		authSessions,
	)
	siteChromeStore := sitechrome.NewPostgresStore(pool)
	compressionService := attachments.NewCompressionService(attachmentStore, attachmentService, optionsService, func(ctx context.Context, taskID int64) error {
		args := attachmentjobs.CompressImageArgs{TaskID: taskID}
		_, err := jobDispatcher.Enqueue(ctx, args, args.EnqueueOptions())
		return err
	})
	attachmentService.WithCompressionScheduler(compressionService)
	attachmentsProvider := providers.NewAttachmentsProviderWithService(attachmentService, attachmentStore, identityStore, authSessions).
		WithEmailVerificationGate(emailVerificationService).
		WithCompressionService(compressionService)
	seoProvider := providers.NewSEOProvider(pool, optionsService)
	databaseProvider := providers.NewDatabaseProvider(databaseStore, identityStore, authSessions)
	jobsProvider := providers.NewJobsProvider(pool, jobClient, identityStore, authSessions)
	routeTraceRing := routes.NewRouteTraceRing(0)
	extensionsProvider := providers.NewExtensionsProviderWithService(extensionService, identityStore, authSessions, extensionRuntime, frontendService).
		WithRouteProviderSelection(lifecycleStack.RouteProviders, auditWriter).
		WithRouteContractCatalog(lifecycleStack.RouteSchemas).
		WithProviderSlotSelection(lifecycleStack.ProviderSlots, lifecycleRuntime, auditWriter).
		WithRouteInspector(routes.NewProviderSelectionInspector(lifecycleStack.RouteProviders, routeTraceRing)).
		WithCacheInspector(hostCacheRuntime.Registry, hostCacheRuntime.Inspector).
		WithComponentCompositionInspector(lifecycleStack.ComponentInspector).
		WithAssetInspector(lifecycleStack.AssetRegistry).
		WithThemeRuntimeInspector(themeRuntime).
		WithEditorRegistry(lifecycleStack.EditorRegistry).
		WithEntityRegistry(lifecycleStack.EntityRegistry).
		WithContentRegistry(lifecycleStack.ContentRegistry).
		WithMediaRegistry(lifecycleStack.MediaRegistry).
		WithAdminSurfaces(extensionRuntime, auditWriter)
	webhooksProvider := providers.NewWebhooksProvider(webhookService, identityStore, authSessions)
	// PageDataLoader 网关：仅从运行中插件 RouteTarget 拉数据（严格 loopback）。
	pageLoaderGateway := pages.NewLoaderGateway(
		pages.NewPageDataLoader(nil),
		pageRouteTargetAdapter{runtime: extensionRuntime},
	).WithPackages(pagePackageRootAdapter{store: extensionStore})
	// Core Page ViewModels reuse the same domain services and policy sources as
	// their JSON endpoints. Only reviewed presentation DTOs cross into themes.
	pageForumService := forum.NewService(forum.ServiceConfig{
		Store:           forumCachedStore,
		Settings:        forumSettingsResolver,
		Publisher:       eventPublisher,
		TopicEventLinks: providers.NewForumTopicEventLinkResolver(optionsService),
	})
	pageProfileService := profile.NewServiceWithAvatar(profileStore, avatarAttachmentService, optionsService).
		WithProfileTabs(providers.NewExtensionProfileTabProvider(extensionService))
	// AI 只读工具：登记表归 Core，白名单归用途（forum.reply 自己声明）。工具只
	// 复用上面这些公开读取路径，因此「机器人能读到什么」与访客一致。
	aiToolRegistry, err := aitools.NewBuiltinRegistry(aitools.Deps{
		Search:   searchService,
		Topics:   forumProvider.Service(),
		Comments: forumProvider.Service(),
		Lister:   forumProvider.Service(),
		Profiles: pageProfileService,
		Users:    pageProfileService,
		SiteURL: func(ctx context.Context) string {
			value, _ := optionsService.WebOption(ctx, "site.url")
			return value
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create ai tool registry: %w", err)
	}
	aiOrchestrator := supportai.NewOrchestrator(supportai.OrchestratorConfig{
		Gateway:  aiProvider.Gateway(),
		Registry: aiToolRegistry,
	})
	// 插件贡献的 AI 工具登记进同一张工具表，让「站内有哪些工具」只有一个可枚举
	// 答案。登记不等于暴露：模型实际能看到哪些工具由用途自己的白名单决定
	// （RunInput.ToolNames），Core 用途的白名单不含插件工具。
	//
	// 恢复模式与 Safe Mode 下插件运行时并未启动，此时登记只会造出「看得见、
	// 调不动」的工具，因此直接跳过。
	if !cfg.SafeMode && !pluginRuntimeRecovery.Active() {
		pluginItems, listErr := extensionStore.List(ctx)
		switch {
		case listErr != nil:
			logger.Warn("plugin AI tool registration skipped: list extensions failed", "error", listErr)
		default:
			for _, failure := range registerPluginAITools(aiToolRegistry, providers.NewAIToolInvoker(lifecycleRuntime), pluginItems) {
				logger.Warn("plugin AI tool registration skipped", "error", failure)
			}
		}
	}
	pageModerationService := moderation.NewServiceWithWorkbench(
		moderationStore, moderation.NewForumTargetValidator(forumStore), moderationStore, moderationStore,
	)
	pageIdentityService := identity.NewServiceWithPolicies(identityStore, eventPublisher, optionsService, optionsService)
	// V3 Navigation Registry：与 lifecycle stack 共享同一实例；Core 已在进程启动时发布。
	pageNavigationRegistry := lifecycleStack.NavigationRegistry
	if pageNavigationRegistry == nil {
		return nil, fmt.Errorf("extension lifecycle navigation registry is unavailable")
	}
	// 生产 Navigation Runtime：精确 runtime instance admission；声明式贡献可合成，
	// Handler 渲染未接入前 fail closed（optional replace 回退 / selected replace 关闭）。
	// 使用 lifecycleRuntime（*Manager），保证 Inspect/Available 与发布制品同源。
	pageNavigationRuntime := newProductionNavigationRuntime(lifecycleRuntime)
	pageSiteChromeService := sitechrome.NewService(siteChromeStore).
		WithExtensionNavItems(providers.NewExtensionNavItemProvider(extensionService)).
		WithNavigationRegistry(pageNavigationRegistry).
		WithNavigationRuntime(pageNavigationRuntime, pageNavigationRuntime).
		WithNavigationThemeLocations(themeRuntime).
		WithNavigationCommandDependencies(siteChromeStore, auditWriter, options.NewPublicSurfaceRevisionTxBumper(optionsService))
	// HTTP and Page Registry intentionally share this resolver. A separate API
	// service would lose the lifecycle-published exact-artifact graph.
	siteChromeProvider := providers.NewSiteChromeProviderWithService(pageSiteChromeService, identityStore, authSessions).
		WithBrandAssets(attachmentService)
	// 导航检查器复用 SiteChrome 内部 trace ring，保证合成与审计同源。
	extensionsProvider.WithNavigationInspector(pageSiteChromeService.NavigationInspector())
	corePageViews := pageviewmodels.NewCorePageViewModelSource(pageviewmodels.CorePageViewModelDependencies{
		Forum: forum.NewPublicReadService(pageForumService).WithShortcodeDispatcher(forumShortcodes), Profiles: pageProfileService, Notifications: notificationStore, NotificationTargets: notificationTargets,
		Moderation: pageModerationService, Options: optionsService, Registration: pageIdentityService,
		Sessions: identityStore, SiteChrome: pageSiteChromeService, Search: searchService,
	})
	pagesProvider := providers.NewPagesProviderWithThemes(pageRegistry, identityStore, authSessions, extensionStore).
		WithAuditor(auditWriter).
		WithLoader(pageLoaderGateway).
		WithThemeRuntime(themeRuntime).
		WithCorePageViewModels(corePageViews).
		// 标准页面区域(forum.page.regions):descriptor + L2 widget 引用。
		WithPageRegions(providers.NewExtensionPageRegionProvider(extensionService))

	// F4.4：实体自定义字段（EAV，无 per-plugin core ALTER）。
	entityMetaStore := entitymeta.NewPostgresStore(pool)
	entityMetaService := entitymeta.NewService(entityMetaStore).WithPublisher(eventPublisher)
	entityMetaProvider := providers.NewEntityMetaProvider(entityMetaService, identityStore, authSessions)

	// Readiness：PG 必检；Redis 失败记 degraded 仍 ready。
	// Meilisearch 已拆为可选 search.provider 插件，不再作为 core readiness 组件。
	// F4.3：合并 system.health.checks 贡献（不调用插件 RPC）。
	readyEvaluate := func(ctx context.Context) health.ReadyReport {
		checkers := []health.Checker{
			health.PostgresChecker{Pool: pool},
			health.RedisChecker{Client: sharedRedisClient},
		}
		var report health.ReadyReport
		if pluginRuntimeRecovery.Active() {
			// Recovery readiness must not inspect the failed artifact again or
			// expose its process error through extension health contributions.
			report = health.Evaluate(ctx, checkers)
		} else {
			report = health.EvaluateWithExtensionContributions(ctx, checkers, extensionService, extensionRuntime)
		}
		return health.ApplyRecoveryRequirement(report, pluginRuntimeRecovery)
	}
	recoveryOnly := pluginRuntimeRecovery.Active()
	extensionGuardPolicy := extensions.NewGuardPolicyCatalog(
		extensionStore,
		executableTrustService,
		frontendTrustStore,
		extensions.GuardPolicyConfig{
			SafeMode: cfg.SafeMode || recoveryOnly, TrustChallengesEnabled: cfg.V3TrustChallenges,
		},
	)
	executableTrustService.WithRevocationSink(extensionsruntime.NewExecutableTrustRevocationFence(
		lifecycleStack.RuntimeManager, extensionGuardPolicy,
	))
	if !recoveryOnly {
		if err := extensionGuardPolicy.Refresh(ctx); err != nil {
			closeNotificationStore()
			stopPluginRuntimeCoordinator()
			if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
				logger.Warn("job dispatcher stop failed", "error", stopErr)
			}
			closePluginRuntime()
			sharedRedisClient.Close()
			if closeErr := redisStorage.Close(); closeErr != nil {
				logger.Warn("redis session storage close failed", "error", closeErr)
			}
			pool.Close()
			return nil, fmt.Errorf("refresh extension guard policy failed: %w", err)
		}
	}
	routeFailureRecorder, err := httpserver.NewRouteFailureRecorder(
		lifecycleStack.RuntimeManager,
		httpserver.NewPostgresRouteRuntimeIncidentStore(pool),
		auditWriter,
		logger,
	)
	if err != nil {
		closeNotificationStore()
		stopPluginRuntimeCoordinator()
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		closePluginRuntime()
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("create route failure recorder: %w", err)
	}
	closeRouteFailureRecorder := func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if closeErr := routeFailureRecorder.Close(closeCtx); closeErr != nil && logger != nil {
			logger.Warn("route failure recorder stop timed out", "error", closeErr)
		}
	}
	// P6 buffered HTTP dispatcher consumes the durable provider selection before
	// Fiber's hardcoded core providers. Declared schemas remain fail-closed until
	// their exact package catalog is production-published.
	routeDispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans: lifecycleStack.RouteProviders,
		Steps: httpserver.NewBufferedRouteStepInvoker(lifecycleStack.RuntimeManager),
		Guard: httpserver.NewProductionRouteGuardAuthorizerWithPolicies(httpserver.ProductionRouteGuardPolicies{
			ForumRead:         optionsService,
			Extensions:        extensionGuardPolicy,
			DeclaredRoutes:    extensionGuardPolicy,
			Options:           optionsService,
			Pages:             pageRegistry,
			IdentityAdmins:    identityStore,
			IdentitySessions:  identityStore,
			IdentityAPITokens: apiTokenStore,
			EntityMetaValues:  entityMetaStore,
			AttachmentReads:   attachmentStore,
			ForumComments:     forumStore,
			ForumResources:    forumStore,
			PluginGuards:      httpserver.NewRuntimePluginRouteGuardEvaluator(lifecycleStack.RuntimeManager, extensionGuardPolicy),
		}),
		Schemas:        httpserver.CatalogRouteSchemaValidator{Catalog: lifecycleStack.RouteSchemas},
		Trace:          routeTraceRing,
		Policies:       lifecycleStack.RouteSchemas,
		Idempotency:    httpserver.NewRequiredRouteIdempotency(idempotencyStore),
		Failures:       routeFailureRecorder,
		StreamFailures: routeFailureRecorder,
	})

	return &apiCoreStack{
		adminOverviewProvider:     adminOverviewProvider,
		aiProvider:                aiProvider,
		aiOrchestrator:            aiOrchestrator,
		systemUpdatesProvider:     systemUpdatesProvider,
		apiTokenService:           apiTokenService,
		attachmentsProvider:       attachmentsProvider,
		attachmentService:         attachmentService,
		auditWriter:               auditWriter,
		authSessions:              authSessions,
		closePluginRuntime:        closePluginRuntime,
		closeRouteFailureRecorder: closeRouteFailureRecorder,
		databaseLeaseRegistry:     databaseLeaseRegistry,
		databaseProvider:          databaseProvider,
		entityMetaProvider:        entityMetaProvider,
		extensionGuardPolicy:      extensionGuardPolicy,
		extensionRuntime:          extensionRuntime,
		extensionService:          extensionService,
		extensionStore:            extensionStore,
		extensionsProvider:        extensionsProvider,
		forumProvider:             forumProvider,
		heartbeatStore:            heartbeatStore,
		hostAPIGateway:            hostAPIGateway,
		hostInstallationID:        hostInstallationID,
		identityProvider:          identityProvider,
		identityStore:             identityStore,
		jobClient:                 jobClient,
		jobsProvider:              jobsProvider,
		lifecycleStack:            lifecycleStack,
		mailProvider:              mailProvider,
		moderationProvider:        moderationProvider,
		notificationsProvider:     notificationsProvider,
		closeNotificationStore:    closeNotificationStore,
		optionsProvider:           optionsProvider,
		optionsService:            optionsService,
		pagesProvider:             pagesProvider,
		pluginRuntimeCoordinator:  pluginRuntimeCoordinator,
		pluginRuntimeRecovery:     pluginRuntimeRecovery,
		pluginRuntimeStopTimeout:  pluginRuntimeStopTimeout,
		pool:                      pool,
		productionSEO:             productionSEO,
		profileProvider:           profileProvider,
		// 将 Query result cache 所有权移交给 finishAPIHTTP（API.close 最终关闭）。
		queryResultCache:             queryCacheOwnership.HandOff(),
		readyEvaluate:                readyEvaluate,
		redisStorage:                 redisStorage,
		routeDispatcher:              routeDispatcher,
		seoProvider:                  seoProvider,
		sharedRedisClient:            sharedRedisClient,
		siteChromeProvider:           siteChromeProvider,
		stopPluginRuntimeCoordinator: stopPluginRuntimeCoordinator,
		webhooksProvider:             webhooksProvider,
	}, nil
}
