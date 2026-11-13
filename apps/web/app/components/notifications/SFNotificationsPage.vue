<script setup lang="ts">
import { useNotifications } from '~/composables/notifications/useNotifications'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useForumApi } from '~/composables/forum/useForumApi'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFNotificationTypeNav from '~/components/notifications/SFNotificationTypeNav.vue'
import SFPublicPageHeader from '~/components/public/SFPublicPageHeader.vue'
import SFPublicMobileUserMenu from '~/components/navigation/SFPublicMobileUserMenu.vue'
import SFPublicMobileRightDrawerHeader from '~/components/navigation/SFPublicMobileRightDrawerHeader.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
import { apiErrorMessage } from '~/composables/useApiClient'
import { isUnauthenticatedAuthError } from '~/composables/identity/useAuthSession'
import {
  filterNotifications,
  groupNotificationsByDate,
  notificationFilterCounts,
  notificationFilters,
  notificationPresentation,
  unreadLoadedCount,
  type NotificationFilter,
  type NotificationPresentation
} from '~/utils/notifications/notificationsPresentation'

/**
 * 宿主 body 岛：forum.notifications。主题 L1 挂载；路由页仅 outlet + fail-closed 回退。
 */

const PAGE_LIMIT = 20

const { t, locale } = useI18n()
const localePath = useLocalePath()
const router = useRouter()
const toast = useToast()
const notifications = useNotifications()
const { user: authUser } = useAuthSession()
const forumApi = useForumApi()
const { can } = usePermissions()
const { format: formatSiteDateTime, timezone } = useSiteDateTime()

const renderedAt = useState<number>('notification-inbox-rendered-at', () => Date.now())
const activeFilter = useState<NotificationFilter>('notification-inbox-filter', () => 'all')
const actionError = ref('')
const loadingMoreError = ref('')
const loadingMore = ref(false)
const markingAll = ref(false)
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()

function serverFilters(filter: NotificationFilter) {
  if (filter === 'all') return {}
  if (filter === 'unread') return { unread: true }
  return { type: filter }
}

const [
  notificationAsync,
  unreadAsync,
  categoryAsync
] = await Promise.all([
  useAsyncData(
    'notification-inbox',
    () => notifications.list(0, PAGE_LIMIT, serverFilters(activeFilter.value)),
    { default: () => ({ items: [], hasMore: false }) }
  ),
  useAsyncData(
    'notification-inbox-unread-count',
    () => notifications.refreshUnreadCount(),
    { default: () => notifications.unreadCount.value }
  ),
  useAsyncData(
    'notification-inbox-categories',
    () => forumApi.listCategoryGroups(),
    { default: () => [] }
  )
])

const items = ref([...notificationAsync.data.value.items])
const hasMore = ref(notificationAsync.data.value.hasMore)

watch(() => notificationAsync.data.value, (page) => {
  items.value = [...page.items]
  hasMore.value = page.hasMore
})

const categories = computed(() => categoryAsync.data.value.flatMap(group => group.categories || []))
const categoriesPending = computed(() => categoryAsync.pending.value)
const categoryTopicTotal = computed(() => categories.value.reduce((sum, category) => sum + category.topicCount, 0))
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))
const unreadTotal = computed(() => notifications.unreadCount.value)
const presentedItems = computed(() => items.value.map(notificationPresentation))
const filteredItems = computed(() => filterNotifications(presentedItems.value, activeFilter.value))
const visibleGroups = computed(() => groupNotificationsByDate(
  filteredItems.value,
  new Date(renderedAt.value),
  timezone.value,
  String(locale.value || 'zh-CN')
))
const loadedUnread = computed(() => unreadLoadedCount(presentedItems.value))
const loadedTypeCounts = computed(() => notificationFilterCounts(presentedItems.value))
const isInitialLoading = computed(() => notificationAsync.pending.value || unreadAsync.pending.value)
const initialError = computed(() => notificationAsync.error.value || unreadAsync.error.value)
const hasActionableUnread = computed(() => unreadTotal.value > 0 || loadedUnread.value > 0)
const filterScopeLabel = computed(() => t('notifications.filter.loadedScope', { count: presentedItems.value.length }))
const unreadSummaryLabel = computed(() => t('notifications.unreadSummary', { count: unreadTotal.value }))

