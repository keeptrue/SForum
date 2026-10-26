package providers

import (
	"github.com/gofiber/fiber/v3"

	databasecontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Database"
	database "github.com/keeptrue/sforum/apps/api/app/Models/Database"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type DatabaseProvider struct {
	controller *databasecontroller.Controller
}

func NewDatabaseProvider(store database.Store, users identity.ActorStore, sessions *authsession.Manager) *DatabaseProvider {
	return &DatabaseProvider{
		controller: databasecontroller.NewController(database.NewService(store), users, sessions),
	}
}

func (p *DatabaseProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
