package webhookscontroller

import (
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	webhooks "github.com/keeptrue/sforum/apps/api/app/Models/Webhooks"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

type Controller struct {
	service  *webhooks.Service
	users    identity.ActorStore
	sessions *authsession.Manager
}

func NewController(service *webhooks.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	admin := api.Group("/admin/webhooks")
	admin.Get("/endpoints", h.listEndpoints)
	admin.Post("/endpoints", h.createEndpoint)
	admin.Patch("/endpoints/:endpointID", h.updateEndpoint)
	admin.Delete("/endpoints/:endpointID", h.deleteEndpoint)
	admin.Get("/deliveries", h.listDeliveries)
	admin.Get("/events", h.listCatalogEvents)

	// 入站 gateway 骨架（F3.3）：插件后续可挂 verify/parse；当前仅校验信封并回执。
	api.Post("/webhooks/inbound/:source", h.inbound)
}

type endpointBody struct {
	Name      string   `json:"name"`
	TargetURL string   `json:"targetUrl"`
	Secret    string   `json:"secret"`
	Events    []string `json:"events"`
	Enabled   *bool    `json:"enabled"`
	// Description 用指针区分「省略」与「显式清空」；PATCH 省略时保留原值。
	Description *string `json:"description"`
	ClearSecret bool    `json:"clearSecret"`
}

func (h *Controller) listEndpoints(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListEndpoints(c.Context(), actor)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) createEndpoint(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body endpointBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "webhook.invalid")
	}
	desc := ""
	if body.Description != nil {
		desc = *body.Description
	}
	item, err := h.service.CreateEndpoint(c.Context(), actor, webhooks.CreateEndpointInput{
		Name: body.Name, TargetURL: body.TargetURL, Secret: body.Secret,
		Events: body.Events, Enabled: body.Enabled, Description: desc,
	})
	if err != nil {
		return mapError(err)
	}
	return apphttp.Created(c, item)
}

func (h *Controller) updateEndpoint(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Params("endpointID"), 10, 64)
	if err != nil || id <= 0 {
		return fiber.NewError(fiber.StatusNotFound, "webhook.not_found")
	}
	var body endpointBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "webhook.invalid")
	}
	input := webhooks.UpdateEndpointInput{ClearSecret: body.ClearSecret}
	if strings.TrimSpace(body.Name) != "" {
		name := body.Name
		input.Name = &name
	}
	if strings.TrimSpace(body.TargetURL) != "" {
		url := body.TargetURL
		input.TargetURL = &url
	}
	if body.Secret != "" {
		secret := body.Secret
		input.Secret = &secret
	}
	if body.Events != nil {
		input.Events = body.Events
	}
	input.Enabled = body.Enabled
	// 仅当 JSON 显式包含 description 时更新；nil 表示省略并保留。
	if body.Description != nil {
		desc := *body.Description
		input.Description = &desc
	}
	item, err := h.service.UpdateEndpoint(c.Context(), actor, id, input)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) deleteEndpoint(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Params("endpointID"), 10, 64)
	if err != nil || id <= 0 {
		return fiber.NewError(fiber.StatusNotFound, "webhook.not_found")
	}
	if err := h.service.DeleteEndpoint(c.Context(), actor, id); err != nil {
		return mapError(err)
	}
	return apphttp.NoData(c)
}

