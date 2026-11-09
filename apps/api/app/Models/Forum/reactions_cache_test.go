package forum

import (
	"context"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
)

// likedViewerStore 让「访客 1 赞过、其它访客没赞过」成为可复现的读模型输入。
// 它包住缓存测试 store，因此计数调用次数仍可观察缓存命中。
//
// ReactionPreviews 按访客返回不同顺序，用来证明预览不进缓存、不跨访客串味。
type likedViewerStore struct {
	*cacheTestStore
	likedViewerID int64
	likedTargets  map[int64]struct{}
	// previewCalls 记录预览查询次数：每次读都应重新查询，命中缓存也不例外。
	previewCalls int
}

func (s *likedViewerStore) ApplyReaction(context.Context, ApplyReactionRecord) (ReactionState, error) {
	return ReactionState{}, nil
}

func (s *likedViewerStore) ListReactionUsers(context.Context, string, int64, int, int) (ReactionRoster, error) {
	return ReactionRoster{Items: []UserSummary{}}, nil
}

func (s *likedViewerStore) LikedTargetIDs(_ context.Context, viewerUserID int64, _ string, _ []int64) (map[int64]struct{}, error) {
	if viewerUserID == s.likedViewerID {
		return s.likedTargets, nil
	}
	return map[int64]struct{}{}, nil
}

// ReactionPreviews 给每个访客不同的「自己优先」顺序：A 看到 [1,3]，其它看到 [5,1]。
func (s *likedViewerStore) ReactionPreviews(_ context.Context, _ string, targetIDs []int64, viewerUserID int64, _ int) (map[int64][]UserSummary, error) {
	s.previewCalls++
	previews := make(map[int64][]UserSummary, len(targetIDs))
	for _, id := range targetIDs {
		if viewerUserID == s.likedViewerID {
			previews[id] = []UserSummary{{ID: viewerUserID}, {ID: 3}}
			continue
		}
		previews[id] = []UserSummary{{ID: 5}, {ID: 1}}
	}
	return previews, nil
}

func previewIDs(items []UserSummary) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

// TestCachedTopicDetailNeverLeaksLikedByMeAcrossViewers 固定 likedByMe 的缓存
// 边界：actor 无关的详情体先落缓存，装饰发生在缓存之后；第二个访客命中同一份
// 缓存时不得看到第一个访客的点赞状态，缓存原文也不得含 likedByMe。
func TestCachedTopicDetailNeverLeaksLikedByMeAcrossViewers(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	liked := &likedViewerStore{
		cacheTestStore: inner,
		likedViewerID:  1,
		likedTargets:   map[int64]struct{}{42: {}},
	}
	memory := cache.NewMemoryCache()
	cached := NewCachedStore(liked, memory)
	service := NewService(ServiceConfig{Store: cached})
	reads := NewPublicReadService(service).WithReactions(NewReactionService(service).WithStore(liked))

	viewerA := identity.Actor{ID: 1, Status: identity.UserStatusActive}
	viewerB := identity.Actor{ID: 2, Status: identity.UserStatusActive}

	first, err := reads.GetTopicForViewer(ctx, 42, viewerA)
	if err != nil {
		t.Fatalf("viewer A read: %v", err)
	}
	if !first.LikedByMe {
		t.Fatal("viewer A should see likedByMe=true for a topic they liked")
	}
	second, err := reads.GetTopicForViewer(ctx, 42, viewerB)
	if err != nil {
		t.Fatalf("viewer B read: %v", err)
	}
	if second.LikedByMe {
		t.Fatal("viewer B must not inherit viewer A's likedByMe through the shared cache")
	}
	if inner.topicCalls != 1 {
		t.Fatalf("second read should hit cache, inner GetTopic calls=%d", inner.topicCalls)
	}

	// 预览是 per-viewer 装饰：缓存命中后每个访客仍拿到自己的顺序（self first）。
	if got := previewIDs(first.LikedBy); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("viewer A preview = %v, want [1 3] (self first)", got)
	}
	if got := previewIDs(second.LikedBy); len(got) != 2 || got[0] != 5 || got[1] != 1 {
		t.Fatalf("viewer B preview = %v, want [5 1] (own order, not viewer A's)", got)
	}
	if inner.topicCalls != 1 || liked.previewCalls != 2 {
		t.Fatalf("previews must be re-queried per read: topicCalls=%d previewCalls=%d", inner.topicCalls, liked.previewCalls)
	}

	raw, found, err := memory.Get(ctx, prefixTopicDetail+"42")
	if err != nil || !found {
		t.Fatalf("cached topic detail missing: found=%t err=%v", found, err)
	}
	if strings.Contains(string(raw), "likedByMe") {
		t.Fatalf("actor-scoped likedByMe leaked into the public cache: %s", raw)
	}
	// "likedBy" 同时覆盖 likedBy 与 likedByMe 两个键：预览永不进缓存。
	if strings.Contains(string(raw), "likedBy") {
		t.Fatalf("per-viewer reaction preview leaked into the public cache: %s", raw)
	}
}

