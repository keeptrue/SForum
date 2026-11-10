import type { Ref } from 'vue'
import {
  BOOKMARK_LIST_LIMIT,
  useBookmarkApi,
  type BookmarkEntry,
  type BookmarkList
} from '~/composables/forum/useBookmark'

/**
 * 资料页「收藏」tab 的数据层：page/perPage 分页 + 切换缓存。
 *
 * 缓存口径与活动流一致：切走不丢已加载结果，切回来立即可见；只有首次激活才取数。
 * 本人视角走后端 `includePrivate` 口径（本人含私密），他人视角服务端只回公开行，
 * 前端不做二次过滤——否则「他人看不到私密」会变成前端承诺而不是服务端保证。
 */
export type ProfileBookmarksOptions = {
  /** 当前 tab 是否激活；激活才取数。 */
  active: () => boolean
  /** 是否本人视角（决定端点与文案口径）。 */
  self: () => boolean
}

export function bookmarkEntryKey(entry: BookmarkEntry): string {
  return `${entry.targetType}:${entry.targetId}`
}

export function useProfileBookmarks(username: Ref<string>, options: ProfileBookmarksOptions) {
  const api = useBookmarkApi()

  const items = ref<BookmarkEntry[]>([])
  const page = ref(0)
  const total = ref(0)
  const hasMore = ref(false)
  const loaded = ref(false)
  const loading = ref(false)
  const loadingMore = ref(false)
  const failed = ref(false)

  /** 混合流按收藏时间倒序；同一时刻按目标稳定排序，避免分页追加时跳动。 */
  const sortedItems = computed(() => [...items.value].sort((left, right) => {
    const diff = Date.parse(right.createdAt) - Date.parse(left.createdAt)
    if (Number.isFinite(diff) && diff !== 0) {
      return diff
    }
    return bookmarkEntryKey(left).localeCompare(bookmarkEntryKey(right))
  }))

  function merge(result: BookmarkList) {
    const seen = new Set(items.value.map(bookmarkEntryKey))
    const appended = result.items.filter((entry) => {
      const key = bookmarkEntryKey(entry)
      if (seen.has(key)) {
        return false
      }
      seen.add(key)
      return true
    })
    items.value = [...items.value, ...appended]
    total.value = Number.isFinite(result.total) ? result.total : items.value.length
    hasMore.value = result.hasMore === true
  }

  function reset() {
    items.value = []
    page.value = 0
    total.value = 0
    hasMore.value = false
    loaded.value = false
    loading.value = false
    loadingMore.value = false
    failed.value = false
  }

  async function fetchPage(nextPage: number): Promise<BookmarkList> {
    return options.self()
      ? await api.listMyBookmarks(nextPage, BOOKMARK_LIST_LIMIT)
      : await api.listUserBookmarks(username.value, nextPage, BOOKMARK_LIST_LIMIT)
  }

  async function ensureLoaded() {
    if (!options.active() || loaded.value || loading.value || !username.value) {
      return
    }
    loading.value = true
    failed.value = false
    try {
      merge(await fetchPage(1))
      page.value = 1
      loaded.value = true
    } catch {
      // 失败保留在 tab 内展示重试入口，不弹 Toast 打断阅读。
      failed.value = true
    } finally {
      loading.value = false
    }
  }

  async function loadMore() {
    if (!options.active() || loading.value || loadingMore.value || !hasMore.value) {
      return
    }
    loadingMore.value = true
    try {
      const nextPage = page.value + 1
      merge(await fetchPage(nextPage))
      page.value = nextPage
    } catch {
      failed.value = true
    } finally {
      loadingMore.value = false
    }
  }

  async function retry() {
    failed.value = false
    loaded.value = false
    await ensureLoaded()
  }

  watch(() => options.active(), (active) => {
    if (active) {
      void ensureLoaded()
    }
  }, { immediate: true })

  watch(username, () => {
    reset()
    void ensureLoaded()
  })

  return {
    items,
    sortedItems,
    total,
    hasMore,
    loaded,
    loading,
    loadingMore,
    failed,
    ensureLoaded,
    loadMore,
    retry
  }
}
