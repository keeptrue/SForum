package trustlevels

import (
	"context"
	"encoding/json"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// RuleOutcome 是一棵规则树的求值结论（Kleene 三值的对外形态）。
//
// `Matched` 与 `Incomplete` **互斥**：`RuleEngine.Eval` 里 Matched 只取 triTrue、
// Incomplete 只取 triUnknown。调用方按「Matched 才成立」判定，绝不能写成
// `!Incomplete`（那会把 false 也算成立）。
type RuleOutcome struct {
	Matched    bool
	Incomplete bool
	// Problems 是被求值过的叶子里出问题的那几个（查询失败、未声明 factKey、脏规则）。
	// 它们不阻断判定，只用于 `Evaluation.Incomplete` 的归因与外传。
	Problems []error
}

// RuleEvaluator 抽象「一个用户的规则求值」。
//
// 它存在的原因是判定层（`LevelOfWithRules`）必须是纯函数式可测的：真正查库的实现
// 在 `conditionsEvaluator`，测试用 fake 就能驱动「某档规则成立/不成立/判不了」三条
// 分支，不需要数据库。
//
// 三个方法都**不返回 error**：任何失败都已经收敛进 `RuleOutcome`（fail-closed +
// Incomplete）。这不是偷懒——如果 Match 能返回 error，`LevelOfWithRules` 就必须决定
// 「求值失败时这一档算不算成立」，而唯一安全的答案是「不算」，那 error 只是噪声。
// 真正会中断整次评估的只有 ctx 取消，它由调用方在循环里检查。
type RuleEvaluator interface {
	// Prepare 预先加载 rules 涉及的全部条件：同一条件类型只查一次。
	//
	// 调用方必须把**所有可能被求值的树**一次传进来。逐棵 Prepare 会让同 type 被查
	// N 次（`Conditions/batch.go` 的 Prepare 不读 memo，D19 已裁决）。
	Prepare(ctx context.Context, rules []json.RawMessage)
	// Match 求值一棵树。
	Match(ctx context.Context, rule json.RawMessage) RuleOutcome
	// LeafConditions 把一棵**扁平 AND** 树逐叶翻成双语短句（供「还差哪几项」）。
	//
	// 非扁平（含 `any` / `not` / 嵌套）返回 nil：公开面只允许逐条句子出现在
	// 「全部满足才算」的形状里，`any` 的逐条句子会让读者以为「满足其中一条即可升级」。
	LeafConditions(ctx context.Context, rule json.RawMessage) []TierCondition
}

// TierCondition 是「离下一档还差哪几项」里的一条。
//
// 只带 `Satisfied` 与双语句子：**不带条件 key、不带参数、不带比较符**。公开面的
// 读者是终端用户，不是运营；把 `activity.count{metric:"topic_count"} >= 3` 的形状
// 发出去等于把判定逻辑印在公告板上（验收 6 的值级断言就是钉这个）。
type TierCondition struct {
	Satisfied bool
	Text      LocalizedName
}

// conditionsEvaluator 把 RuleEvaluator 落到共享条件解析器上。
//
// 一个实例**只服务一个用户**（内部持 `conditions.Resolver`，而它带用户 ID）。
type conditionsEvaluator struct {
	resolver *conditions.Resolver
	// decoded 缓存「规则字节 → AST」：同一次用户求值里 Prepare 与 Match 都要这棵树，
	// 解两遍纯属浪费，而且 Decode 的相对开销不小（叶子最多 32 个）。
	decoded map[string]ruleengine.Node
	broken  map[string]error
}

// NewRuleEvaluator 把一个针对单个用户的条件解析器适配成 `RuleEvaluator`。
//
// resolver 为 nil 时返回 nil：调用方据此走「无求值能力」分支（配了规则的档位一律
// 不成立，fail-closed），而不是在这里编一个恒真的实现。
func NewRuleEvaluator(resolver *conditions.Resolver) RuleEvaluator {
	if resolver == nil {
		return nil
	}
	return &conditionsEvaluator{
		resolver: resolver,
		decoded:  make(map[string]ruleengine.Node, 4),
		broken:   make(map[string]error, 1),
	}
}

// Prepare 把多棵树合成**一棵 `all` 根**只喂给 `Prepare`。
//
// 合成根**禁止进 `Validate`**（D19-b）：每一档单独 ≤32 叶 / ≤64 节点，合起来会超，
// 而对合成根判上限会凭空拒掉「8 档各 32 叶」这种每档都合法的配置。`Prepare` 本身
// 不查上限，只按 type 收集叶子各查一次——这正是我们要的效果。
//
// 合成根**不进任何返回值/字段/参数**：它在这个函数里构造、立刻交出、随即弃用，
// 外部拿不到它（包边界 + 未导出即可保证，不需要测试来守）。
func (e *conditionsEvaluator) Prepare(ctx context.Context, rules []json.RawMessage) {
	if e == nil || e.resolver == nil || len(rules) == 0 {
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}
	children := make([]ruleengine.Node, 0, len(rules))
	for _, rule := range rules {
		node, ok := e.node(rule)
		if !ok {
			continue
		}
		children = append(children, node)
	}
	if len(children) == 0 {
		return
	}
	root := ruleengine.Node{Op: ruleengine.OpAll, Children: children}
	// 返回值刻意丢弃：`Prepare` 的 error 只有 ctx 取消，而逐 type 的失败已经记在
	// resolver 的 groupErrs 里，会在 Resolve/Match 时作为该档的 unknown 冒出来。
	_ = e.resolver.Prepare(ctx, root)
}

// Match 求值一棵树。
func (e *conditionsEvaluator) Match(ctx context.Context, rule json.RawMessage) RuleOutcome {
	if e == nil || e.resolver == nil {
		return RuleOutcome{Incomplete: true}
	}
	node, ok := e.node(rule)
	if !ok {
		// 脏规则（解码期本该拦住，但存量数据/手工改库仍可能带进来）：判不了 →
		// 该档不成立 + Incomplete。绝不能当成「无规则」（那等于 fail-open 白送一级）。
		return RuleOutcome{Incomplete: true, Problems: []error{e.broken[string(rule)]}}
	}
	result, err := ruleengine.Eval(ctx, node, e.resolver)
	if err != nil {
		// ctx 取消 / resolver 为 nil：本次求值没有结论。
		return RuleOutcome{Incomplete: true, Problems: []error{err}}
	}
	return RuleOutcome{
		Matched:    result.Matched,
		Incomplete: result.Incomplete,
		Problems:   result.Errors,
	}
}

// LeafConditions 逐叶求值并生成双语句子（只处理扁平 AND）。
func (e *conditionsEvaluator) LeafConditions(ctx context.Context, rule json.RawMessage) []TierCondition {
	if e == nil || e.resolver == nil {
		return nil
	}
	node, ok := e.node(rule)
	if !ok || node.Op != ruleengine.OpAll || len(node.Children) == 0 {
		return nil
	}
	conditionsOut := make([]TierCondition, 0, len(node.Children))
	// 泛称即可（「指定分类」）：真名解析要走徽章 store 的 `LoadRefNames`，而公开面
	// 的进度句子只需要让人看懂「还差哪一类事」，多一条查询换一个分类名不值（D17 的
	// 依赖方向也不允许本包为了显示名去 import Badges）。
	names := conditions.NewRefNames()
	for _, child := range node.Children {
		if child.IsGroup() {
			// 混合形状（all 里套 any）：整棵都不给逐条句子，避免「这一条满足就升级」的误读。
			return nil
		}
		definition, ok := conditions.Core().Definition(child.Type)
		if !ok {
			continue
		}
		satisfied := false
		if value, err := e.resolver.Resolve(ctx, ruleengine.Leaf{Type: child.Type, Params: child.Params}); err == nil {
			satisfied = leafSatisfied(child, value)
		}
		zh, en := definition.Phrase(child.Params, child.Cmp, names)
		if zh == "" && en == "" {
			continue
		}
		conditionsOut = append(conditionsOut, TierCondition{
			Satisfied: satisfied,
			Text:      LocalizedName{Zh: zh, En: en},
		})
	}
	return conditionsOut
}

// leafSatisfied 用**与引擎同一套**比较规则判一个叶子是否成立。
//
// 这是唯一一处「引擎之外再算一次叶子」的地方，所以必须与 `RuleEngine/eval.go` 的
// leaf 分支逐条对齐：布尔条件不看 cmp（带了 cmp 引擎判 unknown → 这里按不满足），
// 计数条件缺 cmp 同样按不满足。任何偏差都只会让进度显示错，不影响判定本身——
// 这也是它可以用「重新算一遍」而不是「读引擎中间状态」的理由。
func leafSatisfied(node ruleengine.Node, value ruleengine.Value) bool {
	switch value.Kind {
	case ruleengine.ValueBoolean:
		return node.Cmp == nil && value.Bool
	case ruleengine.ValueCount:
		if node.Cmp == nil {
			return false
		}
		switch node.Cmp.Op {
		case ruleengine.CompareGTE:
			return value.Count >= node.Cmp.Value
		case ruleengine.CompareGT:
			return value.Count > node.Cmp.Value
		case ruleengine.CompareLTE:
			return value.Count <= node.Cmp.Value
		case ruleengine.CompareLT:
			return value.Count < node.Cmp.Value
		case ruleengine.CompareEQ:
			return value.Count == node.Cmp.Value
		}
		return false
	}
	return false
}

// node 解码并缓存一棵规则树。
func (e *conditionsEvaluator) node(rule json.RawMessage) (ruleengine.Node, bool) {
	if len(rule) == 0 {
		return ruleengine.Node{}, false
	}
	key := string(rule)
	if node, ok := e.decoded[key]; ok {
		return node, true
	}
	if err, ok := e.broken[key]; ok {
		_ = err
		return ruleengine.Node{}, false
	}
	node, err := ruleengine.Decode(rule)
	if err != nil {
		e.broken[key] = err
		return ruleengine.Node{}, false
	}
	e.decoded[key] = node
	return node, true
}
