package providers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// fakeSettingsFieldDirectory 复刻 entity-meta 侧对同步有意义的语义：
// 按 fieldKey upsert、保留 ID 与已存值、停用只翻 enabled。
type fakeSettingsFieldDirectory struct {
	defs   map[string]entitymeta.FieldDefinition
	inputs []entitymeta.CreateFieldInput
	values map[string]string
	err    error
}

func newFakeSettingsFieldDirectory() *fakeSettingsFieldDirectory {
	return &fakeSettingsFieldDirectory{
		defs:   map[string]entitymeta.FieldDefinition{},
		values: map[string]string{},
	}
}

func (f *fakeSettingsFieldDirectory) EnsureOwnerExtensionField(_ context.Context, input entitymeta.CreateFieldInput) (entitymeta.FieldDefinition, error) {
	if f.err != nil {
		return entitymeta.FieldDefinition{}, f.err
	}
	f.inputs = append(f.inputs, input)
	if existing, ok := f.defs[input.FieldKey]; ok {
		if existing.OwnerExtensionID != input.OwnerExtensionID {
			return entitymeta.FieldDefinition{}, entitymeta.ErrPermission
		}
		existing.Visibility = input.Visibility
		existing.Required = input.Required
		existing.Constraints = input.Constraints
		existing.Enabled = input.Enabled == nil || *input.Enabled
		f.defs[input.FieldKey] = existing
		return existing, nil
	}
	def := entitymeta.FieldDefinition{
		FieldKey:         input.FieldKey,
		EntityType:       input.EntityType,
		ValueType:        input.ValueType,
		Visibility:       input.Visibility,
		OwnerExtensionID: input.OwnerExtensionID,
		Required:         input.Required,
		Enabled:          input.Enabled == nil || *input.Enabled,
		Constraints:      input.Constraints,
	}
	f.defs[input.FieldKey] = def
	return def, nil
}

func (f *fakeSettingsFieldDirectory) ListOwnerExtensionDefinitions(_ context.Context, extensionID string) ([]entitymeta.FieldDefinition, error) {
	out := make([]entitymeta.FieldDefinition, 0, len(f.defs))
	for _, definition := range f.defs {
		if definition.OwnerExtensionID != extensionID {
			continue
		}
		out = append(out, definition)
	}
	return out, nil
}

func (f *fakeSettingsFieldDirectory) SetEnabledByOwnerField(_ context.Context, fieldKey string, enabled bool) error {
	definition, ok := f.defs[fieldKey]
	if !ok {
		return entitymeta.ErrNotFound
	}
	definition.Enabled = enabled
	f.defs[fieldKey] = definition
	return nil
}

func (f *fakeSettingsFieldDirectory) SetEnabledByOwnerExtension(_ context.Context, extensionID string, enabled bool) error {
	if f.err != nil {
		return f.err
	}
	for key, def := range f.defs {
		if def.OwnerExtensionID != extensionID {
			continue
		}
		def.Enabled = enabled
		f.defs[key] = def
	}
	return nil
}

func settingsSurfaceSyncManifest(payload string) extensionmanifest.Manifest {
	return extensionmanifest.Manifest{
		ID:   "demo.settings",
		Type: extensionmanifest.TypePlugin,
		Contributions: []extensionmanifest.ManifestContribution{{
			Point:   extensionmanifest.PointForumSettingsProfileTabs,
			ID:      "demo.settings.tab",
			Order:   10,
			Label:   map[string]string{"en-US": "Preferences"},
			Payload: json.RawMessage(payload),
		}},
	}
}

func TestSettingsSurfaceFieldSyncIsIdempotentAndKeepsValues(t *testing.T) {
	directory := newFakeSettingsFieldDirectory()
	sync := NewSettingsSurfaceFieldSync(directory)
	manifest := settingsSurfaceSyncManifest(`{"type":"fields","fields":[
		{"key":"theme","label":{"zh-CN":"主题","en-US":"Theme"},"type":"string","order":10},
		{"key":"about","label":{"en-US":"About"},"type":"text","order":20,"required":true}
	]}`)

	first, err := sync.SyncManifest(context.Background(), manifest)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first != 2 {
		t.Fatalf("first sync = %d fields, want 2", first)
	}
	if _, ok := directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "theme")]; !ok {
		t.Fatalf("field key must be x.<extension>.<key>, got %#v", directory.defs)
	}
	directory.values[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "theme")] = "dark"

	second, err := sync.SyncManifest(context.Background(), manifest)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second != 2 {
		t.Fatalf("second sync = %d fields, want 2", second)
	}
	if len(directory.defs) != 2 {
		t.Fatalf("repeat sync must not duplicate definitions, got %d", len(directory.defs))
	}
	if len(directory.inputs) != 4 {
		t.Fatalf("repeat sync must re-assert each declaration, got %d inputs", len(directory.inputs))
	}
	if directory.values[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "theme")] != "dark" {
		t.Fatal("repeat sync must not touch stored values")
	}
}

