import type { Ref } from 'vue'
import type { BookmarkTargetType, BookmarkVisibility } from '~/utils/forum/forumBookmark'
import {
  bookmarkErrorKey,
  bookmarkReasonKey,
  bookmarkRemindIssue,
  normalizeBookmarkVisibility,
  recommendedBookmarkVisibility
} from '~/utils/forum/forumBookmark'
import { apiErrorReason } from '~/composables/useApiClient'
import { useAuthSession } from '~/composables/identity/useAuthSession'

/**
 * 收藏前端契约的唯一集中处。
 *
 * 写响应字段与后端 `forum.BookmarkState` 同名；名单行与 `forum.BookmarkEntry` 同名。
 * 老后的 OpenAPI 落地后如果字段有出入，只改这一段类型 + `useBookmarkApi` 的路径，
 * 组件与页面不感知。
 */
export type BookmarkState = {
  targetType: BookmarkTargetType
  targetId: number
  visibility: BookmarkVisibility
  bookmarked: boolean
  /** 目标的公开收藏计数；只统计 visibility=public 的行。 */
  publicBookmarkCount: number
  /** 后端 omitempty：没有提醒时字段可能缺省，读取时按 null 处理。 */
  remindAt?: string | null
  /** 提醒已经触发的时间；有值表示「已提醒」，缺失表示仍在等待。 */
  remindedAt?: string | null
}

export type BookmarkWriteInput = {
  visibility?: BookmarkVisibility
  remindAt?: string | null
  /**
   * 清除提醒的显式语义。`remindAt: null` 在后端旧实现里不清行，
   * 所以清除必须走这个开关，不能靠「传空时间」碰运气。
   */
  clearReminder?: boolean
}

/** 名单里的主题目标摘要：只声明渲染需要的字段，其余后端字段忽略。 */
export type BookmarkTopicEntry = {
  id: number
  slug: string
  title: string
  categoryName?: string
  excerpt?: string
  commentCount?: number
}

/**
 * 名单里的评论目标摘要（后端 `forum.BookmarkCommentRef`）。
 *
 * 只有 id 与 topicId，没有标题/摘要/主题 slug：所以评论行按 `#id` 呈现，
 * 跳转用 id 形态的主题深链（`/t/{topicId}#comment-{id}`）。
 */
export type BookmarkCommentEntry = {
  id: number
  topicId?: number
  authorUserId?: number
  createdAt?: string
}

export type BookmarkEntry = {
  targetType: BookmarkTargetType
  targetId: number
  visibility: BookmarkVisibility
  /** 收藏时间（ISO UTC）；混合流按它倒序。 */
  createdAt: string
  /** 名单行上的提醒元数据：两者都可能缺省。 */
  remindAt?: string | null
  remindedAt?: string | null
  topic?: BookmarkTopicEntry
  comment?: BookmarkCommentEntry
}

/** 名单分页结果：后端按 offset/limit 分页（`BookmarkList`）。 */
export type BookmarkList = {
  items: BookmarkEntry[]
  total: number
  hasMore: boolean
}

export const BOOKMARK_LIST_LIMIT = 20

export function useBookmarkApi() {
  const { request } = useApiClient()

  function endpoint(targetType: BookmarkTargetType, targetId: number) {
    return targetType === 'topic'
      ? `/topics/${targetId}/bookmark`
      : `/comments/${targetId}/bookmark`
  }

  /** 收藏（幂等）。`input` 同时承担可见性与提醒：空体表示「按默认私密补行」。 */
  function bookmark(targetType: BookmarkTargetType, targetId: number, input: BookmarkWriteInput = {}) {
    return request<BookmarkState>(endpoint(targetType, targetId), { method: 'PUT', body: input })
  }

  function unbookmark(targetType: BookmarkTargetType, targetId: number) {
    return request<BookmarkState>(endpoint(targetType, targetId), { method: 'DELETE' })
  }

  /** 本人名单：含私密行。分页口径与资料活动一致：page/perPage。 */
  function listMyBookmarks(page = 1, perPage = BOOKMARK_LIST_LIMIT) {
    return request<BookmarkList>(`/profile/bookmarks?page=${page}&perPage=${perPage}`)
  }

  /** 他人名单：只含公开行（服务端按 viewer 裁剪，前端不做过滤）。 */
  function listUserBookmarks(username: string, page = 1, perPage = BOOKMARK_LIST_LIMIT) {
    const query = `page=${page}&perPage=${perPage}`
    return request<BookmarkList>(`/profiles/${encodeURIComponent(username)}/bookmarks?${query}`)
  }

  return { bookmark, unbookmark, listMyBookmarks, listUserBookmarks }
}

