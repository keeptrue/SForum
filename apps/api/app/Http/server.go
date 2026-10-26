package http

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/compress"
	"github.com/gofiber/fiber/v3/middleware/csrf"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
	health "github.com/keeptrue/sforum/apps/api/app/Support/Health"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/config"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

type healthResponse struct {
	Name             string    `json:"name"`
	Status           string    `json:"status"`
	RecoveryRequired bool      `json:"recoveryRequired,omitempty"`
	Environment      string    `json:"environment"`
	Locale           string    `json:"locale"`
	SupportedLocales []string  `json:"supportedLocales"`
	Time             time.Time `json:"time"`
	// CoreVersion 是兼容判定唯一权威；不含 commit/dev 标识。
	CoreVersion string `json:"coreVersion"`
}

type RouteProvider interface {
	RegisterRoutes(api fiber.Router)
}

// ReadyEvaluator 返回 readiness 探测报告。由 bootstrap 注入真实依赖检查。
// 为 nil 时 /ready 仅返回 ready=true（测试/无依赖装配场景）。
type ReadyEvaluator func(ctx context.Context) health.ReadyReport

type Dependencies struct {
	RouteProviders []RouteProvider
	// RoutePlans 与 Dispatcher 共享同一个 production resolver，只做无副作用
	// 入口分类，使任意 public/admin 路径能进入正确的 Host middleware 链。
	RoutePlans routes.PlanResolver
	// RouteDispatcher 在硬编码 core provider 前消费不可变 Route Registry plan。
	// nil 保持旧路由行为，便于独立测试与回滚。
	RouteDispatcher *routes.Dispatcher
	RouteActors     RouteActorLoader
	// PluginSessionScopes 解析当前请求的 Core 会话范围（S9）。为 nil 时不签发任何
	// 会话委派，插件也就看不到会话状态（fail closed）。
	PluginSessionScopes PluginSessionScopeSource
	Options             *options.Service
	// Storage 用于分布式限流。为 nil 时 limiter 退化为进程内存限流。
	Storage fiber.Storage
	// Ready 为 /api/v1/ready 探测函数（PG required；Redis degraded）。
	Ready ReadyEvaluator
	// BearerTokens 可选：启用 Authorization: Bearer PAT（F3.4）。
	BearerTokens BearerAuthenticator
	// Auditor 可选：PAT 写请求轻量审计。
	Auditor audit.Writer
	// Recovery restricts HTTP to Host-owned liveness/readiness after plugin
	// bootstrap fails. It does not enable or mutate any extension artifact.
	Recovery *health.RecoveryRequirement
}

