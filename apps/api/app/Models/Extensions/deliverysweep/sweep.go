// Package deliverysweep 收口扩展事件投递账本里「永远到不了终态」的行。
//
// 为什么需要它：`Support/Extensions` 的 observe 投递先在
// `extension_event_deliveries` 写一条 queued 行，再把它推进到
// running → succeeded/failed。进程死在这两步之间（或写终态失败被吞掉）时，
// 那行会永久停在 queued/running，而仓里没有任何别的东西会回头处理它——
// 既不会被重试，也不会进终态，管理面只能手工改库。
//
// 安全审计 P3-9 的关闭条件是「行写入与入队同事务，**或**补 sweeper + replay」。
// 事务化只在 Dispatcher + 事务池都接进 ManagerConfig 时才生效，因此这里补上
// 与 P2-6（邮件投递）同款的周期兜底，保证**当前生产路径**（in-process 投递）
// 也不会留下永久中途行。
package deliverysweep

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// StaleEventDeliveryThreshold 是 sweeper 的默认阈值。
//
// 依据：observe 投递在两种装配下都很快——进程内 `Deliver` 是同步调用（秒级），
// 队列化后走 River 默认队列（正常情况下也是秒级）。超过 15 分钟仍未到终态的
// 行不可能是「还在飞」，只可能是调度侧丢了（进程崩溃、job 被丢弃、写终态失败）。
//
// 误杀代价可控：sweep 只把行标成 failed，晚到的 worker 仍会把同一行改成
// running → succeeded（`UpdateEventDelivery` 无条件写状态），所以这是可自愈的
// 观测修正，不是数据破坏。
const StaleEventDeliveryThreshold = 15 * time.Minute

// ReasonStaleSwept 是被 sweep 的行使用的稳定 reason 键。
//
// 它在契约里的位置：`ExtensionEventDelivery.reason` 是**自由文本**（OpenAPI 没有枚举），
// 所以它会作为管理面事件投递日志（`GET .../event-deliveries`）的响应值出现。
// 它**不进** reason 枚举，也**不进** Localization 翻译表——管理面拿到的就是这条稳定键。
const ReasonStaleSwept = "extension.delivery_stale_swept"

// PostgresSweeper 是投递账本的 stale 行收口器。
type PostgresSweeper struct {
	pool *pgxpool.Pool
}

func NewPostgresSweeper(pool *pgxpool.Pool) *PostgresSweeper {
	return &PostgresSweeper{pool: pool}
}

// SweepStaleEventDeliveries 把超过 olderThan 仍停在 queued/running 的行收成 failed。
//
// 幂等：终态行不再匹配 WHERE，重复调用返回 0。
//
// olderThan 小于 1 秒时用默认阈值。**不能**用 `olderThan <= 0` 判定：SQL 里的
// 时长是 `int64(olderThan.Seconds())` 秒级截断，500ms 这种正数会被截成 0 秒，
// 于是 `updated_at < now() - 0s` 成立、把正在飞的投递一把扫掉。
// （P2-6 的邮件 sweeper 是同一个截断写法，属同类问题；那处不在本任务范围。）
// 返回被收口的行数，调用方据此打可观测日志（「本该投递却没投递」的信号）。
func (s *PostgresSweeper) SweepStaleEventDeliveries(ctx context.Context, olderThan time.Duration) (int, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("extensions deliverysweep: sweeper is not configured")
	}
	if olderThan < time.Second {
		olderThan = StaleEventDeliveryThreshold
	}
	// updated_at 而不是 created_at：running 行可能在 created_at 之后很久才被 worker
	// 认领，用 updated_at 才不会把「刚刚开始跑」的行当成超龄。
	// 表名不写 public. 前缀：与同表的其它语句（Models/Extensions 的
	// CreateEventDelivery 等）一致，跟随连接的 search_path。写死 public. 会让
	// schema 隔离的测试库/物理隔离实例改到别的库的同名表上。
	tag, err := s.pool.Exec(ctx, `
		UPDATE extension_event_deliveries
		SET status = $1,
		    reason = $2,
		    message = 'Delivery did not reach a terminal state before the stale threshold; swept by the periodic maintenance job.',
		    updated_at = now(),
		    completed_at = COALESCE(completed_at, now())
		WHERE status IN ($3, $4)
		  AND updated_at < now() - ($5 * interval '1 second')`,
		extensions.DeliveryFailed, ReasonStaleSwept,
		extensions.DeliveryQueued, extensions.DeliveryRunning,
		int64(olderThan.Seconds()))
	if err != nil {
		return 0, fmt.Errorf("sweep stale extension event deliveries: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
