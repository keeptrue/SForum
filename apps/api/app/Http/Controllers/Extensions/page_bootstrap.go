package extensionscontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

// adminPageBootstrapResponse 在目录服务的解析结果之上附带前端信任结论。
//
// page-bootstrap 的语义就是"一次请求渲染这个管理页"，而 component / settings 视图
// 必须先知道信任状态才能决定首帧渲染什么。缺这个字段时前端只能把"尚未读到状态"
// 先画成"未授权"，管理端又是 SSR + 禁缓存，于是警告条会被直接写进服务端 HTML，
// 随后才被真实组件替换——即管理端扩展页的闪屏。
type adminPageBootstrapResponse struct {
	extensions.AdminPageBootstrap
	Frontend *extensions.FrontendStatus `json:"frontend"`
	// Host 让受信管理页组件在首帧就拿到宿主版本，不必再发一次请求；
	// 这是管理面，允许带 buildVersion/valid（公开面只有 coreVersion）。
	Host platformversion.HostIdentity `json:"host"`
}

func (h *Controller) pageBootstrap(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	result, err := h.service.AdminPageBootstrap(c.Context(), actor, c.Params("id"), c.Query("path"), apphttp.Locale(c))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, adminPageBootstrapResponse{
		AdminPageBootstrap: result,
		Frontend:           h.pageFrontendStatus(c, actor, result),
		Host:               platformversion.Identity(),
	})
}

// pageFrontendStatus 只在会挂载扩展 UI 的两个视图下附带信任结论。
// 任何失败（服务未装配、权限不足、信任存储不可读）都降级为 null，
// 绝不让 bootstrap 因为前端状态不可读而整体失败：前端会回落到自己的 /frontend 请求。
func (h *Controller) pageFrontendStatus(c fiber.Ctx, actor identity.Actor, result extensions.AdminPageBootstrap) *extensions.FrontendStatus {
	if h.frontend == nil || result.Page == nil {
		return nil
	}
	if view := result.Page.View; view != "component" && view != "settings" {
		return nil
	}
	status, err := h.frontend.Frontend(c.Context(), actor, result.Extension.ID)
	if err != nil {
		return nil
	}
	return &status
}
