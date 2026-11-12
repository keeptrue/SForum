package ruleengine_test

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

func TestEvalTruthTable(t *testing.T) {
	cases := []struct {
		name   string
		node   ruleengine.Node
		values map[string]ruleengine.Value
		want   bool
	}{
		{
			name:   "all 全真",
			node:   group(ruleengine.OpAll, leaf("identity.email_verified", nil, nil), leaf("content.hidden", nil, gte(1))),
			values: map[string]ruleengine.Value{"identity.email_verified": boolean(true), "content.hidden": count(2)},
			want:   true,
		},
		{
			name:   "all 有一假",
			node:   group(ruleengine.OpAll, leaf("identity.email_verified", nil, nil), leaf("content.hidden", nil, gte(5))),
			values: map[string]ruleengine.Value{"identity.email_verified": boolean(true), "content.hidden": count(2)},
			want:   false,
		},
		{
			name:   "any 有一真",
			node:   group(ruleengine.OpAny, leaf("identity.email_verified", nil, nil), leaf("content.hidden", nil, gte(5))),
			values: map[string]ruleengine.Value{"identity.email_verified": boolean(true), "content.hidden": count(2)},
			want:   true,
		},
		{
			name:   "any 全假",
			node:   group(ruleengine.OpAny, leaf("identity.email_verified", nil, nil), leaf("content.hidden", nil, gte(5))),
			values: map[string]ruleengine.Value{"identity.email_verified": boolean(false), "content.hidden": count(2)},
			want:   false,
		},
		{
			name:   "not 取反",
			node:   group(ruleengine.OpNot, leaf("content.hidden", nil, gte(1))),
			values: map[string]ruleengine.Value{"content.hidden": count(0)},
			want:   true,
		},
		{
			name: "任意嵌套：((a AND b) OR NOT c) AND d",
			node: group(ruleengine.OpAll,
				group(ruleengine.OpAny,
					group(ruleengine.OpAll,
						leaf("activity.streak", map[string]any{"minDays": float64(3)}, gte(3)),
						leaf("identity.email_verified", nil, nil),
					),
					group(ruleengine.OpNot, leaf("content.hidden", nil, gte(1))),
				),
				leaf("activity.count", map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "all"}}, gte(10)),
			),
			values: map[string]ruleengine.Value{
				"activity.streak":            count(1), // a 假
				"identity.email_verified":    boolean(true),
				"content.hidden":             count(0), // not c = 真
				"activity.count/topic_count": count(12),
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustValidate(t, tc.node)
			result, err := ruleengine.Eval(context.Background(), tc.node, &stubResolver{values: tc.values})
			if err != nil {
				t.Fatalf("Eval 返回错误: %v", err)
			}
			if result.Matched != tc.want || result.Incomplete || len(result.Errors) != 0 {
				t.Fatalf("期望 matched=%v 无错误，得到 %+v", tc.want, result)
			}
		})
	}
}

func TestEvalFailClosed(t *testing.T) {
	node := group(ruleengine.OpAll, leaf("identity.email_verified", nil, nil), leaf("content.hidden", nil, gte(1)))
	resolver := &stubResolver{
		values: map[string]ruleengine.Value{"content.hidden": count(3)},
		errs:   map[string]error{"identity.email_verified": errStub},
	}
	result, err := ruleengine.Eval(context.Background(), node, resolver)
	if err != nil {
		t.Fatalf("求值失败不应变成顶层 error: %v", err)
	}
	if result.Matched || !result.Incomplete {
		t.Fatalf("解析失败应按 fail-closed 记为不成立且 Incomplete: %+v", result)
	}
	if len(result.Errors) != 1 || ruleengine.CodeOf(result.Errors[0]) != ruleengine.CodeResolveFailed {
		t.Fatalf("错误清单不符: %v", result.Errors)
	}
}

func TestEvalNotOfUnknownStaysUnknown(t *testing.T) {
	// 关键回归：not(unknown) 不能变成 true。
	node := group(ruleengine.OpNot, leaf("identity.email_verified", nil, nil))
	resolver := &stubResolver{errs: map[string]error{"identity.email_verified": errStub}}
	result, err := ruleengine.Eval(context.Background(), node, resolver)
	if err != nil {
		t.Fatalf("Eval 返回错误: %v", err)
	}
	if result.Matched || !result.Incomplete {
		t.Fatalf("not(unknown) 必须仍是 unknown: %+v", result)
	}
}

