package ruleengine_test

import (
	"fmt"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

func TestValidateShapeErrors(t *testing.T) {
	cases := []struct {
		name string
		node ruleengine.Node
		code string
	}{
		{
			name: "op 与 type 共存",
			node: ruleengine.Node{Op: ruleengine.OpAll, Type: "identity.email_verified"},
			code: ruleengine.CodeShapeInvalid,
		},
		{
			name: "既无 op 也无 type",
			node: ruleengine.Node{Params: map[string]any{}},
			code: ruleengine.CodeShapeInvalid,
		},
		{
			name: "all 没有子节点",
			node: group(ruleengine.OpAll),
			code: ruleengine.CodeShapeInvalid,
		},
		{
			name: "not 有两个子节点",
			node: group(ruleengine.OpNot, leaf("identity.email_verified", nil, nil), leaf("identity.email_verified", nil, nil)),
			code: ruleengine.CodeShapeInvalid,
		},
		{
			name: "not 没有子节点",
			node: group(ruleengine.OpNot),
			code: ruleengine.CodeShapeInvalid,
		},
		{
			name: "未知组运算",
			node: ruleengine.Node{Op: "xor", Children: []ruleengine.Node{leaf("identity.email_verified", nil, nil)}},
			code: ruleengine.CodeShapeInvalid,
		},
		{
			name: "组节点带 cmp",
			node: ruleengine.Node{Op: ruleengine.OpAll, Children: []ruleengine.Node{leaf("identity.email_verified", nil, nil)}, Cmp: gte(1)},
			code: ruleengine.CodeShapeInvalid,
		},
		{
			name: "叶子带 children",
			node: ruleengine.Node{Type: "identity.email_verified", Children: []ruleengine.Node{leaf("identity.email_verified", nil, nil)}},
			code: ruleengine.CodeShapeInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCode(t, ruleengine.Validate(tc.node, testCatalog()), tc.code)
		})
	}
}

func TestValidateConditionAndCompare(t *testing.T) {
	countParams := map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "all"}}
	cases := []struct {
		name string
		node ruleengine.Node
		code string
	}{
		{
			name: "未知条件",
			node: leaf("activity.unknown", nil, gte(1)),
			code: ruleengine.CodeUnknownCondition,
		},
		{
			name: "计数条件缺 cmp",
			node: leaf("activity.count", countParams, nil),
			code: ruleengine.CodeCompareMissing,
		},
		{
			name: "布尔条件带 cmp",
			node: leaf("identity.email_verified", nil, gte(1)),
			code: ruleengine.CodeCompareForbidden,
		},
		{
			name: "不允许的比较运算",
			node: leaf("activity.count", countParams, &ruleengine.Compare{Op: ruleengine.CompareGT, Value: 3}),
			code: ruleengine.CodeCompareNotAllowed,
		},
		{
			name: "value 为 0",
			node: leaf("activity.count", countParams, gte(0)),
			code: ruleengine.CodeValueOutOfRange,
		},
		{
			name: "value 超上限",
			node: leaf("activity.count", countParams, gte(1_000_000_001)),
			code: ruleengine.CodeValueOutOfRange,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCode(t, ruleengine.Validate(tc.node, testCatalog()), tc.code)
		})
	}
}

func TestValidateParams(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		code   string
	}{
		{"未声明参数", map[string]any{"metric": "topic_count", "bogus": 1}, ruleengine.CodeParamUnknown},
		{"缺必需参数", map[string]any{"window": map[string]any{"kind": "all"}}, ruleengine.CodeParamMissing},
		{"枚举外取值", map[string]any{"metric": "post_count"}, ruleengine.CodeParamInvalid},
		{"枚举值不是字符串", map[string]any{"metric": 7}, ruleengine.CodeParamInvalid},
		{"window 不是对象", map[string]any{"metric": "topic_count", "window": "all"}, ruleengine.CodeParamInvalid},
		{"window 缺 kind", map[string]any{"metric": "topic_count", "window": map[string]any{"days": 3}}, ruleengine.CodeParamMissing},
		{"window 未知键", map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "all", "tz": "UTC"}}, ruleengine.CodeParamUnknown},
		{"window kind 未知", map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "last_year"}}, ruleengine.CodeParamInvalid},
		{"window kind=all 带 days", map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "all", "days": 3}}, ruleengine.CodeParamInvalid},
		{"window recent_days 缺 days", map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "recent_days"}}, ruleengine.CodeParamMissing},
		{"window days 下界", map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "recent_days", "days": 0}}, ruleengine.CodeParamInvalid},
		{"window days 上界", map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "recent_days", "days": 3651}}, ruleengine.CodeParamInvalid},
		{"window days 非整数", map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "recent_days", "days": 1.5}}, ruleengine.CodeParamInvalid},
		{"整型参数是字符串", map[string]any{"minDays": "7"}, ruleengine.CodeParamInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			condType := "activity.count"
			if _, ok := tc.params["minDays"]; ok {
				condType = "activity.streak"
			}
			wantCode(t, ruleengine.Validate(leaf(condType, tc.params, gte(3)), testCatalog()), tc.code)
		})
	}
}