func (h *Controller) listDeliveries(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var endpointID int64
	if raw := c.Query("endpointId"); raw != "" {
		endpointID, _ = strconv.ParseInt(raw, 10, 64)
	}
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	items, err := h.service.ListDeliveries(c.Context(), actor, endpointID, limit)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) listCatalogEvents(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	// 与同组 endpoints/deliveries 一致：仅设置管理权限可见，避免向普通用户暴露内部事件目录。
	if !actor.Can(identity.PermissionSettingsManage) && !actor.Can(identity.PermissionSettingsSiteManage) {
		return mapError(identity.ErrPermissionDenied)
	}
	// 仅列出 observe 事件供管理员勾选订阅。
	defs := appevents.Definitions()
	items := make([]map[string]any, 0, len(defs))
	for _, def := range defs {
		if def.Kind != appevents.KindObserve {
			continue
		}
		items = append(items, map[string]any{
			"name": def.Name, "description": def.Description, "kind": def.Kind,
		})
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

// MaxInboundBodyBytes 入站 webhook 单请求体上限。
//
// 这个端点是全仓唯一「无鉴权 + 主动读请求体」的公开写端点。服务端虽然开了
// StreamRequestBody，但 HTTP_BODY_LIMIT 默认 64MB —— 只要处理器调用 c.Body()，
// 匿名请求就能把每个请求的内存拉到全局上限。所以这里按自己的上限读流：先看
// Content-Length 快速拒绝，再按 limit+1 字节截断读取，绝不把 body 复制成整块。
const MaxInboundBodyBytes int64 = 1 << 20

// inbound 入站骨架：记录来源并返回 accepted。
// 插件 verify/parse 钩子将在后续扩展；当前拒绝空 body，避免被当开放中继。
func (h *Controller) inbound(c fiber.Ctx) error {
	source := strings.TrimSpace(c.Params("source"))
	if source == "" || len(source) > 64 {
		return fiber.NewError(fiber.StatusBadRequest, "webhook.inbound_invalid")
	}
	if declared := int64(c.Request().Header.ContentLength()); declared > MaxInboundBodyBytes {
		return rejectInboundBody(c)
	}
	size, tooLarge, err := inboundBodySize(c)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "webhook.inbound_invalid")
	}
	if tooLarge {
		return rejectInboundBody(c)
	}
	if size == 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "webhook.inbound_empty")
	}
	// v1：仅回执，不执行业务副作用。插件 hook 接入点见知识库 F3.3。
	return apphttp.OK(c, map[string]any{
		"accepted": true,
		"source":   source,
		"bytes":    size,
		"note":     "inbound gateway skeleton; plugin verify/parse hooks not yet wired",
	})
}

// rejectInboundBody 拒绝超限 body。
//
// 超过上限的请求体不会被消费完，fasthttp 会把剩下的字节当成下一条请求去解析。
// 所以这里显式关闭连接：413 之后不允许 keep-alive 复用这条连接。
func rejectInboundBody(c fiber.Ctx) error {
	c.Set(fiber.HeaderConnection, "close")
	return fiber.NewError(fiber.StatusRequestEntityTooLarge, "webhook.inbound_too_large")
}

// inboundBodySize 在流式请求体上按上限计数；流已被上游消费时退回已缓冲切片。
//
// 正常情况下（core-only 目标）dispatcher 不读体，这里走流式分支；只有该路径被
// 插件计划包裹时上游才会预读请求体，此时仍要能回执而不是误报空 body。
//
// 口径：插件接管该路径后，1 MiB 上限退化为**事后判定**（读的是已缓冲切片，
// 内存峰值由 dispatcher 的整包预读决定）；拒绝语义（413/422）不变。
func inboundBodySize(c fiber.Ctx) (int64, bool, error) {
	stream := c.Request().BodyStream()
	if stream == nil {
		buffered := int64(len(c.Request().Body()))
		return buffered, buffered > MaxInboundBodyBytes, nil
	}
	return countInboundBody(stream, MaxInboundBodyBytes)
}

// countInboundBody 按 limit 读取流，最多消费 limit+1 字节即停。
// 返回的 n 是实际读取长度；tooLarge 表示请求体超过上限。
//
// stream 为 nil 时说明请求体已被服务器缓冲（未启用流式请求的部署或测试环境），
// 此时调用方仍须先做 Content-Length 预检；这里返回 0 表示「没有可读流」。
func countInboundBody(stream io.Reader, limit int64) (n int64, tooLarge bool, err error) {
	if stream == nil || limit <= 0 {
		return 0, false, nil
	}
	written, copyErr := io.Copy(io.Discard, io.LimitReader(stream, limit+1))
	if copyErr != nil {
		return written, false, copyErr
	}
	return written, written > limit, nil
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.RequireActor(c, h.sessions, h.users)
}

func mapError(err error) error {
	switch {
	case errors.Is(err, webhooks.ErrEndpointNotFound), errors.Is(err, webhooks.ErrDeliveryNotFound):
		return fiber.NewError(fiber.StatusNotFound, "webhook.not_found")
	case errors.Is(err, webhooks.ErrInvalidEndpoint), errors.Is(err, webhooks.ErrInvalidURL):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "webhook.invalid")
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	default:
		return err
	}
}
