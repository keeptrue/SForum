package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	lifecyclerecovery "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/LifecycleRecovery"
	health "github.com/keeptrue/sforum/apps/api/app/Support/Health"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
	"github.com/keeptrue/sforum/apps/api/config"
)

type apiExtensionRestoreInput struct {
	infrastructure         *apiInfrastructure
	extensionService       *extensions.Service
	frontendService        *extensions.FrontendService
	executableTrustService *extensions.ExecutableTrustService
	databaseCatalogBinder  postgresProtocolV2DatabaseCatalogBinder
	extensionRuntime       extensionRuntime
	lifecycleRuntime       *extensionsruntime.Manager
	lifecycleStack         *productionLifecycleStack
	systemTierOrder        []string
	// aiToolRefresh 在期望集应用成功后重建 AI 工具登记表。它由扩展平台装配阶段
	// 创建、领域装配阶段 Arm，因此这里只负责把它交给协调器。
	aiToolRefresh *aiToolRefreshHook
}

type apiExtensionRuntimeState struct {
	pluginRuntimeCoordinator     *pluginRuntimeCoordinatorRuntime
	pluginRuntimeRecovery        *health.RecoveryRequirement
	pluginRuntimeStopTimeout     time.Duration
	stopPluginRuntimeCoordinator func()
	closePluginRuntime           func()
	aiToolRefresh                *aiToolRefreshHook
}

