package entitymeta

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type stubFieldPolicy struct {
	allowRead  bool
	allowWrite bool
	seenRead   []string
	seenWrite  []string
}

func (p *stubFieldPolicy) AllowRead(_ context.Context, _ identity.Actor, definition FieldDefinition) bool {
	p.seenRead = append(p.seenRead, definition.FieldKey)
	return p.allowRead
}

func (p *stubFieldPolicy) AllowWrite(_ context.Context, _ identity.Actor, definition FieldDefinition) bool {
	p.seenWrite = append(p.seenWrite, definition.FieldKey)
	return p.allowWrite
}

func seedPluginField(t *testing.T, service *Service, ctx context.Context) {
	t.Helper()
	if _, err := service.EnsureOwnerExtensionField(ctx, CreateFieldInput{
		FieldKey:         "x.demo-settings.theme",
		EntityType:       EntityUser,
		ValueType:        ValueString,
		Visibility:       VisibilityOwner,
		LabelENUS:        "Theme",
		OwnerExtensionID: "demo.settings",
	}); err != nil {
		t.Fatalf("seed plugin field: %v", err)
	}
	if _, err := service.UpsertValues(ctx, ownerActor(1), EntityUser, 1, []UpsertValueInput{
		{FieldKey: "x.demo-settings.theme", Value: "dark"},
	}); err != nil {
		t.Fatalf("seed value: %v", err)
	}
}

// 治理关掉的插件字段必须在存储层也被拒绝：只「看不见」不叫治理。
func TestServiceFieldPolicyGatesReadAndWrite(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()
	seedPluginField(t, service, ctx)

	policy := &stubFieldPolicy{allowRead: false, allowWrite: false}
	service.WithFieldPolicy(policy)

	values, err := service.ListValues(ctx, ownerActor(1), EntityUser, 1)
	if err != nil {
		t.Fatalf("ListValues: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("policy-denied field must not be readable, got %#v", values)
	}
	if len(policy.seenRead) == 0 {
		t.Fatal("read policy must be consulted for owner-extension fields")
	}

	_, err = service.UpsertValues(ctx, ownerActor(1), EntityUser, 1, []UpsertValueInput{
		{FieldKey: "x.demo-settings.theme", Value: "light"},
	})
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("policy-denied field must not be writable, got %v", err)
	}

	policy.allowRead = true
	policy.allowWrite = true
	values, err = service.ListValues(ctx, ownerActor(1), EntityUser, 1)
	if err != nil || len(values) != 1 {
		t.Fatalf("policy-allowed field must be readable, values=%#v err=%v", values, err)
	}
}

// 策略只作用于插件字段：宿主/运营手建字段不受影响（nil 策略也不过滤）。
func TestServiceFieldPolicyIgnoresHostOwnedFields(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store).WithFieldPolicy(&stubFieldPolicy{})
	ctx := context.Background()
	if _, err := service.CreateDefinition(ctx, managerActor(99), CreateFieldInput{
		FieldKey:   "operator.note",
		EntityType: EntityUser,
		ValueType:  ValueString,
		Visibility: VisibilityPublic,
		LabelENUS:  "Note",
	}); err != nil {
		t.Fatalf("create host field: %v", err)
	}
	if _, err := service.UpsertValues(ctx, ownerActor(1), EntityUser, 1, []UpsertValueInput{
		{FieldKey: "operator.note", Value: "ok"},
	}); err != nil {
		t.Fatalf("host field must bypass the plugin field policy: %v", err)
	}
	values, err := service.ListValues(ctx, ownerActor(1), EntityUser, 1)
	if err != nil || len(values) != 1 {
		t.Fatalf("host field must stay readable, values=%#v err=%v", values, err)
	}
}

// 插件字段的唯一真相是 manifest + 同步：管理端改/删会被下一次 sync 抹掉，直接拒绝。
func TestServiceRefusesAdminMutationOfOwnerExtensionFields(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()
	seedPluginField(t, service, ctx)

	label := "Hijacked"
	if _, err := service.UpdateDefinition(ctx, managerActor(99), "x.demo-settings.theme",
		UpdateFieldInput{LabelENUS: &label}); !errors.Is(err, ErrPermission) {
		t.Fatalf("admin update of a plugin field = %v, want ErrPermission", err)
	}
	constraints := json.RawMessage(`{"maxLength":5}`)
	if _, err := service.UpdateDefinition(ctx, managerActor(99), "x.demo-settings.theme",
		UpdateFieldInput{Constraints: &constraints}); !errors.Is(err, ErrPermission) {
		t.Fatalf("admin constraint update of a plugin field = %v, want ErrPermission", err)
	}
	if err := service.DeleteDefinition(ctx, managerActor(99), "x.demo-settings.theme"); !errors.Is(err, ErrPermission) {
		t.Fatalf("admin delete of a plugin field = %v, want ErrPermission", err)
	}
	// 运营手建字段仍然可改：拒绝范围只到 owner_extension_id 非空。
	if _, err := service.CreateDefinition(ctx, managerActor(99), CreateFieldInput{
		FieldKey: "operator.note", EntityType: EntityUser, ValueType: ValueString,
		Visibility: VisibilityPublic, LabelENUS: "Note",
	}); err != nil {
		t.Fatalf("create host field: %v", err)
	}
	if _, err := service.UpdateDefinition(ctx, managerActor(99), "operator.note", UpdateFieldInput{LabelENUS: &label}); err != nil {
		t.Fatalf("host field update must stay allowed: %v", err)
	}
}

// ownerExtensionId 是宿主内部字段：管理端既不能创建带 owner 的定义，也不能
// 把手建字段「认领」给某个插件（否则该插件 enable 会被这条行锁死）。
func TestServiceRefusesAdminAssignedOwnerExtension(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()
	admin := managerActor(99)

	if _, err := service.CreateDefinition(ctx, admin, CreateFieldInput{
		FieldKey:         "x.other-plugin.theme",
		EntityType:       EntityUser,
		ValueType:        ValueString,
		Visibility:       VisibilityPublic,
		LabelENUS:        "Hijack",
		OwnerExtensionID: "other.plugin",
	}); !errors.Is(err, ErrPermission) {
		t.Fatalf("create with ownerExtensionId = %v, want ErrPermission", err)
	}
	if _, err := store.GetDefinitionByKey(ctx, "x.other-plugin.theme"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected create must not persist a definition, got %v", err)
	}

	if _, err := service.CreateDefinition(ctx, admin, CreateFieldInput{
		FieldKey:   "operator.note",
		EntityType: EntityUser,
		ValueType:  ValueString,
		Visibility: VisibilityPublic,
		LabelENUS:  "Note",
	}); err != nil {
		t.Fatalf("create host field: %v", err)
	}
	claim := "other.plugin"
	if _, err := service.UpdateDefinition(ctx, admin, "operator.note",
		UpdateFieldInput{OwnerExtensionID: &claim}); !errors.Is(err, ErrPermission) {
		t.Fatalf("claim host field = %v, want ErrPermission", err)
	}
	def, err := store.GetDefinitionByKey(ctx, "operator.note")
	if err != nil {
		t.Fatalf("read host field: %v", err)
	}
	if def.OwnerExtensionID != "" {
		t.Fatalf("host field must stay host-owned, got %q", def.OwnerExtensionID)
	}
}
