package profile

import (
	"context"
	"testing"

	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type stubSettingsSections struct{ sections []settingssurface.Section }

func (s stubSettingsSections) ProfileSections(context.Context, identity.Actor) ([]settingssurface.Section, error) {
	return s.sections, nil
}

type stubSettingsMeta struct{ values map[string]any }

func (s stubSettingsMeta) MySettingsValues(context.Context, identity.Actor) (map[string]any, error) {
	return s.values, nil
}

func (s stubSettingsMeta) PublicSettingsMeta(context.Context, identity.Actor, int64) (map[string]SettingsMetaValue, error) {
	return nil, nil
}

// 有 tab 时 settingsValues 恒下发（空则 {}）：整键消失会让前端把「还没填」
// 误判成「不支持插件字段」。
func TestAttachSelfSettingsAlwaysSendsSettingsValues(t *testing.T) {
	sections := []settingssurface.Section{{
		ID:   "demo.tab",
		Type: extensionmanifest.SettingsSectionTabFields,
		Fields: []settingssurface.Field{{
			FieldKey: extensionmanifest.SettingsSurfaceFieldKey("demo.plugin", "theme"),
		}},
	}}
	service := NewService(nil).WithSettingsSections(stubSettingsSections{sections: sections}).
		WithSettingsMeta(stubSettingsMeta{}, nil)

	view := PublicProfile{}
	service.attachSelfSettings(context.Background(), identity.Actor{ID: 1, Status: identity.UserStatusActive}, &view)
	if view.SettingsValues == nil {
		t.Fatal("settingsValues must be present (empty object) when sections exist")
	}
	if len(*view.SettingsValues) != 0 {
		t.Fatalf("settingsValues = %#v", *view.SettingsValues)
	}

	// 有值时只保留当前 tab 声明过的键。
	key := extensionmanifest.SettingsSurfaceFieldKey("demo.plugin", "theme")
	service = NewService(nil).WithSettingsSections(stubSettingsSections{sections: sections}).
		WithSettingsMeta(stubSettingsMeta{values: map[string]any{
			key:                            "dark",
			"x.demo-plugin.undeclared_key": "leak",
		}}, nil)
	view = PublicProfile{}
	service.attachSelfSettings(context.Background(), identity.Actor{ID: 1, Status: identity.UserStatusActive}, &view)
	if view.SettingsValues == nil || len(*view.SettingsValues) != 1 || (*view.SettingsValues)[key] != "dark" {
		t.Fatalf("settingsValues = %#v", view.SettingsValues)
	}

	// 没有 tab 时保持省略（nil），公开端点也不该出现这个键。
	service = NewService(nil).WithSettingsSections(stubSettingsSections{})
	view = PublicProfile{}
	service.attachSelfSettings(context.Background(), identity.Actor{ID: 1, Status: identity.UserStatusActive}, &view)
	if view.SettingsValues != nil {
		t.Fatalf("settingsValues must stay omitted without sections, got %#v", view.SettingsValues)
	}
}
