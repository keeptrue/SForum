package bootstrap

import (
	"context"
	"log/slog"
	"time"

	redisstorage "github.com/gofiber/storage/redis/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	providers "github.com/keeptrue/sforum/apps/api/app/Providers"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	health "github.com/keeptrue/sforum/apps/api/app/Support/Health"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/config"
)

// Production API 分阶段装配（NewAPI 见 app.go 薄封装）。

// apiCoreStack 是 foundation+domain 装配完成后交给 HTTP/worker 阶段的依赖包。
// 失败路径由各阶段自己的收尾逻辑释放已取得的资源；成功后由 API.close 统一收尾。
type apiCoreStack struct {
	adminOverviewProvider        *providers.AdminOverviewProvider
	aiProvider                   *providers.AIProvider
	aiOrchestrator               *supportai.Orchestrator
	systemUpdatesProvider        *providers.SystemUpdatesProvider
	apiTokenService              *apitokens.Service
	attachmentsProvider          *providers.AttachmentsProvider
	attachmentService            *attachments.Service
	auditWriter                  audit.Writer
	authSessions                 *authsession.Manager
	closePluginRuntime           func()
	closeRouteFailureRecorder    func()
	closeCommentRevisionHub      func()
	databaseLeaseRegistry        *extensionsruntime.PostgresExtensionDatabaseRegistry
	databaseProvider             *providers.DatabaseProvider
	entityMetaProvider           *providers.EntityMetaProvider
	extensionGuardPolicy         *extensions.GuardPolicyCatalog
	extensionRuntime             extensionRuntime
	extensionService             *extensions.Service
	extensionStore               *extensions.PostgresStore
	extensionsProvider           *providers.ExtensionsProvider
	forumProvider                *providers.ForumProvider
	heartbeatStore               *health.RedisHeartbeatStore
	hostAPIGateway               *hostapi.Gateway
	hostInstallationID           string
	identityProvider             *providers.IdentityProvider
	identityStore                *identity.PostgresStore
	jobClient                    *supportjobs.Client
	jobsProvider                 *providers.JobsProvider
	lifecycleStack               *productionLifecycleStack
	mailProvider                 *providers.MailProvider
	moderationProvider           *providers.ModerationProvider
	notificationsProvider        *providers.NotificationsProvider
	closeNotificationStore       func()
	optionsProvider              *providers.OptionsProvider
	optionsService               *options.Service
	pagesProvider                *providers.PagesProvider
	pluginRuntimeCoordinator     *pluginRuntimeCoordinatorRuntime
	pluginRuntimeRecovery        *health.RecoveryRequirement
	pluginRuntimeStopTimeout     time.Duration
	pool                         *pgxpool.Pool
	productionSEO                *productionSEORegistry
	profileProvider              *providers.ProfileProvider
	queryResultCache             *productionQueryResultCacheRuntime
	readyEvaluate                func(context.Context) health.ReadyReport
	redisStorage                 *redisstorage.Storage
	routeDispatcher              *routes.Dispatcher
	seoProvider                  *providers.SEOProvider
	sharedRedisClient            *redis.Client
	siteChromeProvider           *providers.SiteChromeProvider
	stopPluginRuntimeCoordinator func()
	webhooksProvider             *providers.WebhooksProvider
}

// apiStartupPoolCloseTimeout 是启动失败路径关池的兜底上限。
const apiStartupPoolCloseTimeout = 5 * time.Second

// closeAPIPoolOnFailure 在启动失败路径关闭连接池，并给 pool.Close 加超时看门狗。
// puddle 的 Close 会等待所有已借出的资源归还：任何仍在持连接的协程（例如漏停的
// LISTEN 循环）都会让它永久阻塞，把「启动失败」变成「进程活着但不监听」。超时
// 只记录日志并继续返回真实启动错误，避免兜底本身成为新的挂死点。
func closeAPIPoolOnFailure(logger *slog.Logger, pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pool.Close()
	}()
	timer := time.NewTimer(apiStartupPoolCloseTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		if logger != nil {
			logger.Error("api database pool close timed out during startup failure",
				"timeout", apiStartupPoolCloseTimeout)
		}
	}
}

func buildProductionAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) (*API, error) {
	core, err := wireAPICoreStack(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	return finishAPIHTTP(ctx, cfg, logger, core)
}

// wireAPICoreStack 按所有权顺序串联基础设施、扩展平台和领域服务阶段。
func wireAPICoreStack(ctx context.Context, cfg config.Config, logger *slog.Logger) (*apiCoreStack, error) {
	infrastructure, err := wireAPIInfrastructure(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	extensionPlatform, err := wireAPIExtensionPlatform(ctx, cfg, logger, infrastructure)
	if err != nil {
		return nil, err
	}
	core, err := wireAPIDomainServices(ctx, cfg, logger, infrastructure, extensionPlatform)
	if err != nil {
		return nil, err
	}
	// 内置插件自动更新在领域服务与插件运行时协调器就绪之后、HTTP 路由冻结之前执行。
	// 失败只记录日志/事件，不阻断启动。
	runBuiltinPluginAutoUpdate(ctx, cfg.SafeMode, core, logger)
	return core, nil
}
