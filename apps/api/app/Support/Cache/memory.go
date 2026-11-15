package cache

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// MemoryCache 是基于 sync.Map + 过期时间的内存实现，仅用于测试和本地开发。
// 不持久化、不跨进程，生产环境请使用 RedisCache。
type MemoryCache struct {
	mu    sync.Mutex
	items map[string]memoryEntry
	gen   map[string]int64
}

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		items: map[string]memoryEntry{},
		gen:   map[string]int64{},
	}
}

func (c *MemoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok || time.Now().After(entry.expiresAt) {
		// 惰性清理过期项，避免内存累积。
		delete(c.items, key)
		return nil, false, nil
	}
	// 返回拷贝，防止调用方无意修改缓存内值。
	out := make([]byte, len(entry.value))
	copy(out, entry.value)
	return out, true, nil
}

func (c *MemoryCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	stored := make([]byte, len(value))
	copy(stored, value)
	c.items[key] = memoryEntry{value: stored, expiresAt: time.Now().Add(ttl)}
	return nil
}

func (c *MemoryCache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		delete(c.items, key)
	}
	return nil
}

// IncrementWithTTL 与 Redis 版同义：写在 Get 能读到的那份键空间里，
// 并顺手刷新过期时间。
func (c *MemoryCache) IncrementWithTTL(_ context.Context, key string, ttl time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int64
	if entry, ok := c.items[key]; ok && time.Now().Before(entry.expiresAt) {
		n, _ = strconv.ParseInt(string(entry.value), 10, 64)
	}
	n++
	c.items[key] = memoryEntry{value: []byte(strconv.FormatInt(n, 10)), expiresAt: time.Now().Add(ttl)}
	return n, nil
}

func (c *MemoryCache) Increment(_ context.Context, key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen[key]++
	return c.gen[key], nil
}
