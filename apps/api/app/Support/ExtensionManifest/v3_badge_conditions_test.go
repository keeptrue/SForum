package extensionmanifest

import (
	"encoding/json"
	"strings"
	"testing"
)

// 事实条件声明的两条校验路径必须**同时**生效：纯 Go 的 `Validate` 与装载真实
// .zip 走 `ValidateV3JSONSchema`。本仓没有 struct↔schema 的对齐测试（见校验器
// 注释），所以「只改一侧」这个变异必须让其中一条路红——本文件就是那条锚。

func validBadgeCondition() ManifestBadgeCondition {
	return ManifestBadgeCondition{
		Key:             "demo.v3.onboarding.tasks",
		ContractVersion: "demo.v3.onboarding.tasks@1",
		Label:           map[string]string{"zh-CN": "完成新手任务数", "en-US": "Completed onboarding tasks"},
		Unit:            map[string]string{"zh-CN": "个", "en-US": "tasks"},
		Compare:         []string{"gte", "lte"},
		Variants: []ManifestBadgeConditionVariant{
			{Value: "tutorial", Label: map[string]string{"zh-CN": "教程任务", "en-US": "Tutorial tasks"}},
			{Value: "profile", Label: map[string]string{"zh-CN": "资料完善任务", "en-US": "Profile tasks"}},
		},
	}
}

func badgeConditionManifest() Manifest {
	manifest := completeV3Manifest()
	manifest.BadgeConditions = []ManifestBadgeCondition{validBadgeCondition()}
	return manifest
}

// TestBadgeConditionsAcceptTwoShapes 断言两种合法形态：带 variants + compare 全集、
// 以及「是否发生过」的 gte-only 形态（不需要 valueKind）。
func TestBadgeConditionsAcceptTwoShapes(t *testing.T) {
	manifest := badgeConditionManifest()
	single := validBadgeCondition()
	single.Key = "demo.v3.verified_reports"
	single.ContractVersion = "demo.v3.verified_reports@1"
	single.Compare = []string{"gte"}
	single.Variants = nil
	single.Unit = nil
	manifest.BadgeConditions = append(manifest.BadgeConditions, single)

	if err := Validate(manifest); err != nil {
		t.Fatalf("合法声明必须通过 Go 校验，得到 %v", err)
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV3JSONSchema(body); err != nil {
		t.Fatalf("合法声明必须通过 schema 校验，得到 %v", err)
	}
}

// TestBadgeConditionsRejections 覆盖冻结规范 §2.4 的六类拒绝。
//
// 每类都同时走 Go 与 schema 两条路径：只改一侧的实现（典型事故是「加了 struct
// 没改 schema」）必须在这里红。schema 对 `compare: []` 走 minItems:1、对 17 个
// variants 走 maxItems:16、对 label 缺 en 走 required、对无前缀 key 走 pattern，
// 因此六类里除了「缺 backend」「type 非 plugin」只由 Go 判，其余两侧都能拒。
func TestBadgeConditionsRejections(t *testing.T) {
	seventeen := make([]ManifestBadgeConditionVariant, 0, 17)
	for index := 0; index < 17; index++ {
		seventeen = append(seventeen, ManifestBadgeConditionVariant{
			Value: "v" + strings.Repeat("x", index),
			Label: map[string]string{"zh-CN": "变体", "en-US": "variant"},
		})
	}
	cases := []struct {
		name       string
		mutate     func(*Manifest)
		wantGoFail bool
		wantJSFail bool
	}{
		{
			name:       "缺 backend",
			mutate:     func(m *Manifest) { m.Backend = ManifestBackend{} },
			wantGoFail: true,
			wantJSFail: true, // `allOf` 的 then 分支要求 backend.entry，不是 `$defs.backend` 自带 minLength
		},
		// 注意：`rule #1`（type != plugin）在实际装载顺序里不可达——主题带 backend 会先被
		// 主题校验拒。这里只断言「Go 路径拒绝」，**不**绑定规则编号：编号是纵深防御的
		// 内部细节，绑上去会让「哪天调整校验顺序」变成假红。
		{
			name: "非插件类型",
			mutate: func(m *Manifest) {
				m.Type = TypeTheme
			},
			wantGoFail: true,
			wantJSFail: true, // then 分支里 type 必须是 plugin（与 identity 声明同款）
		},
		{
			name: "key 无扩展前缀",
			mutate: func(m *Manifest) {
				m.BadgeConditions[0].Key = "onboarding.tasks"
			},
			wantGoFail: true,
			wantJSFail: false, // schema 只校验字符集，前缀是 versionedID 的职责
		},
		{
			name: "contractVersion 漂移",
			mutate: func(m *Manifest) {
				m.BadgeConditions[0].ContractVersion = "demo.v3.condition.tasks@1"
			},
			wantGoFail: true,
			wantJSFail: false, // schema 只校验 `id@N` 形态
		},
		{
			name:       "compare 为空",
			mutate:     func(m *Manifest) { m.BadgeConditions[0].Compare = []string{} },
			wantGoFail: true,
			wantJSFail: true,
		},
		{
			name:       "17 个 variants",
			mutate:     func(m *Manifest) { m.BadgeConditions[0].Variants = seventeen },
			wantGoFail: true,
			wantJSFail: true,
		},
		{
			name: "label 缺 en-US",
			mutate: func(m *Manifest) {
				m.BadgeConditions[0].Label = map[string]string{"zh-CN": "只有中文"}
			},
			wantGoFail: true,
			wantJSFail: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := badgeConditionManifest()
			tc.mutate(&manifest)

			goErr := Validate(manifest)
			if tc.wantGoFail && goErr == nil {
				t.Fatal("Go 校验必须拒绝")
			}
			if !tc.wantGoFail && goErr != nil {
				t.Fatalf("Go 校验不该拒绝：%v", goErr)
			}

			body, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			jsErr := ValidateV3JSONSchema(body)
			if tc.wantJSFail && jsErr == nil {
				t.Fatal("schema 校验必须拒绝（只改一侧的另一条路要红）")
			}
			if !tc.wantJSFail && jsErr != nil {
				t.Fatalf("schema 校验不该拒绝：%v", jsErr)
			}
		})
	}
}

