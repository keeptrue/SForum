<script setup lang="ts">
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useCategoryGroups } from '~/composables/forum/useCategoryGroups'
import { useForumApi } from '~/composables/forum/useForumApi'
import SFTagIndexRightRail from '~/components/forum/SFTagIndexRightRail.vue'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFContentColumnFooter from '~/components/forum/SFContentColumnFooter.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFPublicPageHeader from '~/components/public/SFPublicPageHeader.vue'
import SFPublicMobileRightDrawerHeader from '~/components/navigation/SFPublicMobileRightDrawerHeader.vue'
import SFPublicMobileUserMenu from '~/components/navigation/SFPublicMobileUserMenu.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
/**
 * 宿主 body 岛：forum.tag.index。
 * 三栏壳对齐首页 / 通知 / 分类目录；呈现由主题 L1 挂载，路由页仅 SEO + fail-closed 回退。
 */

import {
  activeForumTags,
  compareTagsByHeat,
  filterTagIndexTags,
  recentTagIndexTags,
  tagDisplayDescription,
  tagHeatEntries,
  tagIndexOverview,
  type TagIndexFilter
} from '~/utils/forum/forumTagsIndex'
import {
  forumTagPath,
  parseForumTagPublicPagesOption,
  type ForumTag
} from '~/utils/forum/forumTaxonomy'

const TAG_HEAT_LIMIT = 6
const RAIL_HOT_LIMIT = 6
const RAIL_RECENT_LIMIT = 4

const TAG_FILTERS: Array<{ key: TagIndexFilter, icon: string, labelKey: string }> = [
  { key: 'all', icon: 'i-lucide-tags', labelKey: 'taxonomy.tags.filters.all' },
  { key: 'hot', icon: 'i-lucide-flame', labelKey: 'taxonomy.tags.filters.hot' },
  { key: 'week', icon: 'i-lucide-calendar-days', labelKey: 'taxonomy.tags.filters.week' },
  { key: 'az', icon: 'i-lucide-arrow-down-a-z', labelKey: 'taxonomy.tags.filters.az' }
]

const { t, locale } = useI18n()
const localePath = useLocalePath()
const { format: formatSiteDateTime } = useSiteDateTime()
const { webOption } = useWebOptions()
const forumApi = useForumApi()
const { can } = usePermissions()

const publicTagPagesEnabled = computed(() => parseForumTagPublicPagesOption(
  webOption('forum.tags.public_pages', 'enabled')
))

if (!publicTagPagesEnabled.value) {
  throw createError({
    statusCode: 404,
    statusMessage: 'Tag pages are disabled'
  })
}

const filter = ref<TagIndexFilter>('all')
const searchQuery = ref('')
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))
const renderedAt = useState<number>('forum-tags-index-rendered-at', () => Date.now())

const { data: categoryGroups, pending: categoriesPending } = await useCategoryGroups()

const {
  data: tags,
  pending: tagsPending,
  error: tagsError,
  refresh: refreshTags
} = await useAsyncData(
  'forum-tags-index-tags',
  () => forumApi.listTags(),
  { default: () => [] as ForumTag[] }
)

const categories = computed(() => categoryGroups.value.flatMap((group) => group.categories || []))
const activeTags = computed(() => activeForumTags(tags.value || []))
const totalTopics = computed(() => categories.value.reduce((sum, category) => sum + category.topicCount, 0))
const overview = computed(() => tagIndexOverview(activeTags.value, renderedAt.value))
const filteredTags = computed(() => filterTagIndexTags(activeTags.value, {
  filter: filter.value,
  query: searchQuery.value,
  locale: locale.value,
  nowMs: renderedAt.value
}))
const heatEntries = computed(() => tagHeatEntries(filteredTags.value, TAG_HEAT_LIMIT, locale.value))
const railHotTags = computed(() =>
  activeTags.value
    .slice()
    .sort((left, right) => compareTagsByHeat(left, right, locale.value))
    .slice(0, RAIL_HOT_LIMIT)
)
const railRecentTags = computed(() =>
  recentTagIndexTags(activeTags.value, RAIL_RECENT_LIMIT, renderedAt.value, locale.value)
)
const hasQuery = computed(() => searchQuery.value.trim().length > 0)
const emptyTitle = computed(() => {
  if (hasQuery.value) return t('taxonomy.tags.emptySearchTitle')
  if (filter.value === 'hot') return t('taxonomy.tags.emptyHotTitle')
  if (filter.value === 'week') return t('taxonomy.tags.emptyWeekTitle')
  return t('taxonomy.tags.emptyTitle')
})
const emptyDescription = computed(() => {
  if (hasQuery.value) return t('taxonomy.tags.emptySearchDescription')
  if (filter.value === 'hot') return t('taxonomy.tags.emptyHotDescription')
  if (filter.value === 'week') return t('taxonomy.tags.emptyWeekDescription')
  return t('taxonomy.tags.emptyDescription')
})

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

