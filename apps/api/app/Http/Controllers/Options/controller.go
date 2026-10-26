package optionscontroller

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	service  *options.Service
	users    identity.ActorStore
	sessions *authsession.Manager
}

func NewController(service *options.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

func NewControllerWithStore(service *options.Service, users identity.ActorStore, sessions *session.Store) *Controller {
	return NewController(service, users, authsession.NewManager(sessions, authsession.Config{}))
}

type updateOptionRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type updateOptionsRequest struct {
	Options []updateOptionRequest `json:"options"`
}

func (h *Controller) listPublic(c fiber.Ctx) error {
	items, err := h.service.List(c.Context())
	if err != nil {
		return err
	}
	return apphttp.OK(c, items)
}

func (h *Controller) getPublic(c fiber.Ctx) error {
	option, err := h.service.Get(c.Context(), c.Params("name"))
	if err != nil {
		return mapOptionsError(err)
	}
	return apphttp.OK(c, option)
}

func (h *Controller) listAdmin(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}

	options, err := h.service.ListAdmin(c.Context(), actor)
	if err != nil {
		return mapOptionsError(err)
	}
	return apphttp.OK(c, options)
}

func (h *Controller) update(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}

	var req updateOptionRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}

	option, err := h.service.Update(c.Context(), actor, options.UpdateInput{
		Name:  req.Name,
		Value: req.Value,
	})
	if err != nil {
		return mapOptionsError(err)
	}
	return apphttp.OK(c, option)
}

func (h *Controller) updateAdmin(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}

	var req updateOptionsRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}

	inputs := make([]options.UpdateInput, 0, len(req.Options))
	for _, item := range req.Options {
		inputs = append(inputs, options.UpdateInput{Name: item.Name, Value: item.Value})
	}

	updated, err := h.service.UpdateMany(c.Context(), actor, inputs)
	if err != nil {
		return mapOptionsError(err)
	}
	return apphttp.OK(c, updated)
}

func (h *Controller) listFeatures(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListFeatureFlagsAdmin(c.Context(), actor)
	if err != nil {
		return mapOptionsError(err)
	}
	return apphttp.OK(c, map[string]any{
		"items":   items,
		"catalog": options.FeatureFlagCatalog(),
	})
}

func (h *Controller) updateFeatures(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req updateOptionsRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	inputs := make([]options.UpdateInput, 0, len(req.Options))
	for _, item := range req.Options {
		// 仅允许目录内 features.* 键，避免误改其它选项。
		if !strings.HasPrefix(item.Name, "features.") {
			return fiber.NewError(fiber.StatusUnprocessableEntity, options.CodeInvalid)
		}
		inputs = append(inputs, options.UpdateInput{Name: item.Name, Value: item.Value})
	}
	if len(inputs) == 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	updated, err := h.service.UpdateMany(c.Context(), actor, inputs)
	if err != nil {
		return mapOptionsError(err)
	}
	return apphttp.OK(c, updated)
}

func (h *Controller) restoreFeatureDefaults(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	updated, err := h.service.RestoreFeatureFlagDefaults(c.Context(), actor)
	if err != nil {
		return mapOptionsError(err)
	}
	return apphttp.OK(c, updated)
}

func (h *Controller) sessionUserID(c fiber.Ctx) (int64, bool, error) {
	return apphttp.ResolveUserID(c, h.sessions)
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func mapOptionsError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, options.ErrInvalidOption):
		return fiber.NewError(fiber.StatusUnprocessableEntity, options.CodeInvalid)
	default:
		return err
	}
}
