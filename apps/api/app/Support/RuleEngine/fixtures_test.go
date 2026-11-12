package ruleengine_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// testCatalog 是只覆盖引擎语义的最小目录：一个带嵌套对象参数的计数条件 +
// 一个布尔条件 + 一个只有整型参数的条件。真实目录在 `Models/Badges/conditions`。
func testCatalog() ruleengine.CatalogMap {
	return ruleengine.CatalogMap{
		"activity.count": {
			Key:            "activity.count",
			ValueKind:      ruleengine.ValueCount,
			AllowedCompare: []ruleengine.CompareOp{ruleengine.CompareGTE, ruleengine.CompareLTE},
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "metric", Required: true, Kind: ruleengine.ParamEnum, Options: []string{"topic_count", "comment_count"}},
				{Name: "window", Kind: ruleengine.ParamWindow},
			}},
		},
		"activity.streak": {
			Key:            "activity.streak",
			ValueKind:      ruleengine.ValueCount,
			AllowedCompare: []ruleengine.CompareOp{ruleengine.CompareGTE},
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "minDays", Required: true, Kind: ruleengine.ParamInt, Min: 1, Max: ruleengine.MaxWindowDays},
			}},
		},
		"identity.email_verified": {
			Key:       "identity.email_verified",
			ValueKind: ruleengine.ValueBoolean,
		},
		"badge.owned": {
			Key:       "badge.owned",
			ValueKind: ruleengine.ValueBoolean,
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "slug", Required: true, Kind: ruleengine.ParamObjectRef, RefKind: "badge"},
			}},
		},
		"content.hidden": {
			Key:            "content.hidden",
			ValueKind:      ruleengine.ValueCount,
			AllowedCompare: []ruleengine.CompareOp{ruleengine.CompareGTE},
		},
		// 只用于穷举比较运算符：真实条件不会同时允许五个。
		"test.compare": {
			Key:            "test.compare",
			ValueKind:      ruleengine.ValueCount,
			AllowedCompare: []ruleengine.CompareOp{ruleengine.CompareGTE, ruleengine.CompareGT, ruleengine.CompareLTE, ruleengine.CompareLT, ruleengine.CompareEQ},
		},
	}
}

func mustDecode(t *testing.T, raw string) ruleengine.Node {
	t.Helper()
	node, err := ruleengine.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("Decode 失败: %v", err)
	}
	return node
}

func mustValidate(t *testing.T, node ruleengine.Node) {
	t.Helper()
	if err := ruleengine.Validate(node, testCatalog()); err != nil {
		t.Fatalf("Validate 期望通过，得到: %v", err)
	}
}

// wantCode 断言校验失败且错误码匹配。
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %q，实际通过", code)
	}
	if got := ruleengine.CodeOf(err); got != code {
		t.Fatalf("期望错误码 %q，得到 %q（%v）", code, got, err)
	}
}

func leaf(condType string, params map[string]any, cmp *ruleengine.Compare) ruleengine.Node {
	return ruleengine.Node{Type: condType, Params: params, Cmp: cmp}
}

func group(op ruleengine.GroupOp, children ...ruleengine.Node) ruleengine.Node {
	return ruleengine.Node{Op: op, Children: children}
}

func gte(value int64) *ruleengine.Compare {
	return &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: value}
}

// stubResolver 按 type（计数条件再加 metric）查表返回固定值，并记录调用顺序，
// 用于验证短路：不该被求值的叶子不能出现在 calls 里。
type stubResolver struct {
	values map[string]ruleengine.Value
	errs   map[string]error
	calls  []string
}

func (s *stubResolver) Resolve(_ context.Context, l ruleengine.Leaf) (ruleengine.Value, error) {
	key := l.Type
	if metric, ok := l.Params["metric"].(string); ok {
		key = l.Type + "/" + metric
	}
	s.calls = append(s.calls, key)
	if err, ok := s.errs[key]; ok {
		return ruleengine.Value{}, err
	}
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return ruleengine.Value{}, fmt.Errorf("no stub value for %q", key)
}

func count(n int64) ruleengine.Value {
	return ruleengine.Value{Kind: ruleengine.ValueCount, Count: n}
}

func boolean(v bool) ruleengine.Value {
	return ruleengine.Value{Kind: ruleengine.ValueBoolean, Bool: v}
}

var errStub = errors.New("stub resolve failure")
