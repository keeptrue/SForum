package ruleengine

// 本文件只描述「条件长什么样」，不含 i18n 文案、不含 SQL、不含任何领域知识。
// 领域注册表（`Models/Conditions`）负责把 key 填进来并实现 Catalog。

// MaxWindowDays 是窗口类条件的最大回溯天数（约十年）。
//
// 它是**产品口径而不是某个条件的自由参数**：上界存在的意义是让「窗口」不会退化成
// 「全量扫描 + 无界区间」，同时挡住 `days: 999999` 这种把索引扫穿的输入。
// 因此 Validate 对任何名为 `days` 的整型参数无条件套用这个区间，即使条件自己的
// 参数 schema 忘了声明范围——schema 漏写不该变成一条可被利用的慢查询。
const MaxWindowDays = 3650

// MinWindowDays 是窗口类条件的最小回溯天数。
const MinWindowDays = 1

// ParamKind 是参数值的形状。**这四个 kind 是 M2 编辑器的校验依据**（前端已按此
// 定稿）：`int` 走 min/max，`enum` 走 options，`window` 是固定的时间窗口对象，
// `objectRef` 是指向 Core 对象的引用（topic / category / tag / role / provider /
// badge），其存在性由领域层在保存路径校验，引擎只保证「非空字符串」。
type ParamKind string

const (
	ParamInt       ParamKind = "int"
	ParamEnum      ParamKind = "enum"
	ParamWindow    ParamKind = "window"
	ParamObjectRef ParamKind = "objectRef"
	// ParamString / ParamBool 只给 M3 插件条件用（自由文本 / 开关）；内置 12 条
	// 一条都不用它们——内置条件的每个取值要么可枚举、要么可解析成 Core 对象。
	ParamString ParamKind = "string"
	ParamBool   ParamKind = "bool"
)

// window 参数的 kind 取值。窗口形状是产品词汇，不是每个条件自由定义的。
const (
	WindowKindAll        = "all"
	WindowKindRecentDays = "recent_days"
)

// ParamSpec 描述一个参数。
//
// 字段名与前端编辑器的契约一致（name / kind / required / min / max / options），
// 改名前先看 M2 的 `badgeRule.ts`：它按这些字段决定必填标记与下拉候选。
//
// Min / Max 只对 ParamInt 生效；0 / 0 表示不限制（名为 `days` 的整型参数另有
// 引擎级区间，见 MaxWindowDays）。RefKind 只在 ParamObjectRef 时有意义，告诉
// 编辑器该渲染哪类候选（category / tag / role / provider / badge）。
type ParamSpec struct {
	Name     string
	Kind     ParamKind
	Required bool
	Min      int64
	Max      int64
	Options  []string
	RefKind  string
}

// ParamsSchema 是一个条件（或一个复合参数）的参数白名单。
//
// 未在 Specs 中出现的键一律拒绝：规则的 params 是运营输入，静默忽略未知键会让
// 「写错的规则看起来保存成功、实际永不命中」，比报错难查得多。
type ParamsSchema struct {
	Specs []ParamSpec
}

// ConditionMeta 是一个条件的元数据，供 Validate 使用。
type ConditionMeta struct {
	Key            string
	ValueKind      ValueKind
	AllowedCompare []CompareOp
	Params         ParamsSchema
}

// Catalog 是条件白名单。key 是规则 JSON 里 `type` 的取值。
type Catalog interface {
	ConditionMeta(key string) (ConditionMeta, bool)
}

// CatalogMap 是 Catalog 的静态实现，便于测试与只读注册表直接返回。
type CatalogMap map[string]ConditionMeta

// ConditionMeta 实现 Catalog。
func (m CatalogMap) ConditionMeta(key string) (ConditionMeta, bool) {
	meta, ok := m[key]
	return meta, ok
}
