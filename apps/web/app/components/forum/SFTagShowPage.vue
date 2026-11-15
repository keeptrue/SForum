<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useCategoryGroups } from '~/composables/forum/useCategoryGroups'
import { useForumApi } from '~/composables/forum/useForumApi'
import SFTagShowRightRail from '~/components/forum/SFTagShowRightRail.vue'
import SFHomeTopicRow from '~/components/forum/SFHomeTopicRow.vue'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFContentColumnFooter from '~/components/forum/SFContentColumnFooter.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFPublicPageHeader from '~/components/public/SFPublicPageHeader.vue'
import SFPublicMobileRightDrawerHeader from '~/components/navigation/SFPublicMobileRightDrawerHeader.vue'
import SFPublicMobileUserMenu from '~/components/navigation/SFPublicMobileUserMenu.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
/**
 * 宿主 body 岛：forum.tag.show。
 * 三栏壳对齐标签索引 / 首页 / 通知；主题 L1 挂载，路由页仅 SEO + fail-closed 回退。
 */

import { compareTagsByHeat } from '~/utils/forum/forumTagsIndex'
import {
  formatForumTopicListTotal,
  forumTagPath,
  forumTagsIndexPath,
  forumTopicPath,
  parseForumTagPublicPagesOption,
  type ForumTag,
  type ForumTopicList,
  type ForumTopicSummary
} from '~/utils/forum/forumTaxonomy'

const RELATED_NAV_LIMIT = 12
const RAIL_RELATED_LIMIT = 6

// 使用 composer 闭包，避免模板直接绑定 $setup.t（async setup + 主题岛 SSR 下偶发非函数）
const i18n = useI18n()
const translate = (key: string, params?: Record<string, unknown>) => {
  const value = params ? i18n.t(key, params) : i18n.t(key)
  return typeof value === 'string' ? value : key
}
const localePath = useLocalePath()
const route = useRoute()
const { seoSettings, webOption } = useWebOptions()
const topicUrlMode = computed(() => seoSettings.value.topicUrlMode)

const forumApi = useForumApi()
const { can } = usePermissions()

const tagSlug = computed(() => routeParam(route.params.tagSlug))
const publicTagPagesEnabled = computed(() => parseForumTagPublicPagesOption(
  webOption('forum.tags.public_pages', 'enabled')
))
const currentPage = computed(() => parsePublicPage(route.query.page))
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()
const renderedAt = useState<number>('forum-taxonomy-rendered-at', () => Date.now())
const emptyTopicList = (): ForumTopicList => ({
  items: [],
  total: 0,
  page: 1,
  perPage: 20
})

if (!publicTagPagesEnabled.value) {
  throw createError({
    statusCode: 404,
    statusMessage: 'Tag pages are disabled'
  })
}

// 三个公开读请求互不依赖，并发启动可避免 SSR 首屏按接口耗时逐个累加。
const [categoryGroupsResult, activeTagsResult, topicListResult] = await Promise.all([
  useCategoryGroups(),
  useAsyncData(
    'forum-tag-page-tags',
    async () => (await forumApi.listTags()).filter((item) => item.status === 'active'),
    { default: () => [] as ForumTag[] }
  ),
  useAsyncData(
    () => `forum-tag-page-topics:${tagSlug.value}:${currentPage.value}`,
    () => forumApi.listTopics({
      tagSlug: tagSlug.value,
      page: currentPage.value
    }),
    {
      default: emptyTopicList,
      watch: [currentPage, tagSlug]
    }
  )
] as const)

const { data: categoryGroupsData, pending: categoriesPending } = categoryGroupsResult
const { data: activeTagsData } = activeTagsResult
const { data: topicListData, pending: topicsPending } = topicListResult

// HMR 与响应式 key 切换期间 AsyncData 可能短暂清空；模板始终只读取稳定形状。
const categoryGroups = computed(() => categoryGroupsData.value || [])
const activeTags = computed(() => activeTagsData.value || [])
const topicList = computed<ForumTopicList>(() => {
  const value = topicListData.value
  return value && Array.isArray(value.items) ? value : emptyTopicList()
})
const categories = computed(() => categoryGroups.value.flatMap((group) => group.categories || []))
const totalTopics = computed(() => categories.value.reduce((sum, item) => sum + item.topicCount, 0))
const tag = computed(() => activeTags.value.find((item) => item.slug === tagSlug.value))

if (!tag.value) {
  throw createError({
    statusCode: 404,
    statusMessage: 'Tag not found'
  })
}

