package extensionmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// 本文件补上 task-31 的三处同类缺口：`seo` / `aiTools` / `notificationTypes`。
//
// 定性依据（task-8 已复核）：全仓 `ValidateV3JSONSchema` 的非测试调用点只有
// `load.go:99` 与 `:116`，且同一条 `loadManifestV3` 链上 `:120 Validate(manifest)`
// 必跑 —— 所以这三处是**潜在缺口、当前不可利用**，与 `permissionDefinitions` 修前同型。
//
// 口径（按家族分别决定）：
//   - `seo`：Go 侧已要求 plugin + backend（v3_validate_platform.go:274），schema 只有
//     backend ⇒ 补 `type.const=plugin` 是**纯对齐**，不新增限制。
//   - `aiTools`：Go 侧要求 plugin + backend(:88) ⇒ schema 补同一对（kind + backend），
//     与 Go 判据等价。夹具矩阵（testdata/v3 + 全部 v3 测试夹具 + 内置清单）里没有
//     任何「Go 合法、schema 会拒」的包 —— 因为 Go 本来就拒它们。
//   - `notificationTypes`：Go 侧只要求 plugin(:190)，不要求 backend ⇒ schema 只补 kind，
//     刻意不加 backend 需求（那会收出一个 Go 不认的限制）。

// seoDeclarationFixture 源自 v3_manifest_test.go 的合法 SEO 声明。
func seoDeclarationFixture() ManifestSEO {
	return ManifestSEO{
		ID: "demo.v3.seo.topic-title", ContractVersion: "demo.v3.seo.topic-title@1",
		Scope: "core.page.topic", Kind: "title", Action: "filter", Handler: "demo.v3.seo.topic-title",
		Priority: 100, FailurePolicy: "fallback", TimeoutMS: 500,
	}
}

// loadNotificationReferenceFixture 装载真实通知夹具包（extensions/fixtures/
// plugins/sforum-notification-reference）。它本来就是 Go 侧可装载的清单，
// 所以「插件侧两侧都必须过」这条可以直接用它证明，不必手搓一份可能不合规的声明。
func loadNotificationReferenceFixture(t *testing.T) Manifest {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve fixture test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../../../../extensions/fixtures/plugins/sforum-notification-reference"))
	templateBody, err := os.ReadFile(filepath.Join(root, "sforum.extension.json.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	schemaBody, err := os.ReadFile(filepath.Join(root, "schemas/order-ready.json"))
	if err != nil {
		t.Fatal(err)
	}
	binaryBody := []byte("notification-reference-fixture-binary")
	binaryDigest := sha256.Sum256(binaryBody)
	schemaDigest := sha256.Sum256(schemaBody)
	body := strings.ReplaceAll(string(templateBody), "__BACKEND_DIGEST__", hex.EncodeToString(binaryDigest[:]))
	body = strings.ReplaceAll(body, "__PAYLOAD_SCHEMA_DIGEST__", hex.EncodeToString(schemaDigest[:]))
	manifest, err := LoadRootBytes([]byte(body), FileMapFS{
		ManifestFileName: []byte(body), "backend/plugin": binaryBody,
		"schemas/order-ready.json": schemaBody,
	})
	if err != nil {
		t.Fatalf("装载真实通知夹具失败：%v", err)
	}
	return manifest
}

// aiToolFixture 取自 testdata/v3/ai-tool-plugin.json（真夹具）。
func aiToolFixture(t *testing.T) ManifestAITool {
	t.Helper()
	raw, err := os.ReadFile("testdata/v3/ai-tool-plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		AITools []ManifestAITool `json:"aiTools"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.AITools) == 0 {
		t.Fatal("ai-tool-plugin.json 里没有 aiTools")
	}
	// 真夹具的 ID 属于 `fixture.ai-tool` 家族；本用例的基座是 demo.v3，
	// 所以把 ID/handler 挪到同一家族下，payload 结构保持原样。
	tool := fixture.AITools[0]
	tool.ID = "demo.v3.ai-tool.order.lookup"
	tool.ContractVersion = "demo.v3.ai-tool.order.lookup@1"
	tool.Handler = "demo.v3.ai-tool.order.lookup"
	return tool
}

// TestManifestV3SchemaKindFamiliesRejectThemes 是三处缺口的双侧判据。
//
// 每个家族都走两趟：主题（必须 Go 拒 + schema 拒）与插件（两侧都必须过）。
// 插件那趟同时证明我没有收紧出「Go 不认的限制」。
func TestManifestV3SchemaKindFamiliesRejectThemes(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*Manifest)
	}{
		{"seo", func(m *Manifest) { m.SEO = []ManifestSEO{seoDeclarationFixture()} }},
		{"aiTools", func(m *Manifest) { m.AITools = []ManifestAITool{aiToolFixture(t)} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// 正面：插件 + 该家族声明 → 两侧都过。
			// 基座用 completeV3Manifest（它本身能过 Validate），再清掉其它会触发的家族，
			// 保证观察点只有本家族。
			plugin := completeV3Manifest()
			plugin.Type = TypePlugin
			plugin.Content, plugin.SEO, plugin.Identity = nil, nil, nil
			plugin.BadgeConditions, plugin.UserFactConditions = nil, nil
			plugin.PermissionDefinitions = nil
			plugin.AITools, plugin.NotificationTypes = nil, nil
			testCase.apply(&plugin)
			pluginBody, err := json.Marshal(plugin)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(plugin); err != nil {
				t.Fatalf("插件声明 %s 必须合法：%v", testCase.name, err)
			}
			if err := ValidateV3JSONSchema(pluginBody); err != nil {
				t.Fatalf("插件声明 %s 在 schema 层也必须合法：%v", testCase.name, err)
			}

			// 反面：同一份声明、kind 换成 theme → 两侧都拒，且 schema 的理由指向 plugin。
			theme := plugin
			theme.Type = TypeTheme
			testCase.apply(&theme)
			themeBody, err := json.Marshal(theme)
			if err != nil {
				t.Fatal(err)
			}
			goErr := Validate(theme)
			schemaErr := ValidateV3JSONSchema(themeBody)
			t.Logf("%s: theme → Validate=%v | ValidateV3JSONSchema=%v", testCase.name, goErr, schemaErr)
			if goErr == nil {
				t.Fatalf("Go Validate 必须拒绝主题声明 %s", testCase.name)
			}
			if schemaErr == nil {
				t.Fatalf("schema 层必须拒绝主题声明 %s（先例见 task-8）", testCase.name)
			}
			if !strings.Contains(schemaErr.Error(), "plugin") {
				t.Fatalf("schema 的拒绝理由应指向 type=plugin，得到 %v", schemaErr)
			}
		})
	}
}

