<script setup lang="ts">
import { useActiveThemeSettings } from '~/composables/themes/useActiveThemeSettings'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useCategoryGroups } from '~/composables/forum/useCategoryGroups'
import { useForumApi } from '~/composables/forum/useForumApi'
import SFHomeTopicRow from '~/components/forum/SFHomeTopicRow.vue'
import SFHomeRightRail from '~/components/forum/SFHomeRightRail.vue'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFContentColumnFooter from '~/components/forum/SFContentColumnFooter.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFPublicPageHeader from '~/components/public/SFPublicPageHeader.vue'
import SFPublicMobileRightDrawerHeader from '~/components/navigation/SFPublicMobileRightDrawerHeader.vue'
import SFPublicMobileUserMenu from '~/components/navigation/SFPublicMobileUserMenu.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
/**
 * 宿主 body 岛：论坛首页与搜索页共享的完整交互 UI（主题 L1 挂载点）。
 * 路由页只负责 SEO + SFPageOutlet fail-closed 回退。
 */
import {
  buildForumHomeQuery,
  forumHomeFeedKey,
  hasReachedForumHomeEnd,
  isForumHomeRequestCurrent,
  parseForumHomeQuery,
  type ForumHomeFilters,
  type ForumHomeRequestToken
} from '~/utils/forum/forumHome'
import {
  forumTopicPath,
  forumTopicSorts,
  normalizeForumTopicSort,
  type ForumTag,
  type ForumTopicList,
  type ForumTopicSort,
  type ForumTopicSummary
} from '~/utils/forum/forumTaxonomy'

const SEARCH_DEBOUNCE_MS = 300

// 排序选项直接由契约枚举派生，UI 与服务端 sort 永不漂移。
const FEED_SORT_LABEL_KEYS: Record<ForumTopicSort, string> = {
  latest: 'home.filter.latest',
  active: 'home.filter.active',
  hot: 'home.filter.hot'
}
// 展示顺序 = 产品优先序：最近活跃在前，其次最新发表，最后热门。
const FEED_SORT_PRIORITY: Record<ForumTopicSort, number> = { active: 0, latest: 1, hot: 2 }
const FEED_SORT_OPTIONS = [...forumTopicSorts]
  .sort((left, right) => FEED_SORT_PRIORITY[left] - FEED_SORT_PRIORITY[right])
  .map(key => ({ key, labelKey: FEED_SORT_LABEL_KEYS[key] }))

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const localePath = useLocalePath()
const isSearchPage = computed(() => route.path === localePath('/search'))
const registryPage = computed(() => isSearchPage.value ? 'forum.search' : 'forum.home')
const surfacePath = computed(() => isSearchPage.value ? localePath('/search') : localePath('/'))
const { seoSettings, webOption } = useWebOptions()
const forumApi = useForumApi()
const { can } = usePermissions()
// 主题 extension settings（后台「主题设置」自定义页）；非 core web_options
const {
  homeNotice,
  homeEmptyTitle,
  homeEmptyDescription,
  rightRailEnabled,
  rightRailHotLimit,
  rightRailTagLimit
} = useActiveThemeSettings()

const topicUrlMode = computed(() => seoSettings.value.topicUrlMode)
const committedFilters = computed(() => parseForumHomeQuery(route.query))
const currentPage = computed(() => parsePublicPage(route.query.page))
const activeFeedKey = computed(() => `${registryPage.value}:${forumHomeFeedKey(committedFilters.value)}`)
const activePageFeedKey = computed(() => `${activeFeedKey.value}:${currentPage.value}`)
const topicDataKey = computed(() => `forum-home-topics:${activePageFeedKey.value}`)
const searchDraft = ref(committedFilters.value.query)
const selectedCategorySlug = computed(() => committedFilters.value.categorySlug)
const selectedTagSlug = computed(() => committedFilters.value.tagSlug)
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()
const renderedAt = useState<number>('forum-home-rendered-at', () => Date.now())
const filterPanelOpen = ref(false)
// 排序切换后的结果公告（无 URL 变更时屏幕阅读器也需要被告知）
const sortAnnouncement = ref('')
let feedGeneration = 0

