package entitymeta

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func boolValue(value bool) *bool { return &value }

// 插件字段同步（Providers 的 SettingsSurfaceFieldSync）依赖两个宿主语义：
// upsert 幂等、停用不删值。这两条都在这里锁住——它们落在 entity-meta 内，
// 外部包无法用 fake Store 证明。
func TestServiceEnsureOwnerExtensionFieldIsIdempotent(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	input := CreateFieldInput{
		FieldKey:         "x.demo-settings.theme",
		EntityType:       EntityUser,
		ValueType:        ValueString,
		Visibility:       VisibilityOwner,
		LabelZHCN:        "主题",
		LabelENUS:        "Theme",
		OwnerExtensionID: "demo.settings",
		Enabled:          boolValue(true),
		Constraints:      json.RawMessage(`{"enum":["dark","light"]}`),
	}
	first, err := service.EnsureOwnerExtensionField(ctx, input)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if _, err := service.UpsertValues(ctx, ownerActor(1), EntityUser, 1, []UpsertValueInput{
		{FieldKey: input.FieldKey, Value: "dark"},
	}); err != nil {
		t.Fatalf("write value: %v", err)
	}

	// 第二次同步：文案变了，但字段身份与已存值都不许变。
	second := input
	second.LabelENUS = "Theme choice"
	again, err := service.EnsureOwnerExtensionField(ctx, second)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if again.ID != first.ID || again.FieldKey != first.FieldKey {
		t.Fatalf("ensure must be idempotent by field key: first=%#v second=%#v", first, again)
	}
	if again.Label["en-US"] != "Theme choice" {
		t.Fatalf("ensure must refresh the declaration label: %#v", again.Label)
	}
	values, err := service.ListValues(ctx, ownerActor(1), EntityUser, 1)
	if err != nil {
		t.Fatalf("list values: %v", err)
	}
	if len(values) != 1 || values[0].Value != "dark" {
		t.Fatalf("ensure must not touch stored values: %#v", values)
	}
}

func TestServiceEnsureOwnerExtensionFieldRefusesTakeover(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	// 运营手建的同名字段（无 owner）不许被插件接管。
	if _, err := service.CreateDefinition(ctx, managerActor(99), CreateFieldInput{
		FieldKey:   "x.demo-settings.theme",
		EntityType: EntityUser,
		ValueType:  ValueString,
		Visibility: VisibilityPublic,
		LabelENUS:  "Operator field",
	}); err != nil {
		t.Fatalf("create operator field: %v", err)
	}
	_, err := service.EnsureOwnerExtensionField(ctx, CreateFieldInput{
		FieldKey:         "x.demo-settings.theme",
		EntityType:       EntityUser,
		ValueType:        ValueString,
		Visibility:       VisibilityPublic,
		LabelENUS:        "Plugin field",
		OwnerExtensionID: "demo.settings",
	})
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("takeover of an operator field must be refused, got %v", err)
	}

	// 别的扩展的字段同样不许抢。
	if _, err := service.EnsureOwnerExtensionField(ctx, CreateFieldInput{
		FieldKey:         "x.other.theme",
		EntityType:       EntityUser,
		ValueType:        ValueString,
		Visibility:       VisibilityPublic,
		LabelENUS:        "Other plugin field",
		OwnerExtensionID: "other",
	}); err != nil {
		t.Fatalf("create other extension field: %v", err)
	}
	if _, err := service.EnsureOwnerExtensionField(ctx, CreateFieldInput{
		FieldKey:         "x.other.theme",
		EntityType:       EntityUser,
		ValueType:        ValueString,
		Visibility:       VisibilityPublic,
		LabelENUS:        "Hijacked",
		OwnerExtensionID: "demo.settings",
	}); !errors.Is(err, ErrPermission) {
		t.Fatalf("takeover of another extension's field must be refused, got %v", err)
	}
}

func TestServiceEnsureOwnerExtensionFieldRejectsValueTypeChange(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	base := CreateFieldInput{
		FieldKey:         "x.demo-settings.count",
		EntityType:       EntityUser,
		ValueType:        ValueString,
		Visibility:       VisibilityPublic,
		LabelENUS:        "Count",
		OwnerExtensionID: "demo.settings",
	}
	if _, err := service.EnsureOwnerExtensionField(ctx, base); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	changed := base
	changed.ValueType = ValueNumber
	if _, err := service.EnsureOwnerExtensionField(ctx, changed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("value type change must be refused (stored values would stop parsing), got %v", err)
	}
}

func TestServiceSetEnabledByOwnerExtensionKeepsValues(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	input := CreateFieldInput{
		FieldKey:         "x.demo-settings.theme",
		EntityType:       EntityUser,
		ValueType:        ValueString,
		Visibility:       VisibilityOwner,
		LabelENUS:        "Theme",
		OwnerExtensionID: "demo.settings",
	}
	if _, err := service.EnsureOwnerExtensionField(ctx, input); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := service.UpsertValues(ctx, ownerActor(1), EntityUser, 1, []UpsertValueInput{
		{FieldKey: input.FieldKey, Value: "light"},
	}); err != nil {
		t.Fatalf("write value: %v", err)
	}

	if err := service.SetEnabledByOwnerExtension(ctx, "demo.settings", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	def, err := store.GetDefinitionByKey(ctx, input.FieldKey)
	if err != nil {
		t.Fatalf("definition must survive disable: %v", err)
	}
	if def.Enabled {
		t.Fatal("disable must flip enabled=false")
	}
	if _, ok := store.values[valueKey(EntityUser, 1, input.FieldKey)]; !ok {
		t.Fatal("disable must not delete stored values")
	}
	if values, err := service.ListValues(ctx, ownerActor(1), EntityUser, 1); err != nil || len(values) != 0 {
		t.Fatalf("disabled field must not be readable, values=%#v err=%v", values, err)
	}

	// 重装回来自动恢复：enable 只翻一列，值原样在。
	if err := service.SetEnabledByOwnerExtension(ctx, "demo.settings", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	values, err := service.ListValues(ctx, ownerActor(1), EntityUser, 1)
	if err != nil {
		t.Fatalf("list after re-enable: %v", err)
	}
	if len(values) != 1 || values[0].Value != "light" {
		t.Fatalf("re-enable must restore the field with its old value: %#v", values)
	}
}

// select 类字段的可选值是存储层约束，不是前端下拉框的装饰。
func TestServiceStringEnumConstraintRejectsValueOutsideSet(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	if _, err := service.EnsureOwnerExtensionField(ctx, CreateFieldInput{
		FieldKey:         "x.demo-settings.theme",
		EntityType:       EntityUser,
		ValueType:        ValueString,
		Visibility:       VisibilityOwner,
		LabelENUS:        "Theme",
		OwnerExtensionID: "demo.settings",
		Constraints:      json.RawMessage(`{"enum":["dark","light"]}`),
	}); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if _, err := service.UpsertValues(ctx, ownerActor(1), EntityUser, 1, []UpsertValueInput{
		{FieldKey: "x.demo-settings.theme", Value: "purple"},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("value outside enum must be rejected, got %v", err)
	}
	values, err := service.UpsertValues(ctx, ownerActor(1), EntityUser, 1, []UpsertValueInput{
		{FieldKey: "x.demo-settings.theme", Value: "dark"},
	})
	if err != nil {
		t.Fatalf("value inside enum must be accepted: %v", err)
	}
	if len(values) != 1 || values[0].Value != "dark" {
		t.Fatalf("unexpected values: %#v", values)
	}
}
