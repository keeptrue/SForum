package bootstrap

import (
	"context"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
)

// profileActivityCounters 把 badges 的活动统计读入口适配成 Profile 需要的最小接口。
//
// 存在的理由与 trustLevelActivityReader 相同：`user_activity` 的读 SQL 全仓只有一份
// （badges.PostgresActivityStore.GetCounters），Profile 不许写第二份。适配器留在装配层，
// Profile 与 Badges 之间没有直接 import。
//
// 只暴露 like_received_count 与 days_visited：like_given_count / bookmark_count 是
// 刻意不公开的窥探面（D1），任何对**他人**的响应都不得包含它们。
type profileActivityCounters struct {
	store *badges.PostgresActivityStore
}

func (r profileActivityCounters) PublicActivityCountersFor(ctx context.Context, userID int64) (profile.PublicActivityCounters, error) {
	if r.store == nil {
		return profile.PublicActivityCounters{}, nil
	}
	counters, err := r.store.GetCounters(ctx, userID)
	if err != nil {
		return profile.PublicActivityCounters{}, err
	}
	// 显式按名字取字段：统计层改字段名时这里编译失败，而不是静默读错列
	// （like_received 与 like_given 只差一个词，读错了数字看着仍然像真的）。
	return profile.PublicActivityCounters{
		LikeReceivedCount: counters.LikeReceivedCount,
		DaysVisited:       counters.DaysVisited,
	}, nil
}

var _ profile.ActivityCountersReader = profileActivityCounters{}
