package extensionmanifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// settingsSectionBase 是只带「设置面所需最小声明」的插件清单：
// 一个权限定义、一个 public L2 add 组件、一个私有组件、一份带 tab 的 Settings Document。
func settingsSectionBase() Manifest {
	return Manifest{
		ManifestVersion: ManifestVersionV3,
		ID:              "demo.settings",
		Name:            "Settings Surface Demo",
		Description:     "settingsSection contribution contract tests.",
		URL:             "https://example.com/settings",
		Author:          ManifestAuthor{Name: "Demo"},
		Version:         "1.0.0",
		Type:            TypePlugin,
		SForumVersion:   ">=0.1.0",
		PermissionDefinitions: []ManifestPermissionDefinition{{
			Key:              "demo.settings.manage",
			ContractVersion:  "demo.settings.manage@1",
			Label:            LocalizedText{Default: "Manage settings"},
			Description:      LocalizedText{Default: "Manage the demo settings surface."},
			AssignmentPolicy: "host",
		}},
		Components: []ManifestComponent{
			{
				ID:              "demo.settings.component.panel",
				ContractVersion: "demo.settings.component.panel@1",
				Action:          ComponentActionAdd,
				L2Component:     "demo.settings.file.frontend.panel",
				PropsSchema:     "demo.settings.component.panel.props@1",
			},
			{
				ID:              "demo.settings.component.private",
				ContractVersion: "demo.settings.component.private@1",
				Action:          ComponentActionAdd,
				Permission:      "demo.settings.manage",
				L2Component:     "demo.settings.file.frontend.private",
				PropsSchema:     "demo.settings.component.private.props@1",
			},
		},
		SettingsDocument: SettingsDocument{
			SchemaVersion: SettingsSchemaVersion,
			UI: SettingsUI{
				Mode:   SettingsUIModeSchema,
				Layout: SettingsLayoutTabs,
				Tabs:   []SettingsTab{{ID: "general", Label: LocalizedText{Default: "General"}}},
			},
		},
	}
}

func settingsSectionContribution(point, id string, order int, payload string) ManifestContribution {
	return ManifestContribution{
		Point:   point,
		ID:      id,
		Order:   order,
		Label:   map[string]string{"en-US": "Demo tab"},
		Icon:    "i-lucide-sliders-horizontal",
		Payload: json.RawMessage(payload),
	}
}

func validateSettingsSections(t *testing.T, manifest Manifest) error {
	t.Helper()
	return validateContributions(Normalize(manifest), ContributionPointDefinitions())
}

