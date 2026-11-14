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

// notificationTypeDeclarationFixture 是 demo.v3 命名空间下的合规通知类型声明。
//
// 不复用真实通知夹具的 ID：`versionedID` 要求 `<manifest.id>.` 前缀，把
// `sforum.notification-reference.*` 挂到 demo.v3 上会被 Go 侧拒，反而让「插件侧必须
// 合法」这条前提失效。真实夹具的双侧判据由
// `TestManifestV3SchemaRequiresPluginForNotificationTypes` 单独覆盖。
func notificationTypeDeclarationFixture() ManifestNotificationType {
	return ManifestNotificationType{
		ID:              "demo.v3.notification.order_ready",
		ContractVersion: "demo.v3.notification.order_ready@1",
		PayloadVersion:  1,
		Category:        "commerce",
		PayloadSchema:   "demo.v3.notification.order_ready.schema@1",
		Label: LocalizedText{
			Default:  "Order ready",
			ByLocale: map[string]string{"zh-CN": "订单已就绪", "en-US": "Order ready"},
		},
		Body: LocalizedText{
			Default:  "Your order is ready.",
			ByLocale: map[string]string{"zh-CN": "你的订单已就绪。", "en-US": "Your order is ready."},
		},
		Icon:       "i-lucide-package-check",
		TargetKind: "none",
		Channels:   []string{"in_app"},
	}
}

// factConditionDeclarationFixture 造一条合规的事实条件声明（两个数组共用一套规则）。
func factConditionDeclarationFixture(key string) ManifestBadgeCondition {
	return ManifestBadgeCondition{
		Key:             key,
		ContractVersion: key + "@1",
		Label:           map[string]string{"zh-CN": "页数", "en-US": "Pages"},
		Compare:         []string{"gte"},
	}
}

// pluginOnlyBaseManifest 是「只留一个家族」的基座。
//
// `completeV3Manifest` 本身能过 Validate；这里把 7 个 plugin-only 家族清空，
// 让观察点只有被测家族（其余声明原样保留，避免误删出「形状不合法」的第二个变量）。
func pluginOnlyBaseManifest() Manifest {
	base := completeV3Manifest()
	base.Type = TypePlugin
	base.Content = nil
	base.SEO = nil
	base.Identity = nil
	base.AITools, base.NotificationTypes = nil, nil
	base.BadgeConditions, base.UserFactConditions = nil, nil
	base.PermissionDefinitions = nil
	return base
}

// v3PluginOnlyFamilyAppliers 给清单里的每个家族配一个「只声明该家族」的夹具。
//
// 每个声明都逐字合规：插件侧必须两侧都过，否则主题侧的「schema 拒」可能只是形状
// 不合规，那条断言会变成空转。
func v3PluginOnlyFamilyAppliers(t *testing.T) map[string]func(*Manifest) {
	t.Helper()
	return map[string]func(*Manifest){
		"aiTools": func(m *Manifest) { m.AITools = []ManifestAITool{aiToolFixture(t)} },
		"notificationTypes": func(m *Manifest) {
			m.NotificationTypes = []ManifestNotificationType{notificationTypeDeclarationFixture()}
			// 通知类型的 payloadSchema 必须落在同包 schema 文件上（validateUIAndPackage
			// rule #11），所以这条声明要自带一个 packageFile 才在插件侧合法。
			m.PackageFiles = append(m.PackageFiles, ManifestPackageFile{
				ID:   "demo.v3.notification.order_ready.schema",
				Kind: "schema", Path: "schemas/order-ready.json", Digest: v3FixtureDigest(), Version: "1",
			})
		},
		"seo": func(m *Manifest) { m.SEO = []ManifestSEO{seoDeclarationFixture()} },
		"identity": func(m *Manifest) {
			// 刻意**不带 providers**：这正是 schema 条件分支的洞——旧分支只在
			// 「identity 且带 providers」时才要求 plugin，主题不带 providers 时放行。
			m.Identity = &ManifestIdentity{ContractVersion: "demo.v3.identity@1"}
		},
		"badgeConditions": func(m *Manifest) {
			m.BadgeConditions = []ManifestBadgeCondition{factConditionDeclarationFixture("demo.v3.condition.badges")}
		},
		"userFactConditions": func(m *Manifest) {
			m.UserFactConditions = []ManifestBadgeCondition{factConditionDeclarationFixture("demo.v3.condition.facts")}
		},
		"permissionDefinitions": func(m *Manifest) {
			m.PermissionDefinitions = []ManifestPermissionDefinition{{
				Key: "demo.v3.manage", ContractVersion: "demo.v3.permission.manage@1",
				Label:            LocalizedText{Default: "Manage demo"},
				Description:      LocalizedText{Default: "Manage the demo plugin."},
				RecommendedRoles: []string{"administrator"}, AssignmentPolicy: "host",
			}}
		},
	}
}