func TestSettingsSurfaceFieldSyncMapsTypesVisibilityAndEnum(t *testing.T) {
	directory := newFakeSettingsFieldDirectory()
	manifest := settingsSurfaceSyncManifest(`{"type":"fields","fields":[
		{"key":"theme","label":{"en-US":"Theme"},"type":"select","visibility":"owner",
		 "options":[{"value":"dark","label":"Dark"},{"value":"light","label":"Light"}]},
		{"key":"bio","label":{"en-US":"Bio"},"type":"text"},
		{"key":"age","label":{"en-US":"Age"},"type":"number","constraints":{"min":13,"max":120}},
		{"key":"beta","label":{"en-US":"Beta"},"type":"boolean"}
	]}`)
	if _, err := NewSettingsSurfaceFieldSync(directory).SyncManifest(context.Background(), manifest); err != nil {
		t.Fatalf("sync: %v", err)
	}

	theme := directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "theme")]
	if theme.ValueType != entitymeta.ValueString || theme.Visibility != entitymeta.VisibilityOwner {
		t.Fatalf("select must degrade to string with declared visibility, got %#v", theme)
	}
	var constraints struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(theme.Constraints, &constraints); err != nil {
		t.Fatalf("select constraints must be JSON: %v", err)
	}
	if len(constraints.Enum) != 2 || constraints.Enum[0] != "dark" || constraints.Enum[1] != "light" {
		t.Fatalf("select options must become the stored enum, got %#v", constraints.Enum)
	}
	if directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "bio")].ValueType != entitymeta.ValueText {
		t.Fatal("text must map to entity-meta text")
	}
	if directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "age")].ValueType != entitymeta.ValueNumber {
		t.Fatal("number must map to entity-meta number")
	}
	if directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "beta")].ValueType != entitymeta.ValueBoolean {
		t.Fatal("boolean must map to entity-meta boolean")
	}
	if directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "bio")].Visibility != entitymeta.VisibilityPublic {
		t.Fatal("omitted visibility must default to public")
	}
	if directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "bio")].OwnerExtensionID != "demo.settings" {
		t.Fatal("owner extension id must be recorded for later bulk disable")
	}
	if !directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "bio")].Enabled {
		t.Fatal("enable sync must leave fields enabled")
	}
}

func TestSettingsSurfaceFieldSyncDisableKeepsDefinitionsAndValues(t *testing.T) {
	directory := newFakeSettingsFieldDirectory()
	sync := NewSettingsSurfaceFieldSync(directory)
	manifest := settingsSurfaceSyncManifest(`{"type":"fields","fields":[
		{"key":"theme","label":{"en-US":"Theme"},"type":"string"}
	]}`)
	if _, err := sync.SyncManifest(context.Background(), manifest); err != nil {
		t.Fatalf("sync: %v", err)
	}
	directory.values[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "theme")] = "light"

	if err := sync.Disable(context.Background(), "demo.settings"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	def, ok := directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "theme")]
	if !ok {
		t.Fatal("disable must not delete the field definition")
	}
	if def.Enabled {
		t.Fatal("disable must flip enabled=false")
	}
	if directory.values[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "theme")] != "light" {
		t.Fatal("disable must not delete stored values")
	}

	// 重新 enable：声明原样回来，字段重新可见。
	if _, err := sync.SyncManifest(context.Background(), manifest); err != nil {
		t.Fatalf("re-enable sync: %v", err)
	}
	if !directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "theme")].Enabled {
		t.Fatal("re-enable must restore the field")
	}
}

