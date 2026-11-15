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
