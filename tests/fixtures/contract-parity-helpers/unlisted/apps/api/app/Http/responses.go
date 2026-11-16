package http

import "github.com/gofiber/fiber/v3"

// 已登记：固定 200。
func OK(c fiber.Ctx, data any) error {
	return c.Status(fiber.StatusOK).JSON(data)
}

// 已登记：码由实参给出。
func JSON(c fiber.Ctx, status int, messageKey string, data any) error {
	return c.Status(status).JSON(data)
}

// 未登记：首参是 fiber.Ctx、函数体自己产出 202，但不在 RESPONSE_HELPERS 里。
// 门必须因此变红 —— 不登记就会让调用它的路由状态码集合变空。
func AcceptedResponse(c fiber.Ctx, data any) error {
	return c.Status(fiber.StatusAccepted).JSON(data)
}
