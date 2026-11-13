<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useNotifications, type NotificationDetail, type NotificationItem } from '~/composables/notifications/useNotifications'
import { userDisplayName } from '~/utils/forum/userDisplay'
import {
  limitNotificationPreviewItems,
  NOTIFICATION_PREVIEW_LIMIT,
  notificationPreviewFilters,
  notificationPreviewTabs,
  type NotificationPreviewTab
} from '~/utils/notifications/notificationPreview'
import {
  notificationPresentation,
  type NotificationPresentation
} from '~/utils/notifications/notificationsPresentation'

const { t } = useI18n()
const localePath = useLocalePath()
const router = useRouter()
const route = useRoute()
const toast = useToast()
const notifications = useNotifications()
const { user: authUser } = useAuthSession()
const { format: formatSiteDateTime } = useSiteDateTime()

const root = useTemplateRef<HTMLElement>('root')
const trigger = useTemplateRef<HTMLElement>('trigger')
const panel = useTemplateRef<HTMLElement>('panel')
const open = ref(false)
const activeTab = ref<NotificationPreviewTab>('all')
const items = ref<NotificationItem[]>([])
const details = ref<Record<number, NotificationDetail>>({})
const loading = ref(false)
const markingAll = ref(false)
const openingId = ref(0)
const errorMessage = ref('')
const panelTop = ref('0px')
const panelRight = ref('12px')
let requestRevision = 0

const presentedItems = computed(() => items.value.map(notificationPresentation))
const inboxFilter = useState<string>('notification-inbox-filter', () => 'all')

function tabLabel(tab: NotificationPreviewTab) {
  return t(`notifications.preview.tabs.${tab}`)
}

function itemExcerpt(item: NotificationPresentation) {
  return details.value[item.id]?.preview?.content.excerpt
    || t(item.bodyKey, item.bodyParams)
}

function notificationTime(item: NotificationPresentation) {
  return formatSiteDateTime(item.createdAt, { now: new Date() })
}

function setItemRead(id: number, readAt: string) {
  const item = items.value.find(entry => entry.id === id)
  if (item) item.readAt = readAt
  const detail = details.value[id]
  if (detail) detail.readAt = readAt
}

async function loadPreview(tab = activeTab.value) {
  const revision = ++requestRevision
  loading.value = true
  errorMessage.value = ''
  try {
    const page = await notifications.list(
      0,
      NOTIFICATION_PREVIEW_LIMIT,
      notificationPreviewFilters(tab)
    )
    const nextItems = limitNotificationPreviewItems(page.items)
    const detailResults = await Promise.allSettled(nextItems.map(item => notifications.get(item.id)))
    if (revision !== requestRevision || tab !== activeTab.value) return

    const nextDetails: Record<number, NotificationDetail> = {}
    detailResults.forEach((result, index) => {
      const item = nextItems[index]
      if (item && result.status === 'fulfilled') nextDetails[item.id] = result.value
    })
    items.value = nextItems
    details.value = nextDetails
  } catch {
    if (revision === requestRevision) {
      items.value = []
      details.value = {}
      errorMessage.value = t('notifications.preview.loadFailed')
    }
  } finally {
    if (revision === requestRevision) loading.value = false
  }
}

async function togglePreview() {
  if (!open.value) updatePanelPosition()
  open.value = !open.value
  if (open.value) {
    await Promise.allSettled([notifications.refreshUnreadCount(), loadPreview()])
  }
}

function closePreview() {
  open.value = false
}

async function selectTab(tab: NotificationPreviewTab) {
  if (tab === activeTab.value && items.value.length) return
  activeTab.value = tab
  items.value = []
  details.value = {}
  await loadPreview(tab)
}

async function openNotification(item: NotificationPresentation) {
  if (openingId.value) return
  openingId.value = item.id
  errorMessage.value = ''
  const destination = item.target.unavailable
    ? `/notifications/${item.id}`
    : item.target.path
  try {
    await router.push(localePath(destination))
    closePreview()
    if (!item.read && !item.target.unavailable) {
      await notifications.markRead(item.id)
      setItemRead(item.id, new Date().toISOString())
    }
  } catch {
    errorMessage.value = t('notifications.preview.openFailed')
  } finally {
    openingId.value = 0
  }
}

