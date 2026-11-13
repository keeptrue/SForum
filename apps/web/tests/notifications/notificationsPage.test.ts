import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { ref } from 'vue'

import { useApiClient } from '../../app/composables/useApiClient'
import { useNotifications, type NotificationItem } from '../../app/composables/notifications/useNotifications'
import {
  limitNotificationPreviewItems,
  NOTIFICATION_PREVIEW_LIMIT,
  notificationPreviewFilters,
  notificationPreviewTabs
} from '../../app/utils/notifications/notificationPreview'
import {
  filterNotifications,
  groupNotificationsByDate,
  notificationFilterCounts,
  notificationFilters,
  notificationHasTarget,
  notificationPresentation,
  notificationTarget,
  notificationTargetTitle,
  notificationTypeIcon,
  unreadLoadedCount
} from '../../app/utils/notifications/notificationsPresentation'

;(globalThis as any).useApiClient = useApiClient

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
const pageSource = () => source('../../app/components/notifications/SFNotificationsPage.vue')
const pageStyles = () => source('../../app/components/notifications/SFNotificationsPage.css')
const detailPageSource = () => source('../../app/components/notifications/detail/SFNotificationDetailPage.vue')
const detailStyles = () => source('../../app/components/notifications/detail/SFNotificationDetailPage.css')
const typeNavSource = () => source('../../app/components/notifications/SFNotificationTypeNav.vue')
const detailRouteSource = () => source('../../app/pages/notifications/[notificationId].vue')
const notificationComposableSource = () => source('../../app/composables/notifications/useNotifications.ts')
const navbarSource = () => source('../../app/components/SFNavbar.vue')
const mobileBottomNavigationSource = () => source('../../app/components/navigation/SFPublicMobileBottomNavigation.vue')
const notificationPreviewSource = () => source('../../app/components/notifications/SFNotificationPreview.vue')
const notificationPreviewStyles = () => source('../../app/components/notifications/SFNotificationPreview.css')
const zh = () => JSON.parse(source('../../i18n/locales/zh-CN.json'))
const en = () => JSON.parse(source('../../i18n/locales/en-US.json'))
const listRouteSource = () => source('../../app/pages/notifications/index.vue')
const emptyStateSource = () => source('../../app/components/SFEmptyState.vue')

function item(input: Partial<NotificationItem>): NotificationItem {
  return {
    id: input.id || 1,
    type: input.type || 'reply',
    targetType: input.targetType || 'comment',
    targetId: input.targetId || 11,
    targetAvailable: input.targetAvailable,
    targetPath: input.targetPath,
    payload: input.payload || {},
    createdAt: input.createdAt || '2026-07-23T08:00:00.000Z',
    readAt: input.readAt,
    actorUserId: input.actorUserId,
    actor: input.actor
  }
}

function createStateStore() {
  const state = new Map<string, ReturnType<typeof ref>>()
  return {
    useState<T>(key: string, init: () => T) {
      if (!state.has(key)) state.set(key, ref(init()))
      return state.get(key) as ReturnType<typeof ref<T>>
    }
  }
}

async function withApiGlobals(run: () => Promise<void>) {
  const originalFetch = globalThis.$fetch
  const originalUseRuntimeConfig = globalThis.useRuntimeConfig
  const originalUseNuxtApp = globalThis.useNuxtApp
  const originalUseCookie = globalThis.useCookie
  const originalUseState = globalThis.useState
  const csrfCookie = ref('csrf-token')
  const stateStore = createStateStore()

  globalThis.useRuntimeConfig = () => ({
    public: { apiBaseUrl: '/api/v1', appLocale: 'zh-CN' }
  })
  globalThis.useNuxtApp = () => ({ $i18n: { locale: ref('zh-CN') } })
  globalThis.useCookie = () => csrfCookie
  globalThis.useState = stateStore.useState

  try {
    await run()
  } finally {
    globalThis.$fetch = originalFetch
    globalThis.useRuntimeConfig = originalUseRuntimeConfig
    globalThis.useNuxtApp = originalUseNuxtApp
    globalThis.useCookie = originalUseCookie
    globalThis.useState = originalUseState
  }
}