type BookmarkSource<T> = T | Ref<T> | (() => T)

export type UseBookmarkOptions = {
  targetType: BookmarkSource<BookmarkTargetType>
  targetId: BookmarkSource<number>
  /** 父层已有的权威初值（`bookmarkedByMe`）。 */
  bookmarked?: BookmarkSource<boolean>
  visibility?: BookmarkSource<BookmarkVisibility>
  remindAt?: BookmarkSource<string | null>
  remindedAt?: BookmarkSource<string | null>
  /** 产品开关；关闭时控件不渲染，也不发请求。 */
  enabled?: BookmarkSource<boolean>
  /** 服务端 settle 后回写父层，避免父层重渲染把状态弹回旧值。 */
  onState?: (state: BookmarkState) => void
}

function readNumber(value: unknown): number | null {
  const parsed = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null
}

/** 从错误对象里读 HTTP 状态：ofetch / $fetch / 手抛三种形态都覆盖，不用 any。 */
function errorStatus(error: unknown): number | null {
  if (!error || typeof error !== 'object') {
    return null
  }
  const record = error as {
    statusCode?: unknown
    status?: unknown
    response?: { status?: unknown }
  }
  return readNumber(record.statusCode ?? record.status ?? record.response?.status)
}

/**
 * 收藏控件的状态机：乐观切换 + 失败回滚 + Toast。
 *
 * 可见性切换与提醒设置复用同一个 PUT（后端把「补行 + 改元数据」放在一个端点里），
 * 所以这里只有一条写路径，不存在「先收藏再改可见性」的两段请求。
 */
