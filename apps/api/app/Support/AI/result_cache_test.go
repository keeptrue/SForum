package ai_test

import (
	"context"
	"errors"
	"testing"
	"time"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
)

type failingCache struct{ cache.Cache }

func (failingCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, errors.New("redis is down")
}

func (failingCache) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("redis is down")
}

func TestResultCacheIsDisabledWithoutAStore(t *testing.T) {
	if ai.NewResultCache(nil) != nil {
		t.Fatal("a nil store must produce no cache, not a cache that always misses silently")
	}
}

func TestResultCacheRoundTripsThroughTheStore(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemoryCache()
	resultCache := ai.NewResultCache(store)
	if resultCache == nil {
		t.Fatal("cache must be constructed")
	}
	want := ai.CompletionResult{
		Text: "hello", StopReason: ai.StopReasonEnd,
		Usage:    ai.Usage{InputTokens: 11, OutputTokens: 4},
		Provider: ai.ProviderArtifact{Protocol: "openai-chat", Model: "deepseek-chat"},
	}
	resultCache.Put(ctx, "key-1", want, time.Minute)

	got, ok := resultCache.Get(ctx, "key-1")
	if !ok || got.Text != want.Text || got.Usage.InputTokens != 11 || got.Provider.Model != "deepseek-chat" {
		t.Fatalf("cached result = %#v (ok=%v)", got, ok)
	}
	if _, ok := resultCache.Get(ctx, "key-2"); ok {
		t.Fatal("unknown key must miss")
	}
	// 缓存键要有自己的命名空间：业务读缓存与 AI 结果缓存共用同一个 Redis。
	if _, found, err := store.Get(ctx, "ai:completion:key-1"); err != nil || !found {
		t.Fatalf("cache entry is not namespaced: found=%v err=%v", found, err)
	}
}

func TestResultCacheHonoursTTLAndIgnoresNonPositiveTTL(t *testing.T) {
	ctx := context.Background()
	resultCache := ai.NewResultCache(cache.NewMemoryCache())
	resultCache.Put(ctx, "short", ai.CompletionResult{Text: "x"}, time.Millisecond)
	if _, ok := resultCache.Get(ctx, "short"); !ok {
		t.Fatal("entry should be readable before the TTL elapses")
	}
	time.Sleep(5 * time.Millisecond)
	if _, ok := resultCache.Get(ctx, "short"); ok {
		t.Fatal("expired entry must miss")
	}
	// TTL 非正表示「不要缓存」：写入会被放弃，而不是变成永不过期的条目。
	resultCache.Put(ctx, "forever", ai.CompletionResult{Text: "y"}, 0)
	if _, ok := resultCache.Get(ctx, "forever"); ok {
		t.Fatal("non-positive TTL must not store anything")
	}
}

func TestResultCacheTreatsCorruptOrVersionDriftedPayloadsAsMisses(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemoryCache()
	resultCache := ai.NewResultCache(store)
	if err := store.Set(ctx, "ai:completion:corrupt", []byte("{not json"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, ok := resultCache.Get(ctx, "corrupt"); ok {
		t.Fatal("corrupt payload must miss")
	}
	if err := store.Set(ctx, "ai:completion:old", []byte(`{"version":0,"result":{"text":"stale"}}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, ok := resultCache.Get(ctx, "old"); ok {
		t.Fatal("a payload written by an older contract shape must miss, not be trusted")
	}
}

// 缓存是优化而不是正确性依赖：底层不可用时读写都必须退化为「没有缓存」。
func TestResultCacheDegradesSilentlyWhenTheStoreFails(t *testing.T) {
	ctx := context.Background()
	resultCache := ai.NewResultCache(failingCache{})
	resultCache.Put(ctx, "key", ai.CompletionResult{Text: "x"}, time.Minute)
	if _, ok := resultCache.Get(ctx, "key"); ok {
		t.Fatal("a failing store must report a miss")
	}
}

func TestGatewayServesRepeatedIdenticalCallsFromTheResultCache(t *testing.T) {
	settings := ai.RecommendedSettings()
	settings.Enabled = true
	profile := settings.Profiles[0]
	profile.Enabled = true
	settings.Profiles[0] = profile
	invoker := &fakeInvoker{response: ai.WireResponse{Text: "first answer", StopReason: ai.StopReasonEnd}}
	traces := &fakeTraces{}
	usage := &fakeUsage{}
	gateway := ai.NewGateway(ai.GatewayConfig{
		Settings: &fakeSettings{settings: settings},
		Usage:    usage,
		Traces:   traces,
		Creds:    &fakeCreds{value: "sk-test"},
		Invoker:  invoker,
		Cache:    ai.NewResultCache(cache.NewMemoryCache()),
	})
	request := ai.CompletionRequest{
		Purpose: "fixture.cache", CostClass: ai.CostClassEconomy, MaxTokens: 64,
		Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Type: ai.PartText, Text: "same question"}}}},
		Metadata: ai.Metadata{CallerExtensionID: "fixture.ai"},
	}
	first, err := gateway.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if first.CacheHit || first.Text != "first answer" {
		t.Fatalf("first result = %#v", first)
	}
	second, err := gateway.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if !second.CacheHit || second.Text != "first answer" {
		t.Fatalf("second result must come from the cache with cacheHit set: %#v", second)
	}
	if invoker.calls != 1 {
		t.Fatalf("identical requests must not pay twice, invoker calls = %d", invoker.calls)
	}
	// 命中不产生新的用量：缓存命中不该消耗额度，也不该产生供应商花费。
	if len(usage.records) != 1 {
		t.Fatalf("cache hit recorded usage: %#v", usage.records)
	}
	hits := 0
	for _, record := range traces.records {
		if record.CacheHit {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("cache hits in traces = %d", hits)
	}
}