describe('notification presentation helpers', () => {
  test('limits every Navbar preview tab to the latest three API-ordered notifications', () => {
    const rows = Array.from({ length: 8 }, (_, index) => item({ id: 8 - index }))

    expect(NOTIFICATION_PREVIEW_LIMIT).toBe(3)
    expect(notificationPreviewTabs).toEqual(['all', 'reply', 'mention'])
    expect(notificationPreviewFilters('all')).toEqual({})
    expect(notificationPreviewFilters('reply')).toEqual({ type: 'reply' })
    expect(notificationPreviewFilters('mention')).toEqual({ type: 'mention' })
    expect(limitNotificationPreviewItems(rows).map(row => row.id)).toEqual([8, 7, 6])
  })

  test('maps only API-backed notification fields into visible presentation', () => {
    const view = notificationPresentation(item({
      id: 42,
      type: 'mention',
      payload: { topicId: 9, commentId: 15, title: 'A real topic title' },
      readAt: undefined
    }))

    expect(view).toMatchObject({
      id: 42,
      type: 'mention',
      read: false,
      titleKey: 'notifications.types.mention',
      targetLabel: 'A real topic title',
      typeLabelKey: 'notifications.filter.mention',
      target: { path: '/t/9#comment-15', unavailable: false }
    })
    expect(view.bodyKey).toBe('notifications.body.mention')
  })

  test('uses actor avatars only for user-authored notification types', () => {
    const actor = {
      id: 8,
      username: 'alice',
      displayName: 'Alice',
      avatar: { kind: 'uploaded' as const, url: '/api/v1/attachments/avatar/content', alt: 'Alice' }
    }

    expect(notificationPresentation(item({ type: 'reply', actor })).actor).toEqual(actor)
    expect(notificationPresentation(item({ type: 'mention', actor })).actor).toEqual(actor)

    const system = notificationPresentation(item({ type: 'moderation_approved', actor }))
    expect(system.actor).toBeUndefined()
    expect(system.icon).toBe('i-tabler-shield-check')
  })

  test('opens pending moderation notifications through the authorized workbench target', () => {
    const view = notificationPresentation(item({
      type: 'moderation_pending',
      targetType: 'moderation_topic',
      targetId: 42,
      targetAvailable: true,
      targetPath: '/moderation?reviewType=topic&reviewId=42&source=pre_publish',
      payload: { title: 'Needs review', topicId: 42 }
    }))

    expect(view.icon).toBe('i-tabler-shield-exclamation')
    expect(view.titleKey).toBe('notifications.types.moderation_pending')
    expect(view.bodyKey).toBe('notifications.body.moderation_pending')
    expect(view.target).toEqual({ path: '/moderation?reviewType=topic&reviewId=42&source=pre_publish', unavailable: false })
    expect(view.actor).toBeUndefined()
  })

  test('counts the loaded notification scope for the shared type menu', () => {
    const counts = notificationFilterCounts([
      { type: 'reply', read: false },
      { type: 'reply', read: true },
      { type: 'mention', read: true }
    ])

    expect(counts.get('all')).toBe(3)
    expect(counts.get('unread')).toBe(1)
    expect(counts.get('reply')).toBe(2)
    expect(counts.get('mention')).toBe(1)
    expect(counts.get('moderation_approved')).toBeUndefined()
  })

  test('groups by today and earlier using the current locale timezone boundary', () => {
    const rows = [
      item({ id: 3, createdAt: '2026-07-22T16:10:00.000Z' }),
      item({ id: 2, createdAt: '2026-07-22T15:50:00.000Z' }),
      item({ id: 1, createdAt: '2026-07-20T05:00:00.000Z' })
    ]

    const groups = groupNotificationsByDate(rows, new Date('2026-07-22T16:30:00.000Z'), 'Asia/Shanghai', 'zh-CN')

    expect(groups.map(group => group.key)).toEqual(['today', 'earlier'])
    expect(groups[0].items.map(row => row.id)).toEqual([3])
    expect(groups[1].items.map(row => row.id)).toEqual([2, 1])
  })

  test('preserves API order inside date groups and filters only loaded notifications', () => {
    const views = [
      notificationPresentation(item({ id: 7, type: 'reply', readAt: undefined })),
      notificationPresentation(item({ id: 6, type: 'mention', readAt: '2026-07-23T09:00:00.000Z' })),
      notificationPresentation(item({ id: 5, type: 'reply', readAt: '2026-07-23T10:00:00.000Z' }))
    ]

    expect(filterNotifications(views, 'reply').map(view => view.id)).toEqual([7, 5])
    expect(filterNotifications(views, 'unread').map(view => view.id)).toEqual([7])
    expect(unreadLoadedCount(views)).toBe(1)
  })

  test('the detail target title prefers the badge name, then the payload title, then the type label', () => {
    // 徽章授予：badgeName 是「我拿到了哪枚」，必须排在主题标题之前。
    expect(notificationTargetTitle(item({
      type: 'badge_granted',
      targetType: 'badge',
      targetId: 12,
      payload: { badgeName: '常客', title: '某个主题' }
    }))).toBe('常客')

    // 没有 badgeName 时退到 payload.title（普通内容类通知的既有口径）。
    expect(notificationTargetTitle(item({
      type: 'badge_granted',
      payload: { title: '某个主题' }
    }))).toBe('某个主题')

    // 两者都没有时返回空串（`typeTargetLabel` 对徽章也只看 badgeName），由调用方决定不画标题。
    expect(notificationTargetTitle(item({ type: 'badge_granted', payload: {} }))).toBe('')
    expect(notificationTargetTitle(item({ type: 'reply', payload: {} }))).toBe('')
  })

  test('the target-available judgement is a real conjunction, not a shortcut', () => {
    // 真值表：只有「unavailable 为假」**且**「path 非空」才算有目标。
    // 任何一半被换成 `||` 或整段漏掉 path，都会在这里翻红 —— 这正是「有目标但无预览」
    // 那个分支的判据，靠页面源码字符串匹配抓不到。
    expect(notificationHasTarget({ path: '/t/9', unavailable: false })).toBe(true)
    expect(notificationHasTarget({ path: '', unavailable: false })).toBe(false)
    expect(notificationHasTarget({ path: '/t/9', unavailable: true })).toBe(false)
    expect(notificationHasTarget({ path: '', unavailable: true })).toBe(false)
    expect(notificationHasTarget(null)).toBe(false)
    expect(notificationHasTarget(undefined)).toBe(false)

    // 与 notificationTarget 的口径对齐：徽章授予（没有正文预览）有可用目标。
    const badge = notificationPresentation(item({
      type: 'badge_granted',
      targetType: 'badge',
      targetId: 12,
      targetAvailable: true,
      targetPath: '/u/zhuchunshu?tab=badges',
      payload: { badgeName: '常客' }
    }))
    expect(badge.target).toEqual({ path: '/u/zhuchunshu?tab=badges', unavailable: false })
    expect(notificationHasTarget(badge.target)).toBe(true)

    // 目标不可用：path 会回落成 /notifications，但 unavailable=true 必须判成没有目标。
    const gone = notificationPresentation(item({
      type: 'badge_granted',
      targetType: 'badge',
      targetId: 12,
      targetAvailable: false,
      payload: { badgeName: '常客' }
    }))
    expect(gone.target).toEqual({ path: '/notifications', unavailable: true })
    expect(notificationHasTarget(gone.target)).toBe(false)
  })

  test('does not fabricate a target link when the API lacks a reliable topic target', () => {
    const target = notificationTarget(item({ type: 'admin_test', targetType: 'system', targetId: 0, payload: {} }))

    expect(target).toEqual({ path: '/notifications', unavailable: true })
  })
})

