package bootstrap

import (
	"context"
	"log/slog"
	"time"

	redisstorage "github.com/gofiber/storage/redis/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	apitokens "github.com/zhuchunshu/sforum/apps/api/app/Models/APITokens"
	attachments "github.com/zhuchunshu/sforum/apps/api/app/Models/Attachments"
	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	options "github.com/zhuchunshu/sforum/apps/api/app/Models/Options"
	providers "github.com/zhuchunshu/sforum/apps/api/app/Providers"
	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
	audit "github.com/zhuchunshu/sforum/apps/api/app/Support/Audit"
	authsession "github.com/zhuchunshu/sforum/apps/api/app/Support/AuthSession"
	extensionsruntime "github.com/zhuchunshu/sforum/apps/api/app/Support/Extensions"
	health "github.com/zhuchunshu/sforum/apps/api/app/Support/Health"
	hostapi "github.com/zhuchunshu/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/zhuchunshu/sforum/apps/api/app/Support/Jobs"
	routes "github.com/zhuchunshu/sforum/apps/api/app/Support/Routes"
	"github.com/zhuchunshu/sforum/apps/api/config"
)

// Production API 分阶段装配（NewAPI 见 app.go 薄封装）。

// apiCoreStack 是 foundation+domain 装配完成后交给 HTTP/worker 阶段的依赖包。
// 失败路径的资源关闭仍在 wireAPICoreStack 内联处理；成功后由 API.close 统一收尾。
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
	return wireAPIDomainServices(ctx, cfg, logger, infrastructure, extensionPlatform)
}
