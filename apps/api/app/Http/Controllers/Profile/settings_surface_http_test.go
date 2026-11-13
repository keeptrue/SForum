package profilecontroller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 这一组是 HTTP 线形状测试：settingsSections / settingsValues / meta 的键名、
// null 语义与错误信封。字段目录与治理过滤的语义由 providers 的单测覆盖，
// 持久化由 providers 的真实 DB 链路测试覆盖，这里只锁协议。

type httpSettingsSurface struct {
	sections []settingssurface.Section
	// disabled 模拟站点治理把某个字段关掉：它不再出现在投影里。
	disabled map[string]bool
}

func (s httpSettingsSurface) ProfileSections(context.Context, identity.Actor) ([]settingssurface.Section, error) {
	out := make([]settingssurface.Section, 0, len(s.sections))
	for _, section := range s.sections {
		copy := section
		copy.Fields = nil
		for _, field := range section.Fields {
			if s.disabled[field.FieldKey] {
				continue
			}
			copy.Fields = append(copy.Fields, field)
		}
		if len(section.Fields) > 0 && len(copy.Fields) == 0 {
			continue
		}
		out = append(out, copy)
	}
	return out, nil
}

type httpSettingsMeta struct {
	surface httpSettingsSurface
	values  map[string]any
}

func (m *httpSettingsMeta) MySettingsValues(context.Context, identity.Actor) (map[string]any, error) {
	out := map[string]any{}
	for key, value := range m.values {
		out[key] = value
	}
	return out, nil
}

func (m *httpSettingsMeta) PublicSettingsMeta(_ context.Context, _ identity.Actor, _ int64) (map[string]profile.SettingsMetaValue, error) {
	out := map[string]profile.SettingsMetaValue{}
	for _, section := range m.surface.sections {
		for _, field := range section.Fields {
			if field.Visibility != "public" || m.surface.disabled[field.FieldKey] {
				continue
			}
			value, ok := m.values[field.FieldKey]
			if !ok {
				continue
			}
			out[field.FieldKey] = profile.SettingsMetaValue{Label: map[string]string{"en-US": field.FieldKey}, Value: value}
		}
	}
	return out, nil
}

func (m *httpSettingsMeta) WriteSettingsValues(ctx context.Context, actor identity.Actor, values map[string]any) error {
	allowed := map[string]bool{}
	sections, _ := m.surface.ProfileSections(ctx, actor)
	for _, section := range sections {
		for _, field := range section.Fields {
			allowed[field.FieldKey] = true
		}
	}
	fieldErrors := map[string][]string{}
	for key, value := range values {
		if !allowed[key] {
			fieldErrors[key] = []string{profile.MessageSettingsFieldUnknown}
			continue
		}
		m.values[key] = value
	}
	if len(fieldErrors) > 0 {
		return &profile.SettingsFieldError{Fields: fieldErrors}
	}
	return nil
}

func httpSettingsSurfaceFixture() (httpSettingsSurface, *httpSettingsMeta) {
	themeKey := extensionmanifest.SettingsSurfaceFieldKey("sforum.settings.surface.demo", "theme")
	aboutKey := extensionmanifest.SettingsSurfaceFieldKey("sforum.settings.surface.demo", "signature_line")
	surface := httpSettingsSurface{sections: []settingssurface.Section{{
		Source:             "extension",
		ExtensionID:        "sforum.settings.surface.demo",
		ID:                 "sforum.settings.surface.demo.preferences",
		Type:               extensionmanifest.SettingsSectionTabFields,
		Label:              map[string]string{"zh-CN": "偏好设置"},
		EffectiveSortOrder: 10,
		Fields: []settingssurface.Field{
			{FieldKey: themeKey, Type: "select", Visibility: "owner", Label: extensionmanifest.LocalizedText{Default: "Theme"}},
			{FieldKey: aboutKey, Type: "text", Visibility: "public", Label: extensionmanifest.LocalizedText{Default: "Signature"}},
		},
	}}, disabled: map[string]bool{}}
	meta := &httpSettingsMeta{surface: surface, values: map[string]any{}}
	return surface, meta
}

type settingsEnvelope struct {
	Data struct {
		SettingsSections []settingssurface.Section `json:"settingsSections"`
		SettingsValues   *map[string]any           `json:"settingsValues"`
		Meta             map[string]struct {
			Label map[string]string `json:"label"`
			Value any               `json:"value"`
		} `json:"meta"`
	} `json:"data"`
	Data2 json.RawMessage `json:"-"`
}

