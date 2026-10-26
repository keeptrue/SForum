package jobscontroller

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	jobs "github.com/keeptrue/sforum/apps/api/app/Models/Jobs"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	service  *jobs.Service
	users    identity.ActorStore
	sessions *authsession.Manager
}

func NewController(service *jobs.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	group := api.Group("/admin/jobs")
	group.Get("/overview", h.overview)
	// /schedules 必须在 /:id 之前注册，避免被当成 job id。
	group.Get("/schedules", h.schedules)
	group.Post("/schedules/:scheduleId/enable", h.enableSchedule)
	group.Post("/schedules/:scheduleId/disable", h.disableSchedule)
	group.Post("/schedules/:scheduleId/trigger", h.triggerSchedule)
	group.Get("", h.list)
	group.Get("/:id", h.detail)
	group.Post("/:id/retry", h.retry)
	group.Post("/:id/cancel", h.cancel)
	group.Post("/queues/:name/pause", h.pause)
	group.Post("/queues/:name/resume", h.resume)
}

func (h *Controller) schedules(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	data, err := h.service.Schedules(c.Context(), actor)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) enableSchedule(c fiber.Ctx) error {
	return h.setScheduleEnabled(c, true)
}

func (h *Controller) disableSchedule(c fiber.Ctx) error {
	return h.setScheduleEnabled(c, false)
}

func (h *Controller) setScheduleEnabled(c fiber.Ctx, enabled bool) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	data, err := h.service.SetScheduleEnabled(c.Context(), actor, c.Params("scheduleId"), enabled)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) triggerSchedule(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	data, err := h.service.TriggerSchedule(c.Context(), actor, c.Params("scheduleId"))
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) overview(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	data, err := h.service.Overview(c.Context(), actor)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) list(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	data, err := h.service.List(c.Context(), actor, jobs.ListInput{Queue: c.Query("queue"), Kind: c.Query("kind"), State: c.Query("state"), Limit: queryInt(c.Query("limit"), 50)})
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) detail(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	data, err := h.service.Detail(c.Context(), actor, jobID(c))
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) retry(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	data, err := h.service.Retry(c.Context(), actor, jobID(c))
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) cancel(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	data, err := h.service.Cancel(c.Context(), actor, jobID(c))
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) pause(c fiber.Ctx) error  { return h.queue(c, true) }
func (h *Controller) resume(c fiber.Ctx) error { return h.queue(c, false) }

func (h *Controller) queue(c fiber.Ctx, paused bool) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if err := h.service.SetQueuePaused(c.Context(), actor, c.Params("name"), paused); err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, map[string]any{"name": c.Params("name"), "paused": paused})
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func jobID(c fiber.Ctx) int64 {
	id, _ := strconv.ParseInt(c.Params("id"), 10, 64)
	return id
}

func queryInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func mapError(err error) error {
	if errors.Is(err, identity.ErrPermissionDenied) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if errors.Is(err, jobs.ErrNotFound) {
		return fiber.NewError(fiber.StatusNotFound, "jobs.not_found")
	}
	if errors.Is(err, jobs.ErrScheduleNotFound) {
		return fiber.NewError(fiber.StatusNotFound, "jobs.schedule_not_found")
	}
	if errors.Is(err, jobs.ErrScheduleDisabled) {
		return fiber.NewError(fiber.StatusConflict, "jobs.schedule_disabled")
	}
	return err
}
