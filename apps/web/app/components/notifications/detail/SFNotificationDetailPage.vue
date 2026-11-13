<script setup lang="ts">
import { useNotifications, type NotificationPreviewContent } from '~/composables/notifications/useNotifications'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useForumApi } from '~/composables/forum/useForumApi'
import { apiErrorMessage, apiErrorStatusCode } from '~/composables/useApiClient'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFNotificationTypeNav from '~/components/notifications/SFNotificationTypeNav.vue'
import SFPublicPageHeader from '~/components/public/SFPublicPageHeader.vue'
import SFPublicMobileUserMenu from '~/components/navigation/SFPublicMobileUserMenu.vue'
import SFPublicMobileRightDrawerHeader from '~/components/navigation/SFPublicMobileRightDrawerHeader.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import {
  notificationFilterCounts,
  notificationFilters,
  notificationHasTarget,
  notificationPresentation,
  notificationTargetTitle,
  type NotificationFilter
} from '~/utils/notifications/notificationsPresentation'

/** forum.notification.show：独立路由详情岛，只呈现 Host 已重新授权的当前摘要。 */

const { t } = useI18n()
const localePath = useLocalePath()
const route = useRoute()
const router = useRouter()
const toast = useToast()
const notifications = useNotifications()
const forumApi = useForumApi()
const { can } = usePermissions()
const { format: formatSiteDateTime } = useSiteDateTime()

const notificationId = Number.parseInt(String(route.params.notificationId || ''), 10)
if (!Number.isSafeInteger(notificationId) || notificationId <= 0) {
  throw createError({ statusCode: 404, statusMessage: 'Notification not found' })
}

const [detailAsync, unreadAsync, categoryAsync, sidebarAsync] = await Promise.all([
  useAsyncData(`notification-detail:${notificationId}`, () => notifications.get(notificationId)),
  useAsyncData('notification-detail-unread-count', () => notifications.refreshUnreadCount(), {
    default: () => notifications.unreadCount.value
  }),
  useAsyncData('notification-detail-categories', () => forumApi.listCategoryGroups(), {
    default: () => []
  }),
  useAsyncData('notification-detail-sidebar-items', () => notifications.list(0, 20), {
    default: () => ({ items: [], hasMore: false })
  })
])

if (detailAsync.error.value && apiErrorStatusCode(detailAsync.error.value) === 404) {
  throw createError({ statusCode: 404, statusMessage: 'Notification not found' })
}

const detail = detailAsync.data
const presented = computed(() => detail.value ? notificationPresentation(detail.value) : null)
const preview = computed(() => detail.value?.preview)
const categories = computed(() => categoryAsync.data.value.flatMap(group => group.categories || []))
const categoryTopicTotal = computed(() => categories.value.reduce((sum, category) => sum + category.topicCount, 0))
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))
const unreadTotal = computed(() => notifications.unreadCount.value)
const sidebarItems = computed(() => sidebarAsync.data.value.items.map(notificationPresentation))
const sidebarCounts = computed(() => notificationFilterCounts(sidebarItems.value))
const activeDetailFilter = computed<NotificationFilter>(() => {
  const type = presented.value?.type
  return type && notificationFilters.includes(type) ? type : 'all'
})
const inboxFilter = useState<NotificationFilter>('notification-inbox-filter', () => 'all')
const markingRead = ref(false)
const actionError = ref('')
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()

const contentHeading = computed(() => {
  if (detail.value?.type === 'reply') return t('notifications.detailPage.replyContent')
  if (detail.value?.type === 'mention') return t('notifications.detailPage.mentionContent')
  return t('notifications.detailPage.targetContent')
})

const actionLabel = computed(() => {
  if (detail.value?.type === 'reply') return t('notifications.actions.viewReply')
  if (detail.value?.type === 'mention') return t('notifications.actions.viewMention')
  return t('notifications.actions.viewTarget')
})

/**
 * 「有可靠目标」的唯一口径：**不是**「有没有正文预览」。
 *
 * 徽章授予没有 topic/comment 预览（`preview` 为空），但目标页是真实可打开的
 * （`/u/{username}?tab=badges`）。过去用 `preview` 判定正文分支，于是徽章通知
 * 被误报成「目标不可用」，而右栏又按 `targetAvailable` 说「可查看」—— 同一页两个结论。
 * 现在正文与右栏共用这一条：`unavailable` 为假且 path 非空才算有目标。
 */
const hasTarget = computed(() => notificationHasTarget(presented.value?.target))

/** 目标卡的标题：徽章名优先（`notificationTargetTitle`），再退到列表用的摘要标签。 */
const targetTitle = computed(() => {
  if (!detail.value) return ''
  return notificationTargetTitle(detail.value) || presented.value?.targetLabel || ''
})

