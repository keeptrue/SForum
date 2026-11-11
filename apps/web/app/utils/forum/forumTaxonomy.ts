import type { AvatarView } from '~/composables/profile/useProfileApi'
import { userDisplayName } from '~/utils/forum/userDisplay'
import type { ForumContentInput, ForumRenderedContent } from './forumTaxonomyContent'

export * from './forumTaxonomyContent'

export type ForumTagCreationMode = 'controlled' | 'review' | 'open'
export type ForumTagStatus = 'active' | 'pending' | 'disabled'
export type ForumVisibility = 'public' | 'hidden'
export type ForumDefaultSort = 'latest' | 'hot'
export type ForumTopicStatus = 'active' | 'locked' | 'hidden' | 'deleted' | 'pending' | 'rejected'

export type ForumGuestRead = 'public' | 'login_required'
export type ForumListSort = 'latest' | 'active' | 'hot'
// warn 为历史兼容值，服务端按 off 处理（无独立 warn 合同）。
export type ForumDuplicateTitlePolicy = 'off' | 'warn' | 'block'
export type ForumSoftDeleteVisibility = 'author_and_staff' | 'staff_only' | 'hidden'

export type ForumSettings = {
  defaultCategorySlug: string
  tagCreationMode: ForumTagCreationMode
  tagPublicPages: boolean
  tagMinPerTopic: number
  tagMaxPerTopic: number
  topicsPerPage: number
  commentsPerPage: number
  topicTitleMinRunes: number
  topicTitleMaxRunes: number
  topicContentMinRunes: number
  topicContentMaxRunes: number
  topicEditWindowMinutes: number
  topicCooldownSeconds: number
  dailyTopicLimit: number
  commentMinRunes: number
  commentMaxRunes: number
  commentMaxNestingDepth: number
  /** view=tree 每根最多子孙数（D2，默认 50） */
  treeDescendantsPerRoot: number
  commentEditWindowMinutes: number
  commentCooldownSeconds: number
  dailyCommentLimit: number
  excerptRuneLimit: number
  guestRead: ForumGuestRead
  listDefaultSort: ForumListSort
  listHotWindowDays: number
  allowAuthorCloseReplies: boolean
  allowAuthorDelete: boolean
  autoLockIdleDays: number
  showTopicEditMark: boolean
  duplicateTitlePolicy: ForumDuplicateTitlePolicy
  showCommentEditMark: boolean
  softDeleteVisibility: ForumSoftDeleteVisibility
  mentionsEnabled: boolean
  mentionsMaxPerPost: number
  /** 站外链接是否经过中转提示页（点击时判定）。 */
  externalInterstitial: boolean
  /** 免中转域名白名单（逐行，host 或 host:port）。 */
  trustedDomains: string
}

export type ForumCategory = {
  id: number
  groupId: number
  groupSlug: string
  groupName: string
  slug: string
  name: string
  description: string
  icon: string
  iconColor: string
  visibility: ForumVisibility
  position: number
  defaultSort: ForumDefaultSort
  topicCount: number
  commentCount: number
  createdAt: string
  updatedAt: string
}

export type ForumCategoryGroup = {
  id: number
  slug: string
  name: string
  description: string
  visibility: ForumVisibility
  position: number
  categories: ForumCategory[]
  createdAt: string
  updatedAt: string
}

export type ForumTag = {
  id: number
  slug: string
  name: string
  description: string
  icon: string
  iconColor: string
  status: ForumTagStatus
  topicCount: number
  createdAt: string
  updatedAt: string
}

export type ForumUserSummary = {
  id: number
  username: string
  displayName: string
  avatar: AvatarView
}

export type ForumTopicTagSummary = {
  id: number
  slug: string
  name: string
  status: ForumTagStatus
}

export type ForumTopicSummary = {
  id: number
  categoryId: number
  categorySlug: string
  categoryName: string
  authorUserId: number
  author?: ForumUserSummary
  /** 作者的最高级徽章（M3 读模型；未获得/功能关闭时不出现）。 */
  topBadge?: import('~/utils/forum/forumBadge').ForumBadgeRef
  /** 最近 active 评论作者；无评论时与 author 相同 */
  lastReplyAuthor?: ForumUserSummary
  title: string
  slug: string
  status: ForumTopicStatus
  isPinned: boolean
  commentCount: number
  viewCount: number
  likeCount: number
  tags?: ForumTopicTagSummary[]
  excerpt: string
  createdAt: string
  updatedAt: string
  lastActivityAt: string
  currentRevision: number
  edited?: boolean
}