func TestSettingsSectionAcceptsDeclaredTabShapes(t *testing.T) {
	cases := []struct {
		name         string
		point        string
		payload      string
		manifestEdit func(*Manifest)
	}{
		{
			name:  "profile fields tab",
			point: PointForumSettingsProfileTabs,
			payload: `{"type":"fields","description":{"zh-CN":"界面偏好"},
				"fields":[
					{"key":"theme","label":{"zh-CN":"主题"},"type":"select","visibility":"owner",
					 "default":"light",
					 "options":[{"value":"dark","label":{"en-US":"Dark"}},{"value":"light","label":{"en-US":"Light"}}]},
					{"key":"about_me","label":"About me","type":"text","required":true,"constraints":{"maxLength":200}}
				]}`,
		},
		{
			name:    "profile component tab",
			point:   PointForumSettingsProfileTabs,
			payload: `{"type":"component","componentId":"demo.settings.component.panel"}`,
		},
		{
			name:    "admin settings tab",
			point:   PointAdminSettingsTabs,
			payload: `{"type":"settings","tabId":"general","permission":"demo.settings.manage"}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := settingsSectionBase()
			manifest.Contributions = []ManifestContribution{
				settingsSectionContribution(testCase.point, "demo.settings.tab", 10, testCase.payload),
			}
			if err := validateSettingsSections(t, manifest); err != nil {
				t.Fatalf("%s should validate: %v", testCase.name, err)
			}
		})
	}
}

// 贡献点目录是唯一真值：两个设置面都必须出现在目录里，且类型是 settingsSection。
func TestSettingsSectionPointsAreRegistered(t *testing.T) {
	points := map[string]ContributionPointDefinition{}
	for _, point := range ContributionPointDefinitions() {
		points[point.ID] = point
	}
	for _, id := range []string{PointForumSettingsProfileTabs, PointAdminSettingsTabs} {
		point, ok := points[id]
		if !ok {
			t.Fatalf("contribution point %q is not registered", id)
		}
		if point.PayloadType != PayloadTypeSettingsSection {
			t.Fatalf("contribution point %q payload type = %q", id, point.PayloadType)
		}
		if point.Description == "" {
			t.Fatalf("contribution point %q needs a catalog description", id)
		}
	}
	// 目录条目必须写清允许的 tab 形态，否则插件作者只能靠猜。
	if !strings.Contains(points[PointForumSettingsProfileTabs].Description, "`fields`") ||
		!strings.Contains(points[PointForumSettingsProfileTabs].Description, "`component`") {
		t.Fatal("profile settings point must document its allowed tab shapes")
	}
	if !strings.Contains(points[PointAdminSettingsTabs].Description, "`settings`") {
		t.Fatal("admin settings point must document its allowed tab shape")
	}
}

// 公开 API 端到端：Validate 必须走到 settingsSection 分支，而不是落到 default。
func TestSettingsSectionValidatesThroughPublicEntrypoint(t *testing.T) {
	manifest := settingsSectionBase()
	manifest.Components = nil
	manifest.Contributions = []ManifestContribution{
		settingsSectionContribution(PointForumSettingsProfileTabs, "demo.settings.tab", 10,
			`{"type":"fields","fields":[{"key":"theme","label":"Theme","type":"string"}]}`),
	}
	if err := Validate(manifest); err != nil {
		t.Fatalf("settingsSection contribution must validate through Validate: %v", err)
	}
	manifest.Contributions[0].Payload = json.RawMessage(`{"type":"settings","tabId":"general"}`)
	if err := Validate(manifest); err == nil {
		t.Fatal("point/type matrix must be enforced through Validate too")
	}
}

func TestSettingsSectionRejectsPointTypeMatrix(t *testing.T) {
	cases := []struct {
		name    string
		point   string
		payload string
	}{
		{"admin tab with fields", PointAdminSettingsTabs,
			`{"type":"fields","fields":[{"key":"theme","label":"Theme","type":"string"}]}`},
		{"admin tab with component", PointAdminSettingsTabs,
			`{"type":"component","componentId":"demo.settings.component.panel"}`},
		{"profile tab with settings", PointForumSettingsProfileTabs, `{"type":"settings","tabId":"general"}`},
		{"unknown shape", PointForumSettingsProfileTabs, `{"type":"widget"}`},
		{"missing shape", PointForumSettingsProfileTabs, `{"fields":[{"key":"theme","label":"Theme","type":"string"}]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := settingsSectionBase()
			manifest.Contributions = []ManifestContribution{
				settingsSectionContribution(testCase.point, "demo.settings.tab", 10, testCase.payload),
			}
			if err := validateSettingsSections(t, manifest); err == nil {
				t.Fatalf("%s must be rejected", testCase.name)
			}
		})
	}
}

func TestSettingsSectionRejectsUndeclaredPermission(t *testing.T) {
	manifest := settingsSectionBase()
	manifest.Contributions = []ManifestContribution{settingsSectionContribution(
		PointForumSettingsProfileTabs, "demo.settings.tab", 10,
		`{"type":"fields","permission":"demo.settings.absent","fields":[{"key":"theme","label":"Theme","type":"string"}]}`,
	)}
	if err := validateSettingsSections(t, manifest); err == nil {
		t.Fatal("tab permission must be declared by the package")
	}

	manifest = settingsSectionBase()
	manifest.Contributions = []ManifestContribution{settingsSectionContribution(
		PointForumSettingsProfileTabs, "demo.settings.tab", 10,
		`{"type":"fields","fields":[{"key":"theme","label":"Theme","type":"string","permission":"demo.settings.absent"}]}`,
	)}
	if err := validateSettingsSections(t, manifest); err == nil {
		t.Fatal("field permission must be declared by the package")
	}

	// 已声明的权限键可用（正向对照，否则上面的拒绝可能只是拒绝一切）。
	manifest = settingsSectionBase()
	manifest.Contributions = []ManifestContribution{settingsSectionContribution(
		PointForumSettingsProfileTabs, "demo.settings.tab", 10,
		`{"type":"fields","permission":"demo.settings.manage","fields":[{"key":"theme","label":"Theme","type":"string","permission":"demo.settings.manage"}]}`,
	)}
	if err := validateSettingsSections(t, manifest); err != nil {
		t.Fatalf("declared permission must be accepted: %v", err)
	}
}

