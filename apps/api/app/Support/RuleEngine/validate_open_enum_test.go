package ruleengine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// factCatalog 复刻插件事实条件的参数声明：`variant` 是 `ParamEnum + Options:nil`。
//
// 这正是 M3 的关键形状：候选由域层在运行期提供，引擎不可能知道全集。
func factCatalog(options []string) ruleengine.CatalogMap {
	return ruleengine.CatalogMap{
		"fact.count": {
			Key:            "fact.count",
			ValueKind:      ruleengine.ValueCount,
			AllowedCompare: []ruleengine.CompareOp{ruleengine.CompareGTE, ruleengine.CompareLTE},
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "factKey", Kind: ruleengine.ParamObjectRef, Required: true, RefKind: "fact"},
				{Name: "variant", Kind: ruleengine.ParamEnum, Required: false, Options: options},
				{Name: "window", Kind: ruleengine.ParamWindow, Required: true},
			}},
		},
	}
}

func factLeaf(variant any) ruleengine.Node {
	params := map[string]any{
		"factKey": "acme.wiki.pages",
		"window":  map[string]any{"kind": "all"},
	}
	if variant != nil {
		params["variant"] = variant
	}
	cmp := ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 1}
	return leaf("fact.count", params, &cmp)
}

// TestValidateTreatsNilEnumOptionsAsOpenSet 是 M3 的保存期前提。
//
// 变异自检：把 `walkParam` 里的 `if spec.Options == nil` 分支删掉（回到
// `slices.Contains(nil, text)`），这条立刻红——那正是「带 variant 的规则 100% 422」
// 的原始故障。
func TestValidateTreatsNilEnumOptionsAsOpenSet(t *testing.T) {
	catalog := factCatalog(nil)
	for _, variant := range []string{"tutorial", "profile", "a-1.b_c"} {
		if err := ruleengine.Validate(factLeaf(variant), catalog); err != nil {
			t.Fatalf("Options=nil 是开放集，%q 应通过，得到 %v", variant, err)
		}
	}
}

// TestValidateOpenSetStillChecksShape 断言开放集不等于「什么都放行」：
// 大小写、空格、非法字符、超长一律拒，否则拼错的 variant 会静默存进规则。
func TestValidateOpenSetStillChecksShape(t *testing.T) {
	catalog := factCatalog(nil)
	for _, variant := range []string{"Tutorial", " tutorial", "tuto rial", "-tutorial", strings.Repeat("a", 65)} {
		err := ruleengine.Validate(factLeaf(variant), catalog)
		var ruleErr *ruleengine.Error
		if !errors.As(err, &ruleErr) || ruleErr.Code != ruleengine.CodeParamInvalid {
			t.Fatalf("%q 必须被形态校验拒绝（param_invalid），得到 %v", variant, err)
		}
	}
}

// TestValidateEmptyEnumOptionsStaysClosed 断言 `[]string{}` 与 nil 的两种语义不混：
// 空集合是**封闭的空集**，一个值都放不过。
func TestValidateEmptyEnumOptionsStaysClosed(t *testing.T) {
	catalog := factCatalog([]string{})
	err := ruleengine.Validate(factLeaf("tutorial"), catalog)
	var ruleErr *ruleengine.Error
	if !errors.As(err, &ruleErr) || ruleErr.Code != ruleengine.CodeParamInvalid {
		t.Fatalf("空集合必须拒绝任何取值，得到 %v", err)
	}
	// 省略 variant 仍然合法（Required:false）。
	if err := ruleengine.Validate(factLeaf(nil), catalog); err != nil {
		t.Fatalf("省略可选参数必须通过，得到 %v", err)
	}
}

// TestValidateClosedEnumStillChecksMembership 断言封闭集合路径没被改坏。
func TestValidateClosedEnumStillChecksMembership(t *testing.T) {
	node := leaf("activity.count", map[string]any{
		"metric": "nope",
		"window": map[string]any{"kind": "all"},
	}, &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 1})
	err := ruleengine.Validate(node, testCatalog())
	var ruleErr *ruleengine.Error
	if !errors.As(err, &ruleErr) || ruleErr.Code != ruleengine.CodeParamInvalid {
		t.Fatalf("封闭集必须拒绝集合外取值，得到 %v", err)
	}
}
