package extensionscontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
)

// 公开声明目录（editor / entity / content / media）。
//
// 这四个目录是同一个责任：把进程内 Registry 只投影成一份声明清单，公开可读、
// 无登录、无执行权限。它们放在一起是因为共享同一组不变量——`no-store` 与
// `nosniff` 必须同时下发，digest 只在非空时回传，Registry 缺失时返回空集合而
// 不是错误。把它们分散在 controller.go 里时，这些不变量只能靠逐处比对才发现
// 是否一致。

// WithEditorRegistry wires the process-local Editor Registry for the public
// editor catalog used by SFEditor trusted L2 admission.
func (h *Controller) WithEditorRegistry(registry *editorregistry.Registry) *Controller {
	if h != nil {
		h.editorRegistry = registry
	}
	return h
}

// publicEditorCatalog 公开：投影当前 Editor Registry 为 sforum.editor-catalog@1。
// 无需登录；Safe Mode 下仅含 core modules。无 registry 时返回空 modules。
func (h *Controller) publicEditorCatalog(c fiber.Ctx) error {
	catalog := (*editorregistry.Registry)(nil).BuildCatalog()
	if h != nil && h.editorRegistry != nil {
		catalog = h.editorRegistry.BuildCatalog()
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	if catalog.Digest != "" {
		c.Set("X-SForum-Editor-Catalog-Digest", catalog.Digest)
	}
	return apphttp.OK(c, catalog)
}

// WithEntityRegistry wires the process-local Entity Registry for the public
// entity catalog (plan projections only; no durable row store).
func (h *Controller) WithEntityRegistry(registry *entityregistry.Registry) *Controller {
	if h != nil {
		h.entityRegistry = registry
	}
	return h
}

// publicEntityCatalog 公开：投影当前 Entity Registry 为 sforum.entity-catalog@1。
// 含 index/importExport/deletion plan 摘要；无 registry 时返回空 entities。
func (h *Controller) publicEntityCatalog(c fiber.Ctx) error {
	catalog := (*entityregistry.Registry)(nil).BuildCatalog()
	if h != nil && h.entityRegistry != nil {
		catalog = h.entityRegistry.BuildCatalog()
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	if catalog.Digest != "" {
		c.Set("X-SForum-Entity-Catalog-Digest", catalog.Digest)
	}
	return apphttp.OK(c, catalog)
}

// WithContentRegistry wires the process-local Content Registry for the public
// content catalog (declaration projection only; no execution bindings).
func (h *Controller) WithContentRegistry(registry *contentregistry.Registry) *Controller {
	if h != nil {
		h.contentRegistry = registry
	}
	return h
}

// publicContentCatalog 公开：投影当前 Content Registry 为 sforum.content-catalog@1。
// 含 block/shortcode/filter 等声明元数据；无 registry 时返回空 content。
func (h *Controller) publicContentCatalog(c fiber.Ctx) error {
	catalog := (*contentregistry.Registry)(nil).BuildCatalog()
	if h != nil && h.contentRegistry != nil {
		catalog = h.contentRegistry.BuildCatalog()
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	if catalog.Digest != "" {
		c.Set("X-SForum-Content-Catalog-Digest", catalog.Digest)
	}
	return apphttp.OK(c, catalog)
}

// WithMediaRegistry wires the process-local Media Registry for the public
// media catalog (declaration projection only; no plan/execute authority).
func (h *Controller) WithMediaRegistry(registry *mediaregistry.Registry) *Controller {
	if h != nil {
		h.mediaRegistry = registry
	}
	return h
}

// publicMediaCatalog 公开：投影当前 Media Registry 为 sforum.media-catalog@1。
// 含 MIME 策略 / processor / variant 元数据；无 registry 时返回空列表。
func (h *Controller) publicMediaCatalog(c fiber.Ctx) error {
	catalog := (*mediaregistry.Registry)(nil).BuildCatalog()
	if h != nil && h.mediaRegistry != nil {
		catalog = h.mediaRegistry.BuildCatalog()
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	if catalog.Digest != "" {
		c.Set("X-SForum-Media-Catalog-Digest", catalog.Digest)
	}
	return apphttp.OK(c, catalog)
}
