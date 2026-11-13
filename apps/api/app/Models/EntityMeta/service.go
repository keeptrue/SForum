package entitymeta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

var fieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,63}$`)

type Service struct {
	store     Store
	publisher appevents.Publisher
	// fieldPolicy 是插件字段的读写策略（nil = 不过滤），见 policy.go。
	fieldPolicy FieldPolicy
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) WithPublisher(publisher appevents.Publisher) *Service {
	s.publisher = publisher
	return s
}

func (s *Service) ListDefinitions(ctx context.Context, actor identity.Actor, entityType string) ([]FieldDefinition, error) {
	if err := requireManage(actor); err != nil {
		return nil, err
	}
	if entityType != "" && !validEntityType(entityType) {
		return nil, ErrInvalid
	}
	rows, err := s.store.ListDefinitions(ctx, entityType)
	if err != nil {
		return nil, err
	}
	return mapDefinitions(rows), nil
}

// ListPublicDefinitions 返回某实体类型已启用字段的公开目录（无需登录）。
func (s *Service) ListPublicDefinitions(ctx context.Context, entityType string) ([]FieldDefinition, error) {
	if !validEntityType(entityType) {
		return nil, ErrInvalid
	}
	rows, err := s.store.ListDefinitions(ctx, entityType)
	if err != nil {
		return nil, err
	}
	out := make([]FieldDefinition, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		// 公开目录不含 admin 可见性字段（避免泄露内部 schema）。
		if row.Visibility == VisibilityAdmin {
			continue
		}
		out = append(out, toDefinition(row))
	}
	return out, nil
}

func (s *Service) CreateDefinition(ctx context.Context, actor identity.Actor, input CreateFieldInput) (FieldDefinition, error) {
	if err := requireManage(actor); err != nil {
		return FieldDefinition{}, err
	}
	// ownerExtensionId 是宿主内部字段：只有设置面同步能写。管理端能设它，
	// 就等于能把任意插件的字段所有权提前占掉（那个插件从此每次 enable 都
	// ErrPreflightFailed，而定义又改不了、删不掉）。
	if strings.TrimSpace(input.OwnerExtensionID) != "" {
		return FieldDefinition{}, ErrPermission
	}
	row, err := normalizeCreate(input)
	if err != nil {
		return FieldDefinition{}, err
	}
	created, err := s.store.CreateDefinition(ctx, row)
	if err != nil {
		return FieldDefinition{}, err
	}
	return toDefinition(created), nil
}

func (s *Service) UpdateDefinition(ctx context.Context, actor identity.Actor, fieldKey string, input UpdateFieldInput) (FieldDefinition, error) {
	if err := requireManage(actor); err != nil {
		return FieldDefinition{}, err
	}
	fieldKey = strings.TrimSpace(fieldKey)
	existing, err := s.store.GetDefinitionByKey(ctx, fieldKey)
	if err != nil {
		return FieldDefinition{}, err
	}
	// 插件字段的唯一真相是 manifest + 同步：管理端改完会被下一次 sync 抹掉，
	// 所以这里直接拒绝（前端把这类行渲染成只读）。
	if existing.OwnerExtensionID != "" {
		return FieldDefinition{}, ErrPermission
	}
	// 反方向也拒：宿主/运营手建字段不能被「认领」成插件字段，否则它会立刻
	// 变成只读且无法删除。
	if input.OwnerExtensionID != nil && strings.TrimSpace(*input.OwnerExtensionID) != "" {
		return FieldDefinition{}, ErrPermission
	}
	applyUpdate(&existing, input)
	if !validVisibility(existing.Visibility) {
		return FieldDefinition{}, ErrInvalid
	}
	updated, err := s.store.UpdateDefinition(ctx, fieldKey, existing)
	if err != nil {
		return FieldDefinition{}, err
	}
	return toDefinition(updated), nil
}

func (s *Service) DeleteDefinition(ctx context.Context, actor identity.Actor, fieldKey string) error {
	if err := requireManage(actor); err != nil {
		return err
	}
	fieldKey = strings.TrimSpace(fieldKey)
	// 插件字段由包声明拥有：删掉它等于让下次 enable 的同步重建一行空字段，
	// 期间用户已填的值会被孤儿化。停用插件才是正确操作。
	if existing, err := s.store.GetDefinitionByKey(ctx, fieldKey); err == nil && existing.OwnerExtensionID != "" {
		return ErrPermission
	}
	return s.store.DeleteDefinition(ctx, fieldKey)
}

// EnsureOwnerExtensionField 是宿主内部使用的字段 upsert（插件 enable 时把
// manifest 声明投影进 entity-meta）。它不要求 actor：同步是宿主动作，不是某个
// 运营的运营操作。
//
// 幂等是硬要求——插件每次 enable 都会调用它：
//   - 字段不存在则建，存在则只刷新声明（label/描述/可见性/必填/顺序/约束）
//   - 绝不接管不属于该扩展的字段（运营手建字段、别的扩展的字段）
//   - 绝不改存储类型（会让已存值无法解析），也不删任何值
//
// enabled 跟随插件状态：enable 时为 true，disable/uninstall 由
// SetEnabledByOwnerExtension 整体翻成 false。运营对单个 tab/字段的开关在
// settings_surface_items 里，不在这张字段表上。
func (s *Service) EnsureOwnerExtensionField(ctx context.Context, input CreateFieldInput) (FieldDefinition, error) {
	row, err := normalizeCreate(input)
	if err != nil {
		return FieldDefinition{}, err
	}
	if strings.TrimSpace(row.OwnerExtensionID) == "" {
		return FieldDefinition{}, ErrInvalid
	}
	existing, err := s.store.GetDefinitionByKey(ctx, row.FieldKey)
	if errors.Is(err, ErrNotFound) {
		created, err := s.store.CreateDefinition(ctx, row)
		if err != nil {
			return FieldDefinition{}, err
		}
		return toDefinition(created), nil
	}
	if err != nil {
		return FieldDefinition{}, err
	}
	if existing.OwnerExtensionID != row.OwnerExtensionID {
		return FieldDefinition{}, ErrPermission
	}
	if existing.EntityType != row.EntityType {
		return FieldDefinition{}, ErrInvalid
	}
	if existing.ValueType != row.ValueType {
		return FieldDefinition{}, ErrInvalid
	}
	existing.Visibility = row.Visibility
	existing.LabelZHCN = row.LabelZHCN
	existing.LabelENUS = row.LabelENUS
	existing.DescriptionZHCN = row.DescriptionZHCN
	existing.DescriptionENUS = row.DescriptionENUS
	existing.Required = row.Required
	existing.Enabled = row.Enabled
	existing.SortOrder = row.SortOrder
	existing.Constraints = row.Constraints
	updated, err := s.store.UpdateDefinition(ctx, row.FieldKey, existing)
	if err != nil {
		return FieldDefinition{}, err
	}
	return toDefinition(updated), nil
}

// ListOwnerExtensionDefinitions 返回某扩展拥有的字段定义（宿主动作，不需要 actor）。
// 差集清扫用它找出「本次声明里已经没有」的旧字段。
func (s *Service) ListOwnerExtensionDefinitions(ctx context.Context, extensionID string) ([]FieldDefinition, error) {
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return nil, ErrInvalid
	}
	rows, err := s.store.ListDefinitions(ctx, EntityUser)
	if err != nil {
		return nil, err
	}
	out := make([]FieldDefinition, 0, len(rows))
	for _, row := range rows {
		if row.OwnerExtensionID != extensionID {
			continue
		}
		out = append(out, toDefinition(row))
	}
	return out, nil
}

// SetEnabledByOwnerField 停用/启用单个插件字段（差集清扫）。只翻 enabled。
func (s *Service) SetEnabledByOwnerField(ctx context.Context, fieldKey string, enabled bool) error {
	fieldKey = strings.TrimSpace(fieldKey)
	if fieldKey == "" {
		return ErrInvalid
	}
	return s.store.SetEnabledDefinition(ctx, fieldKey, enabled)
}

// SetEnabledByOwnerExtension 在插件 disable/uninstall 时整体停用该扩展的字段。
// 已存值一律保留。
func (s *Service) SetEnabledByOwnerExtension(ctx context.Context, extensionID string, enabled bool) error {
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return ErrInvalid
	}
	return s.store.SetEnabledByOwnerExtension(ctx, extensionID, enabled)
}

// ListValues 按可见性过滤后返回实体元数据。
func (s *Service) ListValues(ctx context.Context, actor identity.Actor, entityType string, entityID int64) ([]MetaValue, error) {
	if !validEntityType(entityType) || entityID <= 0 {
		return nil, ErrInvalid
	}
	exists, err := s.store.EntityExists(ctx, entityType, entityID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrEntityNotFound
	}
	defs, err := s.store.ListDefinitions(ctx, entityType)
	if err != nil {
		return nil, err
	}
	values, err := s.store.ListValues(ctx, entityType, entityID)
	if err != nil {
		return nil, err
	}
	valueByKey := make(map[string]valueRow, len(values))
	for _, v := range values {
		valueByKey[v.FieldKey] = v
	}
	ownerID, err := s.entityOwnerID(ctx, entityType, entityID)
	if err != nil {
		return nil, err
	}
	out := make([]MetaValue, 0, len(defs))
	for _, def := range defs {
		if !def.Enabled {
			continue
		}
		if !canReadField(actor, def.Visibility, ownerID) {
			continue
		}
		if !s.allowsRead(ctx, actor, toDefinition(def)) {
			continue
		}
		row, ok := valueByKey[def.FieldKey]
		if !ok {
			continue
		}
		parsed, err := parseStoredValue(def.ValueType, row.ValueText)
		if err != nil {
			continue
		}
		out = append(out, MetaValue{
			FieldKey:   def.FieldKey,
			EntityType: entityType,
			EntityID:   entityID,
			ValueType:  def.ValueType,
			Value:      parsed,
			Visibility: def.Visibility,
			Label:      labelMap(def.LabelZHCN, def.LabelENUS),
			UpdatedAt:  row.UpdatedAt,
		})
	}
	return out, nil
}

// UpsertValues 批量写入；nil 值删除该字段。
func (s *Service) UpsertValues(ctx context.Context, actor identity.Actor, entityType string, entityID int64, inputs []UpsertValueInput) ([]MetaValue, error) {
	if !validEntityType(entityType) || entityID <= 0 {
		return nil, ErrInvalid
	}
	if !actor.IsActive() {
		return nil, identity.ErrPermissionDenied
	}
	exists, err := s.store.EntityExists(ctx, entityType, entityID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrEntityNotFound
	}
	ownerID, err := s.entityOwnerID(ctx, entityType, entityID)
	if err != nil {
		return nil, err
	}
	if !canWriteEntity(actor, entityType, ownerID) {
		return nil, identity.ErrPermissionDenied
	}

	changed := make([]string, 0, len(inputs))
	for _, input := range inputs {
		fieldKey := strings.TrimSpace(input.FieldKey)
		if fieldKey == "" {
			return nil, ErrInvalid
		}
		def, err := s.store.GetDefinitionByKey(ctx, fieldKey)
		if err != nil {
			return nil, err
		}
		if def.EntityType != entityType {
			return nil, ErrInvalid
		}
		if !def.Enabled {
			return nil, ErrFieldDisabled
		}
		if !canWriteField(actor, def.Visibility, ownerID) {
			return nil, identity.ErrPermissionDenied
		}
		// 治理关闭 / 当前不可见的插件字段不许写：否则「关掉」只是看不见。
		if !s.allowsWrite(ctx, actor, toDefinition(def)) {
			return nil, ErrPermission
		}
		if input.Value == nil {
			if def.Required {
				return nil, ErrInvalid
			}
			if err := s.store.DeleteValue(ctx, entityType, entityID, fieldKey); err != nil {
				return nil, err
			}
			changed = append(changed, fieldKey)
			continue
		}
		text, err := normalizeValue(def.ValueType, input.Value, def.Constraints)
		if err != nil {
			return nil, err
		}
		uid := actor.ID
		if _, err := s.store.UpsertValue(ctx, valueRow{
			EntityType:      entityType,
			EntityID:        entityID,
			FieldKey:        fieldKey,
			ValueText:       text,
			UpdatedByUserID: &uid,
		}); err != nil {
			return nil, err
		}
		changed = append(changed, fieldKey)
	}

	if len(changed) > 0 && s.publisher != nil {
		_ = s.publisher.Emit(ctx, appevents.NewEnvelope(appevents.EntityMetaUpdated, map[string]any{
			"entityType":  entityType,
			"entityId":    entityID,
			"fieldKeys":   changed,
			"actorUserId": actor.ID,
		}))
	}
	return s.ListValues(ctx, actor, entityType, entityID)
}

func (s *Service) entityOwnerID(ctx context.Context, entityType string, entityID int64) (int64, error) {
	switch entityType {
	case EntityUser:
		return entityID, nil
	case EntityTopic:
		authorID, ok, err := s.store.TopicAuthorID(ctx, entityID)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, ErrEntityNotFound
		}
		return authorID, nil
	default:
		return 0, ErrInvalid
	}
}

func requireManage(actor identity.Actor) error {
	if !actor.IsActive() || !actor.Can(identity.PermissionEntityMetaManage) {
		return identity.ErrPermissionDenied
	}
	return nil
}

func canWriteEntity(actor identity.Actor, entityType string, ownerID int64) bool {
	if actor.Can(identity.PermissionEntityMetaManage) {
		return true
	}
	switch entityType {
	case EntityUser:
		return actor.ID == ownerID || actor.Can(identity.PermissionUserManage)
	case EntityTopic:
		if actor.Can(identity.PermissionTopicEditAny) {
			return true
		}
		return actor.ID == ownerID && actor.Can(identity.PermissionTopicEditOwn)
	default:
		return false
	}
}

func canReadField(actor identity.Actor, visibility string, ownerID int64) bool {
	switch visibility {
	case VisibilityPublic:
		return true
	case VisibilityOwner:
		if actor.IsActive() && (actor.ID == ownerID || actor.Can(identity.PermissionEntityMetaManage) || actor.Can(identity.PermissionUserManage)) {
			return true
		}
		return false
	case VisibilityAdmin:
		return actor.IsActive() && (actor.Can(identity.PermissionEntityMetaManage) || actor.IsSuperAdmin())
	default:
		return false
	}
}

func canWriteField(actor identity.Actor, visibility string, ownerID int64) bool {
	if actor.Can(identity.PermissionEntityMetaManage) {
		return true
	}
	switch visibility {
	case VisibilityPublic, VisibilityOwner:
		return actor.ID == ownerID
	case VisibilityAdmin:
		return false
	default:
		return false
	}
}

func normalizeCreate(input CreateFieldInput) (fieldRow, error) {
	key := strings.TrimSpace(input.FieldKey)
	if !fieldKeyPattern.MatchString(key) {
		return fieldRow{}, ErrInvalid
	}
	entityType := strings.TrimSpace(input.EntityType)
	valueType := strings.TrimSpace(input.ValueType)
	visibility := strings.TrimSpace(input.Visibility)
	if visibility == "" {
		visibility = VisibilityPublic
	}
	if !validEntityType(entityType) || !validValueType(valueType) || !validVisibility(visibility) {
		return fieldRow{}, ErrInvalid
	}
	labelZH := strings.TrimSpace(input.LabelZHCN)
	labelEN := strings.TrimSpace(input.LabelENUS)
	if labelZH == "" && labelEN == "" {
		return fieldRow{}, ErrInvalid
	}
	if labelZH == "" {
		labelZH = labelEN
	}
	if labelEN == "" {
		labelEN = labelZH
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	sortOrder := 100
	if input.SortOrder != nil {
		sortOrder = *input.SortOrder
	}
	constraints := input.Constraints
	if len(constraints) == 0 {
		constraints = json.RawMessage(`{}`)
	}
	if !json.Valid(constraints) {
		return fieldRow{}, ErrInvalid
	}
	return fieldRow{
		FieldKey:         key,
		EntityType:       entityType,
		ValueType:        valueType,
		Visibility:       visibility,
		LabelZHCN:        labelZH,
		LabelENUS:        labelEN,
		DescriptionZHCN:  strings.TrimSpace(input.DescriptionZHCN),
		DescriptionENUS:  strings.TrimSpace(input.DescriptionENUS),
		OwnerExtensionID: strings.TrimSpace(input.OwnerExtensionID),
		Required:         input.Required,
		Enabled:          enabled,
		SortOrder:        sortOrder,
		Constraints:      constraints,
	}, nil
}

func applyUpdate(row *fieldRow, input UpdateFieldInput) {
	if input.Visibility != nil {
		row.Visibility = strings.TrimSpace(*input.Visibility)
	}
	if input.LabelZHCN != nil {
		row.LabelZHCN = strings.TrimSpace(*input.LabelZHCN)
	}
	if input.LabelENUS != nil {
		row.LabelENUS = strings.TrimSpace(*input.LabelENUS)
	}
	if input.DescriptionZHCN != nil {
		row.DescriptionZHCN = strings.TrimSpace(*input.DescriptionZHCN)
	}
	if input.DescriptionENUS != nil {
		row.DescriptionENUS = strings.TrimSpace(*input.DescriptionENUS)
	}
	if input.OwnerExtensionID != nil {
		row.OwnerExtensionID = strings.TrimSpace(*input.OwnerExtensionID)
	}
	if input.Required != nil {
		row.Required = *input.Required
	}
	if input.Enabled != nil {
		row.Enabled = *input.Enabled
	}
	if input.SortOrder != nil {
		row.SortOrder = *input.SortOrder
	}
	if input.Constraints != nil && len(*input.Constraints) > 0 && json.Valid(*input.Constraints) {
		row.Constraints = *input.Constraints
	}
}

func normalizeValue(valueType string, raw any, constraints []byte) (string, error) {
	switch valueType {
	case ValueBoolean:
		switch v := raw.(type) {
		case bool:
			if v {
				return "true", nil
			}
			return "false", nil
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "1", "yes":
				return "true", nil
			case "false", "0", "no":
				return "false", nil
			}
		}
		return "", ErrInvalid
	case ValueNumber:
		var n float64
		switch v := raw.(type) {
		case float64:
			n = v
		case float32:
			n = float64(v)
		case int:
			n = float64(v)
		case int64:
			n = float64(v)
		case json.Number:
			f, err := v.Float64()
			if err != nil {
				return "", ErrInvalid
			}
			n = f
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return "", ErrInvalid
			}
			n = f
		default:
			return "", ErrInvalid
		}
		if err := checkNumberConstraints(n, constraints); err != nil {
			return "", err
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case ValueString, ValueText:
		var text string
		switch v := raw.(type) {
		case string:
			text = v
		default:
			text = fmt.Sprint(v)
		}
		if valueType == ValueString {
			text = strings.TrimSpace(text)
		}
		if err := checkStringConstraints(text, constraints); err != nil {
			return "", err
		}
		return text, nil
	default:
		return "", ErrInvalid
	}
}

func parseStoredValue(valueType, text string) (any, error) {
	switch valueType {
	case ValueBoolean:
		return text == "true", nil
	case ValueNumber:
		return strconv.ParseFloat(text, 64)
	case ValueString, ValueText:
		return text, nil
	default:
		return nil, ErrInvalid
	}
}

type valueConstraints struct {
	MaxLength *int     `json:"maxLength"`
	Min       *float64 `json:"min"`
	Max       *float64 `json:"max"`
	// Enum 是字符串字段的封闭取值集合。它存在的理由是 select 类字段：
	// 只靠前端下拉框约束等于没有约束，写入路径必须自己拒掉集合外的值。
	Enum []string `json:"enum"`
}

func parseConstraints(raw []byte) valueConstraints {
	var c valueConstraints
	if len(raw) == 0 {
		return c
	}
	_ = json.Unmarshal(raw, &c)
	return c
}

func checkStringConstraints(text string, raw []byte) error {
	c := parseConstraints(raw)
	if c.MaxLength != nil && len([]rune(text)) > *c.MaxLength {
		return ErrInvalid
	}
	if len(c.Enum) > 0 && !constraintEnumAllows(c.Enum, text) {
		return ErrInvalid
	}
	// 默认安全上限，防止无约束大文本。
	if c.MaxLength == nil && len([]rune(text)) > 8000 {
		return ErrInvalid
	}
	return nil
}

func constraintEnumAllows(allowed []string, text string) bool {
	for _, candidate := range allowed {
		if candidate == text {
			return true
		}
	}
	return false
}

func checkNumberConstraints(n float64, raw []byte) error {
	c := parseConstraints(raw)
	if c.Min != nil && n < *c.Min {
		return ErrInvalid
	}
	if c.Max != nil && n > *c.Max {
		return ErrInvalid
	}
	return nil
}

func validEntityType(v string) bool {
	return v == EntityUser || v == EntityTopic
}

func validValueType(v string) bool {
	switch v {
	case ValueString, ValueText, ValueNumber, ValueBoolean:
		return true
	default:
		return false
	}
}

func validVisibility(v string) bool {
	switch v {
	case VisibilityPublic, VisibilityOwner, VisibilityAdmin:
		return true
	default:
		return false
	}
}

func mapDefinitions(rows []fieldRow) []FieldDefinition {
	out := make([]FieldDefinition, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDefinition(row))
	}
	return out
}

func toDefinition(row fieldRow) FieldDefinition {
	constraints := json.RawMessage(row.Constraints)
	if len(constraints) == 0 {
		constraints = json.RawMessage(`{}`)
	}
	return FieldDefinition{
		ID:               row.ID,
		FieldKey:         row.FieldKey,
		EntityType:       row.EntityType,
		ValueType:        row.ValueType,
		Visibility:       row.Visibility,
		Label:            labelMap(row.LabelZHCN, row.LabelENUS),
		Description:      labelMap(row.DescriptionZHCN, row.DescriptionENUS),
		OwnerExtensionID: row.OwnerExtensionID,
		Required:         row.Required,
		Enabled:          row.Enabled,
		SortOrder:        row.SortOrder,
		Constraints:      constraints,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func labelMap(zh, en string) map[string]string {
	out := map[string]string{}
	if zh != "" {
		out["zh-CN"] = zh
	}
	if en != "" {
		out["en-US"] = en
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
