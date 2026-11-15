package forum

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// 浏览量计数（Iteration A WS1 / million-scale M2 D3）：
//   - 去重键 forum:topic:viewed:{topicID}:{visitorHash} TTL 30m（SETNX）
//   - 增量键 forum:topic:views:delta:{topicID}（INCR）
//   - 脏集合 forum:topic:views:dirty（SADD topicID）
// 禁止在公开详情请求路径直接 UPDATE topics.view_count。

const (
	viewedKeyPrefix = "forum:topic:viewed:"
	deltaKeyPrefix  = "forum:topic:views:delta:"
	dirtySetKey     = "forum:topic:views:dirty"
	viewDedupTTL    = 30 * time.Minute
)

// TopicViewRecorder 记录一次唯一访客浏览。实现必须在 Redis 故障时静默跳过，不返回致命错误。
type TopicViewRecorder interface {
	RecordView(ctx context.Context, topicID int64, visitorKey string)
}

// ViewDeltaSource 读取待刷盘增量，但**不消费**。
//
// 消费必须等落库成功之后（见 ViewDeltaAcker）：先扣减再落库，落库失败就会把
// 这段浏览永久丢掉，且没有归还路径（RV-52）。
type ViewDeltaSource interface {
	ReadViewDeltas(ctx context.Context) (map[int64]int64, error)
}

// ViewDeltaAcker 在增量成功写入 topics 之后扣减已应用的部分。
//
// 语义是 at-least-once：扣减失败时增量仍留在 Redis，下一轮会再次落库。
// 浏览量是近似指标，宁可多算一次也不能丢——丢失没有任何补偿路径。
type ViewDeltaAcker interface {
	AckViewDeltas(ctx context.Context, applied map[int64]int64) error
}

// ViewCountApplier 将增量写入 topics.view_count 与 hot_score。
type ViewCountApplier interface {
	ApplyViewCountDeltas(ctx context.Context, deltas map[int64]int64) (int, error)
}

// HashVisitorKey 压缩访客标识，避免 Redis key 过长。
func HashVisitorKey(visitorKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(visitorKey)))
	return hex.EncodeToString(sum[:16])
}

