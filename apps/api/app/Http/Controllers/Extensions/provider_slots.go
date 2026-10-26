package extensionscontroller

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

const (
	providerSlotInvalidReason     = "extensions.provider_slot_invalid"
	providerSlotNotFoundReason    = "extensions.provider_slot_not_found"
	providerSlotConflictReason    = "extensions.provider_slot_conflict"
	providerSlotStaleReason       = "extensions.provider_slot_stale"
	providerSlotUnavailableReason = "extensions.provider_slot_unavailable"
)

type ProviderSlotProber interface {
	ProbeProviderSlotCandidate(context.Context, string, string) (extensionsruntime.ProviderSlotProbeResult, error)
}

type providerSlotSelectRequest struct {
	ContractID       string `json:"contractId"`
	CandidateID      string `json:"candidateId"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

type providerSlotResetRequest struct {
	ContractID       string `json:"contractId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	ReasonCode       string `json:"reasonCode"`
}

type providerSlotProbeRequest struct {
	ContractID  string `json:"contractId"`
	CandidateID string `json:"candidateId"`
}

func (h *Controller) selectProviderSlot(c fiber.Ctx) error {
	actor, err := h.providerSlotMutator(c)
	if err != nil {
		return err
	}
	if h.providerSlots == nil || h.providerAuditor == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotUnavailableReason)
	}
	var body providerSlotSelectRequest
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, providerSlotInvalidReason)
	}
	body.ContractID = strings.TrimSpace(body.ContractID)
	body.CandidateID = strings.TrimSpace(body.CandidateID)
	auditID, err := h.providerAuditor.AppendReturningID(c.Context(), audit.Event{
		ActorUserID: actor.ID, Action: audit.ActionProviderSlotSelect,
		Metadata: map[string]any{
			"contractId": body.ContractID, "candidateId": body.CandidateID,
			"expectedRevision": body.ExpectedRevision,
		},
	})
	if err != nil || auditID <= 0 {
		return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotUnavailableReason)
	}
	selection, err := h.providerSlots.Select(c.Context(), body.ContractID, body.CandidateID,
		body.ExpectedRevision, actor.ID, auditID)
	if err != nil {
		return mapProviderSlotError(err)
	}
	return apphttp.OK(c, selection)
}

func (h *Controller) resetProviderSlot(c fiber.Ctx) error {
	actor, err := h.providerSlotMutator(c)
	if err != nil {
		return err
	}
	if h.providerSlots == nil || h.providerAuditor == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotUnavailableReason)
	}
	var body providerSlotResetRequest
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, providerSlotInvalidReason)
	}
	body.ContractID = strings.TrimSpace(body.ContractID)
	body.ReasonCode = strings.TrimSpace(body.ReasonCode)
	auditID, err := h.providerAuditor.AppendReturningID(c.Context(), audit.Event{
		ActorUserID: actor.ID, Action: audit.ActionProviderSlotReset,
		Metadata: map[string]any{
			"contractId": body.ContractID, "expectedRevision": body.ExpectedRevision,
			"reasonCode": body.ReasonCode,
		},
	})
	if err != nil || auditID <= 0 {
		return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotUnavailableReason)
	}
	err = h.providerSlots.Reset(c.Context(), extensionsruntime.ResetProviderSlotRequest{
		ContractID: body.ContractID, ExpectedRevision: body.ExpectedRevision,
		ActorUserID: actor.ID, AuditEventID: auditID, ReasonCode: body.ReasonCode,
	})
	if err != nil {
		return mapProviderSlotError(err)
	}
	return apphttp.OK(c, map[string]any{"reset": true, "contractId": body.ContractID})
}

func (h *Controller) probeProviderSlot(c fiber.Ctx) error {
	actor, err := h.providerSlotMutator(c)
	if err != nil {
		return err
	}
	if h.providerProber == nil || h.providerAuditor == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotUnavailableReason)
	}
	var body providerSlotProbeRequest
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, providerSlotInvalidReason)
	}
	body.ContractID = strings.TrimSpace(body.ContractID)
	body.CandidateID = strings.TrimSpace(body.CandidateID)
	started := time.Now()
	result, probeErr := h.providerProber.ProbeProviderSlotCandidate(c.Context(), body.ContractID, body.CandidateID)
	_, auditErr := h.providerAuditor.AppendReturningID(c.Context(), audit.Event{
		ActorUserID: actor.ID, Action: audit.ActionProviderSlotProbe,
		Metadata: map[string]any{
			"contractId": body.ContractID, "candidateId": body.CandidateID,
			"success": probeErr == nil && result.OK, "durationMs": time.Since(started).Milliseconds(),
		},
	})
	if auditErr != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotUnavailableReason)
	}
	if probeErr != nil {
		return mapProviderSlotError(probeErr)
	}
	return apphttp.OK(c, result)
}

func (h *Controller) providerSlotEvents(c fiber.Ctx) error {
	if _, err := h.providerSlotViewer(c); err != nil {
		return err
	}
	if h.providerSlots == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotUnavailableReason)
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	events, err := h.providerSlots.Events(c.Context(), c.Query("contractId"), limit)
	if err != nil {
		return mapProviderSlotError(err)
	}
	return apphttp.OK(c, events)
}

func (h *Controller) providerSlotViewer(c fiber.Ctx) (identity.Actor, error) {
	actor, err := h.actor(c)
	if err != nil {
		return identity.Actor{}, err
	}
	if !actor.Can(identity.PermissionExtensionView) && !actor.Can(identity.PermissionExtensionManage) {
		return identity.Actor{}, fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return actor, nil
}

func (h *Controller) providerSlotMutator(c fiber.Ctx) (identity.Actor, error) {
	actor, err := h.actor(c)
	if err != nil {
		return identity.Actor{}, err
	}
	if !actor.IsSuperAdmin() {
		return identity.Actor{}, fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return actor, nil
}

func mapProviderSlotError(err error) error {
	switch {
	case errors.Is(err, extensionsruntime.ErrProviderSlotSelectionInvalid), errors.Is(err, extensionsruntime.ErrProviderSlotInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, providerSlotInvalidReason)
	case errors.Is(err, extensionsruntime.ErrProviderSlotSelectionNotFound), errors.Is(err, extensionsruntime.ErrProviderSlotNotFound):
		return fiber.NewError(fiber.StatusNotFound, providerSlotNotFoundReason)
	case errors.Is(err, extensionsruntime.ErrProviderSlotSelectionStale):
		return fiber.NewError(fiber.StatusConflict, providerSlotStaleReason)
	case errors.Is(err, extensionsruntime.ErrProviderSlotSelectionRevisionConflict), errors.Is(err, extensionsruntime.ErrProviderSlotConflict):
		return fiber.NewError(fiber.StatusConflict, providerSlotConflictReason)
	case errors.Is(err, extensionsruntime.ErrProviderSlotNoProvider):
		return fiber.NewError(fiber.StatusServiceUnavailable, providerSlotUnavailableReason)
	default:
		return err
	}
}
