<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { useModerationReportDialog } from '~/composables/moderation/useModerationReportDialog'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useForumApi } from '~/composables/forum/useForumApi'
import { useLegacyTopicCommentComposerParent, useTopicCommentComposerDrawer } from '~/composables/forum/useTopicCommentComposerDrawer'
import { useTopicCommentAnchor } from '~/composables/forum/useTopicCommentAnchor'
import { useTopicCommentLive } from '~/composables/forum/useTopicCommentLive'
import { useTopicSelectionQuoteReply } from '~/composables/forum/useTopicSelectionQuoteReply'
import { useForumImageViewer } from '~/composables/forum/useForumImageViewer'
import SFReportDialog from '~/components/moderation/SFReportDialog.vue'
import SFTopicSideCard from '~/components/forum/SFTopicSideCard.vue'
import SFTopicMobileInfoDrawer from '~/components/forum/SFTopicMobileInfoDrawer.vue'
import SFTopicReplyComposer from '~/components/forum/SFTopicReplyComposer.vue'
import SFTopicCommentComposerDrawer from '~/components/forum/SFTopicCommentComposerDrawer.vue'
import SFTopicHeading from '~/components/forum/SFTopicHeading.vue'
import SFTopicReactionBar from '~/components/forum/SFTopicReactionBar.vue'
import SFTopicActionMenu from '~/components/forum/SFTopicActionMenu.vue'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFContentColumnFooter from '~/components/forum/SFContentColumnFooter.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFComment from '~/components/forum/SFComment.vue'
import SFCommentStreamNotice from '~/components/forum/SFCommentStreamNotice.vue'
import SFMentionContent from '~/components/forum/SFMentionContent.vue'
import SFUserMentionPreview from '~/components/forum/SFUserMentionPreview.vue'
import SFSelectionQuoteAction from '~/components/forum/SFSelectionQuoteAction.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
import { buildAuthPageLink } from '~/utils/identity/authReturn'
/**
 * 宿主 body 岛：forum.topic.show。主题 L1 挂载；路由页仅 outlet + fail-closed 回退。
 */
import {
  commentFloorLabel, forumAuthorName, forumCategoryPath,
  forumTagPath, forumTopicEditPath,
  forumTopicExtensionActionLabel, forumTopicPath, forumUserProfilePath,
  parseTopicPath, topicPathLookupCandidates, FORUM_TOPIC_ACTIONS,
  type ForumCategoryGroup, type ForumComment,
  type ForumCommentExtensionAction, type ForumCommentList,
  type ForumTopicDetail, type ForumTopicExtensionAction,
  type TopicPathLookup
} from '~/utils/forum/forumTaxonomy'
import { buildCommentActionLabels, buildCommentActionMenuItems, buildTopicActionLabels, buildTopicActionMenuItems } from '~/utils/forum/forumTopicPresentation'
import { useForumContentTime } from '~/composables/forum/useForumContentTime'
import { useTopicCommentClipboard } from '~/composables/forum/useTopicCommentClipboard'
import { useTopicReaction } from '~/composables/forum/useTopicReaction'
const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()
const { openForumImageViewer } = useForumImageViewer()
const localePath = useLocalePath()
const { seoSettings, webOption, reactionsEnabled, bookmarksEnabled } = useWebOptions()
// 当前帖子 URL 形态：决定 catch-all 解析方式与规范化目标。
const topicUrlMode = computed(() => seoSettings.value.topicUrlMode)
const { publishedTime, updatedTime, commentMeta } = useForumContentTime()
const forumApi = useForumApi()
const { can, canEditTopic, canDeleteTopic } = usePermissions()
const { user: reportUser } = useAuthSession()
const toast = useToast()
const { copyCommentLink, copyCommentShortcode } = useTopicCommentClipboard()
const replyActorName = computed(() => userDisplayName(reportUser.value, ''))

function showSuccessToast(title: string) {
  toast.add({ color: 'success', icon: 'i-lucide-check', title, duration: 5000 })
}
// 未登录访客：评论区块照常展示，但用登录引导替代回复编辑器；登录后跳回当前帖子。
const isGuest = computed(() => !reportUser.value)
const guestLoginTo = computed(() => buildAuthPageLink(localePath('/login'), route.fullPath))
const deletingCommentId = ref<number | null>(null)

// catch-all 路由参数解析：按当前 mode 把 /t/<...> 段解析为定位键。
// id_slug → { topicId, slug }；id → { topicId }；slug → { slug }。
// [...path] 恒为数组，但 vue-router 类型宽松，统一归一为 string[]。
const pathSegments = computed<string[]>(() => {
  const raw = route.params.path
  if (Array.isArray(raw)) {
    return raw
  }
  return raw ? [String(raw)] : []
})
const parsedPath = computed(() => parseTopicPath(pathSegments.value, topicUrlMode.value))
const topicLookups = computed(() => topicPathLookupCandidates(pathSegments.value, topicUrlMode.value))
const topicLookupKey = computed(() => topicLookups.value.map((item) => {
  if (item.kind === 'id') {
    return `id:${item.topicId}`
  }
  return `slug:${item.slug}`
}).join('|'))
const topicID = computed(() => {
  const fromPath = parsedPath.value?.topicId
  if (fromPath && fromPath > 0) {
    return fromPath
  }
  const idLookup = topicLookups.value.find((item): item is Extract<TopicPathLookup, { kind: 'id' }> => item.kind === 'id')
  return idLookup?.topicId ?? 0
})
// URL 已带数字 id 时（id / id_slug 模式）可与评论并行拉取；纯 slug 需等详情 resolve。
const urlTopicID = computed(() => {
  const fromPath = topicLookups.value.find((item): item is Extract<TopicPathLookup, { kind: 'id' }> => (
    item.kind === 'id' && item.topicId > 0
  ))
  return fromPath?.topicId ?? 0
})

