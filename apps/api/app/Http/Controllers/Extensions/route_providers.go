package extensionscontroller

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const (
	routeProviderInvalidReason     = "extensions.route_provider_invalid"
	routeProviderNotFoundReason    = "extensions.route_provider_not_found"
	routeProviderConflictReason    = "extensions.route_provider_conflict"
	routeProviderStaleReason       = "extensions.route_provider_stale"
	routeProviderUnavailableReason = "extensions.route_provider_unavailable"
)

type routeProviderSelectRequest struct {
	TargetRouteID           string                `json:"targetRouteId"`
	TargetContractVersion   string                `json:"targetContractVersion"`
	Method                  string                `json:"method"`
	PathSignature           string                `json:"pathSignature"`
	ProviderRouteID         string                `json:"providerRouteId"`
	ProviderContractVersion string                `json:"providerContractVersion"`
	ProviderArtifact        routes.PluginArtifact `json:"providerArtifact"`
	ExpectedRevision        int64                 `json:"expectedRevision"`
}

type routeProviderResetRequest struct {
	TargetRouteID         string `json:"targetRouteId"`
	TargetContractVersion string `json:"targetContractVersion"`
	Method                string `json:"method"`
	PathSignature         string `json:"pathSignature"`
	ExpectedRevision      int64  `json:"expectedRevision"`
	ReasonCode            string `json:"reasonCode"`
}

type routeProviderConflictResponse struct {
	Key             routes.ProviderSelectionKey `json:"key"`
	Candidates      []routeProviderCandidate    `json:"candidates"`
	SelectionStatus string                      `json:"selectionStatus"`
	Selection       *routes.ProviderSelection   `json:"selection,omitempty"`
}

type routeProviderCandidate struct {
	RouteID         string                 `json:"routeId"`
	ContractVersion string                 `json:"contractVersion"`
	Action          string                 `json:"action"`
	TargetRouteID   string                 `json:"targetRouteId,omitempty"`
	Method          string                 `json:"method"`
	Path            string                 `json:"path"`
	PathSignature   string                 `json:"pathSignature"`
	Priority        int                    `json:"priority"`
	ProviderKind    routes.ProviderKind    `json:"providerKind"`
	Artifact        *routes.PluginArtifact `json:"artifact,omitempty"`
	Guard           string                 `json:"guard"`
	Permission      string                 `json:"permission,omitempty"`
	Handler         string                 `json:"handler,omitempty"`
	Destination     string                 `json:"destination,omitempty"`
	Mode            string                 `json:"mode"`
	Fallback        string                 `json:"fallback"`
	RequestSchema   string                 `json:"requestSchema,omitempty"`
	ResponseSchema  string                 `json:"responseSchema,omitempty"`
	TimeoutMS       int                    `json:"timeoutMs"`
}

func (h *Controller) routeProviderConflicts(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.routeProviders == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeProviderUnavailableReason)
	}
	items, err := h.routeProviders.Conflicts(c.Context())
	if err != nil {
		return mapRouteProviderError(err)
	}
	response := make([]routeProviderConflictResponse, 0, len(items))
	for _, item := range items {
		view := routeProviderConflictResponse{
			Key: item.Key, SelectionStatus: item.SelectionStatus, Selection: item.Selection,
			Candidates: make([]routeProviderCandidate, 0, len(item.Conflict.Candidates)),
		}
		for _, candidate := range item.Conflict.Candidates {
			candidateView := routeProviderCandidate{
				RouteID: candidate.ID, ContractVersion: candidate.ContractVersion,
				Action: candidate.Action, TargetRouteID: candidate.TargetID,
				Method: candidate.Method, Path: candidate.Path, PathSignature: candidate.PathSignature,
				Priority: candidate.Priority, ProviderKind: candidate.Provider.Kind,
				Guard: candidate.Guard, Permission: candidate.Permission, Handler: candidate.Handler,
				Destination: candidate.Destination, Mode: candidate.Mode, Fallback: candidate.Fallback,
				RequestSchema: candidate.RequestSchema, ResponseSchema: candidate.ResponseSchema,
				TimeoutMS: candidate.TimeoutMS,
			}
			if candidate.Provider.Kind == routes.ProviderPlugin {
				artifact := candidate.Provider.Artifact
				candidateView.Artifact = &artifact
			}
			view.Candidates = append(view.Candidates, candidateView)
		}
		response = append(response, view)
	}
	return apphttp.OK(c, response)
}

func (h *Controller) routeProviderCurrent(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.routeProviders == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeProviderUnavailableReason)
	}
	selection, err := h.routeProviders.Current(c.Context(), routeProviderKeyFromQuery(c))
	if err != nil {
		return mapRouteProviderError(err)
	}
	return apphttp.OK(c, selection)
}

func (h *Controller) routeProviderEvents(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.routeProviders == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeProviderUnavailableReason)
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	events, err := h.routeProviders.Events(c.Context(), routeProviderKeyFromQuery(c), limit)
	if err != nil {
		return mapRouteProviderError(err)
	}
	return apphttp.OK(c, events)
}