export type ForumTopicDetail = ForumTopicSummary & {
  content: ForumRenderedContent
  likedByMe?: boolean
  /** 当前访客是否已收藏；仅登录访客填充，永不缓存。 */
  bookmarkedByMe?: boolean
  likedBy?: ForumUserSummary[] // 点赞头像预览：自己优先、其余最早，最多 3 个
  /** 当前已接受修订的提交时间；仅在编辑标记开启且主题已编辑时返回。 */
  editedAt?: string
  /** 主题正文贡献者（作者 + 编辑/恢复 actor），作者优先，最多 5 个 */
  contributors?: ForumUserSummary[]
  /** 去重后的贡献者总数，可大于 contributors.length */
  contributorCount?: number
  extensionActions?: ForumTopicExtensionAction[]
  /** forum.topic.sidebar 宿主描述符（E2.1） */
  extensionSidebar?: ForumTopicExtensionSidebarItem[]
  /** forum.topic.badges 宿主描述符（E2.1） */
  extensionBadges?: ForumTopicExtensionBadge[]
}

/** 公开贡献时间线条目：无 reason / 正文 / diff */
export type ForumTopicContributionEvent = {
  revisionNo: number
  current: boolean
  actor?: ForumUserSummary
  operation: 'create' | 'edit' | 'restore' | 'migration' | string
  origin: 'self' | 'staff' | 'migration' | string
  changedFields: string[]
  committedAt: string
  restoredFromRevisionNo?: number
  redacted: boolean
}

export type ForumTopicContributionTimeline = {
  items: ForumTopicContributionEvent[]
  perPage: number
  hasMore: boolean
  nextCursor?: string
}

export type ForumTopicExtensionActionMethod = 'POST' | 'PUT' | 'PATCH' | 'DELETE'

export type ForumTopicExtensionAction = {
  extensionId: string
  id: string
  label?: Record<string, string>
  icon?: string
  method: ForumTopicExtensionActionMethod
  url: string
  confirm?: boolean
}

/** forum.comment.actions 宿主描述符（E2.2）；挂在 CommentList 上 */
export type ForumCommentExtensionAction = ForumTopicExtensionAction & {
  /** UX 提示：为 true 时游客隐藏；权威鉴权仍在扩展路由代理 */
  requiresAuth?: boolean
}

/** forum.topic.sidebar 宿主描述符（E2.1） */
export type ForumTopicExtensionSidebarItem = {
  extensionId: string
  id: string
  order: number
  label?: Record<string, string>
  icon?: string
  kind: 'extensionRoute' | 'hostLink'
  method?: string
  url: string
}

/** forum.topic.badges 宿主描述符（E2.1） */
export type ForumTopicExtensionBadgeTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger'

export type ForumTopicExtensionBadge = {
  extensionId: string
  id: string
  order: number
  label?: Record<string, string>
  tone: ForumTopicExtensionBadgeTone
  href?: string
}

/** forum.composer.toolbar 宿主描述符（F4.3） */
export type ForumComposerToolbarAction = {
  extensionId: string
  id: string
  order: number
  label?: Record<string, string>
  icon?: string
  method: ForumTopicExtensionActionMethod
  url: string
  confirm?: boolean
}

export type ForumCommentStatus = 'active' | 'hidden' | 'deleted' | 'pending' | 'rejected'

export type ForumReplyReference = {
  id: number
  author?: ForumUserSummary
  excerpt: string
  depth: number
}