func TestSettingsSectionRejectsComponentReferences(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"missing componentId", `{"type":"component"}`},
		{"undeclared component", `{"type":"component","componentId":"demo.settings.component.absent"}`},
		{"private component", `{"type":"component","componentId":"demo.settings.component.private"}`},
		{"component with fields", `{"type":"component","componentId":"demo.settings.component.panel",
			"fields":[{"key":"theme","label":"Theme","type":"string"}]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := settingsSectionBase()
			manifest.Contributions = []ManifestContribution{
				settingsSectionContribution(PointForumSettingsProfileTabs, "demo.settings.tab", 10, testCase.payload),
			}
			if err := validateSettingsSections(t, manifest); err == nil {
				t.Fatalf("%s must be rejected", testCase.name)
			}
		})
	}

	// 组件存在但不是 L2 add（只有 SSR 模板）同样不是「public L2 组件」。
	manifest := settingsSectionBase()
	manifest.Components[0].L2Component = ""
	manifest.Contributions = []ManifestContribution{settingsSectionContribution(
		PointForumSettingsProfileTabs, "demo.settings.tab", 10,
		`{"type":"component","componentId":"demo.settings.component.panel"}`,
	)}
	if err := validateSettingsSections(t, manifest); err == nil {
		t.Fatal("component without an L2 implementation must be rejected")
	}
}

func TestSettingsSectionRejectsSettingsTabReferences(t *testing.T) {
	manifest := settingsSectionBase()
	manifest.Contributions = []ManifestContribution{settingsSectionContribution(
		PointAdminSettingsTabs, "demo.settings.tab", 10, `{"type":"settings","tabId":"absent"}`,
	)}
	if err := validateSettingsSections(t, manifest); err == nil {
		t.Fatal("tabId must point at a tab of this package settings document")
	}

	manifest = settingsSectionBase()
	manifest.Contributions = []ManifestContribution{settingsSectionContribution(
		PointAdminSettingsTabs, "demo.settings.tab", 10, `{"type":"settings"}`,
	)}
	if err := validateSettingsSections(t, manifest); err == nil {
		t.Fatal("type=settings requires a tabId")
	}
}

func TestSettingsSectionRejectsInvalidFields(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"uppercase key", `{"key":"Theme","label":"Theme","type":"string"}`},
		{"leading digit key", `{"key":"1theme","label":"Theme","type":"string"}`},
		{"overlong key", fmt.Sprintf(`{"key":%q,"label":"Theme","type":"string"}`, strings.Repeat("a", 42))},
		{"bad type", `{"key":"theme","label":"Theme","type":"secret"}`},
		{"bad visibility", `{"key":"theme","label":"Theme","type":"string","visibility":"world"}`},
		{"missing label", `{"key":"theme","label":"","type":"string"}`},
		{"select without options", `{"key":"theme","label":"Theme","type":"select"}`},
		{"select with empty option value", `{"key":"theme","label":"Theme","type":"select",
			"options":[{"value":"","label":"Empty"}]}`},
		{"select with duplicate option", `{"key":"theme","label":"Theme","type":"select",
			"options":[{"value":"dark","label":"Dark"},{"value":"dark","label":"Dark again"}]}`},
		{"select default outside options", `{"key":"theme","label":"Theme","type":"select","default":"purple",
			"options":[{"value":"dark","label":"Dark"}]}`},
		{"options on non-select", `{"key":"theme","label":"Theme","type":"string",
			"options":[{"value":"dark","label":"Dark"}]}`},
		{"constraints not an object", `{"key":"theme","label":"Theme","type":"string","constraints":[1,2]}`},
		{"number with text default", `{"key":"count","label":"Count","type":"number","default":"many"}`},
		{"boolean with text default", `{"key":"flag","label":"Flag","type":"boolean","default":"yes-please"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := settingsSectionBase()
			manifest.Contributions = []ManifestContribution{settingsSectionContribution(
				PointForumSettingsProfileTabs, "demo.settings.tab", 10,
				`{"type":"fields","fields":[`+testCase.field+`]}`,
			)}
			if err := validateSettingsSections(t, manifest); err == nil {
				t.Fatalf("%s must be rejected", testCase.name)
			}
		})
	}
}