// 站点默认排序来自公开选项 forum.list.default_sort（latest|active|hot），
// 推荐默认 active：URL 不带 sort 时列表就是它，高亮必须与实际排序一致。
const siteDefaultSort = computed<ForumTopicSort>(
  () => normalizeForumTopicSort(webOption('forum.list.default_sort', 'active')) || 'active'
)
// URL 未携带 sort 时列表就是站点默认排序，因此高亮取「URL > 站点默认」。
const activeFeedSort = computed<ForumTopicSort>(() => committedFilters.value.sort || siteDefaultSort.value)
// 关键词搜索端点没有 sort；搜索态不展示排序入口，避免对用户做出假承诺。
const showFeedSort = computed(() => !isSearchPage.value && !committedFilters.value.query)

const emptyTopicList = (): ForumTopicList => ({
  items: [],
  total: 0,
  page: 1,
  perPage: 20,
  hasMore: false
})

// 公开面共享一条稳定 key（RV-35）；分类目录与具体分类/标签无关，不按 slug 分裂。
const { data: categoryGroups, pending: categoriesPending } = await useCategoryGroups()

const { data: activeTags, pending: tagsPending } = await useAsyncData(
  'forum-home-tags',
  async () => (await forumApi.listTags()).filter((tag) => tag.status === 'active'),
  { default: () => [] as ForumTag[] }
)

function loadTopicPage(
  page: number,
  filters: ForumHomeFilters = committedFilters.value,
  after?: string
) {
  if (filters.query) {
    // 搜索端点仍为 page 分页（非 M5 keyset 范围）
    return forumApi.searchTopics({
      query: filters.query,
      categorySlug: filters.categorySlug,
      tagSlug: filters.tagSlug,
      page
    })
  }

  if (isSearchPage.value) {
    return Promise.resolve(emptyTopicList())
  }

  // M5：有 nextCursor 时用 after keyset，避免深 OFFSET
  // 排序始终交给服务端：游标与 sort 绑定，前端不得本地重排
  const sort = filters.sort || undefined
  if (after) {
    return forumApi.listTopics({
      categorySlug: filters.categorySlug,
      tagSlug: filters.tagSlug,
      after,
      sort
    })
  }

  return forumApi.listTopics({
    categorySlug: filters.categorySlug,
    tagSlug: filters.tagSlug,
    page,
    sort
  })
}

const {
  data: topicList,
  pending: topicsPending,
  error: topicsError,
  refresh: refreshTopics
} = await useAsyncData(
  topicDataKey,
  async () => {
    const filters = { ...committedFilters.value }
    return loadTopicPage(currentPage.value, filters)
  },
  {
    default: emptyTopicList
  }
)

const loadedTopics = useState<ForumTopicSummary[]>('forum-home-loaded-topics', () => topicList.value.items)
const loadedTopicTotal = useState<number>('forum-home-topic-total', () => topicList.value.total)
const loadedFeedKey = useState<string>('forum-home-loaded-feed-key', () => activePageFeedKey.value)
const nextPage = ref(currentPage.value + 1)
// M5：优先 cursor 续页；无 nextCursor 时回退 page
const nextCursor = ref(topicList.value.nextCursor || '')
const loadedHasMore = ref<boolean | undefined>(topicList.value.hasMore)
const isLoadingMore = ref(false)
const loadMoreError = ref('')
const loadMoreTrigger = ref<HTMLElement | null>(null)
const hasLoadedAllPages = ref(false)
// 没有 IntersectionObserver 时退化为手动「加载更多」：信息流已无页码控件，
// 不能再让这类环境彻底无法翻页。
const canAutoLoad = ref(true)

const categories = computed(() => categoryGroups.value.flatMap((group) => group.categories || []))
// 列表顺序完全由服务端排序决定（latest / active / hot）：本地重排只能作用于已加载页，
// 还会破坏置顶优先与 cursor 续页语义，因此前端不做二次排序。
const topics = computed(() => loadedTopics.value)
// hasMore 优先 API 字段 / nextCursor；否则用 total 近似（兼容旧响应）
const hasMoreTopics = computed(() => {
  if (hasLoadedAllPages.value) {
    return false
  }
  if (nextCursor.value) {
    return true
  }
  if (typeof loadedHasMore.value === 'boolean') {
    return loadedHasMore.value
  }
  return topics.value.length < loadedTopicTotal.value
})
const hasActiveFilters = computed(() => Boolean(
  committedFilters.value.query
  || committedFilters.value.categorySlug
  || committedFilters.value.tagSlug
))
const feedState = computed(() => {
  if (topicsPending.value) return 'loading'
  if (topicsError.value) return 'error'
  if (topics.value.length) return 'results'
  if (committedFilters.value.query) return 'search-empty'
  if (hasActiveFilters.value) return 'filtered-empty'
  return 'empty'
})