// 评论节点。tree 视图下 children 为嵌套回复；flat 视图下 children 为空。
export type ForumComment = {
  id: number
  topicId: number
  authorUserId: number
  author?: ForumUserSummary
  /** 作者的最高级徽章（M3 读模型；未获得/功能关闭时不出现）。 */
  topBadge?: import('~/utils/forum/forumBadge').ForumBadgeRef
  parentId?: number | null
  rootCommentId: number
  pathKey: string
  depth: number
  replyCount: number
  likeCount: number
  /** 请求级字段：仅登录访客且已赞时为 true，永不缓存。 */
  likedByMe?: boolean
  /** 请求级字段：仅登录访客且已收藏时为 true，永不缓存。 */
  bookmarkedByMe?: boolean
  likedBy?: ForumUserSummary[] // 点赞头像预览：自己优先、其余最早，最多 3 个
  status: ForumCommentStatus
  content: ForumRenderedContent
  replyTo?: ForumReplyReference
  children?: ForumComment[]
  /** tree 子孙被 treeDescendantsPerRoot 截断时为 true；更多走 listCommentReplies */
  hasMoreChildren?: boolean
  createdAt: string
  updatedAt: string
  currentRevision: number
  edited?: boolean
  /** 当前已接受修订的提交时间；仅在编辑标记开启且评论已编辑时返回。 */
  editedAt?: string
}

export type ForumCommentList = {
  items: ForumComment[]
  total: number
  page: number
  perPage: number
  view: 'tree' | 'flat'
  /** M5：是否还有下一页 */
  hasMore?: boolean
  /** M5：flat keyset 续页游标，作为 after 查询参数 */
  nextCursor?: string
  /** forum.comment.actions（E2.2）；列表级一次返回，前端挂到每行菜单 */
  extensionActions?: ForumCommentExtensionAction[]
}

// 主题生命周期动作结果。
export type ForumTopicAction = {
  topicId: number
  status: ForumTopicStatus
  isPinned: boolean
}

// 更新主题输入，所有字段可选；未提供即不修改。
export type ForumTopicUpdateInput = {
	/** Loaded revision token; required by the canonical PATCH contract. */
  expectedRevision: number
	/** Optional for self edits; cross-author admin editing supplies a reason in M5. */
  reason?: string
  categorySlug?: string
  title?: string
  tagSlugs?: string[]
  content?: ForumContentInput
}

export type ForumTopicCreateInput = ForumContentInput & {
  title: string
  categorySlug?: string
  tagSlugs?: string[]
}

export type ForumCommentListView = 'tree' | 'flat'

export type ForumCommentListQuery = {
  view?: ForumCommentListView
  page?: number
  perPage?: number
  /** M5 flat keyset：非空时 API 忽略 page */
  after?: string
}

// 评论页码反查结果：commentID 在 flat 视图分页下所属的页码与每页大小。
// 供帖子详情页带 #comment-{id} 锚点进入时 SSR 阶段确定加载哪一页。
export type ForumCommentPage = {
  page: number
  perPage: number
}

// 主题生命周期动作枚举，与后端 TopicAction 常量保持一致。
export const FORUM_TOPIC_ACTIONS = {
  hide: 'hide',
  restore: 'restore',
  lock: 'lock',
  unlock: 'unlock',
  pin: 'pin',
  unpin: 'unpin'
} as const

export type ForumTopicActionKey = keyof typeof FORUM_TOPIC_ACTIONS

export type ForumTopicList = {
  items: ForumTopicSummary[]
  /**
   * 列表总数（D1）：分类/标签为冗余计数（可短暂陈旧）；
   * 首页/多过滤可能为近似值。不要当严格实时全表 COUNT。
   * Infinite scroll 优先 hasMore / nextCursor。
   */
  total: number
  /** true 时 total 为估计/近似，UI 可显示「约 N」 */
  totalApproximate?: boolean
  page: number
  perPage: number
  /** M5：是否还有下一页（公开列表必填语义） */
  hasMore?: boolean
  /** M5：opaque keyset 游标；有下一页时作为 after 续页 */
  nextCursor?: string
  /** forum.topic.list.badges（E2.4）；列表级一次返回，前端挂到每行标题旁 */
  extensionListBadges?: ForumTopicExtensionBadge[]
}

/** 列表 total 展示：近似时加「约」，分类/标签冗余计数不加。 */
export function formatForumTopicListTotal(
  list: Pick<ForumTopicList, 'total' | 'totalApproximate'>,
  t: (key: string, params?: Record<string, unknown>) => string
): string {
  const count = Math.max(0, Math.floor(Number(list.total) || 0))
  if (list.totalApproximate) {
    return t('home.feed.topicCountMetaApprox', { count })
  }
  return t('home.feed.topicCountMeta', { count })
}

