package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"

	httpserver "github.com/zhuchunshu/sforum/apps/api/app/Http"
	aireplyjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/AIReply"
	aireply "github.com/zhuchunshu/sforum/apps/api/app/Models/AIReply"
	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	options "github.com/zhuchunshu/sforum/apps/api/app/Models/Options"
	providers "github.com/zhuchunshu/sforum/apps/api/app/Providers"
	extensionsruntime "github.com/zhuchunshu/sforum/apps/api/app/Support/Extensions"
	health "github.com/zhuchunshu/sforum/apps/api/app/Support/Health"
	supportjobs "github.com/zhuchunshu/sforum/apps/api/app/Support/Jobs"
	"github.com/zhuchunshu/sforum/apps/api/config"
)

// newEmbeddedAIReplyGenerator 用 API 已装配的依赖构建回复生成器。任一依赖缺失
// 时返回 nil，注册逻辑据此跳过：AI 回复是可选能力，缺装配时应整体关闭而不是
// 退化成一份行为不一致的次品装配。
func newEmbeddedAIReplyGenerator(core *apiCoreStack) *aireply.Generator {
	if core == nil || core.aiProvider == nil || core.forumProvider == nil {
		return nil
	}
	forumService := core.forumProvider.Service()
	gateway := core.aiProvider.Gateway()
	if forumService == nil || gateway == nil {
		return nil
	}
	// AI 服务同时提供回复提示词与工具白名单两项配置来源：两者都住在同一份设置
	// 文档里，且都在每次回复时读取，配置改完立即生效。附件服务让多模态输入
	// 能以机器人身份读取图片附件（是否启用由站点设置的多模态开关决定）。
	return providers.NewAIReplyGenerator(core.pool, core.identityStore, forumService, gateway, core.aiOrchestrator, core.aiProvider.Service(),
		providers.NewAIReplyImageReader(core.attachmentService, core.identityStore))
}

// newEmbeddedAITriageDecider 用 API 已装配的网关与 AI 服务构建自主参与判定器。
// 依赖缺失时返回 nil：注册逻辑据此跳过判定 worker，autonomous 模式退回 mention
// 行为（trigger 侧对缺失判定入队器的处理）。
func newEmbeddedAITriageDecider(core *apiCoreStack) *aireply.TriageDecider {
	if core == nil || core.aiProvider == nil {
		return nil
	}
	gateway := core.aiProvider.Gateway()
	if gateway == nil {
		return nil
	}
	completer := aireply.NewGatewayCompleter(gateway, core.aiOrchestrator)
	return aireply.NewTriageDecider(completer, core.aiProvider.Service())
}

// newEmbeddedAIThreadReader 返回判定读取上下文用的 PostgresReader。它独立构造
// 一份而不是与生成器共享：reader 无状态（只持连接池），共享与否不改变行为，
// 独立构造让两条装配路径互不牵连。
func newEmbeddedAIThreadReader(core *apiCoreStack) aireply.ThreadReader {
	if core == nil || core.pool == nil {
		return nil
	}
	return aireply.NewPostgresReader(core.pool)
}

