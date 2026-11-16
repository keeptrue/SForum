package identitycontroller

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// adminRevokeUserAPITokens 撤销目标用户的全部个人访问令牌（P2-2）。
//
// 权限：user.manage；目标若是 super_admin，则只有 super_admin 能撤销其令牌
// （与 AdminRevokeUserSessions / AdminSetUserPassword 同口径，鉴权在 service 层）。
//
// 与「强制下线全部设备」的区别：那个动作在同一个事务里连 PAT 一起撤销；本端点
// 只清令牌、不动浏览器会话，用于机器客户端密钥疑似泄露时的最小化处置。
func (h *Controller) adminRevokeUserAPITokens(c fiber.Ctx) error {
	actor, err := h.actorWithoutSessionRenewal(c)
	if err != nil {
		return err
	}
	if h.apiTokens == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	targetUserID, err := paramInt64(c, "userID")
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	revoked, err := h.apiTokens.RevokeAllForUser(c.Context(), actor, targetUserID)
	if err != nil {
		return mapAdminAPITokenError(err)
	}
	return apphttp.OK(c, map[string]any{"revoked": revoked})
}

// mapAdminAPITokenError 只覆盖本端点新增的两种结论；其余交给通用映射。
func mapAdminAPITokenError(err error) error {
	switch {
	case errors.Is(err, apitokens.ErrSuperAdminTokenLocked):
		return fiber.NewError(fiber.StatusForbidden, "api_token.super_admin_locked")
	case errors.Is(err, identity.ErrUserNotFound):
		return fiber.NewError(fiber.StatusNotFound, "user.not_found")
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	default:
		return mapIdentityError(err)
	}
}
