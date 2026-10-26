package extensionscontroller

import (
	"context"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

type publicContentStyleRuntimeService interface {
	PublicContentStyles(context.Context) (extensions.PublicContentStyleCatalog, error)
}

func (h *Controller) publicContentStyles(c fiber.Ctx) error {
	runtime, ok := h.frontend.(publicContentStyleRuntimeService)
	if h.frontend == nil || !ok {
		return publicFrontendNotFound()
	}
	catalog, err := runtime.PublicContentStyles(c.Context())
	if err != nil {
		return mapPublicFrontendError(err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	return apphttp.OK(c, catalog)
}