// 公开列表排序：与 GET /topics 的 sort 枚举一一对应
// （contracts/openapi/paths/forum.yaml：latest=创建时间 / active=最后活跃 / hot=hot_score）。
export type ForumTopicSort = 'latest' | 'active' | 'hot'

export const forumTopicSorts: ForumTopicSort[] = ['latest', 'active', 'hot']

// 归一化排序值；空值或非法值返回 ''，语义是「站点默认排序」：
// 请求不携带 sort，由服务端 forum.list.default_sort 决定。
export function normalizeForumTopicSort(value: unknown): ForumTopicSort | '' {
  const raw = typeof value === 'string' ? value.trim().toLowerCase() : ''
  return forumTopicSorts.find((sort) => sort === raw) || ''
}

export type ForumTopicFilters = {
  categorySlug?: string
  tagSlug?: string
  query?: string
  page?: number
  perPage?: number
  /** M5 keyset：非空时 API 忽略 page */
  after?: string
  /** 服务端排序；'' / undefined = 站点默认（forum.list.default_sort） */
  sort?: ForumTopicSort | ''
}

// 搜索输入：query 为关键词，必填；其余为可选过滤与分页。
export type ForumTopicSearchFilters = {
  query: string
  categorySlug?: string
  tagSlug?: string
  page?: number
  perPage?: number
}

export const recommendedForumSettings: ForumSettings = {
  defaultCategorySlug: 'general',
  tagCreationMode: 'controlled',
  tagPublicPages: true,
  tagMinPerTopic: 0,
  tagMaxPerTopic: 5,
  topicsPerPage: 20,
  commentsPerPage: 20,
  topicTitleMinRunes: 2,
  topicTitleMaxRunes: 100,
  topicContentMinRunes: 0,
  topicContentMaxRunes: 50000,
  topicEditWindowMinutes: 0,
  topicCooldownSeconds: 0,
  dailyTopicLimit: 0,
  commentMinRunes: 1,
  commentMaxRunes: 10000,
  commentMaxNestingDepth: 5,
  treeDescendantsPerRoot: 50,
  commentEditWindowMinutes: 0,
  commentCooldownSeconds: 0,
  dailyCommentLimit: 0,
  excerptRuneLimit: 180,
  guestRead: 'public',
  // 推荐默认 = 最后活跃（active）；与公开活动索引对齐，公开列表「最新」即最新活动。
  listDefaultSort: 'active',
  listHotWindowDays: 7,
  allowAuthorCloseReplies: true,
  allowAuthorDelete: true,
  autoLockIdleDays: 0,
  showTopicEditMark: true,
  duplicateTitlePolicy: 'off',
  showCommentEditMark: true,
  softDeleteVisibility: 'author_and_staff',
  mentionsEnabled: true,
  mentionsMaxPerPost: 10,
  // 推荐默认：站外链接经中转页提示；白名单为空。
  externalInterstitial: true,
  trustedDomains: ''
}

export function normalizeForumPageSize(value: number | string | undefined, fallback = 20) {
  const normalized = typeof value === 'number' ? value : Number(value)
  return Number.isInteger(normalized) && normalized >= 1 && normalized <= 100 ? normalized : fallback
}

export function parseForumTagPublicPagesOption(value: string | undefined, fallback = true) {
  switch (value?.trim().toLowerCase()) {
    case 'enabled':
    case 'true':
    case '1':
    case 'yes':
    case 'on':
      return true
    case 'disabled':
    case 'false':
    case '0':
    case 'no':
    case 'off':
      return false
    default:
      return fallback
  }
}

export function normalizeForumTagCreationMode(value: string | undefined): ForumTagCreationMode {
  const normalized = value?.trim().toLowerCase()
  return normalized === 'review' || normalized === 'open' ? normalized : recommendedForumSettings.tagCreationMode
}

export function normalizeForumTagMaxPerTopic(value: string | number | undefined) {
  return normalizeForumBoundedInt(value, 0, 10, recommendedForumSettings.tagMaxPerTopic)
}

export function normalizeForumBoundedInt(
  value: string | number | undefined,
  min: number,
  max: number,
  fallback: number
) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) {
    return fallback
  }
  const normalized = Math.trunc(parsed)
  return normalized >= min && normalized <= max ? normalized : fallback
}

