package http

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"

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

func isAPIRoutePath(value string) bool {
	return value == "/api/v1" || strings.HasPrefix(value, "/api/v1/")
}
