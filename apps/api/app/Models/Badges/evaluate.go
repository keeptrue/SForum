package badges

import (
	"context"
	"fmt"
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
		if !ruleSatisfied(badge, counters) {
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

// ruleSatisfied 判断一条自动规则是否达标。
//
// 阈值与指标缺失的规则视为不达标而不是报错：这类脏数据只可能来自手工改库，
// 让整批评估崩掉会掩盖真正的问题。
func ruleSatisfied(badge Badge, counters ActivityCounters) bool {
	if badge.GrantMode != GrantAutomatic || !badge.Enabled {
		return false
	}
	if badge.Metric == nil || badge.Threshold == nil || *badge.Threshold <= 0 {
		return false
	}
	if !badge.Metric.Valid() {
		return false
	}
	return counters.Value(*badge.Metric) >= *badge.Threshold
}
