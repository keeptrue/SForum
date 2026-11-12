package ruleengine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
)

// CurrentVersion 是规则文档的版本号。目前只接受 1。
//
// 版本字段存在的意义是让「规则形状变更」有一个可判定的分界点：解码器遇到不认识
// 的版本直接拒绝，而不是尽力猜。升级形状时新增版本 + 迁移，不做隐式兼容。
const CurrentVersion = 1

// Decode 把规则 JSON 解码成 AST。
//
// 顶层形状固定为 `{"v":1,"root":{...}}`。**未知字段一律报错**：规则的写入方是后台
// 表单与将来的导入工具，静默忽略拼错的键（`childern`、`cmp.op` 写成 `operator`）
// 会得到一棵「保存成功、永远不命中」的树，比拒绝难查一个数量级。
func Decode(data []byte) (Node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var doc wireDocument
	if err := dec.Decode(&doc); err != nil {
		return Node{}, &Error{Code: CodeDecodeFailed, Path: "root", Detail: err.Error()}
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Node{}, &Error{Code: CodeDecodeFailed, Path: "root", Detail: "规则 JSON 之后还有多余内容"}
	}
	if doc.V == nil {
		return Node{}, &Error{Code: CodeVersionUnsupported, Path: "v", Detail: "缺少版本字段 v"}
	}
	if *doc.V != CurrentVersion {
		return Node{}, &Error{Code: CodeVersionUnsupported, Path: "v", Detail: fmt.Sprintf("只支持 v=%d，收到 %d", CurrentVersion, *doc.V)}
	}
	if doc.Root == nil {
		return Node{}, &Error{Code: CodeShapeInvalid, Path: "root", Detail: "缺少 root 节点"}
	}
	return doc.Root.toNode("root")
}

// wireDocument 是顶层文档形状。
type wireDocument struct {
	V    *int      `json:"v"`
	Root *wireNode `json:"root"`
}

// wireNode 是节点在 JSON 里的形状。
//
// 组节点与叶子共用这一个结构，判别式是**互斥字段**（op / type），不加 kind：
// 多一个判别字段就多一种「kind 说 group、字段却是叶子」的非法组合要处理。
// `v` 只在顶层出现，嵌套节点带 `v` 会被拒绝——否则同一棵树里出现两个版本声明，
// 谁说了算就没有答案了。
type wireNode struct {
	V        *int           `json:"v"`
	Op       string         `json:"op"`
	Children []wireNode     `json:"children"`
	Type     string         `json:"type"`
	Params   map[string]any `json:"params"`
	Cmp      *wireCompare   `json:"cmp"`
}

type wireCompare struct {
	Op    string   `json:"op"`
	Value *float64 `json:"value"`
}

func (w wireNode) toNode(path string) (Node, error) {
	if w.V != nil {
		return Node{}, &Error{Code: CodeShapeInvalid, Path: path + ".v", Detail: "v 只能出现在顶层文档"}
	}
	node := Node{
		Op:     GroupOp(w.Op),
		Type:   w.Type,
		Params: w.Params,
	}
	if len(w.Children) > 0 {
		node.Children = make([]Node, 0, len(w.Children))
		for i, child := range w.Children {
			converted, err := child.toNode(fmt.Sprintf("%s.children[%d]", path, i))
			if err != nil {
				return Node{}, err
			}
			node.Children = append(node.Children, converted)
		}
	}
	if w.Cmp != nil {
		cmp, err := w.Cmp.toCompare(path + ".cmp")
		if err != nil {
			return Node{}, err
		}
		node.Cmp = cmp
	}
	return node, nil
}

func (w wireCompare) toCompare(path string) (*Compare, error) {
	if w.Op == "" {
		return nil, &Error{Code: CodeCompareMissing, Path: path + ".op", Detail: "cmp 缺少 op"}
	}
	if w.Value == nil {
		return nil, &Error{Code: CodeCompareMissing, Path: path + ".value", Detail: "cmp 缺少 value"}
	}
	// 值域由 Validate 判定；这里只拦「不是整数」与「超出 int64」，避免静默截断。
	if math.Trunc(*w.Value) != *w.Value {
		return nil, &Error{Code: CodeCompareInvalid, Path: path + ".value", Detail: fmt.Sprintf("value 必须是整数，收到 %v", *w.Value)}
	}
	if *w.Value > math.MaxInt64 || *w.Value < math.MinInt64 {
		return nil, &Error{Code: CodeCompareInvalid, Path: path + ".value", Detail: fmt.Sprintf("value 超出 int64 范围：%v", *w.Value)}
	}
	return &Compare{Op: CompareOp(w.Op), Value: int64(*w.Value)}, nil
}