func restoreAPIExtensionPlatform(ctx context.Context, cfg config.Config, logger *slog.Logger, input apiExtensionRestoreInput) (*apiExtensionRuntimeState, error) {
	pool := input.infrastructure.pool
	redisStorage := input.infrastructure.redisStorage
	sharedRedisClient := input.infrastructure.sharedRedisClient
	optionsService := input.infrastructure.optionsService
	extensionStore := input.infrastructure.extensionStore
	jobClient := input.infrastructure.jobClient
	extensionService := input.extensionService
	frontendService := input.frontendService
	executableTrustService := input.executableTrustService
	databaseCatalogBinder := input.databaseCatalogBinder
	extensionRuntime := input.extensionRuntime
	lifecycleRuntime := input.lifecycleRuntime
	lifecycleStack := input.lifecycleStack
	if _, err := extensionService.SyncBuiltins(ctx); err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("sync builtin extensions failed: %w", err)
	}
	if err := syncExternalExtensionSources(ctx, logger, extensionService); err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("sync external extension sources failed: %w", err)
	}
	// 开放中的 lifecycle operation 会让 plugin runtime fence 拒绝收敛。这里
	// 先把根因写进日志：否则现场只剩「启动未就绪」这个现象，没有任何可操作
	// 信息（这正是该故障最昂贵的部分）。
	if openOperations, openErr := extensionStore.ListOpenLifecycleOperations(ctx, 8); openErr != nil {
		logger.Warn("inspect open extension lifecycle operations failed", "error", openErr)
	} else {
		for _, operation := range openOperations {
			logger.Warn("open extension lifecycle operation blocks plugin runtime convergence",
				"operationId", operation.ID,
				"extensionId", operation.ExtensionID,
				"operation", operation.Operation,
				"state", operation.State,
				"currentStepId", operation.CurrentStepID,
				"attemptCount", operation.AttemptCount,
				"startedAt", operation.StartedAt,
			)
		}
	}
	// DatabaseService 的 SQL catalog 必须在首次插件 Reconcile/broker 注册前冻结。
	if err := bindProductionProtocolV2DatabaseRuntime(ctx, databaseCatalogBinder, extensionStore, cfg.SafeMode); err != nil {
		if stopErr := supportjobs.Stop(ctx, jobClient); stopErr != nil {
			logger.Warn("job dispatcher stop failed", "error", stopErr)
		}
		extensionRuntime.Close(ctx)
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("DatabaseService runtime setup failed: %w", err)
	}
	// API 启动恢复：活动主题 L0/L1 + 已启用插件页面贡献。Asset Registry
	// 此时尚未注入 Service；唯一权威恢复在 runtime reconciliation 之后。
	// 无效主题安全回退默认；失败不得留下空 Registry 却 DB 指向主题的分裂状态。
	if cfg.SafeMode {
		if err := extensionService.RestoreSafeModeThemeRegistry(ctx); err != nil {
			logger.Warn("restore safe mode default theme registry failed", "error", err)
		}
	} else if err := extensionService.RestoreActiveThemeRegistry(ctx); err != nil {
		logger.Warn("restore active theme page registry failed", "error", err)
	}
	legacyMailValues, err := optionsService.InternalValues(ctx)
	if err != nil {
		return nil, fmt.Errorf("load legacy mail options: %w", err)
	}
	if err := notifications.NewPostgresStore(pool).AdoptLegacyMail(ctx, legacyMailValues); err != nil {
		return nil, fmt.Errorf("adopt legacy mail settings: %w", err)
	}
	var pluginRuntimeCoordinator *pluginRuntimeCoordinatorRuntime
	var pluginRuntimeRecovery *health.RecoveryRequirement
	pluginRuntimeStopTimeout := normalizedPluginRuntimeCoordinatorStopTimeout(cfg.WorkerShutdownTimeout)
	stopPluginRuntimeCoordinator := func() {
		if pluginRuntimeCoordinator == nil {
			return
		}
		if stopErr := pluginRuntimeCoordinator.Stop(context.Background()); stopErr != nil && logger != nil {
			logger.Warn("plugin runtime coordinator stop failed", "error", stopErr)
		}
	}
	var closePluginRuntimeOnce sync.Once
	closePluginRuntime := func() {
		closePluginRuntimeOnce.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), pluginRuntimeStopTimeout)
			defer cancel()
			extensionRuntime.Close(shutdownCtx)
		})
	}
	var reconciledExtensions []extensions.Extension
	// 中断 activation 修复：state 事务已提交 target、publication 未写 commit
	// marker 的 operation 会让 extensions.active_version_id 与已收敛的 runtime
	// 永久分叉，之后每次 lifecycle 都会 drain 一个替身实例并失败关闭。修复只
	// 使用 durable 证据，必须在 plugin runtime 收敛之前执行；失败不阻断启动
	// （运行期仍有 drain 守卫与 source fail-closed 兜底）。
	if !cfg.SafeMode {
		recovered, recoveryErr := lifecyclerecovery.NewPostgresInterruptedActivationRecovery(pool).
			RecoverInterruptedActivations(ctx, 0)
		if recoveryErr != nil {
			logger.Warn("recover interrupted activation state failed; continuing startup", "error", recoveryErr)
		}
		for _, item := range recovered {
			logger.Warn("recovered interrupted activation state",
				"operationId", item.OperationID,
				"extensionId", item.ExtensionID,
				"fromVersionId", item.FromVersionID,
				"toVersionId", item.ToVersionID,
				"auditEventId", item.AuditEventID,
			)
		}
	}
	if cfg.SafeMode {
		reconciledExtensions, err = reconcileAPIExtensionRuntime(ctx, true, extensionStore, extensionRuntime)
	} else {
		pluginRuntimeCoordinator, err = startPluginRuntimeCoordinator(ctx, pluginRuntimeCoordinatorBootstrapConfig{
			ProcessRole:   extensions.PluginRuntimeProcessAPI,
			Store:         extensionStore,
			Manager:       lifecycleRuntime,
			Logger:        logger,
			StopTimeout:   cfg.WorkerShutdownTimeout,
			TierOrder:     input.systemTierOrder,
			AIToolRefresh: input.aiToolRefresh,
		})
		if err == nil {
			reconciledExtensions, err = extensionStore.List(ctx)
		}
	}
	if err != nil && !cfg.SafeMode {
		convergenceErr := err
		stopPluginRuntimeCoordinator()
		pluginRuntimeCoordinator = nil
		pluginRuntimeRecovery = newPluginRuntimeRecoveryRequirement(ctx, extensionStore, convergenceErr)
		logger.Error(
			"plugin runtime convergence failed; entering Host recovery-only mode",
			"error", convergenceErr,
			"publication_revision", pluginRuntimeRecovery.PublicationRevision,
			"artifacts", len(pluginRuntimeRecovery.Artifacts),
		)
		if restoreErr := extensionService.RestoreSafeModeThemeRegistry(ctx); restoreErr != nil {
			logger.Warn("restore recovery-only default theme registry failed", "error", restoreErr)
		}
		reconciledExtensions, err = reconcileAPIExtensionRuntime(ctx, true, extensionStore, extensionRuntime)
		if err != nil {
			err = fmt.Errorf("enter recovery-only mode after %v: %w", convergenceErr, err)
		}
	}
	if err != nil {
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
		return nil, fmt.Errorf("start exact plugin runtime reconciliation failed: %w", err)
	}
	effectiveSafeMode := cfg.SafeMode || pluginRuntimeRecovery.Active()
	if err := lifecycleStack.Registries.RestoreRoutePublications(ctx, reconciledExtensions, effectiveSafeMode); err != nil {
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
		return nil, fmt.Errorf("restore extension route publications failed: %w", err)
	}
	if err := lifecycleStack.bindAssetRegistryConsumers(
		extensionService, frontendService, executableTrustService,
	); err != nil {
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
		return nil, fmt.Errorf("bind extension asset registry consumers failed: %w", err)
	}
	return &apiExtensionRuntimeState{
		pluginRuntimeCoordinator:     pluginRuntimeCoordinator,
		pluginRuntimeRecovery:        pluginRuntimeRecovery,
		pluginRuntimeStopTimeout:     pluginRuntimeStopTimeout,
		stopPluginRuntimeCoordinator: stopPluginRuntimeCoordinator,
		closePluginRuntime:           closePluginRuntime,
		aiToolRefresh:                input.aiToolRefresh,
	}, nil
}

