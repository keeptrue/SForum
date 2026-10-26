package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"
	redisstorage "github.com/gofiber/storage/redis/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	adminoverview "github.com/keeptrue/sforum/apps/api/app/Models/AdminOverview"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	uploadpolicy "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/UploadPolicy"
	database "github.com/keeptrue/sforum/apps/api/app/Models/Database"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	crypto "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	idempotency "github.com/keeptrue/sforum/apps/api/app/Support/Idempotency"
	installationidentity "github.com/keeptrue/sforum/apps/api/app/Support/InstallationIdentity"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	postgres "github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
	redisplatform "github.com/keeptrue/sforum/apps/api/app/Support/Redis"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

type apiInfrastructure struct {
	pool                  *pgxpool.Pool
	databaseLeaseRegistry *extensionsruntime.PostgresExtensionDatabaseRegistry
	hostInstallationID    string
	queryCursorSecret     []byte
	redisStorage          *redisstorage.Storage
	sharedRedisClient     *redis.Client
	queryResultCache      *productionQueryResultCacheRuntime
	optionCipher          *crypto.OptionCipher
	requiredReplayCipher  *idempotency.RequiredReplayCipher
	auditWriter           *audit.PostgresWriter
	optionsService        *options.Service
	seoHostPolicy         seoregistry.HostFinalPolicyConfig
	humanVerifier         *humanverify.RuntimeService
	authSessions          *authsession.Manager
	identityStore         *identity.PostgresStore
	sessionPolicyRenewal  *sessionPolicyRenewalGate
	identityAuthorityGate *deferredIdentityAuthorityMutationGate
	adminOverviewStore    *adminoverview.PostgresStore
	forumStore            *forum.PostgresStore
	forumCachedStore      forum.Store
	profileStore          *profile.PostgresStore
	moderationStore       *moderation.PostgresStore
	attachmentStore       *attachments.PostgresStore
	attachmentService     *attachments.Service
	databaseStore         *database.PostgresStore
	extensionStore        *extensions.PostgresStore
	frontendTrustStore    *extensions.PostgresFrontendTrustStore
	executableTrustStore  *extensions.PostgresExecutableTrustStore
	activationCoordinator *extensions.ActivationCoordinator
	jobClient             *supportjobs.Client
	jobDispatcher         *supportjobs.Dispatcher
}

