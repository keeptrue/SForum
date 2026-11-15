package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache 基于 go-redis/v9 的生产实现。
// 使用与 session/humanverify 一致的 *redis.Client，由调用方负责创建与关闭。
type RedisCache struct {
	client *redis.Client
}

func NewRedisCache(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

func (c *RedisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	cmd := c.client.Get(ctx, key)
	value, err := cmd.Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (c *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

func (c *RedisCache) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return c.client.Del(ctx, keys...).Err()
}

func (c *RedisCache) Increment(ctx context.Context, key string) (int64, error) {
	return c.client.Incr(ctx, key).Result()
}

// incrementWithTTLScript 把「自增」与「续期」压进一次 EVAL：
// 分成 INCR + PEXPIRE 两条命令时，中间崩溃会留下没有 TTL 的计数器。
var incrementWithTTLScript = redis.NewScript(`
local value = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ARGV[1])
return value
`)

func (c *RedisCache) IncrementWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return c.client.Incr(ctx, key).Result()
	}
	return incrementWithTTLScript.Run(ctx, c.client, []string{key}, ttl.Milliseconds()).Int64()
}
