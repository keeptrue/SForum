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

// target **不调用** h.service.Do：同一张映射器表里虽然有「哨兵 ⇒ 403」，但这条 handler
// 不可能触发它。门必须保持绿 —— 这是「收窄归因」与「映射器全量并入」的分界线。
func (h *Controller) target(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{})
}

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
