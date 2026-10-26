package systemupdatescontroller

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	systemupdates "github.com/keeptrue/sforum/apps/api/app/Models/SystemUpdates"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	service  *systemupdates.Service
	users    identity.ActorStore
	sessions *authsession.Manager
}

func NewController(service *systemupdates.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

func (h *Controller) status(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	status, err := h.service.Status(c.Context(), actor)
	if err != nil {
		return mapSystemUpdatesError(err)
	}
	return apphttp.OK(c, status)
}

func (h *Controller) check(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	status, err := h.service.CheckNow(c.Context(), actor)
	if err != nil {
		return mapSystemUpdatesError(err)
	}
	return apphttp.OK(c, status)
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func mapSystemUpdatesError(err error) error {
	if errors.Is(err, identity.ErrPermissionDenied) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return err
}