// redisViewCounterClient 是浏览量计数器用到的最小 Redis 面。
//
// Ack 必须是一次 EVAL（见 ackViewDeltasScript）；保留 DecrBy/Del/SRem 是为了让
// 「拆成多步」的回归能被测试直接看见，而不是只靠读代码。
type redisViewCounterClient interface {
	redis.Scripter
	SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd
	Incr(ctx context.Context, key string) *redis.IntCmd
	SAdd(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	SMembers(ctx context.Context, key string) *redis.StringSliceCmd
	Get(ctx context.Context, key string) *redis.StringCmd
	DecrBy(ctx context.Context, key string, decrement int64) *redis.IntCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	SRem(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
}

// RedisTopicViewCounter 生产实现：SETNX 去重 + INCR 增量 + dirty set。
type RedisTopicViewCounter struct {
	client redisViewCounterClient
	logger *slog.Logger
	// DedupTTL 可测；默认 30m。
	DedupTTL time.Duration
}

func NewRedisTopicViewCounter(client *redis.Client) *RedisTopicViewCounter {
	counter := &RedisTopicViewCounter{DedupTTL: viewDedupTTL}
	// client 为 nil 时不能把 typed-nil 塞进接口字段：接口非 nil 会让各方法的
	// `c.client == nil` 判定失效、直接 panic，打破 worker 侧「nil → 空跑」契约。
	if client != nil {
		counter.client = client
	}
	return counter
}

func (c *RedisTopicViewCounter) WithLogger(logger *slog.Logger) *RedisTopicViewCounter {
	if c != nil {
		c.logger = logger
	}
	return c
}

func (c *RedisTopicViewCounter) RecordView(ctx context.Context, topicID int64, visitorKey string) {
	if c == nil || c.client == nil || topicID <= 0 {
		return
	}
	visitorKey = strings.TrimSpace(visitorKey)
	if visitorKey == "" {
		return
	}
	ttl := c.DedupTTL
	if ttl <= 0 {
		ttl = viewDedupTTL
	}
	viewedKey := viewedKeyPrefix + strconv.FormatInt(topicID, 10) + ":" + HashVisitorKey(visitorKey)
	ok, err := c.client.SetNX(ctx, viewedKey, "1", ttl).Result()
	if err != nil {
		if c.logger != nil {
			c.logger.WarnContext(ctx, "forum view: redis setnx failed, skip count", "topic_id", topicID, "err", err)
		}
		return
	}
	if !ok {
		return
	}
	deltaKey := deltaKeyPrefix + strconv.FormatInt(topicID, 10)
	// 先 INCR 再 SADD：Drain 用 GET+DECRBY，残留增量会留在 key 上并由下次 dirty 刷盘。
	if err := c.client.Incr(ctx, deltaKey).Err(); err != nil {
		if c.logger != nil {
			c.logger.WarnContext(ctx, "forum view: redis incr failed", "topic_id", topicID, "err", err)
		}
		return
	}
	if err := c.client.SAdd(ctx, dirtySetKey, topicID).Err(); err != nil && c.logger != nil {
		c.logger.WarnContext(ctx, "forum view: redis sadd dirty failed", "topic_id", topicID, "err", err)
	}
}

// ReadViewDeltas 读取脏主题当前的增量快照，不扣减任何计数。
func (c *RedisTopicViewCounter) ReadViewDeltas(ctx context.Context) (map[int64]int64, error) {
	if c == nil || c.client == nil {
		return nil, nil
	}
	members, err := c.client.SMembers(ctx, dirtySetKey).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers dirty views: %w", err)
	}
	out := make(map[int64]int64, len(members))
	for _, raw := range members {
		topicID, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || topicID <= 0 {
			_ = c.client.SRem(ctx, dirtySetKey, raw).Err()
			continue
		}
		deltaKey := deltaKeyPrefix + strconv.FormatInt(topicID, 10)
		n, getErr := c.client.Get(ctx, deltaKey).Int64()
		if getErr == redis.Nil || n <= 0 {
			_ = c.client.SRem(ctx, dirtySetKey, raw).Err()
			_ = c.client.Del(ctx, deltaKey).Err()
			continue
		}
		if getErr != nil {
			return out, fmt.Errorf("get view delta %d: %w", topicID, getErr)
		}
		out[topicID] = n
	}
	return out, nil
}

// ackViewDeltasScript 把「扣减」与「归零清理」压进一次 EVAL。
//
// 分成 DECRBY 再 DEL 两步时，夹在中间的并发 INCR 会被 DEL 直接吞掉——这正是
// RV-52.3/52.4 修过的同类缺陷。脚本内先读当前值再决定：归零才清理，否则只扣减，
// 并发 INCR 的残留在两种分支下都不会丢。
var ackViewDeltasScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
local left = tonumber(current or '0') - tonumber(ARGV[1])
if left <= 0 then
  redis.call('DEL', KEYS[1])
  redis.call('SREM', KEYS[2], ARGV[2])
  return 0
end
redis.call('DECRBY', KEYS[1], ARGV[1])
return left
`)

// AckViewDeltas 扣减已落库的增量；并发 INCR 的残留留在 key 上，下一轮再刷。
func (c *RedisTopicViewCounter) AckViewDeltas(ctx context.Context, applied map[int64]int64) error {
	if c == nil || c.client == nil || len(applied) == 0 {
		return nil
	}
	for topicID, n := range applied {
		if topicID <= 0 || n <= 0 {
			continue
		}
		raw := strconv.FormatInt(topicID, 10)
		deltaKey := deltaKeyPrefix + raw
		if _, err := ackViewDeltasScript.Run(ctx, c.client, []string{deltaKey, dirtySetKey}, n, raw).Int64(); err != nil {
			return fmt.Errorf("ack view delta %d: %w", topicID, err)
		}
	}
	return nil
}

// MemoryTopicViewCounter 单测用内存实现（与 Redis 语义对齐）。
type MemoryTopicViewCounter struct {
	mu       sync.Mutex
	viewed   map[string]time.Time
	deltas   map[int64]int64
	dirty    map[int64]struct{}
	DedupTTL time.Duration
	// FailRecord 为 true 时模拟 Redis 故障：RecordView 空操作。
	FailRecord bool
}

func NewMemoryTopicViewCounter() *MemoryTopicViewCounter {
	return &MemoryTopicViewCounter{
		viewed:   map[string]time.Time{},
		deltas:   map[int64]int64{},
		dirty:    map[int64]struct{}{},
		DedupTTL: viewDedupTTL,
	}
}

func (c *MemoryTopicViewCounter) RecordView(_ context.Context, topicID int64, visitorKey string) {
	if c == nil || c.FailRecord || topicID <= 0 {
		return
	}
	visitorKey = strings.TrimSpace(visitorKey)
	if visitorKey == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := c.DedupTTL
	if ttl <= 0 {
		ttl = viewDedupTTL
	}
	key := strconv.FormatInt(topicID, 10) + ":" + HashVisitorKey(visitorKey)
	if exp, ok := c.viewed[key]; ok && time.Now().Before(exp) {
		return
	}
	c.viewed[key] = time.Now().Add(ttl)
	c.deltas[topicID]++
	c.dirty[topicID] = struct{}{}
}

func (c *MemoryTopicViewCounter) ReadViewDeltas(_ context.Context) (map[int64]int64, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int64]int64, len(c.dirty))
	for id := range c.dirty {
		n := c.deltas[id]
		if n <= 0 {
			delete(c.deltas, id)
			delete(c.dirty, id)
			continue
		}
		out[id] = n
	}
	return out, nil
}

func (c *MemoryTopicViewCounter) AckViewDeltas(_ context.Context, applied map[int64]int64) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, n := range applied {
		if n <= 0 {
			continue
		}
		c.deltas[id] -= n
		if c.deltas[id] <= 0 {
			delete(c.deltas, id)
			delete(c.dirty, id)
		}
	}
	return nil
}

// Delta 测试辅助：当前未刷盘增量。
func (c *MemoryTopicViewCounter) Delta(topicID int64) int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deltas[topicID]
}
