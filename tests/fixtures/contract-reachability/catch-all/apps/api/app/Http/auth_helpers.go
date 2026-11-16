package http

import "github.com/gofiber/fiber/v3"

// 精简版 guard 助手：只保留契约可达性门关心的「自己直接产出 401/503」形状。
// 与 app/Http/auth_read.go 同义：C（主体不可用）→401，D/E（依赖故障/未识别）→503。
type Actor struct{ ID int64 }

func RequireActor(c fiber.Ctx) (Actor, error) {
	if c.Get("Authorization") == "" {
		return Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	return Actor{}, fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
}

func ReadActor(c fiber.Ctx) (Actor, error) {
	return Actor{}, nil
}

func LoadActor(c fiber.Ctx) (Actor, error) {
	return Actor{}, nil
}

func OptionalActor(c fiber.Ctx) (Actor, error) {
	return Actor{}, nil
}

func optionalActor(c fiber.Ctx) (Actor, error) {
	return Actor{}, nil
}