// 评论分页页码来源（优先级递减）：
//   1. URL 显式页码（路径段 /page/N，回退旧 query ?page=N 兼容）
//   2. SSR 反查结果（带 #comment-{id} 锚点进入时，由后端算出目标页）
//   3. 默认 1
// 设计为只读 computed；翻页改走 commentPageTo（navigateTo 到新 URL），触发重新解析。
// 这样 SSR 阶段就能确定正确页码，首屏 HTML 直接渲染目标页评论（零闪屏）。

// 从 URL 解析显式页码：路径段优先（/page/N），回退 query.page（旧链接兼容）。
// 二者均无时返回 0 表示"未显式指定"，交由 SSR 反查决定。
const explicitCommentPage = computed(() => {
  // 路径段：parseTopicPath 已剥离 page/N 并返回 page（1 = 无显式段）。
  const fromPath = parsedPath.value?.page
  if (fromPath && fromPath > 0) return fromPath
  // 旧 query 形式 ?page=N（规范化前的兼容入口）。
  // 严格解析：非法值（?page=abc / 数组）不能回落成"显式第 1 页"，
  // 否则会错误抑制锚点反查，第 2 页之后的目标评论永远定位不到。
  const fromQuery = parseExplicitPublicPage(route.query.page)
  if (fromQuery > 0) {
    return fromQuery
  }
  return 0
})

// 目标评论 id：仅识别 #comment-<正整数> 锚点，用于跨页精确定位。
// route.hash 在 SPA 导航下可靠；但整页刷新时 SSR 拿不到 fragment（不发服务器），
// hydration 后 route.hash 可能为空，故客户端额外读 window.location.hash 兜底。
const clientLocationHash = ref('')
const legacyComposerParentId = useLegacyTopicCommentComposerParent()
const targetCommentId = computed(() => {
  const sources = [route.hash, clientLocationHash.value]
  for (const hash of sources) {
    const match = /^#comment-(\d+)$/.exec(hash)
    if (match) {
      return Number(match[1])
    }
  }
  return legacyComposerParentId.value
})

// 默认主题只提供连续时间流；回复关系由引用块表达，不再暴露树/平铺切换。
// 服务端 flat 视图按 created_at ASC, id ASC 排序：新评论恒落在列表末尾（最底楼），
// 既有评论的楼层号不因他人回复而漂移；回复目标靠每行 replyTo 引用块（可点击跳转）表达。
const commentView = ref<'flat'>('flat')

// 整页刷新兜底：fragment 不进 SSR，hydration 后 route.hash 可能为空；
// 客户端尽早同步 window.location.hash，让 targetCommentId 在反查声明前就拿到锚点。
// SPA 导航下 route.hash 已可靠，这里只是补全整页刷新的首屏缺失。
if (import.meta.client && window.location.hash) {
  clientLocationHash.value = window.location.hash
}

function emptyCommentList(): ForumCommentList {
  return { items: [], total: 0, page: 1, perPage: 20, view: commentView.value }
}

async function loadTopicFromCandidates(candidates: TopicPathLookup[]) {
  let lastNotFound: unknown
  for (const candidate of candidates) {
    try {
      // 成功即停：不要在 id 命中后再打 by-slug（否则会重复计浏览）。
      return candidate.kind === 'id'
        ? await forumApi.getTopic(candidate.topicId)
        : await forumApi.getTopicBySlug(candidate.slug)
    } catch (error) {
      if (!isTopicLookupNotFound(error)) {
        throw error
      }
      lastNotFound = error
    }
  }
  if (lastNotFound) {
    throw lastNotFound
  }
  return null
}

function isTopicLookupNotFound(error: unknown) {
  const candidate = error as { statusCode?: unknown, status?: unknown, response?: { status?: unknown } }
  return candidate.statusCode === 404 || candidate.status === 404 || candidate.response?.status === 404
}

// 按 URL 候选顺序加载主题：当前 mode 的规范形态优先，旧的 id/id+slug/slug 链接作为回退。
// D3：一次导航只应成功打一次公开详情 GET（浏览计数副作用在 GET 上；无 POST /view）。
// useAsyncData 在 SSR 与客户端 hydration 间复用 payload，避免 onMounted 二次拉取双计。
// M4：URL 含 id 时 topic+comments Promise.all 并行；纯 slug 时评论等详情 id。
const topicAsync = useAsyncData(
  () => `forum-topic-${topicUrlMode.value}-${topicLookupKey.value}`,
  () => loadTopicFromCandidates(topicLookups.value),
  {
    // 后端对 hidden/deleted 主题返回 404，这里正常抛错由 error 页处理。
    default: () => null as ForumTopicDetail | null
  }
)

// 评论页码反查：带 #comment-{id} 锚点且 URL 未显式指定页码时，
// 由后端算出目标评论所在页，让 SSR 首屏直接渲染该页评论（零闪屏定位）。
// 失败（评论不存在/软删/跨主题）静默降级 page=1，不阻断渲染、不泄漏状态。
const commentPageResolved = useAsyncData(
  () => `forum-topic-comment-page-${urlTopicID.value > 0 ? urlTopicID.value : topicLookupKey.value}-${targetCommentId.value}`,
  async () => {
    if (targetCommentId.value <= 0 || explicitCommentPage.value > 0) {
      return null
    }
    // URL 含 id 时立即可查；纯 slug 等 topic 详情 resolve（复用 topicAsync，避免二次 GET）。
    const id = urlTopicID.value > 0
      ? urlTopicID.value
      : ((await topicAsync).data.value?.id ?? 0)
    if (id <= 0) {
      return null
    }
    try {
      return await forumApi.resolveCommentPage(id, targetCommentId.value)
    } catch {
      // 评论不存在/软删 → 后端 404，降级 page=1。
      return null
    }
  },
  {
    // 不能设 default：整页刷新时 fragment 不进 SSR，客户端 key（含锚点 id）在 payload 里
    // 没有条目；若 default 提供了非 undefined 初值，Nuxt 水合分支会视为"已有数据"而
    // 跳过 handler，反查请求永远不会发出（兜底路径静默失效）。保持 undefined 初值
    // 才能让水合阶段真正执行 handler。
    // 纯 slug 路径下 topic id 从 0→N 时重查；显式页码/锚点变化也重查。
    watch: [() => topicAsync.data.value?.id ?? 0, targetCommentId, explicitCommentPage]
  }
)