// TestManifestV3PluginOnlyFamiliesBehaviourallyRejectThemes 是「清单 ⇄ 行为」的判据。
//
// 每个家族四件事一起断：
//  1. `Declared` 认得出本家族的声明（谓词读错字段 → 红）；
//  2. 插件声明该家族时两侧都过（证明夹具合规，第 3 条才有意义）；
//  3. 主题声明该家族时 schema 拒（分支缺失，或分支带额外条件 → 红）；
//  4. 主题声明该家族时 Go 侧**首个**报错指名该家族（清单不是首个判据 → 红）。
//
// 第 4 条是「清单真的第一」的证据：`isThemeManifestSupported` 抢先返回时，报错是裸
// `extensions: invalid manifest`，不含任何家族名。
func TestManifestV3PluginOnlyFamiliesBehaviourallyRejectThemes(t *testing.T) {
	appliers := v3PluginOnlyFamilyAppliers(t)
	for _, family := range v3PluginOnlyDeclarationFamilies {
		t.Run(family.Family, func(t *testing.T) {
			apply, ok := appliers[family.Family]
			if !ok {
				t.Fatalf("家族 %q 没有行为夹具：清单新增家族必须同时补一条", family.Family)
			}

			plugin := pluginOnlyBaseManifest()
			apply(&plugin)
			if !family.Declared(plugin) {
				t.Fatalf("家族 %q 的 Declared 谓词没有识别出本家族的声明（谓词读错字段？）", family.Family)
			}
			pluginBody, err := json.Marshal(plugin)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(plugin); err != nil {
				t.Fatalf("插件声明 %q 必须合法：%v", family.Family, err)
			}
			if err := ValidateV3JSONSchema(pluginBody); err != nil {
				t.Fatalf("插件声明 %q 在 schema 层也必须合法：%v", family.Family, err)
			}

			theme := pluginOnlyBaseManifest()
			theme.Type = TypeTheme
			apply(&theme)
			themeBody, err := json.Marshal(theme)
			if err != nil {
				t.Fatal(err)
			}
			if schemaErr := ValidateV3JSONSchema(themeBody); schemaErr == nil {
				t.Fatalf("主题声明 %q 时 schema 层放行（分支可能带额外条件）", family.Family)
			} else {
				t.Logf("%s: schema → %v", family.Family, schemaErr)
			}
			goErr := Validate(theme)
			if goErr == nil {
				t.Fatalf("Go Validate 必须拒绝主题声明 %q", family.Family)
			}
			if !strings.Contains(goErr.Error(), family.Family) {
				t.Fatalf("主题声明 %q 时首个报错应指名该家族（清单不是首个判据？），得到 %v", family.Family, goErr)
			}
		})
	}
}

// TestManifestV3SchemaRequiresPluginForNotificationTypes 是 notificationTypes 的双侧判据。
//
// 用**真实通知夹具包**（Go 侧本来就能装载）当插件侧证据，再把它翻成 theme 看两侧是否都拒。
// 这条在 0b38458bd 里被静默删除过（helper 因此零调用），恢复它是本次修的一部分：
// notificationTypes 是唯一有真实包夹具的家族，删掉就只剩手搓声明。
func TestManifestV3SchemaRequiresPluginForNotificationTypes(t *testing.T) {
	plugin := loadNotificationReferenceFixture(t)
	pluginBody, err := json.Marshal(plugin)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(plugin); err != nil {
		t.Fatalf("真实通知夹具在 Go 侧必须合法：%v", err)
	}
	if err := ValidateV3JSONSchema(pluginBody); err != nil {
		t.Fatalf("真实通知夹具在 schema 层也必须合法：%v", err)
	}

	theme := plugin
	theme.Type = TypeTheme
	themeBody, err := json.Marshal(theme)
	if err != nil {
		t.Fatal(err)
	}
	goErr := Validate(theme)
	schemaErr := ValidateV3JSONSchema(themeBody)
	t.Logf("notificationTypes: theme → Validate=%v | ValidateV3JSONSchema=%v", goErr, schemaErr)
	if goErr == nil {
		t.Fatal("Go Validate 必须拒绝主题声明 notificationTypes")
	}
	if schemaErr == nil {
		t.Fatal("schema 层必须拒绝主题声明 notificationTypes")
	}
	if !strings.Contains(schemaErr.Error(), "plugin") {
		t.Fatalf("schema 的拒绝理由应指向 type=plugin，得到 %v", schemaErr)
	}
}
