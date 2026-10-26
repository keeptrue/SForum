package providers

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	seocontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/SEO"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	seo "github.com/keeptrue/sforum/apps/api/app/Models/SEO"
)

type SEOProvider struct {
	sitemap *seocontroller.SitemapController
}

func NewSEOProvider(pool *pgxpool.Pool, optionsService *options.Service) *SEOProvider {
	service := seo.NewSitemapService(seo.NewPostgresStore(pool), optionsService)
	return &SEOProvider{sitemap: seocontroller.NewSitemapController(service)}
}

func (p *SEOProvider) RegisterRoutes(api fiber.Router) { p.sitemap.RegisterRoutes(api) }