function filterLabel(filter: NotificationFilter) {
  return t(`notifications.filter.${filter}`)
}

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

function selectFilter(filter: NotificationFilter) {
	if (activeFilter.value === filter) {
		closeMobileDrawers()
		return
	}
  activeFilter.value = filter
  closeMobileDrawers()
  void refreshInbox()
}

async function refreshInbox() {
  actionError.value = ''
  try {
    const page = await notifications.list(0, PAGE_LIMIT, serverFilters(activeFilter.value))
    items.value = page.items
    hasMore.value = page.hasMore
  } catch (error) {
    actionError.value = apiErrorMessage(error) || t('notifications.loadFailed')
  }
}

function notificationTime(item: NotificationPresentation) {
  return formatSiteDateTime(item.createdAt, { now: new Date(renderedAt.value) })
}

function translatedBody(item: NotificationPresentation) {
  return t(item.bodyKey, item.bodyParams)
}

function groupTitle(key: 'today' | 'earlier') {
  return t(`notifications.groups.${key}`)
}

function groupCount(count: number) {
  return t('notifications.groupCount', { count })
}

function setNotificationRead(id: number, readAt: string | undefined) {
  const item = items.value.find(entry => entry.id === id)
  if (item) {
    item.readAt = readAt
  }
}

async function openNotificationDetail(item: NotificationPresentation) {
  closeMobileDrawers()
  await router.push(localePath(`/notifications/${item.id}`))
}

async function markAllRead() {
  if (!hasActionableUnread.value || markingAll.value) {
    return
  }

  const previousItems = items.value.map(item => ({ id: item.id, readAt: item.readAt }))
  const previousUnreadCount = notifications.unreadCount.value
  const readAt = new Date().toISOString()
  markingAll.value = true
  actionError.value = ''
  items.value.forEach(item => {
    item.readAt ||= readAt
  })

  try {
    await notifications.markAllRead()
    toast.add({
      color: 'primary',
      icon: 'i-lucide-check-check',
      title: t('notifications.allRead'),
      duration: 5000
    })
  } catch (error) {
    for (const previous of previousItems) {
      setNotificationRead(previous.id, previous.readAt)
    }
    notifications.unreadCount.value = previousUnreadCount
    actionError.value = apiErrorMessage(error) || t('notifications.markAllReadFailed')
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: actionError.value,
      duration: 0
    })
  } finally {
    markingAll.value = false
  }
}

async function loadMore() {
  if (!hasMore.value || loadingMore.value || items.value.length === 0) {
    return
  }
  loadingMore.value = true
  loadingMoreError.value = ''
  try {
    const beforeId = items.value[items.value.length - 1]?.id || 0
    const page = await notifications.list(beforeId, PAGE_LIMIT, serverFilters(activeFilter.value))
    const seen = new Set(items.value.map(item => item.id))
    items.value = [...items.value, ...page.items.filter(item => !seen.has(item.id))]
    hasMore.value = page.hasMore
  } catch (error) {
    loadingMoreError.value = apiErrorMessage(error) || t('notifications.loadMoreFailed')
  } finally {
    loadingMore.value = false
  }
}

let stopRealtime = () => {}
watch(() => authUser.value?.id, (actorUserId) => {
  stopRealtime()
  stopRealtime = () => {}
  if (!actorUserId) return
  stopRealtime = notifications.startRealtime(actorUserId, async () => {
    await Promise.allSettled([notifications.refreshUnreadCount(), refreshInbox()])
  })
}, { immediate: true })
onBeforeUnmount(() => stopRealtime())
</script>

