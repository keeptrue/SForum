package democtl

import (
	"github.com/gofiber/fiber/v3"

	apphttp "example.com/fixture/apps/api/app/Http"
)

type Controller struct{}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Get("/demo/secure", h.secure)
	// G3 之后的退化区形态：既不直出状态常量，也不走同包委派 / apphttp.* / 依赖故障出口。
	// 旧的 `apphttp.RequireActor` 单行 handler 已经**不再**退化（F2 跟包识别能看见 401/503），
	// 所以棘轮反例必须换成这种真正没有可识别来源的形状。
	api.Get("/demo/bare", h.bare)
}

func (h *Controller) bare(c fiber.Ctx) error {
	return c.SendString("ok")
}

func (h *Controller) secure(c fiber.Ctx) error {
	if _, err := apphttp.RequireActor(c); err != nil {
		return err
	}
	return nil
}