const topics = computed(() => topicList.value.items)
const totalPages = computed(() => Math.ceil(topicList.value.total / Math.max(topicList.value.perPage, 1)) || 1)
const hasActiveTags = computed(() => activeTags.value.length > 0)
const hasTopics = computed(() => topics.value.length > 0)
const showPagination = computed(() => hasTopics.value && totalPages.value > 1)
const tagsHeading = computed(() => translate('home.tags'))
const topicListTotalLabel = computed(() => formatForumTopicListTotal(topicList.value, translate))
const emptyStateTitle = computed(() => translate('home.emptyState.title'))
const emptyStateDescription = computed(() => translate('home.emptyState.description'))

/** 左栏相关标签：按热度，当前标签置顶高亮 */
const relatedNavTags = computed(() => {
  const locale = i18n.locale.value
  const sorted = activeTags.value
    .slice()
    .sort((left, right) => compareTagsByHeat(left, right, locale))
  const current = tag.value
  if (!current) {
    return sorted.slice(0, RELATED_NAV_LIMIT)
  }
  const others = sorted.filter((item) => item.slug !== current.slug).slice(0, RELATED_NAV_LIMIT - 1)
  return [current, ...others]
})

/** 右栏相关热门：排除当前 */
const railRelatedTags = computed(() => {
  const locale = i18n.locale.value
  return activeTags.value
    .slice()
    .filter((item) => item.slug !== tagSlug.value)
    .sort((left, right) => compareTagsByHeat(left, right, locale))
    .slice(0, RAIL_RELATED_LIMIT)
})

useSForumSeo(computed(() => ({
  type: 'tag',
  path: currentPage.value > 1 ? `${forumTagPath(tagSlug.value)}?page=${currentPage.value}` : forumTagPath(tagSlug.value),
  title: tag.value?.name || tagSlug.value,
  description: tag.value?.description,
  public: tag.value?.status === 'active',
  noindex: topicList.value.total === 0,
  variables: { tagName: tag.value?.name || tagSlug.value },
  breadcrumbs: [
    { name: seoSettings.value.seoSiteName, path: '/' },
    { name: translate('taxonomy.tags.title'), path: forumTagsIndexPath() },
    { name: tag.value?.name || tagSlug.value, path: forumTagPath(tagSlug.value) }
  ]
})))

function routeParam(value: string | string[] | undefined) {
  return Array.isArray(value) ? value[0] || '' : value || ''
}

function tagPageTo(page: number) {
  return publicPageLocation(localePath(forumTagPath(tagSlug.value)), page)
}

function tagTo(slug: string) {
  return localePath(forumTagPath(slug))
}

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
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
    return translate('home.feed.activityMinutes', { count: Math.max(1, Math.floor(diffMs / minute)) })
  }
  if (diffMs >= 0 && diffMs < day) {
    return translate('home.feed.activityHours', { count: Math.max(1, Math.floor(diffMs / hour)) })
  }
  if (diffMs >= 0 && diffMs < 7 * day) {
    return translate('home.feed.activityDays', { count: Math.max(1, Math.floor(diffMs / day)) })
  }

  return date.toISOString().slice(0, 10)
}

function topicCreated(topic: ForumTopicSummary) {
  return topicRelativeLabel(topic.createdAt)
}

function topicActivity(topic: ForumTopicSummary) {
  return topicRelativeLabel(topic.lastActivityAt || topic.createdAt)
}
</script>