func TestProfileSettingsSectionsAndValuesWireShape(t *testing.T) {
	surface, meta := httpSettingsSurfaceFixture()
	app, _, _ := newProfileTestAppWithOptions(profileForumReadPolicy{guestRead: "public", ok: true},
		profileTestOptions{settings: surface, settingsMeta: meta, settingsWrite: meta})
	cookie := loginProfileUser(t, app, 7)

	resp := performProfileRequest(t, app, http.MethodGet, "/api/v1/profile", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /profile = %d", resp.StatusCode)
	}
	var envelope settingsEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data.SettingsSections) != 1 {
		t.Fatalf("settingsSections = %#v", envelope.Data.SettingsSections)
	}
	section := envelope.Data.SettingsSections[0]
	if section.Source != "extension" || len(section.Fields) != 2 {
		t.Fatalf("section = %#v", section)
	}
	if section.Fields[0].FieldKey == "" || !strings.HasPrefix(section.Fields[0].FieldKey, "x.") {
		t.Fatalf("field key must be composite, got %q", section.Fields[0].FieldKey)
	}
	// 还没有任何值时也必须是空对象，而不是整键消失。
	if envelope.Data.SettingsValues == nil {
		t.Fatal("settingsValues must be present even when empty")
	}

	// PUT 组合键成功，值回填。
	themeKey := section.Fields[0].FieldKey
	body, _ := json.Marshal(map[string]any{"meta": map[string]any{themeKey: "dark"}})
	resp = performProfileRequest(t, app, http.MethodPut, "/api/v1/profile", body, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT meta = %d", resp.StatusCode)
	}
	resp = performProfileRequest(t, app, http.MethodGet, "/api/v1/profile", nil, cookie)
	envelope = settingsEnvelope{}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode after put: %v", err)
	}
	if envelope.Data.SettingsValues == nil || (*envelope.Data.SettingsValues)[themeKey] != "dark" {
		t.Fatalf("settingsValues = %#v", envelope.Data.SettingsValues)
	}
}

func TestProfileMetaRejectsUndeclaredAndGovernedOffFields(t *testing.T) {
	surface, meta := httpSettingsSurfaceFixture()
	app, _, _ := newProfileTestAppWithOptions(profileForumReadPolicy{guestRead: "public", ok: true},
		profileTestOptions{settings: surface, settingsMeta: meta, settingsWrite: meta})
	cookie := loginProfileUser(t, app, 7)

	undeclared := extensionmanifest.SettingsSurfaceFieldKey("sforum.settings.surface.demo", "absent")
	body, _ := json.Marshal(map[string]any{"meta": map[string]any{undeclared: "x"}})
	resp := performProfileRequest(t, app, http.MethodPut, "/api/v1/profile", body, cookie)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("undeclared key = %d, want 422", resp.StatusCode)
	}
	var failure struct {
		Data struct {
			Reason string              `json:"reason"`
			Fields map[string][]string `json:"fields"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatalf("decode failure: %v", err)
	}
	if failure.Data.Reason != profile.CodeSettingsInvalid {
		t.Fatalf("reason = %q", failure.Data.Reason)
	}
	if len(failure.Data.Fields[undeclared]) == 0 {
		t.Fatalf("data.fields must key on the composite fieldKey, got %#v", failure.Data.Fields)
	}

	// 治理关闭的字段：键仍在 manifest 里，但投影不再下发 → 同样 422。
	themeKey := extensionmanifest.SettingsSurfaceFieldKey("sforum.settings.surface.demo", "theme")
	surface.disabled[themeKey] = true
	meta.surface = surface
	body, _ = json.Marshal(map[string]any{"meta": map[string]any{themeKey: "light"}})
	resp = performProfileRequest(t, app, http.MethodPut, "/api/v1/profile", body, cookie)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("governed-off key = %d, want 422", resp.StatusCode)
	}
}

func TestPublicProfileMetaOnlyExposesPublicFields(t *testing.T) {
	surface, meta := httpSettingsSurfaceFixture()
	themeKey := extensionmanifest.SettingsSurfaceFieldKey("sforum.settings.surface.demo", "theme")
	aboutKey := extensionmanifest.SettingsSurfaceFieldKey("sforum.settings.surface.demo", "signature_line")
	meta.values[themeKey] = "dark"
	meta.values[aboutKey] = "hello"
	app, _, _ := newProfileTestAppWithOptions(profileForumReadPolicy{guestRead: "public", ok: true},
		profileTestOptions{settings: surface, settingsMeta: meta, settingsWrite: meta})

	resp := performProfileRequest(t, app, http.MethodGet, "/api/v1/profiles/alice", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public profile = %d", resp.StatusCode)
	}
	var envelope settingsEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, leaked := envelope.Data.Meta[themeKey]; leaked {
		t.Fatalf("owner-visibility field leaked into public meta: %#v", envelope.Data.Meta)
	}
	entry, ok := envelope.Data.Meta[aboutKey]
	if !ok || entry.Value != "hello" {
		t.Fatalf("public meta = %#v", envelope.Data.Meta)
	}
}
