package providers

import (
	"github.com/gofiber/fiber/v3"
	notificationscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Notifications"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type NotificationsProvider struct {
	controller *notificationscontroller.Controller
}

func NewNotificationsProvider(store *notifications.PostgresStore, users identity.ActorStore, sessions *authsession.Manager, auditor audit.Writer) *NotificationsProvider {
	return &NotificationsProvider{controller: notificationscontroller.NewController(store, sessions, users, store).WithAuditor(auditor)}
}
func (p *NotificationsProvider) WithTargetVisibility(resolver notifications.TargetVisibilityResolver) *NotificationsProvider {
	if p != nil {
		p.controller.WithTargetVisibility(resolver)
	}
	return p
}
func (p *NotificationsProvider) WithTargetPreview(resolver notifications.TargetPreviewResolver) *NotificationsProvider {
	if p != nil {
		p.controller.WithTargetPreview(resolver)
	}
	return p
}
func (p *NotificationsProvider) WithChannels(runtime notificationscontroller.ChannelRuntime, auditor audit.IDWriter, outbox *notifications.Outbox) *NotificationsProvider {
	if p != nil {
		p.controller.WithChannels(runtime, auditor, outbox)
	}
	return p
}
func (p *NotificationsProvider) RegisterRoutes(api fiber.Router) { p.controller.RegisterRoutes(api) }
