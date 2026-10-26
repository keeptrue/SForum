package bootstrap

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	httpserver "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

func loadSessionPolicyAwareRouteActor(
	c fiber.Ctx,
	sessions *authsession.Manager,
	users identity.ActorStore,
) (identity.Actor, error) {
	if hostLocalSessionAuthorityPath(c.Method(), c.Path()) {
		return httpserver.OptionalActorWithoutRenewal(c, sessions, users)
	}
	return httpserver.OptionalActor(c, sessions, users)
}

// Fiber routing is case-insensitive and non-strict by default, while the Route
// Registry intentionally keeps path identity case-sensitive. Only the four
// canonical Host recovery paths bypass renewal; plugin aliases never inherit
// this Host-only authority reduction.
func hostLocalSessionAuthorityPath(method, requestPath string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	requestPath = strings.TrimSpace(requestPath)
	if requestPath != "/" {
		requestPath = strings.TrimSuffix(requestPath, "/")
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, "/"), "/")
	segment := func(index int, value string) bool {
		return index < len(segments) && strings.EqualFold(segments[index], value)
	}
	if method == fiber.MethodPost && len(segments) == 4 &&
		segment(0, "api") && segment(1, "v1") && segment(2, "auth") && segment(3, "logout") {
		return true
	}
	if len(segments) == 5 && segment(0, "api") && segment(1, "v1") &&
		segment(2, "auth") && segment(3, "sessions") {
		return method == fiber.MethodDelete && segments[4] != "" ||
			method == fiber.MethodPost && segment(4, "revoke-others")
	}
	return method == fiber.MethodPost && len(segments) == 6 &&
		segment(0, "api") && segment(1, "v1") && segment(2, "users") && segments[3] != "" &&
		segment(4, "sessions") && segment(5, "revoke")
}