// 最终生效页码：URL 显式（路径段 /page/N 或旧 query ?page=N）> 反查 > 默认 1。
const commentPage = computed(() => {
  if (explicitCommentPage.value > 0) {
    return explicitCommentPage.value
  }
  const resolved = commentPageResolved.data.value?.page
  if (resolved && resolved > 0) {
    return resolved
  }
  return 1
})

const commentQuery = computed(() => ({
  view: commentView.value,
  page: commentPage.value
}))

// 评论 key：优先 URL id（并行稳定）；slug 模式用 lookup key，resolve 后 watch 刷新。
const commentsKeyTopic = computed(() => (urlTopicID.value > 0 ? String(urlTopicID.value) : `lookup:${topicLookupKey.value}`))

// 左栏分类导航（route 模式）：与首页共用 SFHomeNavigation，仅展示 API 目录数据。
const categoryGroupsAsync = useAsyncData(
  'forum-topic-show-category-groups',
  () => forumApi.listCategoryGroups(),
  {
    default: () => [] as ForumCategoryGroup[],
    lazy: true
  }
)

// 初始 SSR 保持正文、评论和导航完整；客户端切页只让正文阻塞导航提交。
const topicResult = await topicAsync
// 反查必须先于评论列表 resolve：commentPage 依赖反查结果，commentsAsync 又依赖 commentPage。
// SSR 和客户端导航都要等反查完成，否则评论列表会先用 page=1 发请求（闪屏/定位失败的根因）。
await commentPageResolved

// 评论列表：声明在反查之后，确保 commentPage 已是最终值（显式页码或反查结果）。
// 带目标评论锚点时 SSR 必须等评论到位（零闪屏定位）；否则 lazy 不阻塞首屏。
const commentsAsync = useAsyncData(
  () => `forum-topic-comments-${commentsKeyTopic.value}-${commentView.value}-${commentPage.value}`,
  async () => {
    if (urlTopicID.value > 0) {
      return forumApi.listTopicComments(urlTopicID.value, commentQuery.value)
    }
    // 纯 slug：复用同页 topicAsync，避免二次详情 GET（D3）。
    const resolvedTopic = await topicAsync
    const id = resolvedTopic.data.value?.id ?? 0
    if (id <= 0) {
      return emptyCommentList()
    }
    return forumApi.listTopicComments(id, commentQuery.value)
  },
  {
    default: () => emptyCommentList(),
    lazy: targetCommentId.value === 0,
    // 翻页/视图切换；slug 路径下 topic id 从 0→N 时也会触发。
    watch: [() => topicAsync.data.value?.id ?? 0, commentQuery]
  }
)

if (import.meta.server) {
  await Promise.all([commentsAsync, categoryGroupsAsync])
}
const { data: topic, error: topicError } = topicResult
const { applyReactionChange, applyBookmarkChange } = useTopicReaction(topic)
const { data: commentData, pending: commentsPending, error: commentsError, refresh: refreshComments } = commentsAsync
const showReplyEditor = computed(() => Boolean(topic.value && topic.value.status !== 'locked' && can(FORUM_PERMISSIONS.postCreate)))
const { data: categoryGroups, pending: categoriesPending } = categoryGroupsAsync
const navCategories = computed(() => categoryGroups.value.flatMap((group) => group.categories || []))
const navTotalTopics = computed(() => navCategories.value.reduce((sum, category) => sum + category.topicCount, 0))
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))
const showTopicSide = computed(() => Boolean(topic.value))
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()
const canNormalizeTopicURL = import.meta.client
  || (useRequestHeaders(['accept']).accept || '').includes('text/html')

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

// 规范化分两类，分开处理避免破坏 SSR 零闪屏：
// 1. slug/mode 不匹配（真旧链接）：SSR 301 / 客户端 replace，定位键变了必须重定向。
// 2. page 段缺失或旧 query ?page=N：数据已对（反查已算出页码），仅客户端 replace URL，
//    绝不在 SSR 301——否则反查渲染的目标页会被重定向打回，回到闪屏。
watchEffect(() => {
  if (!topic.value || !canNormalizeTopicURL) {
    return
  }
  // 1. slug/mode 规范化：比较不含 page 的基础路径。
  //    必须保留 hash 与 page 段：通知深链 /t/{id}/page/{N}#comment-{id} 在 id_slug/slug
  //    模式下必走本分支；丢 page 会让跨页通知退化为只定位第 1 页，丢 hash 则滚动/高亮失效。
  const basePath = localePath(forumTopicPath(topic.value, topicUrlMode.value))
  const routeBase = route.path.replace(/\/page\/\d+$/, '')
  if (basePath !== routeBase) {
    const target = { path: localePath(forumTopicPath(topic.value, topicUrlMode.value, commentPage.value)), hash: route.hash }
    if (import.meta.server) {
      navigateTo(target, { redirectCode: 301 })
    } else {
      navigateTo(target, { replace: true })
    }
    return
  }
  // 2. page 段规范化：仅客户端 replace（补 /page/N 或迁移旧 query ?page=N）。
  //    SSR 不处理——首屏 HTML 已含正确页评论，URL 在 hydration 后静默修正。
  if (import.meta.client) {
    const targetPath = localePath(forumTopicPath(topic.value, topicUrlMode.value, commentPage.value))
    const hasOldQueryPage = route.query.page !== undefined && route.query.page !== null
    if (targetPath !== route.path || hasOldQueryPage) {
      navigateTo({ path: targetPath, hash: route.hash }, { replace: true })
    }
  }
})

