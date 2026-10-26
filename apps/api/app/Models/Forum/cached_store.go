package forum

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

// CachedStore 通过嵌入 Store 接口装饰底层 store：
//   - 读方法（ListCategories/ListCategoryGroups/ListTags/GetTopic/ListTopics）走短 TTL 缓存。
//   - 写方法在成功后使相关缓存失效（generation 递增 + 精确 key 删除）。
//
// 失效策略采用 generation 版本号，避免对前缀 key 做 SCAN：
//   - 主题列表 generation 按 scope 分片（M6）：global / cat:{slug} / tag:{slug}。
//     单分类写风暴只 bump 首页 gen + 该分类（及标签）gen，其它分类列表缓存保持命中。
//   - 分类/分组/标签列表同理（taxonomy 仍用全局 gen）。
//   - 主题详情按 topicID 精确删除（创建/更新/删除/状态变更时）。
//   - 评论列表按 topicID generation 分片。
//
// cache 为 nil 时退化为透传（不缓存），方便测试和显式关闭。
type CachedStore struct {
	Store
	cache                cache.Cache
	referenceInvalidator ReferenceRenderCacheInvalidator
}

// 生成 generation key 时使用的固定前缀。
const (
	// 主题列表分片 gen（M6）。旧单一 key forum:gen:topics 已废弃，不再读写。
	genTopicsGlobal   = "forum:gen:topics:global" // 无过滤首页
	genTopicsCatPref  = "forum:gen:topics:cat:"   // + categorySlug
	genTopicsTagPref  = "forum:gen:topics:tag:"   // + tagSlug
	genCategories     = "forum:gen:cats"          // 分类列表 generation
	genCategoryGroups = "forum:gen:groups"        // 分类分组列表 generation
	genTags           = "forum:gen:tags"          // 标签列表 generation
	// genCommentsPrefix + topicID：按主题递增，写评论只失效该主题评论列表。
	genCommentsPrefix = "forum:gen:comments:"

	// 缓存 key 前缀。
	// v3：TopicDetail 增加 editedAt 字段后换前缀，避免旧缓存缺字段。
	prefixTopicDetail = "forum:topic:v3:"
	prefixTopicBySlug = "forum:topic-slug:v3:"
	// id→slug 反向映射：写路径只有 topicID 时也能同时失效 by-slug 详情缓存。
	prefixTopicIDSlug  = "forum:topic-id-slug:v3:"
	prefixTopicsList   = "forum:topics:"
	prefixCommentsList = "forum:comments:v2:"
	prefixCatsList     = "forum:cats"
	prefixGroupsList   = "forum:groups"
	prefixTagsList     = "forum:tags"

	ttlList        = 15 * time.Second // 主题列表：变化较频繁
	ttlListPage1   = 45 * time.Second // 第一页（首页/分类热路径）稍长 TTL
	ttlTaxonomy    = 60 * time.Second // 分类/分组/标签：变化极少
	ttlTopicDetail = 30 * time.Second // 主题详情
	ttlComments    = 20 * time.Second // 评论列表：详情热路径
	ttlCommentsP1  = 40 * time.Second // 评论第一页稍长
)

func topicsGenCategory(slug string) string {
	return genTopicsCatPref + strings.TrimSpace(slug)
}

func topicsGenTag(slug string) string {
	return genTopicsTagPref + strings.TrimSpace(slug)
}

// NewCachedStore 包装底层 store。cache 为 nil 时直接返回底层 store（不装饰）。
func NewCachedStore(inner Store, cache cache.Cache) Store {
	if cache == nil || inner == nil {
		return inner
	}
	return &CachedStore{Store: inner, cache: cache}
}

func WithReferenceRenderCacheInvalidator(store Store, invalidator ReferenceRenderCacheInvalidator) Store {
	if cached, ok := store.(*CachedStore); ok {
		cached.referenceInvalidator = invalidator
	}
	return store
}

func (s *CachedStore) LoadPublicShortcodeSources(
	ctx context.Context,
	resources []contentregistry.ShortcodeResourceKey,
) (map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource, error) {
	source, ok := s.Store.(PublicShortcodeSourceStore)
	if !ok {
		return nil, nil
	}
	return source.LoadPublicShortcodeSources(ctx, resources)
}

