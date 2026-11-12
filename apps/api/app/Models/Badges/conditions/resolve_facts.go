package conditions

import (
	"context"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// **两种失效语义在这里分叉，不要「统一」掉**：
//   - 引用对象不存在（主题/分类/标签/角色/provider/徽章 slug 没了）→ 返回**确定值**
//     0/false。那是「没做过」，不是「判不了」；返 error 会让整棵树变 unknown，
//     徽章永远不发——另一类静默失败。
//   - 条件本身失效（factKey 没有被任何已启用扩展声明）→ **必须返 error**，由引擎
//     转 unknown。因为 0 会被 `lte` 判成满足、被 `not` 判成 true，两条都是 fail-open。
//
// 同一包里两种相反的返回策略，是刻意的；改之前先想清楚哪一种是「确定不满足」。
//
// factCountSQL 读插件事实：累计走 `user_fact_counters`，窗口走 `badge_facts` 的
// delta 求和。两者都 `GREATEST(..., 0)`：`total` 允许为负（事实可以是扣减），
// 但徽章条件看的是「净增量」的非负形态——负数漏到规则层会让 `lte` 条件莫名成立。
//
// variant 缺省为空串（M1 没有扩展声明 variant，`ruleengine` 的 enum 选项为空，
// 所以「指定 variant」在 M1 会被校验拒绝；省略即取默认变体）。
const factCountSQL = `
SELECT a.fact_key, a.variant, a.days,
       CASE WHEN a.days IS NULL THEN (
              SELECT GREATEST(counters.total, 0)
                FROM user_fact_counters AS counters
               WHERE counters.user_id = $1
                 AND counters.fact_key = a.fact_key
                 AND counters.variant = a.variant)
            ELSE (
              SELECT GREATEST(COALESCE(sum(facts.delta), 0), 0)
                FROM badge_facts AS facts
               WHERE facts.user_id = $1
                 AND facts.fact_key = a.fact_key
                 AND facts.variant = a.variant
                 AND facts.occurred_at >= now() - make_interval(days => a.days::int))
       END
FROM unnest($2::text[], $3::text[], $4::bigint[]) AS a(fact_key, variant, days)
`

func (r *Resolver) loadFacts(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	keys := make([]string, 0, len(leaves))
	variants := make([]string, 0, len(leaves))
	days := make([]*int64, 0, len(leaves))
	for _, leaf := range leaves {
		window, err := windowDays(leaf)
		if err != nil {
			return nil, err
		}
		factKey := stringParam(leaf, "factKey")
		if _, declared := r.factKeys[factKey]; !declared {
			// 「无法判定」而不是「没做过」：M3 卸载插件后，一条 `lte` 规则会把 0 判成
			// 满足、`not` 会把 false 判成 true，两者都是 fail-open。返 error → unknown。
			return nil, fmt.Errorf("conditions: 事实 %q 未被任何已启用扩展声明，无法判定", factKey)
		}
		keys = append(keys, factKey)
		variants = append(variants, stringParam(leaf, "variant"))
		days = append(days, window)
	}

	rows, err := r.pool.Query(ctx, factCountSQL, r.userID, keys, variants, days)
	if err != nil {
		return nil, fmt.Errorf("conditions: 查询 fact.count 失败：%w", err)
	}
	defer rows.Close()

	type factKey struct {
		fact    string
		variant string
		days    int64
	}
	counts := make(map[factKey]int64, len(leaves))
	for rows.Next() {
		var fact, variant string
		var window *int64
		var total *int64
		if err := rows.Scan(&fact, &variant, &window, &total); err != nil {
			return nil, fmt.Errorf("conditions: 读取 fact.count 结果失败：%w", err)
		}
		key := factKey{fact: fact, variant: variant, days: -1}
		if window != nil {
			key.days = *window
		}
		if total != nil {
			counts[key] = *total
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conditions: 遍历 fact.count 结果失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for index, leaf := range leaves {
		key := factKey{fact: keys[index], variant: variants[index], days: -1}
		if days[index] != nil {
			key.days = *days[index]
		}
		// 没有事实行时 Postgres 返回 NULL，扫描后不在 map 里——按 0 处理是**正确**的
		// （「没有任何事实」就是 0），这与「查询失败」不同：后者是 error，走 unknown。
		count := counts[key]
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = countValue(count)
	}
	return out, nil
}
