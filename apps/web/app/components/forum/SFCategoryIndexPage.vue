<script setup lang="ts">
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useForumApi } from '~/composables/forum/useForumApi'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFContentColumnFooter from '~/components/forum/SFContentColumnFooter.vue'
import SFPublicPageHeader from '~/components/public/SFPublicPageHeader.vue'
import SFPublicMobileRightDrawerHeader from '~/components/navigation/SFPublicMobileRightDrawerHeader.vue'
import SFPublicMobileUserMenu from '~/components/navigation/SFPublicMobileUserMenu.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
/**
 * 宿主 body 岛：forum.category.index。主题 L1 挂载；路由页仅 SEO + fail-closed 回退。
 */

import {
  activeCategoryDirectoryCategories,
  buildCategoryDirectoryDisplayGroups,
  categoryDirectoryGroupKey,
  findCategoryDirectoryGroup,
  summarizeCategoryDirectory,
  summarizeCategoryDirectoryDisplay,
  visibleCategoryDirectoryGroups,
  type CategoryDirectoryFilter
} from '~/utils/forum/forumCategoryDirectory'
import {
  forumCategoryPath,
  taxonomyIconColor,
  type ForumCategory,
  type ForumCategoryGroup
} from '~/utils/forum/forumTaxonomy'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const localePath = useLocalePath()
const forumApi = useForumApi()
const { can } = usePermissions()

const CATEGORY_FILTERS: Array<{ key: CategoryDirectoryFilter, icon: string, labelKey: string }> = [
  { key: 'all', icon: 'i-lucide-layout-grid', labelKey: 'taxonomy.categories.filters.all' },
  { key: 'hot', icon: 'i-lucide-flame', labelKey: 'taxonomy.categories.filters.hot' },
  { key: 'week', icon: 'i-lucide-calendar-days', labelKey: 'taxonomy.categories.filters.week' },
  { key: 'az', icon: 'i-lucide-arrow-down-a-z', labelKey: 'taxonomy.categories.filters.az' }
]

const filter = ref<CategoryDirectoryFilter>('all')
const filterDraft = ref('')
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))
const ALL_GROUPS_VALUE = '__all_groups__'
const renderedAt = useState<number>('forum-categories-index-rendered-at', () => Date.now())

const { data: groups, pending, error, refresh } = await useAsyncData(
  'forum-categories-index',
  () => forumApi.listCategoryGroups(),
  { default: () => [] as ForumCategoryGroup[] }
)

