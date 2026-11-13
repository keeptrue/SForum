package providers

import (
	"context"
	"testing"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type fakeMetaDirectory struct {
	values  []entitymeta.MetaValue
	writes  []entitymeta.UpsertValueInput
	writeFn func(entitymeta.UpsertValueInput) error
	readErr error
}

func (f *fakeMetaDirectory) ListValues(context.Context, identity.Actor, string, int64) ([]entitymeta.MetaValue, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.values, nil
}

func (f *fakeMetaDirectory) UpsertValues(_ context.Context, _ identity.Actor, _ string, _ int64, inputs []entitymeta.UpsertValueInput) ([]entitymeta.MetaValue, error) {
	for _, input := range inputs {
		if f.writeFn != nil {
			if err := f.writeFn(input); err != nil {
				return nil, err
			}
		}
		f.writes = append(f.writes, input)
	}
	return nil, nil
}

// extensionmanifestKey 让测试与实现共用同一个组合键函数（不手写 x.<ext>.<key>）。
func extensionmanifestKey(extensionID, key string) string {
	return extensionmanifest.SettingsSurfaceFieldKey(extensionID, key)
}

func TestSettingsSurfaceMetaStoreRejectsUndeclaredField(t *testing.T) {
	directory := &fakeMetaDirectory{}
	surfaces := NewExtensionSettingsSurfaceProvider(fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution("forum.settings.profile.tabs", "demo.tab", 10,
			`{"type":"fields","fields":[{"key":"theme","label":"Theme","type":"string"}]}`),
	}}, nil)
	store := NewSettingsSurfaceMetaStore(surfaces, directory)
	actor := identity.Actor{ID: 9, Status: identity.UserStatusActive}

	err := store.WriteSettingsValues(context.Background(), actor, map[string]any{
		extensionmanifestKey("demo.plugin", "theme"):  "dark",
		extensionmanifestKey("demo.plugin", "absent"): "x",
	})
	fieldErr, ok := err.(*profile.SettingsFieldError)
	if !ok {
		t.Fatalf("undeclared field must yield a field error, got %v", err)
	}
	if len(fieldErr.Fields) != 1 || fieldErr.Fields[extensionmanifestKey("demo.plugin", "absent")][0] != profile.MessageSettingsFieldUnknown {
		t.Fatalf("field errors = %#v", fieldErr.Fields)
	}
	if len(directory.writes) != 1 || directory.writes[0].FieldKey != extensionmanifestKey("demo.plugin", "theme") {
		t.Fatalf("declared field must still be written, got %#v", directory.writes)
	}
}

func TestSettingsSurfaceMetaStoreMapsEntityMetaRejections(t *testing.T) {
	surfaces := NewExtensionSettingsSurfaceProvider(fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution("forum.settings.profile.tabs", "demo.tab", 10,
			`{"type":"fields","fields":[
				{"key":"theme","label":"Theme","type":"string"},
				{"key":"about","label":"About","type":"text"},
				{"key":"locked","label":"Locked","type":"string"}
			]}`),
	}}, nil)
	key := func(name string) string { return extensionmanifestKey("demo.plugin", name) }
	actor := identity.Actor{ID: 9, Status: identity.UserStatusActive}

	cases := []struct {
		name     string
		value    any
		err      error
		expected string
	}{
		{"invalid value", "too long", entitymeta.ErrInvalid, profile.MessageSettingsFieldInvalid},
		{"required cleared", nil, entitymeta.ErrInvalid, profile.MessageSettingsFieldRequired},
		{"disabled field", "x", entitymeta.ErrFieldDisabled, profile.MessageSettingsFieldForbidden},
		{"no write permission", "x", identity.ErrPermissionDenied, profile.MessageSettingsFieldForbidden},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			directory := &fakeMetaDirectory{writeFn: func(input entitymeta.UpsertValueInput) error {
				if input.FieldKey == key("theme") {
					return testCase.err
				}
				return nil
			}}
			store := NewSettingsSurfaceMetaStore(surfaces, directory)
			err := store.WriteSettingsValues(context.Background(), actor, map[string]any{key("theme"): testCase.value})
			fieldErr, ok := err.(*profile.SettingsFieldError)
			if !ok {
				t.Fatalf("expected field error, got %v", err)
			}
			if fieldErr.Fields[key("theme")][0] != testCase.expected {
				t.Fatalf("message = %#v, want %q", fieldErr.Fields, testCase.expected)
			}
		})
	}
}

