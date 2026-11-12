package ruleengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
)

// 硬上限。数字来自计划 §5.1，是**产品契约**而不是性能调参项：
// 后台表单、导入工具、回扫 job 都按同一组数拒绝，改这里等于改契约。
const (
	// MaxDepth 是表达式树的最大深度，根节点算第 1 层。
	MaxDepth = 5
	// MaxLeaves 是整棵树允许的最大叶子（条件）数。
	MaxLeaves = 32
	// MaxNodes 是整棵树允许的最大节点数（组节点 + 叶子）。
	MaxNodes = 64
	// MinCompareValue / MaxCompareValue 是 cmp.value 的合法区间。
	MinCompareValue = 1
	MaxCompareValue = 1_000_000_000
)

// 校验错误码。领域层按码映射到对外错误码（`forum.badge_rule_too_complex` 等），
// 本包不 import 任何错误码常量，也不产出 i18n 文案。
const (
	CodeDecodeFailed       = "decode_failed"
	CodeVersionUnsupported = "version_unsupported"
	CodeShapeInvalid       = "shape_invalid"
	CodeTooDeep            = "too_deep"
	CodeTooManyNodes       = "too_many_nodes"
	CodeTooManyLeaves      = "too_many_leaves"
	CodeUnknownCondition   = "unknown_condition"
	CodeCompareMissing     = "compare_missing"
	CodeCompareInvalid     = "compare_invalid"
	CodeCompareNotAllowed  = "compare_not_allowed"
	CodeCompareForbidden   = "compare_forbidden"
	CodeValueOutOfRange    = "value_out_of_range"
	CodeParamUnknown       = "param_unknown"
	CodeParamMissing       = "param_missing"
	CodeParamInvalid       = "param_invalid"
	CodeCatalogInvalid     = "catalog_invalid"
	CodeResolveFailed      = "resolve_failed"
)

// Error 是一条带位置的结构化错误。Path 形如 `root.children[1].children[0].cmp`，
// 后台可以据此把错误指到具体那个节点，而不是丢一句「规则非法」。
type Error struct {
	Code   string
	Path   string
	Detail string
}

// Error 实现 error。
func (e *Error) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("ruleengine: %s: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("ruleengine: %s at %s: %s", e.Code, e.Path, e.Detail)
}

