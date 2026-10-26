package adminoverviewcontroller

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	adminoverview "github.com/keeptrue/sforum/apps/api/app/Models/AdminOverview"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	service  *adminoverview.Service
	users    identity.ActorStore
	sessions *authsession.Manager
}

func NewController(service *adminoverview.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

func (h *Controller) overview(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	overview, err := h.service.Overview(c.Context(), actor)
	if err != nil {
		return mapOverviewError(err)
	}
	return apphttp.OK(c, overview)
}

func (h *Controller) resources(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	resources, err := h.service.Resources(c.Context(), actor)
	if err != nil {
		return mapOverviewError(err)
	}
	return apphttp.OK(c, resources)
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func mapOverviewError(err error) error {
	if errors.Is(err, identity.ErrPermissionDenied) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return err
}
