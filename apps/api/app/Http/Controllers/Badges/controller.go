package badgescontroller

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 徽章 HTTP 层：公开 1 条（资料页徽章墙）+ 后台 12 条。
//
// 权限不在 handler 里判断：路由守卫由生成的 core route catalog 声明
// （读 badge.view、写 badge.manage）。这里只负责解码、调用领域、编码。
//
// 两种 403 语义必须分开：
//   - 产品开关关闭 → forum.badges_disabled
//   - 权限不足     → 守卫层拒绝（不是 badges_disabled）
// 混用会让审查无法区分「运营关了功能」与「这个人没权限」。

// BadgeFeatureGate 是 features.badges 的窄查询入口。
type BadgeFeatureGate interface {
	BadgesFeatureEnabled(ctx context.Context) (bool, error)
}

// UserLookup 按用户名解析资料页目标用户。
//
// 只回 userID：徽章墙不需要目标的其它资料，接口越窄越不容易和 Profile 域纠缠。
type UserLookup interface {
	PublicProfileUserID(ctx context.Context, username string) (int64, error)
}

// BadgeCatalogReader 是公开徽章目录的窄读口（*badges.Service 满足它）。
//
// 两个方法刻意分开：PublicCatalog 签名里没有 actor，EarnedBadgeSlugs 是唯一
// 吃 userID 的读。HTTP 层只依赖这两条，测试才能注入「3 条 / 读库失败」这类形状。
type BadgeCatalogReader interface {
	PublicCatalog(ctx context.Context) (badges.PublicCatalog, error)
	EarnedBadgeSlugs(ctx context.Context, userID int64) (map[string]struct{}, error)
}

type Controller struct {
	service  *badges.Service
	catalog  BadgeCatalogReader
	admin    *badges.AdminService
	gate     BadgeFeatureGate
	users    identity.ActorStore
	sessions *authsession.Manager
	lookup   UserLookup
	// loadActor 是可选主体解析的测试缝；生产装配留空，走 apphttp.OptionalActor。
	loadActor func(c fiber.Ctx) (identity.Actor, error)
}

func NewController(service *badges.Service, admin *badges.AdminService, gate BadgeFeatureGate, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, catalog: service, admin: admin, gate: gate, users: users, sessions: sessions}
}

// WithActorLoader 注入可选主体解析器（测试用）。只影响公开读的 viewer 判定，
// 后台端点仍走 authsession 的强制解析。
func (h *Controller) WithActorLoader(loader func(c fiber.Ctx) (identity.Actor, error)) *Controller {
	if h != nil {
		h.loadActor = loader
	}
	return h
}

// WithUserLookup 注入用户名解析器（资料页需要 username → userID）。
func (h *Controller) WithUserLookup(lookup UserLookup) *Controller {
	if h != nil {
		h.lookup = lookup
	}
	return h
}

