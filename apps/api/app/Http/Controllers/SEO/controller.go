package seocontroller

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	seo "github.com/keeptrue/sforum/apps/api/app/Models/SEO"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	assets   *seo.AssetService
	users    identity.ActorStore
	sessions *authsession.Manager
}

type SitemapController struct{ service *seo.SitemapService }

func NewSitemapController(service *seo.SitemapService) *SitemapController {
	return &SitemapController{service: service}
}

func (h *SitemapController) RegisterRoutes(api fiber.Router) { api.Get("/seo/sitemap-entries", h.list) }

func (h *SitemapController) list(c fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page"))
	perPage, _ := strconv.Atoi(c.Query("perPage"))
	result, err := h.service.List(c.Context(), seo.SitemapListInput{Type: c.Query("type"), Page: page, PerPage: perPage})
	if errors.Is(err, seo.ErrInvalidSitemapRequest) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "seo.sitemap.invalid_request")
	}
	if err != nil {
		return err
	}
	return apphttp.OK(c, result)
}

type assetDTO struct {
	PublicID    string `json:"publicId"`
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Width       *int   `json:"width,omitempty"`
	Height      *int   `json:"height,omitempty"`
}

func NewController(assets *seo.AssetService, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{assets: assets, users: users, sessions: sessions}
}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Post("/admin/seo/assets", h.uploadAsset)
}

func (h *Controller) uploadAsset(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeInvalidAttachment)
	}
	file, err := fileHeader.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	item, err := h.assets.Replace(c.Context(), actor, c.FormValue("context"), attachments.UploadInput{
		OriginalName: fileHeader.Filename,
		ContentType:  fileHeader.Header.Get("Content-Type"),
		SizeBytes:    fileHeader.Size,
		File:         file,
	})
	if err != nil {
		return mapError(err)
	}
	return apphttp.Created(c, assetDTO{
		PublicID: item.PublicID, URL: item.URL, ContentType: item.ContentType,
		Size: item.SizeBytes, Width: item.ImageWidth, Height: item.ImageHeight,
	})
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func mapError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, seo.ErrInvalidAsset), errors.Is(err, attachments.ErrInvalidAttachment):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeInvalidAttachment)
	case errors.Is(err, attachments.ErrUploadDisabled):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeUploadDisabled)
	case errors.Is(err, attachments.ErrStorageUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	default:
		return err
	}
}