// 锚点定位与「回复后落到新评论」由 useTopicCommentAnchor 承担（见下方调用），
// 页面只负责把 URL/分页状态与列表数据接进去。

// canonical 用当前 mode 的规范路径（含页码段，与规范化目标一致）。
const canonicalTopicPath = computed(() => topic.value ? forumTopicPath(topic.value, topicUrlMode.value, commentPage.value) : route.path)
const canonicalPath = computed(() => canonicalTopicPath.value)

useSForumSeo(computed(() => ({
  type: 'topic',
  path: canonicalPath.value,
  title: topic.value?.title || '',
  excerpt: topic.value?.content.excerpt || t('topicDetail.metaDescription'),
  public: Boolean(topic.value && (topic.value.status === 'active' || topic.value.status === 'locked')),
  published: Boolean(topic.value),
  noindex: !topic.value,
  variables: {
    topicTitle: topic.value?.title,
    categoryName: topic.value?.categoryName,
    authorName: topic.value ? forumAuthorName(topic.value.author, topic.value.authorUserId) : undefined
  },
  datePublished: topic.value?.createdAt,
  dateModified: topic.value?.updatedAt,
  authorName: topic.value ? forumAuthorName(topic.value.author, topic.value.authorUserId) : undefined,
  breadcrumbs: topic.value ? [
    { name: seoSettings.value.seoSiteName, path: '/' },
    { name: topic.value.categoryName, path: forumCategoryPath(topic.value.categorySlug) },
    { name: topic.value.title, path: canonicalPath.value }
  ] : []
})))

// 翻页目标：路径式 /page/N（page=1 省略），navigateTo 后触发 commentPage 重新解析。
function commentPageTo(page: number) {
  const base = topic.value ? forumTopicPath(topic.value, topicUrlMode.value, page) : route.path
  return localePath(base)
}

// 已加载主题 id（动作/回复路径）；列表拉取优先 urlTopicID 以支持并行。
const loadedTopicID = computed(() => topic.value?.id ?? topicID.value)

// 评论锚点定位 + 回复后落到新评论（时间流末尾/跨页），实现见 composable。
const { flashCommentId, focusCreatedComment } = useTopicCommentAnchor({
  targetCommentId,
  commentPage,
  topicId: loadedTopicID,
  commentList: commentData,
  commentsPending,
  refreshComments,
  pageTo: commentPageTo
})

const comments = computed(() => commentData.value.items)
const commentTotal = computed(() => commentData.value.total)
const commentTotalPages = computed(() => Math.ceil(commentTotal.value / Math.max(commentData.value.perPage, 1)) || 1)
const commentFloorLabelsById = computed(() => new Map<number, string>(
  comments.value.map((comment, index) => [comment.id, commentFloorLabel(index, commentData.value)])
))
// E2.2：列表级评论扩展动作；requiresAuth 仅 UX 过滤，鉴权在扩展路由代理。
const commentExtensionActions = computed(() => commentData.value?.extensionActions || [])
const commentExtensionActionRunning = ref('')
/** D2：正在通过 ListCommentReplies 补全子孙的评论 id */
const loadingMoreCommentId = ref<number | null>(null)

const authorName = computed(() => topic.value ? forumAuthorName(topic.value.author, topic.value.authorUserId) : '')
const authorPath = computed(() => {
  if (!topic.value?.author?.username) {
    return ''
  }
  return localePath(forumUserProfilePath(topic.value.author.username))
})

function tagPath(slug: string) { return localePath(forumTagPath(slug)) }

function commentFloor(comment: ForumComment) { return commentFloorLabelsById.value.get(comment.id) || '' }

function categoryPath(slug: string) { return localePath(forumCategoryPath(slug)) }

const headingTags = computed(() => (topic.value?.tags || []).map(tag => ({
  id: tag.id,
  name: tag.name,
  to: tagPath(tag.slug)
})))

function commentAuthorName(comment: ForumComment) {
  return forumAuthorName(comment.author, comment.authorUserId)
}

const {
  mode: composerMode,
  open: composerOpen,
  context: composerContext,
  modelValue: composerModelValue,
  initialContent: composerInitialContent,
  submitting: composerSubmitting,
  error: composerError,
  editorKey: composerEditorKey,
  editingReason,
  editingReasonError,
  editingAnotherAuthor,
  commentCooldownActive,
  replyError,
  showReplyError,
  startEdit: startEditComment,
  startReply,
  openAdvancedReply,
  updateOpen: updateComposerOpen,
  updateModelValue: updateComposerModel,
  updateReason: updateComposerReason,
  dismissError: dismissComposerError,
  submit: submitComposer
} = useTopicCommentComposerDrawer({
  topic,
  comments,
  currentUserId: computed(() => reportUser.value?.id),
  legacyParentId: legacyComposerParentId,
  refreshComments,
  commentAuthorName,
  commentFloor,
  focusCreatedComment
})

// 评论区免刷新加载：SSE 只发修订信号，本 composable 对账后决定静默追加/出胶囊/静默对齐。
// 锁定或未公开主题不会再有新评论，直接不订阅。
const commentLive = useTopicCommentLive({
  topicId: loadedTopicID,
  list: commentData,
  page: commentPage,
  totalPages: commentTotalPages,
  composerOpen,
  currentUserId: computed(() => reportUser.value?.id),
  refresh: refreshComments,
  pageTo: commentPageTo,
  enabled: computed(() => Boolean(topic.value && topic.value.status === 'active'))
})

function commentAuthorPath(comment: ForumComment) {
  if (!comment.author?.username) {
    return ''
  }
  return localePath(forumUserProfilePath(comment.author.username))
}

