// Package badges 承载徽章与用户活动统计域：计数反规范化、规则评估、授予与通知。
//
// 依赖方向是单向的：Badges 可以依赖 Identity / Jobs，但 Forum 绝不 import 本包。
// Forum 的写事务通过自己包内的薄封装（activity_hooks.go）累加计数，避免反向依赖成环。
package badges

import "time"

// ActivityMetric 是 user_activity 上的可累加计数列。
//
// 白名单与迁移 202611100002 里的 badges_metric_check 逐字一致：新增指标必须先加列、
// 再加约束白名单，最后才允许出现在这里。列名永远来自本白名单，绝不拼接外部输入。
type ActivityMetric string

const (
	MetricTopicCount        ActivityMetric = "topic_count"
	MetricCommentCount      ActivityMetric = "comment_count"
	MetricLikeReceivedCount ActivityMetric = "like_received_count"
	MetricLikeGivenCount    ActivityMetric = "like_given_count"
	MetricBookmarkCount     ActivityMetric = "bookmark_count"
	MetricDaysVisited       ActivityMetric = "days_visited"
)

// activityMetricColumns 把枚举映射到真实列名，是 SQL 拼接的唯一入口。
var activityMetricColumns = map[ActivityMetric]string{
	MetricTopicCount:        "topic_count",
	MetricCommentCount:      "comment_count",
	MetricLikeReceivedCount: "like_received_count",
	MetricLikeGivenCount:    "like_given_count",
	MetricBookmarkCount:     "bookmark_count",
	MetricDaysVisited:       "days_visited",
}

// ActivityMetrics 返回全部合法指标，顺序固定（供后台重算与校验遍历）。
func ActivityMetrics() []ActivityMetric {
	return []ActivityMetric{
		MetricTopicCount,
		MetricCommentCount,
		MetricLikeReceivedCount,
		MetricLikeGivenCount,
		MetricBookmarkCount,
		MetricDaysVisited,
	}
}

// Valid 报告该指标是否属于白名单。
func (m ActivityMetric) Valid() bool {
	_, ok := activityMetricColumns[m]
	return ok
}

// Column 返回白名单列名；非白名单指标返回 false，调用方必须据此拒绝执行。
func (m ActivityMetric) Column() (string, bool) {
	column, ok := activityMetricColumns[m]
	return column, ok
}

// ActivityCounters 是 user_activity 的一行快照。
//
// 阅读类指标（posts_read_count / read_time_seconds）刻意不出现在这里：
// v1 不采集，读出来只会是 0，暴露成结构体字段会误导调用方。
type ActivityCounters struct {
	UserID            int64
	TopicCount        int64
	CommentCount      int64
	LikeReceivedCount int64
	LikeGivenCount    int64
	BookmarkCount     int64
	DaysVisited       int64
	LastVisitedOn     *time.Time
	UpdatedAt         time.Time
}

// Value 按指标取计数值；非白名单指标返回 0。
func (c ActivityCounters) Value(metric ActivityMetric) int64 {
	switch metric {
	case MetricTopicCount:
		return c.TopicCount
	case MetricCommentCount:
		return c.CommentCount
	case MetricLikeReceivedCount:
		return c.LikeReceivedCount
	case MetricLikeGivenCount:
		return c.LikeGivenCount
	case MetricBookmarkCount:
		return c.BookmarkCount
	case MetricDaysVisited:
		return c.DaysVisited
	default:
		return 0
	}
}

// bumpActivitySQL 是唯一的累加语句模板，%[1]s 只能是 activityMetricColumns 里的列名。
//
// 行懒创建：首次累加即建行，不需要回填。
// GREATEST(..., 0) 保证负 delta（取消点赞/收藏）把计数钳在 0，不回绕成负数——
// 计数与真值表短暂不一致是可接受的（见计划 D4），负数不可接受。
const bumpActivitySQL = `INSERT INTO user_activity (user_id, %[1]s)
VALUES ($1, GREATEST($2, 0))
ON CONFLICT (user_id) DO UPDATE
SET %[1]s = GREATEST(user_activity.%[1]s + $2, 0),
    updated_at = now()`

// recordVisitSQL 写一行日访问记录；复合主键让重复写入天然幂等。
const recordVisitSQL = `INSERT INTO user_visits (user_id, visited_on)
VALUES ($1, $2)
ON CONFLICT (user_id, visited_on) DO NOTHING`

// bumpDaysVisitedSQL 在日访问记录真的插入成功后 +1，并推进 last_visited_on。
// GREATEST 在 PostgreSQL 里忽略 NULL，所以首次访问（last_visited_on 为 NULL）
// 也会正确落成当天。
const bumpDaysVisitedSQL = `INSERT INTO user_activity (user_id, days_visited, last_visited_on)
VALUES ($1, 1, $2)
ON CONFLICT (user_id) DO UPDATE
SET days_visited = user_activity.days_visited + 1,
    last_visited_on = GREATEST(user_activity.last_visited_on, EXCLUDED.last_visited_on),
    updated_at = now()`
