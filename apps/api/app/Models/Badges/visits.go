package badges

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	visitKeyPrefix = "badges:visit:"
	// visitKeyTTL 覆盖「跨日边界 + 时钟漂移」：只要超过 24h 就足以让 key 自然过期，
	// 25h 留一小时余量，避免刚好在 23:59 写入、00:01 又被当成新的一天重复记。
	visitKeyTTL = 25 * time.Hour
)

// VisitDedupKey 返回某用户某天的 Redis 去重键（badges:visit:{userID}:{YYYY-MM-DD}）。
func VisitDedupKey(userID int64, day time.Time) string {
	return fmt.Sprintf("%s%d:%s", visitKeyPrefix, userID, day.Format("2006-01-02"))
}

// VisitDeduper 是「今天是否已记过」的最小契约，生产实现是 Redis SETNX。
//
// 抽出这个接口不是为了将来换存储，而是为了让去重语义（首次 true、同日后续 false）
// 与降级路径（返回 error）能在没有 Redis 的环境里被确定性地测到。
type VisitDeduper interface {
	// MarkOnce 原子地占位：首次返回 (true, nil)，已存在返回 (false, nil)；
	// 存储不可用时返回 (false, err)，调用方据此降级为直接写库。
	MarkOnce(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// RedisVisitDeduper 是 VisitDeduper 的 Redis SETNX 实现。
type RedisVisitDeduper struct {
	client *redis.Client
}

func NewRedisVisitDeduper(client *redis.Client) *RedisVisitDeduper {
	return &RedisVisitDeduper{client: client}
}

func (d *RedisVisitDeduper) MarkOnce(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if d == nil || d.client == nil {
		return false, errors.New("badges: visit deduper has no redis client")
	}
	return d.client.SetNX(ctx, key, "1", ttl).Result()
}

// VisitRecorder 记录登录用户的「今天来过」。
//
// 模式照 forum.RedisTopicViewCounter：Redis SETNX 挡掉重复写库，只有首次命中
// 才同步写 user_visits（一天一次、一次一行，写入量 = DAU）。
//
// 失败语义是 fail-open：Record 不返回错误，任何失败只记日志。
// 访问采集是徽章成就的输入，绝不能因为它失败而让详情请求失败。
type VisitRecorder struct {
	store  ActivityStore
	dedupe VisitDeduper
	// evaluator 可选：记录到新的一天后触发一次徽章评估（计划 D2）。
	evaluator EvaluationEnqueuer
	logger    *slog.Logger
	now       func() time.Time
	ttl       time.Duration
}

// NewVisitRecorder 构造访问采集器。client 可以为 nil（无 Redis 的部署），
// 此时退化为「每次都写库」——user_visits 的复合主键让冲突写入无害。
func NewVisitRecorder(store ActivityStore, client *redis.Client, logger *slog.Logger) *VisitRecorder {
	if client == nil {
		return NewVisitRecorderWithDeduper(store, nil, logger)
	}
	return NewVisitRecorderWithDeduper(store, NewRedisVisitDeduper(client), logger)
}

// NewVisitRecorderWithDeduper 供测试与自定义存储注入去重实现；dedupe 为 nil 时
// 退化为「每次都写库」，靠 user_visits 的复合主键兜幂等。
func NewVisitRecorderWithDeduper(store ActivityStore, dedupe VisitDeduper, logger *slog.Logger) *VisitRecorder {
	return &VisitRecorder{
		store:  store,
		dedupe: dedupe,
		logger: logger,
		now:    time.Now,
		ttl:    visitKeyTTL,
	}
}

// EvaluationEnqueuer 在 days_visited 真的变化后触发一次用户状态评估
// （徽章 + 信任等级；两者去重维度各自独立）。
// 实现必须 fail-open；未装配时静默跳过。
type EvaluationEnqueuer interface {
	EnqueueUserEvaluations(ctx context.Context, userID int64)
}

// WithEvaluationEnqueuer 注入评估入队器。
func (r *VisitRecorder) WithEvaluationEnqueuer(enqueuer EvaluationEnqueuer) *VisitRecorder {
	if r != nil {
		r.evaluator = enqueuer
	}
	return r
}

// Record 记录一次登录用户的访问。匿名（userID <= 0）直接返回。
//
// 三层幂等：
//  1. Redis SETNX 未命中 → 今天已记过，直接返回；
//  2. Redis 不可用 → 降级直接写库（多一次冲突写入，无害）；
//  3. 库里的 (user_id, visited_on) 主键 + ON CONFLICT DO NOTHING → 只有真的
//     插入成功才把 days_visited +1。
func (r *VisitRecorder) Record(ctx context.Context, userID int64) {
	if r == nil || r.store == nil || userID <= 0 {
		return
	}
	day := r.now()
	if r.dedupe != nil {
		first, err := r.dedupe.MarkOnce(ctx, VisitDedupKey(userID, day), r.ttl)
		if err != nil {
			// 降级：Redis 挂了也要把访问记下来，代价只是多一次冲突写。
			r.warn("badges visit dedup unavailable, writing visit row directly",
				"user_id", userID, "error", err)
		} else if !first {
			return
		}
	}
	inserted, err := r.store.RecordVisit(ctx, userID, day)
	if err != nil {
		r.warn("record user visit failed", "user_id", userID, "error", err)
		return
	}
	if !inserted {
		// 库里已有当天行：Redis 那边可能刚好过期重建，这里兜住。
		return
	}
	// D2：days_visited 真的变了才触发评估（常客/老住户/元老依赖它）。
	if r.evaluator != nil {
		r.evaluator.EnqueueUserEvaluations(ctx, userID)
	}
}

func (r *VisitRecorder) warn(message string, args ...any) {
	if r.logger == nil {
		return
	}
	r.logger.Warn(message, args...)
}
