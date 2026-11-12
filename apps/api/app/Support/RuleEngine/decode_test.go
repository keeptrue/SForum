package ruleengine_test

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

const planExample = `{
  "v": 1,
  "root": {
    "op": "all",
    "children": [
      {"op": "any", "children": [
        {"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},
         "cmp":{"op":"gte","value":10}},
        {"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"recent_days","days":30}},
         "cmp":{"op":"gte","value":50}}
      ]},
      {"op": "not", "children": [
        {"type":"content.hidden","params":{},"cmp":{"op":"gte","value":1}}
      ]},
      {"type":"identity.email_verified","params":{}}
    ]
  }
}`

func TestDecodePlanExample(t *testing.T) {
	node := mustDecode(t, planExample)
	if node.Op != ruleengine.OpAll || len(node.Children) != 3 {
		t.Fatalf("根节点形状不符: op=%q children=%d", node.Op, len(node.Children))
	}
	anyNode := node.Children[0]
	if anyNode.Op != ruleengine.OpAny || len(anyNode.Children) != 2 {
		t.Fatalf("第一棵子树应为 2 个叶子的 any: %+v", anyNode)
	}
	second := anyNode.Children[1]
	if second.Type != "activity.count" || second.Cmp == nil || second.Cmp.Value != 50 || second.Cmp.Op != ruleengine.CompareGTE {
		t.Fatalf("cmp 解码错误: %+v", second.Cmp)
	}
	window, ok := second.Params["window"].(map[string]any)
	if !ok {
		t.Fatalf("window 应为嵌套对象: %#v", second.Params["window"])
	}
	if window["kind"] != "recent_days" || window["days"] != float64(30) {
		t.Fatalf("window 解码错误: %#v", window)
	}
	if node.Children[2].Cmp != nil {
		t.Fatalf("布尔叶子不应带 cmp: %+v", node.Children[2].Cmp)
	}
	mustValidate(t, node)
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	cases := map[string]string{
		"叶子拼错字段":   `{"v":1,"root":{"type":"identity.email_verified","childern":[]}}`,
		"顶层多余字段":   `{"v":1,"root":{"type":"identity.email_verified"},"extra":1}`,
		"cmp 多余字段": `{"v":1,"root":{"type":"content.hidden","cmp":{"op":"gte","value":1,"weight":2}}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ruleengine.Decode([]byte(raw))
			wantCode(t, err, ruleengine.CodeDecodeFailed)
		})
	}
}

func TestDecodeVersion(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		code string
	}{
		{"缺版本", `{"root":{"type":"identity.email_verified"}}`, ruleengine.CodeVersionUnsupported},
		{"版本 2", `{"v":2,"root":{"type":"identity.email_verified"}}`, ruleengine.CodeVersionUnsupported},
		{"版本 0", `{"v":0,"root":{"type":"identity.email_verified"}}`, ruleengine.CodeVersionUnsupported},
		{"嵌套版本", `{"v":1,"root":{"op":"not","children":[{"v":1,"type":"identity.email_verified"}]}}`, ruleengine.CodeShapeInvalid},
		{"缺 root", `{"v":1}`, ruleengine.CodeShapeInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ruleengine.Decode([]byte(tc.raw))
			wantCode(t, err, tc.code)
		})
	}
}

func TestDecodeRejectsTrailingContent(t *testing.T) {
	_, err := ruleengine.Decode([]byte(`{"v":1,"root":{"type":"identity.email_verified"}} {"v":1}`))
	wantCode(t, err, ruleengine.CodeDecodeFailed)
}

func TestDecodeRejectsBadCompareValue(t *testing.T) {
	cases := map[string]string{
		"小数":      `{"v":1,"root":{"type":"content.hidden","cmp":{"op":"gte","value":1.5}}}`,
		"缺 value": `{"v":1,"root":{"type":"content.hidden","cmp":{"op":"gte"}}}`,
		"缺 op":    `{"v":1,"root":{"type":"content.hidden","cmp":{"value":3}}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ruleengine.Decode([]byte(raw))
			code := ruleengine.CodeOf(err)
			if code != ruleengine.CodeCompareInvalid && code != ruleengine.CodeCompareMissing {
				t.Fatalf("期望 compare 相关错误码，得到 %q（%v）", code, err)
			}
		})
	}
}

func TestDecodeEmptyParamsStaysNil(t *testing.T) {
	node := mustDecode(t, `{"v":1,"root":{"type":"identity.email_verified","params":{}}}`)
	if node.Params == nil || len(node.Params) != 0 {
		t.Fatalf("空 params 应解成空 map: %#v", node.Params)
	}
	mustValidate(t, node)
}