export function normalizeForumTagMinPerTopic(value: string | number | undefined) {
  return normalizeForumBoundedInt(value, 0, 10, recommendedForumSettings.tagMinPerTopic)
}

export function buildForumTopicQuery(filters: ForumTopicFilters = {}) {
  const query: Record<string, string> = {}
  addStringQuery(query, 'categorySlug', filters.categorySlug)
  addStringQuery(query, 'tagSlug', filters.tagSlug)
  addStringQuery(query, 'query', filters.query)
  // M5：after 优先；有 cursor 时不传 page，避免语义混淆
  if (filters.after?.trim()) {
    addStringQuery(query, 'after', filters.after)
  } else {
    addPositiveNumberQuery(query, 'page', filters.page)
  }
  addPositiveNumberQuery(query, 'perPage', filters.perPage)
  // 排序交给服务端：cursor 与 sort 绑定，前端不再本地重排
  addStringQuery(query, 'sort', filters.sort)
  return query
}

// 构造搜索端点（GET /search）的 query。不复用 buildForumTopicQuery，
// 因 search 要求 query 必填，且语义独立于列表。
export function buildForumSearchQuery(filters: ForumTopicSearchFilters) {
  const query: Record<string, string> = {}
  addStringQuery(query, 'query', filters.query)
  addStringQuery(query, 'categorySlug', filters.categorySlug)
  addStringQuery(query, 'tagSlug', filters.tagSlug)
  addPositiveNumberQuery(query, 'page', filters.page)
  addPositiveNumberQuery(query, 'perPage', filters.perPage)
  return query
}

/** 分类/标签图标色：管理端覆盖色优先，未配置时回落站点主色。管理台与公开页共用这一份。 */
export function taxonomyIconColor(value?: string | null): string {
  return String(value ?? '').trim() || 'var(--sf-accent)'
}

/** 颜色选择器的输入值：只接受 `#rrggbb`，空值/旧值/写坏的格式一律回推荐色。 */
export function taxonomyColorInputValue(value?: string | null): string {
  const raw = String(value ?? '').trim()
  return /^#[0-9a-fA-F]{6}$/.test(raw) ? raw : '#0f766e'
}

export function forumCategoryPath(slug: string) {
  return `/c/${encodeURIComponent(slug)}`
}

// 公开「全部分类」列表页路径。
export function forumCategoriesIndexPath() {
  return '/categories'
}

export function forumTagPath(slug: string) {
  return `/tags/${encodeURIComponent(slug)}`
}

// 公开「全部标签」列表页路径（受 forum.tags.public_pages 控制）。
export function forumTagsIndexPath() {
  return '/tags'
}

// 标签云字号分桶：1（最小）…6（最大）。按 topicCount 在当前集合 min/max 上
// 做 log 归一化，避免写死 demo 字号，也避免单点极值把云挤扁。
export function tagCloudSizeBucket(
  topicCount: number,
  minCount: number,
  maxCount: number
): 1 | 2 | 3 | 4 | 5 | 6 {
  const count = Math.max(0, Number.isFinite(topicCount) ? topicCount : 0)
  const min = Math.max(0, Number.isFinite(minCount) ? minCount : 0)
  const max = Math.max(min, Number.isFinite(maxCount) ? maxCount : min)

  if (max <= min) {
    return 3
  }

  const logMin = Math.log1p(min)
  const logMax = Math.log1p(max)
  const span = logMax - logMin
  if (span <= 0) {
    return 3
  }

  const ratio = (Math.log1p(count) - logMin) / span
  const clamped = Math.min(1, Math.max(0, ratio))
  // 映射到 1..6：均匀 6 档
  const bucket = Math.min(6, Math.max(1, Math.floor(clamped * 6) + 1))
  return bucket as 1 | 2 | 3 | 4 | 5 | 6
}

// 热门阈值：取 topicCount 降序后约前 25% 的下界，至少为 1。
// 用于「热门」chip 与统计格，客户端从当前 active 标签集合计算。
export function tagHotThreshold(topicCounts: number[]): number {
  const counts = topicCounts
    .map((n) => (Number.isFinite(n) ? Math.max(0, Math.trunc(n)) : 0))
    .filter((n) => n > 0)
    .sort((a, b) => b - a)

  if (counts.length === 0) {
    return 1
  }
  if (counts.length === 1) {
    return Math.max(1, counts[0]!)
  }

  const index = Math.min(counts.length - 1, Math.max(0, Math.ceil(counts.length * 0.25) - 1))
  return Math.max(1, counts[index]!)
}

