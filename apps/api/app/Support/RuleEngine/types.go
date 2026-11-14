// Package ruleengine 实现徽章规则的布尔表达式 AST：解码、校验、求值。
//
// 它刻意是**纯函数**：不 import 任何 `Models/` 领域包、不碰 SQL、不做 i18n。
// 领域层（`Models/Badges`）负责把条件键映射到具体查询；本包只回答两件事：
// 「这棵树形状合法吗」（Validate）与「这棵树在当前 Resolver 下是否成立」（Eval）。
//
// 这样切分的原因：规则的形状（all / any / not 的嵌套、上限、条件白名单）是产品
// 契约，会被后台表单、导入、回扫三处共用；而「某个条件在某个用户身上成立吗」是
// 领域查询，两者混在一起会逼着每个条件都顺手实现一遍树遍历。
package ruleengine

import "context"

// Node 是表达式树上的一个节点，两种形态互斥：
//
//	组节点：Op 非空，Children 非空（not 恰好一个），Params / Cmp 必须为空
//	叶子：  Type 非空，Children / Op 为空，按条件的 ValueKind 决定是否带 Cmp
//
// **为什么用单一结构体而不是 Group / Leaf 两个接口实现**：树是反序列化来的数据，
// 不是行为。接口会逼出 type switch、nil 判断和「未知实现」的兜底分支；单一结构体
// 让「形状错误」变成可以在 Validate 里逐条报出路径（`children[1].children[0]`）的
// 普通数据错误，后台才能把错误指到具体那个节点。
type Node struct {
	Op       GroupOp
	Children []Node
	Type     string
	Params   map[string]any
	Cmp      *Compare
}

// IsGroup 报告节点是否是组节点。
func (n Node) IsGroup() bool { return n.Op != "" }

// IsLeaf 报告节点是否是叶子条件。
func (n Node) IsLeaf() bool { return n.Type != "" }

// GroupOp 是组节点的布尔运算。括号语义由树结构天然表达，不引入优先级规则。
type GroupOp string

const (
	// OpAll 是合取：全部子节点成立才成立。
	OpAll GroupOp = "all"
	// OpAny 是析取：任一子节点成立即成立。
	OpAny GroupOp = "any"
	// OpNot 是取反：恰好一个子节点。
	OpNot GroupOp = "not"
)

// CompareOp 是计数值与期望值的比较运算符。
//
// 刻意不提供 `ne`：徽章的语义是「达到某条线」，「恰好不等于 N」不是运营会表达的
// 需求，却让 `all` 里的取反更容易写错。需要「未达成」请用 `not` 包住 `gte`。
type CompareOp string

const (
	CompareGTE CompareOp = "gte"
	CompareGT  CompareOp = "gt"
	CompareLTE CompareOp = "lte"
	CompareLT  CompareOp = "lt"
	CompareEQ  CompareOp = "eq"
)

// Compare 是叶子的比较子句。Value 的范围由 Validate 约束（1..1e9）。
type Compare struct {
	Op    CompareOp
	Value int64
}

// ValueKind 是条件求值结果的形状，决定叶子是否需要 Cmp。
type ValueKind string

const (
	// ValueCount 是可计数的条件：必须带 Cmp。
	ValueCount ValueKind = "count"
	// ValueBoolean 是是非型条件：禁止带 Cmp，直接取 Bool。
	ValueBoolean ValueKind = "boolean"
)

// Value 是 Resolver 对一个条件的求值结果。
//
// Kind 由 Resolver 按其条件元数据填写，Eval 据此选择读取 Count 还是 Bool：
// 这样 Eval 不需要 Catalog，签名保持 `Eval(ctx, node, resolver)`。
type Value struct {
	Kind  ValueKind
	Count int64
	Bool  bool
}

// Leaf 是交给 Resolver 的一次求值请求：叶子去掉比较子句后的部分。
//
// 引擎**逐叶子**调用 Resolver，不提供批量接口——「一次评估内按 type 分组、每类型
// 一次查询」是 `Models/Conditions/batch.go` 的实现细节。把批量塞进接口会逼着
// 引擎理解条件的查询语义，那就不是纯布尔引擎了。
type Leaf struct {
	Type   string
	Params map[string]any
}

// Resolver 把一个叶子解析成 Value。实现方（领域层）负责批量加载与 memo：
// 同一次 Eval 内同一条件可能被多次请求，缓存是实现方的责任，不是本包的。
type Resolver interface {
	Resolve(ctx context.Context, leaf Leaf) (Value, error)
}

// Result 是一次 Eval 的结论。
//
// **三值语义**：内部按 Kleene 三值逻辑计算（true / false / unknown），
// `not unknown` 仍然是 unknown 而不是 true——否则「查询失败」会在取反处变成
// 「条件成立」，等于 fail-open。最终 unknown 收敛为 Matched=false 并置
// Incomplete=true，把「不确定」与「确定不成立」区分开，调用方可以只记录不告警。
type Result struct {
	// Matched 是最终结论：只有确定成立才为 true。
	Matched bool
	// Incomplete 表示结论依赖了求值失败的条件，因此 Matched 是 fail-closed 的结果。
	Incomplete bool
	// Errors 收集所有求值失败的原因（按求值顺序）。
	Errors []error
}