func TestSettingsSurfaceMetaStoreSplitsPublicAndSelfVisibility(t *testing.T) {
	surfaces := NewExtensionSettingsSurfaceProvider(fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution("forum.settings.profile.tabs", "demo.tab", 10,
			`{"type":"fields","fields":[
				{"key":"theme","label":"Theme","type":"string","visibility":"owner"},
				{"key":"about","label":"About","type":"text","visibility":"public"},
				{"key":"secret","label":"Secret","type":"string","visibility":"admin"}
			]}`),
	}}, nil)
	directory := &fakeMetaDirectory{values: []entitymeta.MetaValue{
		{FieldKey: extensionmanifestKey("demo.plugin", "theme"), Value: "dark", Visibility: entitymeta.VisibilityOwner},
		{FieldKey: extensionmanifestKey("demo.plugin", "about"), Value: "hello", Visibility: entitymeta.VisibilityPublic},
		{FieldKey: extensionmanifestKey("demo.plugin", "secret"), Value: "ops", Visibility: entitymeta.VisibilityAdmin},
	}}
	store := NewSettingsSurfaceMetaStore(surfaces, directory)
	actor := identity.Actor{ID: 9, Status: identity.UserStatusActive}

	self, err := store.MySettingsValues(context.Background(), actor)
	if err != nil {
		t.Fatalf("MySettingsValues: %v", err)
	}
	if len(self) != 2 || self[extensionmanifestKey("demo.plugin", "theme")] != "dark" {
		t.Fatalf("self values = %#v", self)
	}
	if _, leaked := self[extensionmanifestKey("demo.plugin", "secret")]; leaked {
		t.Fatal("admin-visibility field must not reach the self surface")
	}

	public, err := store.PublicSettingsMeta(context.Background(), identity.Actor{}, 9)
	if err != nil {
		t.Fatalf("PublicSettingsMeta: %v", err)
	}
	if len(public) != 1 || public[extensionmanifestKey("demo.plugin", "about")].Value != "hello" {
		t.Fatalf("public meta = %#v", public)
	}
	if _, leaked := public[extensionmanifestKey("demo.plugin", "theme")]; leaked {
		t.Fatal("owner field must not reach the public surface")
	}
}

// 定义不存在（投影没落库）必须与「未声明/停用/无权」分开报，方便运维定位。
func TestSettingsSurfaceMetaStoreReportsMissingDefinitionSeparately(t *testing.T) {
	surfaces := NewExtensionSettingsSurfaceProvider(fakeContributionSource{items: []extensions.EffectiveContribution{
		settingsSectionContribution("forum.settings.profile.tabs", "demo.tab", 10,
			`{"type":"fields","fields":[{"key":"theme","label":"Theme","type":"string"}]}`),
	}}, nil)
	directory := &fakeMetaDirectory{writeFn: func(entitymeta.UpsertValueInput) error {
		return entitymeta.ErrNotFound
	}}
	store := NewSettingsSurfaceMetaStore(surfaces, directory)
	err := store.WriteSettingsValues(context.Background(),
		identity.Actor{ID: 9, Status: identity.UserStatusActive},
		map[string]any{extensionmanifestKey("demo.plugin", "theme"): "dark"})
	fieldErr, ok := err.(*profile.SettingsFieldError)
	if !ok {
		t.Fatalf("expected field error, got %v", err)
	}
	if got := fieldErr.Fields[extensionmanifestKey("demo.plugin", "theme")][0]; got != profile.MessageSettingsFieldUndefined {
		t.Fatalf("missing definition message = %q, want %q", got, profile.MessageSettingsFieldUndefined)
	}
}
