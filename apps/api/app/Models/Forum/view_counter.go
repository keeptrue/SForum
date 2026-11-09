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

// ViewDeltaDrainer 供 River 周期任务拉取待刷盘增量。
type ViewDeltaDrainer interface {
	DrainDeltas(ctx context.Context) (map[int64]int64, error)
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

// RedisTopicViewCounter 生产实现：SETNX 去重 + INCR 增量 + dirty set。
type RedisTopicViewCounter struct {
	client *redis.Client
	logger *slog.Logger
	// DedupTTL 可测；默认 30m。
	DedupTTL time.Duration
}

func NewRedisTopicViewCounter(client *redis.Client) *RedisTopicViewCounter {
	return &RedisTopicViewCounter{client: client, DedupTTL: viewDedupTTL}
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

// DrainDeltas 读取脏主题增量：GET 后 DECRBY 已读量，避免与并发 INCR 丢计数。
func (c *RedisTopicViewCounter) DrainDeltas(ctx context.Context) (map[int64]int64, error) {
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
		// 扣减已读量；并发 INCR 的残留留在 key 上。
		left, decrErr := c.client.DecrBy(ctx, deltaKey, n).Result()
		if decrErr != nil {
			return out, fmt.Errorf("decr view delta %d: %w", topicID, decrErr)
		}
		out[topicID] = n
		if left <= 0 {
			_ = c.client.Del(ctx, deltaKey).Err()
			_ = c.client.SRem(ctx, dirtySetKey, raw).Err()
		}
		// left > 0：保留 dirty 成员，下一轮再刷。
	}
	return out, nil
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

func (c *MemoryTopicViewCounter) DrainDeltas(_ context.Context) (map[int64]int64, error) {
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
		delete(c.deltas, id)
		delete(c.dirty, id)
	}
	return out, nil
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
