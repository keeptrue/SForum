package trustlevels

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 规则相关的字段级代码（`CodeRule*` / `CodeEarnableNotDeclared`）声明在 config.go 的
// 常量块里，与其余 33 条码同一处：`configErrorReasonCodes` 是那份契约的单一出处，
// 分散到两个文件只会让「忘了加进枚举」变成默认结果。
//
// 为什么这 6 条是**聚合**码：`ruleengine` 已经有 17 个细分码（`too_deep` /
// `too_many_nodes` / `compare_not_allowed` / `param_invalid` …），它们是给引擎内部与
// 引擎测试用的稳定标识；运营要的是「该改哪一类东西」。所以本包用一张**全量映射表**
// 把 17 个引擎码收敛成 4 类规则问题，另加 2 条域层自产（引擎侧根本没有）——
// 既不把引擎的分类学泄漏给运营，也不让「精确 422」退化成桶码。
// ruleErrorCodesByEngineCode 是引擎码 → 本域聚合码的**唯一**映射表。
//
// 表必须对 17 个引擎码**全量**（`rule_engine_code_coverage_test` 钉住）：映射缺一条
// 时零值会静默落成空串，响应里就少一个 reasonCode，前端只能显示兜底文案——那种
// 退化在生产上表现为「运营看不到自己能改什么」，比报错难查。
//
// 分类依据是**修法**，不是错误来源：
//   - 数值/规模越界 → `rule_too_complex`（删条件、拆层、把阈值调回区间）；
//   - 条件不存在 → `rule_unknown_condition`（换一个条件）；
//   - 其余形状问题 → `rule_shape_invalid`（把该补的补上：`cmp`、`children`、参数）。
var ruleErrorCodesByEngineCode = map[string]string{
	ruleEngineCodeTooDeep:          CodeRuleTooComplex,
	ruleEngineCodeTooManyNodes:     CodeRuleTooComplex,
	ruleEngineCodeTooManyLeaves:    CodeRuleTooComplex,
	ruleEngineCodeValueOutOfRange:  CodeRuleTooComplex,
	ruleEngineCodeUnknownCondition: CodeRuleUnknownCondition,

	ruleEngineCodeShapeInvalid:       CodeRuleShapeInvalid,
	ruleEngineCodeCompareMissing:     CodeRuleShapeInvalid,
	ruleEngineCodeCompareInvalid:     CodeRuleShapeInvalid,
	ruleEngineCodeCompareNotAllowed:  CodeRuleShapeInvalid,
	ruleEngineCodeCompareForbidden:   CodeRuleShapeInvalid,
	ruleEngineCodeParamMissing:       CodeRuleShapeInvalid,
	ruleEngineCodeParamInvalid:       CodeRuleShapeInvalid,
	ruleEngineCodeParamUnknown:       CodeRuleShapeInvalid,
	ruleEngineCodeVersionUnsupported: CodeRuleShapeInvalid,
	ruleEngineCodeDecodeFailed:       CodeRuleShapeInvalid,
	ruleEngineCodeCatalogInvalid:     CodeRuleShapeInvalid,
	// resolve_failed 是**求值期**码（`RuleEngine/eval.go`），保存路径永远不会产生它。
	// 它仍然进表，只是因为「全量映射」比「差一条」可靠：真出现在这里说明有人把求值
	// 错误当成校验错误抛了上来，那时落成形状错误至少不会静默。
	ruleEngineCodeResolveFailed: CodeRuleShapeInvalid,
}

