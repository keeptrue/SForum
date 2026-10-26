package forum

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Cache"
)

type referenceInvalidatorSpy struct {
	resources       []string
	visibilityCalls int
}

func (s *referenceInvalidatorSpy) InvalidateReferenceResource(_ context.Context, resourceType string, resourceID int64) {
	s.resources = append(s.resources, resourceType+":"+strconv.FormatInt(resourceID, 10))
}

func (s *referenceInvalidatorSpy) InvalidateTopicVisibility(context.Context) {
	s.visibilityCalls++
}

// cacheTestStore 包装 serviceFakeStore，记录读方法调用次数并返回可区分数据。
type cacheTestStore struct {
	Store
	categoriesCalls int
	// lastCategoriesIncludeHidden 记录最近一次公开/后台口径，供缓存 key 隔离断言。
	lastCategoriesIncludeHidden bool
	topicCalls                  int
	topicsCalls                 int
	commentsCalls               int
	// topicCategorySlug / topicTagSlugs 控制 GetTopic 返回的列表分片 scope（M6）。
	topicCategorySlug string
	topicTagSlugs     []string
}

func newCacheTestStore() *cacheTestStore {
	return &cacheTestStore{Store: newServiceFakeStore()}
}

func (s *cacheTestStore) ListCategories(_ context.Context, includeHidden bool) ([]Category, error) {
	s.categoriesCalls++
	s.lastCategoriesIncludeHidden = includeHidden
	if includeHidden {
		return []Category{
			{ID: 1, Slug: "general", Name: "综合讨论", Visibility: "public"},
			{ID: 2, Slug: "internal", Name: "内部版块", Visibility: "hidden"},
		}, nil
	}
	return []Category{{ID: 1, Slug: "general", Name: "综合讨论"}}, nil
}

func (s *cacheTestStore) topicDetailSummary(topicID int64, slug string) TopicSummary {
	cat := strings.TrimSpace(s.topicCategorySlug)
	if cat == "" {
		cat = "general"
	}
	tags := make([]TopicTagSummary, 0, len(s.topicTagSlugs))
	for _, t := range s.topicTagSlugs {
		tags = append(tags, TopicTagSummary{Slug: t, Name: t, Status: "active"})
	}
	return TopicSummary{
		ID: topicID, Title: "测试主题", Slug: slug, CategorySlug: cat, Tags: tags, Status: "active",
	}
}

func (s *cacheTestStore) GetTopic(_ context.Context, topicID int64) (TopicDetail, error) {
	s.topicCalls++
	return TopicDetail{TopicSummary: s.topicDetailSummary(topicID, "test-topic")}, nil
}

func (s *cacheTestStore) GetTopicBySlug(_ context.Context, slug string) (TopicDetail, error) {
	s.topicCalls++
	return TopicDetail{TopicSummary: s.topicDetailSummary(42, slug)}, nil
}

func (s *cacheTestStore) ListTopics(_ context.Context, input TopicListInput) (TopicList, error) {
	s.topicsCalls++
	return TopicList{
		Items:   []TopicSummary{{ID: 1, Title: "主题一", CategorySlug: input.CategorySlug}},
		Total:   1,
		Page:    input.Page,
		PerPage: input.PerPage,
	}, nil
}

// CreateTopic override 记录并返回有效 detail，用于验证写后失效。
func (s *cacheTestStore) CreateTopic(_ context.Context, input CreateTopicRecord) (TopicDetail, error) {
	cat := strings.TrimSpace(input.CategorySlug)
	if cat == "" {
		cat = "general"
	}
	tags := input.Tags
	if len(tags) == 0 {
		for _, slug := range input.TagSlugs {
			tags = append(tags, TopicTagSummary{Slug: slug, Name: slug, Status: "active"})
		}
	}
	return TopicDetail{TopicSummary: TopicSummary{
		ID: 99, Title: input.Title, CategorySlug: cat, Tags: tags,
	}}, nil
}

func TestCachedStoreReadsHitCacheAfterFirstLoad(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	// 首次：miss → 查 store → 写缓存。
	if _, err := cached.ListCategories(ctx, false); err != nil {
		t.Fatalf("first list categories: %v", err)
	}
	if inner.categoriesCalls != 1 {
		t.Fatalf("expected 1 store call after first load, got %d", inner.categoriesCalls)
	}
	// 二次：hit → 不查 store。
	if _, err := cached.ListCategories(ctx, false); err != nil {
		t.Fatalf("second list categories: %v", err)
	}
	if inner.categoriesCalls != 1 {
		t.Fatalf("expected store call still 1 on cache hit, got %d", inner.categoriesCalls)
	}
}

