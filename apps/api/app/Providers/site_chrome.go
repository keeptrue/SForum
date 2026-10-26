package providers

import (
	"github.com/gofiber/fiber/v3"

	sitechromecontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/SiteChrome"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type SiteChromeProvider struct {
	controller *sitechromecontroller.Controller
}

func (p *SiteChromeProvider) WithBrandAssets(uploader sitechrome.BrandAssetUploader) *SiteChromeProvider {
	if p != nil && p.controller != nil {
		p.controller.WithBrandAssets(sitechrome.NewBrandAssetService(uploader))
	}
	return p
}

func NewSiteChromeProvider(store sitechrome.Store, users identity.ActorStore, sessions *authsession.Manager) *SiteChromeProvider {
	return NewSiteChromeProviderWithExtensionNav(store, users, sessions, nil)
}

// NewSiteChromeProviderWithService keeps the HTTP API and Page Registry on one
// production service instance, including the exact-artifact registry runtime.
func NewSiteChromeProviderWithService(service *sitechrome.Service, users identity.ActorStore, sessions *authsession.Manager) *SiteChromeProvider {
	return &SiteChromeProvider{controller: sitechromecontroller.NewController(service, users, sessions)}
}

// NewSiteChromeProviderWithExtensionNav 注入 forum.nav.items 解析（E2.3）。
func NewSiteChromeProviderWithExtensionNav(store sitechrome.Store, users identity.ActorStore, sessions *authsession.Manager, nav sitechrome.ExtensionNavItemProvider) *SiteChromeProvider {
	service := sitechrome.NewService(store)
	if nav != nil {
		service.WithExtensionNavItems(nav)
	}
	return &SiteChromeProvider{
		controller: sitechromecontroller.NewController(service, users, sessions),
	}
}

func (p *SiteChromeProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
