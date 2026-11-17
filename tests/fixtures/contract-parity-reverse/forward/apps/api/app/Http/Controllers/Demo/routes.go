package democtl

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "example.com/fixture/apps/api/app/Http"
	demo "example.com/fixture/apps/api/app/Models/Demo"
)

type Controller struct {
	service *demo.Service
}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Get("/demo/target", h.target)
}

func (h *Controller) target(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	if _, err := h.service.Do(c.Context()); err != nil {
		return mapDemoError(err)
	}
	return apphttp.OK(c, map[string]any{})
}

// mapDemoError 是映射器：哨兵 ErrDenied ⇒ 403。门按「handler 的 service 方法直接 return
// 的哨兵」收窄归因，所以这个 403 会被算到 target 这条路由上，而契约没声明它。
func mapDemoError(err error) error {
	if errors.Is(err, demo.ErrDenied) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return err
}

// fixture 需要的两个 guard 白名单名字，保证「白名单过期」那条规则不掺进来。
func (h *Controller) requireGuestRead(c fiber.Ctx) error {
	return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
}

func (h *Controller) requireAdmin(c fiber.Ctx) error {
	return fiber.NewError(fiber.StatusForbidden, "permission.denied")
}