const selectedCategory = computed(() => categories.value.find(
  category => category.slug === selectedCategorySlug.value
))
const feedTitle = computed(() => isSearchPage.value
  ? t('search.title')
  : selectedCategory.value?.name || t('home.feed.latestTitle'))
const emptyTitle = computed(() => {
  if (isSearchPage.value && !hasActiveFilters.value) {
    return t('search.emptyTitle')
  }
  if (hasActiveFilters.value) {
    return t('home.emptyState.title')
  }
  return homeEmptyTitle.value || t('home.emptyState.title')
})
const emptyDescription = computed(() => {
  if (isSearchPage.value && !hasActiveFilters.value) {
    return t('search.emptyDescription')
  }
  if (hasActiveFilters.value) {
    return t('home.emptyState.filteredDescription')
  }
  return homeEmptyDescription.value || t('home.emptyState.description')
})
const totalTopics = computed(() => {
  const categoryTotal = categories.value.reduce((total, category) => total + category.topicCount, 0)
  if (categoryTotal > 0) {
    return categoryTotal
  }
  return hasActiveFilters.value ? 0 : loadedTopicTotal.value
})
// 右栏热帖：当前已加载主题按回复数排序，条数来自主题设置
const hotTopics = computed(() => {
  return [...topics.value]
    .sort((left, right) => {
      const replyDiff = right.commentCount - left.commentCount
      if (replyDiff !== 0) {
        return replyDiff
      }
      return right.id - left.id
    })
    .slice(0, rightRailHotLimit.value)
})
const totalReplies = computed(() => {
  const fromCategories = categories.value.reduce((sum, category) => sum + (category.commentCount || 0), 0)
  if (fromCategories > 0) {
    return fromCategories
  }
  return topics.value.reduce((sum, topic) => sum + topic.commentCount, 0)
})
const railTags = computed(() => {
  return [...activeTags.value]
    .sort((left, right) => {
      const countDiff = right.topicCount - left.topicCount
      if (countDiff !== 0) {
        return countDiff
      }
      return left.name.localeCompare(right.name)
    })
    .slice(0, rightRailTagLimit.value)
})
function replaceLoadedTopics(list: ForumTopicList) {
  const seen = new Set<number>()
  loadedTopics.value = list.items.filter((topic) => {
    if (seen.has(topic.id)) {
      return false
    }
    seen.add(topic.id)
    return true
  })
  loadedTopicTotal.value = list.total
  loadedFeedKey.value = activePageFeedKey.value
  nextPage.value = Math.max(currentPage.value + 1, list.page + 1)
  nextCursor.value = list.nextCursor || ''
  loadedHasMore.value = list.hasMore
  loadMoreError.value = ''
  hasLoadedAllPages.value = list.hasMore === false
    || (!list.nextCursor && (list.items.length >= list.total || list.items.length < list.perPage))
}

function shouldIgnoreClientEmptyHydration(list: ForumTopicList) {
  return import.meta.client
    && activePageFeedKey.value === loadedFeedKey.value
    && loadedTopics.value.length > 0
    && loadedTopicTotal.value > 0
    && list.items.length === 0
    && list.total === 0
}

watch(topicList, (list) => {
  if (shouldIgnoreClientEmptyHydration(list)) {
    return
  }
  replaceLoadedTopics(list)
}, { immediate: true })

watch(activePageFeedKey, () => {
  feedGeneration += 1
  nextPage.value = currentPage.value + 1
  nextCursor.value = ''
  loadedHasMore.value = undefined
  isLoadingMore.value = false
  loadMoreError.value = ''
  hasLoadedAllPages.value = false
}, { flush: 'sync' })