async function markAllRead() {
  if (markingAll.value || notifications.unreadCount.value === 0) return
  markingAll.value = true
  errorMessage.value = ''
  try {
    await notifications.markAllRead()
    const readAt = new Date().toISOString()
    items.value.forEach(item => setItemRead(item.id, readAt))
    toast.add({
      color: 'primary',
      icon: 'i-lucide-check-check',
      title: t('notifications.allRead'),
      duration: 5000
    })
  } catch {
    errorMessage.value = t('notifications.markAllReadFailed')
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: errorMessage.value,
      duration: 0
    })
  } finally {
    markingAll.value = false
  }
}

function openInbox() {
  inboxFilter.value = activeTab.value
  closePreview()
}

function updatePanelPosition() {
  if (!trigger.value || typeof window === 'undefined') return
  const rect = trigger.value.getBoundingClientRect()
  panelTop.value = `${Math.round(rect.bottom + 10)}px`
  panelRight.value = `${Math.max(12, Math.round(window.innerWidth - rect.right - 8))}px`
}

function handleDocumentPointerDown(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as Node
  if (!root.value?.contains(target) && !panel.value?.contains(target)) closePreview()
}

function handleDocumentKeydown(event: KeyboardEvent) {
  if (open.value && event.key === 'Escape') closePreview()
}

watch(() => route.fullPath, closePreview)

let stopRealtime = () => {}
watch(() => authUser.value?.id, (actorUserId) => {
  stopRealtime()
  stopRealtime = () => {}
  if (!actorUserId) return
  stopRealtime = notifications.startRealtime(actorUserId, async () => {
    await notifications.refreshUnreadCount()
    if (open.value) await loadPreview()
  })
}, { immediate: true })

onMounted(() => {
  document.addEventListener('pointerdown', handleDocumentPointerDown)
  document.addEventListener('keydown', handleDocumentKeydown)
  window.addEventListener('resize', updatePanelPosition)
  void notifications.refreshUnreadCount().catch(() => {})
})
onBeforeUnmount(() => {
  requestRevision++
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
  document.removeEventListener('keydown', handleDocumentKeydown)
  window.removeEventListener('resize', updatePanelPosition)
  stopRealtime()
})
</script>

