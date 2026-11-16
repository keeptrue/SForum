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
