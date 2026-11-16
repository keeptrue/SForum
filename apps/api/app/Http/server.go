package http

import (
	"context"
	"errors"
	"fmt"
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
		TrustProxy:    cfg.TrustProxy,
		Proxies:       cfg.TrustedProxies,
		TrustPrivate:  cfg.TrustProxyPrivate,
		TrustLoopback: cfg.TrustProxyLoopback,
	})
	if logger != nil {
		// 生效策略必须可观测：生产上「真实 IP 变成服务端跳数地址」只能靠这行定位。
		logger.Info(fmt.Sprintf("client_ip policy: trust=%t private=%t loopback=%t proxies=%v",
			cfg.TrustProxy, cfg.TrustProxyPrivate, cfg.TrustProxyLoopback, cfg.TrustedProxies))
	}
	if logger != nil && cfg.TrustProxy && len(cfg.TrustedProxies) == 0 && !cfg.TrustProxyPrivate && !cfg.TrustProxyLoopback {
		// 开了总开关却没有任何信任集合：转发头不会被采信，限流与审计只能看到 TCP 对端。
		logger.Warn("trust_proxy enabled but no trusted proxies configured; client IPs will fall back to TCP remote")
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
			Storage:    withStorageFailureMarking(deps.Storage),
			Max:        cfg.LimiterWriteMax,
			Expiration: cfg.LimiterWindow,
			// Fiber 的 c.IP() 在「链条全部判为可信」时会回退到最左条目
			// (fiber@v3.4.0/req.go:658-692)，与 clientip 的判定分歧且可伪造，
			// 所以限流键统一走 clientip.FromCtx。
			KeyGenerator: func(c fiber.Ctx) string { return clientip.FromCtx(c) },
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
	registerRouteRegistryMiddleware(app, cfg, deps, logger)

	registerRoutes(app, cfg, deps)

	return app
}

func registerRouteRegistryMiddleware(app *fiber.App, cfg config.Config, deps Dependencies, logger *slog.Logger) {
	// CSRF 防护：double-submit cookie + Origin/Referer 校验，保护所有 unsafe 方法。
	// 仅对 /api/v1 或已由 Registry plan 命中的任意路径启用；未知 Nuxt
	// 请求不能被 CSRF/Bearer 提前截获。
	// TrustedOrigins 必须包含公开站点 origin：API 在反向代理后看到的 Host 是内部地址，
	// 而 Origin 是公开站点，二者不匹配会被默认拒绝。默认从 APP_URL 派生。
	// CSRF 防护仅在配置启用时生效；测试场景通过 CSRF_ENABLED=false 显式关闭，
	// 避免每个测试请求都要携带 token。生产默认启用。
	if cfg.CSRFEnabled {
		app.Use(routeRegistryManagedOnly(csrf.New(csrf.Config{
			Storage:        withStorageFailureMarking(deps.Storage),
			CookieSameSite: fiber.CookieSameSiteLaxMode,
			// 与 session cookie 共用 Secure 判定，避免 staging HTTPS 下 csrf_ 仍可明文读取。
			CookieSecure:   config.ShouldUseSecureCookie(cfg),
			CookieHTTPOnly: false, // SPA 必须能读取 csrf_ cookie 以回传 token
			CookiePath:     "/",
			TrustedOrigins: cfg.CSRFTrustedOrigins,
			ErrorHandler:   csrfErrorHandler(logger),
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
		// 匿名探针只拿摘要（见 readyPublicView）：恢复态的制品清单与发布版本是
		// 「哪一版没收敛」的精确指纹，不对未认证调用方输出（RV-05b）。
		view := publicReadyView(report)
		if !report.Ready {
			// 就绪失败仍返回结构化摘要，便于探针与运维诊断。
			return JSON(c, fiber.StatusServiceUnavailable, "service.not_ready", view)
		}
		return OK(c, view)
	})
}

// recoveryPublicView 是恢复态 503 允许匿名输出的全部内容：人读三元组。
//
// 这里刻意用「白名单结构体」而不是直接序列化 RecoveryRequirement：以后给
// requirement 加字段（例如新的制品清单）不会顺带扩大匿名可见面。
//
// 注意：这个结构体连同 /ready 的 readyPublicView 一起，把 artifacts 与
// publicationRevision 从**所有匿名出口**移除了（RV-05b）。它们此前在恢复态 /ready
// 里可被公网读到。运维侧取详情的方式见
// knowledge/decisions/2026-11-16-read-path-actor-semantics.md：`sforum extension
// recovery list --json`（只读 durable 行、不启动进程）与启动日志的
// publication_revision/artifacts 计数。
type recoveryPublicView struct {
	Code      string `json:"code"`
	Component string `json:"component"`
	Message   string `json:"message"`
}

// readyPublicView 是匿名 /ready 允许输出的全部内容：探针摘要 + 恢复态三元组。
//
// 为什么不用 omitempty 或给 ReadyReport 打 json:"-"：白名单结构体让「新增字段」
// 默认不可见，而 omitempty 只是把「空值」藏起来——只要字段有值就又漏了。
// components 保留：它是探针的既有契约（依赖名/状态/耗时），扩展贡献的组件名
// 只含公开的 extensionId，不含版本与 digest。
type readyPublicView struct {
	Status     string                   `json:"status"`
	Ready      bool                     `json:"ready"`
	CheckedAt  time.Time                `json:"checkedAt"`
	Components []health.ComponentResult `json:"components"`
	Recovery   *recoveryPublicView      `json:"recovery,omitempty"`
}

func publicReadyView(report health.ReadyReport) readyPublicView {
	components := report.Components
	if components == nil {
		// 与 ReadyReport 序列化时的既有形状保持一致：空数组而不是 null。
		components = []health.ComponentResult{}
	}
	view := readyPublicView{
		Status:     report.Status,
		Ready:      report.Ready,
		CheckedAt:  report.CheckedAt,
		Components: components,
	}
	if report.Recovery.Active() {
		view.Recovery = &recoveryPublicView{
			Code:      report.Recovery.Code,
			Component: report.Recovery.Component,
			Message:   report.Recovery.Message,
		}
	}
	return view
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
			return JSON(c, fiber.StatusServiceUnavailable, "service.recovery_required", recoveryPublicView{
				Code:      requirement.Code,
				Component: requirement.Component,
				Message:   requirement.Message,
			})
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
//
// storage 故障必须在这里先被认出来：csrf 默认把所有未知错误当成校验失败，于是
// 一次 Redis 抖动会以 403 的形式出现——调用方读到的是「你没有权限」，运维读到的是
// 「权限配置坏了」，而真正的答案是「依赖不可用」。写请求据此 503，读请求继续。
func csrfErrorHandler(logger *slog.Logger) fiber.ErrorHandler {
	storage := newStorageFailureReporter(logger)
	return func(c fiber.Ctx, err error) error {
		if cause, ok := storageFailureCause(err); ok {
			storage.warn(c, "api csrf storage unavailable", cause)
			if safeMethodWithoutStorage(c.Method()) {
				// 读请求没有可被伪造的副作用，继续走原链路：探针与页面不该因为
				// CSRF 记账失败而变成错误响应。
				return c.Next()
			}
			return storageUnavailable(c)
		}
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
}

const sforumStartupBanner = `
   _____ ______
  / ___// ____/___  _______  ______ ___
  \__ \/ /_  / __ \/ ___/ / / / _  _  \
 ___/ / __/ / /_/ / /  / /_/ / / / / / /
/____/_/    \____/_/   \__,_/_/ /_/ /_/    SForum API
--------------------------------------------------`