func NewApp(cfg config.Config, logger *slog.Logger, deps Dependencies) *fiber.App {
	// 进程级真实 IP 解析器：业务统一走 clientip.FromCtx。
	clientip.Configure(clientip.Config{
		Proxies:       cfg.TrustedProxies,
		TrustPrivate:  cfg.TrustProxyPrivate,
		TrustLoopback: cfg.TrustProxyLoopback,
	})
	if logger != nil && strings.EqualFold(cfg.AppEnv, "production") && cfg.TrustProxy && len(cfg.TrustedProxies) == 0 && !cfg.TrustProxyPrivate && !cfg.TrustProxyLoopback {
		// 生产开了 TrustProxy 却没有任何信任集合：c.IP()/转发头不会被采信，限流与审计可能只看到边缘 IP。
		logger.Warn("trust_proxy enabled but TRUSTED_PROXIES is empty; client IPs may fall back to TCP remote")
	}

	proxyHeader := strings.TrimSpace(cfg.ProxyHeader)
	if proxyHeader == "" {
		proxyHeader = fiber.HeaderXForwardedFor
	}
	app := fiber.New(fiber.Config{
		AppName:      cfg.AppName,
		ErrorHandler: errorHandler(logger),
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
		IdleTimeout:  cfg.HTTPIdleTimeout,
		BodyLimit:    cfg.HTTPBodyLimit,
		// Trusted multipart/stream routes forward request chunks with bounded
		// memory; ordinary handlers still materialize c.Body() on demand.
		StreamRequestBody:            true,
		DisablePreParseMultipartForm: true,
		// Fiber 内置 c.IP() / 限流也走同一套信任代理策略。
		ProxyHeader: proxyHeader,
		TrustProxy:  cfg.TrustProxy,
		TrustProxyConfig: fiber.TrustProxyConfig{
			Proxies:   cfg.TrustedProxies,
			Private:   cfg.TrustProxyPrivate,
			Loopback:  cfg.TrustProxyLoopback,
			LinkLocal: false,
		},
		EnableIPValidation: true,
	})
	app.Hooks().OnPreStartupMessage(func(sm *fiber.PreStartupMessageData) error {
		sm.BannerHeader = sforumStartupBanner
		return nil
	})

	app.Use(requestid.New())
	app.Use(recover.New())
	// 响应压缩：根据 Accept-Encoding 自动 brotli/gzip，降低带宽占用。
	app.Use(compress.New(compress.Config{Level: compress.Level(cfg.CompressLevel)}))
	app.Use(recoveryOnlyMiddleware(deps.Recovery))
	app.Use(routeRegistryIngressMiddleware(deps.RoutePlans))
	// 写接口限流：跳过 GET/HEAD/OPTIONS，按 IP 限制单位时间内的写操作次数。
	// Storage 注入时为分布式限流（多实例共享），否则退化为进程内存限流。
	if cfg.LimiterWriteMax > 0 && cfg.LimiterWindow > 0 {
		app.Use(routeRegistryManagedOnly(limiter.New(limiter.Config{
			Storage:    deps.Storage,
			Max:        cfg.LimiterWriteMax,
			Expiration: cfg.LimiterWindow,
			Next: func(c fiber.Ctx) bool {
				// 只限写方法，读请求直接放行（读路径已有缓存挡）。
				switch c.Method() {
				case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
					return true
				}
				return false
			},
		})))
	}
	app.Use(routeRegistryManagedOnly(localeMiddleware(cfg, deps.Options)))
	// 维护模式：拦截非管理员的写操作；GET 健康检查与登录/注册/web-options 仍可用。
	app.Use(routeRegistryManagedOnly(maintenanceMiddleware(deps.Options)))
	registerRouteRegistryMiddleware(app, cfg, deps)

	registerRoutes(app, cfg, deps)

	return app
}

func registerRouteRegistryMiddleware(app *fiber.App, cfg config.Config, deps Dependencies) {
	// CSRF 防护：double-submit cookie + Origin/Referer 校验，保护所有 unsafe 方法。
	// 仅对 /api/v1 或已由 Registry plan 命中的任意路径启用；未知 Nuxt
	// 请求不能被 CSRF/Bearer 提前截获。
	// TrustedOrigins 必须包含公开站点 origin：API 在反向代理后看到的 Host 是内部地址，
	// 而 Origin 是公开站点，二者不匹配会被默认拒绝。默认从 APP_URL 派生。
	// CSRF 防护仅在配置启用时生效；测试场景通过 CSRF_ENABLED=false 显式关闭，
	// 避免每个测试请求都要携带 token。生产默认启用。
	if cfg.CSRFEnabled {
		app.Use(routeRegistryManagedOnly(csrf.New(csrf.Config{
			Storage:        deps.Storage,
			CookieSameSite: fiber.CookieSameSiteLaxMode,
			// 与 session cookie 共用 Secure 判定，避免 staging HTTPS 下 csrf_ 仍可明文读取。
			CookieSecure:   config.ShouldUseSecureCookie(cfg),
			CookieHTTPOnly: false, // SPA 必须能读取 csrf_ cookie 以回传 token
			CookiePath:     "/",
			TrustedOrigins: cfg.CSRFTrustedOrigins,
			ErrorHandler:   csrfErrorHandler,
			// 入站 webhook / Bearer PAT 由非浏览器客户端调用，无 CSRF cookie。
			Next: func(c fiber.Ctx) bool {
				path := c.Path()
				if strings.HasPrefix(path, "/api/v1/webhooks/inbound/") {
					return true
				}
				// PAT：Authorization Bearer sft_... 跳过 CSRF（F3.4）。
				authz := c.Get("Authorization")
				return strings.HasPrefix(authz, "Bearer sft_")
			},
		})))
	}

	// F3.4：Bearer PAT 鉴权（在路由前解析，供各 controller 读取 context）。
	if deps.BearerTokens != nil {
		app.Use(routeRegistryManagedOnly(bearerMiddleware(deps.BearerTokens, deps.Auditor)))
	}
	// S9：宿主解析出的 Core 会话范围只挂在本次请求的 context 上，因此只有宿主
	// 自己在这次 HTTP 请求里发起的插件调用才拿得到会话委派。必须排在 dispatcher 之前。
	if deps.PluginSessionScopes != nil {
		app.Use(routeRegistryManagedOnly(pluginSessionScopeMiddleware(deps.PluginSessionScopes)))
	}
	if deps.RouteDispatcher != nil {
		app.Use(routeRegistryManagedOnly(routeDispatcherMiddleware(deps.RouteDispatcher, deps.RouteActors)))
	}
}

