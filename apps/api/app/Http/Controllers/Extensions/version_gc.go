package extensionscontroller

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// VersionGCManagementService 是版本 GC 管理端入口的最小读写边界（任务书阶段
// 5）；nil 时路由 fail-closed 为 503。实现方（Models/Extensions 的
// VersionGCAdminService）负责 super_admin 强制与 actor 绑定——守卫层只是粗门，
// 这里的服务策略才是权威。
type VersionGCManagementService interface {
	Candidates(ctx context.Context, actor identity.Actor, options extensions.VersionGCOptions) ([]extensions.RetiredVersionCandidate, error)
	CreateConfirmation(ctx context.Context, actor identity.Actor, options extensions.VersionGCOptions) (extensions.VersionGCConfirmation, error)
	Execute(ctx context.Context, actor identity.Actor, options extensions.VersionGCOptions, token string) (extensions.VersionGCSweepResult, error)
}

// WithVersionGC 注入版本 GC 管理端边界（阶段 5）；生产装配无条件注入，
// nil 时对应路由 fail-closed 为 503。
func (h *Controller) WithVersionGC(service VersionGCManagementService) *Controller {
	h.versionGC = service
	return h
}

// versionGCOptionsBody 承载保留策略与执行确认参数；零值由 store 归一化
// （retain<=0 → 默认 1，retainDays<0 → 0），负数在入口即拒绝。
type versionGCOptionsBody struct {
	Retain      int    `json:"retain"`
	RetainDays  int    `json:"retainDays"`
	ExtensionID string `json:"extensionId"`
	Token       string `json:"token"`
}

// versionGCCandidates 输出回收候选清单（含保留/阻塞原因）。只读、零副作用；
// 确认 token 由 POST /confirmations 单独签发，GET 永远不铸造 token。
func (h *Controller) versionGcCandidates(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.versionGC == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeVersionGCUnavailable)
	}
	options, err := versionGCOptionsFromQuery(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	candidates, err := h.versionGC.Candidates(c.Context(), actor, options)
	if err != nil {
		return mapVersionGCError(err)
	}
	return apphttp.OK(c, fiber.Map{"candidates": candidates})
}

// createVersionGcConfirmation 等价 CLI 的 dry-run 签发：一次性 token 与
// "选项 + 精确候选集"计划摘要绑定，明文只出现一次。
func (h *Controller) createVersionGcConfirmation(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.versionGC == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeVersionGCUnavailable)
	}
	var input versionGCOptionsBody
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	options, err := input.options()
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	confirmation, err := h.versionGC.CreateConfirmation(c.Context(), actor, options)
	if err != nil {
		return mapVersionGCError(err)
	}
	return apphttp.OK(c, confirmation)
}

// executeVersionGCSweep 消费一次性 token 并执行两段回收。存在失败条目时仍
// 返回 200 与完整逐条结果——删除动作本身已按条原子完成，失败条目台账保留。
func (h *Controller) executeVersionGcSweep(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.versionGC == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeVersionGCUnavailable)
	}
	var input versionGCOptionsBody
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	options, err := input.options()
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	if strings.TrimSpace(input.Token) == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	result, err := h.versionGC.Execute(c.Context(), actor, options, input.Token)
	if err != nil {
		return mapVersionGCError(err)
	}
	return apphttp.OK(c, result)
}

func versionGCOptionsFromQuery(c fiber.Ctx) (extensions.VersionGCOptions, error) {
	retain, err := versionGCQueryInt(c, "retain")
	if err != nil {
		return extensions.VersionGCOptions{}, err
	}
	retainDays, err := versionGCQueryInt(c, "retainDays")
	if err != nil {
		return extensions.VersionGCOptions{}, err
	}
	if retain < 0 || retainDays < 0 {
		return extensions.VersionGCOptions{}, errors.New("negative retention")
	}
	return extensions.VersionGCOptions{
		Retain:      retain,
		RetainDays:  retainDays,
		ExtensionID: strings.TrimSpace(c.Query("extensionId")),
	}, nil
}

func versionGCQueryInt(c fiber.Ctx, name string) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func (b versionGCOptionsBody) options() (extensions.VersionGCOptions, error) {
	if b.Retain < 0 || b.RetainDays < 0 {
		return extensions.VersionGCOptions{}, errors.New("negative retention")
	}
	return extensions.VersionGCOptions{
		Retain:      b.Retain,
		RetainDays:  b.RetainDays,
		ExtensionID: strings.TrimSpace(b.ExtensionID),
	}, nil
}

func mapVersionGCError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		return mapExtensionError(err)
	case errors.Is(err, extensions.ErrVersionGCConfirmationInvalid):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeVersionGCConfirmationInvalid)
	case errors.Is(err, extensions.ErrVersionGCInvalidInput):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	default:
		return mapExtensionError(err)
	}
}