const visibleGroups = computed(() => visibleCategoryDirectoryGroups(groups.value))
const categories = computed(() => visibleGroups.value.flatMap((group) => group.categories))
const totalStats = computed(() => summarizeCategoryDirectory(visibleGroups.value))
const focusedGroupKey = computed(() => queryValue(route.query.group))
const focusedGroup = computed(() => focusedGroupKey.value
  ? findCategoryDirectoryGroup(visibleGroups.value, focusedGroupKey.value)
  : undefined
)
const hasFocusedGroup = computed(() => Boolean(focusedGroup.value))
const normalizedFocusedKey = computed(() => focusedGroup.value ? categoryDirectoryGroupKey(focusedGroup.value) : '')
const displayGroups = computed(() => buildCategoryDirectoryDisplayGroups(visibleGroups.value, {
  filter: filter.value,
  locale: locale.value,
  focusedGroupKey: normalizedFocusedKey.value,
  query: filterDraft.value,
  nowMs: renderedAt.value
}))
const displayStats = computed(() => summarizeCategoryDirectoryDisplay(displayGroups.value))
const activeCategories = computed(() => activeCategoryDirectoryCategories(visibleGroups.value, 5, locale.value))
const groupOptions = computed(() => [
  { label: t('taxonomy.categories.allGroups'), value: ALL_GROUPS_VALUE },
  ...visibleGroups.value.map(group => ({
    label: group.name,
    value: categoryDirectoryGroupKey(group)
  }))
])
const selectedGroupKey = computed({
  get: () => normalizedFocusedKey.value || ALL_GROUPS_VALUE,
  set: (value: string) => {
    if (value === ALL_GROUPS_VALUE) {
      void showAllGroups()
      return
    }
    const group = findCategoryDirectoryGroup(visibleGroups.value, value)
    if (group) {
      void focusGroup(group)
      return
    }
    void showAllGroups()
  }
})
const pageTitle = computed(() => focusedGroup.value?.name || t('taxonomy.categories.title'))
const pageDescription = computed(() =>
  focusedGroup.value?.description
  || t('taxonomy.categories.description')
)
const hasFilter = computed(() => filterDraft.value.trim().length > 0)
const showDirectory = computed(() => displayGroups.value.length > 0)
const showGlobalEmpty = computed(() => !pending.value && !error.value && visibleGroups.value.length === 0)
const showFilterEmpty = computed(() =>
  !pending.value
  && !error.value
  && visibleGroups.value.length > 0
  && displayGroups.value.length === 0
)
const filterEmptyTitle = computed(() => {
  if (hasFilter.value) return t('taxonomy.categories.filterEmptyTitle')
  if (filter.value === 'hot') return t('taxonomy.categories.emptyHotTitle')
  if (filter.value === 'week') return t('taxonomy.categories.emptyWeekTitle')
  return t('taxonomy.categories.filterEmptyTitle')
})
const filterEmptyDescription = computed(() => {
  if (hasFilter.value) return t('taxonomy.categories.filterEmptyDescription')
  if (filter.value === 'hot') return t('taxonomy.categories.emptyHotDescription')
  if (filter.value === 'week') return t('taxonomy.categories.emptyWeekDescription')
  return t('taxonomy.categories.filterEmptyDescription')
})

function queryValue(value: unknown) {
  return Array.isArray(value) ? String(value[0] || '') : String(value || '')
}

function formatCount(value: number) {
  return new Intl.NumberFormat(locale.value).format(Math.max(0, Math.trunc(value || 0)))
}

function groupLabel(group: { categories: ForumCategory[] }) {
  return t('taxonomy.categories.groupCount', { count: group.categories.length })
}

function categoryTo(category: ForumCategory) {
  return localePath(forumCategoryPath(category.slug))
}

function categoryAccent(category: ForumCategory) {
  return taxonomyIconColor(category.iconColor)
}

function categoryIconName(category: ForumCategory) {
  const icon = category.icon?.trim() || ''
  return icon.startsWith('i-') ? icon : 'i-lucide-folder'
}

function setFilter(next: CategoryDirectoryFilter, closeDrawer = false) {
  filter.value = next
  if (closeDrawer) {
    closeMobileDrawers()
  }
}

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

async function focusGroup(group: ForumCategoryGroup) {
  closeMobileDrawers()
  await router.push({
    path: localePath('/categories'),
    query: { ...route.query, group: categoryDirectoryGroupKey(group) }
  })
}

async function showAllGroups() {
  closeMobileDrawers()
  const { group: _group, ...query } = route.query
  await router.push({
    path: localePath('/categories'),
    query
  })
}

function clearFilter() {
  filterDraft.value = ''
}

function showAllCategories() {
  filterDraft.value = ''
  filter.value = 'all'
}

async function retryLoad() {
  await refresh()
}
</script>

