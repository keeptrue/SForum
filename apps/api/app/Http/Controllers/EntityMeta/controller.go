package entitymetacontroller

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	service  *entitymeta.Service
	users    identity.ActorStore
	sessions *authsession.Manager
}

func NewController(service *entitymeta.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	// 公开：某实体类型的已启用非 admin 字段目录。
	api.Get("/entity-meta/definitions", h.listPublicDefinitions)
	// 按可见性读取实体元数据（访客仅见 public）。
	api.Get("/entity-meta/:entityType/:entityID", h.listValues)
	// 写入：登录且对实体有写权。
	api.Put("/entity-meta/:entityType/:entityID", h.upsertValues)

	admin := api.Group("/admin/entity-meta")
	admin.Get("/definitions", h.listAdminDefinitions)
	admin.Post("/definitions", h.createDefinition)
	admin.Patch("/definitions/:fieldKey", h.updateDefinition)
	admin.Delete("/definitions/:fieldKey", h.deleteDefinition)
}

type createDefinitionBody struct {
	FieldKey    string            `json:"fieldKey"`
	EntityType  string            `json:"entityType"`
	ValueType   string            `json:"valueType"`
	Visibility  string            `json:"visibility"`
	Label       map[string]string `json:"label"`
	Description map[string]string `json:"description"`
	// ownerExtensionId 是宿主内部字段：只有设置面同步（SyncManifest）能写，
	// 管理端请求体不接受。它决定「谁能改这条定义」，接受外部输入等于把
	// 插件字段的所有权交给任何有 entity_meta.manage 的人。
	Required    bool           `json:"required"`
	Enabled     *bool          `json:"enabled"`
	SortOrder   *int           `json:"sortOrder"`
	Constraints map[string]any `json:"constraints"`
}

type updateDefinitionBody struct {
	Visibility  *string           `json:"visibility"`
	Label       map[string]string `json:"label"`
	Description map[string]string `json:"description"`
	// ownerExtensionId 同上：管理端不接受（插件定义本身也已拒绝 PATCH）。
	Required    *bool          `json:"required"`
	Enabled     *bool          `json:"enabled"`
	SortOrder   *int           `json:"sortOrder"`
	Constraints map[string]any `json:"constraints"`
}

type upsertValuesBody struct {
	Values []struct {
		FieldKey string `json:"fieldKey"`
		Value    any    `json:"value"`
		Clear    bool   `json:"clear"`
	} `json:"values"`
}

func (h *Controller) listPublicDefinitions(c fiber.Ctx) error {
	entityType := strings.TrimSpace(c.Query("entityType"))
	if entityType == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "entity_meta.invalid")
	}
	items, err := h.service.ListPublicDefinitions(c.Context(), entityType)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) listAdminDefinitions(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListDefinitions(c.Context(), actor, strings.TrimSpace(c.Query("entityType")))
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) createDefinition(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body createDefinitionBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "entity_meta.invalid")
	}
	item, err := h.service.CreateDefinition(c.Context(), actor, entitymeta.CreateFieldInput{
		FieldKey:        body.FieldKey,
		EntityType:      body.EntityType,
		ValueType:       body.ValueType,
		Visibility:      body.Visibility,
		LabelZHCN:       body.Label["zh-CN"],
		LabelENUS:       body.Label["en-US"],
		DescriptionZHCN: body.Description["zh-CN"],
		DescriptionENUS: body.Description["en-US"],
		Required:        body.Required,
		Enabled:         body.Enabled,
		SortOrder:       body.SortOrder,
		Constraints:     rawJSON(body.Constraints),
	})
	if err != nil {
		return mapError(err)
	}
	return apphttp.Created(c, item)
}

