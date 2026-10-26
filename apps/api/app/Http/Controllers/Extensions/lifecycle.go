package extensionscontroller

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	defaultLifecycleHistoryLimit = 100
	maxLifecycleHistoryLimit     = 500

	lifecycleOperationNotFoundReason = "extension.lifecycle_operation_not_found"
	lifecycleUnavailableReason       = "extension.lifecycle_unavailable"
)

func (h *Controller) lifecycleOperations(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !canInspectLifecycle(actor) {
		return mapExtensionError(identity.ErrPermissionDenied)
	}
	limit, err := lifecycleHistoryLimit(c)
	if err != nil {
		return err
	}
	items, err := h.service.LifecycleOperations(c.Context(), actor, c.Params("id"), limit)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) lifecycleOperation(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !canInspectLifecycle(actor) {
		return mapExtensionError(identity.ErrPermissionDenied)
	}
	operationID, err := positiveLifecycleID(c.Params("operationID"))
	if err != nil {
		return err
	}
	item, err := h.service.LifecycleOperation(c.Context(), actor, c.Params("id"), operationID)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) recoverLifecycleOperation(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	operationID, err := positiveLifecycleID(c.Params("operationID"))
	if err != nil {
		return err
	}
	var input extensions.LifecycleRecoveryInput
	if len(c.Body()) == 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	item, err := h.service.RecoverLifecycleOperation(c.Context(), actor, c.Params("id"), operationID, input)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, item)
}

func canInspectLifecycle(actor identity.Actor) bool {
	return actor.Can(identity.PermissionExtensionView) || actor.Can(identity.PermissionExtensionManage)
}

func lifecycleHistoryLimit(c fiber.Ctx) (int, error) {
	values := c.Request().URI().QueryArgs().PeekMulti("limit")
	if len(values) == 0 {
		return defaultLifecycleHistoryLimit, nil
	}
	if len(values) != 1 {
		return 0, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	value, err := positiveLifecycleID(string(values[0]))
	if err != nil || value > maxLifecycleHistoryLimit {
		return 0, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	return int(value), nil
}

func positiveLifecycleID(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	return parsed, nil
}

func mapLifecycleInspectionError(err error) error {
	switch {
	case errors.Is(err, extensions.ErrLifecycleOperationNotFound):
		return fiber.NewError(fiber.StatusNotFound, lifecycleOperationNotFoundReason)
	case errors.Is(err, extensions.ErrLifecycleInvalidInput):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	case errors.Is(err, extensions.ErrLifecycleCoordinatorUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, lifecycleUnavailableReason)
	default:
		return nil
	}
}
