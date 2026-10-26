package extensionscontroller

import (
	"regexp"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var frontendDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (h *Controller) frontendStatus(c fiber.Ctx) error {
	if h.frontend == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeFrontendRuntimeUnavailable)
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	status, err := h.frontend.Frontend(c.Context(), actor, c.Params("id"))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, status)
}

func (h *Controller) frontendAsset(c fiber.Ctx) error {
	assets, ok := h.frontend.(TrustedFrontendAssetService)
	if h.frontend == nil || !ok {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeFrontendRuntimeUnavailable)
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	digest := c.Params("digest")
	if !frontendDigestPattern.MatchString(digest) {
		return fiber.NewError(fiber.StatusNotFound, extensions.CodeFrontendTrustNotFound)
	}
	asset, err := assets.Asset(c.Context(), actor, c.Params("id"), digest, c.Params("asset"))
	if err != nil {
		return mapExtensionError(err)
	}
	return sendFrontendAsset(c, asset)
}

func (h *Controller) frontendComponentAsset(c fiber.Ctx) error {
	assets, ok := h.frontend.(TrustedFrontendComponentAssetService)
	if h.frontend == nil || !ok {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeFrontendRuntimeUnavailable)
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	digest := c.Params("digest")
	if !frontendDigestPattern.MatchString(digest) {
		return fiber.NewError(fiber.StatusNotFound, extensions.CodeFrontendTrustNotFound)
	}
	asset, err := assets.ComponentAsset(c.Context(), actor, c.Params("id"), digest, c.Params("component"), c.Params("asset"))
	if err != nil {
		return mapExtensionError(err)
	}
	return sendFrontendAsset(c, asset)
}

func sendFrontendAsset(c fiber.Ctx, asset extensions.FrontendAsset) error {
	c.Set(fiber.HeaderContentType, asset.ContentType)
	c.Set(fiber.HeaderCacheControl, "private, max-age=31536000, immutable")
	c.Set(fiber.HeaderETag, asset.ETag)
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Cross-Origin-Resource-Policy", "same-origin")
	return c.Send(asset.Body)
}

func (h *Controller) frontendConfirmation(c fiber.Ctx) error {
	challenges, ok := h.frontend.(TrustedFrontendChallengeService)
	if h.frontend == nil || !ok {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeFrontendRuntimeUnavailable)
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	challenge, err := challenges.Challenge(c.Context(), actor, c.Params("id"))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, challenge)
}

func (h *Controller) grantFrontendTrust(c fiber.Ctx) error {
	if h.frontend == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeFrontendRuntimeUnavailable)
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input extensions.GrantFrontendInput
	if err := c.Bind().Body(&input); err != nil || !frontendDigestPattern.MatchString(input.Digest) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, extensions.CodeFrontendDigestInvalid)
	}
	status, err := h.frontend.Grant(c.Context(), actor, c.Params("id"), input)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, status)
}

func (h *Controller) revokeFrontendTrust(c fiber.Ctx) error {
	if h.frontend == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeFrontendRuntimeUnavailable)
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	status, err := h.frontend.Revoke(c.Context(), actor, c.Params("id"))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, status)
}
