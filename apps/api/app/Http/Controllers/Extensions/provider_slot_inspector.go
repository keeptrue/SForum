package extensionscontroller

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

const providerSlotInspectorUnavailableReason = "extensions.provider_slot_inspector_unavailable"

func (h *Controller) inspectProviderSlots(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.service == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotInspectorUnavailableReason)
	}
	inspection, err := h.service.InspectProviderSlots(c.Context(), actor)
	if err != nil {
		if errors.Is(err, extensions.ErrProviderSlotInspectionUnavailable) {
			return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotInspectorUnavailableReason)
		}
		return mapExtensionError(err)
	}
	return apphttp.OK(c, inspection)
}
