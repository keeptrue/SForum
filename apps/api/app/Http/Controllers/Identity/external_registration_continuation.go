package identitycontroller

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// createExternalIdentityContinuation is the only ticket issuer for both
// explicit provider registration and the unlinked-login choice flow.
func (h *Controller) createExternalIdentityContinuation(
	c fiber.Ctx,
	assertion identity.ExternalAuthAssertion,
	returnPath string,
	browserBindingDigest string,
) (string, error) {
	if h.externalAuthService == nil || h.registrationTicketStore == nil {
		return "", identity.ErrExternalAuthProviderUnavailable
	}
	if assertion.Operation == identity.ExternalAuthOperationLogin &&
		!h.matchesExternalAuthBrowserBinding(c, browserBindingDigest) {
		return "", identity.ErrCallbackStateInvalid
	}
	if browserBindingDigest != "" && !h.matchesExternalAuthBrowserBinding(c, browserBindingDigest) {
		return "", identity.ErrCallbackStateInvalid
	}
	switch assertion.Operation {
	case identity.ExternalAuthOperationLogin:
		if _, err := h.externalAuthService.ValidateLoginContinuation(c.Context(), assertion); err != nil {
			return "", err
		}
	case identity.ExternalAuthOperationRegistration:
		if err := h.externalAuthService.ValidateRegistrationContinuation(c.Context(), assertion); err != nil {
			return "", err
		}
	default:
		return "", identity.ErrExternalAuthOperationMismatch
	}
	token, err := identity.GenerateOpaqueToken()
	if err != nil {
		return "", identity.ErrExternalAuthProviderUnavailable
	}
	now := time.Now()
	ticket := identity.RegistrationTicket{
		Token:                   token,
		ProviderID:              assertion.ProviderID,
		ProviderContractVersion: assertion.ProviderContractVersion,
		OwnerExtensionID:        assertion.OwnerExtensionID,
		OwnerExtensionVersion:   assertion.OwnerExtensionVersion,
		OwnerPackageDigest:      assertion.OwnerPackageDigest,
		Operation:               assertion.Operation,
		SourceOperation:         assertion.Operation,
		BrowserBindingDigest:    browserBindingDigest,
		ProviderSubject:         assertion.ProviderSubject,
		SubjectDigest:           assertion.SubjectDigest,
		UsernameHint:            assertion.UsernameHint,
		DisplayName:             assertion.DisplayName,
		EmailHint:               assertion.EmailHint,
		EmailVerified:           assertion.EmailVerified,
		CorrelationID:           assertion.CorrelationID,
		CreatedAt:               now,
		ExpiresAt:               now.Add(identity.RegistrationTicketDefaultTTL),
	}
	if err := h.registrationTicketStore.Save(c.Context(), ticket); err != nil {
		return "", identity.ErrExternalAuthProviderUnavailable
	}
	if assertion.Operation == identity.ExternalAuthOperationLogin {
		return identity.ExternalAuthContinuationPath(token, returnPath), nil
	}
	return identity.ExternalRegistrationContinuationPath(token, returnPath), nil
}

// externalAuthContinuationPreparation returns the independently authorized
// choices plus provider-owned presentation metadata. Inspect is non-consuming.
func (h *Controller) externalAuthContinuationPreparation(c fiber.Ctx) error {
	if h.externalAuthService == nil || h.registrationTicketStore == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	ticket, err := h.inspectBoundExternalAuthTicket(c)
	if err != nil {
		return err
	}
	preparation, err := h.externalAuthService.PrepareExternalAuthContinuation(c.Context(), ticket)
	if err != nil {
		return mapExternalAuthRegistrationError(err)
	}
	response := externalAuthContinuationPreparationResponse{
		ProviderID: preparation.ProviderID, UsernameHint: preparation.UsernameHint,
		DisplayName: preparation.DisplayName, EmailHint: preparation.EmailHint,
		EmailVerified: preparation.EmailVerified, CanLinkExisting: preparation.CanLinkExisting,
		CanRegister: preparation.CanRegister,
	}
	if h.providerCatalog != nil {
		if contribution, resolveErr := h.providerCatalog.ResolveProvider(ticket.ProviderID); resolveErr == nil {
			response.ProviderLabel = identityregistry.ResolveProviderLabel(contribution.Provider, apphttp.Locale(c))
			response.ProviderIcon = strings.TrimSpace(contribution.Icon)
		}
	}
	return apphttp.OK(c, response)
}