// 引擎码常量在这里再声明一次而不是直接引用 `ruleengine.Code*`：映射表的键必须是
// **字面量**，否则哪天引擎改名，映射表会跟着静默改变含义（表是契约，不是转发）。
const (
	ruleEngineCodeDecodeFailed       = "decode_failed"
	ruleEngineCodeVersionUnsupported = "version_unsupported"
	ruleEngineCodeShapeInvalid       = "shape_invalid"
	ruleEngineCodeTooDeep            = "too_deep"
	ruleEngineCodeTooManyNodes       = "too_many_nodes"
	ruleEngineCodeTooManyLeaves      = "too_many_leaves"
	ruleEngineCodeUnknownCondition   = "unknown_condition"
	ruleEngineCodeCompareMissing     = "compare_missing"
	ruleEngineCodeCompareInvalid     = "compare_invalid"
	ruleEngineCodeCompareNotAllowed  = "compare_not_allowed"
	ruleEngineCodeCompareForbidden   = "compare_forbidden"
	ruleEngineCodeValueOutOfRange    = "value_out_of_range"
	ruleEngineCodeParamUnknown       = "param_unknown"
	ruleEngineCodeParamMissing       = "param_missing"
	ruleEngineCodeParamInvalid       = "param_invalid"
	ruleEngineCodeCatalogInvalid     = "catalog_invalid"
	ruleEngineCodeResolveFailed      = "resolve_failed"
)

// ruleEngineCodes 是 17 个引擎码的字面量全集（映射表的键集必须与它相等）。
//
// 顺序照 `RuleEngine/validate.go` 的 const 块，便于对照复查。
var ruleEngineCodes = []string{
	ruleEngineCodeDecodeFailed,
	ruleEngineCodeVersionUnsupported,
	ruleEngineCodeShapeInvalid,
	ruleEngineCodeTooDeep,
	ruleEngineCodeTooManyNodes,
	ruleEngineCodeTooManyLeaves,
	ruleEngineCodeUnknownCondition,
	ruleEngineCodeCompareMissing,
	ruleEngineCodeCompareInvalid,
	ruleEngineCodeCompareNotAllowed,
	ruleEngineCodeCompareForbidden,
	ruleEngineCodeValueOutOfRange,
	ruleEngineCodeParamUnknown,
	ruleEngineCodeParamMissing,
	ruleEngineCodeParamInvalid,
	ruleEngineCodeCatalogInvalid,
	ruleEngineCodeResolveFailed,
}

// ruleIssue 是域层自产的规则问题（引擎侧没有对应码的那两类）。
//
// 它只在保存路径存在：`DecodeConfig` 是纯函数（无 ctx、无库），引用存在性检查必须
// 发生在写路径，否则资料页/论坛 provider 的读路径会被拖库。
type ruleIssue struct {
	code   string
	path   string
	ref    string
	detail string
}

// ruleViolation 把「引擎错误 / 域层问题」统一成一条待上报的规则问题。
type ruleViolation struct {
	code   string
	path   string
	ref    string
	reason string
	params map[string]any
}

// decodeTierRule 校验并规范化一档的可选规则树。
//
// 三态：省略 / 显式 `null` → 无规则（返回 nil，写出时 `omitempty` 直接省略）；
// 其余必须是 `{"v":1,"root":…}` 且通过**子集目录**校验。
//
// 返回的是**客户端原始字节**（仅 trim），不是重新序列化的 AST：未编辑的规则必须原样
// 透传，重新编码会重排键序、把「读一次再存一次」变成一次隐式改写。
func decodeTierRule(raw json.RawMessage) (json.RawMessage, []ruleViolation) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	root, err := ruleengine.Decode(trimmed)
	if err != nil {
		return nil, []ruleViolation{engineRuleViolation(err)}
	}
	// 先判「这条条件能不能用」，再判「这棵树合不合法」：前者的修法是换条件，后者是
	// 补参数 / 补 `cmp`。顺序反过来时，一条「条件用错且参数也不全」的叶子会先报形状，
	// 运营改完形状再撞第二个错——一次保存只能修一处。
	if issue, ok := scanConditionAvailability(root); ok {
		return nil, []ruleViolation{conditionRuleViolation(issue)}
	}
	if err := ruleengine.Validate(root, ruleConditionCatalog()); err != nil {
		return nil, []ruleViolation{engineRuleViolation(err)}
	}
	return json.RawMessage(trimmed), nil
}

