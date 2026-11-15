package forum

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 缓存 key 直接拼外部可控的 query 片段（RV-52）：
//   - categorySlug="a:b" + tagSlug="c" 与 "a" + "b:c" 会拼出同一个 key；
//   - after 是客户端提交的 base64url(JSON) 游标，原样拼接等于让任意长输入
//     决定 Redis key 长度。
//
// 两条都必须由 key 编码本身挡住，而不是靠调用方自觉。
func TestListTopicsCacheKeySeparatesAmbiguousFilterSegments(t *testing.T) {
	cache := &keyRecordingCache{}
	cached := NewCachedStore(newCacheTestStore(), cache)
	ctx := context.Background()

	if _, err := cached.ListTopics(ctx, TopicListInput{CategorySlug: "a:b", TagSlug: "c", Sort: "latest", Page: 1, PerPage: 20}); err != nil {
		t.Fatalf("first list topics: %v", err)
	}
	if _, err := cached.ListTopics(ctx, TopicListInput{CategorySlug: "a", TagSlug: "b:c", Sort: "latest", Page: 1, PerPage: 20}); err != nil {
		t.Fatalf("second list topics: %v", err)
	}
	if len(cache.keys) != 2 {
		t.Fatalf("cache keys written = %d, want 2", len(cache.keys))
	}
	if cache.keys[0] == cache.keys[1] {
		t.Fatalf("不同过滤条件共用同一个缓存 key %q：分隔符必须被编码", cache.keys[0])
	}
}

func TestListTopicsCacheKeyBoundsClientCursor(t *testing.T) {
	cache := &keyRecordingCache{}
	cached := NewCachedStore(newCacheTestStore(), cache)
	ctx := context.Background()

	hugeCursor := strings.Repeat("x", 4096)
	if _, err := cached.ListTopics(ctx, TopicListInput{After: hugeCursor, Sort: "latest", Page: 1, PerPage: 20}); err != nil {
		t.Fatalf("list topics with long cursor: %v", err)
	}
	if len(cache.keys) != 1 {
		t.Fatalf("cache keys written = %d, want 1", len(cache.keys))
	}
	if got := len(cache.keys[0]); got > 256 {
		t.Fatalf("cache key length = %d for a %d-byte cursor；客户端输入不能决定 key 长度：%q", got, len(hugeCursor), cache.keys[0][:120])
	}
}

// 编码必须单射：旧实现把 '_' 与所有非白名单字节都折叠成 '_'，
// 于是 "a_b" 与 "a/b"、"a:b" 等互相串用同一个 key（reviewer 实测四对全等）。
func TestCacheKeyPartIsInjective(t *testing.T) {
	pairs := [][2]string{
		{"a_b", "a/b"},
		{"a:b", "a/b"},
		{"a:b", "a_b"},
		{"a+b", "a/b"},
	}
	for _, pair := range pairs {
		left := cacheKeyPart(pair[0], maxCacheSlugLen)
		right := cacheKeyPart(pair[1], maxCacheSlugLen)
		if left == right {
			t.Fatalf("%q and %q both encode to %q; the encoding is not injective", pair[0], pair[1], left)
		}
	}
}

// 不止已知四对：一组混排样本必须两两可区分。
func TestCacheKeyPartDistinguishesMixedSamples(t *testing.T) {
	samples := []string{"a_b", "a/b", "a:b", "a b", "a+b", "a.b", "a%b", "a-b", "A-b", "a_b_", "a__b", "a-"}
	seen := make(map[string]string, len(samples))
	for _, sample := range samples {
		encoded := cacheKeyPart(sample, maxCacheSlugLen)
		if previous, ok := seen[encoded]; ok {
			t.Fatalf("%q and %q collide on %q", previous, sample, encoded)
		}
		seen[encoded] = sample
	}
}

// 分片 generation key 也必须编码并受长度约束（口径纠正：不能只编码列表 key）。
func TestTopicsGenerationKeyUsesEncodedBoundedSegment(t *testing.T) {
	long := strings.Repeat("x", 4096)
	if got := len(topicsGenCategory(long)); got > 200 {
		t.Fatalf("generation key length = %d for a 4096-byte slug; the encoded segment must bound it", got)
	}
	if topicsGenCategory("a_b") == topicsGenCategory("a/b") {
		t.Fatal("generation keys for a_b and a/b collide")
	}
}

// 评论列表 key 同样含 view 与 after：view 是枚举、after 是客户端游标。
func TestListCommentsCacheKeyBoundsClientCursor(t *testing.T) {
	cache := &keyRecordingCache{}
	cached := NewCachedStore(newCacheTestStore(), cache)
	ctx := context.Background()

	if _, err := cached.ListComments(ctx, CommentListInput{TopicID: 7, View: "flat", Page: 1, PerPage: 20, After: strings.Repeat("y", 4096)}); err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(cache.keys) != 1 {
		t.Fatalf("cache keys written = %d, want 1", len(cache.keys))
	}
	if got := len(cache.keys[0]); got > 256 {
		t.Fatalf("comment cache key length = %d for a 4096-byte cursor", got)
	}
}

// keyRecordingCache 只记录写入的 key；读永远 miss，避免干扰首轮写入。
type keyRecordingCache struct {
	keys []string
}

func (c *keyRecordingCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}

func (c *keyRecordingCache) Set(_ context.Context, key string, _ []byte, _ time.Duration) error {
	c.keys = append(c.keys, key)
	return nil
}

func (c *keyRecordingCache) Delete(context.Context, ...string) error { return nil }

func (c *keyRecordingCache) Increment(context.Context, string) (int64, error) { return 0, nil }
