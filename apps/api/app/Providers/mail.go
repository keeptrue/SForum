package providers

import (
	"github.com/gofiber/fiber/v3"
	mailcontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Mail"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

type MailProvider struct{ controller *mailcontroller.Controller }

func NewMailProvider(extensionStore extensions.Store, deliveries notifications.Store, registry *extensionsruntime.MailProviderRegistry, users identity.ActorStore, sessions *authsession.Manager, optionService *options.Service, policy notifications.CorePolicyStore) *MailProvider {
	return &MailProvider{controller: mailcontroller.NewController(extensionStore, deliveries, registry, users, sessions, optionService, policy)}
}
func (p *MailProvider) RegisterRoutes(api fiber.Router) { p.controller.RegisterRoutes(api) }