func wireAPIInfrastructure(ctx context.Context, cfg config.Config, logger *slog.Logger) (*apiInfrastructure, error) {
	// T1B：注入稳定主体 HMAC 密钥（config 已按 APP_ENV=production 校验；禁止进程随机）。
	if err := identity.ConfigureIdentitySubjectHMAC(cfg.IdentitySubjectHMACSecret); err != nil {
		return nil, fmt.Errorf("configure identity subject hmac: %w", err)
	}

	if err := runStartupMigrations(ctx, cfg, logger); err != nil {
		return nil, err
	}

	pool, err := postgres.NewPoolWithOptions(ctx, cfg.DatabaseURL, postgres.PoolOptions{
		MaxConns:        cfg.DatabaseMaxConns,
		MinConns:        cfg.DatabaseMinConns,
		MaxConnIdleTime: cfg.DatabaseMaxConnIdleTime,
		MaxConnLifetime: cfg.DatabaseMaxConnLifetime,
		ConnectTimeout:  cfg.DatabaseConnectTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("postgres setup failed: %w", err)
	}
	databaseLeaseRegistry := extensionsruntime.NewPostgresExtensionDatabaseRegistry(pool, nil)
	if _, err := databaseLeaseRegistry.ReapExpiredRuntimeLeases(
		ctx, extensionsruntime.DefaultExtensionDatabaseRuntimeLeaseReapLimit,
	); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reap expired extension database runtime leases: %w", err)
	}
	hostInstallationID, err := installationidentity.NewPostgresRepository(pool).Ensure(ctx)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("ensure Host installation identity: %w", err)
	}
	queryCursorSecret, err := deriveQueryRegistryCursorSecret(cfg.SessionHashSecret, hostInstallationID)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("derive Query Registry cursor secret: %w", err)
	}

	redisStorage, err := redisplatform.NewStorage(cfg.RedisAddr, cfg.RedisPassword)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("redis session storage setup failed: %w", err)
	}
	// 共享 Redis client：humanverify 与 forum 业务读缓存复用同一连接池，避免多套连接。
	// session storage 走 fiber Storage 接口（内部封装独立连接），不参与合并。
	sharedRedisClient := humanverify.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword, humanverify.RedisClientOptions{
		PoolSize:        cfg.RedisPoolSize,
		MinIdleConns:    cfg.RedisMinIdleConns,
		DialTimeout:     cfg.RedisDialTimeout,
		ReadTimeout:     cfg.RedisReadTimeout,
		WriteTimeout:    cfg.RedisWriteTimeout,
		ConnMaxIdleTime: cfg.RedisConnMaxIdleTime,
		ConnMaxLifetime: cfg.RedisConnMaxLifetime,
	})
	queryResultCache, err := loadOptionalProductionQueryResultCache(ctx, cfg, hostInstallationID, logger)
	if err != nil {
		_ = sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("Query result cache setup failed: %w", err)
	}
	// 阶段移交：成功时 HandOff 给扩展平台阶段；失败路径由 defer 关闭，避免泄漏。
	queryCacheOwnership := newQueryResultCacheStageHandoff(queryResultCache, logger)
	defer queryCacheOwnership.CloseUnlessHandedOff()
	humanVerifyStore := humanverify.NewRedisStore(sharedRedisClient)
	optionStore := options.NewPostgresStore(pool)
	// H2a：构造敏感值加密器；未配置 APP_OPTION_ENC_KEY 时为透明模式（开发环境），生产由 C2 强制要求。
	optionCipher, err := crypto.NewOptionCipher(cfg.OptionEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("create option cipher: %w", err)
	}
	requiredReplayCipher, err := idempotency.NewRequiredReplayCipher(cfg.OptionEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("create required replay cipher: %w", err)
	}
	auditWriter := audit.NewPostgresWriter(pool)
	if cfg.SafeMode {
		if err := auditWriter.Append(ctx, audit.Event{
			Action:   audit.ActionExtensionSafeModeBoot,
			Metadata: map[string]any{"process": "api"},
		}); err != nil {
			logger.Warn("record safe mode boot audit failed", "error", err)
		}
	}
	optionsService := options.NewServiceWithDefaults(optionStore, optionsDefaultsFromConfig(cfg)).
		WithCipher(optionCipher).
		WithAuditor(auditWriter)
	if err := optionsService.EnsureDefaults(ctx); err != nil {
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("ensure runtime options defaults failed: %w", err)
	}
	if err := optionsService.RefreshForumReadPolicy(ctx); err != nil {
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("refresh forum read policy failed: %w", err)
	}
	seoSettings, err := optionsService.RuntimeSettings(ctx)
	if err != nil {
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("load SEO Host policy settings: %w", err)
	}
	seoValues, err := optionsService.InternalValues(ctx)
	if err != nil {
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("load SEO Host policy options: %w", err)
	}
	if strings.TrimSpace(seoSettings.SiteURL) == "" {
		seoSettings.SiteURL = cfg.AppURL
	}
	if len(seoSettings.SupportedLocales) == 0 {
		seoSettings.SupportedLocales = append([]string(nil), cfg.SupportedLocales...)
	}
	seoHostPolicy := seoregistry.HostFinalPolicyConfig{
		SiteURL: seoSettings.SiteURL, SupportedLocales: seoSettings.SupportedLocales,
		AllowIndexing:         productionSEOEnabled(seoValues[options.NameSEOAllowIndexing], true),
		SitemapEnabled:        productionSEOEnabled(seoValues[options.NameSEOSitemapEnabled], true),
		StructuredDataEnabled: productionSEOEnabled(seoValues[options.NameSEOSchemaOrgEnabled], true),
	}
	humanVerifier := humanverify.NewRuntimeService(optionsService, humanVerifyStore, humanVerifyConfigFromConfig(cfg))

	// CookieSecure：生产环境强制 true；此外当 APP_URL 是 https 时也启用，
	// 避免 APP_ENV=staging 等"非 production 但走 HTTPS"的部署误把 Secure 关掉导致 cookie 走 HTTP。
	sessionStore := session.NewStore(session.Config{
		Storage:         redisStorage,
		KeyGenerator:    secureSessionID,
		Extractor:       extractors.FromCookie("sforum_session"),
		IdleTimeout:     cfg.SessionIdleTimeout,
		AbsoluteTimeout: cfg.SessionAbsoluteTimeout,
		CookieHTTPOnly:  true,
		CookieSameSite:  fiber.CookieSameSiteLaxMode,
		CookiePath:      "/",
		CookieSecure:    shouldUseSecureCookie(cfg),
	})
	avatarOptions := avatarOptionsAdapter{options: optionsService}
	identityStore := identity.NewPostgresStoreWithAvatar(pool, avatarOptions)
	// Session policy renewal gate is bound after the lifecycle stack publishes
	// the Identity Registry and Session Policy Store. Until then renew is a
	// Core-equivalent no-op; revocation never uses this gate.
	sessionPolicyRenewal := &sessionPolicyRenewalGate{}
	// Host Command catalog freezes before lifecycle; adopt the Session Policy
	// writer fence once the store is published so status commands cannot race
	// accepted session effects.
	identityAuthorityGate := &deferredIdentityAuthorityMutationGate{}
	authSessions := authsession.NewManager(sessionStore, authsession.Config{
		RenewalInterval: cfg.SessionRenewalInterval,
		HashSecret:      cfg.SessionHashSecret,
		// M8：注入令牌版本号来源，使密码重置/封禁后旧会话失效。
		TokenVersion: func(ctx context.Context, userID int64) (int64, error) {
			return identityStore.GetUserTokenVersion(ctx, userID)
		},
		// 会话目录：登录时登记设备、CurrentUserID 校验是否被下线、logout 时标记。
		// identityStore 满足 authsession.SessionStore 接口（结构化匹配）。
		SessionStore:      identityStore,
		RenewalEffectGate: sessionPolicyRenewal.Evaluate,
	})
	adminOverviewStore := adminoverview.NewPostgresStore(pool)
	forumStore := forum.NewPostgresStoreWithAvatar(pool, avatarOptions).WithAuditor(auditWriter)
	// 业务读缓存复用 sharedRedisClient（与 humanverify 共享连接池）。
	// 失败不阻断启动——缓存为可重建的派生数据，降级为直连 PG。
	forumCachedStore := forum.NewCachedStore(forumStore, cache.NewRedisCache(sharedRedisClient))
	profileStore := profile.NewPostgresStore(pool)
	moderationStore := moderation.NewPostgresStore(pool)
	attachmentStore := attachments.NewPostgresStore(pool)
	attachmentUploadPolicy := uploadpolicy.NewService(uploadpolicy.NewPostgresStore(pool), identityStore)
	// E6.1：附件服务先创建，稍后注入扩展目录与禁用回落。
	attachmentService := attachments.NewServiceWithEvents(attachmentStore, optionsService, nil).
		WithUploadPolicy(attachmentUploadPolicy, int64(cfg.HTTPBodyLimit))
	databaseStore := database.NewPostgresStore(pool)
	extensionStore := extensions.NewPostgresStore(pool)
	frontendTrustStore := extensions.NewPostgresFrontendTrustStore(pool)
	executableTrustStore := extensions.NewPostgresExecutableTrustStore(pool)
	activationCoordinator := extensions.NewActivationCoordinator(extensionStore).WithAuditor(auditWriter)
	// Host API 需要 extensionService 的能力解析；先建 service 再绑 gateway。
	// 运行时 manager 在 service 创建后注入 HostAPI registrar。
	jobClient, err := supportjobs.NewInsertOnlyClient(pool, supportjobs.FromAppConfig(cfg))
	if err != nil {
		sharedRedisClient.Close()
		if closeErr := redisStorage.Close(); closeErr != nil {
			logger.Warn("redis session storage close failed", "error", closeErr)
		}
		pool.Close()
		return nil, fmt.Errorf("job dispatcher setup failed: %w", err)
	}
	jobDispatcher := supportjobs.NewDispatcher(jobClient)

	return &apiInfrastructure{
		pool:                  pool,
		databaseLeaseRegistry: databaseLeaseRegistry,
		hostInstallationID:    hostInstallationID,
		queryCursorSecret:     queryCursorSecret,
		redisStorage:          redisStorage,
		sharedRedisClient:     sharedRedisClient,
		queryResultCache:      queryCacheOwnership.HandOff(),
		optionCipher:          optionCipher,
		requiredReplayCipher:  requiredReplayCipher,
		auditWriter:           auditWriter,
		optionsService:        optionsService,
		seoHostPolicy:         seoHostPolicy,
		humanVerifier:         humanVerifier,
		authSessions:          authSessions,
		identityStore:         identityStore,
		sessionPolicyRenewal:  sessionPolicyRenewal,
		identityAuthorityGate: identityAuthorityGate,
		adminOverviewStore:    adminOverviewStore,
		forumStore:            forumStore,
		forumCachedStore:      forumCachedStore,
		profileStore:          profileStore,
		moderationStore:       moderationStore,
		attachmentStore:       attachmentStore,
		attachmentService:     attachmentService,
		databaseStore:         databaseStore,
		extensionStore:        extensionStore,
		frontendTrustStore:    frontendTrustStore,
		executableTrustStore:  executableTrustStore,
		activationCoordinator: activationCoordinator,
		jobClient:             jobClient,
		jobDispatcher:         jobDispatcher,
	}, nil
}