<template>
  <main
    class="sforum-notifications"
    data-sforum-island-body="forum.component.notifications"
    data-layout="fullwidth-3col"
  >
    <div class="sforum-notifications__layout">
      <SFResponsivePublicSidebar
        owner-id="forum.notifications"
        :title="t('home.sidebar.drawerTitle')"
        class="sforum-notifications__sidebar sforum-home__sidebar"
      >
        <SFHomeNavigation
          desktop-only
          navigation-mode="route"
          :categories="categories"
          :total-topics="categoryTopicTotal"
          :pending="categoriesPending"
          :can-create-topic="canCreateTopic"
          :show-categories="false"
        >
          <template #after-navigation>
            <SFNotificationTypeNav
              :active-filter="activeFilter"
              :counts="loadedTypeCounts"
              :loaded-count="presentedItems.length"
              @select="selectFilter"
            />
          </template>
        </SFHomeNavigation>
      </SFResponsivePublicSidebar>

      <section class="sforum-notifications__main" aria-labelledby="notification-page-title">
        <SFRegionOutlet page="forum.notifications" region="content_before" />

        <SFPublicPageHeader
          class="sforum-notifications__head"
          title-id="notification-page-title"
          :title="t('notifications.title')"
          :subtitle="unreadSummaryLabel"
        >
          <template #aside>
            <div class="sforum-notifications__head-actions">
              <button
                type="button"
                class="sforum-notifications__text-button"
                :disabled="!hasActionableUnread || markingAll"
                @click="markAllRead"
              >
                <UIcon name="i-lucide-check-check" class="size-4" aria-hidden="true" />
                <span>{{ markingAll ? t('notifications.markingAllRead') : t('notifications.markAllRead') }}</span>
              </button>
            </div>
          </template>
        </SFPublicPageHeader>

        <div class="sforum-notifications__filter-strip" :aria-label="t('notifications.filter.aria')">
          <button
            v-for="filter in notificationFilters"
            :key="filter"
            type="button"
            class="sforum-notifications__filter-button"
            :class="{ 'is-active': activeFilter === filter }"
            :aria-pressed="activeFilter === filter"
            @click="selectFilter(filter)"
          >
            {{ filterLabel(filter) }}
          </button>
        </div>
        <p class="sforum-notifications__filter-note">{{ filterScopeLabel }}</p>

        <SFAlert
          v-if="actionError"
          class="sforum-notifications__alert"
          variant="danger"
          :title="actionError"
          closable
          @close="actionError = ''"
        />

        <SFRegionOutlet page="forum.notifications" region="list_before" />

        <SFAlert
          v-if="initialError"
          class="sforum-notifications__alert"
          variant="danger"
          :title="isUnauthenticatedAuthError(initialError) ? t('notifications.authRequired') : t('notifications.loadFailed')"
        />

        <div v-else-if="isInitialLoading" class="sforum-notifications__loading" aria-busy="true">
          <SFSkeleton v-for="item in 5" :key="item" avatar :lines="2" />
        </div>

        <!--
          空状态只有一条分支：列表接口本身按服务端筛选返回，`items` 为空就等于
          「这个筛选下没有通知」。再单列一个 `!items.length` 分支会把筛选空状态永久挡住。
        -->
        <SFEmptyState
          v-else-if="!filteredItems.length"
          class="sforum-notifications__empty"
          :icon="activeFilter === 'all' ? 'i-lucide-bell-off' : 'i-lucide-filter-x'"
          :title="activeFilter === 'all' ? t('notifications.empty') : t('notifications.filter.empty')"
          :description="activeFilter === 'all' ? t('notifications.emptyDescription') : t('notifications.filter.emptyDescription')"
        />

        <div v-else class="sforum-notifications__stream">
          <section
            v-for="group in visibleGroups"
            :key="group.key"
            class="sforum-notifications__group"
            :aria-labelledby="`notification-group-${group.key}`"
          >
            <div class="sforum-notifications__group-head">
              <span :id="`notification-group-${group.key}`">{{ groupTitle(group.key) }}</span>
              <span>{{ groupCount(group.items.length) }}</span>
            </div>

            <div class="sforum-notifications__list">
              <button
                v-for="item in group.items"
                :key="item.id"
                type="button"
                class="sforum-notifications__row"
                :class="{ 'is-unread': !item.read }"
                @click="openNotificationDetail(item)"
              >
                <SFAvatar
                  v-if="item.actor"
                  :name="userDisplayName(item.actor)"
                  :avatar="item.actor.avatar"
                  :alt="userDisplayName(item.actor)"
                  size="list"
                />
                <span
                  v-else
                  class="sforum-notifications__source-icon"
                  :data-type="item.type"
                  aria-hidden="true"
                >
                  <UIcon :name="item.icon" class="size-5" />
                </span>
                <span class="sforum-notifications__row-body">
                  <span class="sforum-notifications__row-lead">
                    {{ t(item.titleKey) }}
                    <span v-if="!item.read" class="sforum-notifications__unread-text">{{ t('notifications.unread') }}</span>
                  </span>
                  <span class="sforum-notifications__row-excerpt">{{ translatedBody(item) }}</span>
                  <span class="sforum-notifications__row-meta">
                    <span class="sforum-notifications__type-label" :data-type="item.type">
                      <UIcon :name="item.icon" class="size-3.5" aria-hidden="true" />
                      {{ t(item.typeLabelKey) }}
                    </span>
                    <span v-if="item.targetLabel">{{ item.targetLabel }}</span>
                    <span v-else>{{ t('notifications.targetUnavailable') }}</span>
                  </span>
                </span>
                <time class="sforum-notifications__row-time" :datetime="item.createdAt">{{ notificationTime(item) }}</time>
              </button>
            </div>
          </section>

          <SFRegionOutlet page="forum.notifications" region="list_after" />

          <div v-if="items.length && !initialError" class="sforum-notifications__more">
            <button
              v-if="hasMore"
              type="button"
              class="sforum-notifications__secondary-button"
              :disabled="loadingMore"
              @click="loadMore"
            >
              <UIcon name="i-lucide-chevron-down" class="size-4" aria-hidden="true" />
              {{ loadingMore ? t('notifications.loadingMore') : t('notifications.loadMore') }}
            </button>
            <span v-else>{{ t('notifications.end') }}</span>
            <div v-if="loadingMoreError" class="sforum-notifications__inline-error" role="alert">
              <span>{{ loadingMoreError }}</span>
              <button type="button" @click="loadMore">{{ t('home.feed.retryLoadMore') }}</button>
            </div>
          </div>
        </div>

        <SFRegionOutlet page="forum.notifications" region="content_after" />

      </section>

      <aside class="sforum-notifications__right" :aria-label="t('notifications.detail.aria')">
        <section class="sforum-notifications__rail-section">
          <div class="sforum-notifications__rail-head">
            <h2>{{ t('notifications.inbox') }}</h2>
            <span>{{ t('notifications.unreadAuthority') }}</span>
          </div>
          <div class="sforum-notifications__unread-summary">
            <strong>{{ unreadTotal }}</strong>
            <span>{{ t('notifications.unreadCountLabel') }}</span>
          </div>
          <p class="sforum-notifications__rail-help">{{ t('notifications.unreadSource') }}</p>
        </section>

        <section class="sforum-notifications__rail-section">
          <div class="sforum-notifications__rail-head">
            <h2>{{ t('notifications.loadedOverview') }}</h2>
            <span>{{ t('notifications.loadedOnly') }}</span>
          </div>
          <dl class="sforum-notifications__loaded-stats">
            <div>
              <dt>{{ t('notifications.loadedTotal') }}</dt>
              <dd>{{ presentedItems.length }}</dd>
            </div>
            <div>
              <dt>{{ t('notifications.loadedUnread') }}</dt>
              <dd>{{ loadedUnread }}</dd>
            </div>
          </dl>
        </section>
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
        :title="t('notifications.detail.title')"
        :close-label="t('common.close')"
        @close="closeMobileDrawers"
      />
      <SFPublicMobileUserMenu />
      <aside class="sforum-notifications__right sforum-notifications__right--drawer" :aria-label="t('notifications.detail.aria')">
        <section class="sforum-notifications__rail-section">
          <div class="sforum-notifications__rail-head">
            <h2>{{ t('notifications.inbox') }}</h2>
            <span>{{ t('notifications.unreadAuthority') }}</span>
          </div>
          <div class="sforum-notifications__unread-summary">
            <strong>{{ unreadTotal }}</strong>
            <span>{{ t('notifications.unreadCountLabel') }}</span>
          </div>
          <p class="sforum-notifications__rail-help">{{ t('notifications.unreadSource') }}</p>
        </section>
      </aside>
    </aside>
  </main>
</template>

<style scoped src="./SFNotificationsPage.css"></style>