func (s *CachedStore) ListReferenceOptions(ctx context.Context, input ReferenceSelectorInput) ([]ReferenceOption, error) {
	store, ok := s.Store.(ReferenceSelectorStore)
	if !ok {
		return nil, ErrInvalidReferenceSelector
	}
	return store.ListReferenceOptions(ctx, input)
}

// currentGen 读取 generation 当前值（不递增），用于构造读缓存 key。
// 失败或 key 不存在时返回 "0"，等价于"首代"——此时缓存 miss，回源后写入，
// 行为与无 generation 一致。
func (s *CachedStore) currentGen(ctx context.Context, key string) string {
	raw, found, err := s.cache.Get(ctx, key)
	if err != nil {
		slog.WarnContext(ctx, "forum cache: read generation failed", "key", key, "err", err)
		return "0"
	}
	if !found {
		return "0"
	}
	return string(raw)
}

// bumpGen 递增 generation 版本号。
// 采用 Get→Set 的非原子递增：对短 TTL 缓存（15-60s）可接受，最坏情况下
// 并发写入产生相同版本号，导致旧列表多缓存一个 TTL 周期，数据最终一致。
// 这避免了 Cache 接口必须暴露原子 Incr 的强约束，简化实现。
func (s *CachedStore) bumpGen(ctx context.Context, key string) {
	current := s.currentGen(ctx, key)
	n, err := strconv.ParseInt(current, 10, 64)
	if err != nil {
		n = 0
	}
	n++
	_ = s.cache.Set(ctx, key, []byte(strconv.FormatInt(n, 10)), 24*time.Hour)
}

// --- 读方法：带缓存 ---

func (s *CachedStore) ListCategories(ctx context.Context, includeHidden bool) ([]Category, error) {
	key := prefixCatsList
	if includeHidden {
		key = prefixCatsList + ":all"
	}
	var out []Category
	if s.loadJSON(ctx, key, &out) {
		return out, nil
	}
	out, err := s.Store.ListCategories(ctx, includeHidden)
	if err != nil {
		return nil, err
	}
	s.saveJSON(ctx, key, out, ttlTaxonomy)
	return out, nil
}

func (s *CachedStore) ListCategoryGroups(ctx context.Context, includeHidden bool) ([]CategoryGroup, error) {
	key := prefixGroupsList
	if includeHidden {
		key = prefixGroupsList + ":all"
	}
	var out []CategoryGroup
	if s.loadJSON(ctx, key, &out) {
		return out, nil
	}
	out, err := s.Store.ListCategoryGroups(ctx, includeHidden)
	if err != nil {
		return nil, err
	}
	s.saveJSON(ctx, key, out, ttlTaxonomy)
	return out, nil
}

func (s *CachedStore) ListTags(ctx context.Context, includePending bool) ([]Tag, error) {
	key := prefixTagsList
	if includePending {
		key = prefixTagsList + ":all"
	}
	var out []Tag
	if s.loadJSON(ctx, key, &out) {
		return out, nil
	}
	out, err := s.Store.ListTags(ctx, includePending)
	if err != nil {
		return nil, err
	}
	s.saveJSON(ctx, key, out, ttlTaxonomy)
	return out, nil
}

