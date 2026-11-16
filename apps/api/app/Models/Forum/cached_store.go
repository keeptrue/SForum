package forum

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	// 缓存 key 前缀。约定：读模型**新增字段就必须换前缀**，否则滚动发布期间
	// 新旧进程会互相读到缺字段的 JSON（缺字段在 Go 里解码成零值，不报错）。
	//   v3 → v4：TopBadge 增加 tierName/tierColor（等级外观），由读 SQL 一并带出。
	//   topics v1 → v2、comments v2 → v3：同一次改动，列表/评论读模型同样多出外观列。
	prefixTopicDetail = "forum:topic:v4:"
	prefixTopicBySlug = "forum:topic-slug:v4:"
	// id→slug 反向映射：写路径只有 topicID 时也能同时失效 by-slug 详情缓存。
	prefixTopicIDSlug  = "forum:topic-id-slug:v4:"
	prefixTopicsList   = "forum:topics:v2:"
	prefixCommentsList = "forum:comments:v3:"
	prefixCatsList     = "forum:cats"
	prefixGroupsList   = "forum:groups"
	prefixTagsList     = "forum:tags"

	// 公开读缓存的**最坏**陈旧窗口是下面最长的那一条：taxonomy 的 60s
	// （分类/分组/标签）。任何「最坏多旧 X 秒」的表述都以它为准，别拿
	// ttlListPage1 的 45s 当上限 —— 主题列表分片还有 60s 的 taxonomy 依赖。
	ttlList        = 15 * time.Second // 主题列表：变化较频繁
	ttlListPage1   = 45 * time.Second // 第一页（首页/分类热路径）稍长 TTL
	ttlTaxonomy    = 60 * time.Second // 分类/分组/标签：变化极少（当前最长窗口）
	ttlTopicDetail = 30 * time.Second // 主题详情
	ttlComments    = 20 * time.Second // 评论列表：详情热路径
	ttlCommentsP1  = 40 * time.Second // 评论第一页稍长
)

// 分片 generation 的 key 也走同一个编码器。
//
// 这一条是口径纠正：只编码「列表 key」而让 gen key 继续嵌原始 slug，等于把
// 客户端输入的长度/字符集留在 Redis key 空间里（gen key 由读写两侧共用同一个
// 编码，所以不会出现两侧不一致）。gen key 只存一个计数器、不承载任何数据，
// 编码后旧计数器自然作废，最坏多缓存一个列表 TTL。
func topicsGenCategory(slug string) string {
	return genTopicsCatPref + cacheKeyPart(slug, maxCacheSlugLen)
}

func topicsGenTag(slug string) string {
	return genTopicsTagPref + cacheKeyPart(slug, maxCacheSlugLen)
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

// genTTL 是 generation key 的过期时间：远长于列表 TTL，又不会永久驻留。
const genTTL = 24 * time.Hour

// bumpGen 递增 generation 版本号。
//
// 优先走 Cache 的原子 TTL 自增：Get→Set 的读-改-写在并发写路径上会让两个请求
// 算出同一个版本号，旧列表因此多缓存一个 TTL（RV-52）。没有原子能力的 Cache
// 实现退回旧路径，行为与此前一致。
func (s *CachedStore) bumpGen(ctx context.Context, key string) {
	if atomicBump, ok := s.cache.(cache.TTLIncrementer); ok {
		if _, err := atomicBump.IncrementWithTTL(ctx, key, genTTL); err == nil {
			return
		} else {
			slog.WarnContext(ctx, "forum cache: atomic generation bump failed, falling back", "key", key, "err", err)
		}
	}
	current := s.currentGen(ctx, key)
	n, err := strconv.ParseInt(current, 10, 64)
	if err != nil {
		n = 0
	}
	n++
	_ = s.cache.Set(ctx, key, []byte(strconv.FormatInt(n, 10)), genTTL)
}

// 缓存 key 片段的上限：slug/sort/view 都是短枚举或标识，超出的部分没有语义。
// 游标（after）没有固定形态，用摘要而不是截断，避免两个不同游标折成同一个 key。
const (
	maxCacheSlugLen = 80
	maxCacheSortLen = 16
	maxCacheViewLen = 16
	// cacheKeyHexDigits 是 _XX 转义的十六进制表。
	cacheKeyHexDigits = "0123456789abcdef"
)

// cacheKeyPart 编码一个外部可控的短过滤值，用于拼缓存 key。
//
// 两件事必须同时成立：
//   - 分隔符不能有歧义：categorySlug="a:b" + tagSlug="c" 与 "a" + "b:c" 曾经拼出
//     同一个 key，于是两个不同的列表互相回放对方的结果（RV-52）；
//   - 长度有界：客户端输入不能决定 key 的长度。
//
// 做法是「长度前缀 + 白名单折叠」：前缀让不同切分不再同形，白名单把分隔符与
// 任意非标识字节折叠掉。
func cacheKeyPart(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit > 0 && len(trimmed) > limit {
		trimmed = trimmed[:limit]
	}
	var b strings.Builder
	b.Grow(len(trimmed) + 4)
	b.WriteString(strconv.Itoa(len(trimmed)))
	b.WriteByte(':')
	for i := 0; i < len(trimmed); i++ {
		char := trimmed[i]
		// 只放行 slug/sort/view 实际用到的字符；其余（含作为转义引导符的 '_'）
		// 一律写成 _XX。这样编码是单射的：a_b 与 a/b 不再折叠成同一个片段。
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9',
			char == '-':
			b.WriteByte(char)
		default:
			b.WriteByte('_')
			b.WriteByte(cacheKeyHexDigits[char>>4])
			b.WriteByte(cacheKeyHexDigits[char&0x0f])
		}
	}
	return b.String()
}

// cacheKeyToken 把客户端游标折成定长摘要：游标是 base64url(JSON)，长度不可控，
// 也不能被截断成同一形状（截断会让两个不同的合法游标共用缓存条目）。
func cacheKeyToken(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return "h" + hex.EncodeToString(sum[:12])
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
	key := fmt.Sprintf("%s%s:%s:%s:%s:%d:%d:%s", prefixTopicsList, gen,
		cacheKeyPart(input.CategorySlug, maxCacheSlugLen),
		cacheKeyPart(input.TagSlug, maxCacheSlugLen),
		cacheKeyPart(input.Sort, maxCacheSortLen),
		input.Page, input.PerPage, cacheKeyToken(after))
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
	key := fmt.Sprintf("%s%s:%d:%s:%d:%d:%d:%s", prefixCommentsList, gen, input.TopicID,
		cacheKeyPart(input.View, maxCacheViewLen), input.Page, input.PerPage, capN, cacheKeyToken(after))
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
		// 防御性分支：合法评论的 topic_id 是 NOT NULL，所以 comment 目标必然能拿到
		// 父主题 ID。真走到这里说明上游形状回归了，能做的是把**不依赖 ID** 的引用侧
		// generation（短代码/引用渲染）bump 掉，评论列表缓存（genComments:<topicID>）
		// 没有 ID 无法失效，只能靠 TTL 自愈。调用方（Moderation）会同时留 warn 日志。
		if targetType == "comment" {
			s.invalidateReferenceTopicVisibility(ctx)
		}
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
