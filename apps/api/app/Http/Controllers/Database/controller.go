package databasecontroller

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	database "github.com/keeptrue/sforum/apps/api/app/Models/Database"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	service  *database.Service
	users    identity.ActorStore
	sessions *authsession.Manager
}

func NewController(service *database.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

func (h *Controller) listTables(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	tables, err := h.service.ListTables(c.Context(), actor)
	if err != nil {
		return mapDatabaseError(err)
	}
	return apphttp.OK(c, tables)
}

func (h *Controller) detail(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	detail, err := h.service.Detail(c.Context(), actor, c.Params("schema"), c.Params("table"))
	if err != nil {
		return mapDatabaseError(err)
	}
	return apphttp.OK(c, detail)
}

func (h *Controller) rows(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	rows, err := h.service.Rows(c.Context(), actor, c.Params("schema"), c.Params("table"), rowsInput(c))
	if err != nil {
		return mapDatabaseError(err)
	}
	return apphttp.OK(c, rows)
}

func (h *Controller) reveal(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	result, err := h.service.Reveal(c.Context(), actor, c.Params("schema"), c.Params("table"), database.RevealInput{
		RowKey: c.Query("rowKey"),
		Column: c.Query("column"),
	})
	if err != nil {
		return mapDatabaseError(err)
	}
	return apphttp.OK(c, result)
}

func (h *Controller) exportCSV(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	payload, err := h.service.ExportCSV(c.Context(), actor, c.Params("schema"), c.Params("table"), rowsInput(c))
	if err != nil {
		return mapDatabaseError(err)
	}
	filename := strings.ReplaceAll(fmt.Sprintf("%s.%s.csv", c.Params("schema"), c.Params("table")), `"`, "")
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, filename))
	return c.Send(payload)
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func rowsInput(c fiber.Ctx) database.RowsInput {
	return database.RowsInput{
		Page:           queryInt(c, "page"),
		PerPage:        queryInt(c, "perPage"),
		Sort:           c.Query("sort"),
		Direction:      c.Query("direction"),
		FilterColumn:   c.Query("filterColumn"),
		FilterOperator: c.Query("filterOperator"),
		FilterValue:    c.Query("filterValue"),
	}
}

func queryInt(c fiber.Ctx, name string) int {
	value, err := strconv.Atoi(c.Query(name))
	if err != nil {
		return 0
	}
	return value
}

func mapDatabaseError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, database.ErrTableNotFound):
		return fiber.NewError(fiber.StatusNotFound, database.CodeTableNotFound)
	case errors.Is(err, database.ErrInvalidColumn):
		return fiber.NewError(fiber.StatusUnprocessableEntity, database.CodeColumnInvalid)
	case errors.Is(err, database.ErrInvalidFilter), errors.Is(err, database.ErrInvalidTable):
		return fiber.NewError(fiber.StatusUnprocessableEntity, database.CodeInvalid)
	case errors.Is(err, database.ErrRevealUnavailable):
		return fiber.NewError(fiber.StatusUnprocessableEntity, database.CodeRevealUnavailable)
	case errors.Is(err, database.ErrRowNotFound):
		return fiber.NewError(fiber.StatusNotFound, database.CodeRowNotFound)
	default:
		return err
	}
}