func TestSettingsSurfaceFieldSyncSkipsNonProfileSurfaces(t *testing.T) {
	directory := newFakeSettingsFieldDirectory()
	manifest := extensionmanifest.Manifest{
		ID:   "demo.settings",
		Type: extensionmanifest.TypePlugin,
		Contributions: []extensionmanifest.ManifestContribution{
			{
				Point: extensionmanifest.PointAdminSettingsTabs, ID: "demo.settings.site", Order: 10,
				Label:   map[string]string{"en-US": "Site"},
				Payload: json.RawMessage(`{"type":"settings","tabId":"general"}`),
			},
			{
				Point: extensionmanifest.PointForumSettingsProfileTabs, ID: "demo.settings.panel", Order: 20,
				Label:   map[string]string{"en-US": "Panel"},
				Payload: json.RawMessage(`{"type":"component","componentId":"demo.settings.component.panel"}`),
			},
		},
	}
	synced, err := NewSettingsSurfaceFieldSync(directory).SyncManifest(context.Background(), manifest)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if synced != 0 || len(directory.defs) != 0 {
		t.Fatalf("only fields tabs produce entity-meta fields, got %d synced / %d defs", synced, len(directory.defs))
	}
}

func TestSettingsSurfaceFieldSyncPropagatesDirectoryErrors(t *testing.T) {
	boom := errors.New("boom")
	directory := newFakeSettingsFieldDirectory()
	directory.err = boom
	manifest := settingsSurfaceSyncManifest(`{"type":"fields","fields":[
		{"key":"theme","label":{"en-US":"Theme"},"type":"string"}
	]}`)
	if _, err := NewSettingsSurfaceFieldSync(directory).SyncManifest(context.Background(), manifest); !errors.Is(err, boom) {
		t.Fatalf("sync error must propagate, got %v", err)
	}
	if err := NewSettingsSurfaceFieldSync(directory).Disable(context.Background(), "demo.settings"); !errors.Is(err, boom) {
		t.Fatalf("disable error must propagate, got %v", err)
	}
}

