package forum

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"
)

// generation 版本号必须原子自增 + 带 TTL（RV-52）。
//
// 旧实现 currentGen → ParseInt → ++ → Set 是「读-改-写」：并发写路径会算出相同
// 版本号，让旧列表多缓存一个 TTL。Cache 已有 Increment，但 Redis 的 INCR 不设
// 过期时间，所以这里要求的是「一次原子自增 + 同时续期」。
func TestBumpGenUsesAtomicIncrementWithTTL(t *testing.T) {
	c := newAtomicGenCache()
	store := &CachedStore{cache: c}
	ctx := context.Background()

	store.bumpGen(ctx, genTopicsGlobal)
	if c.incrementCalls != 1 {
		t.Fatalf("bumpGen issued %d atomic TTL increments, want 1", c.incrementCalls)
	}
	if c.setCalls != 0 {
		t.Fatalf("bumpGen still used read-modify-write Set %d times; concurrent writers can mint the same version", c.setCalls)
	}
	if c.lastTTL != 24*time.Hour {
		t.Fatalf("generation increment TTL = %s, want 24h (INCR alone leaves the key resident forever)", c.lastTTL)
	}
	if got := store.currentGen(ctx, genTopicsGlobal); got != "1" {
		t.Fatalf("currentGen after one bump = %q, want 1", got)
	}
}

// 并发写路径不得产生相同版本号。
func TestBumpGenConcurrentWritersNeverShareAVersion(t *testing.T) {
	c := newAtomicGenCache()
	store := &CachedStore{cache: c}
	ctx := context.Background()

	const writers = 16
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.bumpGen(ctx, genTopicsGlobal)
		}()
	}
	wg.Wait()

	seen := map[int64]struct{}{}
	for _, value := range c.incremented {
		if _, ok := seen[value]; ok {
			t.Fatalf("two concurrent writers minted the same generation %d: %v", value, c.incremented)
		}
		seen[value] = struct{}{}
	}
	if len(seen) != writers {
		t.Fatalf("generation versions issued = %d, want %d: %v", len(seen), writers, c.incremented)
	}
	if got := store.currentGen(ctx, genTopicsGlobal); got != strconv.Itoa(writers) {
		t.Fatalf("final generation = %q, want %d", got, writers)
	}
}

// atomicGenCache 模拟生产 Redis：IncrementWithTTL 写的是同一份键空间，
// 所以 Get 能读到自增后的值（这也是 currentGen 的读路径）。
type atomicGenCache struct {
	mu             sync.Mutex
	items          map[string][]byte
	expiresAt      map[string]time.Time
	incrementCalls int
	setCalls       int
	lastTTL        time.Duration
	incremented    []int64
}

func newAtomicGenCache() *atomicGenCache {
	return &atomicGenCache{items: map[string][]byte{}, expiresAt: map[string]time.Time{}}
}

func (c *atomicGenCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.items[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (c *atomicGenCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCalls++
	c.items[key] = append([]byte(nil), value...)
	c.expiresAt[key] = time.Now().Add(ttl)
	return nil
}

func (c *atomicGenCache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		delete(c.items, key)
	}
	return nil
}

func (c *atomicGenCache) Increment(_ context.Context, key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.incrementLocked(key, 0), nil
}

func (c *atomicGenCache) IncrementWithTTL(_ context.Context, key string, ttl time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.incrementCalls++
	c.lastTTL = ttl
	return c.incrementLocked(key, ttl), nil
}

func (c *atomicGenCache) incrementLocked(key string, ttl time.Duration) int64 {
	var n int64
	if value, ok := c.items[key]; ok {
		n, _ = strconv.ParseInt(string(value), 10, 64)
	}
	n++
	c.items[key] = []byte(strconv.FormatInt(n, 10))
	if ttl > 0 {
		c.expiresAt[key] = time.Now().Add(ttl)
	}
	c.incremented = append(c.incremented, n)
	return n
}
