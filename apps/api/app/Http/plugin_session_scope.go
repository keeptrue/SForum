package http

import (
	"github.com/gofiber/fiber/v3"

	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// PluginSessionScopeSource resolves the Core session scope of the current
// request. It is nil when the assembly has no session authority, and a nil
// source means "no delegation is minted" — never "no isolation required".
type PluginSessionScopeSource func(fiber.Ctx) (authsession.RequestScope, bool)

// pluginSessionScopeMiddleware publishes the resolved scope on the request
// context so the Host's plugin transports can mint exactly one S9 session
// delegation per invocation they initiate.
//
// It is deliberately a separate middleware rather than a parameter of the route
// dispatcher: the scope is Host authority, and a request without one must keep
// behaving exactly as before.
func pluginSessionScopeMiddleware(scopes PluginSessionScopeSource) fiber.Handler {
	return func(c fiber.Ctx) error {
		if scopes != nil && c != nil {
			if scope, ok := scopes(c); ok {
				c.SetContext(authsession.WithPluginSessionScope(c.Context(), scope))
			}
		}
		return c.Next()
	}
}