// 主题生命周期动作。前端仅做 UX 提示，后端 policy 是权威。
type ActionState = 'idle' | 'pending' | 'error'
const actionState = ref<ActionState>('idle')
const actionError = ref('')
const showActionError = ref(false)
const allowAuthorCloseReplies = computed(() => normalizeEnabledOption(
  webOption('forum.topics.allow_author_close_replies', 'enabled'),
  true
))
const canLock = computed(() => Boolean(
  can(FORUM_PERMISSIONS.topicLock) || (
    allowAuthorCloseReplies.value &&
    topic.value?.authorUserId === reportUser.value?.id &&
    can(FORUM_PERMISSIONS.topicEditOwn)
  )
))
const canPin = computed(() => can(FORUM_PERMISSIONS.topicPin))
const canModerate = computed(() => can(FORUM_PERMISSIONS.topicDeleteAny))
const isLocked = computed(() => topic.value?.status === 'locked')
const isPinned = computed(() => Boolean(topic.value?.isPinned))
const extensionActions = computed(() => topic.value?.extensionActions || [])
const extensionActionRunning = ref('')

async function runTopicAction(action: keyof typeof FORUM_TOPIC_ACTIONS, successMessageKey: string) {
  if (!topic.value) {
    return
  }
  actionState.value = 'pending'
  actionError.value = ''
  showActionError.value = false
  try {
    await forumApi.applyTopicAction(topic.value.id, action)
    // 刷新主题以拿到最新状态。
    topic.value = await forumApi.getTopic(topic.value.id)
    showSuccessToast(t(successMessageKey))
  } catch (error) {
    actionState.value = 'error'
    actionError.value = apiErrorMessage(error) || t('topicDetail.actionFailed')
    showActionError.value = true
    return
  }
  actionState.value = 'idle'
}

function topicExtensionActionKey(action: ForumTopicExtensionAction) {
  return `${action.extensionId}:${action.id}`
}

function topicExtensionActionLabel(action: ForumTopicExtensionAction) {
  return forumTopicExtensionActionLabel(action, String(locale.value || 'zh-CN'))
}

async function runTopicExtensionAction(action: ForumTopicExtensionAction) {
  if (!topic.value) {
    return
  }
  const label = topicExtensionActionLabel(action)
  if (action.confirm && !window.confirm(t('topicDetail.confirmExtensionAction', { action: label }))) {
    return
  }
  actionState.value = 'pending'
  actionError.value = ''
  showActionError.value = false
  extensionActionRunning.value = topicExtensionActionKey(action)
  try {
    await forumApi.applyTopicExtensionAction(topic.value.id, action)
    topic.value = await forumApi.getTopic(topic.value.id)
    showSuccessToast(t('topicDetail.extensionActionCompleted'))
  } catch (error) {
    actionState.value = 'error'
    actionError.value = apiErrorMessage(error) || t('topicDetail.extensionActionFailed')
    showActionError.value = true
    return
  } finally {
    extensionActionRunning.value = ''
  }
  actionState.value = 'idle'
}

async function deleteTopic() {
  if (!topic.value) {
    return
  }
  if (!window.confirm(t('topicDetail.confirmDelete'))) {
    return
  }
  actionState.value = 'pending'
  actionError.value = ''
  showActionError.value = false
  try {
    await forumApi.deleteTopic(topic.value.id)
    showSuccessToast(t('topicDetail.topicDeleted'))
    await navigateTo(localePath('/'))
  } catch (error) {
    actionState.value = 'error'
    actionError.value = apiErrorMessage(error) || t('topicDetail.actionFailed')
    showActionError.value = true
  }
}

function commentActions(comment: ForumComment) {
  return buildCommentActionMenuItems({
    canReply: canReplyToComments.value,
    canEdit: isCommentEditable(comment),
    canDelete: isCommentDeletable(comment),
    canReport: canReportComment(),
    labels: buildCommentActionLabels(t, { deleting: deletingCommentId.value === comment.id }),
    extensions: visibleCommentExtensionActions.value.map(action => ({
      label: forumTopicExtensionActionLabel(action, String(locale.value || 'zh-CN')),
      value: `extension:${action.extensionId}:${action.id}`,
      icon: action.icon
    }))
  })
}

const visibleCommentExtensionActions = computed(() => {
  const loggedIn = Boolean(reportUser.value)
  return commentExtensionActions.value.filter(action => !action.requiresAuth || loggedIn)
})

function commentExtensionActionKey(action: ForumCommentExtensionAction) {
  return `${action.extensionId}:${action.id}`
}

async function runCommentExtensionAction(comment: ForumComment, action: ForumCommentExtensionAction) {
  if (!topic.value) {
    return
  }
  const label = forumTopicExtensionActionLabel(action, String(locale.value || 'zh-CN'))
  if (action.confirm && !window.confirm(t('topicDetail.confirmExtensionAction', { action: label }))) {
    return
  }
  commentExtensionActionRunning.value = commentExtensionActionKey(action)
  try {
    await forumApi.applyCommentExtensionAction(topic.value.id, comment.id, action)
    showSuccessToast(t('topicDetail.extensionActionCompleted'))
  } catch (error) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-alert-circle',
      title: apiErrorMessage(error) || t('topicDetail.extensionActionFailed'),
      duration: 0
    })
  } finally {
    commentExtensionActionRunning.value = ''
  }
}

// 评论行内的"举报"按钮（独立于 actions，避免占用回复入口）。
function canReportComment() {
  return Boolean(reportUser.value)
}

// 评论是否可被当前用户编辑/删除。
const { canEditComment, canDeleteComment } = usePermissions()
function isCommentEditable(comment: ForumComment) {
  return canEditComment(comment)
}
function isCommentDeletable(comment: ForumComment) {
  return canDeleteComment(comment)
}