function setFilter(next: TagIndexFilter, closeDrawer = false) {
  filter.value = next
  if (closeDrawer) {
    closeMobileDrawers()
  }
}

function tagPath(slug: string) {
  return localePath(forumTagPath(slug))
}

function formatCount(value: number) {
  return new Intl.NumberFormat(locale.value).format(value)
}

function formatDate(value: string) {
  return value ? formatSiteDateTime(value) : t('taxonomy.tags.noCreatedAt')
}

function descriptionFor(tag: ForumTag) {
  return tagDisplayDescription(tag, t('taxonomy.tags.noDescription'))
}

function retryTags() {
  void refreshTags()
}
</script>

<template>
  <main
    class="sforum-home sforum-tags-page"
    data-sforum-island-body="forum.component.tag_index"
    data-layout="fullwidth-3col"
  >
    <div class="sforum-home__layout sforum-home__layout--with-right">
      <SFResponsivePublicSidebar
        owner-id="forum.tag.index"
        :title="t('home.sidebar.drawerTitle')"
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
            <nav class="sforum-tags-page__filter-nav" :aria-label="t('taxonomy.tags.filterLabel')">
              <div class="sf-home-navigation__label">{{ t('taxonomy.tags.filterLabel') }}</div>
              <button
                v-for="item in TAG_FILTERS"
                :key="item.key"
                type="button"
                class="sf-home-navigation__link"
                :class="{ 'is-active': filter === item.key }"
                :aria-pressed="filter === item.key"
                @click="setFilter(item.key, true)"
              >
                <span class="sf-home-navigation__link-main">
                  <UIcon :name="item.icon" class="size-[18px]" aria-hidden="true" />
                  {{ t(item.labelKey) }}
                </span>
              </button>
            </nav>
          </template>
        </SFHomeNavigation>
      </SFResponsivePublicSidebar>

      <section class="sforum-home__main sforum-tags-page__main sforum-content-column" aria-labelledby="tag-index-title">
        <SFRegionOutlet page="forum.tag.index" region="content_before" />

        <SFPublicPageHeader
          class="sforum-tags-page__head"
          title-id="tag-index-title"
          :title="t('taxonomy.tags.title')"
          :subtitle="t('taxonomy.tags.description')"
        >
          <template #aside>
            <div class="sforum-tags-page__head-count">
              <strong>{{ formatCount(overview.totalTags) }}</strong>
              <span>{{ t('taxonomy.tags.headerCount') }}</span>
            </div>
          </template>
        </SFPublicPageHeader>

        <div class="sforum-tags-page__toolbar">
          <label class="sforum-tags-page__search">
            <UIcon name="i-lucide-search" class="size-4" aria-hidden="true" />
            <input
              v-model="searchQuery"
              type="search"
              :placeholder="t('taxonomy.tags.filterPlaceholder')"
              :aria-label="t('taxonomy.tags.filterPlaceholder')"
            >
          </label>

          <!-- 主列快捷筛选：与左栏共享 filter，中宽屏左栏隐藏时仍可用 -->
          <div class="sforum-tags-page__filters" role="group" :aria-label="t('taxonomy.tags.filterLabel')">
            <button
              v-for="item in TAG_FILTERS"
              :key="`main-${item.key}`"
              type="button"
              class="sforum-tags-page__filter"
              :class="{ 'is-active': filter === item.key }"
              :aria-pressed="filter === item.key"
              @click="setFilter(item.key)"
            >
              {{ t(item.labelKey) }}
            </button>
          </div>
        </div>

        <SFRegionOutlet page="forum.tag.index" region="list_before" />

        <div v-if="tagsError" class="sforum-tags-page__error" role="alert">
          <span>{{ t('taxonomy.tags.loadFailed') }}</span>
          <button type="button" class="sf-button sf-button--ghost sf-button--sm" @click="retryTags">
            <UIcon name="i-lucide-refresh-cw" class="size-4" aria-hidden="true" />
            {{ t('home.feed.retry') }}
          </button>
        </div>

        <template v-else>
          <section class="sforum-tags-page__section" :aria-busy="tagsPending">
            <div class="sforum-tags-page__section-caption">
              <h2>{{ t('taxonomy.tags.heatTitle') }}</h2>
              <span>{{ t('taxonomy.tags.heatSubtitle') }}</span>
            </div>

            <div v-if="tagsPending" class="sforum-tags-page__pending">
              <SFSkeleton v-for="item in 6" :key="item" :lines="1" />
            </div>

            <div v-else-if="heatEntries.length" class="sforum-tags-page__heat-board">
              <NuxtLink
                v-for="entry in heatEntries"
                :key="`heat-${entry.tag.id}`"
                :to="tagPath(entry.tag.slug)"
                class="sforum-tags-page__heat-row"
              >
                <span class="sforum-tags-page__heat-label">
                  <span>#{{ entry.tag.name }}</span>
                </span>
                <span class="sforum-tags-page__heat-track" aria-hidden="true">
                  <i :style="{ width: `${entry.widthPercent}%` }" />
                </span>
                <span class="sforum-tags-page__heat-count">{{ formatCount(entry.tag.topicCount || 0) }}</span>
              </NuxtLink>
            </div>

            <div v-else class="sforum-tags-page__empty">
              <SFEmptyState
                :title="emptyTitle"
                :description="emptyDescription"
              />
            </div>
          </section>

          <section class="sforum-tags-page__section" :aria-busy="tagsPending">
            <div class="sforum-tags-page__section-caption">
              <h2>{{ t('taxonomy.tags.directoryTitle') }}</h2>
              <span>{{ t('taxonomy.tags.directorySummary', { count: formatCount(filteredTags.length) }) }}</span>
            </div>

            <div v-if="tagsPending" class="sforum-tags-page__pending sforum-tags-page__pending--grid">
              <SFSkeleton v-for="item in 8" :key="item" :lines="2" />
            </div>

            <div v-else-if="filteredTags.length" class="sforum-tags-page__directory">
              <NuxtLink
                v-for="tag in filteredTags"
                :key="tag.id"
                :to="tagPath(tag.slug)"
                class="sforum-tags-page__tag"
              >
                <span class="sforum-tags-page__tag-copy">
                  <strong>#{{ tag.name }}</strong>
                  <span>{{ descriptionFor(tag) }}</span>
                  <small>{{ tag.slug }} · {{ formatDate(tag.createdAt) }}</small>
                </span>
                <b>{{ formatCount(tag.topicCount || 0) }}</b>
              </NuxtLink>
            </div>

            <div v-else class="sforum-tags-page__empty">
              <SFEmptyState
                :title="emptyTitle"
                :description="emptyDescription"
              />
            </div>
          </section>
        </template>

        <SFRegionOutlet page="forum.tag.index" region="list_after" />

        <SFRegionOutlet page="forum.tag.index" region="content_after" />

        <SFContentColumnFooter />
      </section>

      <aside class="sforum-home__right" :aria-label="t('taxonomy.tags.railLabel')">
        <SFTagIndexRightRail
          :overview="overview"
          :hot-tags="railHotTags"
          :recent-tags="railRecentTags"
        />
        <SFRegionOutlet page="forum.tag.index" region="sidebar" />
      </aside>
    </div>

    <button
      v-if="mobileInfoOpen"
      type="button"
      class="sforum-mobile-drawer__backdrop"
      :aria-label="t('common.close')"
      @click="closeMobileDrawers"
    />

    <aside v-if="mobileInfoOpen" class="sforum-mobile-drawer sforum-mobile-drawer--right">
      <SFPublicMobileRightDrawerHeader
        :title="t('home.rightRail.drawerTitle')"
        :close-label="t('common.close')"
        @close="closeMobileDrawers"
      />
      <SFPublicMobileUserMenu />
      <aside class="sforum-home__right" :aria-label="t('taxonomy.tags.railLabel')">
        <SFTagIndexRightRail
          :overview="overview"
          :hot-tags="railHotTags"
          :recent-tags="railRecentTags"
          close-on-navigate
          @navigate="closeMobileDrawers"
        />
      </aside>
    </aside>
  </main>
</template>