// 近 7 天内创建（本周新增 / 「本周」chip）。无「本周活跃」API 时的合理降级。
export function isCreatedWithinDays(isoDate: string | undefined, days: number, nowMs = Date.now()) {
  if (!isoDate || !Number.isFinite(days) || days <= 0) {
    return false
  }
  const created = Date.parse(isoDate)
  if (Number.isNaN(created)) {
    return false
  }
  return created <= nowMs && created >= nowMs - days * 24 * 60 * 60 * 1000
}

const forumTagSlugPattern = /^[\p{L}\p{N}]+(?:-[\p{L}\p{N}]+)*$/u

export function normalizeForumTagSlugInput(value: string) {
  return value.trim().toLowerCase()
}

export function isForumTagSlug(value: string) {
  const normalized = normalizeForumTagSlugInput(value)
  return normalized !== '' && forumTagSlugPattern.test(normalized)
}

// 帖子 URL 形态枚举。与 useWebOptions 的 TopicUrlMode 保持一致；
// 此处独立声明以保持 forumTaxonomy 为纯工具模块（不依赖 Nuxt auto-import）。
export type TopicUrlMode = 'id_slug' | 'id' | 'slug'
export type TopicPathLookup = { kind: 'id', topicId: number } | { kind: 'slug', slug: string }

// previewTopicSlug 在前端预览标题对应的 slug（与后端 slugify 主逻辑对齐：
// 转小写、非 [a-z0-9-] 字符替换为 -、首尾去 -）。仅用于编辑保存后跳转的预期路径；
// 若后端因全局唯一追加了 -2/-3 后缀，详情页 canonical 会 301 到真实 slug。
export function previewTopicSlug(title: string): string {
  const slug = title.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  return slug || 'topic'
}

// 评论分页路径段前缀：/t/<topic>/page/N。N>1 时追加，N=1 省略（canonical 同首页）。
// 设计为固定两段（"page", 数字），便于 parseTopicPath 末尾剥离后做定位解析。
const TOPIC_PAGE_SEGMENT = 'page'

export function forumTopicPath(
  topic: Pick<ForumTopicSummary, 'id' | 'slug'>,
  mode: TopicUrlMode = 'id_slug',
  page: number = 1
) {
  let base: string
  switch (mode) {
    case 'id':
      base = `/t/${topic.id}`
      break
    case 'slug':
      base = `/t/${encodeURIComponent(topic.slug)}`
      break
    default:
      base = `/t/${topic.id}/${encodeURIComponent(topic.slug)}`
      break
  }
  // page<=1 不追加段，保持与旧链接同形（canonical 不产生重复页）。
  return Number.isInteger(page) && page > 1 ? `${base}/${TOPIC_PAGE_SEGMENT}/${page}` : base
}

/** 主题编辑独立页路径；按 id 定位（编辑可能改 slug，slug URL 会自失效）。 */
export function forumTopicEditPath(topicId: number) {
  return `/topics/${topicId}/edit`
}

/** 高级回复独立页路径；parentId 可选，表示回复某条评论。 */
export function forumTopicAdvancedReplyPath(topicId: number, parentId?: number | null) {
  const query = new URLSearchParams({ topic: String(topicId) })
  if (parentId != null && parentId > 0) {
    query.set('parent', String(parentId))
  }
  return `/topics/reply?${query.toString()}`
}

/** 紧凑评论框 → 高级回复页的草稿交接（sessionStorage）。 */
export function advancedReplyDraftStorageKey(topicId: number) {
  return `sforum.advanced-reply.draft.${topicId}`
}

