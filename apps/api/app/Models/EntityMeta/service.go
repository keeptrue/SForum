package entitymeta

import (
	"context"
	"encoding/json"
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
	return s.store.DeleteDefinition(ctx, strings.TrimSpace(fieldKey))
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
	// 默认安全上限，防止无约束大文本。
	if c.MaxLength == nil && len([]rune(text)) > 8000 {
		return ErrInvalid
	}
	return nil
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