// TestManifestV3SchemaKindFamiliesMatchDeclarationTable 是「新增家族忘写 schema」的判据。
//
// 比对的两侧都不再手写：左侧是生产侧的 `v3PluginOnlyDeclarationFamilies`（运行期判据
// 的唯一清单），右侧是从 schema 分支扫出来的家族集合。于是：
//   - 「清单里加了家族、schema 没加分支」→ 红并指名；
//   - 「schema 加了分支、清单没加」→ 也红（防止两边各自漂移）。
//
// 选 A（与 Go 校验入口同源）而不是 B（手写清单 + 维护说明）的理由：手写清单只能抓
// 「已登记家族丢分支」，抓不到「新增家族根本不进清单」——后者正是下一个加 plugin-only
// 字段的人会踩的坑，而运行期判据已经从清单来，清单就是唯一事实源。
func TestManifestV3SchemaKindFamiliesMatchDeclarationTable(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(ManifestV3JSONSchema(), &schema); err != nil {
		t.Fatalf("schema 不是合法 JSON：%v", err)
	}
	schemaFamilies := map[string]string{}
	branches, _ := schema["allOf"].([]any)
	for _, branch := range branches {
		entry, _ := branch.(map[string]any)
		condition, _ := entry["if"].(map[string]any)
		required, _ := condition["required"].([]any)
		if len(required) != 1 {
			continue
		}
		family, _ := required[0].(string)
		consequence, _ := entry["then"].(map[string]any)
		properties, _ := consequence["properties"].(map[string]any)
		kind, _ := properties["type"].(map[string]any)
		if value, ok := kind["const"].(string); ok {
			schemaFamilies[family] = value
		}
	}

	tableFamilies := map[string]struct{}{}
	for _, family := range v3PluginOnlyDeclarationFamilies {
		tableFamilies[family.Family] = struct{}{}
		if kind, exists := schemaFamilies[family.Family]; !exists {
			t.Errorf("家族 %q 在 plugin-only 清单里，但 schema 没有 if/then 的 type.const=plugin 分支", family.Family)
		} else if kind != "plugin" {
			t.Errorf("家族 %q 的 schema 约束是 %q，期望 plugin", family.Family, kind)
		}
	}
	for family := range schemaFamilies {
		if _, exists := tableFamilies[family]; !exists {
			t.Errorf("schema 有 %q 的 plugin-only 分支，但生产清单 v3PluginOnlyDeclarationFamilies 里没有它（两边漂移）", family)
		}
	}

	// 清单里的家族名必须是真的 manifest 字段（JSON 名），否则 Declared 谓词是死代码。
	fields := map[string]struct{}{}
	manifestType := reflect.TypeOf(Manifest{})
	for index := 0; index < manifestType.NumField(); index++ {
		name := strings.Split(manifestType.Field(index).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			fields[name] = struct{}{}
		}
	}
	for family := range tableFamilies {
		if _, exists := fields[family]; !exists {
			t.Errorf("清单里的家族 %q 不是 Manifest 的 JSON 字段", family)
		}
	}
}
