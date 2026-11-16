package http

import "github.com/gofiber/fiber/v3"

// 与真实 app/Http/auth_read.go 同形的精简版：C（主体不可用）→401，D/E（依赖故障）→503。
//
// 503 不在 RequireActor 自己的函数体里，而是由 classifyActorError 构造
// readPathDependencyFailure、再由全局 errorHandler 渲染成 503 —— 这正是 F3 形态要认的东西。

type readPathDependencyFailure struct{ cause error }

func (e *readPathDependencyFailure) Error() string { return "dependency unavailable" }
func (e *readPathDependencyFailure) Unwrap() error { return e.cause }

func optionalActor(c fiber.Ctx) (Actor, error) {
	if c.Get("X-Dependency") == "down" {
		return Actor{}, &readPathDependencyFailure{cause: nil}
	}
	return Actor{}, nil
}

func classifyActorError(err error) error {
	if err == nil {
		return nil
	}
	return &readPathDependencyFailure{cause: err}
}

// RequireActor：无凭据 401；依赖故障经 classifyActorError 走 503 出口。
func RequireActor(c fiber.Ctx) (Actor, error) {
	actor, err := optionalActor(c)
	if err != nil {
		return Actor{}, classifyActorError(err)
	}
	if actor.ID == 0 {
		return Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	return actor, nil
}

func ReadActor(c fiber.Ctx) (Actor, error) {
	return optionalActor(c)
}

func LoadActor(c fiber.Ctx) (Actor, error) {
	return RequireActor(c)
}
