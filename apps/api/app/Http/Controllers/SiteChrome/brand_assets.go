package sitechromecontroller

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
)

type brandAssetDTO struct {
	ID          int64  `json:"id"`
	PublicID    string `json:"publicId"`
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Width       *int   `json:"width,omitempty"`
	Height      *int   `json:"height,omitempty"`
}

func (h *Controller) WithBrandAssets(assets *sitechrome.BrandAssetService) *Controller {
	if h != nil {
		h.brandAssets = assets
	}
	return h
}

func (h *Controller) adminUploadBrandAsset(c fiber.Ctx) error {
	if h == nil || h.brandAssets == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	}
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

	item, err := h.brandAssets.Upload(c.Context(), actor, c.FormValue("context"), attachments.UploadInput{
		OriginalName: fileHeader.Filename,
		ContentType:  fileHeader.Header.Get("Content-Type"),
		SizeBytes:    fileHeader.Size,
		File:         file,
	})
	if err != nil {
		return mapBrandAssetError(err)
	}
	return apphttp.Created(c, brandAssetDTO{
		ID: item.ID, PublicID: item.PublicID, URL: item.URL, ContentType: item.ContentType,
		Size: item.SizeBytes, Width: item.ImageWidth, Height: item.ImageHeight,
	})
}

func mapBrandAssetError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, sitechrome.ErrInvalidBrandAsset), errors.Is(err, attachments.ErrInvalidAttachment):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeInvalidAttachment)
	case errors.Is(err, attachments.ErrUploadDisabled):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeUploadDisabled)
	case errors.Is(err, attachments.ErrStorageUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	default:
		return err
	}
}
