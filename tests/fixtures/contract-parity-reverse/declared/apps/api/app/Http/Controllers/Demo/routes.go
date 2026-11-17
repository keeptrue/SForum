package democtl

import (
	"github.com/gofiber/fiber/v3"

	apphttp "example.com/fixture/apps/api/app/Http"
)

type Controller struct{}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Get("/demo/target", h.target)
}

// target 只会答 200 / 401：契约里的 418 在实现侧没有任何出口。
func (h *Controller) target(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
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