// CodeOf 取出错误链上第一个规则引擎错误码；不是本包错误时返回空串。
func CodeOf(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

// IsCode 判断错误链上是否有指定错误码。
func IsCode(err error, code string) bool { return CodeOf(err) == code }

// Validate 校验一棵树的形状、上限、条件白名单与参数。
//
// 它**不**做引用存在性检查（`topicId` 指向的主题是否还在、`slug` 是否真的存在）：
// 那需要查库，属于领域层保存路径的职责。这里只回答「这棵树本身是否自洽」。
func Validate(root Node, catalog Catalog) error {
	if catalog == nil {
		return &Error{Code: CodeCatalogInvalid, Path: "root", Detail: "缺少条件目录"}
	}
	v := &validator{catalog: catalog}
	v.walk(root, 1, "root")
	return v.err
}

type validator struct {
	catalog Catalog
	nodes   int
	leaves  int
	err     error
}

func (v *validator) fail(code, path, detail string) {
	if v.err == nil {
		v.err = &Error{Code: code, Path: path, Detail: detail}
	}
}

func (v *validator) walk(n Node, depth int, path string) {
	if v.err != nil {
		return
	}
	v.nodes++
	if v.nodes > MaxNodes {
		v.fail(CodeTooManyNodes, path, fmt.Sprintf("节点数超过上限 %d", MaxNodes))
		return
	}
	if depth > MaxDepth {
		v.fail(CodeTooDeep, path, fmt.Sprintf("深度超过上限 %d", MaxDepth))
		return
	}

	switch {
	case n.IsGroup() && n.IsLeaf():
		v.fail(CodeShapeInvalid, path, "节点不能同时有 op 与 type")
		return
	case !n.IsGroup() && !n.IsLeaf():
		v.fail(CodeShapeInvalid, path, "节点必须恰好是组节点（op）或叶子（type）")
		return
	}

	if n.IsGroup() {
		v.walkGroup(n, depth, path)
		return
	}
	v.walkLeaf(n, path)
}

func (v *validator) walkGroup(n Node, depth int, path string) {
	switch n.Op {
	case OpAll, OpAny:
		if len(n.Children) == 0 {
			v.fail(CodeShapeInvalid, path, fmt.Sprintf("%s 至少需要 1 个子节点", n.Op))
			return
		}
	case OpNot:
		if len(n.Children) != 1 {
			v.fail(CodeShapeInvalid, path, fmt.Sprintf("not 恰好需要 1 个子节点，收到 %d", len(n.Children)))
			return
		}
	default:
		v.fail(CodeShapeInvalid, path, fmt.Sprintf("未知组运算 %q", n.Op))
		return
	}
	if n.Type != "" || n.Params != nil || n.Cmp != nil {
		v.fail(CodeShapeInvalid, path, "组节点不接受 type / params / cmp")
		return
	}
	for i, child := range n.Children {
		v.walk(child, depth+1, fmt.Sprintf("%s.children[%d]", path, i))
	}
}

func (v *validator) walkLeaf(n Node, path string) {
	if len(n.Children) > 0 {
		v.fail(CodeShapeInvalid, path, "叶子节点不接受 children")
		return
	}
	v.leaves++
	if v.leaves > MaxLeaves {
		v.fail(CodeTooManyLeaves, path, fmt.Sprintf("叶子数超过上限 %d", MaxLeaves))
		return
	}

	meta, ok := v.catalog.ConditionMeta(n.Type)
	if !ok {
		v.fail(CodeUnknownCondition, path, fmt.Sprintf("未知条件 %q", n.Type))
		return
	}

	switch meta.ValueKind {
	case ValueBoolean:
		if n.Cmp != nil {
			v.fail(CodeCompareForbidden, path+".cmp", fmt.Sprintf("布尔条件 %q 不接受 cmp", n.Type))
			return
		}
	case ValueCount:
		if n.Cmp == nil {
			v.fail(CodeCompareMissing, path+".cmp", fmt.Sprintf("计数条件 %q 必须带 cmp", n.Type))
			return
		}
		if !slices.Contains(meta.AllowedCompare, n.Cmp.Op) {
			v.fail(CodeCompareNotAllowed, path+".cmp.op", fmt.Sprintf("条件 %q 不允许 %q", n.Type, n.Cmp.Op))
			return
		}
		if n.Cmp.Value < MinCompareValue || n.Cmp.Value > MaxCompareValue {
			v.fail(CodeValueOutOfRange, path+".cmp.value", fmt.Sprintf("value 必须在 %d..%d", MinCompareValue, MaxCompareValue))
			return
		}
	default:
		v.fail(CodeCatalogInvalid, path, fmt.Sprintf("条件 %q 的 valueKind 非法：%q", n.Type, meta.ValueKind))
		return
	}

	v.walkParams(meta.Params, n.Params, path+".params")
}

func (v *validator) walkParams(schema ParamsSchema, params map[string]any, path string) {
	if v.err != nil {
		return
	}
	for key := range params {
		if !slices.ContainsFunc(schema.Specs, func(spec ParamSpec) bool { return spec.Name == key }) {
			v.fail(CodeParamUnknown, path+"."+key, "未声明的参数")
			return
		}
	}
	for _, spec := range schema.Specs {
		value, present := params[spec.Name]
		if !present || value == nil {
			if spec.Required {
				v.fail(CodeParamMissing, path+"."+spec.Name, "缺少必需参数")
				return
			}
			continue
		}
		v.walkParam(spec, value, path+"."+spec.Name)
		if v.err != nil {
			return
		}
	}
}

func (v *validator) walkParam(spec ParamSpec, value any, path string) {
	switch spec.Kind {
	case ParamEnum:
		text, ok := value.(string)
		if !ok {
			v.fail(CodeParamInvalid, path, "参数必须是字符串")
			return
		}
		// Options == nil 是**开放集**：候选由域层在运行期提供（插件事实条件的 variant
		// 就是这种），引擎不可能知道全集。这里只做形态校验，成员资格由域层按「当前
		// 已声明的候选」兜底（`Models/Badges/rule_store.go`）。空集合（`[]string{}`）
		// 仍然是封闭的空集，一个值都放不过——两种 nil 语义不能混。
		if spec.Options == nil {
			if !validOpenEnumValue(text) {
				v.fail(CodeParamInvalid, path, fmt.Sprintf("取值 %q 不符合开放集参数的形态", text))
			}
			return
		}
		if !slices.Contains(spec.Options, text) {
			v.fail(CodeParamInvalid, path, fmt.Sprintf("取值 %q 不在允许集合内", text))
		}
	case ParamObjectRef:
		// 存在性（这个 slug / roleKey 是否还在）属于领域层保存路径；引擎只挡空引用，
		// 空串几乎总是「编辑器没填」而不是有意指向某个对象。
		text, ok := value.(string)
		if !ok {
			v.fail(CodeParamInvalid, path, "参数必须是字符串")
			return
		}
		if text == "" {
			v.fail(CodeParamInvalid, path, "参数不能是空字符串")
		}
	case ParamInt:
		number, ok := intValue(value)
		if !ok {
			v.fail(CodeParamInvalid, path, "参数必须是整数")
			return
		}
		// 天数区间是引擎级产品口径（见 MaxWindowDays）：schema 漏声明范围也不放开。
		min, max := spec.Min, spec.Max
		if spec.Name == "days" {
			if min < MinWindowDays {
				min = MinWindowDays
			}
			if max == 0 || max > MaxWindowDays {
				max = MaxWindowDays
			}
		}
		if min != 0 && number < min {
			v.fail(CodeParamInvalid, path, fmt.Sprintf("参数不得小于 %d", min))
			return
		}
		if max != 0 && number > max {
			v.fail(CodeParamInvalid, path, fmt.Sprintf("参数不得大于 %d", max))
		}
	case ParamString:
		if _, ok := value.(string); !ok {
			v.fail(CodeParamInvalid, path, "参数必须是字符串")
		}
	case ParamBool:
		if _, ok := value.(bool); !ok {
			v.fail(CodeParamInvalid, path, "参数必须是布尔值")
		}
	case ParamWindow:
		v.walkWindow(value, path)
	default:
		v.fail(CodeCatalogInvalid, path, fmt.Sprintf("参数 %q 的 kind 非法：%q", spec.Name, spec.Kind))
	}
}

// walkWindow 校验时间窗口对象 `{kind, days?}`。
//
// 形状是**固定的产品词汇**，所以写在这里而不是让每个条件自己拼一个 object schema：
// `kind=all` 时不允许带 days（「全部时间 + 30 天」是自相矛盾的输入，静默忽略等于
// 让运营以为筛选生效了）；`kind=recent_days` 时 days 必填且落在 1..3650。
func (v *validator) walkWindow(value any, path string) {
	window, ok := value.(map[string]any)
	if !ok {
		v.fail(CodeParamInvalid, path, "window 必须是对象")
		return
	}
	for key := range window {
		if key != "kind" && key != "days" {
			v.fail(CodeParamUnknown, path+"."+key, "window 不认识的键")
			return
		}
	}
	kindValue, present := window["kind"]
	if !present || kindValue == nil {
		v.fail(CodeParamMissing, path+".kind", "window 缺少 kind")
		return
	}
	kind, ok := kindValue.(string)
	if !ok {
		v.fail(CodeParamInvalid, path+".kind", "window.kind 必须是字符串")
		return
	}
	switch kind {
	case WindowKindAll:
		if _, hasDays := window["days"]; hasDays {
			v.fail(CodeParamInvalid, path+".days", "window.kind=all 不接受 days")
		}
	case WindowKindRecentDays:
		days, present := window["days"]
		if !present || days == nil {
			v.fail(CodeParamMissing, path+".days", "window.kind=recent_days 需要 days")
			return
		}
		number, ok := intValue(days)
		if !ok {
			v.fail(CodeParamInvalid, path+".days", "days 必须是整数")
			return
		}
		if number < MinWindowDays || number > MaxWindowDays {
			v.fail(CodeParamInvalid, path+".days", fmt.Sprintf("days 必须在 %d..%d", MinWindowDays, MaxWindowDays))
		}
	default:
		v.fail(CodeParamInvalid, path+".kind", fmt.Sprintf("未知 window.kind %q", kind))
	}
}

// intValue 把参数值收敛成整数。
//
// JSON 解码出来的数字是 float64，而程序里构造节点（测试、迁移、默认规则）用的是
// int / int64。只认 float64 会让「同一棵树从代码里构造就通不过校验」——那不是
// 校验严格，是类型泄漏。非整数（1.5）一律拒绝，不做四舍五入。
func intValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if math.Trunc(typed) != typed || typed > math.MaxInt64 || typed < math.MinInt64 {
			return 0, false
		}
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// OpenEnumValuePattern 是开放集参数取值的字符集与首字符约束。
//
// 与控制器/域层的标识符习惯一致：小写字母或数字开头，随后是小写字母、数字、点、
// 下划线、连字符。长度上限沿用 `variant` 列的 1–64（迁移 202611120002 的
// `variant TEXT NOT NULL DEFAULT ”` 无 CHECK，所以这里是唯一收口处之一）。
var OpenEnumValuePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// validOpenEnumValue 校验开放集（`Options == nil`）参数的取值形态。
func validOpenEnumValue(value string) bool {
	return OpenEnumValuePattern.MatchString(value)
}
