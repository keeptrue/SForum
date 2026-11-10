package profilecontroller

import (
	"log/slog"
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 收藏名单 HTTP 层：公开资料页名单（他人只见公开行）与本人名单（含私密行）。
//
// 缓存策略是这里最需要小心的一处：本人视角的响应含私密收藏，绝不能进共享
// 缓存；他人/匿名视角只含公开行，可以短缓存，但必须 Vary: Cookie——否则共享
// 缓存会把「他人视角的公开名单」回给行主本人，让本人看不到自己的私密收藏。

const (
	// publicBookmarkListMaxAge 是他人视角名单的短缓存秒数：收藏变更频繁，
	// 30 秒内的一致性损失可以接受，换来资料页在高频访问下的稳定。
	publicBookmarkListMaxAge = 30
)

func (h *Controller) publicBookmarks(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	// 与 publicProfile 同姿态：会话抖动降级为匿名视角，而不是让公开页 500。
	viewer, err := apphttp.OptionalActor(c, h.sessions, h.users)
	if err != nil {
		slog.Warn("profile: optional actor resolve failed, serving anonymous bookmarks", "error", err)
		viewer = identity.Actor{}
	}
	view, err := h.service.ListProfileBookmarks(c.Context(), c.Params("username"), viewer, profileQueryInt(c, "page"), profileQueryInt(c, "perPage"))
	if err != nil {
		return mapProfileError(err)
	}
	if view.OwnerView {
		setViewerScopedResponse(c)
	} else {
		c.Set(fiber.HeaderCacheControl, "public, max-age="+strconv.Itoa(publicBookmarkListMaxAge))
		c.Vary(fiber.HeaderCookie)
	}
	return apphttp.OK(c, view.Page)
}

func (h *Controller) myBookmarks(c fiber.Ctx) error {
	setViewerScopedResponse(c)
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	page, err := h.service.ListMyBookmarks(c.Context(), actor, profileQueryInt(c, "page"), profileQueryInt(c, "perPage"))
	if err != nil {
		return mapProfileError(err)
	}
	return apphttp.OK(c, page)
}
