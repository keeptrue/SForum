package badges

import (
	"context"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// Evaluator 按规则评估单个用户，授予新达标的自动徽章。
//
// 幂等靠两层：GrantAutomatic 的 ON CONFLICT DO NOTHING，以及「只对未持有徽章
// 尝试授予」。同一用户短时间内被评估多次，结果一致且不重复写通知。
type Evaluator struct {
	store    Store
	activity ActivityReader
}

func NewEvaluator(store Store, activity ActivityReader) *Evaluator {
	return &Evaluator{store: store, activity: activity}
}

// EvaluateUser 读一次活动统计，评估全部启用规则，返回本次新授予的徽章。
//
// 只评估 enabled 且 grant_mode = 'automatic' 的徽章：停用后不再评估，
// 已授予行保留（历史不抹），top_badge_id 由停用时的重算处理。
func (e *Evaluator) EvaluateUser(ctx context.Context, userID int64) ([]Badge, error) {
	if e == nil || e.store == nil || userID <= 0 {
		return nil, nil
	}
	var counters ActivityCounters
	if e.activity != nil {
		loaded, err := e.activity.GetCounters(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("load activity counters: %w", err)
		}
		counters = loaded
	}
	return e.EvaluateWithCounters(ctx, userID, counters)
}

// EvaluateWithCounters 用给定快照评估（测试与批量重算复用，避免重复读库）。
func (e *Evaluator) EvaluateWithCounters(ctx context.Context, userID int64, counters ActivityCounters) ([]Badge, error) {
	if e == nil || e.store == nil || userID <= 0 {
		return nil, nil
	}
	rules, err := e.store.ListEnabledAutomaticBadges(ctx)
	if err != nil {
		return nil, err
	}
	granted := make([]Badge, 0, 2)
	for _, badge := range rules {
		satisfied, err := e.satisfied(ctx, userID, badge, counters)
		if err != nil {
			return nil, err
		}
		if !satisfied {
			continue
		}
		inserted, err := e.store.GrantAutomatic(ctx, userID, badge.ID)
		if err != nil {
			return nil, err
		}
		if !inserted {
			// 已持有：幂等跳过，不重写通知、不动 top_badge_id。
			continue
		}
		granted = append(granted, badge)
	}
	if len(granted) == 0 {
		return nil, nil
	}
	// 只有真的新增了授予才重算：避免每次评估都 touch users 行。
	if err := e.store.RecomputeTopBadge(ctx, userID); err != nil {
		return nil, err
	}
	return granted, nil
}

// RuleResolver 由支持规则求值的 store 实现（`PostgresStore`）。
//
// 单独一个接口而不是往 `Store` 上加方法：`Store` 有大量测试用 fake，加方法会逼所有
// fake 实现一个它们不需要的能力；而「能不能跑规则引擎」正是 PostgresStore 独有的。
type RuleResolver interface {
	EvaluateRule(ctx context.Context, userID int64, rule ruleengine.Node) (ruleengine.Result, error)
}

// satisfied 判断一条自动徽章是否达标。
//
// **P-04：`grant_mode='automatic'` 只认 `rule`，绝不读 `metric`/`threshold`。**
// 纯规则徽章的 `metric` 是 NULL；若写成「先看 metric，没有再看 rule」，结果是
// 「保存成功、用户达标、永远不发、零报错」——本刀最贵的失败模式。旧两列只服务于
// down 迁移与后台只读展示。
//
// 两条路径：
//   - **快路径**：规则就是单个 `activity.count` + `window.all` 叶子时，直接用已读到的
//     计数器比较，SQL 与改造前完全一致（12 枚内置徽章零回归、零额外查询）；
//   - **引擎路径**：其余形状交给 `ruleengine.Eval`（Kleene 三值，fail-closed）。
//
// 规则缺失或解码失败（`Rule == nil`）一律视为不满足而不是报错：一条脏规则只可能来自
// 手工改库，让整批评估崩掉会掩盖真正的问题；`rule_health` 会把它列出来。
func (e *Evaluator) satisfied(ctx context.Context, userID int64, badge Badge, counters ActivityCounters) (bool, error) {
	if badge.GrantMode != GrantAutomatic || !badge.Enabled {
		return false, nil
	}
	if badge.Rule == nil {
		return false, nil
	}
	if metric, threshold, ok := fastPathRule(*badge.Rule); ok {
		return counters.Value(metric) >= threshold, nil
	}
	resolver, ok := e.store.(RuleResolver)
	if !ok {
		// 测试用 fake store 没有引擎能力；宁可判不满足，也不要假装成立。
		return false, nil
	}
	result, err := resolver.EvaluateRule(ctx, userID, *badge.Rule)
	if err != nil {
		return false, err
	}
	// Incomplete = 结论依赖了求值失败的条件，Matched 已按 fail-closed 落为 false。
	// 这里不再把 Errors 升级成 error：徽章评估是批量的，一个条件坏掉不该让整批失败。
	return result.Matched, nil
}

// fastPathRule 识别「单 activity.count + window.all + gte」这一种规则形状。
//
// 只认 `gte`：`lte`/`eq` 等其它比较在快路径上等价于「计数器 vs 阈值」的另一种方向，
// 但内置徽章全部是 gte，把 lte 也塞进快路径会让「快路径 = 内置徽章形状」这条
// 断言变得含糊，得不偿失。
func fastPathRule(node ruleengine.Node) (ActivityMetric, int64, bool) {
	if !node.IsLeaf() || node.Type != conditions.KeyActivityCount || node.Cmp == nil {
		return "", 0, false
	}
	if node.Cmp.Op != ruleengine.CompareGTE || node.Cmp.Value <= 0 {
		return "", 0, false
	}
	metric, _ := node.Params["metric"].(string)
	value := ActivityMetric(metric)
	if !value.Valid() {
		return "", 0, false
	}
	window, ok := node.Params["window"].(map[string]any)
	if !ok || window["kind"] != ruleengine.WindowKindAll {
		return "", 0, false
	}
	if _, hasDays := window["days"]; hasDays {
		return "", 0, false
	}
	return value, node.Cmp.Value, true
}