func (h *Controller) updateDefinition(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var body updateDefinitionBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "entity_meta.invalid")
	}
	input := entitymeta.UpdateFieldInput{
		Visibility: body.Visibility,
		Required:   body.Required,
		Enabled:    body.Enabled,
		SortOrder:  body.SortOrder,
	}
	if body.Label != nil {
		if v, ok := body.Label["zh-CN"]; ok {
			input.LabelZHCN = &v
		}
		if v, ok := body.Label["en-US"]; ok {
			input.LabelENUS = &v
		}
	}
	if body.Description != nil {
		if v, ok := body.Description["zh-CN"]; ok {
			input.DescriptionZHCN = &v
		}
		if v, ok := body.Description["en-US"]; ok {
			input.DescriptionENUS = &v
		}
	}
	if body.Constraints != nil {
		raw := json.RawMessage(rawJSON(body.Constraints))
		input.Constraints = &raw
	}
	item, err := h.service.UpdateDefinition(c.Context(), actor, c.Params("fieldKey"), input)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) deleteDefinition(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if err := h.service.DeleteDefinition(c.Context(), actor, c.Params("fieldKey")); err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, map[string]any{"deleted": true})
}

func (h *Controller) listValues(c fiber.Ctx) error {
	actor, err := h.readActor(c)
	if err != nil {
		return err
	}
	entityType := c.Params("entityType")
	entityID, err := strconv.ParseInt(c.Params("entityID"), 10, 64)
	if err != nil || entityID <= 0 {
		return fiber.NewError(fiber.StatusNotFound, "entity_meta.entity_not_found")
	}
	items, err := h.service.ListValues(c.Context(), actor, entityType, entityID)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) upsertValues(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	entityType := c.Params("entityType")
	entityID, err := strconv.ParseInt(c.Params("entityID"), 10, 64)
	if err != nil || entityID <= 0 {
		return fiber.NewError(fiber.StatusNotFound, "entity_meta.entity_not_found")
	}
	var body upsertValuesBody
	if err := c.Bind().Body(&body); err != nil || len(body.Values) == 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "entity_meta.invalid")
	}
	inputs := make([]entitymeta.UpsertValueInput, 0, len(body.Values))
	for _, item := range body.Values {
		if item.Clear {
			inputs = append(inputs, entitymeta.UpsertValueInput{FieldKey: item.FieldKey, Value: nil})
			continue
		}
		inputs = append(inputs, entitymeta.UpsertValueInput{FieldKey: item.FieldKey, Value: item.Value})
	}
	items, err := h.service.UpsertValues(c.Context(), actor, entityType, entityID, inputs)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	// 写路径：必须登录，但「解析不出来」仍要分类 —— 主体不存在/不可用 → 401，
	// 会话或身份存储不可用 → 503。旧实现在 error 上直接返回，Redis 抖动时是 500。
	return apphttp.RequireActor(c, h.sessions, h.users)
}

// readActor 是元数据读路径的主体门。
//
// 这里保留「不降级成匿名」的立场：匿名可见集合比登录集合更宽，静默降级等于把受限
// 字段读成公开字段。但「拿不到可信主体」不是一种原因——它至少分成两类，答案不同：
//
//	主体不存在/不可用（ErrUserNotFound、disabled/banned）→ 401 auth.required
//	会话或身份存储不可用（Redis/PG/超时/未识别）      → 503 service.not_ready
//
// 分类逻辑在 apphttp.ReadActor（app/Http/auth_read.go）。此前这里是「任何 error 一律
// 401」，于是 Redis 抖动会被答成「你的登录无效」——与 OPS2-01 立的规则自相矛盾。
// 另外：「凭据不可解析」在本仓不会以 error 出现（会话层把它折成 (0,false,nil)），
// 所以它和「没有凭据」一样走零值 Actor + 200 匿名视角，而不是 401。
func (h *Controller) readActor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.ReadActor(c, h.sessions, h.users)
}

func mapError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied), errors.Is(err, entitymeta.ErrPermission):
		return fiber.NewError(fiber.StatusForbidden, "auth.forbidden")
	case errors.Is(err, entitymeta.ErrNotFound):
		return fiber.NewError(fiber.StatusNotFound, "entity_meta.not_found")
	case errors.Is(err, entitymeta.ErrEntityNotFound):
		return fiber.NewError(fiber.StatusNotFound, "entity_meta.entity_not_found")
	case errors.Is(err, entitymeta.ErrFieldDisabled):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "entity_meta.field_disabled")
	case errors.Is(err, entitymeta.ErrInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "entity_meta.invalid")
	default:
		return err
	}
}

func rawJSON(m map[string]any) []byte {
	if m == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}
