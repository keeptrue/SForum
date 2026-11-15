// Package cache 提供轻量的业务读缓存抽象。
//
// 设计目标：为论坛公共读路径（分类、标签、主题列表、主题详情）提供短 TTL 缓存，
// 降低千万级数据下 PostgreSQL 的重复扫描压力。Cache 只缓存可重建的派生数据，
// 绝不缓存鉴权/会话等安全相关状态。
//
// 实现策略：
//   - Cache 接口保持极简，方便测试用内存实现（项目不依赖 miniredis）。
//   - 生产用 RedisCache（go-redis/v9），开发/测试用 MemoryCache。
//   - 写失效采用 generation 方案（见 CachedStore），避免对前缀 key 做 SCAN。
package cache

import (
	"context"
	"time"
)

// Cache 是业务缓存的统一抽象。value 统一为 []byte，由调用方自行 JSON 序列化。
type Cache interface {
	// Get 读取 key。found 为 false 表示 miss（含底层错误时 miss 并返回 err）。
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	// Set 写入 key 并设置 TTL。
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Delete 删除一个或多个 key。
	Delete(ctx context.Context, keys ...string) error
	// Increment 对整数计数器自增 1 并返回新值。key 不存在时从 0 开始。
	// 用于 CachedStore 的 generation 版本号失效方案。
	Increment(ctx context.Context, key string) (int64, error)
}

// TTLIncrementer 是 Cache 的可选原子能力：自增并保证 key 带过期时间。
//
// 为什么不把 Increment 直接换掉：Redis 的 INCR 不设置 TTL，单用它会让 generation
// 这类长期计数器永久驻留。读-改-写（Get→Set）又能让并发写者算出同一个版本号。
// 需要同时满足「原子」与「有 TTL」的调用方（例如 CachedStore.bumpGen）通过这个
// 可选接口拿能力，没有实现的 Cache 退回旧路径。
type TTLIncrementer interface {
	IncrementWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error)
}

// NoopCache 不缓存任何东西，所有读操作均 miss。用于显式关闭缓存的场景。
type NoopCache struct{}

func (NoopCache) Get(_ context.Context, _ string) ([]byte, bool, error) { return nil, false, nil }
func (NoopCache) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}
func (NoopCache) Delete(_ context.Context, _ ...string) error          { return nil }
func (NoopCache) Increment(_ context.Context, _ string) (int64, error) { return 0, nil }
func (NoopCache) IncrementWithTTL(_ context.Context, _ string, _ time.Duration) (int64, error) {
	return 0, nil
}
