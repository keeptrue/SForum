package attachmentscontroller

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	uploadpolicy "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/UploadPolicy"
)

func (h *Controller) currentUploadPolicy(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	policy, err := h.service.UploadPolicy(c.Context(), actor)
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, policy)
}

func (h *Controller) listRoleUploadPolicies(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListRoleUploadPolicies(c.Context(), actor)
	if err != nil {
		return mapUploadPolicyError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) getUserUploadPolicy(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	userID, err := uploadPolicyUserID(c)
	if err != nil {
		return mapUploadPolicyError(err)
	}
	item, err := h.service.GetUserUploadPolicy(c.Context(), actor, userID)
	if err != nil {
		return mapUploadPolicyError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) setRoleUploadPolicy(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input uploadpolicy.LimitInput
	if err := c.Bind().Body(&input); err != nil {
		return mapUploadPolicyError(uploadpolicy.ErrInvalidPolicy)
	}
	item, err := h.service.SetRoleUploadPolicy(c.Context(), actor, strings.TrimSpace(c.Params("roleKey")), input)
	if err != nil {
		return mapUploadPolicyError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) deleteRoleUploadPolicy(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	item, err := h.service.DeleteRoleUploadPolicy(c.Context(), actor, strings.TrimSpace(c.Params("roleKey")))
	if err != nil {
		return mapUploadPolicyError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) setUserUploadPolicy(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	userID, err := uploadPolicyUserID(c)
	if err != nil {
		return mapUploadPolicyError(err)
	}
	var input uploadpolicy.LimitInput
	if err := c.Bind().Body(&input); err != nil {
		return mapUploadPolicyError(uploadpolicy.ErrInvalidPolicy)
	}
	item, err := h.service.SetUserUploadPolicy(c.Context(), actor, userID, input)
	if err != nil {
		return mapUploadPolicyError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) deleteUserUploadPolicy(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	userID, err := uploadPolicyUserID(c)
	if err != nil {
		return mapUploadPolicyError(err)
	}
	item, err := h.service.DeleteUserUploadPolicy(c.Context(), actor, userID)
	if err != nil {
		return mapUploadPolicyError(err)
	}
	return apphttp.OK(c, item)
}

func uploadPolicyUserID(c fiber.Ctx) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(c.Params("userID")), 10, 64)
	if err != nil || value <= 0 {
		return 0, uploadpolicy.ErrInvalidPolicy
	}
	return value, nil
}

func mapUploadPolicyError(err error) error {
	switch {
	case errors.Is(err, uploadpolicy.ErrProtectedActor):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeUploadPolicyProtected)
	case errors.Is(err, uploadpolicy.ErrInvalidPolicy):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeUploadPolicyInvalid)
	default:
		return mapAttachmentError(err)
	}
}