func newPluginRuntimeRecoveryRequirement(
	ctx context.Context,
	store *extensions.PostgresStore,
	cause error,
) *health.RecoveryRequirement {
	requirement := &health.RecoveryRequirement{
		Code:      health.PluginRuntimeRecoveryCode,
		Component: "plugin_runtime",
		Message:   pluginRuntimeRecoveryMessage(cause),
	}
	if store == nil {
		return requirement
	}
	publication, err := store.LatestPluginRuntimePublication(ctx)
	if err != nil {
		return requirement
	}
	requirement.PublicationRevision = publication.Revision
	requirement.Artifacts = make([]health.RecoveryArtifact, 0, len(publication.Members))
	for _, member := range publication.Members {
		requirement.Artifacts = append(requirement.Artifacts, health.RecoveryArtifact{
			ExtensionID: member.ExtensionID,
			Version:     member.ExtensionVersion,
			Digest:      member.PackageDigest,
		})
	}
	return requirement
}

func pluginRuntimeRecoveryMessage(err error) string {
	switch {
	case errors.Is(err, errPluginRuntimeCoordinatorStartupPending):
		return "plugin runtime did not converge before the startup deadline; an extension lifecycle operation is still open"
	case errors.Is(err, pluginbootstrap.ErrBootstrapABIIncompatible):
		return "plugin process bootstrap ABI is incompatible"
	case errors.Is(err, pluginbootstrap.ErrExecutableArchitecture):
		return "plugin executable architecture is incompatible"
	case errors.Is(err, pluginbootstrap.ErrExecutableDependency):
		return "plugin executable dependency is unavailable"
	case errors.Is(err, pluginbootstrap.ErrExecutablePermission):
		return "plugin executable permission was denied"
	case errors.Is(err, pluginbootstrap.ErrProcessStart):
		return "plugin process failed to start"
	default:
		return "plugin runtime convergence failed; inspect API logs for the exact cause"
	}
}
