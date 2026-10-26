package identitycontroller

import (
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	externalAuthBrowserCookieName = "sforum_ext_auth"
	externalAuthBrowserCookieTTL  = 30 * time.Minute
)

// ensureExternalAuthBrowserBinding creates a Host-only browser secret before
// leaving for the provider. Only its digest is persisted with callback state.
func (h *Controller) ensureExternalAuthBrowserBinding(c fiber.Ctx) (string, error) {
	raw := strings.TrimSpace(c.Cookies(externalAuthBrowserCookieName))
	if !validExternalAuthBrowserCookie(raw) {
		var err error
		raw, err = identity.GenerateOpaqueToken()
		if err != nil {
			return "", err
		}
	}
	secure := h.externalAuthCookieSecure()
	sameSite := fiber.CookieSameSiteLaxMode
	if secure {
		// OAuth may return through mobile browser redirect chains that classify the
		// callback as cross-site. None keeps the correlation cookie available while
		// Secure, HttpOnly, short TTL, and the stored digest retain its narrow scope.
		sameSite = fiber.CookieSameSiteNoneMode
	}
	c.Cookie(&fiber.Cookie{
		Name:     externalAuthBrowserCookieName,
		Value:    raw,
		Path:     "/",
		MaxAge:   int(externalAuthBrowserCookieTTL.Seconds()),
		Expires:  time.Now().Add(externalAuthBrowserCookieTTL),
		Secure:   secure,
		HTTPOnly: true,
		SameSite: sameSite,
	})
	return identity.ExternalAuthBrowserBindingDigest(raw), nil
}

func (h *Controller) matchesExternalAuthBrowserBinding(c fiber.Ctx, expectedDigest string) bool {
	raw := strings.TrimSpace(c.Cookies(externalAuthBrowserCookieName))
	return validExternalAuthBrowserCookie(raw) &&
		identity.ExternalAuthBrowserBindingDigest(raw) == strings.ToLower(strings.TrimSpace(expectedDigest))
}

func (h *Controller) matchesExternalAuthTicketBrowserBinding(c fiber.Ctx, ticket identity.RegistrationTicket) bool {
	// Registration-only tickets may have been issued by the immediately prior
	// deployment and cannot bind an existing account. Their ten-minute rolling
	// compatibility window does not weaken the login-continuation boundary.
	if ticket.Operation == identity.ExternalAuthOperationRegistration && strings.TrimSpace(ticket.BrowserBindingDigest) == "" {
		return true
	}
	return h.matchesExternalAuthBrowserBinding(c, ticket.BrowserBindingDigest)
}

func (h *Controller) externalAuthCookieSecure() bool {
	if strings.EqualFold(strings.TrimSpace(h.appEnv), "production") {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(h.appURL))
	return err == nil && strings.EqualFold(parsed.Scheme, "https")
}

func validExternalAuthBrowserCookie(value string) bool {
	if len(value) != 43 {
		return false
	}
	for index := range len(value) {
		char := value[index]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
