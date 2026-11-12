package badgescontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// 回扫进度端点（`GET /admin/badges/backfill-status`）。
//
// 它回答的是一个**运维问题**：「我刚才点的重算，那批复杂规则徽章到底跑到哪了？」
// 重算回执里只有 `queued: N`，而回扫是逐页的异步链；链断在第 N 页时如果不给读面，
// 运营唯一的选择是翻 worker 日志。所以这条端点读的是状态表，不是授予数——
// 用 `user_badges` 反推进度等于「用结果猜过程」，规则不满足时进度还会倒退。
//
// 形状选择「每个徽章最新一次」而不是历史列表：运营关心的是「现在」；历史留在表里，
// 需要时用 SQL 查，不为它开第二条路由。
//
// 空 items 是 200 正常结果（还没人点过重算），不是 404。
func (h *Controller) adminBadgeBackfillStatus(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.admin.BackfillStatus(c.Context(), actor)
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	if items == nil {
		items = []badges.BackfillRunStatus{}
	}
	return apphttp.OK(c, map[string]any{"items": items})
}
