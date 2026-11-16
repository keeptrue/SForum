package extensionscontroller

import (
	"github.com/gofiber/fiber/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func (h *Controller) proxyExtensionRoute(c fiber.Ctx) error {
	exact, err := h.publicFrontendBridgeIdentity(c)
	if err != nil {
		return mapExtensionError(err)
	}
	routePath := "/" + c.Params("*")
	matched, err := h.service.MatchRoute(c.Context(), c.Params("extensionId"), c.Method(), routePath)
	if err != nil {
		return mapExtensionError(err)
	}
	if exact != nil && (matched.Extension.ID != exact.ExtensionID ||
		matched.Extension.Version != exact.ExtensionVersion ||
		matched.Extension.PackageDigest != exact.PackageDigest) {
		return mapExtensionError(ErrPublicFrontendBridgeStale)
	}
	actor, hasActor, err := h.optionalActor(c)
	if err != nil {
		return err
	}
	// 授权结论只从 DeriveRouteAccess 取：这个前缀只服务 core guard 路由。
	// custom / raw_request / inherit 守卫路由在这里 fail-closed 成 404（不是 403，
	// 403 会泄露路由存在性），必须走可信 Route Registry —— 否则任何已登录用户都能
	// 经这个前缀到达那些本应由插件守卫代码裁决的处理器（P1-2）。
	access, permission, proxyable := extensionmanifest.DeriveRouteAccess(matched.Route)
	if !proxyable {
		return mapExtensionError(extensions.ErrRouteNotFound)
	}
	switch access {
	case extensions.RouteAccessLogin:
		if !hasActor {
			return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
		}
	case extensions.RouteAccessPermission:
		if !hasActor {
			return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
		}
		if !actor.Can(permission) {
			return fiber.NewError(fiber.StatusForbidden, "permission.denied")
		}
	}
	if h.gateway == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeRuntimeUnavailable)
	}
	if err := h.gateway.Proxy(c, ProxyInput{
		Matched: matched, Actor: actor, HasActor: hasActor, PublicFrontendExact: exact,
	}); err != nil {
		return mapExtensionError(err)
	}
	return nil
}