/** 目标卡的按钮文案：徽章授予说「查看我的徽章」，其余沿用内容类动作文案。 */
const targetActionLabel = computed(() => (detail.value?.type === 'badge_granted'
  ? t('notifications.actions.viewBadges')
  : actionLabel.value))

function authorName(content?: NotificationPreviewContent) {
  return userDisplayName(content?.author, t('notifications.detailPage.unknownAuthor'))
}

function contextHeading(content?: NotificationPreviewContent) {
  return content?.type === 'comment'
    ? t('notifications.detailPage.repliedComment')
    : t('notifications.detailPage.originalTopic')
}

function notificationTime(value: string) {
  return formatSiteDateTime(value, { now: new Date() })
}

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

async function openTarget() {
  if (!presented.value || presented.value.target.unavailable) return
  closeMobileDrawers()
  await router.push(localePath(presented.value.target.path))
}

async function selectNotificationFilter(filter: NotificationFilter) {
  inboxFilter.value = filter
  closeMobileDrawers()
  await router.push(localePath('/notifications'))
}

onMounted(async () => {
  if (!detail.value || detail.value.readAt || markingRead.value) return
  markingRead.value = true
  try {
    await notifications.markRead(detail.value.id)
    detail.value.readAt = new Date().toISOString()
    const sidebarItem = sidebarAsync.data.value.items.find(item => item.id === detail.value?.id)
    if (sidebarItem) sidebarItem.readAt = detail.value.readAt
  } catch (error) {
    actionError.value = apiErrorMessage(error) || t('notifications.markReadFailed')
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: actionError.value,
      duration: 0
    })
  } finally {
    markingRead.value = false
  }
})

// 详情页是会话私有内容：标题/robots 由公共 SEO 解析器统一拥有，不再用裸 useHead 抢标题。
useSForumSeo({
  title: () => preview.value?.topicTitle || t('notifications.detailPage.title'),
  type: 'website',
  noindex: true
})
</script>