func TestEvalFalseDominatesUnknownInAll(t *testing.T) {
	node := group(ruleengine.OpAll,
		leaf("identity.email_verified", nil, nil),
		leaf("content.hidden", nil, gte(1)),
	)
	resolver := &stubResolver{
		values: map[string]ruleengine.Value{"identity.email_verified": boolean(false)},
		errs:   map[string]error{"content.hidden": errStub},
	}
	result, err := ruleengine.Eval(context.Background(), node, resolver)
	if err != nil {
		t.Fatalf("Eval 返回错误: %v", err)
	}
	if result.Matched || result.Incomplete {
		t.Fatalf("all 里已有确定的 false，结论应是确定不成立: %+v", result)
	}
}

func TestEvalTrueDominatesUnknownInAny(t *testing.T) {
	node := group(ruleengine.OpAny,
		leaf("identity.email_verified", nil, nil),
		leaf("content.hidden", nil, gte(1)),
	)
	resolver := &stubResolver{
		values: map[string]ruleengine.Value{"identity.email_verified": boolean(true)},
		errs:   map[string]error{"content.hidden": errStub},
	}
	result, err := ruleengine.Eval(context.Background(), node, resolver)
	if err != nil {
		t.Fatalf("Eval 返回错误: %v", err)
	}
	if !result.Matched || result.Incomplete {
		t.Fatalf("any 里已有确定的 true，结论应是确定成立: %+v", result)
	}
}

func TestEvalShortCircuits(t *testing.T) {
	node := group(ruleengine.OpAny,
		leaf("identity.email_verified", nil, nil),
		leaf("content.hidden", nil, gte(1)),
	)
	resolver := &stubResolver{values: map[string]ruleengine.Value{"identity.email_verified": boolean(true)}}
	if _, err := ruleengine.Eval(context.Background(), node, resolver); err != nil {
		t.Fatalf("Eval 返回错误: %v", err)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != "identity.email_verified" {
		t.Fatalf("any 命中后不应继续求值: %v", resolver.calls)
	}
}

func TestEvalUnvalidatedTreeDoesNotPanic(t *testing.T) {
	cases := map[string]ruleengine.Node{
		"计数叶子缺 cmp":  leaf("content.hidden", nil, nil),
		"not 子节点数不对": group(ruleengine.OpNot),
		"布尔叶子带 cmp":  leaf("identity.email_verified", nil, gte(1)),
	}
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			resolver := &stubResolver{values: map[string]ruleengine.Value{
				"content.hidden":          count(1),
				"identity.email_verified": boolean(true),
			}}
			result, err := ruleengine.Eval(context.Background(), node, resolver)
			if err != nil {
				t.Fatalf("Eval 返回错误: %v", err)
			}
			if result.Matched || !result.Incomplete || len(result.Errors) == 0 {
				t.Fatalf("未校验的树应记为 unknown + 错误: %+v", result)
			}
		})
	}
}

func TestEvalUnknownValueKind(t *testing.T) {
	node := leaf("content.hidden", nil, gte(1))
	resolver := &stubResolver{values: map[string]ruleengine.Value{"content.hidden": {Kind: "weird"}}}
	result, err := ruleengine.Eval(context.Background(), node, resolver)
	if err != nil {
		t.Fatalf("Eval 返回错误: %v", err)
	}
	if result.Matched || !result.Incomplete {
		t.Fatalf("未知 valueKind 应 fail-closed: %+v", result)
	}
}

func TestEvalNilResolver(t *testing.T) {
	if _, err := ruleengine.Eval(context.Background(), leaf("identity.email_verified", nil, nil), nil); err == nil {
		t.Fatal("resolver 为 nil 时应返回 error")
	}
}

func TestEvalCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	node := leaf("identity.email_verified", nil, nil)
	if _, err := ruleengine.Eval(ctx, node, &stubResolver{}); err == nil {
		t.Fatal("ctx 已取消时应返回 error")
	}
}

func TestEvalCompareOperators(t *testing.T) {
	cases := []struct {
		op    ruleengine.CompareOp
		got   int64
		want  int64
		match bool
	}{
		{ruleengine.CompareGTE, 5, 5, true},
		{ruleengine.CompareGTE, 4, 5, false},
		{ruleengine.CompareGT, 6, 5, true},
		{ruleengine.CompareGT, 5, 5, false},
		{ruleengine.CompareLTE, 5, 5, true},
		{ruleengine.CompareLTE, 6, 5, false},
		{ruleengine.CompareLT, 4, 5, true},
		{ruleengine.CompareLT, 5, 5, false},
		{ruleengine.CompareEQ, 5, 5, true},
		{ruleengine.CompareEQ, 4, 5, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.op), func(t *testing.T) {
			node := leaf("test.compare", nil, &ruleengine.Compare{Op: tc.op, Value: tc.want})
			mustValidate(t, node)
			result, err := ruleengine.Eval(context.Background(), node, &stubResolver{
				values: map[string]ruleengine.Value{"test.compare": count(tc.got)},
			})
			if err != nil {
				t.Fatalf("Eval 返回错误: %v", err)
			}
			if result.Matched != tc.match || result.Incomplete {
				t.Fatalf("%s(%d, %d) 期望 %v，得到 %+v", tc.op, tc.got, tc.want, tc.match, result)
			}
		})
	}
}