describe('useNotifications', () => {
  test('uses the current notification API pagination contract', async () => {
    const calls: string[] = []

    await withApiGlobals(async () => {
      globalThis.$fetch = async (url: string) => {
        calls.push(url)
        return { code: 200, message: 'ok', data: { items: [], hasMore: false } }
      }

      const api = useNotifications()
      await api.list()
      await api.list(88, 10, { category: 'moderation', type: 'moderation_approved', unread: true })
      await api.get(42)
    })

    expect(calls).toEqual([
      '/api/v1/notifications?limit=20',
      '/api/v1/notifications?beforeId=88&limit=10&category=moderation&type=moderation_approved&unread=true',
      '/api/v1/notifications/42'
    ])
  })

  test('syncs unread count after single and all-read operations', async () => {
    const calls: Array<{ url: string, method?: string }> = []

    await withApiGlobals(async () => {
      globalThis.$fetch = async (url: string, options?: { method?: string }) => {
        calls.push({ url, method: options?.method })
        if (url.endsWith('/unread-count')) return { code: 200, message: 'ok', data: { count: 3 } }
        if (url.endsWith('/read-all')) return { code: 200, message: 'ok', data: { updated: 2 } }
        return { code: 200, message: 'ok', data: { read: true } }
      }

      const api = useNotifications()
      await api.refreshUnreadCount()
      expect(api.unreadCount.value).toBe(3)
      await api.markRead(12)
      expect(api.unreadCount.value).toBe(2)
      const updated = await api.markAllRead()
      expect(updated).toBe(2)
      expect(api.unreadCount.value).toBe(0)
    })

    expect(calls.map(call => [call.url, call.method || 'GET'])).toEqual([
      ['/api/v1/notifications/unread-count', 'GET'],
      ['/api/v1/notifications/12/read', 'PATCH'],
      ['/api/v1/notifications/read-all', 'POST']
    ])
  })

  test('shares one EventSource and coalesces revision refreshes', async () => {
    const originalEventSource = globalThis.EventSource
    const sources: FakeEventSource[] = []
    class FakeEventSource {
      static readonly CONNECTING = 0
      static readonly OPEN = 1
      static readonly CLOSED = 2
      readonly url: string
      readyState = FakeEventSource.OPEN
      closed = false
      listeners = new Map<string, Array<(event: MessageEvent<string>) => void>>()
      constructor(url: string) {
        this.url = url
        sources.push(this)
      }
      addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
        const callback = listener as (event: MessageEvent<string>) => void
        this.listeners.set(type, [...(this.listeners.get(type) || []), callback])
      }
      emit(type: string, data: string) {
        for (const listener of this.listeners.get(type) || []) listener({ data } as MessageEvent<string>)
      }
      close() { this.closed = true; this.readyState = FakeEventSource.CLOSED }
    }

    await withApiGlobals(async () => {
      globalThis.EventSource = FakeEventSource as unknown as typeof EventSource
      const first = useNotifications()
      const second = useNotifications()
      let firstRefreshes = 0
      let secondRefreshes = 0
      const stopFirst = first.startRealtime(1, () => { firstRefreshes++ })
      const stopSecond = second.startRealtime(1, () => { secondRefreshes++ })
      expect(sources).toHaveLength(1)
      expect(sources[0]?.url).toBe('/api/v1/notifications/stream?revision=0')

      sources[0]?.emit('revision', '{"revision":1}')
      sources[0]?.emit('revision', '{"revision":2}')
      await new Promise(resolve => setTimeout(resolve, 130))
      expect(firstRefreshes).toBe(1)
      expect(secondRefreshes).toBe(1)
      expect(first.revision.value).toBe(2)

      stopFirst()
      expect(sources[0]?.closed).toBe(false)
      sources[0]?.emit('revision', '{"revision":3}')
      second.stopRealtime()
      expect(sources[0]?.closed).toBe(true)
      await new Promise(resolve => setTimeout(resolve, 130))
      expect(firstRefreshes).toBe(1)
      expect(secondRefreshes).toBe(1)
      stopSecond()
    })
    globalThis.EventSource = originalEventSource
  })

  test('closes a failed EventSource and reconnects it through the controlled timer', async () => {
    const originalEventSource = globalThis.EventSource
    const sources: RecoveringFakeEventSource[] = []
    class RecoveringFakeEventSource {
      static readonly CONNECTING = 0
      static readonly OPEN = 1
      static readonly CLOSED = 2
      readonly url: string
      readyState = RecoveringFakeEventSource.OPEN
      listeners = new Map<string, Array<(event: Event) => void>>()
      constructor(url: string) {
        this.url = url
        sources.push(this)
      }
      addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
        const callback = listener as (event: Event) => void
        this.listeners.set(type, [...(this.listeners.get(type) || []), callback])
      }
      failWhileConnecting() {
        this.readyState = RecoveringFakeEventSource.CONNECTING
        for (const listener of this.listeners.get('error') || []) listener(new Event('error'))
      }
      close() { this.readyState = RecoveringFakeEventSource.CLOSED }
    }

    await withApiGlobals(async () => {
      globalThis.EventSource = RecoveringFakeEventSource as unknown as typeof EventSource
      const api = useNotifications()
      let refreshes = 0
      const stop = api.startRealtime(1, () => { refreshes++ })

      expect(sources).toHaveLength(1)
      sources[0]?.failWhileConnecting()
      expect(sources[0]?.readyState).toBe(RecoveringFakeEventSource.CLOSED)
      await new Promise(resolve => setTimeout(resolve, 130))
      expect(refreshes).toBe(1)

      await new Promise(resolve => setTimeout(resolve, 950))
      expect(sources).toHaveLength(2)
      stop()
    })
    globalThis.EventSource = originalEventSource
  })

  test('keeps manual REST available when EventSource construction fails', async () => {
    const originalEventSource = globalThis.EventSource
    await withApiGlobals(async () => {
      globalThis.EventSource = class {
        constructor() { throw new Error('SSE unavailable') }
      } as unknown as typeof EventSource
      globalThis.$fetch = async () => ({ code: 200, message: 'ok', data: { count: 4 } })
      const api = useNotifications()
      const stop = api.startRealtime(1, () => {})
      expect(await api.refreshUnreadCount()).toBe(4)
      stop()
    })
    globalThis.EventSource = originalEventSource
  })
})

