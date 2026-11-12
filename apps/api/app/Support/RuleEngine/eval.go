package ruleengine

import (
	"context"
	"errors"
	"fmt"
)

// Eval 在 resolver 上求值一棵表达式树。
//
// 返回的 error **只**表示「这次评估根本无法给出结论」：ctx 已取消、resolver 为 nil、
// 树本身形状非法到无法遍历。条件求值失败（查询超时、行不存在、参数不匹配）不进
// 顶层 error，而是作为该叶子按 false 计并收进 Result.Errors——一次评估里坏掉一个
// 条件不该让整批用户的评估全部失败，但也不能让它悄悄成立。
//
// 三值语义：内部区分 true / false / unknown，`not unknown` 仍是 unknown。若按二值
// 处理（把 unknown 当 false 再取反），「查询失败」会在 `not` 下面变成「条件成立」，
// 那是 fail-open。最终 unknown 收敛为 Matched=false 且 Incomplete=true。
//
// 短路：`all` 遇到 false、`any` 遇到 true 立即返回，后续叶子不再求值（少打查询）。
// 因此 Result.Errors 只包含「实际被求值过的叶子」的失败，这是刻意的。
//
// Eval 不要求先 Validate，**并且自己兜住畸形树**：空 `all`/`any`、`not` 子节点数
// 不等于 1、组节点带叶子字段、叶子带 children，一律判 unknown 并记
// `CodeShapeInvalid`。空 `all` 在二值逻辑里是 vacuously true，若按真值算，
// 一条绕过保存校验写进库的空组会让全库用户被授予该徽章且零日志——这是本包必须
// 自己防住的最贵失败模式，不能把责任推给「调用方应该先 Validate」。
func Eval(ctx context.Context, node Node, resolver Resolver) (Result, error) {
	if resolver == nil {
		return Result{}, errors.New("ruleengine: resolver 为 nil")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	e := &evaluator{ctx: ctx, resolver: resolver}
	state := e.walk(node, "root")
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return Result{
		Matched:    state == triTrue,
		Incomplete: state == triUnknown,
		Errors:     e.errs,
	}, nil
}

// triState 是 Kleene 三值逻辑的状态。零值刻意是 unknown：漏赋值时按「不确定」处理，
// 而不是按 false——把未赋值当 false 会在 `not` 下面变成 true。
type triState int

const (
	triUnknown triState = iota
	triFalse
	triTrue
)

type evaluator struct {
	ctx      context.Context
	resolver Resolver
	errs     []error
}

func (e *evaluator) record(path, code, detail string) {
	e.errs = append(e.errs, &Error{Code: code, Path: path, Detail: detail})
}

func (e *evaluator) walk(n Node, path string) triState {
	if e.ctx.Err() != nil {
		return triUnknown
	}
	switch {
	case n.IsGroup() && n.IsLeaf():
		e.record(path, CodeShapeInvalid, "节点同时有 op 与 type")
		return triUnknown
	case !n.IsGroup() && !n.IsLeaf():
		e.record(path, CodeShapeInvalid, "节点既不是组节点也不是叶子")
		return triUnknown
	}
	if n.IsGroup() {
		return e.group(n, path)
	}
	return e.leaf(n, path)
}

func (e *evaluator) group(n Node, path string) triState {
	// 组节点不接受叶子字段。畸形时判 unknown 而不是「忽略多余字段继续算」：
	// 一条 `{op:"all", cmp:{...}, children:[真叶子]}` 如果按真值算，写规则的人
	// 会以为 cmp 生效了，而它其实被静默丢弃。
	if n.Type != "" || n.Params != nil || n.Cmp != nil {
		e.record(path, CodeShapeInvalid, "组节点不接受 type / params / cmp")
		return triUnknown
	}
	switch n.Op {
	case OpAll:
		// 空 all 在二值逻辑里是 vacuously true——那会让「规则解析失败/被写成空组」
		// 的徽章瞬间发给全库用户。这是本包最贵的失败模式，所以在这里硬拒。
		if len(n.Children) == 0 {
			e.record(path, CodeShapeInvalid, "all 至少需要 1 个子节点")
			return triUnknown
		}
		out := triTrue
		for i, child := range n.Children {
			switch e.walk(child, fmt.Sprintf("%s.children[%d]", path, i)) {
			case triFalse:
				return triFalse
			case triUnknown:
				out = triUnknown
			}
		}
		return out
	case OpAny:
		if len(n.Children) == 0 {
			e.record(path, CodeShapeInvalid, "any 至少需要 1 个子节点")
			return triUnknown
		}
		out := triFalse
		for i, child := range n.Children {
			switch e.walk(child, fmt.Sprintf("%s.children[%d]", path, i)) {
			case triTrue:
				return triTrue
			case triUnknown:
				out = triUnknown
			}
		}
		return out
	case OpNot:
		if len(n.Children) != 1 {
			e.record(path, CodeShapeInvalid, fmt.Sprintf("not 需要恰好 1 个子节点，收到 %d", len(n.Children)))
			return triUnknown
		}
		switch e.walk(n.Children[0], path+".children[0]") {
		case triTrue:
			return triFalse
		case triFalse:
			return triTrue
		default:
			return triUnknown
		}
	default:
		e.record(path, CodeShapeInvalid, fmt.Sprintf("未知组运算 %q", n.Op))
		return triUnknown
	}
}

func (e *evaluator) leaf(n Node, path string) triState {
	// 叶子带 children 是「把组节点写成了叶子」的典型笔误：忽略 children 会把
	// 一个本该是 `all(a,b)` 的规则降级成只判 a。
	if len(n.Children) > 0 {
		e.record(path, CodeShapeInvalid, "叶子节点不接受 children")
		return triUnknown
	}
	value, err := e.resolver.Resolve(e.ctx, Leaf{Type: n.Type, Params: n.Params})
	if err != nil {
		if e.ctx.Err() != nil {
			// 取消是整次评估的问题，交给顶层转成 error，不记为「条件失败」。
			return triUnknown
		}
		e.record(path, CodeResolveFailed, fmt.Sprintf("条件 %q 求值失败：%v", n.Type, err))
		return triUnknown
	}

	switch value.Kind {
	case ValueBoolean:
		if n.Cmp != nil {
			e.record(path+".cmp", CodeCompareForbidden, fmt.Sprintf("布尔条件 %q 不接受 cmp", n.Type))
			return triUnknown
		}
		if value.Bool {
			return triTrue
		}
		return triFalse
	case ValueCount:
		if n.Cmp == nil {
			e.record(path+".cmp", CodeCompareMissing, fmt.Sprintf("计数条件 %q 缺少 cmp", n.Type))
			return triUnknown
		}
		matched, ok := compare(value.Count, n.Cmp.Op, n.Cmp.Value)
		if !ok {
			e.record(path+".cmp.op", CodeCompareInvalid, fmt.Sprintf("未知比较运算 %q", n.Cmp.Op))
			return triUnknown
		}
		if matched {
			return triTrue
		}
		return triFalse
	default:
		e.record(path, CodeResolveFailed, fmt.Sprintf("条件 %q 返回了未知 valueKind %q", n.Type, value.Kind))
		return triUnknown
	}
}

// compare 返回 (是否成立, 运算是否被识别)。第二项为 false 时调用方记错误而不是
// 当成不成立——静默把未知运算当 false 会让一条写错的规则永久不命中。
func compare(got int64, op CompareOp, want int64) (bool, bool) {
	switch op {
	case CompareGTE:
		return got >= want, true
	case CompareGT:
		return got > want, true
	case CompareLTE:
		return got <= want, true
	case CompareLT:
		return got < want, true
	case CompareEQ:
		return got == want, true
	}
	return false, false
}
