package badges

import (
	"context"
	"sort"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// MeasureProgress 批量测量一组规则徽章的当前进度（实现 `ProgressMeasurer`）。
//
// 形状与 `EvaluateRule` 同源：建 resolver → **一次** Prepare → 逐徽章 Resolve
// 读 memo。待测徽章的叶子先合成一个临时 `all` 组，所以 Prepare 按条件类型分组，
// 每种类型只发一条 SQL（同类型的十几枚徽章共享一次往返）。
//
// 失败语义是 fail-soft，**绝不上抛**：
//   - 单类型查询失败由 `Prepare` 记在 groupErrs 里，Resolve 把该错误还给这里，
//     只有该类条件的徽章没有进度；
//   - resolver 建不起来（pool 为 nil / userID 非法 / ctx 已取消）时返回空 map。
//
// 进度是装饰：一次数据库抖动不该让整个资料页或目录页变成 500。
func (s *PostgresStore) MeasureProgress(ctx context.Context, userID int64, measures map[string]ProgressMeasure) map[string]BadgeProgress {
	result := make(map[string]BadgeProgress, len(measures))
	if s == nil || s.pool == nil || userID <= 0 || len(measures) == 0 {
		return result
	}
	// 稳定顺序：同一批徽章每次都以相同顺序拼树，SQL 参数与日志可复现。
	slugs := make([]string, 0, len(measures))
	for slug := range measures {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	root := ruleengine.Node{Op: ruleengine.OpAll, Children: make([]ruleengine.Node, 0, len(slugs))}
	for _, slug := range slugs {
		measure := measures[slug]
		root.Children = append(root.Children, ruleengine.Node{
			Type:   measure.Leaf.Type,
			Params: measure.Leaf.Params,
		})
	}

	var factKeys []string
	if s.factKeys != nil {
		factKeys = s.factKeys()
	}
	resolver, err := conditions.NewResolver(s.pool, userID, factKeys...)
	if err != nil {
		return result
	}
	if err := resolver.Prepare(ctx, root); err != nil {
		// 整体性失败（ctx 取消等）：不逐条重试——那会把一次失败放大成 N 次往返。
		return result
	}
	for _, slug := range slugs {
		value, err := resolver.Resolve(ctx, measures[slug].Leaf)
		if err != nil || value.Kind != ruleengine.ValueCount {
			continue
		}
		result[slug] = BadgeProgress{Current: value.Count, Threshold: measures[slug].Threshold}
	}
	return result
}