<template>
  <main
    class="sforum-notifications sforum-notification-detail"
    data-sforum-island-body="forum.component.notification_detail"
    data-layout="fullwidth-3col"
  >
    <div class="sforum-notifications__layout">
      <SFResponsivePublicSidebar
        owner-id="forum.notification.detail"
        :title="t('home.sidebar.drawerTitle')"
        class="sforum-notifications__sidebar sforum-home__sidebar"
      >
        <SFHomeNavigation
          desktop-only
          navigation-mode="route"
          :categories="categories"
          :total-topics="categoryTopicTotal"
          :pending="categoryAsync.pending.value"
          :can-create-topic="canCreateTopic"
          :show-categories="false"
        >
          <template #after-navigation>
            <SFNotificationTypeNav
              :active-filter="activeDetailFilter"
              :counts="sidebarCounts"
              :loaded-count="sidebarItems.length"
              @select="selectNotificationFilter"
            />
          </template>
        </SFHomeNavigation>
      </SFResponsivePublicSidebar>

      <section class="sforum-notifications__main" aria-labelledby="notification-detail-title">
        <SFPublicPageHeader
          class="sforum-notifications__head sforum-notification-detail__head"
          title-id="notification-detail-title"
          :title="t('notifications.detailPage.title')"
          :subtitle="presented ? t(presented.titleKey) : ''"
        >
          <template #eyebrow>
            <NuxtLink class="sforum-notification-detail__back" :to="localePath('/notifications')">
              <UIcon name="i-lucide-arrow-left" class="size-4" aria-hidden="true" />
              {{ t('notifications.detailPage.back') }}
            </NuxtLink>
          </template>
        </SFPublicPageHeader>

        <SFAlert
          v-if="actionError"
          class="sforum-notifications__alert"
          variant="danger"
          :title="actionError"
          closable
          @close="actionError = ''"
        />

        <div v-if="detailAsync.pending.value || unreadAsync.pending.value" class="sforum-notifications__loading" aria-busy="true">
          <SFSkeleton avatar :lines="3" />
          <SFSkeleton :lines="4" />
        </div>

        <SFAlert
          v-else-if="detailAsync.error.value"
          class="sforum-notifications__alert"
          variant="danger"
          :title="apiErrorMessage(detailAsync.error.value) || t('notifications.loadFailed')"
        />

        <article v-else-if="detail && presented" class="sforum-notification-detail__reader">
          <div class="sforum-notification-detail__event">
            <SFAvatar
              :name="authorName(preview?.content)"
              :avatar="preview?.content.author?.avatar"
              size="sm"
            />
            <div>
              <strong>{{ t(presented.titleKey) }}</strong>
              <time :datetime="detail.createdAt">{{ notificationTime(detail.createdAt) }}</time>
            </div>
            <span class="sforum-notifications__type-label" :data-type="detail.type">
              <UIcon :name="presented.icon" class="size-3.5" aria-hidden="true" />
              {{ t(presented.typeLabelKey) }}
            </span>
          </div>

          <template v-if="preview">
            <h2>{{ preview.topicTitle }}</h2>

            <section class="sforum-notification-detail__content-block" :aria-labelledby="`notification-content-${detail.id}`">
              <div class="sforum-notification-detail__section-head">
                <h3 :id="`notification-content-${detail.id}`">{{ contentHeading }}</h3>
                <span>{{ authorName(preview.content) }}</span>
              </div>
              <blockquote>{{ preview.content.excerpt || t('notifications.detailPage.emptyExcerpt') }}</blockquote>
            </section>

            <section v-if="preview.context" class="sforum-notification-detail__content-block is-context">
              <div class="sforum-notification-detail__section-head">
                <h3>{{ contextHeading(preview.context) }}</h3>
                <span>{{ authorName(preview.context) }}</span>
              </div>
              <blockquote>{{ preview.context.excerpt || t('notifications.detailPage.emptyExcerpt') }}</blockquote>
            </section>

            <div class="sforum-notification-detail__actions">
              <button type="button" class="sforum-notifications__primary-button" @click="openTarget">
                {{ actionLabel }}
                <UIcon name="i-lucide-arrow-right" class="size-4" aria-hidden="true" />
              </button>
            </div>
          </template>

          <!--
            有可靠目标但没有正文预览（徽章授予就是这一类）：画目标卡而不是「目标不可用」。
            卡片给出「目标是什么 + 能去哪」，按钮复用 openTarget()，与内容类通知同一个跳转口径。
          -->
          <section
            v-else-if="hasTarget"
            class="sforum-notification-detail__content-block sforum-notification-detail__target"
            :aria-labelledby="`notification-target-${detail.id}`"
          >
            <div class="sforum-notification-detail__section-head">
              <h3 :id="`notification-target-${detail.id}`">{{ t('notifications.detailPage.target') }}</h3>
              <span>{{ t(presented.typeLabelKey) }}</span>
            </div>

            <div class="sforum-notification-detail__target-row">
              <span class="sforum-notification-detail__target-icon" aria-hidden="true">
                <UIcon :name="presented.icon" class="size-4" />
              </span>
              <div class="sforum-notification-detail__target-body">
                <strong v-if="targetTitle">{{ targetTitle }}</strong>
                <p>{{ t('notifications.detailPage.targetAvailableHelp') }}</p>
              </div>
            </div>

            <div class="sforum-notification-detail__actions sforum-notification-detail__actions--inline">
              <button type="button" class="sforum-notifications__primary-button" @click="openTarget">
                {{ targetActionLabel }}
                <UIcon name="i-lucide-arrow-right" class="size-4" aria-hidden="true" />
              </button>
            </div>
          </section>

          <SFAlert
            v-else
            variant="info"
            :title="t('notifications.targetUnavailable')"
            :description="t('notifications.targetUnavailableHelp')"
          />
        </article>

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
        </section>
        <section v-if="detail && presented" class="sforum-notifications__rail-section">
          <div class="sforum-notifications__rail-head">
            <h2>{{ t('notifications.detailPage.status') }}</h2>
            <span>{{ detail.readAt ? t('notifications.detailPage.read') : t('notifications.unread') }}</span>
          </div>
          <dl class="sforum-notification-detail__meta">
            <div>
              <dt>{{ t('notifications.detailPage.type') }}</dt>
              <dd>{{ t(presented.typeLabelKey) }}</dd>
            </div>
            <div>
              <dt>{{ t('notifications.detailPage.target') }}</dt>
              <dd>{{ hasTarget ? t('notifications.detailPage.available') : t('notifications.targetUnavailable') }}</dd>
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
        :title="t('notifications.detailPage.title')"
        :close-label="t('common.close')"
        @close="closeMobileDrawers"
      />
      <SFPublicMobileUserMenu />
      <div class="sforum-notification-detail__drawer-summary">
        <strong>{{ unreadTotal }}</strong>
        <span>{{ t('notifications.unreadCountLabel') }}</span>
        <p v-if="detail && presented">{{ t(presented.typeLabelKey) }}</p>
      </div>
    </aside>
  </main>
</template>

<style scoped src="../SFNotificationsPage.css"></style>
<style scoped src="./SFNotificationDetailPage.css"></style>
