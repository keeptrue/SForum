package democtl

import (
	"github.com/gofiber/fiber/v3"

	apphttp "example.com/fixture/apps/api/app/Http"
)

// 本 fixture 钉住 G3 扩出来的三条调用边识别形态与三条精度闸门。
//
// 五条路由各自只测一件事，且**契约声明的就是「真实可达」的那一组**：
//   1. /demo/delegate  F1 同包委派（handler 只有一行 return h.action(...)）
//   2. /demo/apphttp   F2 跟包 apphttp.* + F3 依赖故障 503 出口
//   3. /demo/mapper    映射器识别但不归因（mapDemoError 有 422/409，绝不能算到路由头上）
//   4. /demo/swallowed error 被折成 bool 的调用不产出状态码（viewer 内部有 401/503）
//   5. /demo/pruned    实参 bool 字面量剪枝（decodePruned 的 422 只在「加」侧可达）
//
// 因此：门必须**绿**。任何一条闸门失效都会让多出的码撞上契约 → 红；
// 反过来，把调用边识别整个摘掉，1/2 两条路由会掉进空集 → 空集棘轮红。

type Controller struct{}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Get("/demo/delegate", h.delegate)
	api.Get("/demo/apphttp", h.secure)
	api.Post("/demo/mapper", h.mapped)
	api.Put("/demo/swallowed", h.swallow)
	api.Delete("/demo/pruned", h.pruned)
}

// --- 1. F1 同包委派 -------------------------------------------------------

func (h *Controller) delegate(c fiber.Ctx) error {
	return h.action(c, false)
}

func (h *Controller) action(c fiber.Ctx, admin bool) error {
	if err := h.guard(c); err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{"admin": admin})
}

func (h *Controller) guard(c fiber.Ctx) error {
	return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
}

// fixture 需要的两个 guard 白名单名字，保证「白名单过期」那条规则不掺进来。
func (h *Controller) requireGuestRead(c fiber.Ctx) error {
	return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
}

func (h *Controller) requireAdmin(c fiber.Ctx) error {
	return fiber.NewError(fiber.StatusForbidden, "permission.denied")
}

// --- 2. F2 跟包 apphttp.* + F3 依赖故障 503 -------------------------------

func (h *Controller) secure(c fiber.Ctx) error {
	if _, err := apphttp.RequireActor(c); err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{})
}

// --- 3. 映射器识别但不归因 ------------------------------------------------

func (h *Controller) mapped(c fiber.Ctx) error {
	if err := do(); err != nil {
		return mapDemoError(err)
	}
	return apphttp.OK(c, map[string]any{})
}

func do() error { return nil }

// mapDemoError 故意同时产出 422 与 409：只要门把映射器整段并进路由集合，
// 这条路由就会「多出=409,422」而变红。
func mapDemoError(err error) error {
	if err == nil {
		return nil
	}
	if err.Error() == "invalid" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "demo.invalid")
	}
	return fiber.NewError(fiber.StatusConflict, "demo.conflict")
}

// --- 4. error 被折成 bool：不产出状态码 -----------------------------------

func (h *Controller) swallow(c fiber.Ctx) error {
	viewer, ok := h.viewer(c)
	if !ok {
		return apphttp.OK(c, map[string]any{"anonymous": true})
	}
	return apphttp.OK(c, map[string]any{"viewer": viewer.ID})
}

// viewer 把 RequireActor 的 error 折成 bool：匿名拿 200，永不产出 401/503。
func (h *Controller) viewer(c fiber.Ctx) (apphttp.Actor, bool) {
	actor, err := apphttp.RequireActor(c)
	if err != nil {
		return apphttp.Actor{}, false
	}
	return actor, true
}

// --- 5. 实参 bool 字面量剪枝 ---------------------------------------------

func (h *Controller) pruned(c fiber.Ctx) error {
	return h.applyPruned(c, false)
}

func (h *Controller) applyPruned(c fiber.Ctx, add bool) error {
	input, err := decodePruned(c, add)
	if err != nil {
		return err
	}
	return apphttp.OK(c, input)
}

// decodePruned：`if !add` 之后只服务「加」这一侧，DELETE 走不到 422。
func decodePruned(c fiber.Ctx, add bool) (map[string]any, error) {
	input := map[string]any{}
	if !add {
		return input, nil
	}
	if err := c.Bind().Body(&input); err != nil {
		return input, fiber.NewError(fiber.StatusUnprocessableEntity, "demo.body_invalid")
	}
	return input, nil
}
