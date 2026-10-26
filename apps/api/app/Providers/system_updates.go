package providers

import (
	"github.com/gofiber/fiber/v3"

	systemupdatescontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/SystemUpdates"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	systemupdates "github.com/keeptrue/sforum/apps/api/app/Models/SystemUpdates"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type SystemUpdatesProvider struct {
	controller *systemupdatescontroller.Controller
}

func NewSystemUpdatesProvider(service *systemupdates.Service, users identity.ActorStore, sessions *authsession.Manager) *SystemUpdatesProvider {
	return &SystemUpdatesProvider{controller: systemupdatescontroller.NewController(service, users, sessions)}
}

func (p *SystemUpdatesProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