function handleCommentClick(comment: ForumComment, value: string) {
  // 评论操作分发：由 SFComment 的 actions 触发，替代之前硬编码在模板里的按钮。
  if (value === 'reply') {
    startReply(comment)
  } else if (value === 'link' || value === 'quote-shortcode') {
    void (value === 'link' ? copyCommentLink : copyCommentShortcode)(comment)
  } else if (value === 'edit') {
    startEditComment(comment)
  } else if (value === 'delete') {
    deleteComment(comment)
  } else if (value === 'report') {
    openReportDialog({ type: 'comment', id: comment.id })
  } else if (value.startsWith('extension:')) {
    const action = visibleCommentExtensionActions.value.find(
      candidate => value === `extension:${candidate.extensionId}:${candidate.id}`
    )
    if (action) {
      void runCommentExtensionAction(comment, action)
    }
  }
}

// 回复：仅在主题未锁定且当前用户有 post.create 时允许。
const canReplyToComments = computed(() => Boolean(topic.value && topic.value.status !== 'locked' && can(FORUM_PERMISSIONS.postCreate)))
const handleSelectionQuote = useTopicSelectionQuoteReply({ comments, canReply: canReplyToComments, startReply, openTopicReply: openAdvancedReply })

// 评论删除（软删）。
async function deleteComment(comment: ForumComment) {
  if (deletingCommentId.value) {
    return
  }
  if (!window.confirm(t('topicDetail.confirmCommentDelete'))) {
    return
  }
  deletingCommentId.value = comment.id
  try {
    await forumApi.deleteComment(comment.id)
    await refreshComments()
    showSuccessToast(t('topicDetail.commentDeleted'))
  } catch (error) {
    replyError.value = apiErrorMessage(error) || t('topicDetail.deleteFailed')
    showReplyError.value = true
  } finally {
    deletingCommentId.value = null
  }
}

// D2：树视图截断后，用 ListCommentReplies 合并直系回复到本地树。
function mergeCommentReplies(items: ForumComment[], parentId: number, replies: ForumComment[]): ForumComment[] {
  return items.map((item) => {
    if (item.id === parentId) {
      const existing = new Map((item.children || []).map(child => [child.id, child]))
      const merged: ForumComment[] = []
      for (const reply of replies) {
        const prev = existing.get(reply.id)
        merged.push(prev ? { ...reply, children: prev.children, hasMoreChildren: prev.hasMoreChildren } : reply)
        existing.delete(reply.id)
      }
      // 保留 API 未返回、但本地已有的深层节点（path 序可能被 cap 截断过）。
      for (const leftover of existing.values()) {
        merged.push(leftover)
      }
      return { ...item, children: merged, hasMoreChildren: false }
    }
    if (item.children?.length) {
      return { ...item, children: mergeCommentReplies(item.children, parentId, replies) }
    }
    return item
  })
}

async function loadMoreCommentReplies(comment: ForumComment) {
  if (loadingMoreCommentId.value === comment.id) {
    return
  }
  loadingMoreCommentId.value = comment.id
  try {
    const replies = await forumApi.listCommentReplies(comment.id)
    if (!commentData.value) {
      return
    }
    commentData.value = {
      ...commentData.value,
      items: mergeCommentReplies(commentData.value.items, comment.id, replies)
    }
    showSuccessToast(t('topicDetail.loadMoreReplies'))
  } catch (error) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('topicDetail.loadMoreRepliesFailed')
    })
  } finally {
    loadingMoreCommentId.value = null
  }
}

// 举报对话框状态机（目标/原因/提交/错误）见 composables/moderation/useModerationReportDialog；
// 这里保持原有短名解构，模板绑定与视觉契约不变。
const {
  target: reportingTarget,
  reason: reportReason,
  body: reportBody,
  submitting: reportSubmitting,
  error: reportError,
  success: reportSuccess,
  reasonOptions: reportReasonOptions,
  open: openReportDialog,
  close: closeReportDialog,
  setReason: updateReportReason,
  submit: submitReport
} = useModerationReportDialog({ canReport: () => Boolean(reportUser.value) })
const topicActionItems = computed(() => {
  if (!topic.value) {
    return []
  }
  return buildTopicActionMenuItems({
    canEdit: canEditTopic(topic.value),
    canDelete: canDeleteTopic(topic.value),
    canLock: canLock.value,
    canPin: canPin.value,
    canModerate: canModerate.value,
    canReport: Boolean(reportUser.value),
    locked: isLocked.value,
    pinned: isPinned.value,
    hidden: topic.value.status === 'hidden',
    labels: buildTopicActionLabels(t),
    extensions: extensionActions.value.map(action => ({
      extensionId: action.extensionId,
      id: action.id,
      label: topicExtensionActionLabel(action),
      icon: action.icon,
      confirm: action.confirm
    }))
  })
})

async function handleTopicActionSelect(id: string) {
  if (!topic.value) {
    return
  }
  if (id === 'edit') {
    // 编辑走独立页 /topics/:id/edit（forum.topic.edit）。
    await navigateTo(localePath(forumTopicEditPath(topic.value.id)))
    return
  }
  if (id === 'delete') {
    await deleteTopic()
    return
  }
  if (id === 'report') {
    openReportDialog({ type: 'topic', id: topic.value.id })
    return
  }
  if (id.startsWith('extension:')) {
    const action = extensionActions.value.find(candidate => id === `extension:${candidate.extensionId}:${candidate.id}`)
    if (action) {
      await runTopicExtensionAction(action)
    }
    return
  }
  if (id in FORUM_TOPIC_ACTIONS) {
    const successKey = id === 'lock' || id === 'unlock'
      ? 'topicDetail.lockToggled'
      : id === 'pin' || id === 'unpin'
        ? 'topicDetail.pinToggled'
        : 'topicDetail.hidden'
    await runTopicAction(id as keyof typeof FORUM_TOPIC_ACTIONS, successKey)
  }
}

