import { reactive, ref, type Ref } from 'vue'
import { useForumApi } from '~/composables/forum/useForumApi'
import {
  commentListChanged,
  commentLiveAppendLimit,
  decideCommentLive,
  type CommentLiveContext,
  type ForumCommentRevisionState
} from '~/utils/forum/commentLiveDecision'
import type { ForumComment, ForumCommentList } from '~/utils/forum/forumTaxonomy'
import { createRevisionStreamClient } from '~/utils/realtime/revisionStream'

export type CommentLiveNotice = 'none' | 'new' | 'changed'

type TopicCommentLiveOptions = {
  /** 已解析的主题 id；0 表示尚未确定（纯 slug 路径）。 */
  topicId: Ref<number>
  /** 当前已渲染的评论列表（useAsyncData 数据，本 composable 会就地替换）。 */
  list: Ref<ForumCommentList>
  /** 当前页码与总页数（决定能否自动追加）。 */
  page: Ref<number>
  totalPages: Ref<number>
  /** 编辑器/回复抽屉是否打开：打开时绝不改动列表 DOM。 */
  composerOpen: Ref<boolean>
  /** 当前登录用户 id；用于识别「刚发布的是自己的评论」。 */
  currentUserId: Ref<number | undefined>
  /** 复用页面已有的评论刷新（同一 useAsyncData key，SSR/客户端同源）。 */
  refresh: () => Promise<unknown>
  /** 页码 → 路径（跳转到最后一页复用页面既有实现）。 */
  pageTo: (page: number) => string
  /** 是否允许订阅：隐藏/删除/锁定主题，或访客禁读时为 false。 */
  enabled: Ref<boolean>
}

// 模块级单例：同一浏览器内多标签页通过 BroadcastChannel + Web Locks 共用一条 SSE 连接。
const commentStreamClient = createRevisionStreamClient()

const SCROLL_BOTTOM_THRESHOLD_PX = 120
const OWN_COMMENT_FRESH_MS = 15_000
const RECONCILE_FALLBACK_MS = 30_000

/**
 * 主题评论区免刷新加载：
 * 服务端只发「修订号变了」的轻信号（SSE），这里用 revision 事实对账，
 * 再按阅读状态决定静默追加、出胶囊提示，还是只做静默对齐。
 *
 * 契约：本 composable 返回的是 **reactive 对象**，消费方必须整体持有、**不得解构**。
 * reactive 在属性读取时会把内部的 ref 解包成一次性快照，一旦消费方写下
 * `const { notice } = useTopicCommentLive(...)`，拿到的就是初始值，之后再也读不到变更。
 * 同类事故参考 `useModerationReportDialog`：它当年也是 reactive 包装 + 页面解构，
 * 导致公开页「举报」按钮点了完全没反应。
 *
 * 正确用法：整体持有（`const commentLive = useTopicCommentLive(...)`），在模板里读
 * `commentLive.notice`；需要子组件消费时传对象或用 `toRefs` 显式转回 ref。
 */
