package badgescontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// 批量授予的 HTTP 层（2 条：候选搜索 + 批量授予）。
//
// 与 tier_handlers.go 分文件：一个是等级配置，一个是授予动作，两条不同的写路径
// （等级改动影响排序与展示，批量授予影响 user_badges 与 top_badge_id）。

// adminSearchGrantCandidates 是 GET /admin/badges/grant-candidates?q=&badgeId=。
//
// 权限是 badge.view（不是 user.view）：它只回答「谁可以被授予这个徽章」，
// 不暴露邮箱、角色、封禁理由；要求 user.view 会让「能管徽章但不能看用户目录」
// 的运营角色用不了批量授予。这条判断在领域层（AdminService）里，不在 handler。
func (h *Controller) adminSearchGrantCandidates(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.admin.SearchGrantCandidates(c.Context(), actor, badges.GrantCandidateInput{
		Query:   c.Query("q"),
		BadgeID: int64(queryInt(c, "badgeId")),
		Limit:   queryInt(c, "limit"),
	})
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, map[string]any{"items": toGrantCandidates(items)})
}

// adminBatchGrantBadge 是 POST /admin/badges/:badgeID/grants/batch。
//
// 部分成功返回 200：granted / skipped / failed 三个计数就是结果本身。
// 用 4xx 表示「有些人已持有」会让运营无法区分「整批被拒」与「48 成 2 跳过」。
func (h *Controller) adminBatchGrantBadge(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body struct {
		UserIDs []int64 `json:"userIds"`
		Reason  string  `json:"reason"`
	}
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeGrantBatchInvalid)
	}
	result, err := h.admin.BatchGrant(c.Context(), actor, badges.BatchGrantInput{
		BadgeID:   int64(paramInt(c, "badgeID")),
		UserIDs:   body.UserIDs,
		Reason:    body.Reason,
		GrantedBy: actor.ID,
	})
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, toBatchGrantResult(result))
}
