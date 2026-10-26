package providers

import (
	"github.com/gofiber/fiber/v3"

	entitymetacontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/EntityMeta"
	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type EntityMetaProvider struct {
	controller *entitymetacontroller.Controller
}

func NewEntityMetaProvider(service *entitymeta.Service, users identity.ActorStore, sessions *authsession.Manager) *EntityMetaProvider {
	return &EntityMetaProvider{
		controller: entitymetacontroller.NewController(service, users, sessions),
	}
}

func (p *EntityMetaProvider) RegisterRoutes(api fiber.Router) {
	if p == nil || p.controller == nil {
		return
	}
	p.controller.RegisterRoutes(api)
}