// RegisterRoutes 注册徽章路由。
//
// 函数名即路由 ID 的来源（生成器从 handler 名反推 core.route.badges.<snake>），
// 所以名字必须与 contracts/openapi/paths/badges.yaml 的 operationId 一致。
func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Get("/badges", h.publicCatalog)
	api.Get("/profiles/:username/badges", h.publicProfileBadges)

	// 公开等级目录：前端渲染 tier 角标的中文名/颜色要靠它，不能让前端硬编码三档。
	api.Get("/badges/tiers", h.publicBadgeTiers)

	admin := api.Group("/admin/badges")
	// 静态段必须先于 /:badgeID 注册：`DELETE /admin/badges/tiers` 与
	// `DELETE /admin/badges/:badgeID` 形状相同，后注册的会被先注册的吃掉
	// （Fiber 按注册顺序匹配，params 段会把 "tiers" 当 badgeID 解析成 0）。
	admin.Get("/tiers", h.adminListBadgeTiers)
	admin.Post("/tiers", h.adminCreateBadgeTier)
	admin.Put("/tiers/order", h.adminReorderBadgeTiers)
	admin.Patch("/tiers/:tierKey", h.adminUpdateBadgeTier)
	admin.Delete("/tiers/:tierKey", h.adminDeleteBadgeTier)
	admin.Get("/grant-candidates", h.adminSearchGrantCandidates)
	// 规则引擎的两条静态段：与 /:badgeID 同形，必须在这里注册。
	// 顺序错了 `conditions` 会被当成 badgeID 解析成 0（报 invalid id / 500）。
	admin.Get("/conditions", h.adminListBadgeConditions)
	admin.Get("/rule-health", h.adminBadgeRuleHealth)
	// 回扫进度同样是静态段：与 /:badgeID 系列同形，必须排在任何含参数段的 GET 之前。
	admin.Get("/backfill-status", h.adminBadgeBackfillStatus)
	admin.Get("/", h.adminListBadges)
	admin.Post("/", h.adminCreateBadge)
	admin.Patch("/:badgeID", h.adminUpdateBadge)
	admin.Delete("/:badgeID", h.adminDeleteBadge)
	admin.Get("/groups", h.adminListBadgeGroups)
	admin.Post("/groups", h.adminCreateBadgeGroup)
	admin.Patch("/groups/:groupID", h.adminUpdateBadgeGroup)
	admin.Delete("/groups/:groupID", h.adminDeleteBadgeGroup)
	admin.Get("/:badgeID/rule-impact", h.adminBadgeRuleImpact)
	admin.Get("/:badgeID/grants", h.adminListBadgeGrants)
	admin.Post("/:badgeID/grants", h.adminCreateBadgeGrant)
	admin.Post("/:badgeID/grants/batch", h.adminBatchGrantBadge)
	admin.Delete("/:badgeID/grants/:userID", h.adminDeleteBadgeGrant)
	admin.Post("/recompute", h.adminRecomputeBadges)
}

// requireFeature 在产品开关关闭时返回 403 forum.badges_disabled。
//
// 刻意只作用于徽章端点：资料页的获赞数与访问天数是统计，不是徽章，
// 关闭徽章不该让它们消失（计划 D1）。
func (h *Controller) requireFeature(ctx context.Context) error {
	if h == nil || h.gate == nil {
		return nil
	}
	enabled, err := h.gate.BadgesFeatureEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return fiber.NewError(fiber.StatusForbidden, badges.CodeBadgesDisabled)
	}
	return nil
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	// loadActor 是测试缝（生产装配留空）：后台端点要能验证「未登录 401 / 缺权限 403 /
	// 有权限 200 + no-store」这三条路径，而真实会话需要 Redis。缝只影响 actor 解析，
	// 权限判定仍在域层（`requireBadgePermission`）与路由守卫里，不放宽任何边界。
	if h.loadActor != nil {
		actor, err := h.loadActor(c)
		if err != nil {
			return identity.Actor{}, err
		}
		if actor.ID <= 0 {
			return identity.Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
		}
		return actor, nil
	}
	actor, err := apphttp.OptionalActor(c, h.sessions, h.users)
	if err != nil {
		return identity.Actor{}, err
	}
	if actor.ID <= 0 {
		return identity.Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	return actor, nil
}

// publicProfileBadges 是资料页徽章墙（GET /profiles/:username/badges）。
func (h *Controller) publicProfileBadges(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	username := c.Params("username")
	userID, err := h.resolveProfileUser(c.Context(), username)
	if err != nil {
		return err
	}
	sections, err := h.service.ListUserBadges(c.Context(), userID)
	if err != nil {
		return mapBadgeError(err)
	}
	// 徽章对所有人相同（actor 无关），可被共享缓存；资料页私有数据不在这个响应里。
	setPublicCacheable(c)
	return apphttp.OK(c, toProfileBadges(sections))
}

