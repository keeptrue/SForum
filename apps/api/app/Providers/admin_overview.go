package providers

import (
	"github.com/gofiber/fiber/v3"

	adminoverviewcontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/AdminOverview"
	adminoverview "github.com/keeptrue/sforum/apps/api/app/Models/AdminOverview"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type AdminOverviewProvider struct {
	controller *adminoverviewcontroller.Controller
}

func NewAdminOverviewProvider(store adminoverview.Store, runtime adminoverview.RuntimeProvider, users identity.ActorStore, sessions *authsession.Manager) *AdminOverviewProvider {
	return NewAdminOverviewProviderWithWidgets(store, runtime, users, sessions, nil)
}

// NewAdminOverviewProviderWithWidgets 注入扩展仪表盘 widgets（F4.3）。
func NewAdminOverviewProviderWithWidgets(store adminoverview.Store, runtime adminoverview.RuntimeProvider, users identity.ActorStore, sessions *authsession.Manager, widgets adminoverview.DashboardWidgetProvider) *AdminOverviewProvider {
	opts := []adminoverview.Option{}
	if widgets != nil {
		opts = append(opts, adminoverview.WithDashboardWidgets(widgets))
	}
	return &AdminOverviewProvider{
		controller: adminoverviewcontroller.NewController(adminoverview.NewService(store, runtime, opts...), users, sessions),
	}
}

func (p *AdminOverviewProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