func TestValidateReservedKinds(t *testing.T) {
	// string / bool 是给 M3 插件条件预留的 kind，引擎必须支持它们的类型检查。
	catalog := ruleengine.CatalogMap{
		"plugin.text": {
			Key:       "plugin.text",
			ValueKind: ruleengine.ValueBoolean,
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "note", Kind: ruleengine.ParamString},
				{Name: "flag", Kind: ruleengine.ParamBool},
			}},
		},
	}
	must := leaf("plugin.text", map[string]any{"note": "hello", "flag": true}, nil)
	if err := ruleengine.Validate(must, catalog); err != nil {
		t.Fatalf("合法 string/bool 参数应通过: %v", err)
	}
	wantCode(t, ruleengine.Validate(leaf("plugin.text", map[string]any{"note": 1}, nil), catalog), ruleengine.CodeParamInvalid)
	wantCode(t, ruleengine.Validate(leaf("plugin.text", map[string]any{"flag": "true"}, nil), catalog), ruleengine.CodeParamInvalid)
}

func TestValidateObjectRef(t *testing.T) {
	mustValidate(t, leaf("badge.owned", map[string]any{"slug": "veteran"}, nil))
	wantCode(t, ruleengine.Validate(leaf("badge.owned", map[string]any{"slug": ""}, nil), testCatalog()), ruleengine.CodeParamInvalid)
	wantCode(t, ruleengine.Validate(leaf("badge.owned", map[string]any{"slug": 7}, nil), testCatalog()), ruleengine.CodeParamInvalid)
	wantCode(t, ruleengine.Validate(leaf("badge.owned", nil, nil), testCatalog()), ruleengine.CodeParamMissing)
}

func TestValidateWindowDaysRangeIndependentOfSchema(t *testing.T) {
	// window 的 days 区间是引擎级产品口径：条件不需要、也无法自己放宽。
	catalog := ruleengine.CatalogMap{
		"activity.count": {
			Key:            "activity.count",
			ValueKind:      ruleengine.ValueCount,
			AllowedCompare: []ruleengine.CompareOp{ruleengine.CompareGTE},
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "window", Kind: ruleengine.ParamWindow},
			}},
		},
	}
	ok := leaf("activity.count", map[string]any{"window": map[string]any{"kind": "recent_days", "days": 3650}}, gte(1))
	if err := ruleengine.Validate(ok, catalog); err != nil {
		t.Fatalf("days=3650 应通过: %v", err)
	}
	tooBig := leaf("activity.count", map[string]any{"window": map[string]any{"kind": "recent_days", "days": 3651}}, gte(1))
	wantCode(t, ruleengine.Validate(tooBig, catalog), ruleengine.CodeParamInvalid)
}

func TestValidateDepthBoundary(t *testing.T) {
	// 根节点算第 1 层：5 层合法，6 层拒绝。
	build := func(depth int) ruleengine.Node {
		node := leaf("identity.email_verified", nil, nil)
		for i := 1; i < depth; i++ {
			node = group(ruleengine.OpNot, node)
		}
		return node
	}
	mustValidate(t, build(ruleengine.MaxDepth))
	wantCode(t, ruleengine.Validate(build(ruleengine.MaxDepth+1), testCatalog()), ruleengine.CodeTooDeep)
}

func TestValidateLeafBoundary(t *testing.T) {
	build := func(count int) ruleengine.Node {
		children := make([]ruleengine.Node, 0, count)
		for i := 0; i < count; i++ {
			children = append(children, leaf("identity.email_verified", nil, nil))
		}
		return group(ruleengine.OpAll, children...)
	}
	mustValidate(t, build(ruleengine.MaxLeaves))
	wantCode(t, ruleengine.Validate(build(ruleengine.MaxLeaves+1), testCatalog()), ruleengine.CodeTooManyLeaves)
}

func TestValidateNodeBoundary(t *testing.T) {
	// 组节点多、叶子少：验证节点上限独立生效（叶子上限此时不会先触发）。
	build := func(wrappers int) ruleengine.Node {
		children := make([]ruleengine.Node, 0, wrappers)
		for i := 0; i < wrappers; i++ {
			children = append(children, group(ruleengine.OpNot, group(ruleengine.OpNot, leaf("identity.email_verified", nil, nil))))
		}
		return group(ruleengine.OpAll, children...)
	}
	// 1 个根 + 21 组 = 64 个节点、21 个叶子。
	mustValidate(t, build(21))
	if nodes := 1 + 21*3; nodes != ruleengine.MaxNodes {
		t.Fatalf("夹具节点数应为 %d，实际 %d", ruleengine.MaxNodes, nodes)
	}
	// 再加一层 not(leaf)：66 个节点，越过上限。
	over := build(21)
	over.Children = append(over.Children, group(ruleengine.OpNot, leaf("identity.email_verified", nil, nil)))
	wantCode(t, ruleengine.Validate(over, testCatalog()), ruleengine.CodeTooManyNodes)
}

func TestValidateReportsPath(t *testing.T) {
	node := group(ruleengine.OpAll,
		leaf("identity.email_verified", nil, nil),
		group(ruleengine.OpAny,
			leaf("content.hidden", nil, gte(1)),
			leaf("nope.condition", nil, gte(1)),
		),
	)
	err := ruleengine.Validate(node, testCatalog())
	wantCode(t, err, ruleengine.CodeUnknownCondition)
	if got := fmt.Sprint(err); got != "ruleengine: unknown_condition at root.children[1].children[1]: 未知条件 \"nope.condition\"" {
		t.Fatalf("错误路径不符: %s", got)
	}
}

func TestValidateNilCatalog(t *testing.T) {
	wantCode(t, ruleengine.Validate(leaf("identity.email_verified", nil, nil), nil), ruleengine.CodeCatalogInvalid)
}
