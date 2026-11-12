package badgescontroller

import (
	"encoding/json"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
)

// TestFactCandidateWireShape 钉住 `factCandidates[]` 的键集合。
//
// 与 `conditions` 的 `items` 是**两个面**：这里是 fact.count 的 factKey/variant 候选，
// 双语 label 属于下拉项文案。契约漂移的典型形态是「多一个键」或「漏赋值变成缺键」，
// 所以断言键集合精确相等 + 空集合必须是数组而不是 null。
func TestFactCandidateWireShape(t *testing.T) {
	raw, err := json.Marshal(toFactCandidateDTO(conditions.FactCandidate{
		Key:      "acme.wiki.pages",
		LabelZh:  "页面数",
		LabelEn:  "Pages",
		UnitZh:   "个",
		UnitEn:   "pages",
		Compare:  []string{"gte", "lte"},
		Variants: []conditions.FactVariant{{Value: "tutorial", LabelZh: "教程", LabelEn: "Tutorial"}},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertKeys(t, "fact candidate", wire, []string{"key", "label", "unit", "compare", "variants"})

	label, ok := wire["label"].(map[string]any)
	if !ok || label["zh"] != "页面数" || label["en"] != "Pages" {
		t.Fatalf("label 必须是 {zh,en}，得到 %v", wire["label"])
	}
	unit, ok := wire["unit"].(map[string]any)
	if !ok || unit["zh"] != "个" || unit["en"] != "pages" {
		t.Fatalf("unit 必须是 {zh,en}，得到 %v", wire["unit"])
	}
	compare, ok := wire["compare"].([]any)
	if !ok || len(compare) != 2 {
		t.Fatalf("compare 必须是非空数组，得到 %v", wire["compare"])
	}
	variants, ok := wire["variants"].([]any)
	if !ok || len(variants) != 1 {
		t.Fatalf("variants 必须是数组，得到 %v", wire["variants"])
	}
	first, ok := variants[0].(map[string]any)
	if !ok {
		t.Fatalf("variant 元素形状不对：%v", variants[0])
	}
	assertKeys(t, "fact variant", first, []string{"value", "label"})
}

// TestFactCandidateEmptyCollectionsAreArrays 断言「无 unit / 无 variants」不会变成 null
// 或缺失键：编辑器按数组渲染，null 会让下拉直接崩。
func TestFactCandidateEmptyCollectionsAreArrays(t *testing.T) {
	raw, err := json.Marshal(toFactCandidateDTO(conditions.FactCandidate{Key: "acme.wiki.pages"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertKeys(t, "fact candidate", wire, []string{"key", "label", "unit", "compare", "variants"})
	if _, present := wire["unit"]; !present || wire["unit"] != nil {
		t.Fatalf("没有单位时 unit 必须是 null（键在），得到 %v", wire["unit"])
	}
	if compare, ok := wire["compare"].([]any); !ok || len(compare) != 0 {
		t.Fatalf("compare 必须是空数组，得到 %v", wire["compare"])
	}
	if variants, ok := wire["variants"].([]any); !ok || len(variants) != 0 {
		t.Fatalf("variants 必须是空数组，得到 %v", wire["variants"])
	}
}
