package conditions

import (
	"context"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// activityCountSQL 一次算完「所有请求的窗口 × 6 个指标」。
//
// 窗口用 `unnest($2::bigint[]) UNION ALL SELECT NULL` 展开：NULL 行代表「全部时间」，
// 走 `user_activity` 的反规范化计数（与旧快路径同一个真值位置，保证迁移前后逐人
// 等价）；非 NULL 行代表最近 N 天，必须回到源表按 `created_at` 数，因为
// `user_activity` 只有累计值、没有时间维度。
//
// 日界与时间戳的分工（加窗口条件时照抄这一段）：
//   - `days_visited` 是**按日**的：下界必须由 Go 传 `$3::date`（进程本地日），
//     与 `user_visits.visited_on` 的写入时钟一致。用 `CURRENT_DATE` 或
//     `(now() - interval)::date` 都是「拿 PG 会话时区取日界」，跨零点会差一天。
//   - `bookmark_count` 的累计与窗口都**不看 visibility**：写路径按 relationDelta 计数、
//     `Recompute` 数全部行。窗口侧曾只算 public，导致同一用户「累计 5 / 最近 30 天 0」。
//   - 其余五个指标比的是 `created_at` 这类**时间戳**，`now() - make_interval(...)`
//     在 SQL 里取是对的：同一瞬间在哪个时区都是同一时刻，不存在日界问题。
//
// 六个子查询都不按 `status` / `deleted_at` 过滤：软删内容仍计入「做过什么」
// （成就口径）。唯一看状态的条件是 `content.hidden`。
//
// 窗口「获赞」必须排除自赞（`r.user_id <> $1`）：写路径 `activityDeltaForReaction`
// 对自赞返 0，累计列本来就不含自赞；窗口版若含，同一枚徽章会出现「累计 0 但最近
// 30 天 5」的自相矛盾。注意这与 P-11 不冲突——P-11 的「自评论算」只针对评论动作，
// 获赞在计数层早就排除了自赞，那是既有口径。
const activityCountSQL = `
SELECT w.days,
       CASE WHEN w.days IS NULL THEN COALESCE(ua.topic_count, 0)
            ELSE (SELECT count(*) FROM topics t
                   WHERE t.author_user_id = $1
                     AND t.created_at >= now() - make_interval(days => w.days::int)) END,
       CASE WHEN w.days IS NULL THEN COALESCE(ua.comment_count, 0)
            ELSE (SELECT count(*) FROM comments c
                   WHERE c.author_user_id = $1
                     AND c.created_at >= now() - make_interval(days => w.days::int)) END,
       CASE WHEN w.days IS NULL THEN COALESCE(ua.like_received_count, 0)
            ELSE ((SELECT count(*) FROM content_reactions r
                     JOIN topics t ON t.id = r.target_id
                    WHERE r.target_type = 'topic' AND r.reaction = 'like'
                      AND r.user_id <> $1
                      AND t.author_user_id = $1
                      AND r.created_at >= now() - make_interval(days => w.days::int))
                + (SELECT count(*) FROM content_reactions r
                     JOIN comments c ON c.id = r.target_id
                    WHERE r.target_type = 'comment' AND r.reaction = 'like'
                      AND r.user_id <> $1
                      AND c.author_user_id = $1
                      AND r.created_at >= now() - make_interval(days => w.days::int))) END,
       CASE WHEN w.days IS NULL THEN COALESCE(ua.like_given_count, 0)
            ELSE (SELECT count(*) FROM content_reactions r
                   WHERE r.user_id = $1 AND r.reaction = 'like'
                     AND r.created_at >= now() - make_interval(days => w.days::int)) END,
       CASE WHEN w.days IS NULL THEN COALESCE(ua.bookmark_count, 0)
            ELSE (SELECT count(*) FROM content_bookmarks b
                   WHERE b.user_id = $1
                     AND b.created_at >= now() - make_interval(days => w.days::int)) END,
       CASE WHEN w.days IS NULL THEN COALESCE(ua.days_visited, 0)
            ELSE (SELECT count(*) FROM user_visits v
                   WHERE v.user_id = $1
                     AND v.visited_on >= $3::date - w.days::int) END
FROM (SELECT unnest($2::bigint[]) AS days UNION ALL SELECT NULL) AS w
LEFT JOIN user_activity ua ON ua.user_id = $1
`

// loadActivityCount 一次查询覆盖本棵树里出现的所有窗口。
func (r *Resolver) loadActivityCount(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	daysSet := make([]int64, 0, len(leaves))
	seen := make(map[int64]struct{}, len(leaves))
	leafDays := make([]*int64, len(leaves))
	for index, leaf := range leaves {
		days, err := windowDays(leaf)
		if err != nil {
			return nil, err
		}
		leafDays[index] = days
		if days != nil {
			if _, ok := seen[*days]; !ok {
				seen[*days] = struct{}{}
				daysSet = append(daysSet, *days)
			}
		}
	}

	rows, err := r.pool.Query(ctx, activityCountSQL, r.userID, daysSet, r.today())
	if err != nil {
		return nil, fmt.Errorf("conditions: 查询 activity.count 失败：%w", err)
	}
	defer rows.Close()

	type counters [6]int64
	byDays := make(map[int64]counters, len(daysSet)+1)
	for rows.Next() {
		var days *int64
		var values counters
		if err := rows.Scan(&days, &values[0], &values[1], &values[2], &values[3], &values[4], &values[5]); err != nil {
			return nil, fmt.Errorf("conditions: 读取 activity.count 结果失败：%w", err)
		}
		key := int64(-1)
		if days != nil {
			key = *days
		}
		byDays[key] = values
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conditions: 遍历 activity.count 结果失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for index, leaf := range leaves {
		key := int64(-1)
		if leafDays[index] != nil {
			key = *leafDays[index]
		}
		values, ok := byDays[key]
		if !ok {
			return nil, fmt.Errorf("conditions: activity.count 缺少窗口 %d 的结果", key)
		}
		metric := stringParam(leaf, "metric")
		slot, ok := metricSlot(metric)
		if !ok {
			return nil, fmt.Errorf("conditions: activity.count 未知指标 %q", metric)
		}
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = countValue(values[slot])
	}
	return out, nil
}

func metricSlot(metric string) (int, bool) {
	switch metric {
	case MetricTopicCount:
		return 0, true
	case MetricCommentCount:
		return 1, true
	case MetricLikeReceivedCount:
		return 2, true
	case MetricLikeGivenCount:
		return 3, true
	case MetricBookmarkCount:
		return 4, true
	case MetricDaysVisited:
		return 5, true
	default:
		return 0, false
	}
}

// activityStreakSQL 用「gaps and islands」算连续访问天数。
//
// `visited_on + row_number()` 在连续区间内是常量（d、d-1、d-2 配 1、2、3 都得 d+1），
// 所以按该常量分组即可，不需要把日期拉回 Go 里逐天比较。
//
// **今天由 Go 传进来（$2::date），不用 `CURRENT_DATE`**：`user_visits.visited_on` 的
// 日界是 API 进程本地时区（`visits.go` 用 `time.Now`，装配无时钟注入），而
// `site.timezone` 选项除 Options 包外零消费。API 设 `TZ=Asia/Shanghai`、PG 是 UTC 时，
// 跨零点的 8 小时里 `CURRENT_DATE` 还停在昨天，会把刚记下的「今天」排除，
// 连续 7 天在零点后立刻误判断签。
const activityStreakSQL = `
WITH ordered AS (
  SELECT visited_on,
         visited_on + (row_number() OVER (ORDER BY visited_on DESC))::int AS anchor
  FROM user_visits
  WHERE user_id = $1
    AND visited_on <= $2::date
    AND visited_on >= $2::date - 3650
)
SELECT CASE
         WHEN NOT EXISTS (SELECT 1 FROM ordered) THEN 0
         WHEN (SELECT max(visited_on) FROM ordered) < $2::date - 1 THEN 0
         ELSE (SELECT count(*) FROM ordered
                WHERE anchor = (SELECT anchor FROM ordered ORDER BY visited_on DESC LIMIT 1))
       END
`

// loadActivityStreak 每种条件类型一次查询；streak 与 minDays 参数无关
// （minDays 是阈值，由 cmp 比较）。
func (r *Resolver) loadActivityStreak(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	var streak int64
	if err := r.pool.QueryRow(ctx, activityStreakSQL, r.userID, r.today()).Scan(&streak); err != nil {
		return nil, fmt.Errorf("conditions: 查询 activity.streak 失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = countValue(streak)
	}
	return out, nil
}