// publicCatalog 是公开徽章目录（GET /badges）。
//
// 与资料页徽章墙的语义差异是本函数存在的理由：资料页回答「他有什么」，
// 目录回答「站内有什么、我拿到了哪些」——所以这里返回**全部启用徽章**，
// 用 earned 标记查看者自己的获得情况。
//
// 也正因为响应含查看者的 earned，缓存头必须是 private（见 setViewerScopedNoStore）。
func (h *Controller) publicCatalog(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	reader := h.catalog
	if reader == nil {
		reader = h.service
	}
	catalog, err := reader.PublicCatalog(c.Context())
	if err != nil {
		// 读库失败必须上抛（500）。当成空目录返回等于告诉运营「站内没有徽章」，
		// 而那正是公开面最不该撒的谎。
		return mapBadgeError(err)
	}
	var earned map[string]struct{}
	if actor, ok := h.viewer(c); ok {
		earned, err = reader.EarnedBadgeSlugs(c.Context(), actor.ID)
		if err != nil {
			return mapBadgeError(err)
		}
	}
	setViewerScopedNoStore(c)
	return apphttp.OK(c, toPublicCatalog(catalog, earned))
}

// viewer 解析可选登录主体。匿名、会话失效、坏 token 一律按匿名处理：
// 公开读不该因为一个过期 cookie 变成 401（与 TrustLevels 公开面同口径）。
func (h *Controller) viewer(c fiber.Ctx) (identity.Actor, bool) {
	load := h.loadActor
	if load == nil {
		load = func(c fiber.Ctx) (identity.Actor, error) {
			return apphttp.OptionalActor(c, h.sessions, h.users)
		}
	}
	actor, err := load(c)
	if err != nil || actor.ID <= 0 {
		return identity.Actor{}, false
	}
	return actor, true
}

func (h *Controller) resolveProfileUser(ctx context.Context, username string) (int64, error) {
	if h.lookup == nil {
		return 0, fiber.NewError(fiber.StatusNotFound, badges.CodeBadgeNotFound)
	}
	userID, err := h.lookup.PublicProfileUserID(ctx, username)
	if err != nil || userID <= 0 {
		return 0, fiber.NewError(fiber.StatusNotFound, badges.CodeBadgeNotFound)
	}
	return userID, nil
}

func (h *Controller) adminListBadges(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.admin.ListBadges(c.Context(), actor)
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, map[string]any{"items": toAdminBadges(items)})
}

// patchField 区分「字段缺失」与「显式 null」。
//
// PATCH 的语义要求这个区分：`{"metric":null}` 是「清空」，字段根本不出现是
// 「不修改」。用 *T 只能表达前者，所以这里自己解一层。
type patchField[T any] struct {
	Set   bool
	Value *T
}

