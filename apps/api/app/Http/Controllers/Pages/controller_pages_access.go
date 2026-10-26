package pagescontroller

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// Pages 控制器的访问校验：把贡献声明的 access/permission 映射为传输层结果。
// 与传输类型分文件，保持 controller.go 只承担路由与编排。

// enforcePageAccess 严格校验 access；未知值拒绝。
func (h *Controller) enforcePageAccess(c fiber.Ctx, access pages.Access, permissionKey string) error {
	normalized, err := pages.NormalizeAccess(string(access))
	if err != nil {
		// 已注册贡献不应含未知 access；fail closed
		return fiber.NewError(fiber.StatusNotFound, "pages.not_found")
	}
	switch normalized {
	case pages.AccessPublic:
		return nil
	case pages.AccessLogin:
		actor, aerr := h.actor(c)
		if aerr != nil || actor.ID == 0 {
			return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
		}
		return nil
	case pages.AccessGuest:
		// 已登录用户：返回冲突，引导离开 guest 页（不模糊放行）
		if actor, aerr := h.optionalActor(c); aerr == nil && actor.ID > 0 {
			return fiber.NewError(fiber.StatusConflict, "pages.guest_only")
		}
		return nil
	case pages.AccessModeration:
		actor, aerr := h.actor(c)
		if aerr != nil || actor.ID == 0 {
			return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
		}
		if !actor.Can(identity.PermissionModerationReview) && !actor.IsSuperAdmin() {
			return fiber.NewError(fiber.StatusForbidden, "permission.denied")
		}
		return nil
	case pages.AccessPermission:
		actor, aerr := h.actor(c)
		if aerr != nil || actor.ID == 0 {
			return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
		}
		key := strings.TrimSpace(permissionKey)
		if key == "" {
			return fiber.NewError(fiber.StatusForbidden, "permission.denied")
		}
		if !actor.Can(key) && !actor.IsSuperAdmin() {
			return fiber.NewError(fiber.StatusForbidden, "permission.denied")
		}
		return nil
	default:
		return fiber.NewError(fiber.StatusNotFound, "pages.not_found")
	}
}
