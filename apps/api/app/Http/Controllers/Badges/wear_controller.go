package badgescontroller

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// 自由佩戴（`GET/PUT/DELETE /api/v1/badges/worn`）的 HTTP 层。
//
// 三条端点返回**同一个** wire 形状（`badgeDisplayResponse`，由 `toBadgeDisplay` 从
// 领域 `badges.BadgeDisplay` 投影）：前端只有一种响应形状，佩戴（PUT）与取消佩戴
// （DELETE）不需要各自维护一套「改完之后长什么样」的推导。投影在 HTTP 层完成而不是
// 直接序列化领域结构——领域 `BadgeRef.Appearance` 没有 json tag，原样下发会把等级
// 外观吐成 Go 字段名（`tierAppearance.NameZh`），与站点其它公开徽章响应的
// `tierName{zh,en}` + `tierColor` 不是一套解析。
//
// 这也是为什么 PUT/DELETE 成功后要再读一次 DisplayBadge，而不是回 `{ok:true}`：
// 展示位可能在同一条语句里被回落逻辑改写（例如佩戴的徽章刚好被停用），
// 只有重新读出来的才是真的。
//
// 放在独立文件而不是 controller.go：那边已经是 800+ 行，佩戴与后台 CRUD 没有
// 共享状态，混在一起只会让两个方向都更难读。

// errBadgeWearerMissing 表示装配里没有佩戴能力。
//
// 这是装配错误（NewController 没接上 Service、测试忘了 WithBadgeWearer），
// 上抛成 500：折成 422 会让「接口没装配好」看起来像「用户没资格戴」。
var errBadgeWearerMissing = errors.New("badges: wear endpoints are not wired")

// WithBadgeWearer 注入佩戴能力（测试缝）。
//
// 生产装配不需要调用：NewController 已把 service 接上，Service 再委托给
// PostgresStore 的 CTE 实现。这里只影响这三条端点，不触碰其它徽章端点。
func (h *Controller) WithBadgeWearer(wearer badges.WearStore) *Controller {
	if h != nil {
		h.wear = wearer
	}
	return h
}

// badgeWearer 返回本次请求要用的佩戴实现。
func (h *Controller) badgeWearer() badges.WearStore {
	if h == nil {
		return nil
	}
	if h.wear != nil {
		return h.wear
	}
	// 兜底：测试里直接构造 `&Controller{service: ...}` 时也能工作。
	if h.service != nil {
		return h.service
	}
	return nil
}

// wornBadgeBody 是 PUT 的请求体：slug 与 hidden 二选一，互斥。
//
// 用非指针 string / bool：佩戴没有「部分更新」语义。`hidden: true` 表示「什么都不戴」；
// 两者同填是客户端 bug（不知道自己要哪个），400 forum.badge_worn_body_invalid
// 而不是猜一个执行——猜错的那一半会静默改掉用户的展示位。
// `{"hidden":false}` 与缺字段等价：走 slug 路径，空 slug 由领域层折成 ErrBadgeNotOwned。
type wornBadgeBody struct {
	Slug   string `json:"slug"`
	Hidden bool   `json:"hidden"`
}

// getWornBadge 是 GET /api/v1/badges/worn：读取当前佩戴状态。
func (h *Controller) getWornBadge(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	wearer := h.badgeWearer()
	if wearer == nil {
		return errBadgeWearerMissing
	}
	display, err := wearer.DisplayBadge(c.Context(), actor.ID)
	if err != nil {
		return mapBadgeError(err)
	}
	setViewerScopedNoStore(c)
	return apphttp.OK(c, toBadgeDisplay(display))
}

// setWornBadge 是 PUT /api/v1/badges/worn：佩戴一枚自己已获得的徽章。
func (h *Controller) setWornBadge(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body wornBadgeBody
	if err := c.Bind().Body(&body); err != nil {
		// 400 而不是 422：body 根本不是合法 JSON（或 slug 类型不对）时，
		// 「你没持有这枚徽章」是一句谎话——服务端连 slug 都没读到。
		return fiber.NewError(fiber.StatusBadRequest, badges.CodeBadgeWornBodyInvalid)
	}
	// 互斥在解析成功后、触达领域层前判定：两个动作写同一列的不同状态，
	// 同填意味着调用方自己也不知道想要哪个，执行哪个都是错的。
	if body.Hidden && body.Slug != "" {
		return fiber.NewError(fiber.StatusBadRequest, badges.CodeBadgeWornBodyInvalid)
	}
	wearer := h.badgeWearer()
	if wearer == nil {
		return errBadgeWearerMissing
	}
	if body.Hidden {
		if err := wearer.HideWornBadge(c.Context(), actor.ID); err != nil {
			return mapBadgeError(err)
		}
	} else {
		// 空 slug 不在这里拦：SetWornBadge 归一化后同样返回 ErrBadgeNotOwned，
		// 两处判断迟早分叉，而分叉后的症状是「接口拒绝但错误码说的是另一回事」。
		if err := wearer.SetWornBadge(c.Context(), actor.ID, body.Slug); err != nil {
			return mapBadgeError(err)
		}
	}
	display, err := wearer.DisplayBadge(c.Context(), actor.ID)
	if err != nil {
		return mapBadgeError(err)
	}
	setViewerScopedNoStore(c)
	return apphttp.OK(c, toBadgeDisplay(display))
}

// clearWornBadge 是 DELETE /api/v1/badges/worn：取消佩戴，回到自动推导。
func (h *Controller) clearWornBadge(c fiber.Ctx) error {
	if err := h.requireFeature(c.Context()); err != nil {
		return err
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	wearer := h.badgeWearer()
	if wearer == nil {
		return errBadgeWearerMissing
	}
	if err := wearer.ClearWornBadge(c.Context(), actor.ID); err != nil {
		return mapBadgeError(err)
	}
	display, err := wearer.DisplayBadge(c.Context(), actor.ID)
	if err != nil {
		return mapBadgeError(err)
	}
	setViewerScopedNoStore(c)
	return apphttp.OK(c, toBadgeDisplay(display))
}