// parseTopicPath 解析 catch-all 详情页路由参数为帖子定位键。
// 返回值：id 模式下为 { topicId }；slug 模式下为 { slug }；
// id_slug 模式下为 { topicId, slug }。无法识别时返回 null（调用方应 404）。
// 若末尾存在 page/<正整数> 分页段，一并解析为 page（默认 1），不参与定位。
export function parseTopicPath(
  segments: string[] | readonly string[] | undefined,
  mode: TopicUrlMode = 'id_slug'
): { topicId?: number, slug?: string, page: number } | null {
  const { core, page } = splitTopicPageSegment(segments)
  if (core.length === 0) {
    return null
  }
  const first = core[0]!
  if (mode === 'id') {
    const topicId = Number(first)
    return Number.isInteger(topicId) && topicId > 0 ? { topicId, page } : null
  }
  if (mode === 'slug') {
    return { slug: decodeURIComponent(first), page }
  }
  // id_slug：期望 [id, slug]
  const topicId = Number(first)
  if (!Number.isInteger(topicId) || topicId <= 0) {
    return null
  }
  const rest = core.slice(1).join('/')
  return { topicId, slug: rest ? decodeURIComponent(rest) : '', page }
}

// splitTopicPageSegment 把 catch-all 段拆为 { 定位核心, 分页页码 }。
// 仅识别末尾固定的 page/<正整数> 两段；非法形态（如 page/abc、page/0）视为普通段，
// 交回定位解析处理（通常自然 404），避免吞掉用户误输入的 slug。
export function splitTopicPageSegment(segments: string[] | readonly string[] | undefined): { core: string[], page: number } {
  const parts = (segments ? [...segments] : []).filter((s) => s !== '')
  if (parts.length >= 2) {
    const len = parts.length
    if (parts[len - 2] === TOPIC_PAGE_SEGMENT) {
      const n = parsePositiveInteger(parts[len - 1]!)
      if (n > 0) {
        return { core: parts.slice(0, -2), page: n }
      }
    }
  }
  return { core: parts, page: 1 }
}

// 为详情页生成有序查询候选。当前 URL 模式的规范形态优先，同时兼容切换模式前
// 留下的 id、id+slug、slug 三种旧链接，再由详情页统一跳转到当前 canonical。
// 注意：page/<N> 分页段已先剥离，不参与定位（page 不是 slug/id 候选）。
export function topicPathLookupCandidates(
  segments: string[] | readonly string[] | undefined,
  mode: TopicUrlMode = 'id_slug'
): TopicPathLookup[] {
  // 先剥离末尾分页段，避免 /t/26/page/2 在 id 模式下把 "page" 当 slug。
  const parts = splitTopicPageSegment(segments).core
  if (parts.length === 0) {
    return []
  }

  const candidates: TopicPathLookup[] = []
  const first = parts[0]!
  const topicId = parsePositiveInteger(first)
  const addID = () => {
    if (topicId && !candidates.some((item) => item.kind === 'id' && item.topicId === topicId)) {
      candidates.push({ kind: 'id', topicId })
    }
  }
  const addSlug = (raw: string) => {
    const slug = decodeURIComponent(raw)
    if (slug && !candidates.some((item) => item.kind === 'slug' && item.slug === slug)) {
      candidates.push({ kind: 'slug', slug })
    }
  }

  if (parts.length >= 2) {
    addID()
    return candidates
  }

  if (mode === 'slug') {
    addSlug(first)
    addID()
    return candidates
  }

  addID()
  addSlug(first)
  return candidates
}

function parsePositiveInteger(value: string) {
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : 0
}

export function forumUserProfilePath(username: string) {
  return `/u/${encodeURIComponent(username)}`
}

export function forumTopicExtensionLabel(
  item: { label?: Record<string, string> },
  locale = 'zh-CN'
) {
  const labels = item.label || {}
  return labels[locale] || labels['zh-CN'] || labels['en-US'] || Object.values(labels)[0] || ''
}

export function forumTopicExtensionActionLabel(action: ForumTopicExtensionAction, locale = 'zh-CN') {
  return forumTopicExtensionLabel(action, locale) || action.id
}

export function forumTopicExtensionActionRequestPath(action: Pick<ForumTopicExtensionAction, 'url'>) {
  const url = `${action.url || ''}`.trim()
  if (!url.startsWith('/extensions/') || url.includes('://') || url.includes('..') || url.startsWith('/api')) {
    return ''
  }
  return url
}

export function forumTopicExtensionActionRequest(action: ForumTopicExtensionAction, topicId: number) {
  const path = forumTopicExtensionActionRequestPath(action)
  if (!path || !Number.isInteger(topicId) || topicId <= 0 || !isTopicExtensionActionMethod(action.method)) {
    return null
  }
  return {
    path,
    method: action.method,
    body: { topicId }
  }
}

