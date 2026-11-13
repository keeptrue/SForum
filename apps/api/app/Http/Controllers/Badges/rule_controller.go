package badgescontroller

import (
	"encoding/json"
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// 规则引擎的三条只读后台端点。
//
// 三条都是 GET、都要求 badge.view（守卫由生成的 route catalog 声明），且都
// **不碰写路径**：保存规则触发的回扫一律入队，不在 HTTP 内跑全量。

// adminListBadgeConditions 返回条件目录。
//
// **只调 `PublishableDefinitions(factKeys)`，不调 `Definitions()`**：
// 前者是展示面（objectRef 没有候选的条件不出现，M1 的 `fact.count` 就是这种），
// 后者是校验/求值的全集。混用的后果是双向的——展示面用全集会让运营看到空下拉，
// 校验用展示面会让 M3 卸载插件后已存规则保存不了。
func (h *Controller) adminListBadgeConditions(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	catalog, err := h.admin.BadgeConditions(c.Context(), actor)
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	items := make([]conditionDTO, 0, len(catalog.Items))
	for _, definition := range catalog.Items {
		items = append(items, conditionToDTO(definition))
	}
	// `factCandidates` 是 additive 顶层字段：`items` 的形状一个字都没动（M2 已发的
	// 编辑器不会被这个版本打断）。两个面故意分开——`items` 是规则 type 维的目录，
	// 候选是 `fact.count` 的 factKey/variant 维，混进去会让条件名被插件文案覆盖。
	candidates := make([]factCandidateDTO, 0, len(catalog.FactCandidates))
	for _, candidate := range catalog.FactCandidates {
		candidates = append(candidates, toFactCandidateDTO(candidate))
	}
	// `refCandidates` 是第二个 additive 顶层字段：`items` 与 `factCandidates` 的
	// 拼装一个字都没动（M2/M3 已发的编辑器不会被这个版本打断）。它回答的是另一个
	// 维度——`ParamSpec.RefKind` 的取值域（分类/标签/角色/登录方式/徽章），
	// 与 `items`（规则 type 维）、`factCandidates`（factKey/variant 维）并列而不是
	// 互相嵌套：编辑器按 refKind 查同一张表，独立成面才不会被条件名绑死。
	return apphttp.OK(c, map[string]any{
		"items":          items,
		"factCandidates": candidates,
		"refCandidates":  toRefCandidateSetDTO(catalog.RefCandidates),
	})
}

// adminBadgeRuleText 预览一条草稿规则生成的公开说明（只读，无副作用）。
//
// 权限是 `badge.view`，**不要**按 method 一刀切改成 `badge.manage`：本端点不写库、
// 不审计、不入队，而且同一批管理员本来就能从 `GET /admin/badges` 拿到 `rule` 原文，
// 预览没有扩大任何信息面。改成 manage 只会让只读管理员看不到自己有权读的规则。
//
// 句子在本包之外：本 handler 只做「解码 → 调域层 → 编码」，措辞与拼接全部在
// `Badges.BuildRuleRequirementText`（与公开面同一实现）。预览与保存共用一条校验链，
// 非法规则返回与保存相同的 422 错误码。
func (h *Controller) adminBadgeRuleText(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body struct {
		Rule json.RawMessage `json:"rule"`
	}
	if err := c.Bind().Body(&body); err != nil {
		// 与保存路径同一个码：请求体形状错误属于「规则不合法」，不是 500。
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleInvalid)
	}
	text, err := h.admin.PreviewRequirementText(c.Context(), actor, body.Rule)
	if err != nil {
		return mapBadgeError(err)
	}
	// 只读预览：不写库，也不让代理/浏览器缓存把草稿文案当公开内容。
	setAdminNoStore(c)
	return apphttp.OK(c, toRequirementText(text))
}

// adminBadgeRuleHealth 返回失效引用清单。空 items 是 200 正常结果（不是 404）。
func (h *Controller) adminBadgeRuleHealth(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.admin.RuleHealth(c.Context(), actor)
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	if items == nil {
		items = []badges.BadgeRuleHealth{}
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

// adminBadgeRuleImpact 估算一条规则的影响面（语义是「≈」，不是精确值）。
func (h *Controller) adminBadgeRuleImpact(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	badgeID, err := strconv.ParseInt(c.Params("badgeID"), 10, 64)
	if err != nil || badgeID <= 0 {
		return mapBadgeError(badges.ErrBadgeNotFound)
	}
	impact, err := h.admin.EstimateRuleImpact(c.Context(), actor, badgeID)
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, impact)
}
