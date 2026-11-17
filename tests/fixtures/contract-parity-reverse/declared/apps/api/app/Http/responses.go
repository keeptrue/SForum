package http

import "github.com/gofiber/fiber/v3"

// 只放 fixture 用得到的两个已登记助手，避免「未登记助手」那条规则掺进来。
func OK(c fiber.Ctx, data any) error {
	return c.Status(fiber.StatusOK).JSON(data)
}

func JSON(c fiber.Ctx, status int, messageKey string, data any) error {
	return c.Status(status).JSON(data)
}