func TestSettingsSectionRejectsDuplicateFieldKeys(t *testing.T) {
	manifest := settingsSectionBase()
	manifest.Contributions = []ManifestContribution{settingsSectionContribution(
		PointForumSettingsProfileTabs, "demo.settings.tab", 10,
		`{"type":"fields","fields":[
			{"key":"theme","label":"Theme","type":"string"},
			{"key":"theme","label":"Theme again","type":"string"}
		]}`,
	)}
	if err := validateSettingsSections(t, manifest); err == nil {
		t.Fatal("duplicate field keys inside one tab must be rejected")
	}
}

func TestSettingsSectionRejectsBadIcon(t *testing.T) {
	manifest := settingsSectionBase()
	manifest.Contributions = []ManifestContribution{settingsSectionContribution(
		PointForumSettingsProfileTabs, "demo.settings.tab", 10,
		`{"type":"fields","icon":"i-fontawesome-star","fields":[{"key":"theme","label":"Theme","type":"string"}]}`,
	)}
	if err := validateSettingsSections(t, manifest); err == nil {
		t.Fatal("payload icon must stay inside the host icon set")
	}
}

func TestSettingsSectionCaps(t *testing.T) {
	buildFields := func(count int) string {
		fields := make([]string, 0, count)
		for index := 0; index < count; index++ {
			fields = append(fields, fmt.Sprintf(`{"key":"f%d","label":"Field %d","type":"string"}`, index, index))
		}
		return `{"type":"fields","fields":[` + strings.Join(fields, ",") + `]}`
	}

	t.Run("per tab field cap", func(t *testing.T) {
		manifest := settingsSectionBase()
		manifest.Contributions = []ManifestContribution{settingsSectionContribution(
			PointForumSettingsProfileTabs, "demo.settings.tab", 10, buildFields(SettingsSectionFieldsPerTabMax+1),
		)}
		if err := validateSettingsSections(t, manifest); err == nil {
			t.Fatalf("%d fields in one tab must be rejected", SettingsSectionFieldsPerTabMax+1)
		}
	})

	t.Run("per extension field cap", func(t *testing.T) {
		manifest := settingsSectionBase()
		perTab := SettingsSectionFieldsPerTabMax
		tabs := SettingsSectionFieldsPerExtensionMax/perTab + 1
		manifest.Contributions = make([]ManifestContribution, 0, tabs)
		for index := 0; index < tabs; index++ {
			manifest.Contributions = append(manifest.Contributions,
				settingsSectionContribution(PointForumSettingsProfileTabs,
					fmt.Sprintf("demo.settings.tab%d", index), index, buildFields(perTab)))
		}
		if err := validateSettingsSections(t, manifest); err == nil {
			t.Fatalf("%d fields across one package must be rejected", perTab*tabs)
		}
	})

	t.Run("per surface tab cap", func(t *testing.T) {
		manifest := settingsSectionBase()
		manifest.Contributions = make([]ManifestContribution, 0, SettingsSectionTabsPerPointMax+1)
		for index := 0; index <= SettingsSectionTabsPerPointMax; index++ {
			manifest.Contributions = append(manifest.Contributions,
				settingsSectionContribution(PointForumSettingsProfileTabs,
					fmt.Sprintf("demo.settings.tab%d", index), index,
					`{"type":"fields","fields":[{"key":"theme","label":"Theme","type":"string"}]}`))
		}
		if err := validateSettingsSections(t, manifest); err == nil {
			t.Fatalf("%d tabs on one surface must be rejected", SettingsSectionTabsPerPointMax+1)
		}
	})

	t.Run("caps allow the declared maximum", func(t *testing.T) {
		manifest := settingsSectionBase()
		manifest.Contributions = []ManifestContribution{settingsSectionContribution(
			PointForumSettingsProfileTabs, "demo.settings.tab", 10, buildFields(SettingsSectionFieldsPerTabMax),
		)}
		if err := validateSettingsSections(t, manifest); err != nil {
			t.Fatalf("%d fields in one tab must be accepted: %v", SettingsSectionFieldsPerTabMax, err)
		}
	})
}

