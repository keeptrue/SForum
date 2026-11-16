package cachepolicy

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
)

const (
	// generationKeyPrefix 与 forum 缓存（forum:gen:*）同族，但不共用键空间：
	// 两个缓存体系的失效节奏互不相干。
	generationKeyPrefix = "sforum:cachepolicy:gen:"
	// generationTTL 远长于条目 TTL 上限（MaxTTL = 24h），又不会永久驻留。
	// generation 过期等价于「回到首代」：条目里记的旧值会比 0 大 → 全部 miss，
	// 只会多一次回源，不会把旧数据当成新鲜数据。
	generationTTL = 7 * 24 * time.Hour
)

// GenerationStore 是 tag 失效的**权威**：每个 tag 一个单调递增的 generation。
//
// 语义：
//   - 条目写入时记下当时各 tag 的 generation；
//   - 读取时与当前值比对，任何一项不等即视为 miss；
//   - 失效 = 递增该 tag 的 generation。
//
// 权威必须在进程之外（生产实现是 Redis），否则多副本/蓝绿发布下 A 副本的失效
// 对 B 副本完全不可见——这正是 P3-11 的原缺陷（进程内 tagIndex）。
type GenerationStore interface {
	// Current 返回各 tag 的当前 generation；从未失效过的 tag 为 0。
	Current(ctx context.Context, tags []string) (map[string]uint64, error)
	// Bump 递增各 tag 的 generation 并返回新值。
	Bump(ctx context.Context, tags []string) (map[string]uint64, error)
}

// CacheGenerations 用 Host Cache 抽象实现 generation 存储。
//
// 底层是 RedisCache 时 Get/Increment 天然跨进程；底层是 MemoryCache 时只在本进程
// 有效——这与 ProviderMemory 的单进程语义一致，不要用它承载多副本部署。
type CacheGenerations struct {
	backend cache.Cache
}

func NewCacheGenerations(backend cache.Cache) *CacheGenerations {
	return &CacheGenerations{backend: backend}
}

func (g *CacheGenerations) Current(ctx context.Context, tags []string) (map[string]uint64, error) {
	out := make(map[string]uint64, len(tags))
	for _, tag := range normalizeTags(tags) {
		raw, found, err := g.backend.Get(ctx, generationKey(tag))
		if err != nil {
			return nil, fmt.Errorf("read cache generation %q: %w", tag, err)
		}
		if !found {
			out[tag] = 0
			continue
		}
		value, parseErr := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if parseErr != nil {
			// 坏值按首代处理：宁可多一次回源，也不让一个坏计数器永久挡住缓存。
			out[tag] = 0
			continue
		}
		out[tag] = value
	}
	return out, nil
}

func (g *CacheGenerations) Bump(ctx context.Context, tags []string) (map[string]uint64, error) {
	out := make(map[string]uint64, len(tags))
	for _, tag := range normalizeTags(tags) {
		key := generationKey(tag)
		var (
			next int64
			err  error
		)
		if atomic, ok := g.backend.(cache.TTLIncrementer); ok {
			// 原子自增 + 续期一次完成：Get→Set 的读-改-写会让并发失效算出同一个
			// 版本号，等于丢一次失效（与 forum.bumpGen 的取舍一致）。
			next, err = atomic.IncrementWithTTL(ctx, key, generationTTL)
		} else {
			next, err = g.backend.Increment(ctx, key)
		}
		if err != nil {
			return nil, fmt.Errorf("bump cache generation %q: %w", tag, err)
		}
		out[tag] = uint64(next)
	}
	return out, nil
}

func generationKey(tag string) string {
	return generationKeyPrefix + tag
}

// normalizeTags 去空白、去重、排序：generation 读写必须对同一批 tag 稳定。
func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}
