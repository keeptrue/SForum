package cachepolicy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

// TestTagGenerationIsCrossProcessOnRedis 用真实 Redis 证明「跨进程可见」：
// 两个 Service 各自持有独立 client（等价两个进程 / 两个副本），共享同一个 Redis。
//
// 没有 SFORUM_TEST_REDIS_ADDR 时**显式失败**而不是 t.Skip：`go test` 对 skip 只打一行
// `ok`，等于这条用例从不运行。一次性 Redis 由 scripts/test-api-isolated.sh 提供。
func TestTagGenerationIsCrossProcessOnRedis(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("SFORUM_TEST_REDIS_ADDR"))
	if addr == "" {
		t.Fatalf("SFORUM_TEST_REDIS_ADDR is required: without a disposable Redis the cross-process generation claim has no evidence at all (run ./scripts/test-api-isolated.sh, or export SFORUM_TEST_REDIS_ADDR for a throwaway Redis)")
	}
	ctx := context.Background()
	clientA := redis.NewClient(&redis.Options{Addr: addr})
	clientB := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})
	if err := clientA.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping disposable redis: %v", err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	cacheID := "demo.cache.redis." + suffix
	tag := "demo.cache.tag.redis." + suffix
	registry := sharedTestRegistry(t, cacheID, cacheregistry.PolicyPublic, []string{tag})

	instanceA, err := New(registry, cache.NewRedisCache(clientA), ProviderRedis)
	if err != nil {
		t.Fatal(err)
	}
	instanceB, err := New(registry, cache.NewRedisCache(clientB), ProviderRedis)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := instanceA.PlanKey(KeyRequest{CacheID: cacheID, Namespace: cacheID + ".namespace"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clientA.Del(context.Background(), plan.Key, generationKey(tag))
	})

	if err := instanceA.Set(ctx, plan, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if value, found, err := instanceB.Get(ctx, plan); err != nil || !found || string(value) != "v1" {
		t.Fatalf("second process must see the cached value: found=%t value=%q err=%v", found, value, err)
	}

	if _, err := instanceA.Invalidate(ctx, InvalidateRequest{
		Tags: []string{tag}, Actor: "admin:1", Reason: "entity.updated",
	}); err != nil {
		t.Fatal(err)
	}
	// B 没有参与失效，也没有被通知；它只能通过 Redis 里的 generation 判定过期。
	if _, found, err := instanceB.Get(ctx, plan); err != nil || found {
		t.Fatalf("tag invalidation must be visible in another process: found=%t err=%v", found, err)
	}

	// generation 必须带 TTL：长期计数器不该永久驻留。
	ttl, err := clientA.TTL(ctx, generationKey(tag)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 {
		t.Fatalf("generation key must carry a TTL, got %v", ttl)
	}
}
