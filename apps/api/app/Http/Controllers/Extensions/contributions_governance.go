package extensionscontroller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// SettingsSurfaceGovernanceService 是设置面治理的策略入口（Providers 层实现）。
//
// 控制器只做路径/请求体解码与错误翻译：谁能改、改的是哪一行、恢复默认是什么意思，
// 全部在实现里裁决。
type SettingsSurfaceGovernanceService interface {
	Contributions(ctx context.Context, actor identity.Actor) ([]settingssurface.ContributionView, error)
	SurfaceTabs(ctx context.Context, actor identity.Actor, locale string) ([]settingssurface.SurfaceTab, error)
	Mutate(ctx context.Context, actor identity.Actor, input settingssurface.MutateInput) (settingssurface.OverrideView, error)
	Reset(ctx context.Context, actor identity.Actor, key settingssurface.OverrideKey) error
}

// WithSettingsSurfaceGovernance 注入设置面治理能力（链式可选）。
// 未注入时治理端点返回 503，贡献列表退回未加工形状——装配漏接必须显形，
// 而不是悄悄给出「所有 tab 都启用、顺序都等于声明顺序」的假读模型。
func (h *Controller) WithSettingsSurfaceGovernance(service SettingsSurfaceGovernanceService) *Controller {
	if h != nil {
		h.settingsSurface = service
	}
	return h
}

// governanceRequest 是治理写入的请求体。
//
// sortOrder 用 RawMessage 保留三态：没传（不改）、null（清掉顺序覆盖）、数字（设定）。
// 只有两态实现的话，改顺序会顺手把运营关掉的 tab 重新打开。
type governanceRequest struct {
	Enabled   *bool           `json:"enabled"`
	SortOrder json.RawMessage `json:"sortOrder"`
}

func (r governanceRequest) mutateInput(key settingssurface.OverrideKey) (settingssurface.MutateInput, error) {
	input := settingssurface.MutateInput{Key: key, Enabled: r.Enabled}
	raw := strings.TrimSpace(string(r.SortOrder))
	if raw == "" {
		return input, nil
	}
	input.SortOrderSet = true
	if raw == "null" {
		return input, nil
	}
	var order int
	if err := json.Unmarshal(r.SortOrder, &order); err != nil {
		return settingssurface.MutateInput{}, err
	}
	input.SortOrder = &order
	return input, nil
}

// surfaceTabs 返回站点设置页上的插件 tab（type=settings 的 tab 内联设置文档）。
func (h *Controller) surfaceTabs(c fiber.Ctx) error {
	if h.settingsSurface == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	tabs, err := h.settingsSurface.SurfaceTabs(c.Context(), actor, apphttp.Locale(c))
	if err != nil {
		return mapSettingsSurfaceGovernanceError(err)
	}
	if tabs == nil {
		tabs = []settingssurface.SurfaceTab{}
	}
	return apphttp.OK(c, tabs)
}

func (h *Controller) updateContributionGovernance(c fiber.Ctx) error {
	return h.mutateContributionGovernance(c, "")
}

func (h *Controller) resetContributionGovernance(c fiber.Ctx) error {
	return h.resetContributionGovernanceTarget(c, "")
}

func (h *Controller) updateContributionItemGovernance(c fiber.Ctx) error {
	return h.mutateContributionGovernance(c, c.Params("itemKey"))
}

func (h *Controller) resetContributionItemGovernance(c fiber.Ctx) error {
	return h.resetContributionGovernanceTarget(c, c.Params("itemKey"))
}

func (h *Controller) mutateContributionGovernance(c fiber.Ctx, itemKey string) error {
	if h.settingsSurface == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req governanceRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	key, err := settingsSurfaceTargetKey(c, itemKey)
	if err != nil {
		return err
	}
	input, err := req.mutateInput(key)
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	view, err := h.settingsSurface.Mutate(c.Context(), actor, input)
	if err != nil {
		return mapSettingsSurfaceGovernanceError(err)
	}
	return apphttp.OK(c, view)
}

func (h *Controller) resetContributionGovernanceTarget(c fiber.Ctx, itemKey string) error {
	if h.settingsSurface == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	key, err := settingsSurfaceTargetKey(c, itemKey)
	if err != nil {
		return err
	}
	if err := h.settingsSurface.Reset(c.Context(), actor, key); err != nil {
		return mapSettingsSurfaceGovernanceError(err)
	}
	// 恢复默认是幂等的：返回 204 而不是回显「现在长这样」，
	// 读模型（GET /admin/extensions/contributions）才是唯一事实来源。
	return c.SendStatus(fiber.StatusNoContent)
}

// settingsSurfaceTargetKey 解析治理目标。
//
// itemKey 两种入参都接受：组合键（x.<extension>.<key>，前端下发的字段键）与裸键
// （manifest 声明的键）。落库统一存裸键，换算只走 ExtensionManifest 的那一个函数。
func settingsSurfaceTargetKey(c fiber.Ctx, itemKey string) (settingssurface.OverrideKey, error) {
	point := strings.TrimSpace(c.Params("point"))
	contributionID := strings.TrimSpace(c.Params("contributionId"))
	if point == "" || contributionID == "" {
		return settingssurface.OverrideKey{}, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	// extensionId 可选：contributionId 在生效声明里唯一时由服务层解析；
	// 声明已被删除（悬空覆盖行）时用它定位。
	extensionID := strings.TrimSpace(c.Query("extensionId"))
	itemKey = strings.ToLower(strings.TrimSpace(itemKey))
	if segment, bare, ok := extensionmanifest.SettingsSurfaceSplitFieldKey(itemKey); ok {
		itemKey = bare
		if extensionID == "" {
			extensionID = segment
		}
	}
	return settingssurface.OverrideKey{
		ExtensionID:    extensionID,
		Point:          point,
		ContributionID: contributionID,
		ItemKey:        itemKey,
	}, nil
}

func mapSettingsSurfaceGovernanceError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, settingssurface.ErrUnknownContribution),
		errors.Is(err, settingssurface.ErrUnknownItem),
		errors.Is(err, settingssurface.ErrInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	default:
		return mapExtensionError(err)
	}
}