// 插件声明是本功能新开的入口，约束不能 fail-open：解析不了就不许落定义。
func TestSettingsSurfaceFieldSyncRejectsInvalidConstraints(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"unknown constraint key", `{"key":"theme","label":"Theme","type":"string","constraints":{"maxSize":3}}`},
		{"empty enum", `{"key":"theme","label":"Theme","type":"select","constraints":{"enum":[]},
			"options":[{"value":"dark","label":"Dark"}]}`},
		{"enum not matching options", `{"key":"theme","label":"Theme","type":"select","constraints":{"enum":["dark","light"]},
			"options":[{"value":"dark","label":"Dark"}]}`},
		{"string with object maxLength", `{"key":"theme","label":"Theme","type":"string","constraints":{"maxLength":{"a":1}}}`},
		{"number with maxLength", `{"key":"count","label":"Count","type":"number","constraints":{"maxLength":3}}`},
		{"string with array maxLength", `{"key":"theme","label":"Theme","type":"string","constraints":{"maxLength":[1,2]}}`},
		{"boolean with min", `{"key":"flag","label":"Flag","type":"boolean","constraints":{"min":0}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			directory := newFakeSettingsFieldDirectory()
			manifest := settingsSurfaceSyncManifest(`{"type":"fields","fields":[` + testCase.field + `]}`)
			if _, err := NewSettingsSurfaceFieldSync(directory).SyncManifest(context.Background(), manifest); err == nil {
				t.Fatalf("%s must fail the sync", testCase.name)
			}
			if len(directory.defs) != 0 {
				t.Fatalf("%s must not persist a definition, got %#v", testCase.name, directory.defs)
			}
		})
	}
}

// 省略 enum 是合法的：options 就是唯一真值，同步时派生存储层 enum。
func TestSettingsSurfaceFieldSyncDerivesEnumFromOptions(t *testing.T) {
	directory := newFakeSettingsFieldDirectory()
	manifest := settingsSurfaceSyncManifest(`{"type":"fields","fields":[
		{"key":"theme","label":"Theme","type":"select","constraints":{"maxLength":16},
		 "options":[{"value":"dark","label":"Dark"},{"value":"light","label":"Light"}]}
	]}`)
	if _, err := NewSettingsSurfaceFieldSync(directory).SyncManifest(context.Background(), manifest); err != nil {
		t.Fatalf("sync: %v", err)
	}
	def := directory.defs[extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "theme")]
	var constraints struct {
		Enum      []string `json:"enum"`
		MaxLength *int     `json:"maxLength"`
	}
	if err := json.Unmarshal(def.Constraints, &constraints); err != nil {
		t.Fatalf("constraints: %v", err)
	}
	if len(constraints.Enum) != 2 || constraints.MaxLength == nil || *constraints.MaxLength != 16 {
		t.Fatalf("derived constraints = %#v", constraints)
	}
}

// R4 差集清扫：声明里被删掉的字段必须停用（值保留），回到旧声明能恢复。
// 不做的后果是旧 public 字段的历史值继续匿名可读、仍可写。
func TestSettingsSurfaceFieldSyncDisablesUndeclaredFields(t *testing.T) {
	directory := newFakeSettingsFieldDirectory()
	sync := NewSettingsSurfaceFieldSync(directory)
	v1 := settingsSurfaceSyncManifest(`{"type":"fields","fields":[
		{"key":"alpha","label":"Alpha","type":"string"},
		{"key":"beta","label":"Beta","type":"string"}
	]}`)
	if _, err := sync.SyncManifest(context.Background(), v1); err != nil {
		t.Fatalf("sync v1: %v", err)
	}
	alphaKey := extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "alpha")
	betaKey := extensionmanifest.SettingsSurfaceFieldKey("demo.settings", "beta")
	directory.values[alphaKey] = "keep-me"
	directory.values[betaKey] = "old"

	// v2：删除 beta。
	v2 := settingsSurfaceSyncManifest(`{"type":"fields","fields":[
		{"key":"alpha","label":"Alpha","type":"string"}
	]}`)
	if _, err := sync.SyncManifest(context.Background(), v2); err != nil {
		t.Fatalf("sync v2: %v", err)
	}
	if !directory.defs[alphaKey].Enabled {
		t.Fatal("declared field must stay enabled")
	}
	if directory.defs[betaKey].Enabled {
		t.Fatal("undeclared field must be disabled by the diff sweep")
	}
	if directory.values[alphaKey] != "keep-me" {
		t.Fatalf("diff sweep must not touch values, got %#v", directory.values)
	}

	// 回到 v1：beta 恢复 enabled（值从未删除）。
	if _, err := sync.SyncManifest(context.Background(), v1); err != nil {
		t.Fatalf("sync v1 again: %v", err)
	}
	if !directory.defs[betaKey].Enabled {
		t.Fatal("re-declaring a field must re-enable it")
	}
	if directory.values[betaKey] != "old" {
		t.Fatalf("re-enable must keep the old value, got %#v", directory.values)
	}
}

// 悬空治理覆盖行：声明里已经没有的 tab / 字段，覆盖面必须被清掉。
func TestSettingsSurfaceFieldSyncPrunesDanglingOverrides(t *testing.T) {
	directory := newFakeSettingsFieldDirectory()
	overrides := newFakeOverrideStore()
	sync := NewSettingsSurfaceFieldSync(directory, overrides)
	point := extensionmanifest.PointForumSettingsProfileTabs
	v1 := settingsSurfaceSyncManifest(`{"type":"fields","fields":[
		{"key":"alpha","label":"Alpha","type":"string"},
		{"key":"beta","label":"Beta","type":"string"}
	]}`)
	if _, err := sync.SyncManifest(context.Background(), v1); err != nil {
		t.Fatalf("sync v1: %v", err)
	}
	seed := func(contributionID, itemKey string) {
		t.Helper()
		if _, err := overrides.UpsertOverride(context.Background(), settingssurface.OverrideInput{
			Key: settingssurface.OverrideKey{
				ExtensionID: "demo.settings", Point: point,
				ContributionID: contributionID, ItemKey: itemKey,
			},
			Enabled: false,
		}); err != nil {
			t.Fatalf("seed override: %v", err)
		}
	}
	seed("demo.settings.tab", "alpha")
	seed("demo.settings.tab", "beta")
	seed("demo.settings.tab.removed", "")

	// v2：删掉 beta 与整个 removed tab。
	v2 := settingsSurfaceSyncManifest(`{"type":"fields","fields":[
		{"key":"alpha","label":"Alpha","type":"string"}
	]}`)
	if _, err := sync.SyncManifest(context.Background(), v2); err != nil {
		t.Fatalf("sync v2: %v", err)
	}
	if len(overrides.rows) != 1 {
		t.Fatalf("dangling overrides must be pruned, got %#v", overrides.rows)
	}
	for key := range overrides.rows {
		if key.ContributionID != "demo.settings.tab" || key.ItemKey != "alpha" {
			t.Fatalf("surviving override = %#v", key)
		}
	}
}