func (f *patchField[T]) UnmarshalJSON(data []byte) error {
	f.Set = true
	if string(data) == "null" {
		f.Value = nil
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}

// badgeBody 是创建/更新共用的请求体。
//
// 全部字段可选：PATCH 只带要改的字段（后台「停用」开关只发 {"enabled":false}）。
// 旧版本这里是非指针 string，导致 PATCH 空 body 被当成「slug 为空」→ 422，
// 迫使前端每次改一个字段都先 GET 再全量提交——两次请求之间的并发修改会被静默覆盖。
type badgeBody struct {
	Slug        *string            `json:"slug"`
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	Tier        *string            `json:"tier"`
	Icon        *string            `json:"icon"`
	GrantMode   *string            `json:"grantMode"`
	Position    *int               `json:"position"`
	Enabled     *bool              `json:"enabled"`
	GroupID     patchField[int64]  `json:"groupId"`
	Metric      patchField[string] `json:"metric"`
	Threshold   patchField[int64]  `json:"threshold"`
	// Rule 三态：缺省 = 不改 / null = 清空 / 对象 = 替换。
	// 用既有的 patchField（Set + *Value）而不是自造 `*json.RawMessage` 判空：
	// 后者会把「缺省」和「显式 null」写成同一个分支，症状是「只改名字」把规则清空。
	Rule patchField[json.RawMessage] `json:"rule"`
	// RequirementSummary 是**整体替换**：字段存在就两种语言都写（缺哪个当空串），
	// 缺省则两个都不动。
	RequirementSummary patchField[requirementSummaryBody] `json:"requirementSummary"`
}

// requirementSummaryBody 是运营填的双语要求。
type requirementSummaryBody struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// createInput 组装创建入参：创建时缺省字段用推荐默认值。
func (b badgeBody) createInput() badges.BadgeInput {
	input := badges.BadgeInput{Enabled: true, Icon: "i-lucide-award"}
	if b.Rule.Set && b.Rule.Value != nil {
		input.RuleSet = true
		input.RuleRaw = append(json.RawMessage(nil), (*b.Rule.Value)...)
	}
	if b.RequirementSummary.Set && b.RequirementSummary.Value != nil {
		input.RequirementSummarySet = true
		input.RequirementSummaryZh = b.RequirementSummary.Value.Zh
		input.RequirementSummaryEn = b.RequirementSummary.Value.En
	}
	if b.Slug != nil {
		input.Slug = *b.Slug
	}
	if b.Name != nil {
		input.Name = *b.Name
	}
	if b.Description != nil {
		input.Description = *b.Description
	}
	if b.Tier != nil {
		input.Tier = badges.BadgeTier(*b.Tier)
	}
	if b.Icon != nil {
		input.Icon = *b.Icon
	}
	if b.GrantMode != nil {
		input.GrantMode = badges.GrantMode(*b.GrantMode)
	}
	if b.Position != nil {
		input.Position = *b.Position
	}
	if b.Enabled != nil {
		input.Enabled = *b.Enabled
	}
	input.GroupID = b.GroupID.Value
	if b.Metric.Set {
		input.Metric = metricValue(b.Metric.Value)
	}
	input.Threshold = b.Threshold.Value
	return input
}

// mergeInto 把 PATCH body 合并到旧值上，产出完整 BadgeInput。
//
// grantMode 从 automatic 切到 manual 时**强制清空 metric/threshold**：
// 切到手动意味着原规则作废，保留旧阈值会撞 validateBadgeInput 的
// 「manual badge must not carry metric or threshold」。切回 automatic 需要重填规则。
func (b badgeBody) mergeInto(current badges.Badge) badges.BadgeInput {
	input := badges.BadgeInput{
		Slug:        current.Slug,
		Name:        current.Name,
		Description: current.Description,
		GroupID:     current.GroupID,
		Tier:        current.Tier,
		Icon:        current.Icon,
		GrantMode:   current.GrantMode,
		Metric:      current.Metric,
		Threshold:   current.Threshold,
		Enabled:     current.Enabled,
		Position:    current.Position,
	}
	if b.Slug != nil {
		input.Slug = *b.Slug
	}
	if b.Name != nil {
		input.Name = *b.Name
	}
	if b.Description != nil {
		input.Description = *b.Description
	}
	if b.Tier != nil {
		input.Tier = badges.BadgeTier(*b.Tier)
	}
	if b.Icon != nil {
		input.Icon = *b.Icon
	}
	if b.GrantMode != nil {
		input.GrantMode = badges.GrantMode(*b.GrantMode)
	}
	if b.Position != nil {
		input.Position = *b.Position
	}
	if b.Enabled != nil {
		input.Enabled = *b.Enabled
	}
	if b.GroupID.Set {
		input.GroupID = b.GroupID.Value
	}
	if b.Metric.Set {
		input.Metric = metricValue(b.Metric.Value)
	}
	if b.Rule.Set {
		input.RuleSet = true
		if b.Rule.Value != nil {
			input.RuleRaw = append(json.RawMessage(nil), (*b.Rule.Value)...)
		}
	}
	if b.RequirementSummary.Set && b.RequirementSummary.Value != nil {
		input.RequirementSummarySet = true
		input.RequirementSummaryZh = b.RequirementSummary.Value.Zh
		input.RequirementSummaryEn = b.RequirementSummary.Value.En
	}
	if b.Threshold.Set {
		input.Threshold = b.Threshold.Value
	}
	if input.GrantMode == badges.GrantManual {
		input.Metric = nil
		input.Threshold = nil
	}
	return input
}

func metricValue(raw *string) *badges.ActivityMetric {
	if raw == nil {
		return nil
	}
	value := badges.ActivityMetric(*raw)
	return &value
}

func (h *Controller) adminCreateBadge(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body badgeBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleInvalid)
	}
	created, err := h.admin.CreateBadge(c.Context(), actor, body.createInput())
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.Created(c, toAdminBadge(created))
}

