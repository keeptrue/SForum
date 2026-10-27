<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useForumApi } from '~/composables/forum/useForumApi'
import SFHomeTopicRow from '~/components/forum/SFHomeTopicRow.vue'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFContentColumnFooter from '~/components/forum/SFContentColumnFooter.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFPublicPageHeader from '~/components/public/SFPublicPageHeader.vue'
import SFPublicMobileRightDrawerHeader from '~/components/navigation/SFPublicMobileRightDrawerHeader.vue'
import SFPublicMobileUserMenu from '~/components/navigation/SFPublicMobileUserMenu.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
/**
 * 宿主 body 岛：forum.category.show。主题 L1 挂载；路由页仅 SEO + fail-closed 回退。
 */

import {
  formatForumTopicListTotal,
  forumCategoryPath,
  forumTopicPath,
  type ForumCategoryGroup,
  type ForumTopicList,
  type ForumTopicSummary
} from '~/utils/forum/forumTaxonomy'

const { t } = useI18n()
const localePath = useLocalePath()
const route = useRoute()
const { seoSettings } = useWebOptions()
const topicUrlMode = computed(() => seoSettings.value.topicUrlMode)

const forumApi = useForumApi()
const { can } = usePermissions()

const categorySlug = computed(() => routeParam(route.params.categorySlug))
const currentPage = computed(() => parsePublicPage(route.query.page))
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))
// 移动端导航栏头像只翻转共享状态；抽屉由本 body 岛渲染，因此必须在此消费它。
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()
const renderedAt = useState<number>('forum-taxonomy-rendered-at', () => Date.now())
const emptyTopicList = (): ForumTopicList => ({
  items: [],
  total: 0,
  page: 1,
  perPage: 20
})

const { data: categoryGroups, pending: categoriesPending } = await useAsyncData(
  `forum-category-page-groups:${categorySlug.value}`,
  () => forumApi.listCategoryGroups(),
  { default: () => [] as ForumCategoryGroup[] }
)

const categories = computed(() => categoryGroups.value.flatMap((group) => group.categories || []))
const category = computed(() => categories.value.find((item) => item.slug === categorySlug.value))

useSForumSeo(computed(() => ({
  type: 'category',
  path: currentPage.value > 1 ? `${forumCategoryPath(categorySlug.value)}?page=${currentPage.value}` : forumCategoryPath(categorySlug.value),
  title: category.value?.name || categorySlug.value,
  description: category.value?.description,
  public: category.value?.visibility !== 'hidden',
  variables: { categoryName: category.value?.name || categorySlug.value },
  breadcrumbs: [
    { name: seoSettings.value.seoSiteName, path: '/' },
    { name: category.value?.name || categorySlug.value, path: forumCategoryPath(categorySlug.value) }
  ]
})))

const totalTopics = computed(() => categories.value.reduce((sum, item) => sum + item.topicCount, 0))

if (!category.value || category.value.visibility === 'hidden') {
  throw createError({
    statusCode: 404,
    statusMessage: 'Category not found'
  })
}

const { data: topicList, pending: topicsPending } = await useAsyncData(
  () => `forum-category-page-topics:${categorySlug.value}:${currentPage.value}`,
  () => forumApi.listTopics({
    categorySlug: categorySlug.value,
    page: currentPage.value
  }),
  {
    default: emptyTopicList,
    watch: [currentPage, categorySlug]
  }
)

const topics = computed(() => topicList.value.items)
const totalPages = computed(() => Math.ceil(topicList.value.total / Math.max(topicList.value.perPage, 1)) || 1)


function routeParam(value: string | string[] | undefined) {
  return Array.isArray(value) ? value[0] || '' : value || ''
}

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

function categoryPageTo(page: number) {
  return publicPageLocation(localePath(forumCategoryPath(categorySlug.value)), page)
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

function topicCreated(topic: ForumTopicSummary) {
  return topicRelativeLabel(topic.createdAt)
}

function topicActivity(topic: ForumTopicSummary) {
  return topicRelativeLabel(topic.lastActivityAt || topic.createdAt)
}

</script>

<template>

<main class="sforum-home">
    <div class="sforum-home__layout">
      <SFResponsivePublicSidebar
        owner-id="forum.category.show"
        :title="t('home.sidebar.drawerTitle')"
        class="sforum-home__sidebar"
      >
        <SFHomeNavigation
          desktop-only
          navigation-mode="route"
          :categories="categories"
          :selected-category-slug="categorySlug"
          :total-topics="totalTopics"
          :pending="categoriesPending"
          :can-create-topic="canCreateTopic"
        />
      </SFResponsivePublicSidebar>

      <section class="sforum-home__main sforum-content-column" aria-labelledby="category-page-title">
        <SFRegionOutlet page="forum.category.show" region="content_before" />

        <SFPublicPageHeader
          class="sforum-home__page-header"
          title-id="category-page-title"
          :title="category?.name || ''"
          :subtitle="category?.description || ''"
        >
          <template #eyebrow>
            <p v-if="category?.groupName" class="sforum-home__page-group">{{ category.groupName }}</p>
          </template>
          <template #meta>
            <div class="sforum-home__page-meta">
              {{ formatForumTopicListTotal(topicList, t) }}
            </div>
          </template>
        </SFPublicPageHeader>

        <SFRegionOutlet page="forum.category.show" region="list_before" />

        <div id="feed-list-container" class="sforum-home__feed-list" data-sf-region="topic-list">
          <template v-if="topicsPending">
            <div
              v-for="item in 6"
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

          <div v-else class="px-4 py-10 text-center">
            <SFEmptyState
              :title="t('home.emptyState.title')"
              :description="t('home.emptyState.description')"
            />
          </div>
        </div>

        <SFRegionOutlet page="forum.category.show" region="list_after" />

        <div v-if="topics.length > 0 && !topicsPending && totalPages > 1" class="mt-3">
          <SFPagination
            :page="currentPage"
            :total-pages="totalPages"
            :page-to="categoryPageTo"
          />
        </div>

        <SFRegionOutlet page="forum.category.show" region="content_after" />

        <SFContentColumnFooter />
      </section>
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
    </aside>
  </main>
</template>
