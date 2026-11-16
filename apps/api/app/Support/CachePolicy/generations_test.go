package cachepolicy

import (
	"context"
	"errors"
	"testing"
	"time"

	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

// failingGenerations 让 generation 读写全部失败，用来固定 Redis 故障时的姿态。
type failingGenerations struct{ err error }

func (f failingGenerations) Current(context.Context, []string) (map[string]uint64, error) {
	return nil, f.err
}

func (f failingGenerations) Bump(context.Context, []string) (map[string]uint64, error) {
	return nil, f.err
}

func sharedTestRegistry(t *testing.T, cacheID, policy string, tags []string) *cacheregistry.Registry {
	t.Helper()
	publication := testPublication("demo.cache", false, 'c')
	registry := cacheregistry.New().WithPluginAdmission(func(artifact cacheregistry.Artifact) bool {
		return artifact == publication.Artifact
	})
	publication.Caches = []cacheregistry.Declaration{{
		ID: cacheID, ContractVersion: cacheID + "@1",
		Namespace: cacheID + ".namespace", Policy: policy, Tags: tags,
	}}
	if _, err := registry.Publish(publication); err != nil {
		t.Fatalf("publish cache declaration: %v", err)
	}
	return registry
}

// TestTagInvalidationIsVisibleToAnotherServiceInstance 固定 P3-11：tag 失效必须对
// **另一个实例**生效。两个 Service 共享同一后端（等价两个副本连同一个 Redis），
// 但各自持有独立的本地索引——A 的失效只能通过 generation 传到 B。
//
// 变异要求：把 Invalidate 改回「只清本进程内存/只删本地索引里的 key」，
// 本用例必须红在 B 的 Get 上。
func TestTagInvalidationIsVisibleToAnotherServiceInstance(t *testing.T) {
	ctx := context.Background()
	registry := sharedTestRegistry(t, "demo.cache.shared", cacheregistry.PolicyPublic, []string{"demo.cache.tag.shared"})
	backend := cache.NewMemoryCache()

	instanceA, err := New(registry, backend, ProviderMemory)
	if err != nil {
		t.Fatal(err)
	}
	instanceB, err := New(registry, backend, ProviderMemory)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := instanceA.PlanKey(KeyRequest{
		CacheID: "demo.cache.shared", Namespace: "demo.cache.shared.namespace", PageID: "page.list",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := instanceA.Set(ctx, plan, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if value, found, err := instanceB.Get(ctx, plan); err != nil || !found || string(value) != "v1" {
		t.Fatalf("instance B must see instance A's value: found=%t value=%q err=%v", found, value, err)
	}

	// 关键：由**没写过这个 key** 的实例 B 发起失效。B 的本地索引是空的，删不到
	// 任何 key——所以只有 generation 递增能让 A 手里的条目失效。若失效实现退化成
	// 「只清本进程内存」，A 会继续命中，本用例必红。
	if _, err := instanceB.Invalidate(ctx, InvalidateRequest{
		Tags: []string{"demo.cache.tag.shared"}, Actor: "admin:2", Reason: "entity.updated",
	}); err != nil {
		t.Fatal(err)
	}

	if _, found, err := instanceA.Get(ctx, plan); err != nil || found {
		t.Fatalf("tag invalidation must reach the instance that wrote the entry: found=%t err=%v", found, err)
	}
	if _, found, err := instanceB.Get(ctx, plan); err != nil || found {
		t.Fatalf("invalidating instance must miss too: found=%t err=%v", found, err)
	}
}

// TestGenerationFailureIsFailClosed 固定 Redis 故障姿态：
//   - 读不到 generation 时不服务不可校验的条目（返回错误，而不是当命中）；
//   - 递增失败时不声称失效成功（返回错误，而不是静默 no-op）；
//   - 拿不到 generation 时不写入无法失效的条目。
func TestGenerationFailureIsFailClosed(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t, "demo.cache.fail", cacheregistry.PolicyPublic, []string{"demo.cache.tag.fail"})
	plan, err := service.PlanKey(KeyRequest{
		CacheID: "demo.cache.fail", Namespace: "demo.cache.fail.namespace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Set(ctx, plan, []byte("v1")); err != nil {
		t.Fatal(err)
	}

	service.WithGenerationStore(failingGenerations{err: errors.New("redis: connection refused")})

	if _, found, err := service.Get(ctx, plan); err == nil || found {
		t.Fatalf("unverifiable entry must not be served: found=%t err=%v", found, err)
	}
	if _, err := service.Invalidate(ctx, InvalidateRequest{
		Tags: []string{"demo.cache.tag.fail"}, Actor: "admin:1",
	}); err == nil {
		t.Fatal("invalidation must fail closed when the generation cannot advance")
	}
	if err := service.Set(ctx, plan, []byte("v2")); err == nil {
		t.Fatal("set must refuse to write an entry whose generations cannot be read")
	}
}

// TestLegacyRawEntryIsTreatedAsMiss 旧格式（裸值）条目无法判断新鲜度，必须当 miss
// 并清掉，不能因为「解不开」就把它原样返回。
func TestLegacyRawEntryIsTreatedAsMiss(t *testing.T) {
	ctx := context.Background()
	backend := cache.NewMemoryCache()
	registry := sharedTestRegistry(t, "demo.cache.legacy", cacheregistry.PolicyPublic, []string{"demo.cache.tag.legacy"})
	service, err := New(registry, backend, ProviderMemory)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanKey(KeyRequest{
		CacheID: "demo.cache.legacy", Namespace: "demo.cache.legacy.namespace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Set(ctx, plan.Key, []byte("raw-legacy-value"), time.Minute); err != nil {
		t.Fatal(err)
	}

	if _, found, err := service.Get(ctx, plan); err != nil || found {
		t.Fatalf("legacy raw entry must be a miss: found=%t err=%v", found, err)
	}
	if _, found, _ := backend.Get(ctx, plan.Key); found {
		t.Fatal("legacy raw entry must be deleted after being treated as a miss")
	}
}

// TestEntryIsRejectedWhenAnyOfItsTagsMoves 多 tag 逐项比对（P3-11 复审必改 1）。
//
// 条目带两个 tag，只递增其中一个：**必须** miss。这条钉住「全部相等才算新鲜」，
// 因为单 tag 用例下「任一相等即命中」与「全部相等」不可区分——reviewer 的计数中性
// 变异（保留 Bump，把比对改成任一 tag 相等即命中）在只有单 tag 用例时整包全绿。
//
// 变异要求：把 Get 的逐项比对改成「任一 tag 相等即命中」，本用例必须红
// （症状是服务了陈旧值：found=true）。
func TestEntryIsRejectedWhenAnyOfItsTagsMoves(t *testing.T) {
	ctx := context.Background()
	tags := []string{"demo.cache.tag.alpha", "demo.cache.tag.beta"}
	service := newTestService(t, "demo.cache.multi", cacheregistry.PolicyPublic, tags)
	plan, err := service.PlanKey(KeyRequest{
		CacheID: "demo.cache.multi", Namespace: "demo.cache.multi.namespace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tags) != 2 {
		t.Fatalf("fixture must declare two tags, got %#v", plan.Tags)
	}
	if err := service.Set(ctx, plan, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if _, found, err := service.Get(ctx, plan); err != nil || !found {
		t.Fatalf("pre-bump hit failed: found=%t err=%v", found, err)
	}

	// 只动 beta：alpha 没变。若实现写成「任一 tag 相等即命中」，这里会错误命中。
	if _, err := service.generations.Bump(ctx, []string{"demo.cache.tag.beta"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := service.Get(ctx, plan); err != nil || found {
		t.Fatalf("entry must expire when ANY of its tags moves: found=%t err=%v", found, err)
	}
}

// TestEntryIsRejectedWhenItsTagGenerationMoves 只递增 generation、不删任何 key、
// 也不经过 Invalidate：条目必须自己判定过期。这条把「条目侧 generation 校验」
// 与「本地索引删 key」分开，避免两者互相掩盖。
func TestEntryIsRejectedWhenItsTagGenerationMoves(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t, "demo.cache.move", cacheregistry.PolicyPublic, []string{"demo.cache.tag.move"})
	plan, err := service.PlanKey(KeyRequest{
		CacheID: "demo.cache.move", Namespace: "demo.cache.move.namespace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Set(ctx, plan, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if _, found, err := service.Get(ctx, plan); err != nil || !found {
		t.Fatalf("pre-bump hit failed: found=%t err=%v", found, err)
	}

	// 模拟另一个副本的失效：只递增 generation，本地索引毫不知情。
	if _, err := service.generations.Bump(ctx, []string{"demo.cache.tag.move"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := service.Get(ctx, plan); err != nil || found {
		t.Fatalf("entry must expire when its tag generation moves: found=%t err=%v", found, err)
	}
}
