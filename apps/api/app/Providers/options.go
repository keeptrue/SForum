package providers

import (
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	optionscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Options"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type OptionsProvider struct {
	controller *optionscontroller.Controller
}

func NewOptionsProvider(store options.Store, users identity.ActorStore, sessions *session.Store) *OptionsProvider {
	return NewOptionsProviderWithSessions(store, users, authsession.NewManager(sessions, authsession.Config{}))
}

func NewOptionsProviderWithSessions(store options.Store, users identity.ActorStore, sessions *authsession.Manager) *OptionsProvider {
	return NewOptionsProviderWithService(options.NewService(store), users, sessions)
}

func NewOptionsProviderWithService(service *options.Service, users identity.ActorStore, sessions *authsession.Manager) *OptionsProvider {
	return &OptionsProvider{
		controller: optionscontroller.NewController(service, users, sessions),
	}
}

func (p *OptionsProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