// CI fixture 必须一直是「可照抄的合法示例」：两个设置面都在，且分别用了
// fields / component / settings 三种形态。它同时保证 extensions/fixtures 下的
// 包与宿主校验器不漂移（作者复制它就能过 validate）。
func TestSettingsSectionFixturePackageValidates(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source),
		"../../../../../extensions/fixtures/plugins/sforum-settings-surface-demo"))
	body, err := os.ReadFile(filepath.Join(root, "sforum.extension.json"))
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	if err := Validate(manifest); err != nil {
		t.Fatalf("fixture manifest must validate: %v", err)
	}

	kinds := map[string]map[string]bool{}
	for _, contribution := range Normalize(manifest).Contributions {
		var payload SettingsSectionContributionPayload
		if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
			t.Fatalf("fixture contribution %q payload: %v", contribution.ID, err)
		}
		if kinds[contribution.Point] == nil {
			kinds[contribution.Point] = map[string]bool{}
		}
		kinds[contribution.Point][strings.ToLower(payload.Type)] = true
	}
	for _, expected := range []struct{ point, kind string }{
		{PointForumSettingsProfileTabs, SettingsSectionTabFields},
		{PointForumSettingsProfileTabs, SettingsSectionTabComponent},
		{PointAdminSettingsTabs, SettingsSectionTabSettings},
	} {
		if !kinds[expected.point][expected.kind] {
			t.Fatalf("fixture must declare a %s tab on %s", expected.kind, expected.point)
		}
	}
	if len(manifest.PermissionDefinitions) == 0 {
		t.Fatal("fixture must declare a permission its admin tab can reference")
	}
}

// 字段裸键唯一性是整包级的：两个 tab 用同名裸键会共用同一行 entity-meta 字段。
func TestSettingsSectionRejectsDuplicateFieldKeysAcrossTabs(t *testing.T) {
	manifest := settingsSectionBase()
	manifest.Contributions = []ManifestContribution{
		settingsSectionContribution(PointForumSettingsProfileTabs, "demo.settings.tab.a", 10,
			`{"type":"fields","fields":[{"key":"theme","label":"Theme","type":"string"}]}`),
		settingsSectionContribution(PointForumSettingsProfileTabs, "demo.settings.tab.b", 20,
			`{"type":"fields","fields":[{"key":"theme","label":"Theme again","type":"string"}]}`),
	}
	if err := validateSettingsSections(t, manifest); err == nil {
		t.Fatal("duplicate field key across tabs must be rejected")
	}
}

// 约束在 manifest 校验阶段就严格拒绝（同一解析器供同步复用）。
func TestSettingsSectionRejectsInvalidConstraintShapes(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"unknown key", `{"key":"theme","label":"Theme","type":"string","constraints":{"maxSize":3}}`},
		{"empty enum", `{"key":"theme","label":"Theme","type":"select","constraints":{"enum":[]},
			"options":[{"value":"dark","label":"Dark"}]}`},
		{"enum outside options", `{"key":"theme","label":"Theme","type":"select","constraints":{"enum":["dark","light"]},
			"options":[{"value":"dark","label":"Dark"}]}`},
		{"string with object value", `{"key":"theme","label":"Theme","type":"string","constraints":{"maxLength":{"a":1}}}`},
		{"number with maxLength", `{"key":"count","label":"Count","type":"number","constraints":{"maxLength":3}}`},
		{"boolean with enum", `{"key":"flag","label":"Flag","type":"boolean","constraints":{"enum":["a"]}}`},
		{"min greater than max", `{"key":"count","label":"Count","type":"number","constraints":{"min":5,"max":1}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := settingsSectionBase()
			manifest.Contributions = []ManifestContribution{settingsSectionContribution(
				PointForumSettingsProfileTabs, "demo.settings.tab", 10,
				`{"type":"fields","fields":[`+testCase.field+`]}`,
			)}
			if err := validateSettingsSections(t, manifest); err == nil {
				t.Fatalf("%s must be rejected", testCase.name)
			}
		})
	}
	// 正向对照：合法约束仍然通过。
	manifest := settingsSectionBase()
	manifest.Contributions = []ManifestContribution{settingsSectionContribution(
		PointForumSettingsProfileTabs, "demo.settings.tab", 10,
		`{"type":"fields","fields":[
			{"key":"about","label":"About","type":"string","constraints":{"maxLength":200}},
			{"key":"count","label":"Count","type":"number","constraints":{"min":1,"max":10}},
			{"key":"theme","label":"Theme","type":"select","constraints":{"enum":["dark","light"]},
			 "options":[{"value":"dark","label":"Dark"},{"value":"light","label":"Light"}]}
		]}`,
	)}
	if err := validateSettingsSections(t, manifest); err != nil {
		t.Fatalf("valid constraints must pass: %v", err)
	}
}