// engineRuleViolation 把 `ruleengine` 的错误翻成一条规则问题。
//
// `Error.Path` 原样保留（`root.children[1].cmp` 形状）：前端据此把错误指到编辑器里
// 那个节点，而不是丢一句「规则非法」。这也是 Q6 裁决里「`params.path` 放引擎坐标」
// 的落点。
func engineRuleViolation(err error) ruleViolation {
	path := ""
	code := ""
	var ruleErr *ruleengine.Error
	if errors.As(err, &ruleErr) {
		path = ruleErr.Path
		code = ruleErr.Code
	}
	mapped, ok := ruleErrorCodesByEngineCode[code]
	if !ok {
		// 未知引擎码不静默：落成形状错误 + 在 reason 里留原始码，便于日志定位。
		mapped = CodeRuleShapeInvalid
	}
	reason := err.Error()
	return ruleViolation{
		code:   mapped,
		path:   path,
		reason: fmt.Sprintf("规则不合法（%s）：%s", code, reason),
		params: map[string]any{"path": path},
	}
}

// conditionRuleViolation 把域层自产的「条件不可用 / 未知条件」翻成规则问题。
func conditionRuleViolation(issue ruleIssue) ruleViolation {
	params := map[string]any{"path": issue.path}
	if issue.ref != "" {
		params["condition"] = issue.ref
	}
	return ruleViolation{
		code:   issue.code,
		path:   issue.path,
		ref:    issue.ref,
		reason: issue.detail,
		params: params,
	}
}

// fieldError 把规则问题投影成字段级明细。
//
// `Field` 恒为 `rule`：规则树是档位的一个字段，明细的定位入口是 `field=rule` +
// `params.path` 的节点坐标。把 `Field` 写成 `rule.children[1]` 会让前端不得不用字符串
// 前缀判断「这是规则里的错误」，那等于把 AST 词汇搬进传输层。
func (v ruleViolation) fieldError(rank Level, tierID string) FieldError {
	return FieldError{
		Rank:   rank,
		TierID: tierID,
		Field:  "rule",
		Code:   v.code,
		Params: v.params,
		Reason: v.reason,
	}
}

// scanConditionAvailability 找出第一个「不在共享目录」或「在共享目录但不在子集」的
// 叶子条件。
//
// 为什么必须在 `Validate` 之前单独扫一遍：`Validate` 拿到的是**子集目录**，它对这两
// 种情况只会报同一个 `unknown_condition`。而两者修法不同（换 key vs 换一条可用条件），
// 合并就是把情报丢掉。扫描按树的书写顺序取第一个，所以同一份配置的报错稳定可复现。
func scanConditionAvailability(root ruleengine.Node) (ruleIssue, bool) {
	var found ruleIssue
	ok := false
	walkRuleLeaves(root, "root", func(node ruleengine.Node, path string) {
		if ok {
			return
		}
		if RuleConditionAllowed(node.Type) {
			return
		}
		if _, shared := SharedCondition(node.Type); shared {
			found = ruleIssue{
				code: CodeRuleConditionNotAvailable, path: path, ref: node.Type,
				detail: fmt.Sprintf("条件 %q 属于内容质量信号，不在信任等级可用条件里；请换成活跃度、身份或插件事实类条件", node.Type),
			}
			ok = true
			return
		}
		found = ruleIssue{
			code: CodeRuleUnknownCondition, path: path, ref: node.Type,
			detail: fmt.Sprintf("条件 %q 不在条件目录里（拼写错误，或提供它的插件已被停用）", node.Type),
		}
		ok = true
	})
	return found, ok
}

// walkRuleLeaves 按 `ruleengine` 的坐标格式遍历叶子（`root` / `root.children[1]`）。
//
// 不用 `ruleengine` 内部的 walker（未导出）也不复用 `Validate`：这里要的是「遍历」
// 而不是「判定」，混进校验会把两件事的失败顺序耦合起来。
func walkRuleLeaves(node ruleengine.Node, path string, visit func(ruleengine.Node, string)) {
	if node.IsLeaf() {
		visit(node, path)
		return
	}
	for index, child := range node.Children {
		walkRuleLeaves(child, fmt.Sprintf("%s.children[%d]", path, index), visit)
	}
}
