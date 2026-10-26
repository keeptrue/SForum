package identitycontroller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const maximumRoleSuggestionPageSize = 100

type roleSuggestionDecisionRequest struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	ApprovalState    string `json:"approvalState"`
}

// WithIdentityRegistryStore binds the Host-owned durable review repository.
// An unbound controller keeps the feature fail-closed through the Model service.
func (h *Controller) WithIdentityRegistryStore(store identityregistry.Store) *Controller {
	if h != nil && h.service != nil {
		h.service.WithIdentityRegistryStore(store)
	}
	return h
}

func (h *Controller) listRoleSuggestions(c fiber.Ctx) error {
	actor, err := h.roleSuggestionCookieActor(c)
	if err != nil {
		return err
	}

	limit, err := roleSuggestionLimit(c.Query("limit"))
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "identity.role_suggestion.invalid")
	}
	page, err := h.service.ListRoleSuggestionPage(c.Context(), actor, identityregistry.RoleSuggestionPageInput{
		Filter: identityregistry.RoleSuggestionFilter{
			ApprovalState:    c.Query("approvalState"),
			RoleKey:          c.Query("roleKey"),
			PermissionKey:    c.Query("permissionKey"),
			OwnerExtensionID: c.Query("ownerExtensionId"),
			Limit:            limit,
		},
		Cursor: c.Query("cursor"),
	})
	if err != nil {
		return mapRoleSuggestionError(err)
	}
	return apphttp.OK(c, page)
}

func (h *Controller) decideRoleSuggestion(c fiber.Ctx) error {
	actor, err := h.roleSuggestionCookieActor(c)
	if err != nil {
		return err
	}

	suggestionID, err := strconv.ParseInt(c.Params("suggestionID"), 10, 64)
	if err != nil || suggestionID <= 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "identity.role_suggestion.invalid")
	}
	var request roleSuggestionDecisionRequest
	if err := decodeRoleSuggestionDecision(c.Body(), &request); err != nil ||
		request.ExpectedRevision <= 0 || !validRoleSuggestionApprovalState(request.ApprovalState) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "identity.role_suggestion.invalid")
	}

	suggestion, err := h.service.DecideRoleSuggestion(c.Context(), actor, identity.RoleSuggestionDecisionInput{
		ID:               suggestionID,
		ExpectedRevision: request.ExpectedRevision,
		ApprovalState:    request.ApprovalState,
	})
	if err != nil {
		return mapRoleSuggestionError(err)
	}
	return apphttp.OK(c, suggestion)
}

// Role suggestion approval is deliberately cookie-bound. A PAT with role.manage
// remains machine authority and cannot turn a plugin recommendation into a grant.
func (h *Controller) roleSuggestionCookieActor(c fiber.Ctx) (identity.Actor, error) {
	if h == nil || h.authSessions == nil || h.service == nil {
		return identity.Actor{}, fiber.NewError(fiber.StatusServiceUnavailable, "identity.registry_unavailable")
	}
	// Bearer middleware runs before the Controller. Reject its presence even when
	// the same request also carries a valid browser cookie; otherwise that mixed
	// request could use PAT's CSRF exemption while exercising cookie authority.
	if apitokens.TokenIDFromContext(c.Context()) > 0 {
		return identity.Actor{}, fiber.NewError(fiber.StatusForbidden, "identity.role_suggestion.cookie_required")
	}
	userID, ok, err := h.authSessions.CurrentUserID(c)
	if err != nil {
		return identity.Actor{}, err
	}
	if !ok {
		return identity.Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	actor, err := h.service.Actor(c.Context(), userID)
	if err != nil {
		return identity.Actor{}, mapIdentityError(err)
	}
	return actor, nil
}

func roleSuggestionLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maximumRoleSuggestionPageSize {
		return 0, identityregistry.ErrInvalid
	}
	return value, nil
}

func validRoleSuggestionApprovalState(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case identityregistry.RoleSuggestionApproved, identityregistry.RoleSuggestionRejected:
		return true
	default:
		return false
	}
}

func decodeRoleSuggestionDecision(body []byte, target *roleSuggestionDecisionRequest) error {
	if len(bytes.TrimSpace(body)) == 0 || target == nil {
		return identityregistry.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return identityregistry.ErrInvalid
	}
	return nil
}

func mapRoleSuggestionError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied), errors.Is(err, identityregistry.ErrUnauthorized):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, identity.ErrIdentityRegistryUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, "identity.registry_unavailable")
	case errors.Is(err, identityregistry.ErrInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "identity.role_suggestion.invalid")
	case errors.Is(err, identityregistry.ErrNotFound):
		return fiber.NewError(fiber.StatusNotFound, "identity.role_suggestion.not_found")
	case errors.Is(err, identityregistry.ErrRevisionConflict):
		return fiber.NewError(fiber.StatusConflict, "identity.role_suggestion.revision_conflict")
	case errors.Is(err, identityregistry.ErrStale):
		return fiber.NewError(fiber.StatusConflict, "identity.role_suggestion.stale")
	case errors.Is(err, identityregistry.ErrTargetConflict):
		return fiber.NewError(fiber.StatusConflict, "identity.role_suggestion.target_unavailable")
	default:
		return err
	}
}