// TestBadgeConditionKeyLengthSubset 钉住三方长度关系里的安全侧偏差（spec §8 #5）：
// Go 用 manifestIDPattern（≤81）比 schema/DB 的 121 更严，82–121 可过 schema 但被
// manifest 拒。这条测试同时证明「schema 路径放行、Go 路径拒绝」是**已知且刻意**的。
func TestBadgeConditionKeyLengthSubset(t *testing.T) {
	keyOfLength := func(total int) string {
		prefix := "demo.v3."
		return prefix + strings.Repeat("k", total-len(prefix))
	}
	// 81 字符：三方都通过。
	ok := badgeConditionManifest()
	ok.BadgeConditions[0].Key = keyOfLength(81)
	ok.BadgeConditions[0].ContractVersion = ok.BadgeConditions[0].Key + "@1"
	if err := Validate(ok); err != nil {
		t.Fatalf("81 字符 key 必须通过 Go 校验，得到 %v", err)
	}
	// 82 字符：schema 通过、Go 拒绝。
	long := badgeConditionManifest()
	long.BadgeConditions[0].Key = keyOfLength(82)
	long.BadgeConditions[0].ContractVersion = long.BadgeConditions[0].Key + "@1"
	if err := Validate(long); err == nil {
		t.Fatal("82 字符 key 必须被 manifest 拒（manifestIDPattern ≤81）")
	}
	body, err := json.Marshal(long)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV3JSONSchema(body); err != nil {
		t.Fatalf("schema 允许到 121 字符，82 不该被 schema 拒：%v", err)
	}
}

