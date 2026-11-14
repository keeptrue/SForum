package trustlevels

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// TestRuleEngineCodeMappingIsTotal 钉住「17 个引擎码 → 4 类聚合码」这张表。
//
// 这三条断言守的是三种不同的失败：
//  1. 字面量漂移（引擎改名而映射表没跟）：左列是**本包的字面量常量**，右列是引擎的
//     导出常量，两者相等即证明映射表的键真是引擎码，而不是一份平行词汇表；
//  2. 漏一条（新增引擎码没人补表）：键数必须等于 `ruleEngineCodes` 的 17；
//  3. 归错类（把「条件不存在」塞进「形状不对」）：每条只允许落进 3 类之一，
//     `rule_reference_invalid` / `rule_condition_not_available` 是域层自产，
//     引擎码一条都不许映射到它们。
func TestRuleEngineCodeMappingIsTotal(t *testing.T) {
	literals := map[string]string{
		ruleEngineCodeDecodeFailed:       ruleengine.CodeDecodeFailed,
		ruleEngineCodeVersionUnsupported: ruleengine.CodeVersionUnsupported,
		ruleEngineCodeShapeInvalid:       ruleengine.CodeShapeInvalid,
		ruleEngineCodeTooDeep:            ruleengine.CodeTooDeep,
		ruleEngineCodeTooManyNodes:       ruleengine.CodeTooManyNodes,
		ruleEngineCodeTooManyLeaves:      ruleengine.CodeTooManyLeaves,
		ruleEngineCodeUnknownCondition:   ruleengine.CodeUnknownCondition,
		ruleEngineCodeCompareMissing:     ruleengine.CodeCompareMissing,
		ruleEngineCodeCompareInvalid:     ruleengine.CodeCompareInvalid,
		ruleEngineCodeCompareNotAllowed:  ruleengine.CodeCompareNotAllowed,
		ruleEngineCodeCompareForbidden:   ruleengine.CodeCompareForbidden,
		ruleEngineCodeValueOutOfRange:    ruleengine.CodeValueOutOfRange,
		ruleEngineCodeParamUnknown:       ruleengine.CodeParamUnknown,
		ruleEngineCodeParamMissing:       ruleengine.CodeParamMissing,
		ruleEngineCodeParamInvalid:       ruleengine.CodeParamInvalid,
		ruleEngineCodeCatalogInvalid:     ruleengine.CodeCatalogInvalid,
		ruleEngineCodeResolveFailed:      ruleengine.CodeResolveFailed,
	}
	for literal, engineConst := range literals {
		if literal != engineConst {
			t.Fatalf("映射表键 %q 与引擎常量 %q 不一致：引擎改名后表必须同批更新", literal, engineConst)
		}
	}
	if len(ruleEngineCodes) != 17 {
		t.Fatalf("引擎码全集应为 17 个，得到 %d：新增引擎码必须同批补映射", len(ruleEngineCodes))
	}
	if len(ruleErrorCodesByEngineCode) != len(ruleEngineCodes) {
		t.Fatalf("映射表 %d 条 ≠ 引擎码 %d 个：漏映射会让 reasonCode 变成空串",
			len(ruleErrorCodesByEngineCode), len(ruleEngineCodes))
	}
	allowed := map[string]bool{
		CodeRuleTooComplex:       true,
		CodeRuleUnknownCondition: true,
		CodeRuleShapeInvalid:     true,
	}
	for _, code := range ruleEngineCodes {
		mapped, ok := ruleErrorCodesByEngineCode[code]
		if !ok {
			t.Fatalf("引擎码 %q 没有映射", code)
		}
		if !allowed[mapped] {
			t.Fatalf("引擎码 %q 映射到 %q：只允许落进 3 类规则聚合码", code, mapped)
		}
	}
}

// TestUnknownEngineCodeFallsBackWithoutEmptyCode 钉住兜底：未知码不产生空 reasonCode。
func TestUnknownEngineCodeFallsBackWithoutEmptyCode(t *testing.T) {
	violation := engineRuleViolation(&ruleengine.Error{Code: "some_future_engine_code", Path: "root.children[0]"})
	if violation.code != CodeRuleShapeInvalid {
		t.Fatalf("未知引擎码应兜底为 %q，得到 %q", CodeRuleShapeInvalid, violation.code)
	}
	if violation.path != "root.children[0]" {
		t.Fatalf("path 必须原样透出引擎坐标，得到 %q", violation.path)
	}
	if violation.params["path"] != "root.children[0]" {
		t.Fatalf("params.path 必须是引擎坐标（Q6 裁决），得到 %v", violation.params["path"])
	}
}

// TestFieldErrorUsesRuleField 钉住 FieldError 的定位口径（Q6/裁决）。
//
// `field` 恒为 `rule`（档位级字段名），节点坐标放 `params.path`：写成
// `rule.children[1]` 会让前端不得不按字符串前缀判断「这是规则里的错误」。
func TestFieldErrorUsesRuleField(t *testing.T) {
	violation := engineRuleViolation(&ruleengine.Error{Code: ruleengine.CodeTooDeep, Path: "root.children[2]"})
	detail := violation.fieldError(3, "tier_7")
	if detail.Field != "rule" {
		t.Fatalf("field = %q（期望 rule）", detail.Field)
	}
	if detail.Rank != 3 || detail.TierID != "tier_7" {
		t.Fatalf("定位字段丢失：rank=%d tierId=%q", detail.Rank, detail.TierID)
	}
	if detail.Code != CodeRuleTooComplex {
		t.Fatalf("code = %q（期望 %q）", detail.Code, CodeRuleTooComplex)
	}
}
