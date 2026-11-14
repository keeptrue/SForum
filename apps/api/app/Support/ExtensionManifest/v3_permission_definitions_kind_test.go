package extensionmanifest

import (
	"encoding/json"
	"strings"
	"testing"
)

// 本文件是 M4a O1 的判定与断言：`permissionDefinitions` 只对 plugin 合法。
//
// 判定依据（schema 里已有同类「只允许插件」的先例，所以这是漏了一处，不是口径）：
//   - schemas/manifest-v3.schema.json:300-301  `if required identity` → `then.type.const = "plugin"`
//   - schemas/manifest-v3.schema.json:342-343  `if required badgeConditions` → `then.type.const = "plugin"`
//   - schemas/manifest-v3.schema.json:384-385  `if required userFactConditions` → `then.type.const = "plugin"`
//
// 因此 `permissionDefinitions` 补上同一族的 if/then（见 schema 里该块的注释），
// 主题夹具在 **schema 层**就拒；Go `Validate` 仍是权威（它同时覆盖结构与时序约束）。

// permissionDefinitionFixture 是一条合法的权限声明（同名在 plugin 上合法）。
func permissionDefinitionFixture() ManifestPermissionDefinition {
	return ManifestPermissionDefinition{
		Key:              "demo.v3.theme.apply",
		ContractVersion:  "demo.v3.theme.apply@1",
		Label:            LocalizedText{ByLocale: map[string]string{"zh-CN": "应用主题", "en-US": "Apply theme"}},
		Description:      LocalizedText{ByLocale: map[string]string{"zh-CN": "应用主题", "en-US": "Apply theme"}},
		AssignmentPolicy: "host",
	}
}

// themeWithPermissionDefinitionsFixture 是**真主题夹具**：一份最小合法清单 + 权限声明。
//
// 刻意不挂 content/seo/identity/badgeConditions/userFactConditions：那些 if/then 也能
// 把主题拒掉，混在一起就分不清到底是谁拒的——本用例要单点观察 permissionDefinitions 那条。
func themeWithPermissionDefinitionsFixture(t *testing.T) Manifest {
	t.Helper()
	manifest := versionedTestManifest(ManifestVersionV3)
	manifest.ID = "demo.v3.theme"
	manifest.Type = TypeTheme
	manifest.PermissionDefinitions = []ManifestPermissionDefinition{permissionDefinitionFixture()}
	return manifest
}

// TestManifestV3SchemaRejectsPermissionDefinitionsOnThemes 是 O1 的两侧判据。
//
// 正反各一：
//   - 主题 + permissionDefinitions → Go Validate 拒、ValidateV3JSONSchema 拒；
//   - 同一份清单改成 plugin     → 两侧都过。
//
// 为什么要求 schema 侧也拒：装载链是「schema → Go Validate」，只靠后一步兜住时，
// 任何在 schema 之后、Validate 之前消费清单的路径（预览、编辑器、未来的静态校验）
// 都会看到一份「schema 认为合法」的主题。先例三条都写在 schema 层，这里不享受豁免。
func TestManifestV3SchemaRejectsPermissionDefinitionsOnThemes(t *testing.T) {
	theme := themeWithPermissionDefinitionsFixture(t)
	themeBody, err := json.Marshal(theme)
	if err != nil {
		t.Fatal(err)
	}
	goErr := Validate(theme)
	schemaErr := ValidateV3JSONSchema(themeBody)
	t.Logf("theme(permissionDefinitions): Validate=%v | ValidateV3JSONSchema=%v", goErr, schemaErr)
	if goErr == nil {
		t.Fatal("Go Validate 必须拒绝主题声明 permissionDefinitions")
	}
	if schemaErr == nil {
		t.Fatal("schema 层也必须拒绝主题声明 permissionDefinitions（先例见文件头三条）")
	}
	if !strings.Contains(schemaErr.Error(), "plugin") {
		t.Fatalf("schema 的拒绝理由应指向 type=plugin，得到 %v", schemaErr)
	}

	// 正面：同一份清单、只把 kind 换成 plugin，两侧都应通过（否则是过度限制）。
	plugin := theme
	plugin.Type = TypePlugin
	pluginBody, err := json.Marshal(plugin)
	if err != nil {
		t.Fatal(err)
	}
	pluginGoErr := Validate(plugin)
	pluginSchemaErr := ValidateV3JSONSchema(pluginBody)
	t.Logf("plugin(permissionDefinitions): Validate=%v | ValidateV3JSONSchema=%v", pluginGoErr, pluginSchemaErr)
	if pluginGoErr != nil {
		t.Fatalf("插件声明同名权限必须合法：%v", pluginGoErr)
	}
	if pluginSchemaErr != nil {
		t.Fatalf("插件声明同名权限在 schema 层也必须合法：%v", pluginSchemaErr)
	}
}

// TestManifestV3SchemaDeclaresPermissionDefinitionsPluginOnly 是结构性断言：
// 这条约束真的写在 allOf 里，而不是被别的字段偶然命中。
func TestManifestV3SchemaDeclaresPermissionDefinitionsPluginOnly(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(ManifestV3JSONSchema(), &schema); err != nil {
		t.Fatalf("schema 不是合法 JSON：%v", err)
	}
	branches, _ := schema["allOf"].([]any)
	for _, branch := range branches {
		entry, _ := branch.(map[string]any)
		condition, _ := entry["if"].(map[string]any)
		required, _ := condition["required"].([]any)
		if len(required) != 1 || required[0] != "permissionDefinitions" {
			continue
		}
		consequence, _ := entry["then"].(map[string]any)
		properties, _ := consequence["properties"].(map[string]any)
		kind, _ := properties["type"].(map[string]any)
		if kind["const"] == "plugin" {
			return
		}
	}
	t.Fatal("manifest-v3 schema 缺少 `if required permissionDefinitions → then.type.const = plugin` 分支")
}