// hasCode 报告错误清单里是否含指定错误码。
func hasCode(errs []error, code string) bool {
	for _, err := range errs {
		if ruleengine.CodeOf(err) == code {
			return true
		}
	}
	return false
}

// P-12：空组必须判为 unknown。
//
// 为什么这条比「Validate 会拒」更重要：空 `all` 在二值逻辑里是 vacuously true，
// 一条绕过保存校验写进库的空 `all`（手工改库 / 导入工具 / 未来迁移）会让**全库
// 用户**在下一次回扫时拿到这枚徽章，而且零错误零日志。Eval 的契约是「不要求先
// Validate」，所以兜底必须在这里。
func TestEvalEmptyGroupStaysUnknown(t *testing.T) {
	for _, op := range []ruleengine.GroupOp{ruleengine.OpAll, ruleengine.OpAny} {
		t.Run(string(op), func(t *testing.T) {
			result, err := ruleengine.Eval(context.Background(), group(op), &stubResolver{})
			if err != nil {
				t.Fatalf("形状错误不应变成顶层 error: %v", err)
			}
			if result.Matched || !result.Incomplete {
				t.Fatalf("空 %s 必须 matched=false + incomplete=true，得到 %+v", op, result)
			}
			if !hasCode(result.Errors, ruleengine.CodeShapeInvalid) {
				t.Fatalf("空 %s 必须记 CodeShapeInvalid，得到 %v", op, result.Errors)
			}
		})
	}
}

func TestEvalNotArityStaysUnknown(t *testing.T) {
	cases := map[string]ruleengine.Node{
		"零个子节点": group(ruleengine.OpNot),
		"两个子节点": group(ruleengine.OpNot,
			leaf("identity.email_verified", nil, nil),
			leaf("identity.email_verified", nil, nil)),
	}
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := ruleengine.Eval(context.Background(), node, &stubResolver{
				values: map[string]ruleengine.Value{"identity.email_verified": boolean(true)},
			})
			if err != nil {
				t.Fatalf("形状错误不应变成顶层 error: %v", err)
			}
			if result.Matched || !result.Incomplete {
				t.Fatalf("not 子节点数不对必须 unknown，得到 %+v", result)
			}
			if !hasCode(result.Errors, ruleengine.CodeShapeInvalid) {
				t.Fatalf("必须记 CodeShapeInvalid，得到 %v", result.Errors)
			}
		})
	}
}

func TestEvalMalformedGroupStaysUnknown(t *testing.T) {
	cases := map[string]ruleengine.Node{
		"组节点带 cmp": {
			Op:       ruleengine.OpAll,
			Children: []ruleengine.Node{leaf("identity.email_verified", nil, nil)},
			Cmp:      gte(1),
		},
		"组节点带 type": {
			Op:       ruleengine.OpAll,
			Type:     "identity.email_verified",
			Children: []ruleengine.Node{leaf("identity.email_verified", nil, nil)},
		},
		"叶子带 children": {
			Type:     "identity.email_verified",
			Children: []ruleengine.Node{leaf("identity.email_verified", nil, nil)},
		},
	}
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := ruleengine.Eval(context.Background(), node, &stubResolver{
				values: map[string]ruleengine.Value{"identity.email_verified": boolean(true)},
			})
			if err != nil {
				t.Fatalf("形状错误不应变成顶层 error: %v", err)
			}
			if result.Matched || !result.Incomplete {
				t.Fatalf("畸形节点不得被当成真值，得到 %+v", result)
			}
			if !hasCode(result.Errors, ruleengine.CodeShapeInvalid) {
				t.Fatalf("必须记 CodeShapeInvalid，得到 %v", result.Errors)
			}
		})
	}
}

// 反向断言：兜底不能把正常路径也变成 unknown。
func TestEvalWellFormedTreeStillResolves(t *testing.T) {
	node := group(ruleengine.OpAll,
		group(ruleengine.OpAny,
			leaf("identity.email_verified", nil, nil),
			leaf("content.hidden", nil, gte(9)),
		),
		group(ruleengine.OpNot, leaf("content.hidden", nil, gte(1))),
	)
	result, err := ruleengine.Eval(context.Background(), node, &stubResolver{
		values: map[string]ruleengine.Value{
			"identity.email_verified": boolean(true),
			"content.hidden":          count(0),
		},
	})
	if err != nil {
		t.Fatalf("Eval 返回错误: %v", err)
	}
	if !result.Matched || result.Incomplete || len(result.Errors) != 0 {
		t.Fatalf("正常树必须确定成立且无错误，得到 %+v", result)
	}
}