// TestManifestV3SchemaDeclaresBadgeConditions 直接读 schema 原文断言关键约束。
//
// 这些值不是「随手写的上限」：key pattern 必须与
// `badge_facts.fact_key` 的 CHECK 逐字一致，32 与 `ruleengine.MaxLeaves` 对齐，
// 16 是 variants 的产品上限。改 schema 任一处，这条会红。
func TestManifestV3SchemaDeclaresBadgeConditions(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(ManifestV3JSONSchema(), &schema); err != nil {
		t.Fatalf("schema 不是合法 JSON：%v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	root, ok := properties["badgeConditions"].(map[string]any)
	if !ok {
		t.Fatal("schema 根 properties 必须声明 badgeConditions")
	}
	if ref, _ := root["$ref"].(string); ref != "#/$defs/badgeConditions" {
		t.Fatalf("badgeConditions 必须 $ref 到 $defs，得到 %v", root["$ref"])
	}

	defs, _ := schema["$defs"].(map[string]any)
	def, ok := defs["badgeConditions"].(map[string]any)
	if !ok {
		t.Fatal("$defs 必须声明 badgeConditions")
	}
	if maxItems, _ := def["maxItems"].(float64); maxItems != float64(BadgeConditionsMaximum) {
		t.Fatalf("badgeConditions.maxItems = %v，want %d", def["maxItems"], BadgeConditionsMaximum)
	}
	items, _ := def["items"].(map[string]any)
	itemProps, _ := items["properties"].(map[string]any)

	key, _ := itemProps["key"].(map[string]any)
	if pattern, _ := key["pattern"].(string); pattern != `^[a-z0-9][a-z0-9._-]{1,120}$` {
		t.Fatalf("key pattern 必须与 DB CHECK 逐字一致，得到 %q", pattern)
	}
	compare, _ := itemProps["compare"].(map[string]any)
	if minItems, _ := compare["minItems"].(float64); minItems != 1 {
		t.Fatalf("compare.minItems = %v，want 1", compare["minItems"])
	}
	variants, _ := itemProps["variants"].(map[string]any)
	if maxItems, _ := variants["maxItems"].(float64); maxItems != float64(BadgeConditionVariantsMaximum) {
		t.Fatalf("variants.maxItems = %v，want %d", variants["maxItems"], BadgeConditionVariantsMaximum)
	}
	label, _ := itemProps["label"].(map[string]any)
	required, _ := label["required"].([]any)
	hasZh, hasEn := false, false
	for _, item := range required {
		if item == "zh-CN" {
			hasZh = true
		}
		if item == "en-US" {
			hasEn = true
		}
	}
	if !hasZh || !hasEn {
		t.Fatalf("label.required 必须含 zh-CN 与 en-US，得到 %v", required)
	}
	// 三条纪律：不得加回 valueKind。
	if _, present := itemProps["valueKind"]; present {
		t.Fatal("badgeConditions 不得声明 valueKind（插件条件只有 count 语义）")
	}
}

// TestManifestV3SchemaRequiresBackendForBadgeConditions 是 §10 Errata 4 的缺口断言。
//
// 今天装包不会漏是因为 `load.go` 在 schema 之后**必跑** Go `Validate` —— 那是调用
// 顺序在兜，不是 schema 自洽。这条用例刻意构造「只有 badgeConditions 触发 backend
// 需求」的清单（去掉 content/seo/identity 这三条既有 if/then 的触发字段），
// **只走 schema 路径**，断言它仍然被拒。
func TestManifestV3SchemaRequiresBackendForBadgeConditions(t *testing.T) {
	manifest := badgeConditionManifest()
	// 去掉 backend 与三条既有 if/then 的触发家族，让 backend 需求只能来自 badgeConditions。
	manifest.Backend = ManifestBackend{}
	manifest.Content = nil
	manifest.SEO = nil
	manifest.Identity = nil

	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// 前置条件：Go 路径确实拒（否则这条用例在测一个本来就不合规的清单）。
	if err := Validate(manifest); err == nil {
		t.Fatal("Go 校验必须拒绝缺 backend 的 badgeConditions 声明")
	}
	jsErr := ValidateV3JSONSchema(body)
	if jsErr == nil {
		t.Fatal("schema 路径也必须拒绝：只靠调用顺序兜底不是自洽")
	}
	if !strings.Contains(jsErr.Error(), "backend") {
		t.Fatalf("schema 的拒绝理由必须指向 backend，得到 %v", jsErr)
	}

	// 结构性断言：这条约束真的写在 allOf 里（不是被别的字段偶然命中）。
	var schema map[string]any
	if err := json.Unmarshal(ManifestV3JSONSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	clauses, _ := schema["allOf"].([]any)
	found := false
	for _, raw := range clauses {
		clause, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		condition, _ := clause["if"].(map[string]any)
		required, _ := condition["required"].([]any)
		for _, item := range required {
			if item == "badgeConditions" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("`allOf` 必须有一条以 badgeConditions 为 if 条件的子句")
	}
}