/** 评论行扩展动作：body 带 topicId + commentId，路径校验与主题动作相同 */
export function forumCommentExtensionActionRequest(
  action: ForumCommentExtensionAction,
  topicId: number,
  commentId: number
) {
  const path = forumTopicExtensionActionRequestPath(action)
  if (
    !path
    || !Number.isInteger(topicId)
    || topicId <= 0
    || !Number.isInteger(commentId)
    || commentId <= 0
    || !isTopicExtensionActionMethod(action.method)
  ) {
    return null
  }
  return {
    path,
    method: action.method,
    body: { topicId, commentId }
  }
}

function isTopicExtensionActionMethod(value: string): value is ForumTopicExtensionActionMethod {
  return value === 'POST' || value === 'PUT' || value === 'PATCH' || value === 'DELETE'
}

// 构建评论列表查询参数，仅写入非空字段。
export function buildForumCommentQuery(query: ForumCommentListQuery = {}) {
  const params: Record<string, string> = {}
  if (query.view === 'tree' || query.view === 'flat') {
    params.view = query.view
  }
  if (query.after?.trim()) {
    addStringQuery(params, 'after', query.after)
  } else {
    addPositiveNumberQuery(params, 'page', query.page)
  }
  addPositiveNumberQuery(params, 'perPage', query.perPage)
  return params
}

// 评论楼层是当前公开列表的顺序号，不暴露数据库自增 ID。
export function commentFloorNumber(index: number, list: Pick<ForumCommentList, 'page' | 'perPage'>) {
  if (!Number.isFinite(index)) {
    return 0
  }

  const normalizedIndex = Math.trunc(index)
  if (normalizedIndex < 0) {
    return 0
  }

  const page = Number.isFinite(list.page) ? Math.max(1, Math.trunc(list.page)) : 1
  const perPage = Number.isFinite(list.perPage) ? Math.max(1, Math.trunc(list.perPage)) : 20
  return (page - 1) * perPage + normalizedIndex + 1
}

export function commentFloorLabel(index: number, list: Pick<ForumCommentList, 'page' | 'perPage'>) {
  const floor = commentFloorNumber(index, list)
  return floor > 0 ? `#${floor}` : ''
}

// 统一取作者展示名：解析规则收口到 utils/forum/userDisplay.ts，此处只提供评论区
// 场景的 `#userId` 回退终点，保持既有签名不变。
export function forumAuthorName(user: ForumUserSummary | undefined, fallbackUserId: number) {
  return userDisplayName(user, `#${fallbackUserId}`)
}

// 扁平化评论树：把 tree 视图返回的嵌套 children 拍平成一维列表。
// 扁平列表布局（无缩进无树）：每条评论平铺，回复对象靠各自的 replyTo 引用块表达。
//
// 规则：
// - 根评论（depth=0）照常保留；非根评论如果后端没给 replyTo，
//   用其父评论补一个 replyTo（人名 + 摘要），保证每条回复都能看出"回复的是谁"。
// - 拍平顺序按 children 原始顺序深度优先（保留讨论时间线）。
export function flattenCommentTree(roots: ForumComment[]): ForumComment[] {
  const result: ForumComment[] = []
  const walk = (list: ForumComment[], parent: ForumComment | null) => {
    for (const c of list) {
      // 非根评论且后端未填充 replyTo：用父评论补上引用信息（author 保持完整对象）。
      if (parent && !c.replyTo) {
        result.push({
          ...c,
          replyTo: {
            id: parent.id,
            author: parent.author,
            excerpt: parent.content.excerpt,
            depth: parent.depth
          }
        })
      } else {
        result.push(c)
      }
      if (c.children && c.children.length > 0) {
        walk(c.children, c)
      }
    }
  }
  walk(roots, null)
  return result
}

function addStringQuery(query: Record<string, string>, key: string, value: string | undefined) {
  const normalized = value?.trim()
  if (normalized) {
    query[key] = normalized
  }
}

function addPositiveNumberQuery(query: Record<string, string>, key: string, value: number | undefined) {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return
  }

  const normalized = Math.trunc(value)
  if (normalized > 0) {
    query[key] = String(normalized)
  }
}