// finishAPIHTTP：Fiber 应用、主题 watcher、嵌入 worker 与 API 句柄。
func finishAPIHTTP(ctx context.Context, cfg config.Config, logger *slog.Logger, core *apiCoreStack) (*API, error) {
	var err error
	// 承接 wire 阶段移交的 cache：本阶段失败则关闭；成功后由 API.close 关闭。
	queryCacheOwnership := newQueryResultCacheStageHandoff(core.queryResultCache, logger)
	defer queryCacheOwnership.CloseUnlessHandedOff()
	app := httpserver.NewApp(cfg, logger, httpserver.Dependencies{
		RouteProviders:  []httpserver.RouteProvider{core.identityProvider, core.notificationsProvider, core.mailProvider, core.adminOverviewProvider, core.systemUpdatesProvider, core.forumProvider, core.profileProvider, core.moderationProvider, core.optionsProvider, core.siteChromeProvider, core.attachmentsProvider, core.seoProvider, core.databaseProvider, core.jobsProvider, core.extensionsProvider, core.webhooksProvider, core.entityMetaProvider, core.pagesProvider, core.aiProvider},
		RoutePlans:      core.lifecycleStack.RouteProviders,
		RouteDispatcher: core.routeDispatcher,
		RouteActors: func(c fiber.Ctx) (identity.Actor, error) {
			return loadSessionPolicyAwareRouteActor(c, core.authSessions, core.identityStore)
		},
		Options:      core.optionsService,
		Storage:      core.redisStorage,
		Ready:        core.readyEvaluate,
		BearerTokens: httpserver.TokenServiceAdapter{Service: core.apiTokenService},
		Auditor:      core.auditWriter,
		Recovery:     core.pluginRuntimeRecovery,
	})
	var themeRuntimeWatcher *apiThemeRuntimeWatcherRuntime
	themeRuntimeStopTimeout := normalizedPluginRuntimeCoordinatorStopTimeout(cfg.WorkerShutdownTimeout)
	stopThemeRuntimeWatcher := func() {
		if themeRuntimeWatcher == nil {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), themeRuntimeStopTimeout)
		defer cancel()
		if stopErr := themeRuntimeWatcher.Stop(shutdownCtx); stopErr != nil && logger != nil {
			logger.Warn("theme runtime watcher stop failed", "error", stopErr)
		}
	}
	themeRuntimeHandedOff := false
	defer func() {
		if !themeRuntimeHandedOff {
			stopThemeRuntimeWatcher()
		}
	}()
	if !cfg.SafeMode && !core.pluginRuntimeRecovery.Active() {
		themeRuntimeWatcher, err = startAPIThemeRuntimeWatcher(
			ctx, core.extensionStore, core.extensionService, logger, themeRuntimeStopTimeout,
		)
		if err != nil {
			core.closeRouteFailureRecorder()
			core.stopPluginRuntimeCoordinator()
			if stopErr := supportjobs.Stop(ctx, core.jobClient); stopErr != nil {
				logger.Warn("job dispatcher stop failed", "error", stopErr)
			}
			core.closePluginRuntime()
			_ = core.hostAPIGateway.Close()
			core.sharedRedisClient.Close()
			if closeErr := core.redisStorage.Close(); closeErr != nil {
				logger.Warn("redis session storage close failed", "error", closeErr)
			}
			core.closeNotificationStore()
			core.pool.Close()
			return nil, fmt.Errorf("start theme runtime watcher failed: %w", err)
		}
	}

	// Worker 心跳由 API 内嵌实例发布；NewWorker 仅保留给内部兼容测试。
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())

	var embeddedWorker *Worker
	if !core.pluginRuntimeRecovery.Active() {
		workerQueryInvalidation := newEmbeddedWorkerQueryInvalidationRuntime(cfg, core.hostInstallationID, logger)
		// Embed 时复用 API 已 Reconcile 的 core.extensionRuntime，避免每个后端插件双起子进程。
		// 插件 runtime 复用，但 Query invalidator 独占 Redis client，避免 worker
		// 的 terminal latch 污染 API execution cache。
		embeddedWorker, err = newWorkerWithPool(cfg, core.pool, logger, workerRuntimeDeps{
			ExtensionRuntime:  core.extensionRuntime,
			PluginSchedules:   core.lifecycleStack.Schedules,
			QueryInvalidation: workerQueryInvalidation,
			// 与 API 共用 Redis：flush_view_counts 读 API 写入的 view delta。
			HostCacheRedis: core.sharedRedisClient,
			OwnsRuntime:    false,
			// AI 回复复用 API 已装配的论坛服务与网关。worker 侧另建一份会得到
			// 独立的设置与发布策略，让同样内容在两处走不同的审核判断。
			AIReplyGenerator: newEmbeddedAIReplyGenerator(core),
			// 自主参与判定与生成共用同一份网关、论坛服务与队列入队器；判定
			// 组件与生成器绑定同一批依赖，避免两条路径对设置做出不同解释。
			AIReplyTriageDecider: newEmbeddedAITriageDecider(core),
			AIReplyThreadReader:  newEmbeddedAIThreadReader(core),
			AIReplyEnqueuer:      aireplyjobs.NewEnqueuer(supportjobs.NewDispatcher(core.jobClient)),
		})
		if err != nil {
			core.closeRouteFailureRecorder()
			stopThemeRuntimeWatcher()
			heartbeatCancel()
			core.stopPluginRuntimeCoordinator()
			if stopErr := supportjobs.Stop(ctx, core.jobClient); stopErr != nil {
				logger.Warn("job dispatcher stop failed", "error", stopErr)
			}
			core.closePluginRuntime()
			core.sharedRedisClient.Close()
			if closeErr := core.redisStorage.Close(); closeErr != nil {
				logger.Warn("redis session storage close failed", "error", closeErr)
			}
			core.closeNotificationStore()
			core.pool.Close()
			return nil, fmt.Errorf("embedded worker setup failed: %w", err)
		}
		if err := embeddedWorker.Start(ctx); err != nil {
			core.closeRouteFailureRecorder()
			stopThemeRuntimeWatcher()
			heartbeatCancel()
			embeddedWorker.Close()
			core.stopPluginRuntimeCoordinator()
			if stopErr := supportjobs.Stop(ctx, core.jobClient); stopErr != nil {
				logger.Warn("job dispatcher stop failed", "error", stopErr)
			}
			core.closePluginRuntime()
			core.sharedRedisClient.Close()
			if closeErr := core.redisStorage.Close(); closeErr != nil {
				logger.Warn("redis session storage close failed", "error", closeErr)
			}
			core.closeNotificationStore()
			core.pool.Close()
			return nil, fmt.Errorf("embedded worker start failed: %w", err)
		}
		go (&health.Publisher{Store: core.heartbeatStore}).Run(heartbeatCtx)
		logger.InfoContext(ctx, "embedded api worker started")
	}
	forumReadPolicyCtx, forumReadPolicyCancel := context.WithCancel(context.Background())
	go core.optionsService.RunForumReadPolicyRefresh(forumReadPolicyCtx, options.RecommendedForumReadPolicyRefreshInterval)
	extensionGuardPolicyCtx, extensionGuardPolicyCancel := context.WithCancel(context.Background())
	if !core.pluginRuntimeRecovery.Active() {
		go core.extensionGuardPolicy.RunRefresh(extensionGuardPolicyCtx, extensions.RecommendedGuardPolicyRefreshInterval)
	}
	databaseLeaseReaperCtx, databaseLeaseReaperCancel := context.WithCancel(context.Background())
	var databaseLeaseReaperWait sync.WaitGroup
	databaseLeaseReaperWait.Add(1)
	go func() {
		defer databaseLeaseReaperWait.Done()
		ticker := time.NewTicker(extensionsruntime.RecommendedExtensionDatabaseRuntimeLeaseReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-databaseLeaseReaperCtx.Done():
				return
			case <-ticker.C:
				if _, err := core.databaseLeaseRegistry.ReapExpiredRuntimeLeases(
					databaseLeaseReaperCtx, extensionsruntime.DefaultExtensionDatabaseRuntimeLeaseReapLimit,
				); err != nil && databaseLeaseReaperCtx.Err() == nil {
					logger.Warn("extension database runtime lease reaper degraded", "error", err)
				}
			}
		}
	}()
	pluginRuntimeFailures, pluginRuntimeMonitorDone := superviseAPIPluginRuntimeCoordinator(
		core.pluginRuntimeCoordinator,
		core.closePluginRuntime,
	)
	apiRuntimeFailures, stopAPIRuntimeFailureMerge := mergeAPIRuntimeFailureSources(
		pluginRuntimeFailures,
		themeRuntimeWatcher.Failures(),
	)

	// development：延迟清理 air 热重载后被 init 收养的 backend plugin 孤儿。
	// 必须在旧 sforum-api 被 stopBin 杀掉之后（kill_delay≈2s）再扫，pre_cmd 单独不够。
	stopOrphanPluginReaper := startDevelopmentOrphanPluginReaper(cfg, logger)

	api := &API{
		App:      app,
		Addr:     apiAddress(cfg),
		SEO:      core.productionSEO.Execution,
		failures: apiRuntimeFailures,
		close: func() {
			stopOrphanPluginReaper()
			core.closeRouteFailureRecorder()
			stopAPIRuntimeFailureMerge()
			core.stopPluginRuntimeCoordinator()
			if pluginRuntimeMonitorDone != nil {
				waitCtx, cancel := context.WithTimeout(context.Background(), core.pluginRuntimeStopTimeout)
				select {
				case <-pluginRuntimeMonitorDone:
				case <-waitCtx.Done():
					if logger != nil {
						logger.Warn("plugin runtime coordinator monitor stop timed out", "error", waitCtx.Err())
					}
				}
				cancel()
			}
			stopThemeRuntimeWatcher()
			databaseLeaseReaperCancel()
			databaseLeaseReaperWait.Wait()
			extensionGuardPolicyCancel()
			forumReadPolicyCancel()
			heartbeatCancel()
			if embeddedWorker != nil {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.WorkerShutdownTimeout)
				defer cancel()
				if err := embeddedWorker.Stop(shutdownCtx); err != nil {
					logger.Warn("embedded worker stop failed", "error", err)
				}
				embeddedWorker.Close()
			}
			if err := supportjobs.Stop(context.Background(), core.jobClient); err != nil {
				logger.Warn("job dispatcher stop failed", "error", err)
			}
			core.closePluginRuntime()
			_ = core.hostAPIGateway.Close()
			core.queryResultCache.Close(logger)
			if err := core.redisStorage.Close(); err != nil {
				logger.Warn("redis session storage close failed", "error", err)
			}
			// core.sharedRedisClient 被 humanverify 与 forum 缓存共用，关闭一次即可。
			if err := core.sharedRedisClient.Close(); err != nil {
				logger.Warn("shared redis client close failed", "error", err)
			}
			core.closeNotificationStore()
			core.pool.Close()
		},
	}
	// 成功装配：cache 所有权交给 API.close（close 闭包已捕获 core.queryResultCache）。
	_ = queryCacheOwnership.HandOff()
	select {
	case runtimeErr, ok := <-apiRuntimeFailures:
		if ok && runtimeErr != nil {
			api.Close()
			return nil, fmt.Errorf("API runtime failed during startup: %w", runtimeErr)
		}
	default:
	}
	themeRuntimeHandedOff = true
	return api, nil
}