describe('SFNotificationsPage contract', () => {
  test('delegates the Navbar bell, unread reconciliation, and realtime preview to the notification domain', () => {
    const navbar = navbarSource()
    const mobileBottomNavigation = mobileBottomNavigationSource()
    const preview = notificationPreviewSource()

    expect(navbar).toContain('<SFNotificationPreview v-if="user" />')
    expect(navbar).toContain('<SFPublicMobileBottomNavigation')
    expect(mobileBottomNavigation).toContain("import { useNotifications } from '~/composables/notifications/useNotifications'")
    expect(mobileBottomNavigation).toContain('const notifications = useNotifications()')
    expect(mobileBottomNavigation).toContain('notifications.unreadCount.value')
    expect(mobileBottomNavigation).toContain('sf-public-mobile-bottom-nav__notifications')
    const bottomNav = mobileBottomNavigation.slice(mobileBottomNavigation.indexOf('<nav class="sf-public-mobile-bottom-nav"'))
    expect(bottomNav.indexOf("<span>{{ t('nav.home') }}</span>")).toBeLessThan(bottomNav.indexOf("<span>{{ t('nav.newTopic') }}</span>"))
    expect(bottomNav.indexOf("<span>{{ t('nav.newTopic') }}</span>")).toBeLessThan(bottomNav.indexOf("<span>{{ t('nav.notifications') }}</span>"))
    expect(preview).toContain('onMounted(() =>')
    expect(preview).toContain('void notifications.refreshUnreadCount().catch(() => {})')
    expect(preview).toContain('stopRealtime = notifications.startRealtime(actorUserId, async () =>')
    expect(preview).toContain('stopRealtime()')
  })

  test('renders an accessible three-item desktop popover and mobile bottom sheet for each preview tab', () => {
    const preview = notificationPreviewSource()
    const styles = notificationPreviewStyles()

    expect(preview).toContain('notificationPreviewTabs')
    expect(preview).toContain('notifications.list(')
    expect(preview).toContain('NOTIFICATION_PREVIEW_LIMIT')
    expect(preview).toContain('notificationPreviewFilters(tab)')
    expect(preview).toContain('limitNotificationPreviewItems(page.items)')
    expect(preview).toContain('Promise.allSettled(nextItems.map(item => notifications.get(item.id)))')
    expect(preview).toContain('role="tablist"')
    expect(preview).toContain('role="dialog"')
    expect(preview).toContain("event.key === 'Escape'")
    expect(preview).toContain("t('notifications.preview.limitHint', { count: NOTIFICATION_PREVIEW_LIMIT })")
    expect(preview).toContain(":to=\"localePath('/settings/notifications')\"")
    expect(preview).toContain(":aria-label=\"t('notificationSettings.title')\"")
    expect(styles).toContain('@media (max-width: 640px)')
    expect(styles).toContain('bottom: 0;')
    expect(styles).toContain('max-height: min(78vh, 660px);')
  })

  test('opens the authorized target before marking a preview notification read', () => {
    const preview = notificationPreviewSource()
    const navigateIndex = preview.indexOf('await router.push(localePath(destination))')
    const markReadIndex = preview.indexOf('await notifications.markRead(item.id)')

    expect(navigateIndex).toBeGreaterThan(-1)
    expect(markReadIndex).toBeGreaterThan(navigateIndex)
    expect(preview).toContain('item.target.unavailable')
    expect(preview).toContain('`/notifications/${item.id}`')
  })

  test('compiles as a focused Page Registry island and keeps the route shell thin', () => {
    const route = source('../../app/pages/notifications/index.vue')
    const page = pageSource()
    expect(page).toContain('<script setup lang="ts">')
    expect(page).toContain('<template>')
    expect(page).toContain('<style scoped src="./SFNotificationsPage.css"></style>')
    expect(route).toContain('definePageMeta({ requiresAuth: true })')
    expect(route).toContain('<SFPageOutlet page="forum.notifications">')
    expect(route).toContain('<SFNotificationsPage />')
    expect(route).not.toContain('useNotifications()')
  })

  test('renders user-authored sources as avatars and system sources as Tabler icons', () => {
    const page = pageSource()
    const styles = pageStyles()

    expect(page).toContain('v-if="item.actor"')
    expect(page).toContain(':avatar="item.actor.avatar"')
    expect(page).toContain('sforum-notifications__source-icon')
    expect(page).not.toContain(':name="filterLabel(item.type)"')
    expect(styles).toContain('.sforum-notifications__source-icon')
  })

  test('uses the default theme three-column shell, shared community nav, and mobile drawer state', () => {
    const page = pageSource()
    const styles = pageStyles()

    expect(page).toContain('data-layout="fullwidth-3col"')
    expect(page).toContain('sforum-notifications__layout')
    expect(page).toContain('sforum-notifications__sidebar sforum-home__sidebar')
    expect(page).toContain('<SFHomeNavigation')
    expect(page).toContain('usePermissions()')
    expect(page).toContain('can(FORUM_PERMISSIONS.topicCreate)')
    expect(page).not.toContain("can('forum.topic.create')")
    expect(page).toContain("navigation-mode=\"route\"")
    expect(page).toContain('<SFResponsivePublicSidebar')
    expect(page).toContain('owner-id="forum.notifications"')
    expect(page).not.toContain("useState<boolean>('forum-mobile-menu-open'")
    expect(page).toContain("useState<boolean>('forum-mobile-info-open'")
    expect(page).not.toContain('@click="mobileInfoOpen = true"')
    expect(page).not.toContain('sforum-mobile-drawer sforum-mobile-drawer--left')
    expect(page).toContain('sforum-mobile-drawer sforum-mobile-drawer--right')
    expect(page).not.toContain('<SFContentColumnFooter')
    expect(styles).toContain('.sforum-mobile-drawer .sforum-notifications__right--drawer')
    expect(styles).toContain('display: block')
    expect(styles).toContain('.sforum-notifications__main {\n    position: sticky;\n    top: var(--sf-public-topbar-height);\n    height: calc(100vh - var(--sf-public-topbar-height));\n    min-height: 0;\n    overflow-y: auto;')
    expect(styles).toContain('.sforum-notifications__sidebar,\n  .sforum-notifications__right {\n    position: sticky;\n    top: var(--sf-public-topbar-height);\n    height: calc(100vh - var(--sf-public-topbar-height));\n    min-height: 0;\n    overflow-y: auto;')
    expect(page).not.toContain('ssr: false')
  })

  test('does not duplicate the global mobile right-rail trigger on notification detail', () => {
    const detailPage = source('../../app/components/notifications/detail/SFNotificationDetailPage.vue')

    expect(detailPage).toContain("useState<boolean>('forum-mobile-info-open'")
    expect(detailPage).not.toContain('@click="mobileInfoOpen = true"')
    expect(detailPage).toContain('sforum-mobile-drawer sforum-mobile-drawer--right')
  })

  test('uses server-authoritative type filters while preserving cursor pagination', () => {
    const page = pageSource()
    const composable = notificationComposableSource()

    expect(page).toContain('serverFilters(activeFilter.value)')
    expect(page).toContain('notifications.list(beforeId, PAGE_LIMIT, serverFilters(activeFilter.value))')
    expect(page).toContain('notificationFilters')
    expect(composable).toContain("params.set('type', filters.type)")
    expect(composable).toContain("params.set('unread', String(filters.unread))")
    expect(composable).toContain('beforeId')
    expect(composable).toContain('limit')
  })

  test('shares the notification type menu between list and detail sidebars', () => {
    const page = pageSource()
    const detail = detailPageSource()
    const typeNav = typeNavSource()

    expect(page).toContain('<SFNotificationTypeNav')
    expect(detail).toContain('<SFNotificationTypeNav')
    expect(detail).toContain("notifications.list(0, 20)")
    expect(detail).toContain("useState<NotificationFilter>('notification-inbox-filter'")
    expect(detail).toContain("router.push(localePath('/notifications'))")
    expect(typeNav).toContain('v-for="filter in notificationFilters"')
    expect(typeNav).toContain("t('notifications.filter.loadedScope', { count: loadedCount })")
  })

  test('opens an independent detail route and keeps all-read rollback and themed feedback', () => {
    const page = pageSource()

    expect(page).toContain("useState<NotificationFilter>('notification-inbox-filter'")
    expect(page).toContain('router.push(localePath(`/notifications/${item.id}`))')
    expect(page).not.toContain('selectedNotification')
    expect(page).toContain('markingAll.value')
    expect(page).toContain('previousUnreadCount')
    expect(page).toContain('setNotificationRead(previous.id, previous.readAt)')
    expect(page).toContain('notifications.markAllRead()')
    expect(page).toContain("duration: 0")
    expect(page).toContain("duration: 5000")
    expect(page).toContain("color: 'primary'")
    expect(page).toContain("color: 'error'")
  })

  test('uses an authenticated Page Registry route and marks read only after detail mounts', () => {
    const route = detailRouteSource()
    const detail = detailPageSource()

    expect(route).toContain('definePageMeta({ requiresAuth: true })')
    expect(route).toContain('<SFPageOutlet page="forum.notification.show">')
    expect(route).toContain('<SFNotificationDetailPage />')
    expect(route).not.toContain('useNotifications()')
    expect(detail).toContain('notifications.get(notificationId)')
    expect(detail).toContain('apiErrorStatusCode(detailAsync.error.value) === 404')
    expect(detail).toContain('onMounted(async () =>')
    expect(detail).toContain('await notifications.markRead(detail.value.id)')
    expect(detail).toContain('preview.context')
    expect(detail).toContain('router.push(localePath(presented.value.target.path))')
    expect(detail).toContain('data-sforum-island-body="forum.component.notification_detail"')
    expect(detail).toContain('data-layout="fullwidth-3col"')
    expect(detail).not.toContain('<SFContentColumnFooter')
    expect(detail).not.toContain('ssr: false')
  })

  test('the detail page takes the target judgement from the shared helper and wires both branches', () => {
    const detail = detailPageSource()

    // 判据只有一份：组件不再自己写布尔组合（否则真值表保护不到它）。
    expect(detail).toContain('notificationHasTarget(presented.value?.target)')
    expect(detail).not.toContain('!presented.value.target.unavailable')

    // 正文：有预览走内容块，没预览但有目标走目标卡，最后才是「目标不可用」。
    expect(detail).toContain('v-else-if="hasTarget"')
    const targetCardAt = detail.indexOf('v-else-if="hasTarget"')
    const unavailableAt = detail.indexOf("t('notifications.targetUnavailable')")
    expect(targetCardAt).toBeGreaterThan(-1)
    expect(unavailableAt).toBeGreaterThan(targetCardAt)

    // 右栏与正文共用同一口径（同一页不能出现两个结论）。
    expect(detail).toContain("{{ hasTarget ? t('notifications.detailPage.available') : t('notifications.targetUnavailable') }}")
    expect(detail).not.toContain('detail.targetAvailable ?')

    // 目标卡：说明文案 + 「查看我的徽章」+ 复用 openTarget()。
    expect(detail).toContain("t('notifications.detailPage.targetAvailableHelp')")
    expect(detail).toContain("t('notifications.actions.viewBadges')")
    expect(detail).toContain('@click="openTarget"')
    expect(detail).toContain('notificationTargetTitle(detail.value)')
  })

  test('ships bilingual copy for every notification type and state added by the page', () => {
    for (const locale of [zh(), en()]) {
      expect(locale.notifications.types.admin_test).toBeTruthy()
      expect(locale.notifications.types.moderation_pending).toBeTruthy()
      expect(locale.notifications.filter.loadedScope).toBeTruthy()
      expect(locale.notifications.groups.today).toBeTruthy()
      expect(locale.notifications.groups.earlier).toBeTruthy()
      expect(locale.notifications.body.moderation_rejected_note).toBeTruthy()
      expect(locale.notifications.actions.viewReply).toBeTruthy()
      expect(locale.notifications.detail.empty).toBeTruthy()
      expect(locale.notifications.detailPage.repliedComment).toBeTruthy()
      expect(locale.notifications.detailPage.originalTopic).toBeTruthy()
      expect(locale.notifications.targetUnavailableHelp).toBeTruthy()
      expect(locale.notifications.detailPage.targetAvailableHelp).toBeTruthy()
      expect(locale.notifications.actions.viewBadges).toBeTruthy()
      expect(locale.notifications.preview.limitHint).toBeTruthy()
      expect(locale.notifications.preview.viewAll).toBeTruthy()
      expect(locale.notifications.preview.tabs.reply).toBeTruthy()
    }
  })
})