func registerRoutes(app *fiber.App, cfg config.Config, deps Dependencies) {
	api := app.Group("/api/v1")

	for _, provider := range deps.RouteProviders {
		if provider != nil {
			provider.RegisterRoutes(api)
		}
	}

	// Liveness：进程存活即可，不探测依赖（K8s/Compose liveness probe 用）。
	api.Get("/health", func(c fiber.Ctx) error {
		settings := runtimeSettings(c.Context(), cfg, deps.Options)
		status := "ok"
		if deps.Recovery.Active() {
			status = "recovery_required"
		}
		return OK(c, healthResponse{
			Name:             settings.SiteName,
			Status:           status,
			RecoveryRequired: deps.Recovery.Active(),
			Environment:      cfg.AppEnv,
			Locale:           settings.DefaultLocale,
			SupportedLocales: settings.SupportedLocales,
			Time:             time.Now().UTC(),
			CoreVersion:      platformversion.CoreCompatibilityVersion(),
		})
	})

	// Readiness：依赖探测。PG 失败 → 503；Redis 失败 → 200 + degraded。
	api.Get("/ready", func(c fiber.Ctx) error {
		report := health.ReadyReport{
			Status:     "ready",
			Ready:      true,
			CheckedAt:  time.Now().UTC(),
			Components: []health.ComponentResult{},
		}
		if deps.Ready != nil {
			// 给探测一个短超时，避免就绪探针挂死。
			ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
			defer cancel()
			report = deps.Ready(ctx)
		}
		if !report.Ready {
			// 就绪失败仍返回结构化 report，便于探针与运维诊断。
			return JSON(c, fiber.StatusServiceUnavailable, "service.not_ready", report)
		}
		return OK(c, report)
	})
}

func recoveryOnlyMiddleware(requirement *health.RecoveryRequirement) fiber.Handler {
	return func(c fiber.Ctx) error {
		if !requirement.Active() {
			return c.Next()
		}
		switch c.Path() {
		case "/api/v1/health", "/api/v1/ready":
			return c.Next()
		default:
			return JSON(c, fiber.StatusServiceUnavailable, "service.recovery_required", requirement)
		}
	}
}

func runtimeSettings(ctx context.Context, cfg config.Config, service *options.Service) options.RuntimeSettings {
	if service != nil {
		if settings, err := service.RuntimeSettings(ctx); err == nil {
			return settings
		}
	}
	return fallbackRuntimeSettings(cfg)
}

func fallbackRuntimeSettings(cfg config.Config) options.RuntimeSettings {
	return options.RuntimeSettings{
		SiteName:                  cfg.AppName,
		SiteURL:                   "",
		DefaultLocale:             cfg.AppLocale,
		SupportedLocales:          cfg.SupportedLocales,
		HumanVerificationProvider: cfg.HumanVerificationProvider,
	}
}

// csrfErrorHandler 把 CSRF 中间件的错误映射为统一 envelope reason。
// token 缺失/不匹配 → csrf.invalid；Origin/Referer 不匹配 → csrf.origin_invalid。
// 返回 *APIError 由全局 errorHandler 渲染成 {code,message,data:{reason}} 结构。
func csrfErrorHandler(_ fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, csrf.ErrOriginNoMatch),
		errors.Is(err, csrf.ErrOriginInvalid),
		errors.Is(err, csrf.ErrRefererNoMatch),
		errors.Is(err, csrf.ErrRefererInvalid),
		errors.Is(err, csrf.ErrRefererNotFound):
		return NewError(fiber.StatusForbidden, "csrf.origin_invalid")
	default:
		return NewError(fiber.StatusForbidden, "csrf.invalid")
	}
}

const sforumStartupBanner = `
   _____ ______
  / ___// ____/___  _______  ______ ___
  \__ \/ /_  / __ \/ ___/ / / / _  _  \
 ___/ / __/ / /_/ / /  / /_/ / / / / / /
/____/_/    \____/_/   \__,_/_/ /_/ /_/    SForum API
--------------------------------------------------`
