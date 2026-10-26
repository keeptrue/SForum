package providers

import (
	"github.com/gofiber/fiber/v3"

	moderationcontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Moderation"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type ModerationProvider struct {
	controller *moderationcontroller.Controller
}

func NewModerationProvider(store moderation.Store, forumStore forum.Store, users identity.ActorStore, sessions *authsession.Manager) *ModerationProvider {
	validator := moderation.NewForumTargetValidator(forumStore)
	return &ModerationProvider{
		controller: moderationcontroller.NewController(moderation.NewService(store, validator), users, sessions),
	}
}

type ModerationWorkbenchStore interface {
	moderation.Store
	moderation.SettingsStore
	moderation.WorkbenchStore
}

func NewModerationWorkbenchProvider(store ModerationWorkbenchStore, forumStore forum.Store, users identity.ActorStore, sessions *authsession.Manager) *ModerationProvider {
	return NewModerationWorkbenchProviderWithIndexer(store, forumStore, users, sessions, nil)
}

func NewModerationWorkbenchProviderWithIndexer(store ModerationWorkbenchStore, forumStore forum.Store, users identity.ActorStore, sessions *authsession.Manager, indexer moderation.DecisionIndexer, readModels ...moderation.DecisionReadModelInvalidator) *ModerationProvider {
	validator := moderation.NewForumTargetValidator(forumStore)
	service := moderation.NewServiceWithWorkbenchIndexer(store, validator, store, store, indexer)
	if len(readModels) > 0 {
		service.WithDecisionReadModelInvalidator(readModels[0])
	}
	return &ModerationProvider{controller: moderationcontroller.NewController(service, users, sessions)}
}

func (p *ModerationProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
