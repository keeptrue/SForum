package badgescontroller

import (
	"context"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/app/Support/Palette"
)

// 徽章等级的 HTTP 层（后台 5 条 + 公开 1 条）。
//
// 放在独立文件而不是继续加宽 controller.go（730 行）：等级表是新的一张表、
// 一套 CRUD 与一条公开读，塞进那个文件只会让「徽章目录」与「等级表」两种语义
// 混在同一个文件里。
//
// 权限仍然不在 handler 里判断：读走 badge.view、写走 badge.manage，由生成的
// core route catalog 声明；领域层（AdminService）再独立校验一次，任何新调用方
// （CLI / job / 插件）都绕不过去。

// tierBody 是等级创建/更新的请求体。
//
// 与 badgeBody 同一套 PATCH 语义：字段全部可选，PATCH 只带要改的字段。
// key 只在创建时有意义（PATCH 不允许改 key：改 key 等于换一个等级）。
type tierBody struct {
	Key      *string `json:"key"`
	NameZh   *string `json:"nameZh"`
	NameEn   *string `json:"nameEn"`
	Icon     *string `json:"icon"`
	Color    *string `json:"color"`
	Position *int    `json:"position"`
	Enabled  *bool   `json:"enabled"`
}

// createInput 组装创建入参：新等级默认启用、默认图标由领域层补。
//
// position 缺省走 TierPositionAppend（追加到末尾）。不能把「没传」当 0：0 是最高位次，
// 新等级会与金牌并列，挂在它上面的徽章会和金牌徽章争同一个名次。
func (b tierBody) createInput() badges.TierInput {
	input := badges.TierInput{Enabled: true, Position: badges.TierPositionAppend}
	if b.Key != nil {
		input.Key = *b.Key
	}
	if b.NameZh != nil {
		input.NameZh = *b.NameZh
	}
	if b.NameEn != nil {
		input.NameEn = *b.NameEn
	}
	if b.Icon != nil {
		input.Icon = *b.Icon
	}
	if b.Color != nil {
		input.Color = *b.Color
	}
	if b.Position != nil {
		input.Position = *b.Position
	}
	if b.Enabled != nil {
		input.Enabled = *b.Enabled
	}
	return input
}

// mergeInto 把 PATCH body 合并到旧值上，产出完整 TierInput。
//
// key 不参与合并（不可变），position 允许合并：后台「上移/下移」按钮走 PATCH，
// 拖拽排序走 PUT /order，两条路径都写同一个 position 列。
func (b tierBody) mergeInto(current badges.TierDef) badges.TierInput {
	input := badges.TierInput{
		Key:      current.Key,
		NameZh:   current.NameZh,
		NameEn:   current.NameEn,
		Icon:     current.Icon,
		Color:    current.Color,
		Position: current.Position,
		Enabled:  current.Enabled,
	}
	if b.NameZh != nil {
		input.NameZh = *b.NameZh
	}
	if b.NameEn != nil {
		input.NameEn = *b.NameEn
	}
	if b.Icon != nil {
		input.Icon = *b.Icon
	}
	if b.Color != nil {
		input.Color = *b.Color
	}
	if b.Position != nil {
		input.Position = *b.Position
	}
	if b.Enabled != nil {
		input.Enabled = *b.Enabled
	}
	return input
}

// adminListBadgeTiers 是 GET /admin/badges/tiers。
//
// colorChoices 与 items 同体下发：色板白名单的唯一来源是 Support/Palette，
// 前端抄一份就会在服务端调整时变成「UI 能选、保存 422」。
func (h *Controller) adminListBadgeTiers(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.admin.ListTiers(c.Context(), actor)
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, toAdminBadgeTiers(items, palette.Tokens()))
}

func (h *Controller) adminCreateBadgeTier(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body tierBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeTierInvalid)
	}
	created, err := h.admin.CreateTier(c.Context(), actor, body.createInput())
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.Created(c, toAdminBadgeTier(created))
}

func (h *Controller) adminUpdateBadgeTier(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body tierBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeTierInvalid)
	}
	key := c.Params("tierKey")
	// 先读旧值再合并：PATCH 只带要改的字段，领域层始终收到完整 input。
	current, err := h.admin.GetTier(c.Context(), actor, key)
	if err != nil {
		return mapBadgeError(err)
	}
	updated, err := h.admin.UpdateTier(c.Context(), actor, key, body.mergeInto(current))
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, toAdminBadgeTier(updated))
}

// adminDeleteBadgeTier 是 DELETE /admin/badges/tiers/:tierKey?migrateTo=key。
//
// migrateTo 是必填的：删除一档必须显式回答「引用它的徽章归到哪一档」。
// 缺失时领域层返回 422（badge_tier_invalid）而不是静默降级——静默把 30 个徽章
// 变铜牌，是运营在确认框里看不到的破坏。
func (h *Controller) adminDeleteBadgeTier(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if err := h.admin.DeleteTier(c.Context(), actor, c.Params("tierKey"), c.Query("migrateTo")); err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, map[string]any{"deleted": true})
}

// adminReorderBadgeTiers 是 PUT /admin/badges/tiers/order。
//
// 只提交 keys 的顺序：服务端按数组下标重写 position，未列出的档位保持相对顺序
// 追加在后（后台可能只提交它看得见的那几档）。
func (h *Controller) adminReorderBadgeTiers(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body struct {
		Keys []string `json:"keys"`
	}
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeTierInvalid)
	}
	if err := h.admin.ReorderTiers(c.Context(), actor, body.Keys); err != nil {
		return mapBadgeError(err)
	}
	// 重排会改变「谁是他最高级徽章」，领域层已全量重算；这里回读一次给前端最新顺序。
	items, err := h.admin.ListTiers(c.Context(), actor)
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, toAdminBadgeTiers(items, palette.Tokens()))
}

// publicBadgeTiers 是 GET /badges/tiers（公开等级目录）。
//
// 它存在的理由：公开徽章目录只给 tier 的 key，前端要渲染角标的中文名/颜色就必须
// 知道等级表。让前端硬编码三档映射，会在运营新增档位后直接失效。
//
// 缓存：public, max-age=60。等级表是低频变更的运营配置，且响应 actor 无关
// （没有 earned、没有 enabled），因此可以进共享缓存——这与 /badges 的
// private,no-store 是两种语义，不要复用那边的 helper。
func (h *Controller) publicBadgeTiers(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	// 公开面只列启用档位：停用档位不参与展示与排序，出现在公开目录里会让前端
	// 渲染出一个「没有徽章会用它」的角标。
	items, err := h.listPublicTiers(c.Context())
	if err != nil {
		return mapBadgeError(err)
	}
	c.Set(fiber.HeaderCacheControl, "public, max-age=60")
	return apphttp.OK(c, map[string]any{"items": toPublicBadgeTiers(items)})
}

// listPublicTiers 过滤出启用档位。
//
// 走 store 的 ListTiers 而不是新加一条「只读启用」的 SQL：等级表是几十行的配置，
// 在 Go 层过滤的代价可以忽略，而少一条 SQL 就少一处可能与后台列表漂移的地方。
func (h *Controller) listPublicTiers(ctx context.Context) ([]badges.TierDef, error) {
	all, err := h.service.ListTiers(ctx)
	if err != nil {
		return nil, err
	}
	enabled := make([]badges.TierDef, 0, len(all))
	for _, tier := range all {
		if tier.Enabled {
			enabled = append(enabled, tier)
		}
	}
	return enabled, nil
}
