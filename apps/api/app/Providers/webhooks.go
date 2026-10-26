package providers

import (
	"github.com/gofiber/fiber/v3"

	webhookscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Webhooks"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	webhooks "github.com/keeptrue/sforum/apps/api/app/Models/Webhooks"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type WebhooksProvider struct {
	controller *webhookscontroller.Controller
}

func NewWebhooksProvider(service *webhooks.Service, users identity.ActorStore, sessions *authsession.Manager) *WebhooksProvider {
	return &WebhooksProvider{controller: webhookscontroller.NewController(service, users, sessions)}
}

func (p *WebhooksProvider) RegisterRoutes(api fiber.Router) {
	if p == nil || p.controller == nil {
		return
	}
	p.controller.RegisterRoutes(api)
}