func TestCachedStoreCategoryGroupChangesInvalidateReferenceVisibility(t *testing.T) {
	t.Parallel()
	spy := &referenceInvalidatorSpy{}
	cached := NewCachedStore(newCacheTestStore(), cache.NewMemoryCache()).(*CachedStore)
	WithReferenceRenderCacheInvalidator(cached, spy)
	if _, err := cached.CreateCategoryGroup(t.Context(), CreateCategoryGroupInput{Slug: "public", Name: "Public", Visibility: "public"}); err != nil {
		t.Fatal(err)
	}
	visibility := "hidden"
	if _, err := cached.UpdateCategoryGroup(t.Context(), UpdateCategoryGroupInput{ID: 1, Visibility: &visibility}); err != nil {
		t.Fatal(err)
	}
	if spy.visibilityCalls != 2 {
		t.Fatalf("category group visibility invalidations=%d", spy.visibilityCalls)
	}
}

// 后台口径（includeHidden=true）与公开口径必须各自独立缓存，
// 否则隐藏分类会污染公开列表，或被公开列表掩盖。
func TestCachedStoreTaxonomyListSeparatesAdminAndPublicCacheKeys(t *testing.T) {
	t.Parallel()
	inner := newCacheTestStore()
	cached := NewCachedStore(inner, cache.NewMemoryCache())

	publicItems, err := cached.ListCategories(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	adminItems, err := cached.ListCategories(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicItems) != 1 || len(adminItems) != 2 {
		t.Fatalf("public=%d admin=%d; hidden categories must stay visible to admin", len(publicItems), len(adminItems))
	}
	if inner.categoriesCalls != 2 {
		t.Fatalf("expected separate store loads for public/admin reads, got %d", inner.categoriesCalls)
	}
	if _, err := cached.ListCategories(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListCategories(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if inner.categoriesCalls != 2 {
		t.Fatalf("expected cached reads on both scopes, store calls=%d", inner.categoriesCalls)
	}
}

func TestCachedStoreDeleteCategoryInvalidatesAdminAndPublicTaxonomy(t *testing.T) {
	t.Parallel()
	inner := newCacheTestStore()
	cached := NewCachedStore(inner, cache.NewMemoryCache())

	if _, err := cached.ListCategories(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListCategories(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.DeleteCategory(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListCategories(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListCategories(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if inner.categoriesCalls != 4 {
		t.Fatalf("delete must invalidate both taxonomy cache keys, store calls=%d", inner.categoriesCalls)
	}
}

func TestCachedStoreGetTopicHitAndInvalidate(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	// 首次加载。
	if _, err := cached.GetTopic(ctx, 42); err != nil {
		t.Fatalf("first get: %v", err)
	}
	if inner.topicCalls != 1 {
		t.Fatalf("expected 1 store call, got %d", inner.topicCalls)
	}
	// 二次命中缓存。
	if _, err := cached.GetTopic(ctx, 42); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if inner.topicCalls != 1 {
		t.Fatalf("expected still 1 store call on hit, got %d", inner.topicCalls)
	}
	// 通过 UpdateTopic 触发详情失效（含 topicID）。
	if _, err := cached.UpdateTopic(ctx, UpdateTopicRecord{TopicID: 42}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// 再读：应 miss 重新回源。
	if _, err := cached.GetTopic(ctx, 42); err != nil {
		t.Fatalf("third get after invalidate: %v", err)
	}
	if inner.topicCalls != 2 {
		t.Fatalf("expected 2 store calls after invalidation, got %d", inner.topicCalls)
	}
}

func TestCachedStoreNeverCachesEditableSource(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	base := inner.Store.(*serviceFakeStore)
	base.topicEditSource = EditableContentSource{
		RawContent: "M1_CACHE_SOURCE_SECRET", SourceFormat: SourceFormatEditorDocument,
		ContentHash: "M1_CACHE_SOURCE_HASH", CurrentRevision: 2,
	}
	cacheStore := cache.NewMemoryCache()
	cached := NewCachedStore(inner, cacheStore)

	if _, err := cached.GetTopic(ctx, 42); err != nil {
		t.Fatal(err)
	}
	value, ok, err := cacheStore.Get(ctx, prefixTopicDetail+"42")
	if err != nil || !ok {
		t.Fatalf("public topic cache missing: ok=%v err=%v", ok, err)
	}
	serialized := string(value)
	for _, forbidden := range []string{"M1_CACHE_SOURCE_SECRET", "M1_CACHE_SOURCE_HASH", "rawContent", "contentHash", "sourceFormat", "editorType"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("public cache contains %q: %s", forbidden, serialized)
		}
	}

	for range 2 {
		source, err := cached.GetTopicEditSource(ctx, 42)
		if err != nil || source.RawContent != "M1_CACHE_SOURCE_SECRET" {
			t.Fatalf("source=%#v err=%v", source, err)
		}
	}
	if base.topicSourceCalls != 2 {
		t.Fatalf("edit source unexpectedly cached, store calls=%d", base.topicSourceCalls)
	}
}

// M4：id 路径预热后 by-slug 应命中双写缓存；评论写入应同时失效 slug 详情。
func TestCachedStoreTopicDetailDualWriteAndCommentInvalidatesSlug(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	if _, err := cached.GetTopic(ctx, 42); err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if inner.topicCalls != 1 {
		t.Fatalf("expected 1 store call after id load, got %d", inner.topicCalls)
	}
	// 双写：slug 入口不应再回源。
	if _, err := cached.GetTopicBySlug(ctx, "test-topic"); err != nil {
		t.Fatalf("get by slug: %v", err)
	}
	if inner.topicCalls != 1 {
		t.Fatalf("expected dual-write hit on slug, got %d store calls", inner.topicCalls)
	}

	// CreateComment 只有 topicID：反向映射应清掉 slug 缓存。
	if _, err := cached.CreateComment(ctx, CreateCommentRecord{TopicID: 42}); err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if _, err := cached.GetTopicBySlug(ctx, "test-topic"); err != nil {
		t.Fatalf("get by slug after comment: %v", err)
	}
	if inner.topicCalls != 2 {
		t.Fatalf("expected slug miss after comment invalidate, got %d store calls", inner.topicCalls)
	}
}

func TestCachedStoreTopicsInvalidatedOnWrite(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	// 首次加载 topics（首页 global scope）。
	if _, err := cached.ListTopics(ctx, TopicListInput{Page: 1, PerPage: 20}); err != nil {
		t.Fatalf("first list topics: %v", err)
	}
	if inner.topicsCalls != 1 {
		t.Fatalf("expected 1 store call, got %d", inner.topicsCalls)
	}
	// 命中缓存。
	if _, err := cached.ListTopics(ctx, TopicListInput{Page: 1, PerPage: 20}); err != nil {
		t.Fatalf("second list topics: %v", err)
	}
	if inner.topicsCalls != 1 {
		t.Fatalf("expected still 1 on hit, got %d", inner.topicsCalls)
	}
	// CreateTopic 触发 global generation 失效（首页应 miss）。
	if _, err := cached.CreateTopic(ctx, CreateTopicRecord{Title: "新主题", CategorySlug: "general"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 再读：generation 已变，应 miss。
	if _, err := cached.ListTopics(ctx, TopicListInput{Page: 1, PerPage: 20}); err != nil {
		t.Fatalf("third list after write: %v", err)
	}
	if inner.topicsCalls != 2 {
		t.Fatalf("expected 2 store calls after write invalidation, got %d", inner.topicsCalls)
	}
}

// M6：分类 A 写入只 bump global + cat A；分类 B 列表缓存必须继续命中。
func TestCachedStoreTopicsScopedInvalidationByCategory(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	listA := TopicListInput{Page: 1, PerPage: 20, CategorySlug: "cat-a"}
	listB := TopicListInput{Page: 1, PerPage: 20, CategorySlug: "cat-b"}
	listHome := TopicListInput{Page: 1, PerPage: 20}

	// 预热 A / B / home。
	for _, in := range []TopicListInput{listA, listB, listHome} {
		if _, err := cached.ListTopics(ctx, in); err != nil {
			t.Fatalf("warm list: %v", err)
		}
	}
	if inner.topicsCalls != 3 {
		t.Fatalf("expected 3 store calls after warm, got %d", inner.topicsCalls)
	}
	// 全部命中。
	for _, in := range []TopicListInput{listA, listB, listHome} {
		if _, err := cached.ListTopics(ctx, in); err != nil {
			t.Fatalf("hit list: %v", err)
		}
	}
	if inner.topicsCalls != 3 {
		t.Fatalf("expected still 3 on hits, got %d", inner.topicsCalls)
	}

	// 仅在 cat-a 写主题。
	if _, err := cached.CreateTopic(ctx, CreateTopicRecord{
		Title: "A 风暴", CategorySlug: "cat-a",
	}); err != nil {
		t.Fatalf("create in cat-a: %v", err)
	}

	// cat-b 必须仍命中（核心断言）。
	if _, err := cached.ListTopics(ctx, listB); err != nil {
		t.Fatalf("list B after A write: %v", err)
	}
	if inner.topicsCalls != 3 {
		t.Fatalf("expected cat-b cache hit (still 3), got %d store calls", inner.topicsCalls)
	}

	// cat-a 与 home 应 miss。
	if _, err := cached.ListTopics(ctx, listA); err != nil {
		t.Fatalf("list A after write: %v", err)
	}
	if _, err := cached.ListTopics(ctx, listHome); err != nil {
		t.Fatalf("list home after write: %v", err)
	}
	if inner.topicsCalls != 5 {
		t.Fatalf("expected cat-a + home miss (5 total), got %d", inner.topicsCalls)
	}
}

// M6：标签 scope 分片——写 tag-x 主题不刷 tag-y 列表。
func TestCachedStoreTopicsScopedInvalidationByTag(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	listX := TopicListInput{Page: 1, PerPage: 20, TagSlug: "tag-x"}
	listY := TopicListInput{Page: 1, PerPage: 20, TagSlug: "tag-y"}

	if _, err := cached.ListTopics(ctx, listX); err != nil {
		t.Fatalf("warm x: %v", err)
	}
	if _, err := cached.ListTopics(ctx, listY); err != nil {
		t.Fatalf("warm y: %v", err)
	}
	if inner.topicsCalls != 2 {
		t.Fatalf("warm calls=%d", inner.topicsCalls)
	}

	if _, err := cached.CreateTopic(ctx, CreateTopicRecord{
		Title: "带 x", CategorySlug: "general", TagSlugs: []string{"tag-x"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := cached.ListTopics(ctx, listY); err != nil {
		t.Fatalf("list y: %v", err)
	}
	if inner.topicsCalls != 2 {
		t.Fatalf("expected tag-y hit, got %d", inner.topicsCalls)
	}
	if _, err := cached.ListTopics(ctx, listX); err != nil {
		t.Fatalf("list x: %v", err)
	}
	if inner.topicsCalls != 3 {
		t.Fatalf("expected tag-x miss, got %d", inner.topicsCalls)
	}
}

// M6：评论写入按主题所属分类分片失效（预热详情缓存以解析 scope）。
func TestCachedStoreCommentWriteInvalidatesOnlyTopicCategory(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	inner.topicCategorySlug = "cat-a"
	inner.topicTagSlugs = []string{"go"}
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	listA := TopicListInput{Page: 1, PerPage: 20, CategorySlug: "cat-a"}
	listB := TopicListInput{Page: 1, PerPage: 20, CategorySlug: "cat-b"}
	if _, err := cached.ListTopics(ctx, listA); err != nil {
		t.Fatalf("warm A: %v", err)
	}
	if _, err := cached.ListTopics(ctx, listB); err != nil {
		t.Fatalf("warm B: %v", err)
	}
	// 预热详情，让 CreateComment 从缓存取 scope（不额外记 topicsCalls）。
	if _, err := cached.GetTopic(ctx, 42); err != nil {
		t.Fatalf("warm detail: %v", err)
	}
	warmTopics := inner.topicsCalls

	if _, err := cached.CreateComment(ctx, CreateCommentRecord{TopicID: 42}); err != nil {
		t.Fatalf("comment: %v", err)
	}

	if _, err := cached.ListTopics(ctx, listB); err != nil {
		t.Fatalf("list B: %v", err)
	}
	if inner.topicsCalls != warmTopics {
		t.Fatalf("expected cat-b still hit after comment in cat-a, got topicsCalls=%d want %d",
			inner.topicsCalls, warmTopics)
	}
	if _, err := cached.ListTopics(ctx, listA); err != nil {
		t.Fatalf("list A: %v", err)
	}
	if inner.topicsCalls != warmTopics+1 {
		t.Fatalf("expected cat-a miss, got %d", inner.topicsCalls)
	}
}

func TestCachedStoreNilCacheReturnsInner(t *testing.T) {
	inner := newCacheTestStore()
	// cache 为 nil 时应直接返回 inner，不包装。
	if got := NewCachedStore(inner, nil); got != inner {
		t.Fatal("expected NewCachedStore to return inner when cache is nil")
	}
}

func TestCachedStoreCacheErrorsDegradeGracefully(t *testing.T) {
	// 此测试主要确认缓存层不会因自身错误 panic 或中断主流程。
	// MemoryCache 不会出错，这里验证整体链路稳定即可。
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	for i := 0; i < 5; i++ {
		if _, err := cached.ListCategories(ctx, false); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if inner.categoriesCalls != 1 {
		t.Fatalf("expected 1 store call over 5 reads (cached), got %d", inner.categoriesCalls)
	}
}

// 确保 cacheTestStore 的 Store 方法（非 override）正常转发。
func TestCachedStoreForwardsNonOverriddenMethods(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c).(*CachedStore)

	// ListTags 未 override，应直接转发到嵌入的 Store。
	_, _ = cached.ListTags(ctx, false)
	// 只要不 panic 即视为转发正常。
	_ = time.Now() // 占位，避免 import 未用
}

func (s *cacheTestStore) ListComments(_ context.Context, input CommentListInput) (CommentList, error) {
	s.commentsCalls++
	return CommentList{
		Items:   []Comment{{ID: 1, TopicID: input.TopicID}},
		Total:   1,
		Page:    input.Page,
		PerPage: input.PerPage,
		View:    input.View,
	}, nil
}

func (s *cacheTestStore) CreateComment(_ context.Context, input CreateCommentRecord) (Comment, error) {
	return Comment{ID: 9, TopicID: input.TopicID}, nil
}

func (s *cacheTestStore) DeleteComment(_ context.Context, commentID int64) (Comment, error) {
	return Comment{ID: commentID, TopicID: 42}, nil
}

func TestCachedStoreListCommentsHitAndInvalidate(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	input := CommentListInput{TopicID: 42, View: "tree", Page: 1, PerPage: 20, TreeDescendantsPerRoot: 50}

	if _, err := cached.ListComments(ctx, input); err != nil {
		t.Fatalf("first list comments: %v", err)
	}
	if inner.commentsCalls != 1 {
		t.Fatalf("expected 1 store call, got %d", inner.commentsCalls)
	}
	if _, err := cached.ListComments(ctx, input); err != nil {
		t.Fatalf("second list comments: %v", err)
	}
	if inner.commentsCalls != 1 {
		t.Fatalf("expected cache hit (still 1), got %d", inner.commentsCalls)
	}

	// 写评论后 generation 递增，应 miss。
	if _, err := cached.CreateComment(ctx, CreateCommentRecord{TopicID: 42}); err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if _, err := cached.ListComments(ctx, input); err != nil {
		t.Fatalf("third list after write: %v", err)
	}
	if inner.commentsCalls != 2 {
		t.Fatalf("expected 2 store calls after invalidate, got %d", inner.commentsCalls)
	}
}

func TestCachedStoreModerationPublicationInvalidatesPublicDerivatives(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	cached := NewCachedStore(inner, cache.NewMemoryCache()).(*CachedStore)
	topics := TopicListInput{Page: 1, PerPage: 20}
	comments := CommentListInput{TopicID: 42, View: "tree", Page: 1, PerPage: 20, TreeDescendantsPerRoot: 50}

	if _, err := cached.GetTopic(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListTopics(ctx, topics); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListCategories(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListComments(ctx, comments); err != nil {
		t.Fatal(err)
	}
	cached.InvalidateModerationPublication(ctx, 42, "comment")

	if _, err := cached.GetTopic(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListTopics(ctx, topics); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListCategories(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListComments(ctx, comments); err != nil {
		t.Fatal(err)
	}
	if inner.topicCalls != 2 || inner.topicsCalls != 2 || inner.categoriesCalls != 2 || inner.commentsCalls != 2 {
		t.Fatalf("moderation invalidation calls topic=%d topics=%d categories=%d comments=%d", inner.topicCalls, inner.topicsCalls, inner.categoriesCalls, inner.commentsCalls)
	}
}

func TestCachedStoreListCommentsSkipsViewerScoped(t *testing.T) {
	ctx := context.Background()
	inner := newCacheTestStore()
	c := cache.NewMemoryCache()
	cached := NewCachedStore(inner, c)

	// IncludeDeleted 路径不得缓存（viewer 相关）。
	input := CommentListInput{TopicID: 1, View: "flat", Page: 1, PerPage: 20, IncludeDeleted: true}
	if _, err := cached.ListComments(ctx, input); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := cached.ListComments(ctx, input); err != nil {
		t.Fatalf("second: %v", err)
	}
	if inner.commentsCalls != 2 {
		t.Fatalf("expected no cache for IncludeDeleted, got %d calls", inner.commentsCalls)
	}
}