export function useBookmark(options: UseBookmarkOptions) {
  const api = useBookmarkApi()
  const { t } = useI18n()
  const toast = useToast()
  const router = useRouter()
  const route = useRoute()
  const localePath = useLocalePath()
  const { user } = useAuthSession()

  const targetType = computed(() => toValue(options.targetType))
  const targetId = computed(() => Number(toValue(options.targetId) || 0))
  const enabled = computed(() => toValue(options.enabled) !== false)

  const bookmarked = ref(Boolean(toValue(options.bookmarked)))
  const visibility = ref(normalizeBookmarkVisibility(toValue(options.visibility)))
  const remindAt = ref<string | null>(toValue(options.remindAt) ?? null)
  const remindedAt = ref<string | null>(toValue(options.remindedAt) ?? null)
  const publicBookmarkCount = ref(0)
  const pending = ref(false)

  watch(() => toValue(options.bookmarked), (value) => {
    bookmarked.value = Boolean(value)
  })
  watch(() => toValue(options.visibility), (value) => {
    visibility.value = normalizeBookmarkVisibility(value)
  })
  watch(() => toValue(options.remindAt), (value) => {
    remindAt.value = value ?? null
  })
  watch(() => toValue(options.remindedAt), (value) => {
    remindedAt.value = value ?? null
  })

  function applyState(state: BookmarkState) {
    bookmarked.value = state.bookmarked === true
    visibility.value = normalizeBookmarkVisibility(state.visibility)
    remindAt.value = state.remindAt ?? null
    remindedAt.value = state.remindedAt ?? null
    publicBookmarkCount.value = Number.isFinite(state.publicBookmarkCount)
      ? Math.max(0, state.publicBookmarkCount)
      : 0
    options.onState?.(state)
  }

  function failToast(error: unknown) {
    const reason = apiErrorReason(error)
    // 具名原因优先（422 的时间边界码只在这条路径上出现），未知原因再回落到状态码。
    const key = bookmarkReasonKey(reason) || bookmarkErrorKey(errorStatus(error))
    // 错误 Toast 不自动关闭（项目约定）：用户要看到失败原因再决定重试。
    toast.add({
      color: 'error',
      icon: 'i-lucide-alert-circle',
      duration: 0,
      title: t(key)
    })
  }

  function canWrite(): boolean {
    return enabled.value && targetId.value > 0 && !pending.value
  }

  /** 匿名访客先跳登录；返回 true 表示已经离开当前操作。 */
  async function redirectToLogin(): Promise<boolean> {
    if (user.value) {
      return false
    }
    await router.push(localePath({ path: '/login', query: { redirect: route.fullPath } }))
    return true
  }

  async function toggle() {
    if (!canWrite() || await redirectToLogin()) {
      return
    }
    const previous = bookmarked.value
    bookmarked.value = !previous
    pending.value = true
    try {
      const state = previous
        ? await api.unbookmark(targetType.value, targetId.value)
        : await api.bookmark(targetType.value, targetId.value, {
            visibility: visibility.value,
            remindAt: remindAt.value
          })
      applyState(state)
      toast.add({
        color: 'success',
        icon: 'i-lucide-check',
        duration: 5000,
        title: bookmarked.value ? t('bookmark.saved') : t('bookmark.removed')
      })
    } catch (error) {
      bookmarked.value = previous
      failToast(error)
    } finally {
      pending.value = false
    }
  }

  /** 切可见性：未收藏时同一个 PUT 会补行，已收藏时只改元数据。 */
  async function setVisibility(next: BookmarkVisibility) {
    if (!canWrite() || await redirectToLogin()) {
      return
    }
    const normalized = normalizeBookmarkVisibility(next)
    if (normalized === visibility.value && bookmarked.value) {
      return
    }
    const previous = visibility.value
    visibility.value = normalized
    pending.value = true
    try {
      const state = await api.bookmark(targetType.value, targetId.value, {
        visibility: normalized,
        remindAt: remindAt.value
      })
      applyState(state)
      toast.add({
        color: 'success',
        icon: 'i-lucide-check',
        duration: 5000,
        title: t('bookmark.visibility.updated')
      })
    } catch (error) {
      visibility.value = previous
      failToast(error)
    } finally {
      pending.value = false
    }
  }

  /**
   * 设/清提醒；`null` 表示清除。
   *
   * 清除走 `{ clearReminder: true }`：旧后端收到 `remindAt: null` 不会清行。
   * 成功后本地必须真置空——即便响应把旧 remindAt 回显回来，也不能让界面
   * 弹回原值却提示「已清除」。
   */
  async function setRemindAt(value: string | null): Promise<boolean> {
    if (!canWrite() || await redirectToLogin()) {
      return false
    }
    // 时间边界在本地先挡一次；非法值由控件就地提示，不发请求。
    if (value && bookmarkRemindIssue(value)) {
      return false
    }
    const previous = remindAt.value
    const previousRemindedAt = remindedAt.value
    remindAt.value = value
    pending.value = true
    try {
      const state = await api.bookmark(targetType.value, targetId.value, value
        ? { visibility: visibility.value, remindAt: value }
        : { visibility: visibility.value, clearReminder: true })
      applyState(value ? state : { ...state, remindAt: null, remindedAt: null })
      toast.add({
        color: 'success',
        icon: 'i-lucide-check',
        duration: 5000,
        title: value ? t('bookmark.remind.saved') : t('bookmark.remind.cleared')
      })
      return true
    } catch (error) {
      remindAt.value = previous
      remindedAt.value = previousRemindedAt
      failToast(error)
      return false
    } finally {
      pending.value = false
    }
  }

  return {
    bookmarked,
    visibility,
    remindAt,
    remindedAt,
    publicBookmarkCount,
    pending,
    defaultVisibility: recommendedBookmarkVisibility,
    toggle,
    setVisibility,
    setRemindAt
  }
}