<template>
  <main
    class="sforum-home sforum-tags-page sforum-tag-show"
    data-sforum-island-body="forum.component.tag_show"
    data-layout="fullwidth-3col"
  >
    <div class="sforum-home__layout sforum-home__layout--with-right">
      <SFResponsivePublicSidebar
        owner-id="forum.tag.show"
        :title="translate('home.sidebar.drawerTitle')"
        class="sforum-home__sidebar"
      >
        <SFHomeNavigation
          desktop-only
          navigation-mode="route"
          :categories="categories"
          selected-category-slug=""
          :total-topics="totalTopics"
          :pending="categoriesPending"
          :can-create-topic="canCreateTopic"
          :show-categories="false"
        >
          <template #after-navigation>
            <nav class="sforum-tags-page__filter-nav" :aria-label="translate('taxonomy.tags.show.relatedNav')">
              <div class="sf-home-navigation__label">{{ translate('taxonomy.tags.show.relatedNav') }}</div>
              <NuxtLink
                :to="localePath(forumTagsIndexPath())"
                class="sf-home-navigation__link"
              >
                <span class="sf-home-navigation__link-main">
                  <UIcon name="i-lucide-layout-grid" class="size-[18px]" aria-hidden="true" />
                  {{ translate('taxonomy.tags.show.viewAll') }}
                </span>
              </NuxtLink>
              <NuxtLink
                v-for="item in relatedNavTags"
                :key="item.slug"
                :to="tagTo(item.slug)"
                class="sf-home-navigation__link"
                :class="{ 'is-active': item.slug === tagSlug }"
              >
                <span class="sf-home-navigation__link-main">
                  <UIcon name="i-lucide-hash" class="size-[18px]" aria-hidden="true" />
                  {{ item.name }}
                </span>
                <span class="sf-home-navigation__count">{{ item.topicCount || 0 }}</span>
              </NuxtLink>
            </nav>
          </template>
        </SFHomeNavigation>
      </SFResponsivePublicSidebar>

      <section class="sforum-home__main sforum-tags-page__main sforum-content-column" aria-labelledby="tag-page-title">
        <SFRegionOutlet page="forum.tag.show" region="content_before" />

        <SFPublicPageHeader
          class="sforum-home__page-header"
          title-id="tag-page-title"
          :title="tag?.name ? `#${tag.name}` : ''"
          :subtitle="tag?.description || ''"
        >
          <template #eyebrow>
            <p class="sforum-home__page-group">{{ tagsHeading }}</p>
          </template>
          <template #meta>
            <div class="sforum-home__page-meta">
              {{ topicListTotalLabel }}
            </div>
          </template>
        </SFPublicPageHeader>

        <div v-if="hasActiveTags" class="sforum-home__filters">
          <div class="sforum-home__tag-list">
            <NuxtLink
              v-for="item in activeTags"
              :key="item.slug"
              :to="tagTo(item.slug)"
              class="sforum-home__tag"
              :class="{ 'is-active': item.slug === tagSlug }"
            >
              <span>#{{ item.name }}</span>
            </NuxtLink>
          </div>
        </div>

        <SFRegionOutlet page="forum.tag.show" region="list_before" />

        <div
          id="feed-list-container"
          class="sforum-home__feed-list mt-3 overflow-hidden rounded-[var(--sf-public-radius,6px)] border border-[var(--sf-public-border)] bg-[var(--sf-public-surface)] shadow-[var(--sf-public-shadow)]"
          data-sf-region="topic-list"
        >
          <template v-if="topicsPending">
            <div
              v-for="item in 6"
              :key="item"
              class="border-b border-[var(--sf-border-light,#eef0f3)] p-3.5 last:border-b-0"
            >
              <SFSkeleton avatar :lines="2" />
            </div>
          </template>

          <template v-else-if="hasTopics">
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

          <div v-else class="px-4 py-10 text-center">
            <SFEmptyState
              :title="emptyStateTitle"
              :description="emptyStateDescription"
            />
          </div>
        </div>

        <SFRegionOutlet page="forum.tag.show" region="list_after" />

        <div v-if="showPagination && !topicsPending" class="mt-3">
          <SFPagination
            :page="currentPage"
            :total-pages="totalPages"
            :page-to="tagPageTo"
          />
        </div>

        <SFRegionOutlet page="forum.tag.show" region="content_after" />

        <SFContentColumnFooter />
      </section>

      <aside
        v-if="tag"
        class="sforum-home__right"
        :aria-label="translate('taxonomy.tags.railLabel')"
      >
        <SFTagShowRightRail
          :tag="tag"
          :topic-total="topicList.total"
          :related-tags="railRelatedTags"
        />
        <SFRegionOutlet page="forum.tag.show" region="sidebar" />
      </aside>
    </div>

    <button
      v-if="mobileInfoOpen"
      type="button"
      class="sforum-mobile-drawer__backdrop"
      :aria-label="translate('common.close')"
      @click="closeMobileDrawers"
    />

    <aside v-if="mobileInfoOpen && tag" class="sforum-mobile-drawer sforum-mobile-drawer--right">
      <SFPublicMobileRightDrawerHeader
        :title="translate('home.rightRail.drawerTitle')"
        :close-label="translate('common.close')"
        @close="closeMobileDrawers"
      />
      <SFPublicMobileUserMenu />
      <aside class="sforum-home__right" :aria-label="translate('taxonomy.tags.railLabel')">
        <SFTagShowRightRail
          :tag="tag"
          :topic-total="topicList.total"
          :related-tags="railRelatedTags"
          close-on-navigate
          @navigate="closeMobileDrawers"
        />
      </aside>
    </aside>
  </main>
</template>
