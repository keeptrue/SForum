package democtl

import (
	"github.com/gofiber/fiber/v3"

	apphttp "example.com/fixture/apps/api/app/Http"
)

type Controller struct{}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Get("/demo/secure", h.secure)
	api.All("/demo/:extensionId/*", h.proxy)
}

func (h *Controller) secure(c fiber.Ctx) error {
	if _, err := apphttp.RequireActor(c); err != nil {
		return err
	}
	return nil
}

func (h *Controller) proxy(c fiber.Ctx) error {
	return nil
}
