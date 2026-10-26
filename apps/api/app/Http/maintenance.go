package http

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// maintenanceMiddleware 在站点维护模式下拦截写请求。
// 放行：安全方法、健康检查、登录/注册/session、公开 web-options、管理员会话（后续由路由内权限处理）。
// 这里只做粗粒度写拦截；管理员绕过依赖 cookie session 中的 user 权限在更深层判断成本较高，
// Wave 1 策略：维护开启时所有写操作返回 503，管理员可在后台先关闭维护（后台写路径同样拦截——
// 因此恢复维护需通过 env 或数据库，或后续加 admin.access 旁路）。
// 为避免管理员锁死自己，对 /api/v1/admin/* 与 /api/v1/auth/* 写路径放行。
func maintenanceMiddleware(optionsService *options.Service) fiber.Handler {
	return func(c fiber.Ctx) error {
		if optionsService == nil {
			return c.Next()
		}
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			return c.Next()
		}
		path := c.Path()
		// 认证与后台管理在维护期间仍需可用，否则无法关闭维护模式。
		if strings.HasPrefix(path, "/api/v1/auth/") || strings.HasPrefix(path, "/api/v1/admin/") ||
			path == "/admin" || strings.HasPrefix(path, "/admin/") {
			return c.Next()
		}
		// 入站 webhook 由外部系统回调，维护模式不阻断（F3.3）。
		if strings.HasPrefix(path, "/api/v1/webhooks/inbound/") {
			return c.Next()
		}
		if strings.HasPrefix(path, "/api/v1/web-options") {
			return c.Next()
		}
		policy, err := optionsService.MaintenancePolicy(c.Context())
		if err != nil || !policy.Enabled {
			return c.Next()
		}
		message := strings.TrimSpace(policy.Message)
		if message == "" {
			message = "site.maintenance"
		}
		// 自定义文案时仍用稳定 reason，message 走 envelope（Fiber Error 仅 reason）。
		return fiber.NewError(fiber.StatusServiceUnavailable, "site.maintenance")
	}
}
