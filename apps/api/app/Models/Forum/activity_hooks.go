package forum

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// 用户活动统计的写路径挂载点。
//
// 为什么 SQL 写在这里而不是 import app/Models/Badges：
// Badges 依赖 Forum（装饰主题/评论摘要的徽章），Forum 反向 import 会成环。
// 因此 Forum 只承认一个「计数列名 + 增量」的窄契约，列名白名单与
// badges.ActivityMetric 逐字一致，两边由迁移里的 badges_metric_check 与
// database/migrations 的结构测试共同钉死。
//
// 写入位置约束：必须落在调用方已有的写事务里，不开新事务、不同步转异步。
// 统计与业务写同生共死——业务回滚时计数一起回滚，不会留下孤儿累加。
const bumpUserActivitySQL = `INSERT INTO user_activity (user_id, %[1]s)
VALUES ($1, GREATEST($2, 0))
ON CONFLICT (user_id) DO UPDATE
SET %[1]s = GREATEST(user_activity.%[1]s + $2, 0),
    updated_at = now()`

// 活动指标名（与 user_activity 列名、迁移白名单三处逐字一致）。
const (
	activityMetricTopicCount        = "topic_count"
	activityMetricCommentCount      = "comment_count"
	activityMetricLikeReceivedCount = "like_received_count"
	activityMetricLikeGivenCount    = "like_given_count"
	activityMetricBookmarkCount     = "bookmark_count"
)

// activityMetricColumns 是 SQL 拼接的唯一入口：任何外部输入都不能进 %[1]s。
var activityMetricColumns = map[string]string{
	activityMetricTopicCount:        "topic_count",
	activityMetricCommentCount:      "comment_count",
	activityMetricLikeReceivedCount: "like_received_count",
	activityMetricLikeGivenCount:    "like_given_count",
	activityMetricBookmarkCount:     "bookmark_count",
}

// activityExecer 是 bumpUserActivity 需要的最小事务能力。
// pgx.Tx 天然满足它；测试用假实现即可逐字断言 metric 与 delta。
type activityExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// bumpUserActivityTx 在既有写事务内累加一次用户活动计数。
func bumpUserActivityTx(ctx context.Context, tx pgx.Tx, userID int64, metric string, delta int64) error {
	return bumpUserActivity(ctx, tx, userID, metric, delta)
}

// bumpUserActivity 是上面的实现体，接收窄接口以便单测。
//
// no-op 条件：userID 非正（匿名/系统动作）、delta 为 0（关系行没变化）。
// 未知 metric 是编程错误，直接报错而不是静默跳过——静默会让计数悄悄缺失。
func bumpUserActivity(ctx context.Context, exec activityExecer, userID int64, metric string, delta int64) error {
	if userID <= 0 || delta == 0 {
		return nil
	}
	column, ok := activityMetricColumns[metric]
	if !ok {
		return fmt.Errorf("forum: unknown activity metric %q", metric)
	}
	if _, err := exec.Exec(ctx, fmt.Sprintf(bumpUserActivitySQL, column), userID, delta); err != nil {
		return fmt.Errorf("bump user activity %s: %w", column, err)
	}
	return nil
}

// activityDeltaForReaction 返回点赞/取消点赞对用户活动统计的影响。
//
// 计划 D3：自赞不计入 like_received 与 like_given（内容行的 like_count 照常变，
// 那是热度不是成就）。因此自赞时返回 0,0，调用方拿到的 delta 为 0 即为 no-op。
// 计划 D4：取消点赞是关系行被删除，两个计数都回退。
func activityDeltaForReaction(actorUserID, recipientUserID int64, add bool) (received int64, given int64) {
	if actorUserID <= 0 || recipientUserID <= 0 || actorUserID == recipientUserID {
		return 0, 0
	}
	delta := int64(1)
	if !add {
		delta = -1
	}
	return delta, delta
}

// BadgeEvaluationEnqueuer 在写事务提交后请求一次徽章评估（计划 D2）。
//
// 约定：实现必须 fail-open（入队失败只记日志、不返回错误），
// 因此接口没有 error 返回值——统计已经落库，评估迟到几秒是可接受的，
// 让请求因为「队列抖动」失败不可接受。
type BadgeEvaluationEnqueuer interface {
	EnqueueBadgeEvaluation(ctx context.Context, userID int64)
}

// enqueueBadgeEvaluation 是 PostgresStore 的 fail-open 包装：未装配时静默跳过。
func (s *PostgresStore) enqueueBadgeEvaluation(ctx context.Context, userID int64) {
	if s == nil || s.badgeEvaluation == nil || userID <= 0 {
		return
	}
	s.badgeEvaluation.EnqueueBadgeEvaluation(ctx, userID)
}
