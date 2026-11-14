package http

import (
	"errors"
	"net/netip"
	"strings"

	"github.com/gofiber/fiber/v3"

	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const (
	routeIngressManagedKey = "sforum.route_ingress.managed"

	internalRouteProbeHeader       = "X-SForum-Internal-Route-Probe"
	internalRouteProbeMethodHeader = "X-SForum-Internal-Route-Method"
	internalRouteProbeResultHeader = "X-SForum-Internal-Route-Result"
	internalRouteProbeVersion      = "v1"
	internalRouteProbeMatch        = "plugin"
	internalRouteProbeMiss         = "miss"
)

// routeRegistryIngressMiddleware performs planning only. It never loads an
// actor, reads a request body, or invokes plugin code, so Nuxt may safely use
// the probe result to choose the authoritative upstream for an arbitrary path.
func routeRegistryIngressMiddleware(plans routes.PlanResolver) fiber.Handler {
	return func(c fiber.Ctx) error {
		method := c.Method()
		probe := c.Get(internalRouteProbeHeader) == internalRouteProbeVersion
		if probe && !isTrustedInternalRouteProbeCaller(c) {
			// 探针只服务宿主自己的 SSR 反代。对外可用等于把「这条路径归插件」
			// 变成可枚举的响应差异，因此外部调用方连探针入口都不进。
			return fiber.NewError(fiber.StatusForbidden, "route.probe_forbidden")
		}
		if probe {
			if method != fiber.MethodHead {
				return fiber.NewError(fiber.StatusBadRequest, "route.probe_invalid")
			}
			method = strings.ToUpper(strings.TrimSpace(c.Get(internalRouteProbeMethodHeader)))
		}
		if !probe && isAPIRoutePath(c.Path()) {
			// `/api/v1` 原本就由 Go API 权威处理；Dispatcher 会按需建一次
			// plan，不在入口重复解析所有现有 API 请求。
			c.Locals(routeIngressManagedKey, true)
			return c.Next()
		}

		managed, plugin := classifyRouteIngress(c, plans, method)
		if probe {
			c.Set(fiber.HeaderCacheControl, "no-store")
			if plugin {
				c.Set(internalRouteProbeResultHeader, internalRouteProbeMatch)
				return c.SendStatus(fiber.StatusNoContent)
			}
			c.Set(internalRouteProbeResultHeader, internalRouteProbeMiss)
			return c.SendStatus(fiber.StatusNotFound)
		}

		c.Locals(routeIngressManagedKey, managed)
		return c.Next()
	}
}

func classifyRouteIngress(c fiber.Ctx, plans routes.PlanResolver, method string) (managed bool, plugin bool) {
	apiPath := isAPIRoutePath(c.Path())
	if plans == nil {
		return apiPath, false
	}
	plan, err := plans.BuildExecutionPlan(c.Context(), method, c.Path())
	if err != nil {
		if errors.Is(err, routes.ErrRouteNotFound) {
			return apiPath, false
		}
		// Ambiguous, stale, or unavailable selection is still Registry-owned and
		// must fail closed in Dispatcher instead of falling through to Nuxt.
		return true, true
	}
	for _, step := range plan.Chain() {
		if step.Provider.Kind == routes.ProviderPlugin {
			return true, true
		}
	}
	return apiPath, false
}

func routeRegistryManagedOnly(handler fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		managed, _ := c.Locals(routeIngressManagedKey).(bool)
		if !managed {
			return c.Next()
		}
		return handler(c)
	}
}

// forwardedProbeHeaders 是边缘代理注入的客户端来源头。探针必须由内部调用方
// 直连 API；出现这些头说明请求穿过了面向公网的边缘代理。
var forwardedProbeHeaders = []string{
	fiber.HeaderXForwardedFor,
	"X-Real-IP",
	"Forwarded",
	"CF-Connecting-IP",
	"True-Client-IP",
}

// isTrustedInternalRouteProbeCaller 判定探针调用方是否来自宿主内部网络。
//
// 依据是 clientip 解析后的客户端地址（而不是 TCP 对端）：API 在容器网络里，
// 边缘代理同样来自私网，只有按转发链算出的真实客户端地址才能区分
// 「web 容器直连探针」与「公网客户端经代理进来」。任何转发链头的出现都意味着
// 请求穿过了面向公网的边缘代理，直接拒绝。
func isTrustedInternalRouteProbeCaller(c fiber.Ctx) bool {
	for _, name := range forwardedProbeHeaders {
		if strings.TrimSpace(c.Get(name)) != "" {
			return false
		}
	}
	return isInternalRouteProbeAddress(clientip.FromCtx(c))
}

// isInternalRouteProbeAddress 判定地址是否属于宿主内部网络。
func isInternalRouteProbeAddress(value string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	// 0.0.0.0 / :: 不是真实 TCP 对端地址，只出现在进程内传输（Fiber 的测试
	// 连接、unix socket）。真实连接的 RemoteAddr 一定是具体地址，所以放行
	// unspecified 不会给远程调用方开路径；转发头分支已在上面拒绝。
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified()
}

// isAPIRoutePath 判定路径是否落在 Core API 前缀下。
//
// Fiber 的路由匹配不区分大小写（内部 detectionPath 小写化），而 Route
// Registry 的 plan 匹配区分大小写。入口判定必须与路由匹配同一口径：否则
// `/API/V1/...` 会被判成「非 API 路径」，以 managed=false 跳过限流、维护、
// CSRF 与 Dispatcher 之后仍然命中 handler。
func isAPIRoutePath(value string) bool {
	value = strings.ToLower(value)
	return value == "/api/v1" || strings.HasPrefix(value, "/api/v1/")
}
