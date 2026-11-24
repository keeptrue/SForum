<script setup lang="ts">
/**
 * `/search` 的 Host 岛（island id：`forum.component.search_page`）。
 *
 * 渲染路径：主题模板 `templates/search.html` 的 `<sf-search-page>` → `SFThemeTemplate`
 * 的 island 映射 → 本组件；`pages/search.vue` 的 slot 只在 fail-closed 回退时生效。
 * 两条路径都指向这里，所以身份属性 `data-sforum-island-body` 必须留在根节点。
 *
 * 编排边界：数据在 `useSearchFeed`，表现拆给 hero / quick terms / result surface。
 * 本文件只做组合、区域出口与页面级语义（h1、data-feed-state）。
 */
import SFSearchHero from './SFSearchHero.vue'
import SFSearchQuickTerms from './SFSearchQuickTerms.vue'
import SFSearchResultSurface from './SFSearchResultSurface.vue'
import SFSearchResultCard from './SFSearchResultCard.vue'
import { useSearchFeed } from './useSearchFeed'
import SFHomeTopicRow from '~/components/forum/SFHomeTopicRow.vue'
import { useForumApi } from '~/composables/forum/useForumApi'
import { forumTopicPath } from '~/utils/forum/forumTaxonomy'
import { usePageRegionsState } from '~/composables/pages/usePageRegions'

const { t } = useI18n()
const localePath = useLocalePath()
const { seoSettings } = useWebOptions()
const forumApi = useForumApi()

const {
  draft,
  query,
  items,
  extensionListBadges,
  total,
  state,
  hasMore,
  loadingMore,
  loadMoreError,
  submitNow,
  setQuery,
  reset,
  loadMore,
  retry
} = await useSearchFeed()

const topicUrlMode = computed(() => seoSettings.value.topicUrlMode)

// 快捷词来自真实标签（按主题数取前 5），不写死示例词。
const { data: quickTags } = await useAsyncData(
  'forum-search-quick-tags',
  async () => (await forumApi.listTags()).filter(tag => tag.status === 'active'),
  { default: () => [] }
)
const quickTerms = computed(() => [...quickTags.value]
  .sort((left, right) => (right.topicCount || 0) - (left.topicCount || 0) || left.name.localeCompare(right.name))
  .slice(0, 5)
  .map(tag => ({ label: `#${tag.name}`, value: tag.name })))

// `forum.search` 只暴露 5 个区域；sidebar 无内容时不占位，但不静默吞掉插件 widget。
const regionState = usePageRegionsState('forum.search')
const hasSidebarRegion = computed(() =>
  (regionState.value?.regions.find(region => region.id === 'sidebar')?.items.length ?? 0) > 0
)

// 相对时间：与 SFHomePage 的同名 helper 同源同输出（出现第三个调用方时再抽到 utils）。
const renderedAt = useState<number>('forum-search-rendered-at', () => Date.now())
function relativeLabel(iso: string) {
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

function topicCreated(createdAt: string) {
  return relativeLabel(createdAt)
}

function topicActivity(topic: { lastActivityAt?: string, createdAt: string }) {
  return relativeLabel(topic.lastActivityAt || topic.createdAt)
}
</script>

<template>
  <main
    class="sforum-search"
    data-sforum-island-body="forum.component.search_page"
    :data-feed-state="state"
  >
    <!-- 极光层：视口级固定层，不参与布局，不接收指针事件 -->
    <div class="sf-aurora" aria-hidden="true">
      <span class="sf-aurora__blob sf-aurora__blob--1" />
      <span class="sf-aurora__blob sf-aurora__blob--2" />
      <span class="sf-aurora__blob sf-aurora__blob--3" />
      <span class="sf-aurora__grain" />
    </div>

    <div class="sforum-search__inner">
      <SFRegionOutlet page="forum.search" region="content_before" />

      <header class="sforum-search__intro">
        <h1 class="sforum-search__title">
          {{ t('search.title') }}
        </h1>
        <p class="sforum-search__subtitle">
          {{ t('search.heroSubtitle') }}
        </p>
      </header>

      <SFSearchHero v-model="draft" :busy="state === 'loading'" @submit="submitNow" />

      <SFSearchQuickTerms
        :terms="quickTerms"
        :group-label="t('search.quickTermsLabel')"
        :active-value="query"
        @select="setQuery"
      />

      <SFRegionOutlet page="forum.search" region="list_before" />

      <SFSearchResultSurface
        :state="state"
        :count="total"
        :query="query"
        :has-more="hasMore"
        :loading-more="loadingMore"
        :load-more-error="loadMoreError"
        @reset="reset"
        @retry="retry"
        @load-more="loadMore"
      >
        <!--
          两套渲染器同时存在于 DOM，由结果面的容器查询决定谁可见：
          ≥900px 走卡片（摘要才是搜索相关性的依据），<900px 走行式（复用已测的行组件）。
          用 CSS 而不是 JS 测量，是为了 SSR 首帧就正确、没有 hydration 换布局的 CLS。
        -->
        <div class="sforum-search__rows">
          <SFHomeTopicRow
            v-for="topic in items"
            :key="topic.id"
            class="sforum-search__row"
            :topic="topic"
            :to="localePath(forumTopicPath(topic, topicUrlMode))"
            :created-label="topicCreated(topic.createdAt)"
            :activity-label="topicActivity(topic)"
            :extension-list-badges="extensionListBadges"
            :highlight-query="query"
          />
        </div>
        <div class="sforum-search__cards">
          <SFSearchResultCard
            v-for="topic in items"
            :key="`card-${topic.id}`"
            :topic="topic"
            :to="localePath(forumTopicPath(topic, topicUrlMode))"
            :created-label="topicCreated(topic.createdAt)"
            :activity-label="topicActivity(topic)"
            :highlight-query="query"
          />
        </div>
      </SFSearchResultSurface>

      <SFRegionOutlet page="forum.search" region="list_after" />

      <div v-if="hasSidebarRegion" class="sforum-search__regions">
        <SFRegionOutlet page="forum.search" region="sidebar" />
      </div>

      <SFRegionOutlet page="forum.search" region="content_after" />
    </div>
  </main>
</template>

<style src="~/assets/css/sforum-search.css" lang="css"></style>
