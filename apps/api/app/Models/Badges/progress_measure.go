package badges

import (
	"context"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// ProgressMeasure 是一枚规则徽章的可测进度：规则根部的那个「计数达到 N」的叶子。
//
// 它刻意不是公开结构的一部分（`PublicBadge.Measure` 是 json:"-"）：阈值只用来画
// 进度条，不参与任何下发决策。放在领域结构里是为了让「能不能测」在投影时就定下来，
// HTTP 层不必再理解一遍规则 AST。
type ProgressMeasure struct {
	// Leaf 是去掉比较子句后的计数条件，交给 `conditions.Resolver.Resolve`。
	Leaf ruleengine.Leaf
	// Threshold 是 `gte` 的期望值（恒 > 0，由 `measureForRule` 保证）。
	Threshold int64
}

// measureForRule 从规则树派生可测进度。
//
// 只接受**根是单叶** + 该条件在条件目录里是 count 型 + `cmp.op == "gte"` + value > 0。
// 其余一律返回 nil（= 无进度，只留条件句）：
//   - `lte` / `lt` / `eq`：进度条的语义是「还差多少」，只有「达到某条线」可表达；
//   - 布尔条件：没有可比的数值；
//   - 多条件组（all / any / not）：一个进度条无法表达合取或析取，
//     求和或取最大都会给出错误的「还差多少」；
//   - 规则为 nil / 解不开：没有可测的东西。
//
// 这里**不拼自然语言**：公开文案的唯一实现是 `requirement_text_render.go`，
// 两套文案必然漂移（这也是不要复用它的原因——它输出的是句子，不是测量输入）。
func measureForRule(root *ruleengine.Node) *ProgressMeasure {
	if root == nil || root.IsGroup() || !root.IsLeaf() || root.Cmp == nil {
		return nil
	}
	if root.Cmp.Op != ruleengine.CompareGTE || root.Cmp.Value <= 0 {
		return nil
	}
	definition, ok := conditions.Core().Definition(root.Type)
	if !ok || definition.ValueKind != ruleengine.ValueCount {
		return nil
	}
	return &ProgressMeasure{
		Leaf:      ruleengine.Leaf{Type: root.Type, Params: root.Params},
		Threshold: root.Cmp.Value,
	}
}

// ProgressMeasurer 批量测量一组规则徽章的当前进度（`*PostgresStore` 满足它）。
//
// 签名里没有 error 是刻意的：进度是装饰，测量失败只该让对应徽章没有进度条，
// 不该让整个资料页/目录页 500。未装配（nil）时 Service 同样静默留空。
//
// `measures` 的 key 是徽章 slug；实现方负责把叶子合成一次批量查询，
// **禁止**按徽章逐条查（那就是 N+1，目录页一次要测十几枚）。
type ProgressMeasurer interface {
	MeasureProgress(ctx context.Context, userID int64, measures map[string]ProgressMeasure) map[string]BadgeProgress
}