<template>
  <main
    class="sforum-home sforum-category-directory"
    data-sforum-island-body="forum.component.category_index"
    data-layout="fullwidth-3col"
  >
    <div class="sforum-home__layout sforum-home__layout--with-right sforum-category-directory__layout">
      <SFResponsivePublicSidebar
        owner-id="forum.category.index"
        :title="t('home.sidebar.drawerTitle')"
        class="sforum-home__sidebar"
      >
        <SFHomeNavigation
          desktop-only
          navigation-mode="route"
          :categories="categories"
          :selected-category-slug="''"
          :total-topics="totalStats.topicCount"
          :pending="pending"
          :can-create-topic="canCreateTopic"
          :show-categories="false"
        >
          <template #after-navigation>
            <nav class="sforum-category-directory__filter-nav" :aria-label="t('taxonomy.categories.filterLabel')">
              <div class="sf-home-navigation__label">{{ t('taxonomy.categories.filterLabel') }}</div>
              <button
                v-for="item in CATEGORY_FILTERS"
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

      <section class="sforum-home__main sforum-category-directory__main sforum-content-column" aria-labelledby="category-directory-title">
        <SFRegionOutlet page="forum.category.index" region="content_before" />

        <SFPublicPageHeader
          class="sforum-category-directory__head"
          title-id="category-directory-title"
          :title="pageTitle"
          :subtitle="pageDescription"
        >
          <template #aside>
            <div
              class="sforum-category-directory__summary"
              :aria-label="t('taxonomy.categories.visibleSummary', {
                groups: formatCount(displayStats.groupCount),
                categories: formatCount(displayStats.categoryCount)
              })"
            >
              <div>
                <strong>{{ formatCount(displayStats.groupCount) }}</strong>
                <span>{{ t('taxonomy.categories.stats.groups') }}</span>
              </div>
              <div>
                <strong>{{ formatCount(displayStats.categoryCount) }}</strong>
                <span>{{ t('taxonomy.categories.stats.categories') }}</span>
              </div>
            </div>
          </template>
        </SFPublicPageHeader>

        <div class="sforum-category-directory__toolbar">
          <div class="sforum-category-directory__filters">
            <USelect
              v-model="selectedGroupKey"
              :items="groupOptions"
              value-key="value"
              label-key="label"
              class="sforum-category-directory__group-select"
              :aria-label="t('taxonomy.categories.groupFilterLabel')"
            />
            <label class="sforum-category-directory__filter">
              <UIcon name="i-lucide-search" class="size-4" aria-hidden="true" />
              <input
                v-model="filterDraft"
                type="search"
                :placeholder="t('taxonomy.categories.filterPlaceholder')"
                :aria-label="t('taxonomy.categories.filterPlaceholder')"
              >
              <button
                v-if="hasFilter"
                type="button"
                :aria-label="t('taxonomy.categories.clearFilter')"
                :title="t('taxonomy.categories.clearFilter')"
                @click="clearFilter"
              >
                <UIcon name="i-lucide-x" class="size-4" aria-hidden="true" />
              </button>
            </label>
          </div>

          <div class="sforum-category-directory__sorts" role="group" :aria-label="t('taxonomy.categories.filterLabel')">
            <button
              v-for="item in CATEGORY_FILTERS"
              :key="`main-${item.key}`"
              type="button"
              class="sforum-category-directory__sort"
              :class="{ 'is-active': filter === item.key }"
              :aria-pressed="filter === item.key"
              @click="setFilter(item.key)"
            >
              {{ t(item.labelKey) }}
            </button>
          </div>
        </div>

        <div v-if="hasFocusedGroup || hasFilter" class="sforum-category-directory__scope">
          <span>
            {{ t('taxonomy.categories.visibleSummary', {
              groups: formatCount(displayStats.groupCount),
              categories: formatCount(displayStats.categoryCount)
            }) }}
          </span>
          <button v-if="hasFocusedGroup" type="button" @click="showAllGroups">
            <UIcon name="i-lucide-layers-3" class="size-4" aria-hidden="true" />
            {{ t('taxonomy.categories.returnAllGroups') }}
          </button>
        </div>

        <SFRegionOutlet page="forum.category.index" region="list_before" />

        <div v-if="error" class="sforum-category-directory__state" role="alert">
          <SFEmptyState
            :title="t('taxonomy.categories.loadFailedTitle')"
            :description="t('taxonomy.categories.loadFailedDescription')"
            :action-label="t('home.feed.retry')"
            @action="retryLoad"
          />
        </div>

        <div v-else-if="pending" class="sforum-category-directory__pending" aria-busy="true">
          <div v-for="item in 6" :key="item" class="sforum-category-directory__skeleton">
            <SFSkeleton :lines="2" />
          </div>
        </div>

        <div v-else-if="showDirectory" class="sforum-category-directory__groups">
          <section
            v-for="group in displayGroups"
            :id="`category-group-${categoryDirectoryGroupKey(group)}`"
            :key="group.id"
            class="sforum-category-directory__group"
            :class="{ 'is-focused': normalizedFocusedKey === categoryDirectoryGroupKey(group) }"
            tabindex="-1"
          >
            <header class="sforum-category-directory__group-head">
              <div class="sforum-category-directory__group-main">
                <span class="sforum-category-directory__group-mark" aria-hidden="true" />
                <div>
                  <h2>{{ group.name }}</h2>
                  <p v-if="group.description">{{ group.description }}</p>
                </div>
              </div>
              <span>{{ groupLabel(group) }}</span>
            </header>

            <div v-if="group.categories.length" class="sforum-category-directory__board-grid">
              <NuxtLink
                v-for="category in group.categories"
                :key="category.id"
                :to="categoryTo(category)"
                class="sforum-category-directory__board"
                :style="{ '--board-color': categoryAccent(category) }"
                @click="closeMobileDrawers"
              >
                <span class="sforum-category-directory__board-icon" aria-hidden="true">
                  <UIcon :name="categoryIconName(category)" class="size-[21px]" />
                </span>
                <span class="sforum-category-directory__board-copy">
                  <span class="sforum-category-directory__board-title">{{ category.name }}</span>
                  <span class="sforum-category-directory__board-description">
                    {{ category.description || t('taxonomy.categories.noDescription') }}
                  </span>
                </span>
                <span class="sforum-category-directory__board-stats">
                  <span>
                    <strong>{{ formatCount(category.topicCount) }}</strong>
                    <span>{{ t('taxonomy.categories.stats.topics') }}</span>
                  </span>
                  <span>
                    <strong>{{ formatCount(category.commentCount) }}</strong>
                    <span>{{ t('taxonomy.categories.stats.replies') }}</span>
                  </span>
                </span>
              </NuxtLink>
            </div>

            <div v-else class="sforum-category-directory__group-empty">
              {{ t('taxonomy.categories.emptyGroup') }}
            </div>
          </section>
        </div>

        <div v-else-if="showFilterEmpty" class="sforum-category-directory__state">
          <SFEmptyState
            :title="filterEmptyTitle"
            :description="filterEmptyDescription"
            :action-label="t('taxonomy.categories.showAllCategories')"
            @action="showAllCategories"
          />
        </div>

        <div v-else-if="showGlobalEmpty" class="sforum-category-directory__state">
          <SFEmptyState
            :title="t('taxonomy.categories.emptyTitle')"
            :description="t('taxonomy.categories.emptyDescription')"
          />
        </div>

        <SFRegionOutlet page="forum.category.index" region="list_after" />

        <SFRegionOutlet page="forum.category.index" region="content_after" />

        <SFContentColumnFooter />
      </section>

      <aside class="sforum-home__right sforum-category-directory__right" :aria-label="t('taxonomy.categories.rightRailLabel')">
        <SFRegionOutlet page="forum.category.index" region="sidebar" />
        <div class="sf-home-right-rail">
          <section class="sf-home-right-rail__card sforum-category-directory__rail-section">
            <header class="sf-home-right-rail__head">
              <h3 class="sf-home-right-rail__title">{{ t('taxonomy.categories.overviewTitle') }}</h3>
              <span class="sf-home-right-rail__meta">{{ t('taxonomy.categories.publicScope') }}</span>
            </header>
            <dl class="sforum-category-directory__facts">
              <div>
                <dt>{{ t('taxonomy.categories.stats.groups') }}</dt>
                <dd>{{ formatCount(totalStats.groupCount) }}</dd>
              </div>
              <div>
                <dt>{{ t('taxonomy.categories.stats.categories') }}</dt>
                <dd>{{ formatCount(totalStats.categoryCount) }}</dd>
              </div>
              <div>
                <dt>{{ t('taxonomy.categories.stats.topics') }}</dt>
                <dd>{{ formatCount(totalStats.topicCount) }}</dd>
              </div>
              <div>
                <dt>{{ t('taxonomy.categories.stats.replies') }}</dt>
                <dd>{{ formatCount(totalStats.commentCount) }}</dd>
              </div>
            </dl>
          </section>

          <section class="sf-home-right-rail__card sforum-category-directory__rail-section">
            <header class="sf-home-right-rail__head">
              <h3 class="sf-home-right-rail__title">{{ t('taxonomy.categories.activeTitle') }}</h3>
              <span class="sf-home-right-rail__meta">{{ t('taxonomy.categories.byTopicCount') }}</span>
            </header>
            <div v-if="activeCategories.length" class="sforum-category-directory__active-list">
              <NuxtLink
                v-for="category in activeCategories"
                :key="category.id"
                :to="categoryTo(category)"
                class="sforum-category-directory__active-item"
                :style="{ '--board-color': categoryAccent(category) }"
              >
                <UIcon :name="categoryIconName(category)" class="size-4" aria-hidden="true" />
                <strong>{{ category.name }}</strong>
                <b>{{ t('taxonomy.categories.topicCount', { count: formatCount(category.topicCount) }) }}</b>
              </NuxtLink>
            </div>
            <p v-else class="sf-home-right-rail__empty">
              {{ t('taxonomy.categories.noActiveCategories') }}
            </p>
          </section>

        </div>
      </aside>
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
      <aside class="sforum-home__right sforum-category-directory__right" :aria-label="t('taxonomy.categories.rightRailLabel')">
        <div class="sf-home-right-rail">
          <section class="sf-home-right-rail__card sforum-category-directory__rail-section">
            <header class="sf-home-right-rail__head">
              <h3 class="sf-home-right-rail__title">{{ t('taxonomy.categories.overviewTitle') }}</h3>
              <span class="sf-home-right-rail__meta">{{ t('taxonomy.categories.publicScope') }}</span>
            </header>
            <dl class="sforum-category-directory__facts">
              <div>
                <dt>{{ t('taxonomy.categories.stats.groups') }}</dt>
                <dd>{{ formatCount(totalStats.groupCount) }}</dd>
              </div>
              <div>
                <dt>{{ t('taxonomy.categories.stats.categories') }}</dt>
                <dd>{{ formatCount(totalStats.categoryCount) }}</dd>
              </div>
              <div>
                <dt>{{ t('taxonomy.categories.stats.topics') }}</dt>
                <dd>{{ formatCount(totalStats.topicCount) }}</dd>
              </div>
              <div>
                <dt>{{ t('taxonomy.categories.stats.replies') }}</dt>
                <dd>{{ formatCount(totalStats.commentCount) }}</dd>
              </div>
            </dl>
          </section>
          <section class="sf-home-right-rail__card sforum-category-directory__rail-section">
            <header class="sf-home-right-rail__head">
              <h3 class="sf-home-right-rail__title">{{ t('taxonomy.categories.activeTitle') }}</h3>
              <span class="sf-home-right-rail__meta">{{ t('taxonomy.categories.byTopicCount') }}</span>
            </header>
            <div v-if="activeCategories.length" class="sforum-category-directory__active-list">
              <NuxtLink
                v-for="category in activeCategories"
                :key="category.id"
                :to="categoryTo(category)"
                class="sforum-category-directory__active-item"
                :style="{ '--board-color': categoryAccent(category) }"
                @click="closeMobileDrawers"
              >
                <UIcon :name="categoryIconName(category)" class="size-4" aria-hidden="true" />
                <strong>{{ category.name }}</strong>
                <b>{{ t('taxonomy.categories.topicCount', { count: formatCount(category.topicCount) }) }}</b>
              </NuxtLink>
            </div>
          </section>
        </div>
      </aside>
    </aside>
  </main>
</template>
