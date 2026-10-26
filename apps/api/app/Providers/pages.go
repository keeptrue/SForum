package providers

import (
	"github.com/gofiber/fiber/v3"

	pagescontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Pages"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	pageviewmodels "github.com/keeptrue/sforum/apps/api/app/Models/PageViewModels"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// PagesProvider 注册 Page Registry HTTP 路由。
type PagesProvider struct {
	controller *pagescontroller.Controller
}

func NewPagesProvider(registry *pages.Registry, users identity.ActorStore, sessions *authsession.Manager) *PagesProvider {
	return &PagesProvider{
		controller: pagescontroller.NewController(registry, users, sessions),
	}
}

func NewPagesProviderWithThemes(registry *pages.Registry, users identity.ActorStore, sessions *authsession.Manager, themes pagescontroller.ThemePackageStore) *PagesProvider {
	return &PagesProvider{
		controller: pagescontroller.NewControllerWithThemes(registry, users, sessions, themes),
	}
}

// 确保 extensions.Store 满足 ThemePackageStore。
var _ pagescontroller.ThemePackageStore = (extensions.Store)(nil)

// WithAuditor 注入页面批准/恢复审计。
func (p *PagesProvider) WithAuditor(w audit.Writer) *PagesProvider {
	if p != nil && p.controller != nil {
		p.controller.WithAuditor(w)
	}
	return p
}

// WithLoader 注入受控 PageDataLoader 网关。
func (p *PagesProvider) WithLoader(g *pages.LoaderGateway) *PagesProvider {
	if p != nil && p.controller != nil {
		p.controller.WithLoader(g)
	}
	return p
}

// WithThemeRuntime 注入启动/生命周期统一发布的精确主题快照。
func (p *PagesProvider) WithThemeRuntime(runtime *pages.ThemeRuntimeRegistry) *PagesProvider {
	if p != nil && p.controller != nil {
		p.controller.WithThemeRuntime(runtime)
	}
	return p
}

func (p *PagesProvider) WithCorePageViewModels(source *pageviewmodels.CorePageViewModelSource) *PagesProvider {
	if p != nil && p.controller != nil {
		p.controller.WithCorePageViewModels(source)
	}
	return p
}

// WithPageRegions 注入 forum.page.regions 区域贡献解析源。
func (p *PagesProvider) WithPageRegions(source pagescontroller.PageRegionSource) *PagesProvider {
	if p != nil && p.controller != nil {
		p.controller.WithPageRegions(source)
	}
	return p
}

func (p *PagesProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
