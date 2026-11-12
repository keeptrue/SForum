package conditions

import (
	"testing"
)

// TestMergeFactDeclarationsRejectsWrongNamespace 断言前缀与重名收口。
//
// 校验器已经挡过一遍（spec §2.4），这里再挡是纵深防御：装配层的输入可能来自
// 历史数据、测试夹具或将来「从库里重建候选」的路径，不该假设上游一定干净。
func TestMergeFactDeclarationsRejectsWrongNamespace(t *testing.T) {
	candidates, conflicts := MergeFactDeclarations([]FactDeclaration{
		{ExtensionID: "acme.wiki", Key: "acme.wiki.pages", LabelZh: "页面", LabelEn: "Pages"},
		{ExtensionID: "acme.wiki", Key: "other.pages", LabelZh: "越界", LabelEn: "Out of namespace"},
		{ExtensionID: "acme.wiki", Key: "fact.count", LabelZh: "撞内置", LabelEn: "Core key"},
		{ExtensionID: "acme.wiki", Key: "acme.wiki.pages", LabelZh: "重复", LabelEn: "Duplicate"},
	})
	if len(candidates) != 1 {
		t.Fatalf("只应留下一条合法候选，得到 %+v", candidates)
	}
	if candidates[0].Key != "acme.wiki.pages" || candidates[0].LabelZh != "页面" {
		t.Fatalf("合法候选被改写：%+v", candidates[0])
	}
	reasons := make(map[string]int, len(conflicts))
	for _, conflict := range conflicts {
		reasons[conflict.Reason]++
	}
	if reasons[FactConflictNamespace] != 1 || reasons[FactConflictCoreKey] != 1 || reasons[FactConflictDuplicate] != 1 {
		t.Fatalf("三类冲突必须各记一条，得到 %+v", conflicts)
	}
}

// TestMergeFactDeclarationsNormalizes 断言 compare/variants 归一化与稳定排序。
//
// 归一化放在合并处而不是渲染处：候选是下发给编辑器的数据，服务端两次请求给出
// 不同顺序会让前端每次刷新都重排。
func TestMergeFactDeclarationsNormalizes(t *testing.T) {
	candidates, conflicts := MergeFactDeclarations([]FactDeclaration{
		{ExtensionID: "b.ext", Key: "b.ext.z", Compare: []string{" GTE ", "gte", "", "lte"}},
		{ExtensionID: "a.ext", Key: "a.ext.a", Variants: []FactVariant{
			{Value: " tutorial ", LabelZh: "教程", LabelEn: "Tutorial"},
			{Value: "tutorial"},
			{Value: "  "},
		}},
	})
	if len(conflicts) != 0 {
		t.Fatalf("合法声明不该产生冲突，得到 %+v", conflicts)
	}
	if candidates[0].Key != "a.ext.a" || candidates[1].Key != "b.ext.z" {
		t.Fatalf("候选必须按 key 排序，得到 %+v", candidates)
	}
	if got := candidates[1].Compare; len(got) != 2 || got[0] != "gte" || got[1] != "lte" {
		t.Fatalf("compare 必须去重、小写、保序，得到 %v", got)
	}
	variants := candidates[0].Variants
	if len(variants) != 1 || variants[0].Value != "tutorial" || variants[0].LabelZh != "教程" {
		t.Fatalf("variants 必须 trim、去重、丢弃空值，得到 %+v", variants)
	}
}

// TestVariantDeclaredFourCases 钉住保存期 variant 兜底的四种放行/拒绝形态。
//
// 三种放行都是刻意的（spec §8 Errata 2 #1）：扩展卸载后运营仍要能保存规则，
// 只有「声明了候选却写了别的值」才是真错。
func TestVariantDeclaredFourCases(t *testing.T) {
	candidates := []FactCandidate{{
		Key:      "acme.wiki.pages",
		Variants: []FactVariant{{Value: "tutorial"}, {Value: "profile"}},
	}}
	cases := []struct {
		name     string
		factKey  string
		variant  string
		declared bool
	}{
		{"未声明 factKey（扩展卸载）放行", "gone.ext.thing", "tutorial", true},
		{"候选没有 variants 放行", "acme.wiki.pages", "whatever", true},
		{"variant 为空放行", "acme.wiki.pages", "", true},
		{"声明过的 variant 放行", "acme.wiki.pages", " tutorial ", true},
		{"未声明的 variant 拒绝", "acme.wiki.pages", "tutorials", false},
	}
	// 第二例需要「有 key 但无 variants」的候选，单独补一条。
	noVariants := []FactCandidate{{Key: "acme.wiki.pages"}}
	for _, tc := range cases {
		pool := candidates
		if tc.name == "候选没有 variants 放行" {
			pool = noVariants
		}
		if got := VariantDeclared(pool, tc.factKey, tc.variant); got != tc.declared {
			t.Fatalf("%s: VariantDeclared = %v, want %v", tc.name, got, tc.declared)
		}
	}
}

// TestPublishableDefinitionsHidesFactCountWithoutCandidates 钉住「有候选才出现」。
//
// 没有候选时暴露 fact.count 只是给运营一个空下拉（M1 无扩展时必然为空）；
// 但校验/求值仍认它，否则 M3 存下的规则通不过校验。
func TestPublishableDefinitionsHidesFactCountWithoutCandidates(t *testing.T) {
	registry := Core()

	hasFactCount := func(definitions []Definition) bool {
		for _, definition := range definitions {
			if definition.Key == KeyFactCount {
				return true
			}
		}
		return false
	}

	if hasFactCount(registry.PublishableDefinitions(nil)) {
		t.Fatal("没有候选时 fact.count 不该出现在可发布目录里")
	}
	if !hasFactCount(registry.PublishableDefinitions([]FactCandidate{{Key: "acme.wiki.pages"}})) {
		t.Fatal("有候选时 fact.count 必须出现")
	}
	// 单例不能被污染：Options 是复制后填的，不是原地写。
	if _, ok := registry.ConditionMeta(KeyFactCount); !ok {
		t.Fatal("校验用全集必须始终包含 fact.count")
	}
	def, _ := registry.Definition(KeyFactCount)
	for _, spec := range def.Params.Specs {
		if spec.RefKind == "fact" && len(spec.Options) != 0 {
			t.Fatalf("单例被污染：factKey 参数带了候选 %v", spec.Options)
		}
	}
}

// TestFactKeysIsStable 断言 FactKeys 保序（resolver 与 rule_health 读它）。
func TestFactKeysIsStable(t *testing.T) {
	candidates := []FactCandidate{{Key: "b.ext.z"}, {Key: "a.ext.a"}}
	keys := FactKeys(candidates)
	if len(keys) != 2 || keys[0] != "b.ext.z" || keys[1] != "a.ext.a" {
		t.Fatalf("FactKeys 必须保序，得到 %v", keys)
	}
}