async function loadMoreTopics(forceRetry = false) {
  if (topicsPending.value || isLoadingMore.value || !hasMoreTopics.value) {
    return
  }
  if (loadMoreError.value && !forceRetry) {
    return
  }

  const filters = { ...committedFilters.value }
  const request: ForumHomeRequestToken = {
    generation: feedGeneration,
    feedKey: activePageFeedKey.value
  }
  const page = nextPage.value
  const cursor = nextCursor.value
  isLoadingMore.value = true
  loadMoreError.value = ''

  try {
    // 搜索走 page；主题列表优先 after cursor（M5）
    const nextList = filters.query
      ? await loadTopicPage(page, filters)
      : await loadTopicPage(page, filters, cursor || undefined)
    if (!isForumHomeRequestCurrent(request, feedGeneration, activePageFeedKey.value)) {
      return
    }

    const existingIds = new Set(loadedTopics.value.map((topic) => topic.id))
    const newTopics = nextList.items.filter((topic) => !existingIds.has(topic.id))
    loadedTopics.value = [...loadedTopics.value, ...newTopics]
    loadedTopicTotal.value = Math.max(loadedTopicTotal.value, nextList.total)
    loadedFeedKey.value = activePageFeedKey.value
    nextPage.value = nextList.page + 1
    nextCursor.value = nextList.nextCursor || ''
    loadedHasMore.value = nextList.hasMore
    hasLoadedAllPages.value = hasReachedForumHomeEnd({
      requestedPage: page,
      responsePage: nextList.page,
      responseItemCount: nextList.items.length,
      newItemCount: newTopics.length,
      loadedCount: loadedTopics.value.length,
      total: loadedTopicTotal.value,
      perPage: nextList.perPage,
      hasMore: nextList.hasMore,
      usedCursor: Boolean(cursor)
    })
  } catch {
    if (isForumHomeRequestCurrent(request, feedGeneration, activePageFeedKey.value)) {
      loadMoreError.value = 'home.feed.loadMoreFailed'
    }
  } finally {
    if (isForumHomeRequestCurrent(request, feedGeneration, activePageFeedKey.value)) {
      isLoadingMore.value = false
    }
  }
}

let loadMoreObserver: IntersectionObserver | null = null
let stopLoadMoreTriggerWatch: (() => void) | null = null
let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null

function clearSearchDebounce() {
  if (searchDebounceTimer) {
    clearTimeout(searchDebounceTimer)
    searchDebounceTimer = null
  }
}

function commitFilters(nextFilters: ForumHomeFilters) {
  if (forumHomeFeedKey(nextFilters) === activeFeedKey.value && currentPage.value === 1) {
    return
  }
  return router.replace({
    path: surfacePath.value,
    query: buildForumHomeQuery(nextFilters)
  })
}

watch(() => committedFilters.value.query, (query) => {
  clearSearchDebounce()
  if (searchDraft.value !== query) {
    searchDraft.value = query
  }
})

watch(searchDraft, (query) => {
  clearSearchDebounce()
  if (query.trim() === committedFilters.value.query) {
    return
  }
  searchDebounceTimer = setTimeout(() => {
    searchDebounceTimer = null
    const nextFilters = { ...committedFilters.value, query: query.trim() }
    void commitFilters(nextFilters)
  }, SEARCH_DEBOUNCE_MS)
})

function submitSearch(query: string) {
  clearSearchDebounce()
  searchDraft.value = query
  return commitFilters({ ...committedFilters.value, query: query.trim() })
}

function selectCategory(slug: string) {
  const categorySlug = selectedCategorySlug.value === slug ? '' : slug
  return commitFilters({ ...committedFilters.value, categorySlug })
}

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

function selectMobileCategory(slug: string) {
  closeMobileDrawers()
  return selectCategory(slug)
}

function selectMobileTag(slug: string) {
  closeMobileDrawers()
  return selectTag(slug)
}

function selectTag(slug: string) {
  const tagSlug = selectedTagSlug.value === slug ? '' : slug
  return commitFilters({ ...committedFilters.value, tagSlug })
}

