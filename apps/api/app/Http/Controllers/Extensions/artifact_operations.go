package extensionscontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	modelextensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func (h *Controller) cleanupMissingArtifacts(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input modelextensions.MissingArtifactCleanupInput
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	result, err := h.service.CleanupMissingArtifacts(c.Context(), actor, input)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, result)
}

// finalizeUninstallCleanup 是卸载物理清理的免键恢复入口：原幂等键丢失时，
// super_admin 按扩展维度把全部 pending tombstone 收敛到 finalized。端点幂等。
func (h *Controller) finalizeUninstallCleanup(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.cleanupConvergence == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "extension.lifecycle_cleanup_failed")
	}
	result, err := h.cleanupConvergence.FinalizeUninstallCleanupForExtension(c.Context(), actor, c.Params("id"))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, result)
}

func (h *Controller) restart(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input modelextensions.RestartInput
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&input); err != nil {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
		}
	}
	input.IdempotencyKey = c.Get("Idempotency-Key")
	item, err := h.service.Restart(c.Context(), actor, c.Params("id"), input)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, item)
}