// externalAuthContinuationLink consumes one login assertion and binds it to the
// current recently-authenticated session. No provider OAuth call is repeated.
func (h *Controller) externalAuthContinuationLink(c fiber.Ctx) error {
	if h.externalAuthService == nil || h.registrationTicketStore == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	ticket, err := h.inspectBoundExternalAuthTicket(c)
	if err != nil {
		return err
	}
	assertion := externalAuthAssertionFromTicket(ticket)
	fingerprint := h.currentSessionFingerprint(c)
	if _, err := h.externalAuthService.AuthorizeExistingAccountContinuation(
		c.Context(), assertion, actor.ID, fingerprint,
	); err != nil {
		return mapExternalAuthError(err)
	}
	consumed, err := h.registrationTicketStore.Consume(c.Context(), ticket.Token)
	if err != nil {
		return mapRegistrationTicketError(err)
	}
	if !h.matchesExternalAuthTicketBrowserBinding(c, consumed) {
		return mapRegistrationTicketError(identity.ErrRegistrationTicketInvalid)
	}
	result, err := h.externalAuthService.CompleteAuthenticatedContinuation(
		c.Context(), externalAuthAssertionFromTicket(consumed), actor.ID, fingerprint,
	)
	if err != nil {
		return mapExternalAuthError(err)
	}
	return apphttp.OK(c, result.User)
}

// externalRegistrationPreparation returns only editable, redacted form hints.
// Inspect does not consume the ticket; submission remains the one-use boundary.
func (h *Controller) externalRegistrationPreparation(c fiber.Ctx) error {
	if h.externalAuthService == nil || h.registrationTicketStore == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	ticket, err := h.inspectBoundExternalAuthTicket(c)
	if err != nil {
		return err
	}
	preparation, err := h.externalAuthService.PrepareExternalRegistration(c.Context(), ticket)
	if err != nil {
		return mapExternalAuthRegistrationError(err)
	}
	return apphttp.OK(c, preparation)
}

func (h *Controller) inspectBoundExternalAuthTicket(c fiber.Ctx) (identity.RegistrationTicket, error) {
	var req externalRegistrationPreparationRequest
	if err := c.Bind().Body(&req); err != nil || strings.TrimSpace(req.Ticket) == "" {
		return identity.RegistrationTicket{}, fiber.NewError(fiber.StatusNotFound, "auth.external_registration_ticket_invalid")
	}
	ticket, err := h.registrationTicketStore.Inspect(c.Context(), strings.TrimSpace(req.Ticket))
	if err != nil {
		return identity.RegistrationTicket{}, mapRegistrationTicketError(err)
	}
	if !h.matchesExternalAuthTicketBrowserBinding(c, ticket) {
		return identity.RegistrationTicket{}, mapRegistrationTicketError(identity.ErrRegistrationTicketInvalid)
	}
	return ticket, nil
}

func externalAuthAssertionFromTicket(ticket identity.RegistrationTicket) identity.ExternalAuthAssertion {
	return identity.ExternalAuthAssertion{
		ProviderID: ticket.ProviderID, ProviderContractVersion: ticket.ProviderContractVersion,
		OwnerExtensionID: ticket.OwnerExtensionID, OwnerExtensionVersion: ticket.OwnerExtensionVersion,
		OwnerPackageDigest: ticket.OwnerPackageDigest, Operation: ticket.SourceOperation,
		SourceOperation: ticket.SourceOperation, ProviderSubject: ticket.ProviderSubject,
		SubjectDigest: ticket.SubjectDigest, UsernameHint: ticket.UsernameHint,
		DisplayName: ticket.DisplayName, EmailHint: ticket.EmailHint,
		EmailVerified: ticket.EmailVerified, CorrelationID: ticket.CorrelationID,
	}
}

type externalRegistrationPreparationRequest struct {
	Ticket string `json:"ticket"`
}

type externalAuthContinuationPreparationResponse struct {
	ProviderID      string `json:"providerId"`
	ProviderLabel   string `json:"providerLabel"`
	ProviderIcon    string `json:"providerIcon"`
	UsernameHint    string `json:"usernameHint"`
	DisplayName     string `json:"displayName"`
	EmailHint       string `json:"emailHint"`
	EmailVerified   bool   `json:"emailVerified"`
	CanLinkExisting bool   `json:"canLinkExisting"`
	CanRegister     bool   `json:"canRegister"`
}

func mapRegistrationContinuationReason(err error) string {
	switch {
	case errors.Is(err, identity.ErrExternalAuthBootstrapRequired):
		return "auth.external_bootstrap_required"
	case errors.Is(err, identity.ErrRegistrationDisabled):
		return "auth.registration_disabled"
	case errors.Is(err, identity.ErrExternalAuthOperationNotActivated),
		errors.Is(err, identity.ErrAuthProviderNotFound):
		return "auth.provider_not_enabled"
	default:
		return mapExternalAuthReason(err)
	}
}
