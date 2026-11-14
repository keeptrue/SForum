package trustlevels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// RuleImpact 是「这档会覆盖多少人」的**估算**。
//
// 语义就是「≈」：AND 取最严叶子、OR 取和，两者对「同时满足」的真实人数都只是上界
// （叶子之间通常相关，人数不会相加）。Method 存在的唯一目的，是让前端能如实说
// 「≈ 120 人（按最严条件估算）」——写成精确数字，运营会拿它跟实际授予数对账。
//
// 形状与徽章 `badges.RuleImpact` 逐字段对齐（同样的 JSON tag）：前端已经有一套渲染
// 「有值 / 无法估算 / 部分」的文案，本包不发明第二套。
type RuleImpact struct {
	// Estimated 是估算人数；nil 表示**无法估算**（不是 0 人）。
	//
	// 用 0 表达「算不出来」会让运营读成「这规则没人能达标」，然后去改一条本来正确的
	// 规则。这与 factKey 上那条原则同源：「查不到数据」与「无法判定」必须不同值。
	Estimated *int64 `json:"estimated"`
	Method    string `json:"method"`
	// Partial 表示估算只用了部分条件（AND 里有算不出来的叶子）。
	Partial bool `json:"partial"`
	// UnsupportedConditions 列出参与规则但无法估算的条件 key。
	UnsupportedConditions []string `json:"unsupportedConditions"`
}

// 估算方法标识。前端按它选文案，所以是契约（与徽章同名同义）。
const (
	ImpactMethodNoRule         = "no_rule"
	ImpactMethodUnconditional  = "unconditional"
	ImpactMethodEstimateFailed = "estimate_failed"
)

// RuleImpactEstimator 估算一棵规则树覆盖的人数。
//
// 由 bootstrap 注入真实实现（`*badges.PostgresStore` 的 `EstimateRuleImpact`）：
// 聚合 SQL 留在它已经在的那一侧，本包不写第二份（D17 —— `Models/TrustLevels`
// 不许 import `Models/Badges`，但可以声明自己要什么）。
type RuleImpactEstimator interface {
	EstimateRuleImpact(ctx context.Context, rule ruleengine.Node) (RuleImpact, error)
}

// EstimateTierImpact 估算「满足这一档」的人数上界。
//
// 口径：数值快筛（Requirements）与规则树是 **AND**，所以两者都折成叶子塞进同一棵
// `all` 根一起估：
//   - 只给规则估（不含 Requirements）会把默认值清零的档位估成全员，运营点开弹窗看到
//     「≈ 5000 人」，而真实情况是「数值门槛先砍掉 99%」；
//   - 六个指标都是 `activity.count` 的累计 `gte`，正好是条件目录里已有的叶子，
//     不新造估算 SQL（`EstimateRuleImpact` 本来就支持这一族）。
//
// 没有任何条件（数值全 0 且无规则）时返回 `no_rule`/`unconditional` 而不是编一个数：
// 这种档位的答案是「全员」，而「全员」不该伪装成一次估算（它不查库、也不随数据变）。
func EstimateTierImpact(
	ctx context.Context,
	estimator RuleImpactEstimator,
	requirements Requirements,
	rawRule json.RawMessage,
) (RuleImpact, error) {
	if estimator == nil {
		return RuleImpact{}, fmt.Errorf("trustlevels: 影响估算未装配（RuleImpactEstimator 未注入）")
	}
	children := requirementLeaves(requirements)
	trimmed := bytes.TrimSpace(rawRule)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		root, err := ruleengine.Decode(rawRule)
		if err != nil {
			// 草稿规则解不开：不估算，也不报 500——估算接口的存在是为了编辑期
			// 试算，半成品规则是常态。前端按 estimated:null 显示「无法估算」。
			return RuleImpact{Method: ImpactMethodEstimateFailed, UnsupportedConditions: []string{}}, nil
		}
		children = append(children, root)
	}
	switch len(children) {
	case 0:
		return RuleImpact{Method: ImpactMethodUnconditional, UnsupportedConditions: []string{}}, nil
	case 1:
		return estimator.EstimateRuleImpact(ctx, children[0])
	default:
		// 合成 `all` 根：**只喂给估算器**，不做 `Validate`（那是保存期按档位做的，
		// 且 D19-b 已裁决：合成根不得参与上限判定）。
		return estimator.EstimateRuleImpact(ctx, ruleengine.Node{Op: ruleengine.OpAll, Children: children})
	}
}

// requirementLeaves 把六个阈值折成 `activity.count`（累计）叶子。
//
// 只折**非零**指标：零值在 Requirements 里的语义是「该指标不设门槛」，折成
// `gte 0` 虽然恒真，但会凭空多一个「参与估算的条件」，让 unsupportedConditions 与
// partial 的判断都变脏。
func requirementLeaves(requirements Requirements) []ruleengine.Node {
	leaves := make([]ruleengine.Node, 0, len(Metrics))
	for _, metric := range Metrics {
		value, ok := requirements.Value(metric)
		if !ok || value <= 0 {
			continue
		}
		leaves = append(leaves, ruleengine.Node{
			Type:   conditions.KeyActivityCount,
			Params: map[string]any{"metric": metric},
			Cmp:    &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: value},
		})
	}
	return leaves
}
