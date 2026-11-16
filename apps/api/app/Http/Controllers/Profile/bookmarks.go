package profilecontroller

import (
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
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
	// 与 publicProfile 同姿态、同一套分类：匿名 200 只属于「本来就没凭据」，
	// 依赖故障答 503、主体不可用答 401。旧实现的降级同时也是权限语义问题——
	// 行主本人的私密行会因为一次 Redis 抖动而从「我的名单」里消失。
	viewer, err := apphttp.ReadActor(c, h.sessions, h.users)
	if err != nil {
		return err
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
