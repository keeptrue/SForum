package humanverify

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisStoreClient 是 RedisStore 用到的最小 Redis 面。
// 保留 Incr/Expire 是为了让「拆成两条命令」的回归能被测试直接看见。
type redisStoreClient interface {
	redis.Scripter
	SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd
	Incr(ctx context.Context, key string) *redis.IntCmd
	Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd
	Close() error
}

type RedisStore struct {
	client redisStoreClient
	prefix string
}

func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client, prefix: "humanverify:"}
}

// RedisClientOptions 封装 go-redis 连接池与超时的可配置项。
// 零值字段会走 go-redis 默认值，与改造前行为兼容。
type RedisClientOptions struct {
	PoolSize        int
	MinIdleConns    int
	DialTimeout     time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ConnMaxIdleTime time.Duration
	ConnMaxLifetime time.Duration
}

// NewRedisClient 创建 go-redis 客户端。opts 为零值时与原行为一致（全默认）。
// 该函数被 humanverify 与 forum 缓存层共用，调用方应传入同一个 client
// 以复用连接池（见 bootstrap/app.go）。
func NewRedisClient(addr string, password string, opts RedisClientOptions) *redis.Client {
	options := &redis.Options{
		Addr:     addr,
		Password: password,
	}
	if opts.PoolSize > 0 {
		options.PoolSize = opts.PoolSize
	}
	if opts.MinIdleConns > 0 {
		options.MinIdleConns = opts.MinIdleConns
	}
	if opts.DialTimeout > 0 {
		options.DialTimeout = opts.DialTimeout
	}
	if opts.ReadTimeout > 0 {
		options.ReadTimeout = opts.ReadTimeout
	}
	if opts.WriteTimeout > 0 {
		options.WriteTimeout = opts.WriteTimeout
	}
	if opts.ConnMaxIdleTime > 0 {
		options.ConnMaxIdleTime = opts.ConnMaxIdleTime
	}
	if opts.ConnMaxLifetime > 0 {
		options.ConnMaxLifetime = opts.ConnMaxLifetime
	}
	return redis.NewClient(options)
}

func (s *RedisStore) MarkUsed(ctx context.Context, key string, ttl time.Duration) error {
	ok, err := s.client.SetNX(ctx, s.prefix+"used:"+key, "1", ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return ErrReplayed
	}
	return nil
}

func (s *RedisStore) IncrementRate(ctx context.Context, key string, window time.Duration, limit int) (bool, error) {
	if limit <= 0 {
		return false, nil
	}

	redisKey := s.prefix + "rate:" + key
	count, err := rateLimitScript.Run(ctx, s.client, []string{redisKey}, window.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return count > int64(limit), nil
}

// rateLimitScript 把「自增」与「首次设置 TTL」压进一次 EVAL（RV-52）。
//
// 分成 INCR + EXPIRE 两条命令时，两者之间崩溃/断连会留下没有 TTL 的计数键：
// 该 subject 被永久限流，且 key 永不过期。`PTTL < 0` 分支顺带修复历史遗留的
// 无 TTL 键，而不必等它自然消失。
var rateLimitScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 or redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

func (s *RedisStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}
