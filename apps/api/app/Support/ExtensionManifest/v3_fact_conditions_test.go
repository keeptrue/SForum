package extensionmanifest

import (
	"encoding/json"
	"strings"
	"testing"
)

// `userFactConditions` 是事实条件的中性别名（`badgeConditions` 是兼容名）。两个数组
// 共用同一套校验与同一个上限。
//
// **两份契约必须同批**：Go struct 与 `schemas/manifest-v3.schema.json`。本仓没有
// reflect 式对齐测试，所以本文件对**两条路径同时**断言：
//   - 只改 struct 不改 schema → `ValidateV3JSONSchema`（装真 zip 时走的那条）会因根
//     `additionalProperties:false` 拒包；
//   - 只改 schema 不改 struct → `encoding/json` 静默丢弃声明，Go 侧候选集合为空。
//
// 两个方向各做一次变异（删字段 / 删 schema 属性）都必须让本文件红，这是唯一能证明
// 「两侧都在」的手段。

func userFactConditionManifest() Manifest {
	manifest := completeV3Manifest()
	manifest.UserFactConditions = []ManifestBadgeCondition{validBadgeCondition()}
	return manifest
}

// TestUserFactConditionsAcceptedByBothPaths 是双侧锚：Go 校验与 JSON schema 同时通过。
func TestUserFactConditionsAcceptedByBothPaths(t *testing.T) {
	manifest := userFactConditionManifest()
	if err := Validate(manifest); err != nil {
		t.Fatalf("Go 校验拒绝了 userFactConditions：%v", err)
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := ValidateV3JSONSchema(body); err != nil {
		t.Fatalf("JSON schema 拒绝了 userFactConditions（若刚加 struct 未加 schema，就是这个错）：%v", err)
	}
	// 反方向：Go 侧必须真的**看见**它（否则「schema 通过、声明被 json 静默丢弃」会绿）。
	var roundTrip Manifest
	if err := json.Unmarshal(body, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(roundTrip.UserFactConditions) != 1 {
		t.Fatalf("声明被静默丢弃：%d 条", len(roundTrip.UserFactConditions))
	}
}

// TestFactConditionsShareCombinedLimit 钉住「合计 ≤32」：两个数组各 20 条 = 40 → 拒。
func TestFactConditionsShareCombinedLimit(t *testing.T) {
	condition := func(suffix string) ManifestBadgeCondition {
		item := validBadgeCondition()
		item.Key = "demo.v3.bulk_" + suffix
		item.ContractVersion = item.Key + "@1"
		return item
	}
	manifest := completeV3Manifest()
	for index := 0; index < 20; index++ {
		manifest.BadgeConditions = append(manifest.BadgeConditions, condition("b"+string(rune('a'+index))))
	}
	for index := 0; index < 20; index++ {
		manifest.UserFactConditions = append(manifest.UserFactConditions, condition("u"+string(rune('a'+index))))
	}
	err := Validate(manifest)
	if err == nil {
		t.Fatal("两个数组合计 40 条必须被拒（单条规则上限 32 叶）")
	}
	if !strings.Contains(err.Error(), "rule #3") {
		t.Fatalf("应报合计上限 rule #3，得到 %v", err)
	}
}

// TestFactConditionsRejectCrossArrayDuplicateKey 钉住「key 跨数组全局唯一」。
func TestFactConditionsRejectCrossArrayDuplicateKey(t *testing.T) {
	manifest := completeV3Manifest()
	manifest.BadgeConditions = []ManifestBadgeCondition{validBadgeCondition()}
	manifest.UserFactConditions = []ManifestBadgeCondition{validBadgeCondition()}
	if err := Validate(manifest); err == nil {
		t.Fatal("同一个 key 同时出现在两个数组里必须被拒（v.ids 共享注册表）")
	}
}

// TestUserFactConditionsRequireBackend 钉住 rule #2 对别名数组同样生效。
func TestUserFactConditionsRequireBackend(t *testing.T) {
	manifest := userFactConditionManifest()
	manifest.Backend.Entry = ""
	manifest.Backend.ProtocolVersion = 0
	if err := Validate(manifest); err == nil {
		t.Fatal("没有 backend 的扩展不能声明事实条件")
	}
}

// TestTrustLevelEarnableIsStructurallyPluginOnly 钉住「主题不可能声明它」。
//
// 这不是靠一条 if 拦的，而是**结构保证**：`permissionDefinitions` 本身只对 plugin 合法，
// 主题带它会在类型校验阶段被拒。所以主题永远拿不到这个字段——比加一条 `TypePlugin`
// 判断更可靠（那种判断在结构变化后会变成死代码）。
func TestTrustLevelEarnableIsStructurallyPluginOnly(t *testing.T) {
	manifest := completeV3Manifest()
	manifest.Type = TypeTheme
	manifest.PermissionDefinitions = []ManifestPermissionDefinition{{
		Key:                "demo.v3.theme.apply",
		ContractVersion:    "demo.v3.theme.apply@1",
		Label:              LocalizedText{ByLocale: map[string]string{"zh-CN": "应用主题", "en-US": "Apply theme"}},
		Description:        LocalizedText{ByLocale: map[string]string{"zh-CN": "应用主题", "en-US": "Apply theme"}},
		AssignmentPolicy:   "host",
		TrustLevelEarnable: true,
	}}
	if err := Validate(manifest); err == nil {
		t.Fatal("主题带 permissionDefinitions 必须被拒（无论是否声明 trustLevelEarnable）")
	}
}

// TestTrustLevelEarnableSchemaProperty 钉住 schema 那一侧也在（只改 struct 的变异会红）。
func TestTrustLevelEarnableSchemaProperty(t *testing.T) {
	manifest := completeV3Manifest()
	manifest.PermissionDefinitions = []ManifestPermissionDefinition{{
		Key:                "demo.v3.orders.read",
		ContractVersion:    "demo.v3.orders.read@1",
		Label:              LocalizedText{ByLocale: map[string]string{"zh-CN": "读订单", "en-US": "Read orders"}},
		Description:        LocalizedText{ByLocale: map[string]string{"zh-CN": "读订单", "en-US": "Read orders"}},
		AssignmentPolicy:   "host",
		TrustLevelEarnable: true,
	}}
	if err := Validate(manifest); err != nil {
		t.Fatalf("Go 校验拒绝了 trustLevelEarnable：%v", err)
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"trustLevelEarnable":true`) {
		t.Fatalf("字段没被序列化：%s", body)
	}
	if err := ValidateV3JSONSchema(body); err != nil {
		t.Fatalf("schema 不接受 trustLevelEarnable（schema 未同批改）：%v", err)
	}
}

// TestUserFactConditionsSurviveRawPluginJSON 是「只改 schema 不改 struct」的静默丢失守卫。
//
// 上一条测试从 Go struct 出发，抓不到这个方向：插件的**原始 JSON** 里带
// `userFactConditions`、而 struct 没有这个字段时，`encoding/json` 会静默忽略它——
// 插件作者以为声明成功，候选目录里却永远是空的，且没有任何报错。
//
// 做法：拿一份合法清单的 JSON，**手工注入**一个 `userFactConditions` 键（不经过
// struct），再解码到新的 Manifest 上断言它真的被填上。struct 缺字段时这里必为空。
func TestUserFactConditionsSurviveRawPluginJSON(t *testing.T) {
	base, err := json.Marshal(completeV3Manifest())
	if err != nil {
		t.Fatalf("marshal base: %v", err)
	}
	injected := `{"key":"demo.v3.onboarding.tasks","contractVersion":"demo.v3.onboarding.tasks@1",` +
		`"label":{"zh-CN":"完成新手任务数","en-US":"Completed onboarding tasks"},"compare":["gte"]}`
	body := strings.Replace(string(base), `"packageFiles"`, `"userFactConditions":[`+injected+`],"packageFiles"`, 1)
	if !strings.Contains(body, `"userFactConditions"`) {
		t.Fatal("夹具注入失败：base 清单里没有 packageFiles 锚点")
	}

	var manifest Manifest
	if err := json.Unmarshal([]byte(body), &manifest); err != nil {
		t.Fatalf("unmarshal injected manifest: %v", err)
	}
	if len(manifest.UserFactConditions) != 1 {
		t.Fatalf("原始 JSON 里的 userFactConditions 被静默丢弃（struct 缺字段）：%d 条", len(manifest.UserFactConditions))
	}
	if manifest.UserFactConditions[0].Key != "demo.v3.onboarding.tasks" {
		t.Fatalf("key 解析错：%q", manifest.UserFactConditions[0].Key)
	}
	// 注入后的清单也必须能过两条校验路径（声明是合法的）。
	if err := Validate(manifest); err != nil {
		t.Fatalf("Go 校验拒绝了注入的声明：%v", err)
	}
	if err := ValidateV3JSONSchema([]byte(body)); err != nil {
		t.Fatalf("schema 拒绝了注入的声明：%v", err)
	}
}
