package ai

import (
	"context"
	"encoding/json"
	"time"

	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
)

// 结果缓存的生产实现：把 cache.Cache（Redis）接到网关的 ResultCache 面上。
//
// 为什么不复用 Cache Registry：那是给插件用的能力面（命名空间、准入、租约、trace、
// 失效代际）。AI 结果缓存是宿主内部的派生数据——按精确 CacheKey 读写、短 TTL、可
// 重建——把它塞进插件治理表只会让两种寿命与两种权限混在一处。CacheKey 里已经带了
// 站点配置 revision，因此运营者改配置就会自然换键，不需要任何显式失效。
//
// 缓存永远是优化，不是正确性依赖：Redis 抖动时读一律 miss、写一律放弃。反过来
// （因为缓存不可用而拒绝一次补全）才是不可接受的。
const (
	// resultCacheKeyPrefix 与其它缓存分开命名空间，避免与业务读缓存的键空间重叠。
	resultCacheKeyPrefix = "ai:completion:"
	// resultCacheVersion 是缓存载荷自身的形状版本。CompletionResult 形状变更时必须
	// 递增，否则旧载荷会被反序列化成「看起来合理但缺字段」的结果。
	//
	// 2：Text 不再包含推理正文（此前 reasoning_content / thinking 会被提升成回答）。
	// 不换版本的话，升级后 10 分钟内仍会命中把思维链当成答案的旧载荷——修复会被
	// 缓存挡住，表现为「改了代码但还在泄露」。
	resultCacheVersion = 2
)

type resultCacheEnvelope struct {
	Version int              `json:"version"`
	Result  CompletionResult `json:"result"`
}

type storeResultCache struct {
	store cache.Cache
}

// NewResultCache 在给定的缓存实现上构造结果缓存。store 为 nil 时返回 nil，
// 网关据此把缓存当作未启用（与 GatewayConfig.Cache 的既有语义一致）。
func NewResultCache(store cache.Cache) ResultCache {
	if store == nil {
		return nil
	}
	return &storeResultCache{store: store}
}

func (c *storeResultCache) Get(ctx context.Context, key string) (CompletionResult, bool) {
	if c == nil || c.store == nil {
		return CompletionResult{}, false
	}
	payload, found, err := c.store.Get(ctx, resultCacheKeyPrefix+key)
	if err != nil || !found {
		return CompletionResult{}, false
	}
	var envelope resultCacheEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Version != resultCacheVersion {
		return CompletionResult{}, false
	}
	return envelope.Result, true
}

func (c *storeResultCache) Put(ctx context.Context, key string, result CompletionResult, ttl time.Duration) {
	if c == nil || c.store == nil || ttl <= 0 {
		return
	}
	payload, err := json.Marshal(resultCacheEnvelope{Version: resultCacheVersion, Result: result})
	if err != nil {
		return
	}
	_ = c.store.Set(ctx, resultCacheKeyPrefix+key, payload, ttl)
}