// 排序写进 URL：可分享、可刷新保持，并让 feedKey 变化触发分页与 cursor 重置。
// 选中站点默认排序时清掉参数，保持链接干净且与默认态等价。
function selectFeedSort(sort: ForumTopicSort) {
  const nextSort = sort === siteDefaultSort.value ? '' : sort
  if (nextSort === committedFilters.value.sort) {
    return
  }
  sortAnnouncement.value = t('home.filter.sortedBy', { label: t(FEED_SORT_LABEL_KEYS[sort]) })
  return commitFilters({ ...committedFilters.value, sort: nextSort })
}

function resetFilters() {
  clearSearchDebounce()
  searchDraft.value = ''
  // 排序不是筛选项：清除筛选时保留用户已选的排序方式。
  return commitFilters({ ...committedFilters.value, query: '', categorySlug: '', tagSlug: '' })
}

async function retryFirstPage() {
  await refreshTopics()
}

function topicRelativeLabel(iso: string) {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) {
    return ''
  }

  const diffMs = renderedAt.value - date.getTime()
  const minute = 60 * 1000
  const hour = 60 * minute
  const day = 24 * hour

  if (diffMs >= 0 && diffMs < hour) {
    return t('home.feed.activityMinutes', { count: Math.max(1, Math.floor(diffMs / minute)) })
  }
  if (diffMs >= 0 && diffMs < day) {
    return t('home.feed.activityHours', { count: Math.max(1, Math.floor(diffMs / hour)) })
  }
  if (diffMs >= 0 && diffMs < 7 * day) {
    return t('home.feed.activityDays', { count: Math.max(1, Math.floor(diffMs / day)) })
  }

  return date.toISOString().slice(0, 10)
}

/** 左侧 meta：发帖时间 */
function topicCreated(topic: ForumTopicSummary) {
  return topicRelativeLabel(topic.createdAt)
}

/** 右侧列：最近活动时间 */
function topicActivity(topic: ForumTopicSummary) {
  return topicRelativeLabel(topic.lastActivityAt || topic.createdAt)
}

onMounted(() => {
  if (typeof IntersectionObserver === 'undefined') {
    canAutoLoad.value = false
    return
  }

  loadMoreObserver = new IntersectionObserver((entries) => {
    if (entries.some((entry) => entry.isIntersecting)) {
      void loadMoreTopics()
    }
  }, {
    rootMargin: '360px 0px'
  })

  stopLoadMoreTriggerWatch = watch(loadMoreTrigger, (element, previousElement) => {
    if (previousElement) {
      loadMoreObserver?.unobserve(previousElement)
    }
    if (element) {
      loadMoreObserver?.observe(element)
    }
  }, { immediate: true })
})

onBeforeUnmount(() => {
  clearSearchDebounce()
  stopLoadMoreTriggerWatch?.()
  loadMoreObserver?.disconnect()
})
</script>

