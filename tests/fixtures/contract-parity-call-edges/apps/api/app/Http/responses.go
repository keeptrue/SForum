package http

import "github.com/gofiber/fiber/v3"

// 与真实 app/Http/responses.go 同形的精简版：固定码 + statusArg 两种形态。

type Actor struct{ ID int64 }

type APIError struct {
	Status int
	Reason string
}

func JSON(c fiber.Ctx, status int, messageKey string, data any) error {
	return c.Status(status).JSON(data)
}

func OK(c fiber.Ctx, data any) error {
	return JSON(c, fiber.StatusOK, "ok", data)
}

func Created(c fiber.Ctx, data any) error {
	return JSON(c, fiber.StatusCreated, "ok", data)
}

// #235 取 b：与仓库实现一致（200 + 统一 envelope，data 为 null），
// 否则 [helper-codes] 自证会把这条 fixture 判红。
func NoData(c fiber.Ctx) error {
	return JSON(c, fiber.StatusOK, "ok", nil)
}

func ErrorResponse(c fiber.Ctx, status int, reason string) error {
	return c.Status(status).JSON(APIError{Status: status, Reason: reason})
}

func NewError(status int, reason string) *APIError {
	return &APIError{Status: status, Reason: reason}
}
