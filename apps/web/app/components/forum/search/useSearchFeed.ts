import { useForumApi } from '~/composables/forum/useForumApi'
import {
  buildForumHomeQuery,
  forumHomeFeedKey,
  parseForumHomeQuery
} from '~/utils/forum/forumHome'
import type { ForumTopicExtensionBadge, ForumTopicList, ForumTopicSummary } from '~/utils/forum/forumTaxonomy'

/**
 * `/search` 的数据流：URL ?q= → 防抖 → forumApi.searchTopics → 首屏 + 加载更多。
 *
 * 与 `SFHomePage` 的首页信息流是两件事：
 * - 首页走 `listTopics`（cursor 续页、排序、分类/标签导航）；
 * - 搜索走 `searchTopics`（page 分页、无 sort，见 `buildForumSearchQuery`）。
 * 因此这里不复用首页的 feed 状态，只复用 `forumHome` 里的 URL 契约纯函数。
 */
const SEARCH_DEBOUNCE_MS = 300
const SEARCH_PAGE_SIZE = 20

export type SearchFeedState = 'idle' | 'loading' | 'ready' | 'empty' | 'error'

function emptyList(): ForumTopicList {
  return { items: [], total: 0, page: 1, perPage: SEARCH_PAGE_SIZE, hasMore: false }
}

export async function useSearchFeed() {
  const route = useRoute()
  const router = useRouter()
  const localePath = useLocalePath()
  const forumApi = useForumApi()

  const surfacePath = computed(() => localePath('/search'))
  const committed = computed(() => parseForumHomeQuery(route.query))
  const query = computed(() => committed.value.query)
  const draft = ref(query.value)

  // URL → 输入框（前进/后退、外部跳转、快捷词回填都要跟上）
  watch(query, (value) => {
    if (draft.value !== value) {
      draft.value = value
    }
  })

  let debounceTimer: ReturnType<typeof setTimeout> | null = null
  function clearDebounce() {
    if (debounceTimer) {
      clearTimeout(debounceTimer)
      debounceTimer = null
    }
  }

  /** 只写 q（以及搜索端点支持的 category/tag）；sort 在搜索端点不存在，一律不写。 */
  function commitQuery(next: string) {
    return router.replace({
      path: surfacePath.value,
      query: buildForumHomeQuery({
        query: next,
        categorySlug: committed.value.categorySlug,
        tagSlug: committed.value.tagSlug,
        sort: ''
      })
    })
  }

  // 输入框 → URL（防抖，避免每个字符一次路由）
  watch(draft, (value) => {
    clearDebounce()
    const next = value.trim()
    if (next === query.value) {
      return
    }
    debounceTimer = setTimeout(() => {
      debounceTimer = null
      void commitQuery(next)
    }, SEARCH_DEBOUNCE_MS)
  })

  /** Enter / 点按钮：跳过防抖立刻提交。 */
  function submitNow(value?: string) {
    clearDebounce()
    const next = (value ?? draft.value).trim()
    draft.value = next
    if (next === query.value) {
      return Promise.resolve()
    }
    return commitQuery(next)
  }

  /** 快捷词：立即写入并提交。 */
  function setQuery(value: string) {
    clearDebounce()
    draft.value = value
    return commitQuery(value.trim())
  }

  function reset() {
    clearDebounce()
    draft.value = ''
    return commitQuery('')
  }

  // 必须在下面的 `await useAsyncData` 之前注册：异步 setup 里 await 之后就拿不到组件实例了。
  onBeforeUnmount(clearDebounce)

  const feedKey = computed(() => `forum-search:${forumHomeFeedKey({
    query: query.value,
    categorySlug: committed.value.categorySlug,
    tagSlug: committed.value.tagSlug,
    sort: ''
  })}`)

  const { data: list, pending, error, refresh } = await useAsyncData(
    feedKey,
    async () => {
      if (!query.value) {
        // 没有关键词就是空态，不请求搜索端点（后端会 422/空结果，白白多一跳）。
        return emptyList()
      }
      return forumApi.searchTopics({
        query: query.value,
        categorySlug: committed.value.categorySlug,
        tagSlug: committed.value.tagSlug,
        page: 1
      })
    },
    { default: emptyList }
  )

  const items = ref<ForumTopicSummary[]>([])
  const extensionListBadges = ref<ForumTopicExtensionBadge[]>([])
  const total = ref(0)
  const page = ref(1)
  const hasMore = ref(false)
  const loadingMore = ref(false)
  const loadMoreError = ref(false)

  function applyList(next: ForumTopicList, append: boolean) {
    const seen = new Set(append ? items.value.map(topic => topic.id) : [])
    const fresh = next.items.filter(topic => !seen.has(topic.id))
    items.value = append ? [...items.value, ...fresh] : fresh
    if (!append) {
      extensionListBadges.value = next.extensionListBadges || []
    }
    total.value = next.total
    page.value = next.page
    hasMore.value = typeof next.hasMore === 'boolean'
      ? next.hasMore
      : items.value.length < next.total
    loadMoreError.value = false
  }

  watch(list, (value) => {
    // 查询变化时 useAsyncData 会重新取数，这里按「替换」而不是「追加」应用。
    applyList(value, false)
  }, { immediate: true })

  async function loadMore() {
    if (loadingMore.value || pending.value || !hasMore.value) {
      return
    }
    loadingMore.value = true
    loadMoreError.value = false
    try {
      const next = await forumApi.searchTopics({
        query: query.value,
        categorySlug: committed.value.categorySlug,
        tagSlug: committed.value.tagSlug,
        page: page.value + 1
      })
      // 请求期间查询被改掉时丢弃结果，避免串页。
      if (next.page !== page.value + 1) {
        return
      }
      applyList(next, true)
    } catch {
      loadMoreError.value = true
    } finally {
      loadingMore.value = false
    }
  }

  function retry() {
    return refresh()
  }

  const state = computed<SearchFeedState>(() => {
    if (!query.value) {
      return 'idle'
    }
    if (pending.value) {
      return 'loading'
    }
    if (error.value) {
      return 'error'
    }
    if (!items.value.length) {
      return 'empty'
    }
    return 'ready'
  })

  return {
    /** 已提交的查询词（来自 URL） */
    query,
    /** 输入框草稿 */
    draft,
    items,
    extensionListBadges,
    total,
    state,
    pending,
    hasMore,
    loadingMore,
    loadMoreError,
    submitNow,
    setQuery,
    reset,
    loadMore,
    retry
  }
}