// recordingCache 记录每一次写入的原始载荷，用来断言缓存内容本身（而不是
// 靠 key 格式猜位置）不含 per-viewer 字段。
type recordingCache struct {
	inner cache.Cache
	sets  map[string][]byte
}

func newRecordingCache() *recordingCache {
	return &recordingCache{inner: cache.NewMemoryCache(), sets: map[string][]byte{}}
}

func (c *recordingCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return c.inner.Get(ctx, key)
}

func (c *recordingCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	c.sets[key] = append([]byte(nil), value...)
	return c.inner.Set(ctx, key, value, ttl)
}

func (c *recordingCache) Delete(ctx context.Context, keys ...string) error {
	return c.inner.Delete(ctx, keys...)
}

func (c *recordingCache) Increment(ctx context.Context, key string) (int64, error) {
	return c.inner.Increment(ctx, key)
}

// TestCachedCommentListNeverCachesLikedByPreview 固定评论列表的同一缓存边界：
// 匿名读写入的缓存原文不含 likedBy（预览只在装饰阶段、按访客补），登录访客各自
// 拿到自己的预览顺序。
//
// 登录访客带软删可见性范围时会绕过共享评论缓存（CachedStore.ListComments 的
// 既有规则），所以缓存边界用匿名读验证，per-viewer 顺序用登录读验证。
func TestCachedCommentListNeverCachesLikedByPreview(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	liked := &likedViewerStore{
		cacheTestStore: inner,
		likedViewerID:  1,
		likedTargets:   map[int64]struct{}{1: {}},
	}
	recorder := newRecordingCache()
	cached := NewCachedStore(liked, recorder)
	service := NewService(ServiceConfig{Store: cached})
	reads := NewPublicReadService(service).WithReactions(NewReactionService(service).WithStore(liked))

	// 匿名两次：第二次必须命中缓存，说明缓存确实生效。
	for i := 0; i < 2; i++ {
		if _, err := reads.ListComments(ctx, CommentListInput{TopicID: 42, Viewer: identity.Actor{}}); err != nil {
			t.Fatalf("anonymous comment list #%d: %v", i, err)
		}
	}
	if inner.commentsCalls != 1 {
		t.Fatalf("second anonymous read should hit cache, inner ListComments calls=%d", inner.commentsCalls)
	}
	var payloads int
	for key, value := range recorder.sets {
		if !strings.HasPrefix(key, prefixCommentsList) {
			continue
		}
		payloads++
		if strings.Contains(string(value), "likedBy") {
			t.Fatalf("per-viewer reaction preview leaked into the public comment cache: %s", value)
		}
	}
	if payloads == 0 {
		t.Fatal("expected the comment list to be cached at least once")
	}

	// 登录访客各自拿到自己的顺序（self first），互不串味。
	viewerA := identity.Actor{ID: 1, Status: identity.UserStatusActive}
	viewerB := identity.Actor{ID: 2, Status: identity.UserStatusActive}
	first, err := reads.ListComments(ctx, CommentListInput{TopicID: 42, Viewer: viewerA})
	if err != nil {
		t.Fatalf("viewer A comment list: %v", err)
	}
	second, err := reads.ListComments(ctx, CommentListInput{TopicID: 42, Viewer: viewerB})
	if err != nil {
		t.Fatalf("viewer B comment list: %v", err)
	}
	if !first.Items[0].LikedByMe || second.Items[0].LikedByMe {
		t.Fatalf("likedByMe must stay per-viewer: A=%t B=%t", first.Items[0].LikedByMe, second.Items[0].LikedByMe)
	}
	if got := previewIDs(first.Items[0].LikedBy); len(got) != 2 || got[0] != 1 {
		t.Fatalf("viewer A comment preview = %v, want self first", got)
	}
	if got := previewIDs(second.Items[0].LikedBy); len(got) != 2 || got[0] != 5 {
		t.Fatalf("viewer B comment preview = %v, want own order", got)
	}
}