<template>
  <main
    class="sforum-home"
    :data-sforum-island-body="isSearchPage ? 'forum.component.search_page' : 'forum.component.home_page'"
    data-layout="fullwidth-3col"
    :data-feed-state="feedState"
  >
    <div
      class="sforum-home__layout"
      :class="{ 'sforum-home__layout--with-right': rightRailEnabled }"
    >
      <SFResponsivePublicSidebar
        :owner-id="registryPage"
        :title="t('home.sidebar.drawerTitle')"
        class="sforum-home__sidebar"
      >
        <SFHomeNavigation
          desktop-only
          :categories="categories"
          :selected-category-slug="selectedCategorySlug"
          :total-topics="totalTopics"
          :pending="categoriesPending"
          :can-create-topic="canCreateTopic"
          @select-category="selectMobileCategory"
        />
      </SFResponsivePublicSidebar>

      <section class="sforum-home__main sforum-content-column" aria-labelledby="forum-feed-title">
        <SFRegionOutlet :page="registryPage" region="content_before" />

        <div
          v-if="homeNotice"
          class="sforum-home__notice mb-3.5 rounded-lg border border-[var(--sf-public-notice-border)] bg-[var(--sf-public-notice-bg)] px-3.5 py-2.5 text-sm font-semibold leading-normal text-[var(--sf-public-notice-text)]"
          role="note"
        >
          {{ homeNotice }}
        </div>

        <!-- 列表抬头由宿主提供真实筛选状态，默认主题只通过 token 换肤。 -->
        <SFPublicPageHeader
          class="sforum-home__feed-head"
          title-id="forum-feed-title"
          :title="feedTitle"
          :subtitle="!selectedCategorySlug && !selectedTagSlug
            ? t('home.feed.subtitle')
            : t('home.feed.topicCountMeta', { count: totalTopics })"
          :level="2"
          variant="section"
        >
          <template #aside>
            <div
              v-if="showFeedSort"
              class="sforum-home__feed-tools"
              role="group"
              :aria-label="t('home.filter.sortLabel')"
            >
              <button
                v-for="option in FEED_SORT_OPTIONS"
                :key="option.key"
                type="button"
                class="sforum-home__feed-sort"
                :class="{ 'is-active': activeFeedSort === option.key }"
                :aria-pressed="activeFeedSort === option.key"
                @click="selectFeedSort(option.key)"
              >
                {{ t(option.labelKey) }}
              </button>
              <button
                type="button"
                class="sforum-home__feed-filter-button"
                :class="{ 'is-active': filterPanelOpen || hasActiveFilters }"
                :title="t('home.filter.openFilters')"
                :aria-label="t('home.filter.openFilters')"
                :aria-expanded="filterPanelOpen"
                aria-controls="home-topic-filters"
                @click="filterPanelOpen = !filterPanelOpen"
              >
                <UIcon name="i-lucide-sliders-horizontal" class="size-[19px]" aria-hidden="true" />
              </button>
            </div>
            <p class="sr-only" aria-live="polite">{{ sortAnnouncement }}</p>
          </template>
        </SFPublicPageHeader>

        <p
          v-if="committedFilters.query"
          class="mt-2.5 mb-0 text-xs text-[var(--sf-public-text-muted)]"
        >
          {{ t('home.searchResults', { query: committedFilters.query }) }}
        </p>

        <div
          v-if="filterPanelOpen && (categories.length || activeTags.length || hasActiveFilters)"
          id="home-topic-filters"
          class="sforum-home__tag-filter"
          :aria-busy="tagsPending"
        >
          <div class="sforum-home__filter-group" :aria-label="t('home.categories')">
            <button
              v-for="category in categories"
              :key="category.slug"
              type="button"
              class="sforum-home__filter-chip"
              :class="{ 'is-active': selectedCategorySlug === category.slug }"
              :aria-pressed="selectedCategorySlug === category.slug"
              @click="selectCategory(category.slug)"
            >
              {{ category.name }}
            </button>
          </div>
          <div id="home-tags" class="sforum-home__filter-group" :aria-label="t('home.tags')">
            <button
              v-for="tag in activeTags"
              :key="tag.slug"
              type="button"
              class="sforum-home__filter-chip"
              :class="{ 'is-active': selectedTagSlug === tag.slug }"
              :aria-pressed="selectedTagSlug === tag.slug"
              @click="selectTag(tag.slug)"
            >
              <span>#{{ tag.name }}</span>
            </button>
          </div>
          <button
            v-if="hasActiveFilters"
            type="button"
            class="sforum-home__filter-clear"
            @click="resetFilters"
          >
            <UIcon name="i-lucide-x" class="size-3.5" aria-hidden="true" />
            {{ t('home.clearFilters') }}
          </button>
        </div>

        <SFRegionOutlet :page="registryPage" region="list_before" />

        <div
          id="feed-list-container"
          class="sforum-home__feed-list"
          data-sf-region="topic-list"
        >
          <div
            v-if="topicsError"
            class="flex flex-col items-center gap-3 px-4 py-10 text-center text-sm text-[var(--sf-public-text-muted)]"
            role="alert"
          >
            <span>{{ t('home.feed.loadFailed') }}</span>
            <button type="button" class="sf-button sf-button--ghost sf-button--sm" @click="retryFirstPage">
              <UIcon name="i-lucide-refresh-cw" class="size-4" aria-hidden="true" />
              {{ t('home.feed.retry') }}
            </button>
          </div>

          <template v-else-if="topicsPending">
            <div
              v-for="item in 7"
              :key="item"
              class="border-b border-[var(--sf-border-light,#eef0f3)] p-3.5 last:border-b-0"
            >
              <SFSkeleton avatar :lines="2" />
            </div>
          </template>

          <template v-else-if="topics.length">
            <SFHomeTopicRow
              v-for="topic in topics"
              :key="topic.id"
              :topic="topic"
              :to="localePath(forumTopicPath(topic, topicUrlMode))"
              :created-label="topicCreated(topic)"
              :activity-label="topicActivity(topic)"
              :extension-list-badges="topicList.extensionListBadges || []"
            />
          </template>

          <div
            v-else
            class="sforum-home__empty px-4 py-10 text-center"
            :class="{ 'sforum-home__empty--search': Boolean(committedFilters.query) }"
            :data-sf-region="committedFilters.query ? 'search-empty' : 'topic-list-empty'"
          >
            <SFEmptyState
              :title="emptyTitle"
              :description="emptyDescription"
              :action-label="hasActiveFilters ? t('home.clearFilters') : undefined"
              @action="resetFilters"
            />
          </div>
        </div>

        <SFRegionOutlet :page="registryPage" region="list_after" />

        <div
          v-if="topics.length > 0 && !topicsPending && !topicsError"
          ref="loadMoreTrigger"
          class="sforum-home__infinite-state"
          role="status"
          aria-live="polite"
        >
          <span v-if="isLoadingMore">{{ t('home.feed.loadingMore') }}</span>
          <template v-else-if="loadMoreError">
            <span>{{ t(loadMoreError) }}</span>
            <button type="button" class="sf-button sf-button--ghost sf-button--sm" @click="loadMoreTopics(true)">
              <UIcon name="i-lucide-refresh-cw" class="size-4" aria-hidden="true" />
              {{ t('home.feed.retryLoadMore') }}
            </button>
          </template>
          <span v-else-if="!hasMoreTopics">{{ t('home.feed.end') }}</span>
          <button
            v-else-if="!canAutoLoad"
            type="button"
            class="sf-button sf-button--ghost sf-button--sm"
            @click="loadMoreTopics()"
          >
            <UIcon name="i-lucide-chevron-down" class="size-4" aria-hidden="true" />
            {{ t('home.feed.loadMore') }}
          </button>
          <span v-else class="sforum-home__sentinel" aria-hidden="true" />
        </div>

        <SFRegionOutlet :page="registryPage" region="content_after" />

        <SFContentColumnFooter />
      </section>

      <SFHomeRightRail
        v-if="rightRailEnabled"
        :hot-topics="hotTopics"
        :tags="railTags"
        :selected-tag-slug="selectedTagSlug"
        :total-topics="totalTopics"
        :total-replies="totalReplies"
        :category-count="categories.length"
        :tag-count="activeTags.length"
        :topic-url-mode="topicUrlMode"
        :can-create-topic="canCreateTopic"
        @select-tag="selectTag"
      >
        <SFRegionOutlet :page="registryPage" region="sidebar_top" />
        <SFRegionOutlet :page="registryPage" region="sidebar" />
        <SFRegionOutlet :page="registryPage" region="sidebar_bottom" />
      </SFHomeRightRail>
    </div>

    <button
      v-if="mobileInfoOpen"
      type="button"
      class="sforum-mobile-drawer__backdrop"
      :aria-label="t('topicDetail.cancel')"
      @click="closeMobileDrawers"
    />

    <aside v-if="mobileInfoOpen" class="sforum-mobile-drawer sforum-mobile-drawer--right">
      <SFPublicMobileRightDrawerHeader
        :title="t('home.rightRail.drawerTitle')"
        :close-label="t('topicDetail.cancel')"
        @close="closeMobileDrawers"
      />
      <SFPublicMobileUserMenu />
      <SFHomeRightRail
        :hot-topics="hotTopics"
        :tags="railTags"
        :selected-tag-slug="selectedTagSlug"
        :total-topics="totalTopics"
        :total-replies="totalReplies"
        :category-count="categories.length"
        :tag-count="activeTags.length"
        :topic-url-mode="topicUrlMode"
        :can-create-topic="canCreateTopic"
        @select-tag="selectMobileTag"
      />
    </aside>
  </main>
</template>
