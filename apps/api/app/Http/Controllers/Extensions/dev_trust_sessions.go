package extensionscontroller

import (
	"context"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// DevTrustSessionService 是 dev 信任会话的最小读取/写入边界；nil 时路由 fail-closed。
type DevTrustSessionService interface {
	Create(ctx context.Context, actor identity.Actor, input extensions.DevTrustSessionGrantInput) (extensions.DevTrustSession, error)
	List(ctx context.Context, actor identity.Actor, input extensions.DevTrustSessionListInput) ([]extensions.DevTrustSession, error)
	Revoke(ctx context.Context, actor identity.Actor, sessionID string) error
	GrantDevArtifact(ctx context.Context, actor identity.Actor, input extensions.DevTrustGrantInput) error
}

type devTrustSessionCreateInput struct {
	ExtensionID string `json:"extensionId"`
	PackageSlug string `json:"packageSlug"`
	RootPath    string `json:"rootPath"`
	Confirm     string `json:"confirm"`
}

// createDevTrustSession 建立「会话级自动信任」：请求本身必须来自 super_admin 会话
// 并显式携带 confirm=true（这就是每个开发会话的一次人工确认）。
func (h *Controller) createDevTrustSession(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.devTrustSessions == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeDevTrustDisabled)
	}
	var input devTrustSessionCreateInput
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	if input.Confirm != "true" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	session, err := h.devTrustSessions.Create(c.Context(), actor, extensions.DevTrustSessionGrantInput{
		ExtensionID: input.ExtensionID,
		PackageSlug: input.PackageSlug,
		RootPath:    input.RootPath,
	})
	if err != nil {
		return mapDevTrustError(err)
	}
	return apphttp.OK(c, session)
}

// listDevTrustSessions 活跃/历史会话列表（extension.view 或 manage）。
func (h *Controller) listDevTrustSessions(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.devTrustSessions == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeDevTrustDisabled)
	}
	sessions, err := h.devTrustSessions.List(c.Context(), actor, extensions.DevTrustSessionListInput{
		ExtensionID:    strings.TrimSpace(c.Query("extensionId")),
		IncludeRevoked: c.Query("includeRevoked") == "true",
	})
	if err != nil {
		return mapDevTrustError(err)
	}
	return apphttp.OK(c, sessions)
}

// revokeDevTrustSession 一键撤销（创建者本人或 super_admin）。
func (h *Controller) revokeDevTrustSession(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.devTrustSessions == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeDevTrustDisabled)
	}
	if err := h.devTrustSessions.Revoke(c.Context(), actor, c.Params("id")); err != nil {
		return mapDevTrustError(err)
	}
	return apphttp.OK(c, fiber.Map{"revoked": true})
}

// devTrustRefresh 由 CLI watch 循环在构建成功后调用：
// 上传新包（已由 CLI 完成 stage）→ 自动写入 exact live grant → enable。
// 无活跃会话时返回 trust challenge required，与人工路径一致。
func (h *Controller) devTrustRefresh(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.devTrustSessions == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeDevTrustDisabled)
	}
	var input extensions.DevTrustGrantInput
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&input); err != nil {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
		}
	}
	input.ExtensionID = c.Params("id")
	if err := h.devTrustSessions.GrantDevArtifact(c.Context(), actor, input); err != nil {
		return mapDevTrustError(err)
	}
	return apphttp.OK(c, fiber.Map{"devTrusted": true})
}

func mapDevTrustError(err error) error {
	switch {
	case errors.Is(err, extensions.ErrDevTrustDisabled),
		errors.Is(err, extensions.ErrDevTrustRootForbidden):
		return fiber.NewError(fiber.StatusForbidden, extensions.CodeDevTrustDisabled)
	case errors.Is(err, extensions.ErrDevTrustSessionNotFound):
		return fiber.NewError(fiber.StatusNotFound, extensions.CodeDevTrustSessionNotFound)
	case errors.Is(err, extensions.ErrDevTrustSessionExists):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeDevTrustSessionExists)
	case errors.Is(err, extensions.ErrDevTrustSessionExpired):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeDevTrustSessionNotFound)
	case errors.Is(err, extensions.ErrTrustChallengeRequired):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeTrustChallengeRequired)
	default:
		return mapExtensionError(err)
	}
}
