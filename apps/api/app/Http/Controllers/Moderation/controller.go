package moderationcontroller

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	service  *moderation.Service
	users    identity.ActorStore
	sessions *authsession.Manager
}

func NewController(service *moderation.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

type createReportRequest struct {
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
	ReasonCode string `json:"reasonCode"`
	Body       string `json:"body"`
}

type updateReportRequest struct {
	Status     string `json:"status"`
	ReviewNote string `json:"reviewNote"`
}

type decisionRequest struct {
	Source     string `json:"source"`
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
	ReportID   int64  `json:"reportId"`
	Action     string `json:"action"`
	ReviewNote string `json:"reviewNote"`
}

func (h *Controller) getSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	settings, err := h.service.GetSettings(c.Context(), actor)
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) updateSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var settings moderation.Settings
	if err := c.Bind().Body(&settings); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "moderation.settings_invalid")
	}
	updated, err := h.service.UpdateSettings(c.Context(), actor, settings)
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, updated)
}

func (h *Controller) resetSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	settings, err := h.service.ResetSettings(c.Context(), actor)
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) queueCounts(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	counts, err := h.service.QueueCounts(c.Context(), actor)
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, counts)
}

func (h *Controller) listPending(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	list, err := h.service.ListPending(c.Context(), actor, moderation.WorkbenchListInput{
		TargetType: c.Query("targetType"), Page: queryInt(c, "page"), PerPage: queryInt(c, "perPage"),
	})
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, list)
}

func (h *Controller) listReportItems(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	list, err := h.service.ListReportItems(c.Context(), actor, moderation.WorkbenchListInput{
		TargetType: c.Query("targetType"), Page: queryInt(c, "page"), PerPage: queryInt(c, "perPage"),
	})
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, list)
}

func (h *Controller) listAdminDecisions(c fiber.Ctx) error {
	return h.listDecisions(c, true)
}

func (h *Controller) listWorkbenchHistory(c fiber.Ctx) error {
	return h.listDecisions(c, false)
}

func (h *Controller) listDecisions(c fiber.Ctx, admin bool) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	list, err := h.service.ListDecisions(c.Context(), actor, moderation.DecisionListInput{
		Action: c.Query("action"), TargetType: c.Query("targetType"), ReviewerID: queryInt64(c, "reviewerId"),
		Page: queryInt(c, "page"), PerPage: queryInt(c, "perPage"),
	}, admin)
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, list)
}

func (h *Controller) getReviewContext(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	item, err := h.service.GetReviewContext(c.Context(), actor, moderation.ReviewContextInput{
		Source: c.Query("source"), TargetType: c.Params("targetType"),
		TargetID: int64(paramInt(c, "targetID")), ReportID: queryInt64(c, "reportId"),
	})
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) submitDecision(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req decisionRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "moderation.decision_invalid")
	}
	decision, err := h.service.SubmitDecision(c.Context(), actor, moderation.DecisionInput{
		Source: req.Source, TargetType: req.TargetType, TargetID: req.TargetID,
		ReportID: req.ReportID, Action: req.Action, ReviewNote: req.ReviewNote,
	})
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, decision)
}

// createReport 公开举报入口（登录活跃用户）。
func (h *Controller) createReport(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req createReportRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, moderation.CodeReportInvalid)
	}
	report, err := h.service.CreateReport(c.Context(), actor, moderation.CreateReportInput{
		TargetType: req.TargetType,
		TargetID:   req.TargetID,
		ReasonCode: req.ReasonCode,
		Body:       req.Body,
	})
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.Created(c, report)
}

// listReports 管理员审核队列。
func (h *Controller) listReports(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	list, err := h.service.ListReports(c.Context(), actor, moderation.ReportListInput{
		Status:     c.Query("status"),
		TargetType: c.Query("targetType"),
		ReporterID: queryInt64(c, "reporterId"),
		Page:       queryInt(c, "page"),
		PerPage:    queryInt(c, "perPage"),
	})
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, list)
}

// updateReport 审核员更新举报状态。
func (h *Controller) updateReport(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req updateReportRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, moderation.CodeReportInvalid)
	}
	report, err := h.service.UpdateReport(c.Context(), actor, moderation.UpdateReportInput{
		ReportID:   int64(paramInt(c, "reportID")),
		Status:     req.Status,
		ReviewNote: req.ReviewNote,
	})
	if err != nil {
		return mapModerationError(err)
	}
	return apphttp.OK(c, report)
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func mapModerationError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, moderation.ErrReportInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, moderation.CodeReportInvalid)
	case errors.Is(err, moderation.ErrReportNotFound):
		return fiber.NewError(fiber.StatusNotFound, moderation.CodeReportNotFound)
	case errors.Is(err, moderation.ErrReportDuplicate):
		return fiber.NewError(fiber.StatusConflict, moderation.CodeReportDuplicate)
	case errors.Is(err, moderation.ErrReportTargetInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, moderation.CodeReportTargetInvalid)
	case errors.Is(err, moderation.ErrSettingsInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "moderation.settings_invalid")
	case errors.Is(err, moderation.ErrDecisionInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "moderation.decision_invalid")
	case errors.Is(err, moderation.ErrTaskNotFound):
		return fiber.NewError(fiber.StatusNotFound, "moderation.task_not_found")
	case errors.Is(err, moderation.ErrTaskConflict):
		return fiber.NewError(fiber.StatusConflict, "moderation.task_conflict")
	default:
		return err
	}
}

func queryInt(c fiber.Ctx, key string) int {
	value, _ := strconv.Atoi(c.Query(key))
	return value
}

func queryInt64(c fiber.Ctx, key string) int64 {
	value, _ := strconv.ParseInt(c.Query(key), 10, 64)
	return value
}

func paramInt(c fiber.Ctx, key string) int {
	value, _ := strconv.Atoi(c.Params(key))
	return value
}
