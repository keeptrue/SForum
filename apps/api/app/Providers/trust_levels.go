package providers

import (
	"github.com/gofiber/fiber/v3"

	trustlevelscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/TrustLevels"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// TrustLevelsProvider 装配信任等级 HTTP 面（公开 1 条 + 后台 3 条）。
//
// 权限不在 provider 里判断：`admin.access` / `trust.manage` 由控制器用 actor.Can
// 真正执行（路由目录只是目录，不是运行时守卫）。
type TrustLevelsProvider struct {
	controller *trustlevelscontroller.Controller
}

func NewTrustLevelsProvider(
	optionsService *options.Service,
	resolver *trustlevels.Resolver,
	users identity.ActorStore,
	sessions *authsession.Manager,
) *TrustLevelsProvider {
	return &TrustLevelsProvider{
		controller: trustlevelscontroller.NewController(optionsService, resolver, users, sessions),
	}
}

func (p *TrustLevelsProvider) RegisterRoutes(api fiber.Router) {
	if p == nil || p.controller == nil {
		return
	}
	p.controller.RegisterRoutes(api)
}
