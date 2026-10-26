package sitechromecontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
)

type navigationApplyRequest struct {
	ExpectedRevision uint64                        `json:"expectedRevision"`
	Reason           string                        `json:"reason"`
	Document         sitechrome.NavigationDocument `json:"document"`
}

type navigationDefaultsPreviewRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
	Scope            string `json:"scope"`
	Location         string `json:"location"`
}

type navigationPreviewApplyRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
	PreviewToken     string `json:"previewToken"`
	Reason           string `json:"reason"`
}

type navigationImportPreviewRequest struct {
	ExpectedRevision uint64                      `json:"expectedRevision"`
	Mode             string                      `json:"mode"`
	Backup           sitechrome.NavigationBackup `json:"backup"`
}

type navigationSnapshotRestoreRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
	Reason           string `json:"reason"`
}

func (h *Controller) adminNavigationDocument(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	document, err := h.service.ReadAdminNavigationDocument(c.Context(), actor)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, document)
}

func (h *Controller) adminApplyNavigation(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var request navigationApplyRequest
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	document, err := h.service.ApplyNavigationDocument(c.Context(), actor, sitechrome.NavigationApplyInput{ExpectedRevision: request.ExpectedRevision, Reason: request.Reason, Document: request.Document})
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, document)
}

func (h *Controller) adminPreviewNavigationDefaults(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var request navigationDefaultsPreviewRequest
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	preview, err := h.service.PreviewNavigationDefaults(c.Context(), actor, sitechrome.NavigationDefaultsPreviewInput{ExpectedRevision: request.ExpectedRevision, Scope: request.Scope, Location: request.Location})
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, preview)
}

func (h *Controller) adminApplyNavigationPreview(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var request navigationPreviewApplyRequest
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	document, err := h.service.ApplyNavigationPreview(c.Context(), actor, sitechrome.NavigationPreviewApplyInput{ExpectedRevision: request.ExpectedRevision, PreviewToken: request.PreviewToken, Reason: request.Reason})
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, document)
}

func (h *Controller) adminNavigationSnapshots(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	snapshots, err := h.service.ListNavigationSnapshots(c.Context(), actor)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, snapshots)
}

func (h *Controller) adminNavigationSnapshot(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := parseID(c.Params("snapshotID"))
	if err != nil {
		return err
	}
	snapshot, err := h.service.GetNavigationSnapshot(c.Context(), actor, id)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, snapshot)
}

func (h *Controller) adminRestoreNavigationSnapshot(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := parseID(c.Params("snapshotID"))
	if err != nil {
		return err
	}
	var request navigationSnapshotRestoreRequest
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	document, err := h.service.RestoreNavigationSnapshot(c.Context(), actor, id, request.ExpectedRevision, request.Reason)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, document)
}

func (h *Controller) adminExportNavigation(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	backup, err := h.service.ExportNavigationBackup(c.Context(), actor)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, backup)
}

func (h *Controller) adminPreviewNavigationImport(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var request navigationImportPreviewRequest
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	preview, err := h.service.PreviewNavigationImport(c.Context(), actor, sitechrome.NavigationImportPreviewInput{ExpectedRevision: request.ExpectedRevision, Mode: request.Mode, Backup: request.Backup})
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, preview)
}
