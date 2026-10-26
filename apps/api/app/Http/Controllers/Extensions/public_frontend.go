package extensionscontroller

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// publicFrontendPagePolicy 公开：按页面即将挂载的 L2 soft refs 聚合 Host CSP。
// 使用 GET + 可重复 component=extensionId/componentId，避免 SSR 路径触发 CSRF。
// 空列表在 public L2 门禁通过时返回 Host 基线 document policy。
func (h *Controller) publicFrontendPagePolicy(c fiber.Ctx) error {
	runtime, ok := h.frontend.(PublicFrontendRuntimeService)
	if h.frontend == nil || !ok {
		return publicFrontendNotFound()
	}
	refs, err := parsePublicPagePolicyComponentQuery(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "extensions.public_page_policy_invalid")
	}
	policy, err := runtime.PublicPagePolicyForComponents(c.Context(), refs)
	if err != nil {
		return mapPublicPagePolicyError(err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	// 文档级 CSP 摘要便于 Nuxt/CDN 观测；权威值仍是 body.documentPolicy.headerValue。
	if policy.DocumentPolicy.Digest != "" {
		c.Set("X-SForum-Document-Policy-Digest", policy.DocumentPolicy.Digest)
	}
	return apphttp.OK(c, policy)
}

func parsePublicPagePolicyComponentQuery(c fiber.Ctx) ([]extensions.PublicFrontendComponentRef, error) {
	rawValues := c.Request().URI().QueryArgs().PeekMulti("component")
	if len(rawValues) == 0 {
		// Fiber 单值查询回退：component=a/b
		if single := strings.TrimSpace(c.Query("component")); single != "" {
			rawValues = [][]byte{[]byte(single)}
		}
	}
	if len(rawValues) > 256 {
		return nil, errors.New("too many components")
	}
	refs := make([]extensions.PublicFrontendComponentRef, 0, len(rawValues))
	seen := make(map[string]struct{}, len(rawValues))
	for _, raw := range rawValues {
		value := strings.TrimSpace(string(raw))
		if value == "" {
			return nil, errors.New("empty component")
		}
		extensionID, componentID, ok := strings.Cut(value, "/")
		extensionID = strings.TrimSpace(extensionID)
		componentID = strings.TrimSpace(componentID)
		if !ok || extensionID == "" || componentID == "" || strings.Contains(componentID, "/") {
			return nil, errors.New("invalid component ref")
		}
		key := extensionID + "\x00" + componentID
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, extensions.PublicFrontendComponentRef{
			ExtensionID: extensionID, ComponentID: componentID,
		})
	}
	return refs, nil
}

func mapPublicPagePolicyError(err error) error {
	var policyErr *extensions.PublicPagePolicyError
	if errors.As(err, &policyErr) {
		switch policyErr.Code {
		case extensions.PublicPagePolicyInvalidInput:
			return fiber.NewError(fiber.StatusUnprocessableEntity, "extensions.public_page_policy_invalid")
		case extensions.PublicPagePolicyRuntimeUnavailable,
			extensions.PublicPagePolicyComponentUnavailable,
			extensions.PublicPagePolicyTrustUnavailable,
			extensions.PublicPagePolicyDependencyInvalid,
			extensions.PublicPagePolicyDirectiveInvalid,
			extensions.PublicPagePolicyBoundsExceeded,
			extensions.PublicPagePolicySnapshotChanged:
			// 与 public component 一致：对公开调用方折叠为 404，避免泄漏信任/制品状态。
			return publicFrontendNotFound()
		}
	}
	return mapPublicFrontendError(err)
}

func (h *Controller) publicFrontendComponent(c fiber.Ctx) error {
	runtime, ok := h.frontend.(PublicFrontendRuntimeService)
	if h.frontend == nil || !ok {
		return publicFrontendNotFound()
	}
	descriptor, err := runtime.PublicComponent(c.Context(), c.Params("extensionId"), c.Params("componentId"))
	if err != nil {
		return mapPublicFrontendError(err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	return apphttp.OK(c, descriptor)
}

func (h *Controller) publicFrontendAsset(c fiber.Ctx) error {
	runtime, ok := h.frontend.(PublicFrontendRuntimeService)
	if h.frontend == nil || !ok {
		return publicFrontendNotFound()
	}
	packageDigest := c.Params("packageDigest")
	digest := c.Params("digest")
	if !frontendDigestPattern.MatchString(packageDigest) || !frontendDigestPattern.MatchString(digest) {
		return publicFrontendNotFound()
	}
	asset, err := runtime.PublicAsset(
		c.Context(), c.Params("extensionId"), packageDigest, digest, c.Params("handle"),
	)
	if err != nil {
		return mapPublicFrontendError(err)
	}
	c.Set(fiber.HeaderContentType, asset.ContentType)
	c.Set(fiber.HeaderCacheControl, "public, max-age=31536000, immutable")
	c.Set(fiber.HeaderETag, asset.ETag)
	c.Set("X-Content-Type-Options", "nosniff")
	// 公开只读资产（hash 定址、immutable）：允许跨源读取，避免 QQ X5 云加速、
	// CDN 等换源链路按 CORP 拒载。私有 frontend 资产仍保持 same-origin。
	c.Set("Cross-Origin-Resource-Policy", "cross-origin")
	c.Set("X-SForum-Asset-Digest", asset.Digest)
	c.Set("X-SForum-Asset-Integrity", asset.Integrity)
	return c.Send(asset.Body)
}

func (h *Controller) publicFrontendPackageAsset(c fiber.Ctx) error {
	runtime, ok := h.frontend.(PublicFrontendRuntimeService)
	if h.frontend == nil || !ok {
		return publicFrontendNotFound()
	}
	packageDigest := c.Params("packageDigest")
	if !frontendDigestPattern.MatchString(packageDigest) {
		return publicFrontendNotFound()
	}
	asset, err := runtime.PublicPackageAsset(
		c.Context(), c.Params("extensionId"), packageDigest, c.Params("*"),
	)
	if err != nil {
		return mapPublicFrontendError(err)
	}
	c.Set(fiber.HeaderContentType, asset.ContentType)
	c.Set(fiber.HeaderCacheControl, "public, max-age=31536000, immutable")
	c.Set(fiber.HeaderETag, asset.ETag)
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Cross-Origin-Resource-Policy", "cross-origin")
	c.Set("X-Frame-Options", "DENY")
	c.Set("X-SForum-Asset-Digest", asset.Digest)
	c.Set("X-SForum-Asset-Integrity", asset.Integrity)
	return c.Send(asset.Body)
}

func mapPublicFrontendError(err error) error {
	// Public callers cannot distinguish disabled, revoked, stale, changed, or
	// undeclared artifacts. The exact trust inspector remains admin-only.
	if errors.Is(err, extensions.ErrPublicFrontendUnavailable) ||
		errors.Is(err, extensions.ErrFrontendPackageChanged) ||
		errors.Is(err, extensions.ErrExtensionNotFound) {
		return publicFrontendNotFound()
	}
	return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeFrontendRuntimeUnavailable)
}

func publicFrontendNotFound() error {
	return fiber.NewError(fiber.StatusNotFound, extensions.CodeFrontendTrustNotFound)
}