export function useTopicCommentLive(options: TopicCommentLiveOptions) {
  const forumApi = useForumApi()
  const apiBaseUrl = String(useRuntimeConfig().public.apiBaseUrl || '/api/v1').replace(/\/+$/, '')

  const notice = ref<CommentLiveNotice>('none')
  const newCount = ref(0)
  const jumpToLastPage = ref(false)
  // 两个修订号必须分开：streamRevision 是 SSE 续传游标（传输层会立刻推进它），
  // appliedRevision 是「已经对账过的版本」。决策只能看后者，否则传输层先把修订号
  // 推到新值，对账时就会得出 remote <= local 而漏掉本次变化。
  const streamRevision = ref(0)
  const appliedRevision = ref(0)
  const nearBottom = ref(true)
  let stopStream: (() => void) | null = null
  let reconciling = false

  function liveContext(): CommentLiveContext {
    return {
      isLastPage: options.page.value >= Math.max(1, options.totalPages.value),
      nearBottom: nearBottom.value,
      composerOpen: options.composerOpen.value,
      selfJustPosted: lastItemIsOwnFreshComment(),
      autoAppendLimit: commentLiveAppendLimit(options.list.value?.perPage || 20)
    }
  }

  // 自己刚发布的评论会成为本地列表末条：此时服务端修订信号是自己的写入引起的，
  // 不该提示成「有新评论」。用末尾条目身份 + 发表时间派生，无需在发布路径埋钩子。
  function lastItemIsOwnFreshComment(): boolean {
    const items = options.list.value?.items || []
    const last = items[items.length - 1]
    const viewerId = options.currentUserId.value
    if (!last || !viewerId || last.authorUserId !== viewerId) return false
    const createdAt = Date.parse(last.createdAt || '')
    return Number.isFinite(createdAt) && Date.now() - createdAt < OWN_COMMENT_FRESH_MS
  }

  function clearNotice() {
    notice.value = 'none'
    newCount.value = 0
    jumpToLastPage.value = false
  }

  async function scrollToStreamEnd() {
    if (!import.meta.client) return
    await nextTick()
    document.querySelector('.sforum-topic-comments__stream')?.scrollIntoView({ behavior: 'smooth', block: 'end' })
    computeNearBottom()
  }

  // 桌面宽度下中心列（.sforum-topic-page__main）是内层滚动容器，window/document 的
  // 滚动度量看不到它。这里用评论流底边与视口的相对位置判断「是否贴近底部」，
  // 并在捕获阶段监听滚动（内层 scroll 不冒泡）。
  function computeNearBottom() {
    if (!import.meta.client) return
    const stream = document.querySelector('.sforum-topic-comments__stream')
    if (stream) {
      nearBottom.value = stream.getBoundingClientRect().bottom
        <= window.innerHeight + SCROLL_BOTTOM_THRESHOLD_PX
      return
    }
    const doc = document.scrollingElement || document.documentElement
    nearBottom.value = doc.scrollHeight - doc.scrollTop - doc.clientHeight <= SCROLL_BOTTOM_THRESHOLD_PX
  }

  function openPill(count: number, jump: boolean) {
    notice.value = 'new'
    newCount.value = count
    jumpToLastPage.value = jump
  }

  /** 用修订事实对账：SSE 帧直接带事实，连接建立/断开与兜底轮询走 revision 端点。 */
  async function reconcile(state?: ForumCommentRevisionState) {
    if (reconciling || !import.meta.client || options.topicId.value <= 0) return
    reconciling = true
    try {
      const fact = state || await forumApi.getCommentRevision(options.topicId.value)
      const decision = decideCommentLive(
        { revision: fact.revision, commentCount: fact.commentCount },
        {
          total: options.list.value?.total ?? 0,
          lastItemId: options.list.value?.items?.at(-1)?.id ?? 0,
          revision: appliedRevision.value
        },
        liveContext()
      )
      // 无论决策如何都推进「已对账」修订号：同一帧不应被反复对账。
      appliedRevision.value = Math.max(appliedRevision.value, fact.revision)
      if (decision.kind === 'ignore') return
      if (decision.kind === 'pill-new') {
        openPill(decision.newCount, decision.jumpToLastPage)
        return
      }
      const previous: ForumComment[] = options.list.value?.items || []
      await options.refresh()
      if (decision.kind === 'append') {
        clearNotice()
        if (nearBottom.value && !options.composerOpen.value) {
          await scrollToStreamEnd()
        }
        computeNearBottom()
        return
      }
      // 数量没变（编辑/隐藏/回复数变化）：只有当前页真的变了才提示，避免噪声。
      if (commentListChanged(previous, options.list.value?.items || [])) {
        notice.value = 'changed'
        newCount.value = 0
        jumpToLastPage.value = false
      }
    } catch {
      // 对账失败（离线/后端不可用）不打断阅读；流重连与兜底轮询会再次尝试。
    } finally {
      reconciling = false
    }
  }

  function extractRevision(payload: unknown): number | null {
    const value = Number((payload as { revision?: unknown } | null)?.revision)
    return Number.isSafeInteger(value) && value >= 0 ? value : null
  }

  function revisionStateFromPayload(payload: unknown): ForumCommentRevisionState | undefined {
    if (!payload || typeof payload !== 'object') return undefined
    const candidate = payload as Partial<ForumCommentRevisionState>
    if (extractRevision(payload) === null || !Number.isFinite(Number(candidate.commentCount))) {
      return undefined
    }
    return {
      revision: Number(candidate.revision),
      commentCount: Number(candidate.commentCount),
      lastCommentId: candidate.lastCommentId,
      lastCommentCreatedAt: candidate.lastCommentCreatedAt
    }
  }

  function startStream() {
    if (!import.meta.client || stopStream || !options.enabled.value || options.topicId.value <= 0) return
    const topicId = options.topicId.value
    stopStream = commentStreamClient.subscribe({
      coordinationKey: `sforum:comments:revision:topic:${apiBaseUrl}:${topicId}`,
      streamUrl: currentRevision => `${apiBaseUrl}/topics/${topicId}/comments/stream?revision=${currentRevision}`,
      eventName: 'revision',
      revision: streamRevision,
      extractRevision,
      // SSE 帧、连接建立/断开、兜底轮询都汇聚到同一次对账。
      refresh: payload => reconcile(revisionStateFromPayload(payload)),
      fallbackIntervalMs: RECONCILE_FALLBACK_MS
    })
  }

  function stopStreamNow() {
    stopStream?.()
    stopStream = null
  }

  /** 用户点击胶囊：追加并滚到最新，或跳到最后一页（两种胶囊语义）。 */
  async function applyPending() {
    const jump = jumpToLastPage.value
    const changed = notice.value === 'changed'
    clearNotice()
    if (jump) {
      await navigateTo(options.pageTo(Math.max(1, options.totalPages.value)))
      return
    }
    await options.refresh()
    if (!changed) {
      await scrollToStreamEnd()
    }
    computeNearBottom()
  }

  function dismiss() {
    clearNotice()
  }

  function trackScroll() {
    if (!import.meta.client) return
    computeNearBottom()
    window.addEventListener('scroll', computeNearBottom, { passive: true, capture: true })
    window.addEventListener('resize', computeNearBottom, { passive: true })
    onBeforeUnmount(() => {
      window.removeEventListener('scroll', computeNearBottom, { capture: true })
      window.removeEventListener('resize', computeNearBottom)
    })
  }

  if (import.meta.client) {
    onMounted(() => {
      trackScroll()
      startStream()
    })
    // 纯 slug 路径下主题 id 后到；id/enabled 变化时重连到正确主题并重置本地修订号。
    watch([() => options.topicId.value, () => options.enabled.value], () => {
      stopStreamNow()
      streamRevision.value = 0
      appliedRevision.value = 0
      clearNotice()
      startStream()
    })
    onBeforeUnmount(stopStreamNow)
  }

  return reactive({
    notice,
    newCount,
    jumpToLastPage,
    /** 手动对账（点击胶囊或离线兜底）。 */
    apply: applyPending,
    dismiss
  })
}

if (import.meta.hot) {
  import.meta.hot.dispose(() => commentStreamClient.stopAll())
}
