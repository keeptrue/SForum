package http

import "github.com/gofiber/fiber/v3"

// 依赖故障统一出口：503 + Retry-After。
func storageUnavailable(c fiber.Ctx) error {
	c.Set(fiber.HeaderRetryAfter, "1")
	return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]any{"code": 503})
}