func (s *CachedStore) GetTopic(ctx context.Context, topicID int64) (TopicDetail, error) {
	key := fmt.Sprintf("%s%d", prefixTopicDetail, topicID)
	var out TopicDetail
	if s.loadJSON(ctx, key, &out) {
		return out, nil
	}
	out, err := s.Store.GetTopic(ctx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	// 双写 id + slug，避免 id/slug 两种入口各 miss 一次。
	s.saveTopicDetail(ctx, out)
	return out, nil
}

// Editable source is intentionally never loaded from or written to the
// actor-independent public cache.
func (s *CachedStore) GetTopicEditSource(ctx context.Context, topicID int64) (EditableContentSource, error) {
	return s.Store.GetTopicEditSource(ctx, topicID)
}

func (s *CachedStore) GetCommentEditSource(ctx context.Context, commentID int64) (EditableContentSource, error) {
	return s.Store.GetCommentEditSource(ctx, commentID)
}

func (s *CachedStore) GetTopicEventSnapshot(ctx context.Context, topicID int64) (TopicSummary, error) {
	if store, ok := s.Store.(TopicEventSnapshotStore); ok {
		return store.GetTopicEventSnapshot(ctx, topicID)
	}
	return s.Store.GetTopicForAction(ctx, topicID)
}

func (s *CachedStore) ListAuthorReviewItems(ctx context.Context, authorUserID int64) (AuthorReviewList, error) {
	store, ok := s.Store.(AuthorReviewStore)
	if !ok {
		return AuthorReviewList{}, ErrInvalidAction
	}
	return store.ListAuthorReviewItems(ctx, authorUserID)
}

// GetTopicBySlug 缓存按 slug 查询的主题详情。slug 模式下访问量高，缓存命中可减少 DB 压力。
func (s *CachedStore) GetTopicBySlug(ctx context.Context, slug string) (TopicDetail, error) {
	key := prefixTopicBySlug + slug
	var out TopicDetail
	if s.loadJSON(ctx, key, &out) {
		return out, nil
	}
	out, err := s.Store.GetTopicBySlug(ctx, slug)
	if err != nil {
		return TopicDetail{}, err
	}
	s.saveTopicDetail(ctx, out)
	return out, nil
}

// saveTopicDetail 写入 id 键、slug 键与 id→slug 反向映射（同 TTL）。
// 公开详情不含权限抬升字段；私有/hidden 主题不会进入本路径。
func (s *CachedStore) saveTopicDetail(ctx context.Context, out TopicDetail) {
	if out.ID <= 0 {
		return
	}
	s.saveJSON(ctx, fmt.Sprintf("%s%d", prefixTopicDetail, out.ID), out, ttlTopicDetail)
	if out.Slug == "" {
		return
	}
	s.saveJSON(ctx, prefixTopicBySlug+out.Slug, out, ttlTopicDetail)
	_ = s.cache.Set(ctx, fmt.Sprintf("%s%d", prefixTopicIDSlug, out.ID), []byte(out.Slug), ttlTopicDetail)
}

// TopicSlugExists 不缓存：写路径的实时唯一性校验，必须读最新数据。
func (s *CachedStore) TopicSlugExists(ctx context.Context, slug string, excludeTopicID int64) (bool, error) {
	return s.Store.TopicSlugExists(ctx, slug, excludeTopicID)
}

func (s *CachedStore) ListTopics(ctx context.Context, input TopicListInput) (TopicList, error) {
	// key 含分片 gen + sort + after：不同 scope/排序/游标不能共用同一缓存条目。
	gen := s.listTopicsCacheGen(ctx, input)
	after := strings.TrimSpace(input.After)
	key := fmt.Sprintf("%s%s:%s:%s:%s:%d:%d:%s", prefixTopicsList, gen, input.CategorySlug, input.TagSlug, input.Sort, input.Page, input.PerPage, after)
	var out TopicList
	if s.loadJSON(ctx, key, &out) {
		return out, nil
	}
	out, err := s.Store.ListTopics(ctx, input)
	if err != nil {
		return TopicList{}, err
	}
	// 首页/分类第一页更热；稍长 TTL 降低冷路径压力（generation 写路径仍失效）。
	ttl := ttlList
	if input.Page <= 1 && after == "" {
		ttl = ttlListPage1
	}
	s.saveJSON(ctx, key, out, ttl)
	return out, nil
}

// listTopicsCacheGen 按列表过滤条件选择分片 generation（M6）。
// 分类+标签双过滤时拼接两个 gen，任一维度写路径 bump 即 miss。
func (s *CachedStore) listTopicsCacheGen(ctx context.Context, input TopicListInput) string {
	cat := strings.TrimSpace(input.CategorySlug)
	tag := strings.TrimSpace(input.TagSlug)
	switch {
	case cat != "" && tag != "":
		return "c" + s.currentGen(ctx, topicsGenCategory(cat)) + ":t" + s.currentGen(ctx, topicsGenTag(tag))
	case cat != "":
		return "c" + s.currentGen(ctx, topicsGenCategory(cat))
	case tag != "":
		return "t" + s.currentGen(ctx, topicsGenTag(tag))
	default:
		return "g" + s.currentGen(ctx, genTopicsGlobal)
	}
}

// ListComments 缓存公开评论列表（不含 soft-delete 扩展范围）。
// key 含 topic 级 generation、view/page|after/perPage/tree cap，写路径 bump 后旧 key 自然过期。
func (s *CachedStore) ListComments(ctx context.Context, input CommentListInput) (CommentList, error) {
	// 含软删墓碑的结果与 viewer 相关，禁止共享缓存。
	if input.IncludeDeleted || input.DeletedAuthorUserID != 0 {
		return s.Store.ListComments(ctx, input)
	}
	gen := s.currentGen(ctx, commentsGenKey(input.TopicID))
	capN := input.TreeDescendantsPerRoot
	if capN <= 0 {
		capN = RecommendedTreeDescendantsPerRoot
	}
	after := strings.TrimSpace(input.After)
	key := fmt.Sprintf("%s%s:%d:%s:%d:%d:%d:%s", prefixCommentsList, gen, input.TopicID, input.View, input.Page, input.PerPage, capN, after)
	var out CommentList
	if s.loadJSON(ctx, key, &out) {
		return out, nil
	}
	out, err := s.Store.ListComments(ctx, input)
	if err != nil {
		return CommentList{}, err
	}
	ttl := ttlComments
	if input.Page <= 1 && after == "" {
		ttl = ttlCommentsP1
	}
	s.saveJSON(ctx, key, out, ttl)
	return out, nil
}

// CommentRevisionState 透传底层 store：修订号是 SSE 对账的事实来源，
// 一旦被 generation 缓存住就会持续发出「没有变化」的错误信号。
func (s *CachedStore) CommentRevisionState(ctx context.Context, topicID int64) (CommentRevisionState, error) {
	inner, ok := s.Store.(CommentRevisionStore)
	if !ok {
		return CommentRevisionState{}, ErrCommentRevisionUnavailable
	}
	return inner.CommentRevisionState(ctx, topicID)
}

// InvalidateModerationPublication closes the cache boundary for moderation,
// whose approval transaction writes the forum tables directly. It runs only
// after commit, so cache generations never advertise a rolled-back decision.
func (s *CachedStore) InvalidateModerationPublication(ctx context.Context, topicID int64, targetType string) {
	if topicID <= 0 {
		return
	}
	scopes := s.topicListScopes(ctx, topicID)
	s.invalidateTopicDetail(ctx, topicID)
	s.invalidateTopicsScoped(ctx, scopes)
	s.invalidateTaxonomy(ctx)
	if targetType == "comment" {
		s.invalidateComments(ctx, topicID)
		s.invalidateReferenceTopicVisibility(ctx)
	} else {
		s.invalidateReferenceResource(ctx, "topic", topicID)
	}
}

func commentsGenKey(topicID int64) string {
	return fmt.Sprintf("%s%d", genCommentsPrefix, topicID)
}

// --- 写方法：成功后失效 ---

func (s *CachedStore) CreateTopic(ctx context.Context, input CreateTopicRecord) (TopicDetail, error) {
	out, err := s.Store.CreateTopic(ctx, input)
	if err != nil {
		return out, err
	}
	s.invalidateTopicsScoped(ctx, topicListScopesFromDetail(out))
	s.invalidateTaxonomy(ctx)
	s.invalidateTopicBySlug(ctx, out.Slug)
	s.invalidateReferenceResource(ctx, "topic", out.ID)
	return out, nil
}

func (s *CachedStore) UpdateTopic(ctx context.Context, input UpdateTopicRecord) (TopicDetail, error) {
	// 分类/标签可能迁移：写前取旧 scope，与写后结果合并 bump。
	oldScopes := s.topicListScopes(ctx, input.TopicID)
	out, err := s.Store.UpdateTopic(ctx, input)
	if err != nil {
		return out, err
	}
	s.invalidateTopicDetail(ctx, input.TopicID)
	s.invalidateTopicsScoped(ctx, mergeTopicListScopes(oldScopes, topicListScopesFromDetail(out)))
	s.invalidateTaxonomy(ctx)
	s.invalidateTopicBySlug(ctx, out.Slug)
	s.invalidateReferenceResource(ctx, "topic", input.TopicID)
	return out, nil
}

func (s *CachedStore) RedactTopicRevision(ctx context.Context, input RevisionRedactionRecord) error {
	return s.Store.RedactTopicRevision(ctx, input)
}

func (s *CachedStore) RedactCommentRevision(ctx context.Context, input RevisionRedactionRecord) error {
	return s.Store.RedactCommentRevision(ctx, input)
}

func (s *CachedStore) DeleteTopic(ctx context.Context, topicID int64) (TopicDetail, error) {
	// 删除前尽量解析 scope（详情缓存或回源）；失败时至少 bump global。
	scopes := s.topicListScopes(ctx, topicID)
	out, err := s.Store.DeleteTopic(ctx, topicID)
	if err != nil {
		return out, err
	}
	s.invalidateTopicDetail(ctx, topicID)
	s.invalidateTopicBySlug(ctx, out.Slug)
	s.invalidateTopicsScoped(ctx, mergeTopicListScopes(scopes, topicListScopesFromDetail(out)))
	s.invalidateTaxonomy(ctx)
	s.invalidateReferenceResource(ctx, "topic", topicID)
	return out, nil
}

func (s *CachedStore) ApplyTopicAction(ctx context.Context, input TopicLifecycleInput) (TopicLifecycleRecord, error) {
	scopes := s.topicListScopes(ctx, input.TopicID)
	out, err := s.Store.ApplyTopicAction(ctx, input)
	if err != nil {
		return out, err
	}
	s.invalidateTopicDetail(ctx, input.TopicID)
	s.invalidateTopicsScoped(ctx, scopes)
	s.invalidateReferenceResource(ctx, "topic", input.TopicID)
	return out, nil
}

func (s *CachedStore) CreateComment(ctx context.Context, input CreateCommentRecord) (Comment, error) {
	// 评论写入前解析主题分类/标签 scope（详情缓存命中时零 DB）。
	scopes := s.topicListScopes(ctx, input.TopicID)
	out, err := s.Store.CreateComment(ctx, input)
	if err != nil {
		return out, err
	}
	// 新评论更新主题 last_activity_at，影响列表排序与详情评论计数。
	s.invalidateTopicDetail(ctx, input.TopicID)
	s.invalidateTopicsScoped(ctx, scopes)
	s.invalidateTaxonomy(ctx)
	s.invalidateComments(ctx, input.TopicID)
	s.invalidateReferenceResource(ctx, "comment", out.ID)
	return out, nil
}

func (s *CachedStore) UpdateComment(ctx context.Context, input UpdateCommentRecord) (Comment, error) {
	out, err := s.Store.UpdateComment(ctx, input)
	if err != nil {
		return out, err
	}
	if out.TopicID > 0 {
		// 纯正文编辑不改 last_activity / 列表序，不 bump 列表 gen。
		s.invalidateTopicDetail(ctx, out.TopicID)
		s.invalidateComments(ctx, out.TopicID)
	}
	s.invalidateReferenceResource(ctx, "comment", out.ID)
	return out, nil
}

func (s *CachedStore) DeleteComment(ctx context.Context, commentID int64) (Comment, error) {
	out, err := s.Store.DeleteComment(ctx, commentID)
	if err != nil {
		return out, err
	}
	if out.TopicID > 0 {
		// Delete 返回后详情可能已脏；优先用返回前缓存 scope，否则回源。
		scopes := s.topicListScopes(ctx, out.TopicID)
		s.invalidateTopicDetail(ctx, out.TopicID)
		s.invalidateTopicsScoped(ctx, scopes)
		s.invalidateTaxonomy(ctx)
		s.invalidateComments(ctx, out.TopicID)
	}
	s.invalidateReferenceResource(ctx, "comment", commentID)
	return out, nil
}

func (s *CachedStore) CreateCategory(ctx context.Context, input CreateCategoryInput) (Category, error) {
	out, err := s.Store.CreateCategory(ctx, input)
	if err != nil {
		return out, err
	}
	s.invalidateTaxonomy(ctx)
	s.invalidateReferenceTopicVisibility(ctx)
	return out, nil
}

func (s *CachedStore) UpdateCategory(ctx context.Context, input UpdateCategoryInput) (Category, error) {
	out, err := s.Store.UpdateCategory(ctx, input)
	if err != nil {
		return out, err
	}
	s.invalidateTaxonomy(ctx)
	s.invalidateReferenceTopicVisibility(ctx)
	return out, nil
}

func (s *CachedStore) DeleteCategory(ctx context.Context, categoryID int64) (Category, error) {
	out, err := s.Store.DeleteCategory(ctx, categoryID)
	if err != nil {
		return out, err
	}
	s.invalidateTaxonomy(ctx)
	s.invalidateReferenceTopicVisibility(ctx)
	return out, nil
}

func (s *CachedStore) invalidateReferenceResource(ctx context.Context, resourceType string, resourceID int64) {
	if s != nil && s.referenceInvalidator != nil {
		s.referenceInvalidator.InvalidateReferenceResource(ctx, resourceType, resourceID)
	}
}

func (s *CachedStore) invalidateReferenceTopicVisibility(ctx context.Context) {
	if s != nil && s.referenceInvalidator != nil {
		s.referenceInvalidator.InvalidateTopicVisibility(ctx)
	}
}

func (s *CachedStore) CreateCategoryGroup(ctx context.Context, input CreateCategoryGroupInput) (CategoryGroup, error) {
	out, err := s.Store.CreateCategoryGroup(ctx, input)
	if err != nil {
		return out, err
	}
	s.invalidateTaxonomy(ctx)
	s.invalidateReferenceTopicVisibility(ctx)
	return out, nil
}

func (s *CachedStore) UpdateCategoryGroup(ctx context.Context, input UpdateCategoryGroupInput) (CategoryGroup, error) {
	out, err := s.Store.UpdateCategoryGroup(ctx, input)
	if err != nil {
		return out, err
	}
	s.invalidateTaxonomy(ctx)
	s.invalidateReferenceTopicVisibility(ctx)
	return out, nil
}

func (s *CachedStore) DeleteCategoryGroup(ctx context.Context, groupID int64) (CategoryGroup, error) {
	out, err := s.Store.DeleteCategoryGroup(ctx, groupID)
	if err != nil {
		return out, err
	}
	s.invalidateTaxonomy(ctx)
	s.invalidateReferenceTopicVisibility(ctx)
	return out, nil
}

func (s *CachedStore) CreateTag(ctx context.Context, input CreateTagInput) (Tag, error) {
	out, err := s.Store.CreateTag(ctx, input)
	if err != nil {
		return out, err
	}
	s.invalidateTaxonomy(ctx)
	return out, nil
}

func (s *CachedStore) UpdateTag(ctx context.Context, input UpdateTagInput) (Tag, error) {
	out, err := s.Store.UpdateTag(ctx, input)
	if err != nil {
		return out, err
	}
	s.invalidateTaxonomy(ctx)
	return out, nil
}

// --- 失效辅助 ---

// topicListScopes 描述一次写操作应失效的主题列表分片（M6）。
// 始终 bump global（首页）；分类/标签只 bump 受影响 slug，避免单分类写风暴刷全站。
type topicListScopes struct {
	CategorySlugs []string
	TagSlugs      []string
}

func topicListScopesFromDetail(d TopicDetail) topicListScopes {
	tags := make([]string, 0, len(d.Tags))
	for _, t := range d.Tags {
		if slug := strings.TrimSpace(t.Slug); slug != "" {
			tags = append(tags, slug)
		}
	}
	cat := strings.TrimSpace(d.CategorySlug)
	var cats []string
	if cat != "" {
		cats = []string{cat}
	}
	return topicListScopes{CategorySlugs: cats, TagSlugs: tags}
}

func mergeTopicListScopes(parts ...topicListScopes) topicListScopes {
	var out topicListScopes
	for _, p := range parts {
		out.CategorySlugs = append(out.CategorySlugs, p.CategorySlugs...)
		out.TagSlugs = append(out.TagSlugs, p.TagSlugs...)
	}
	return out
}

// topicListScopes 解析主题所属分类/标签，供评论/生命周期等仅有 topicID 的写路径使用。
// 优先读详情缓存；miss 时回源 GetTopic（写路径可接受一次轻量查询）。
// 解析失败时返回空 scope——仍会 bump global，分类列表依赖 TTL 收敛。
func (s *CachedStore) topicListScopes(ctx context.Context, topicID int64) topicListScopes {
	if topicID <= 0 {
		return topicListScopes{}
	}
	var detail TopicDetail
	if s.loadJSON(ctx, fmt.Sprintf("%s%d", prefixTopicDetail, topicID), &detail) {
		return topicListScopesFromDetail(detail)
	}
	// 反向映射 → slug 详情缓存
	if raw, found, err := s.cache.Get(ctx, fmt.Sprintf("%s%d", prefixTopicIDSlug, topicID)); err == nil && found && len(raw) > 0 {
		if s.loadJSON(ctx, prefixTopicBySlug+string(raw), &detail) {
			return topicListScopesFromDetail(detail)
		}
	}
	detail, err := s.Store.GetTopic(ctx, topicID)
	if err != nil {
		slog.WarnContext(ctx, "forum cache: resolve topic list scopes failed", "topicID", topicID, "err", err)
		return topicListScopes{}
	}
	return topicListScopesFromDetail(detail)
}

// invalidateTopicsScoped 递增 global + 受影响 cat/tag generation。
// 不去重以外的 scope：其它分类/标签列表 gen 不变 → 缓存继续命中。
func (s *CachedStore) invalidateTopicsScoped(ctx context.Context, scopes topicListScopes) {
	s.bumpGen(ctx, genTopicsGlobal)
	seenCat := map[string]struct{}{}
	for _, slug := range scopes.CategorySlugs {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if _, ok := seenCat[slug]; ok {
			continue
		}
		seenCat[slug] = struct{}{}
		s.bumpGen(ctx, topicsGenCategory(slug))
	}
	seenTag := map[string]struct{}{}
	for _, slug := range scopes.TagSlugs {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if _, ok := seenTag[slug]; ok {
			continue
		}
		seenTag[slug] = struct{}{}
		s.bumpGen(ctx, topicsGenTag(slug))
	}
}

func (s *CachedStore) invalidateTaxonomy(ctx context.Context) {
	s.bumpGen(ctx, genCategories)
	s.bumpGen(ctx, genCategoryGroups)
	s.bumpGen(ctx, genTags)
	_ = s.cache.Delete(ctx, prefixCatsList, prefixCatsList+":all", prefixGroupsList, prefixGroupsList+":all", prefixTagsList, prefixTagsList+":all")
}

// invalidateTopicDetail 同时清除 id 详情、反向映射对应的 slug 详情。
// 评论写入等只有 topicID 的路径依赖此方法失效 by-slug 缓存（comment_count 等）。
func (s *CachedStore) invalidateTopicDetail(ctx context.Context, topicID int64) {
	if topicID <= 0 {
		return
	}
	idKey := fmt.Sprintf("%s%d", prefixTopicDetail, topicID)
	revKey := fmt.Sprintf("%s%d", prefixTopicIDSlug, topicID)
	keys := []string{idKey, revKey}
	if raw, found, err := s.cache.Get(ctx, revKey); err == nil && found && len(raw) > 0 {
		keys = append(keys, prefixTopicBySlug+string(raw))
	}
	_ = s.cache.Delete(ctx, keys...)
}

// invalidateComments 递增该主题评论 generation，使 ListComments 缓存 miss。
func (s *CachedStore) invalidateComments(ctx context.Context, topicID int64) {
	if topicID <= 0 {
		return
	}
	s.bumpGen(ctx, commentsGenKey(topicID))
}

// invalidateTopicBySlug 清除按 slug 缓存的详情条目。用于写操作后保证 slug 维度缓存及时更新；
// 若写路径同时有 topicID，优先走 invalidateTopicDetail（含反向映射）。
// 旧 slug 在改名后若反向映射已更新，残留条目依赖 TTL（30s）自然过期，规范化 301 兜底。
func (s *CachedStore) invalidateTopicBySlug(ctx context.Context, slug string) {
	if slug == "" {
		return
	}
	_ = s.cache.Delete(ctx, prefixTopicBySlug+slug)
}

// --- JSON 序列化辅助 ---

// loadJSON 尝试从缓存加载并反序列化。命中返回 true。
func (s *CachedStore) loadJSON(ctx context.Context, key string, dst any) bool {
	raw, found, err := s.cache.Get(ctx, key)
	if err != nil {
		slog.WarnContext(ctx, "forum cache: get failed", "key", key, "err", err)
		return false
	}
	if !found {
		return false
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		slog.WarnContext(ctx, "forum cache: unmarshal failed", "key", key, "err", err)
		return false
	}
	return true
}

// saveJSON 序列化并写入缓存。失败只记日志（缓存写失败不应影响主流程）。
func (s *CachedStore) saveJSON(ctx context.Context, key string, value any, ttl time.Duration) {
	raw, err := json.Marshal(value)
	if err != nil {
		slog.WarnContext(ctx, "forum cache: marshal failed", "key", key, "err", err)
		return
	}
	if err := s.cache.Set(ctx, key, raw, ttl); err != nil {
		slog.WarnContext(ctx, "forum cache: set failed", "key", key, "err", err)
	}
}

var _ CommentRevisionStore = (*CachedStore)(nil)