describe('notification inbox polish contract', () => {
  test('every inbox filter has a label, a type title, and a body template in both locales', () => {
    // 目录是页面渲染的输入：少一条 key 就会在 UI 上漏出 `notifications.filter.*` 原始键。
    expect(notificationFilters).toContain('reaction')
    expect(notificationFilters).toContain('bookmark_reminder')
    expect(notificationFilters.indexOf('bookmark_reminder')).toBeGreaterThan(notificationFilters.indexOf('reaction'))

    for (const locale of [zh(), en()]) {
      for (const filter of notificationFilters) {
        expect(locale.notifications.filter[filter]).toBeTruthy()
        // `all` / `unread` 是范围不是类型，没有专属标题与正文模板。
        if (filter === 'all' || filter === 'unread') continue
        expect(locale.notifications.types[filter]).toBeTruthy()
        expect(locale.notifications.body[filter]).toBeTruthy()
      }
    }
  })

  test('the shared type icon catalog never degrades a real type into the generic bell', () => {
    const realTypes = [
      'reply',
      'mention',
      'reaction',
      'bookmark_reminder',
      'badge_granted',
      'moderation_pending',
      'moderation_approved',
      'moderation_rejected',
      'admin_test'
    ]

    for (const type of realTypes) {
      expect(notificationTypeIcon(type)).not.toBe('i-tabler-bell')
      expect(notificationTypeIcon(type).startsWith('i-tabler-')).toBe(true)
    }

    // 三种互动语义必须彼此可区分，而不是共用一个铃铛。
    expect(notificationTypeIcon('reaction')).toBe('i-tabler-heart')
    expect(notificationTypeIcon('badge_granted')).toBe('i-tabler-award')
    expect(notificationTypeIcon('bookmark_reminder')).toBe('i-tabler-bookmark')
    // 未知/插件类型才允许回退。
    expect(notificationTypeIcon('plugin_unknown_type')).toBe('i-tabler-bell')

    // 桌面左栏复用同一目录：本地只留 all/unread，不再维护第二份类型映射。
    const typeNav = typeNavSource()
    expect(typeNav).toContain('notificationTypeIcon(filter)')
    expect(typeNav).not.toContain("reply: 'i-tabler-message-reply'")
    expect(typeNav).not.toContain("moderation_approved: 'i-tabler-shield-check'")
  })

  test('owns inbox robots and title through the shared SEO resolver on list and detail', () => {
    const route = listRouteSource()
    const detail = detailPageSource()

    expect(route).toContain("import { useSForumSeo } from '~/composables/seo/useSForumSeo'")
    expect(route).toContain("title: () => t('notifications.title')")
    expect(route).toContain("description: () => t('notifications.description')")
    expect(route).toMatch(/useSForumSeo\(\{[\s\S]*?noindex: true/)

    expect(detail).toContain("import { useSForumSeo } from '~/composables/seo/useSForumSeo'")
    expect(detail).toContain("title: () => preview.value?.topicTitle || t('notifications.detailPage.title')")
    expect(detail).toMatch(/useSForumSeo\(\{[\s\S]*?noindex: true/)
    // 裸 useHead 会与公共 SEO 解析器抢 title/robots 归属。
    expect(detail).not.toContain('useHead(')
  })

  test('keeps the middle column on the shared surface token and a single desktop filter surface', () => {
    const styles = pageStyles()

    expect(styles).toMatch(/\.sforum-notifications__main \{[^}]*background: var\(--sf-public-surface\);/)
    expect(styles).not.toContain('var(--sf-public-bg)')

    const desktopStart = styles.indexOf('@media (min-width: 981px) {')
    const desktopEnd = styles.indexOf('@media (max-width: 980px) {')
    expect(desktopStart).toBeGreaterThan(-1)
    expect(desktopEnd).toBeGreaterThan(desktopStart)
    expect(styles.slice(desktopStart, desktopEnd))
      .toMatch(/\.sforum-notifications__filter-strip,\s*\.sforum-notifications__filter-note \{\s*display: none;/)

    // 空状态自带虚线边框，外层不再叠一条实线。
    const emptyRule = styles.slice(
      styles.indexOf('.sforum-notifications__empty {'),
      styles.indexOf('.sforum-notifications__group-head {')
    )
    expect(emptyRule.length).toBeGreaterThan(0)
    expect(emptyRule).not.toContain('border-bottom')
  })

  test('renders one reachable empty state that switches copy and icon by the active filter', () => {
    const empty = emptyStateSource()
    expect(empty).toContain('icon?: string')
    expect(empty).toContain("icon: 'i-lucide-inbox'")
    expect(empty).toContain(':name="props.icon"')

    const page = pageSource()
    // 服务端已按类型筛选，`items` 为空就是「这个筛选下没有通知」；两段分支会把后者挡死。
    expect(page.match(/<SFEmptyState/g)).toHaveLength(1)
    expect(page).toContain('v-else-if="!filteredItems.length"')
    expect(page).not.toContain('v-else-if="!items.length"')
    expect(page).toContain("activeFilter === 'all' ? 'i-lucide-bell-off' : 'i-lucide-filter-x'")
    expect(page).toContain("activeFilter === 'all' ? t('notifications.empty') : t('notifications.filter.empty')")
    expect(page).toContain("activeFilter === 'all' ? t('notifications.emptyDescription') : t('notifications.filter.emptyDescription')")
    expect(page).not.toContain("t('notifications.filter.emptyDescription', { count:")
  })
})

describe('notification mobile layout and hydration contract', () => {
  test('the detail header left-aligns below 560px while the desktop rule stays flex-end', () => {
    const styles = detailStyles()

    // 桌面：头部是 row，flex-end 是有意的右对齐，不能被顺手改掉。
    expect(styles).toMatch(/^\.sforum-notification-detail__head \{\n  align-items: flex-end;/m)

    const mobileStart = styles.indexOf('@media (max-width: 560px) {')
    expect(mobileStart).toBeGreaterThan(-1)
    // 窄屏头部已变成 column，交叉轴上的 flex-end 会把整块内容推到右边。
    expect(styles.slice(mobileStart)).toMatch(/\.sforum-notification-detail__head \{\s*align-items: flex-start;/)
    expect(styles.slice(0, mobileStart)).not.toMatch(/\.sforum-notification-detail__head \{\s*align-items: flex-start;/)
  })

  test('both unread badges sit behind ClientOnly so SSR and the client first frame agree', () => {
    const cases = [
      { label: 'SFNotificationPreview', src: notificationPreviewSource(), badge: 'sf-notification-preview__badge' },
      { label: 'SFPublicMobileBottomNavigation', src: mobileBottomNavigationSource(), badge: 'sf-public-mobile-bottom-nav__badge' }
    ]

    for (const entry of cases) {
      const badgeAt = entry.src.indexOf(entry.badge)
      expect(badgeAt).toBeGreaterThan(-1)

      const openAt = entry.src.lastIndexOf('<ClientOnly>', badgeAt)
      const closeAt = entry.src.indexOf('</ClientOnly>', badgeAt)
      expect(openAt).toBeGreaterThan(-1)
      expect(closeAt).toBeGreaterThan(badgeAt)
      // 徽章必须整段落在同一对 ClientOnly 之内。
      expect(entry.src.slice(openAt, closeAt)).toContain(entry.badge)
    }

    // 防止「把徽章删掉」冒充修复：未读数的渲染条件必须原样保留。
    expect(notificationPreviewSource()).toContain('v-if="notifications.unreadCount.value"')
    expect(mobileBottomNavigationSource()).toContain('v-if="authenticated && notifications.unreadCount.value"')
  })
})