function startTopLevelReply() {
  if (!showReplyEditor.value) {
    return
  }
  openAdvancedReply()
}

async function shareTopic() {
  if (!import.meta.client || !topic.value) {
    return
  }
  const url = window.location.href
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(url)
      showSuccessToast(t('topicDetail.linkCopied'))
      return
    }
  } catch {
    // 剪贴板失败时退到 prompt，避免静默失败。
  }
  window.prompt(t('topicDetail.copyLinkHint'), url)
}

</script>

<template>
  <main class="sforum-topic-page" data-layout="fullwidth-3col" @click="openForumImageViewer">
    <div
      class="sforum-topic-page__layout"
      :class="{ 'sforum-topic-page__layout--with-side': showTopicSide }"
    >
      <SFResponsivePublicSidebar
        owner-id="forum.topic.show"
        :title="t('home.sidebar.navTitle')"
        class="sforum-topic-page__sidebar"
      >
        <SFHomeNavigation
          desktop-only
          navigation-mode="route"
          :categories="navCategories"
          :selected-category-slug="topic?.categorySlug || ''"
          :total-topics="navTotalTopics"
          :pending="categoriesPending"
          :can-create-topic="canCreateTopic"
        />
      </SFResponsivePublicSidebar>

      <div class="sforum-topic-page__main sforum-content-column">
        <SFSelectionQuoteAction :enabled="canReplyToComments" @quote="handleSelectionQuote" />
        <div class="sforum-topic-page__inner">
          <SFRegionOutlet page="forum.topic.show" region="content_before" />

          <!-- 错误 / 未找到 -->
          <SFCard v-if="topicError && !topic" class="p-10">
            <SFEmptyState
              :title="t('topicDetail.notFound.title')"
              :description="t('topicDetail.notFound.description')"
            />
          </SFCard>

          <template v-else-if="topic">
            <div class="sforum-topic-page__shell">
              <div class="sforum-topic-page__reading">
                <article id="topic-start" class="sforum-topic-page__article">
                  <div class="sforum-topic-page__heading-row">
                    <SFTopicHeading
                      :topic="topic"
                      :author-name="authorName"
                      :author-to="authorPath"
                      :category-to="categoryPath(topic.categorySlug)"
                      :tags="headingTags"
                      :published-label="publishedTime(topic.createdAt)"
                      :updated-label="topic.editedAt ? updatedTime(topic.editedAt) : ''"
                      :extension-badges="topic.extensionBadges || []"
                    />
                    <SFTopicActionMenu
                      :items="topicActionItems"
                      :pending="actionState === 'pending'"
                      :running-id="extensionActionRunning ? `extension:${extensionActionRunning}` : ''"
                      @select="handleTopicActionSelect"
                    />
                  </div>
                  <SFRegionOutlet page="forum.topic.show" region="detail_before" />
                  <div class="sforum-topic-page__post-card">
                    <!-- 正文（后端已 sanitize）；v-highlight 高亮代码块，SFMentionContent 负责 @提及 链接化与预览 -->
                    <SFMentionContent v-highlight class="sforum-topic-page__prose sf-prose" data-sforum-image-gallery="topic" data-selection-quote-source="topic" :html="sanitizeHtml(topic.content.htmlContent)" />

                    <SFTopicReactionBar :topic-id="topic.id" :like-count="topic.likeCount" :liked-by-me="topic.likedByMe === true" :bookmarked="topic.bookmarkedByMe === true"
                      :liked-by="topic.likedBy" :show-reply="showReplyEditor" :reactions-enabled="reactionsEnabled" :bookmarks-enabled="bookmarksEnabled" @share="shareTopic" @reply="startTopLevelReply" @change="applyReactionChange" @bookmark="applyBookmarkChange" />

                    <SFAlert
                      v-if="showActionError"
                      variant="danger"
                      :title="actionError"
                      closable
                      class="mt-3"
                      @close="showActionError = false"
                    />
                  </div>
                </article>
                <SFRegionOutlet page="forum.topic.show" region="detail_after" />
                <section id="topic-latest" class="sforum-topic-comments">
                  <header class="sf-comment-stream-controls">
                    <div>
                      <span>{{ topic.commentCount }} REPLIES</span>
                      <h2>{{ t('topicDetail.discussionContinues') }}</h2>
                    </div>
                    <a
                      v-if="comments.length"
                      class="sf-comment-stream-controls__latest"
                      :href="`#comment-${comments[comments.length - 1]?.id}`"
                    >
                      <UIcon name="i-lucide-arrow-down" class="size-4" aria-hidden="true" />
                      {{ t('topicDetail.progress.latest') }}
                    </a>
                  </header>
                  <SFRegionOutlet page="forum.topic.show" region="comments_before" />
                  <div v-if="commentsError" class="sforum-topic-comments__error">
                    <SFAlert variant="danger" :title="t('topicDetail.commentsLoadFailed')" />
                    <SFButton variant="ghost" size="sm" @click="() => { void refreshComments() }">
                      <UIcon name="i-lucide-refresh-cw" class="size-4" aria-hidden="true" />
                      {{ t('topicDetail.retryComments') }}
                    </SFButton>
                  </div>

                  <template v-if="commentsPending && !comments.length">
                    <div v-for="i in 3" :key="i" class="sforum-topic-comments__skeleton">
                      <SFSkeleton width="20%" height="1rem" class="mb-2" />
                      <SFSkeleton width="90%" class="mb-1" />
                      <SFSkeleton width="70%" />
                    </div>
                  </template>

                  <template v-else-if="comments.length">
                    <div class="sforum-topic-comments__stream sf-comment-list">
                      <SFComment
                        v-for="comment in comments"
                        :key="comment.id"
                        :comment="comment"
                        :author="commentAuthorName(comment)"
                        :avatar="comment.author?.avatar"
                        :author-link="commentAuthorPath(comment)"
                        :html-content="comment.content.htmlContent"
                        :meta="commentMeta(comment)"
                        :floor-label="commentFloor(comment)"
                        :presentation="commentView"
                        :depth="0"
                        :collapse-from-depth="2"
                        :reply-to="comment.replyTo"
                        :actions="commentActions(comment)"
                        :comment-meta-builder="commentMeta"
                        :comment-author-link-builder="commentAuthorPath"
                        :comment-actions-builder="commentActions"
                        :loading-more-comment-id="loadingMoreCommentId"
                        :flash="flashCommentId === comment.id"
                        :op-user-id="topic.authorUserId"
                        @action-comment="(c: ForumComment, value: string) => handleCommentClick(c, value)"
                        @load-more-replies="(c: ForumComment) => { void loadMoreCommentReplies(c) }"
                      />
                    </div>

                    <SFCommentStreamNotice
                      :notice="commentLive.notice"
                      :new-count="commentLive.newCount"
                      :jump-to-last-page="commentLive.jumpToLastPage"
                      @apply="commentLive.apply()"
                      @dismiss="commentLive.dismiss()"
                    />

                    <div v-if="commentTotalPages > 1" class="flex justify-center pt-2">
                      <SFPagination
                        :page="commentPage"
                        :total-pages="commentTotalPages"
                        :page-to="commentPageTo"
                      />
                    </div>
                  </template>

                  <div v-else-if="!commentsError" class="sforum-topic-comments__empty">
                    <SFEmptyState
                      :title="t('topicDetail.emptyComments.title')"
                      :description="t('topicDetail.emptyComments.description')"
                    />
                  </div>
                  <SFTopicReplyComposer
                    v-if="showReplyEditor"
                    :topic="topic"
                    :refresh-comments="refreshComments"
                    :actor-name="replyActorName"
                    :avatar="reportUser?.avatar"
                    :focus-created-comment="focusCreatedComment"
                    @open="openAdvancedReply"
                  />
                  <div
                    v-else-if="isGuest && !isLocked"
                    class="sforum-topic-comments__guest-notice"
                  >
                    <UIcon name="i-lucide-message-circle" class="sforum-topic-comments__guest-notice-icon" aria-hidden="true" />
                    <p>{{ t('topicDetail.guestReplyNotice') }}</p>
                    <NuxtLink
                      :to="guestLoginTo"
                      class="sforum-topic-page__action-btn sforum-topic-page__action-btn--primary"
                    >
                      {{ t('topicDetail.guestReplyLogin') }}
                    </NuxtLink>
                  </div>

                  <SFAlert
                    v-if="isLocked"
                    variant="warning"
                    :title="t('topicDetail.lockedNotice')"
                    closable
                  />
                  <SFRegionOutlet page="forum.topic.show" region="comments_after" />
                </section>
              </div>
            </div>
          </template>
        </div>

        <SFRegionOutlet page="forum.topic.show" region="content_after" />

        <SFContentColumnFooter />
      </div>

      <SFTopicSideCard
        v-if="showTopicSide && topic"
        :topic="topic"
        :author-name="authorName"
        :author-to="authorPath"
        :tags="headingTags"
        :category-to="categoryPath(topic.categorySlug)"
        :first-comment-id="comments[0]?.id"
        :latest-comment-id="comments[comments.length - 1]?.id"
        :extension-sidebar="topic.extensionSidebar || []"
      >
        <SFRegionOutlet page="forum.topic.show" region="sidebar_top" />
        <SFRegionOutlet page="forum.topic.show" region="sidebar" />
        <SFRegionOutlet page="forum.topic.show" region="sidebar_bottom" />
      </SFTopicSideCard>
    </div>

    <button
      v-if="mobileInfoOpen"
      type="button"
      class="sforum-mobile-drawer__backdrop"
      :aria-label="t('topicDetail.cancel')"
      @click="closeMobileDrawers"
    />

    <SFTopicMobileInfoDrawer
      v-if="mobileInfoOpen && topic"
      :topic="topic"
      :author-name="authorName"
      :author-to="authorPath"
      :tags="headingTags"
      :category-to="categoryPath(topic.categorySlug)"
      :comments="comments"
      @close="closeMobileDrawers"
    />

    <SFTopicCommentComposerDrawer
      v-if="topic"
      :open="composerOpen"
      :mode="composerMode || 'advanced'"
      :editor-key="composerEditorKey"
      :topic-title="topic.title"
      :topic-excerpt="topic.content.excerpt"
      :actor-name="replyActorName"
      :avatar="reportUser?.avatar"
      :context="composerContext"
      :model-value="composerModelValue"
      :initial-content="composerInitialContent"
      :submitting="composerSubmitting"
      :submit-disabled="composerMode === 'edit' ? false : commentCooldownActive"
      :error="composerError"
      :error-closable="composerMode === 'edit' || !commentCooldownActive"
      :reason="editingReason"
      :require-reason="composerMode === 'edit' && editingAnotherAuthor"
      :reason-error="editingReasonError"
      @update:open="updateComposerOpen"
      @update:model-value="updateComposerModel"
      @update:reason="updateComposerReason"
      @submit="submitComposer"
      @dismiss-error="dismissComposerError"
    />

    <SFReportDialog
      :open="Boolean(reportingTarget)"
      :reasons="reportReasonOptions"
      :reason="reportReason"
      :body="reportBody"
      :submitting="reportSubmitting"
      :error="reportError"
      :success="reportSuccess"
      @update:reason="updateReportReason"
      @update:body="reportBody = $event"
      @dismiss-error="reportError = ''"
      @close="closeReportDialog"
      @submit="submitReport"
    />

    <SFUserMentionPreview />
  </main>
</template>
