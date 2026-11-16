package democtl

import (
	"github.com/gofiber/fiber/v3"

	apphttp "example.com/fixture/apps/api/app/Http"
)

type Controller struct{}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Get("/demo/x", h.x)
}

func (h *Controller) x(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	if c.Query("bad") == "1" {
		// 本用例的唯一起因：数字字面量实参，statusesIn 看不见这个 422。
		return apphttp.NewError(422, "demo.invalid")
	}
	return apphttp.OK(c, map[string]any{})
}

// fixture 需要的两个 guard 白名单名字，保证「白名单过期」那条规则不掺进来。
func (h *Controller) requireGuestRead(c fiber.Ctx) error {
	return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
}

func (h *Controller) requireAdmin(c fiber.Ctx) error {
	return fiber.NewError(fiber.StatusForbidden, "permission.denied")
}