<template>
  <div ref="root" class="sf-notification-preview">
    <button
      ref="trigger"
      type="button"
      class="sf-notification-preview__trigger"
      :aria-label="t('nav.notifications')"
      :title="t('nav.notifications')"
      aria-haspopup="dialog"
      :aria-expanded="open"
      aria-controls="navbar-notification-preview"
      @click="togglePreview"
    >
      <UIcon name="i-lucide-bell" class="size-5" aria-hidden="true" />
      <!--
        未读数由页面 useAsyncData 在 SSR 之后写入共享 state：服务端 HTML 恒为空，
        客户端首帧却已有值 → hydration node mismatch。徽章是装饰性内容，
        交给 ClientOnly 在挂载后再画，两边首帧一致。
      -->
      <ClientOnly>
        <span
          v-if="notifications.unreadCount.value"
          class="sf-notification-preview__badge"
        >{{ notifications.unreadCount.value > 99 ? '99+' : notifications.unreadCount.value }}</span>
      </ClientOnly>
    </button>

    <Teleport to="body">
      <button
        v-if="open"
        type="button"
        class="sf-notification-preview__backdrop"
        :aria-label="t('common.close')"
        @click="closePreview"
      />

      <section
        v-if="open"
        id="navbar-notification-preview"
        ref="panel"
        class="sf-notification-preview__panel"
        role="dialog"
        aria-labelledby="navbar-notification-preview-title"
        :style="{
          '--sf-notification-preview-top': panelTop,
          '--sf-notification-preview-right': panelRight
        }"
      >
      <header class="sf-notification-preview__header">
        <div>
          <h2 id="navbar-notification-preview-title">{{ t('notifications.title') }}</h2>
          <span>{{ t('notifications.unreadSummary', { count: notifications.unreadCount.value }) }}</span>
        </div>
        <div class="sf-notification-preview__header-actions">
          <NuxtLink
            :to="localePath('/settings/notifications')"
            class="sf-notification-preview__icon-button"
            :aria-label="t('notificationSettings.title')"
            :title="t('notificationSettings.title')"
            @click="closePreview"
          >
            <UIcon name="i-lucide-settings" class="size-4" aria-hidden="true" />
          </NuxtLink>
          <button
            type="button"
            class="sf-notification-preview__icon-button"
            :aria-label="t('common.close')"
            :title="t('common.close')"
            @click="closePreview"
          >
            <UIcon name="i-lucide-x" class="size-4" aria-hidden="true" />
          </button>
        </div>
      </header>

      <div class="sf-notification-preview__tabs" role="tablist" :aria-label="t('notifications.filter.aria')">
        <button
          v-for="tab in notificationPreviewTabs"
          :key="tab"
          type="button"
          role="tab"
          :aria-selected="activeTab === tab"
          :class="{ 'is-active': activeTab === tab }"
          @click="selectTab(tab)"
        >
          {{ tabLabel(tab) }}
        </button>
      </div>

      <div class="sf-notification-preview__content" aria-live="polite">
        <div v-if="loading" class="sf-notification-preview__loading" aria-busy="true">
          <SFSkeleton v-for="index in 3" :key="index" avatar :lines="2" />
        </div>

        <div v-else-if="errorMessage" class="sf-notification-preview__state" role="alert">
          <UIcon name="i-lucide-triangle-alert" class="size-5" aria-hidden="true" />
          <span>{{ errorMessage }}</span>
          <button type="button" @click="loadPreview()">{{ t('home.feed.retryLoadMore') }}</button>
        </div>

        <div v-else-if="!presentedItems.length" class="sf-notification-preview__state">
          <UIcon name="i-lucide-bell-off" class="size-5" aria-hidden="true" />
          <span>{{ t('notifications.preview.empty') }}</span>
        </div>

        <div v-else class="sf-notification-preview__list">
          <button
            v-for="item in presentedItems"
            :key="item.id"
            type="button"
            class="sf-notification-preview__row"
            :class="{ 'is-unread': !item.read }"
            :disabled="openingId === item.id"
            @click="openNotification(item)"
          >
            <SFAvatar
              v-if="item.actor"
              :name="userDisplayName(item.actor)"
              :avatar="item.actor.avatar"
              :alt="''"
              size="list"
            />
            <span v-else class="sf-notification-preview__source-icon" aria-hidden="true">
              <UIcon :name="item.icon" class="size-[18px]" />
            </span>
            <span class="sf-notification-preview__copy">
              <strong>{{ t(item.titleKey) }}</strong>
              <span>{{ itemExcerpt(item) }}</span>
              <small>{{ notificationTime(item) }}<template v-if="item.targetLabel"> · {{ item.targetLabel }}</template></small>
            </span>
            <span v-if="!item.read" class="sf-notification-preview__unread-dot" :aria-label="t('notifications.unread')" />
          </button>
        </div>
      </div>

      <footer class="sf-notification-preview__footer">
        <p>{{ t('notifications.preview.limitHint', { count: NOTIFICATION_PREVIEW_LIMIT }) }}</p>
        <div>
          <button
            type="button"
            :disabled="markingAll || notifications.unreadCount.value === 0"
            @click="markAllRead"
          >
            <UIcon name="i-lucide-check-check" class="size-4" aria-hidden="true" />
            {{ t('notifications.markAllRead') }}
          </button>
          <NuxtLink :to="localePath('/notifications')" @click="openInbox">
            {{ t('notifications.preview.viewAll') }}
            <UIcon name="i-lucide-arrow-right" class="size-4" aria-hidden="true" />
          </NuxtLink>
        </div>
      </footer>
      </section>
    </Teleport>
  </div>
</template>

<style scoped src="./SFNotificationPreview.css"></style>