func (h *Controller) adminUpdateBadge(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body badgeBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleInvalid)
	}
	badgeID := int64(paramInt(c, "badgeID"))
	// 先读旧值再合并：PATCH 只带要改的字段，领域层始终收到完整 input。
	current, err := h.admin.GetBadge(c.Context(), actor, badgeID)
	if err != nil {
		return mapBadgeError(err)
	}
	updated, err := h.admin.UpdateBadge(c.Context(), actor, badgeID, body.mergeInto(current))
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, toAdminBadge(updated))
}

func (h *Controller) adminDeleteBadge(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if err := h.admin.DeleteBadge(c.Context(), actor, int64(paramInt(c, "badgeID"))); err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, map[string]any{"deleted": true})
}

func (h *Controller) adminListBadgeGroups(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.admin.ListGroups(c.Context(), actor)
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, map[string]any{"items": toAdminBadgeGroups(items)})
}

// badgeGroupBody 是分组创建/更新的请求体。
//
// 与 badgeBody 同一套 PATCH 语义：字段全部可选，只改带上的字段。
// 分组不需要 patchField（没有「显式 null 清空」的需求——description: "" 就是清空）。
// 两个 PATCH 语义必须一致，否则「改 position 顺手把描述清空」这种 bug 迟早出现。
type badgeGroupBody struct {
	Key         *string `json:"key"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Position    *int    `json:"position"`
}

func (b badgeGroupBody) createInput() badges.BadgeGroupInput {
	input := badges.BadgeGroupInput{}
	if b.Key != nil {
		input.Key = *b.Key
	}
	if b.Name != nil {
		input.Name = *b.Name
	}
	if b.Description != nil {
		input.Description = *b.Description
	}
	if b.Position != nil {
		input.Position = *b.Position
	}
	return input
}

func (b badgeGroupBody) mergeInto(current badges.BadgeGroup) badges.BadgeGroupInput {
	input := badges.BadgeGroupInput{
		Key:         current.Key,
		Name:        current.Name,
		Description: current.Description,
		Position:    current.Position,
	}
	if b.Key != nil {
		input.Key = *b.Key
	}
	if b.Name != nil {
		input.Name = *b.Name
	}
	if b.Description != nil {
		input.Description = *b.Description
	}
	if b.Position != nil {
		input.Position = *b.Position
	}
	return input
}

func (h *Controller) adminCreateBadgeGroup(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body badgeGroupBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleInvalid)
	}
	created, err := h.admin.CreateGroup(c.Context(), actor, body.createInput())
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.Created(c, toAdminBadgeGroup(created))
}

func (h *Controller) adminUpdateBadgeGroup(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body badgeGroupBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleInvalid)
	}
	groupID := int64(paramInt(c, "groupID"))
	// 与徽章 PATCH 一致：先读旧值再合并，只改带上的字段。
	current, err := h.findGroup(c.Context(), actor, groupID)
	if err != nil {
		return mapBadgeError(err)
	}
	updated, err := h.admin.UpdateGroup(c.Context(), actor, groupID, body.mergeInto(current))
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, toAdminBadgeGroup(updated))
}

func (h *Controller) adminDeleteBadgeGroup(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if err := h.admin.DeleteGroup(c.Context(), actor, int64(paramInt(c, "groupID"))); err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, map[string]any{"deleted": true})
}

func (h *Controller) adminListBadgeGrants(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	list, err := h.admin.ListGrants(c.Context(), actor, badges.GrantListInput{
		BadgeID: int64(paramInt(c, "badgeID")),
		Limit:   queryInt(c, "perPage"),
		Offset:  queryInt(c, "offset"),
	})
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, map[string]any{"items": list.Items, "total": list.Total})
}

type badgeGrantBody struct {
	UserID   int64  `json:"userId"`
	Reason   string `json:"reason"`
	Username string `json:"username"`
}

func (h *Controller) adminCreateBadgeGrant(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body badgeGrantBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleInvalid)
	}
	userID := body.UserID
	if userID <= 0 && body.Username != "" && h.lookup != nil {
		targetID, lookupErr := h.lookup.PublicProfileUserID(c.Context(), body.Username)
		if lookupErr != nil || targetID <= 0 {
			return fiber.NewError(fiber.StatusNotFound, badges.CodeBadgeNotFound)
		}
		userID = targetID
	}
	if err := h.admin.Grant(c.Context(), actor, badges.GrantInput{
		UserID: userID, BadgeID: int64(paramInt(c, "badgeID")),
		GrantedBy: actor.ID, GrantReason: body.Reason,
	}); err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	return apphttp.OK(c, map[string]any{"granted": true})
}

func (h *Controller) adminDeleteBadgeGrant(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	revoked, err := h.admin.Revoke(c.Context(), actor, int64(paramInt(c, "userID")), int64(paramInt(c, "badgeID")))
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	// 透传「是否真的删到行」：收回一个不存在的授予时是 false（仍是 200，保持幂等）。
	// 响应体不能报假——前端拿到 true 会显示「已收回」，而徽章可能本来就没授予过。
	return apphttp.OK(c, map[string]any{"revoked": revoked})
}

func (h *Controller) adminRecomputeBadges(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	result, err := h.admin.Recompute(c.Context(), actor)
	if err != nil {
		return mapBadgeError(err)
	}
	setAdminNoStore(c)
	// 三个计数器分开返回：granted=本次补的授予，refreshed=top_badge_id 变化的人数，
	// queued=转入异步评估的徽章数。映射抽在 dto.go，便于单测与契约比对。
	return apphttp.OK(c, toRecomputeResponse(result))
}

// findGroup 从分组列表里取当前值（分组只有几十行，不值得为它加一条 store 接口）。
func (h *Controller) findGroup(ctx context.Context, actor identity.Actor, groupID int64) (badges.BadgeGroup, error) {
	groups, err := h.admin.ListGroups(ctx, actor)
	if err != nil {
		return badges.BadgeGroup{}, err
	}
	for _, group := range groups {
		if group.ID == groupID {
			return group, nil
		}
	}
	return badges.BadgeGroup{}, badges.ErrBadgeNotFound
}

// mapBadgeError 把领域错误映射成 HTTP 状态 + 稳定错误码。
//
// 状态码选择：disabled=403（功能关闭，不是权限）、not_found=404、
// slug_taken=409（冲突）、rule_invalid/manual_only=422（请求语义不合法）、
// already_granted=409（幂等冲突，不是校验失败）。
func mapBadgeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, badges.ErrBadgesDisabled):
		return fiber.NewError(fiber.StatusForbidden, badges.CodeBadgesDisabled)
	case errors.Is(err, badges.ErrBadgeNotFound):
		return fiber.NewError(fiber.StatusNotFound, badges.CodeBadgeNotFound)
	case errors.Is(err, badges.ErrBadgeSlugTaken):
		return fiber.NewError(fiber.StatusConflict, badges.CodeBadgeSlugTaken)
	case errors.Is(err, badges.ErrBadgeAlreadyGranted):
		return fiber.NewError(fiber.StatusConflict, badges.CodeBadgeAlreadyGranted)
	case errors.Is(err, badges.ErrBadgeRuleInvalid):
		// 规则引擎的错误按**引擎错误码**细分：前端有
		// `admin.badges.rule.errors.<code>` 文案，笼统返回 badge_rule_invalid
		// 会让「层级太深」与「条件未知」显示成同一句话。
		switch ruleengine.CodeOf(err) {
		case ruleengine.CodeUnknownCondition:
			return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleUnknownCond)
		case ruleengine.CodeTooDeep, ruleengine.CodeTooManyNodes, ruleengine.CodeTooManyLeaves, ruleengine.CodeValueOutOfRange:
			return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleTooComplex)
		default:
			return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleInvalid)
		}
	case errors.Is(err, badges.ErrBadgeRuleReferenceInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleReferenceInvalid)
	case errors.Is(err, badges.ErrBadgeRuleCycle):
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleCycle)
	case errors.Is(err, badges.ErrBadgeFactWriteDenied):
		return fiber.NewError(fiber.StatusForbidden, badges.CodeBadgeFactWriteDenied)
	case errors.Is(err, badges.ErrBadgeBackfillUnavailable):
		// 422 而不是 503：这是「请求当前无法完成，需要运维接入回扫入口」，
		// 属于业务前置条件缺失，重试不会变好。
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeBackfillUnavailable)
	case errors.Is(err, badges.ErrBadgeManualOnly):
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeManualOnly)
	case errors.Is(err, badges.ErrBadgeTierNotFound):
		return fiber.NewError(fiber.StatusNotFound, badges.CodeBadgeTierNotFound)
	case errors.Is(err, badges.ErrBadgeTierKeyTaken):
		return fiber.NewError(fiber.StatusConflict, badges.CodeBadgeTierKeyTaken)
	case errors.Is(err, badges.ErrBadgeTierInUse):
		return fiber.NewError(fiber.StatusConflict, badges.CodeBadgeTierInUse)
	case errors.Is(err, badges.ErrBadgeTierInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeTierInvalid)
	case errors.Is(err, badges.ErrBadgeGrantBatchInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, badges.CodeBadgeGrantBatchInvalid)
	default:
		return err
	}
}

func paramInt(c fiber.Ctx, name string) int {
	value, err := strconv.Atoi(c.Params(name))
	if err != nil {
		return 0
	}
	return value
}

func queryInt(c fiber.Ctx, name string) int {
	value, err := strconv.Atoi(c.Query(name))
	if err != nil {
		return 0
	}
	return value
}

// setPublicCacheable：徽章墙 actor 无关，可被共享缓存；只对公开端点生效。
//
// 只对**被查看者**的徽章成立（响应里没有查看者字段）。含查看者 earned 的
// /badges 必须用 setViewerScopedNoStore，不能复用本函数——见那边的注释。
func setPublicCacheable(c fiber.Ctx) {
	c.Set(fiber.HeaderCacheControl, "public, max-age=30")
}

// setViewerScopedNoStore：响应含查看者自己的 earned，同一个 URL 对不同人不同体。
//
// 这与 setPublicCacheable 的 "public, max-age=30" 是互斥的语义：public 是写给
// 共享缓存（CDN / 反向代理）看的，一旦用上，A 的 earned 会被投给 B。
// 本控制器也没有 Vary 兜底（RegisterRoutes 不设 Vary），所以不能靠它补救。
// private + no-store 的取舍：目录是低频读，宁可每次回源，也不赌缓存键正确。
func setViewerScopedNoStore(c fiber.Ctx) {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
}

// setAdminNoStore：后台端点含运营状态与审计字段，禁止任何缓存。
func setAdminNoStore(c fiber.Ctx) {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
}
