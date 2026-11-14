package conditions

import (
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// TestIdentityProfileDefinitionPinsEnumOrder 钉住 `identity.profile` 的线形契约：
// 布尔条件、无 compare、`field` 是四值闭集，且顺序就是编辑器下拉顺序。
//
// 顺序不是审美问题：前端按 `Options` 原序渲染下拉，golden 样本也按取值取；
// 顺序变了，运营看到的默认项就变了。
func TestIdentityProfileDefinitionPinsEnumOrder(t *testing.T) {
	def, ok := Core().Definition(KeyIdentityProfile)
	if !ok {
		t.Fatal("identity.profile 未注册")
	}
	if def.ValueKind != ruleengine.ValueBoolean {
		t.Fatalf("identity.profile 必须是布尔条件，得到 %q", def.ValueKind)
	}
	if len(def.Compare) != 0 {
		t.Fatalf("布尔条件不得声明 compare，得到 %v", def.Compare)
	}
	if len(def.Params.Specs) != 1 || def.Params.Specs[0].Name != "field" {
		t.Fatalf("identity.profile 应只有 field 一个参数，得到 %+v", def.Params.Specs)
	}
	spec := def.Params.Specs[0]
	if spec.Kind != ruleengine.ParamEnum || !spec.Required {
		t.Fatalf("field 应是必填枚举，得到 %+v", spec)
	}
	want := []string{"bio", "signature", "website", "location"}
	if len(spec.Options) != len(want) {
		t.Fatalf("field 取值应为 %v，得到 %v", want, spec.Options)
	}
	for index := range want {
		if spec.Options[index] != want[index] {
			t.Fatalf("field 取值顺序应为 %v，得到 %v", want, spec.Options)
		}
	}
	if def.Phrase == nil {
		t.Fatal("identity.profile 缺 Phrase：公开面这个条件会没有说明")
	}
	if def.LabelZh == "" || def.LabelEn == "" || def.DescribeZh == "" || def.DescribeEn == "" {
		t.Fatalf("identity.profile 缺双语 label/describe：%+v", def)
	}
}

// TestProfileFieldTablesCoverSameEnum 钉住三张表同源：注册项的 `Options`、解析器的
// 白名单（`profileFieldOptions`）与文案表（`profileFieldPhrases`）。
//
// 漏一处就是「保存成功、永不成立、零报错」：目录里有取值而解析器不认 → 求值期
// unknown；文案表缺 → 公开面回落泛称或出现裸 slug。
func TestProfileFieldTablesCoverSameEnum(t *testing.T) {
	options := profileFieldOptions()
	if len(options) != 4 {
		t.Fatalf("资料项应为 4 个，得到 %v", options)
	}
	if len(profileFieldPhrases) != len(options) {
		t.Fatalf("文案表与枚举不同源：%v vs %v", profileFieldPhrases, options)
	}
	for _, field := range options {
		if !isProfileField(field) {
			t.Fatalf("%q 在枚举里但 isProfileField 不认（求值会变 unknown）", field)
		}
		labels, ok := profileFieldPhrases[field]
		if !ok {
			t.Fatalf("%q 没有文案：公开面会回落泛称", field)
		}
		if labels[0] == "" || labels[1] == "" {
			t.Fatalf("%q 的文案为空：%v", field, labels)
		}
	}
	// 脏规则必须 fail-closed：大小写不同也算未知取值（契约字面量是大小写敏感的）。
	for _, dirty := range []string{"", "nickname", "Bio", "website_url"} {
		if isProfileField(dirty) {
			t.Fatalf("%q 不该通过白名单：未知取值必须转 unknown，不能静默 false", dirty)
		}
	}
}

// TestProfileFilledSQLCoversEveryProfileField 钉住「枚举 ⇄ SQL」同源。
//
// 红判据：profileFields 加了取值而生成的 SQL 没有对应 WHEN（或列名写错）→ 指名该取值。
// 2026-11-14 审查 §5.2 的实证：CASE 曾是手写字面量，加第 5 个取值后整包测试全绿、
// 运行期静默恒 false（在 `not` 子树下反转成 true）。派生 + 这条断言取代了「记得
// 同时改两处」的纪律要求。
func TestProfileFilledSQLCoversEveryProfileField(t *testing.T) {
	if got := strings.Count(profileFilledSQL, "WHEN '"); got != len(profileFields) {
		t.Fatalf("SQL 的 CASE 分支数 %d 与枚举数 %d 不一致：\n%s", got, len(profileFields), profileFilledSQL)
	}
	for _, field := range profileFields {
		if field.Column == "" {
			t.Fatalf("取值 %q 没有列名：SQL 会生成 profiles. 空标识符", field.Field)
		}
		want := "WHEN '" + field.Field + "' THEN profiles." + field.Column + " <> ''"
		if !strings.Contains(profileFilledSQL, want) {
			t.Fatalf("SQL 缺少取值 %q 的分支（期望包含 %q）：\n%s", field.Field, want, profileFilledSQL)
		}
	}
}