func (h *Controller) selectRouteProvider(c fiber.Ctx) error {
	actor, err := h.routeProviderMutator(c)
	if err != nil {
		return err
	}
	if h.routeProviders == nil || h.routeAuditor == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeProviderUnavailableReason)
	}
	var body routeProviderSelectRequest
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, routeProviderInvalidReason)
	}
	key := routeProviderKey(body.TargetRouteID, body.TargetContractVersion, body.Method, body.PathSignature)
	auditID, err := h.routeAuditor.AppendReturningID(c.Context(), audit.Event{
		ActorUserID: actor.ID, Action: audit.ActionRouteProviderSelect,
		Metadata: map[string]any{
			"targetRouteId": key.TargetRouteID, "targetContractVersion": key.TargetContractVersion,
			"method": key.Method, "pathSignature": key.PathSignature,
			"providerRouteId": body.ProviderRouteID, "providerContractVersion": body.ProviderContractVersion,
			"providerExtensionId":      body.ProviderArtifact.ExtensionID,
			"providerExtensionVersion": body.ProviderArtifact.ExtensionVersion,
			"providerPackageDigest":    body.ProviderArtifact.PackageDigest,
			"expectedRevision":         body.ExpectedRevision,
		},
	})
	if err != nil || auditID <= 0 {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeProviderUnavailableReason)
	}
	selection, err := h.routeProviders.Select(c.Context(), routes.SelectProviderRequest{
		Key: key, ProviderRouteID: strings.TrimSpace(body.ProviderRouteID),
		ProviderContractVersion: strings.TrimSpace(body.ProviderContractVersion),
		ProviderArtifact:        body.ProviderArtifact, ExpectedRevision: body.ExpectedRevision,
		ActorUserID: actor.ID, AuditEventID: auditID,
	})
	if err != nil {
		return mapRouteProviderError(err)
	}
	return apphttp.OK(c, selection)
}

func (h *Controller) resetRouteProvider(c fiber.Ctx) error {
	actor, err := h.routeProviderMutator(c)
	if err != nil {
		return err
	}
	if h.routeProviders == nil || h.routeAuditor == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeProviderUnavailableReason)
	}
	var body routeProviderResetRequest
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, routeProviderInvalidReason)
	}
	key := routeProviderKey(body.TargetRouteID, body.TargetContractVersion, body.Method, body.PathSignature)
	auditID, err := h.routeAuditor.AppendReturningID(c.Context(), audit.Event{
		ActorUserID: actor.ID, Action: audit.ActionRouteProviderReset,
		Metadata: map[string]any{
			"targetRouteId": key.TargetRouteID, "targetContractVersion": key.TargetContractVersion,
			"method": key.Method, "pathSignature": key.PathSignature,
			"expectedRevision": body.ExpectedRevision, "reasonCode": body.ReasonCode,
		},
	})
	if err != nil || auditID <= 0 {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeProviderUnavailableReason)
	}
	err = h.routeProviders.Reset(c.Context(), routes.ResetProviderRequest{
		Key: key, ExpectedRevision: body.ExpectedRevision, ActorUserID: actor.ID,
		AuditEventID: auditID, ReasonCode: strings.TrimSpace(body.ReasonCode),
	})
	if err != nil {
		return mapRouteProviderError(err)
	}
	return apphttp.OK(c, map[string]any{"reset": true, "key": key})
}

func (h *Controller) routeProviderViewer(c fiber.Ctx) (identity.Actor, error) {
	actor, err := h.actor(c)
	if err != nil {
		return identity.Actor{}, err
	}
	if !actor.Can(identity.PermissionExtensionView) && !actor.Can(identity.PermissionExtensionManage) {
		return identity.Actor{}, fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return actor, nil
}

func (h *Controller) routeProviderMutator(c fiber.Ctx) (identity.Actor, error) {
	actor, err := h.actor(c)
	if err != nil {
		return identity.Actor{}, err
	}
	if !actor.IsSuperAdmin() {
		return identity.Actor{}, fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return actor, nil
}

func routeProviderKeyFromQuery(c fiber.Ctx) routes.ProviderSelectionKey {
	return routeProviderKey(c.Query("targetRouteId"), c.Query("targetContractVersion"), c.Query("method"), c.Query("pathSignature"))
}

func routeProviderKey(routeID, contractVersion, method, pathSignature string) routes.ProviderSelectionKey {
	return routes.ProviderSelectionKey{
		TargetRouteID: strings.TrimSpace(routeID), TargetContractVersion: strings.TrimSpace(contractVersion),
		Method: strings.ToUpper(strings.TrimSpace(method)), PathSignature: strings.TrimSpace(pathSignature),
	}
}

func mapRouteProviderError(err error) error {
	switch {
	case errors.Is(err, routes.ErrProviderSelectionInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, routeProviderInvalidReason)
	case errors.Is(err, routes.ErrProviderSelectionNotFound):
		return fiber.NewError(fiber.StatusNotFound, routeProviderNotFoundReason)
	case errors.Is(err, routes.ErrProviderSelectionStale):
		return fiber.NewError(fiber.StatusConflict, routeProviderStaleReason)
	case errors.Is(err, routes.ErrProviderSelectionRevisionConflict),
		errors.Is(err, routes.ErrAmbiguousRoute), errors.Is(err, routes.ErrRevisionConflict):
		return fiber.NewError(fiber.StatusConflict, routeProviderConflictReason)
	default:
		return err
	}
}
