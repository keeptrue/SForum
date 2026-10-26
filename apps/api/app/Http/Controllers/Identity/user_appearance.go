package identitycontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func (h *Controller) updateCurrentUserAppearance(c fiber.Ctx) error {
	userID, ok, err := h.sessionUserID(c)
	if err != nil {
		return err
	}
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	var req struct {
		Theme           string `json:"theme"`
		LightBackground string `json:"lightBackground"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	current, err := h.appearance.Update(c.Context(), userID, identity.AppearancePreference{
		Theme:           req.Theme,
		LightBackground: req.LightBackground,
	})
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, current)
}

func (h *Controller) clearCurrentUserAppearance(c fiber.Ctx) error {
	userID, ok, err := h.sessionUserID(c)
	if err != nil {
		return err
	}
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	current, err := h.appearance.Clear(c.Context(), userID)
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, current)
}
