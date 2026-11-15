import { describe, expect, test } from 'bun:test'
import { ref } from 'vue'

import { useApiClient } from '../../app/composables/useApiClient'
import { useNotifications } from '../../app/composables/notifications/useNotifications'

/**
 * RV-33：未读角标必须按 id 幂等递减。
 *
 * 真实的双调用来自两个组件：导航栏预览弹层在跳转成功后 `markRead`，
 * 详情页在 `onMounted` 里对同一条再 `markRead` 一次。服务端 PATCH 是幂等的，
 * 所以问题只在本地角标——两次调用必须只减 1。
 */

;(globalThis as any).useApiClient = useApiClient

function createStateStore() {
  const state = new Map<string, ReturnType<typeof ref>>()
  return {
    useState<T>(key: string, init: () => T) {
      if (!state.has(key)) state.set(key, ref(init()))
      return state.get(key) as ReturnType<typeof ref<T>>
    }
  }
}

async function withApiGlobals(run: (calls: Array<{ url: string, method?: string }>) => Promise<void>) {
  const originalFetch = globalThis.$fetch
  const originalUseRuntimeConfig = globalThis.useRuntimeConfig
  const originalUseNuxtApp = globalThis.useNuxtApp
  const originalUseCookie = globalThis.useCookie
  const originalUseState = globalThis.useState
  const csrfCookie = ref('csrf-token')
  const stateStore = createStateStore()
  const calls: Array<{ url: string, method?: string }> = []

  globalThis.useRuntimeConfig = () => ({
    public: { apiBaseUrl: '/api/v1', appLocale: 'zh-CN' }
  })
  globalThis.useNuxtApp = () => ({ $i18n: { locale: ref('zh-CN') } })
  globalThis.useCookie = () => csrfCookie
  globalThis.useState = stateStore.useState
  globalThis.$fetch = (async (url: string, options?: { method?: string }) => {
    calls.push({ url, method: options?.method })
    if (url.endsWith('/unread-count')) return { code: 200, message: 'ok', data: { count: 3 } }
    return { code: 200, message: 'ok', data: { read: true } }
  }) as unknown as typeof globalThis.$fetch

  try {
    await run(calls)
  } finally {
    globalThis.$fetch = originalFetch
    globalThis.useRuntimeConfig = originalUseRuntimeConfig
    globalThis.useNuxtApp = originalUseNuxtApp
    globalThis.useCookie = originalUseCookie
    globalThis.useState = originalUseState
  }
}

describe('useNotifications markRead idempotency', () => {
  test('marks the same notification read twice but decrements the badge once', async () => {
    await withApiGlobals(async (calls) => {
      const api = useNotifications()
      await api.refreshUnreadCount()
      expect(api.unreadCount.value).toBe(3)

      await api.markRead(12)
      expect(api.unreadCount.value).toBe(2)

      // 详情页对同一条再调一次：PATCH 照发（服务端幂等），角标不能再减。
      await api.markRead(12)
      expect(api.unreadCount.value).toBe(2)

      expect(calls.map(call => [call.url, call.method || 'GET'])).toEqual([
        ['/api/v1/notifications/unread-count', 'GET'],
        ['/api/v1/notifications/12/read', 'PATCH'],
        ['/api/v1/notifications/12/read', 'PATCH']
      ])
    })
  })

  test('decrements once per distinct notification id', async () => {
    await withApiGlobals(async () => {
      const api = useNotifications()
      await api.refreshUnreadCount()

      await api.markRead(12)
      await api.markRead(13)
      expect(api.unreadCount.value).toBe(1)

      await api.markRead(13)
      expect(api.unreadCount.value).toBe(1)
    })
  })

  test('shares the read accounting between the two components that call markRead', async () => {
    await withApiGlobals(async () => {
      // 预览弹层与详情页各自调用一次 useNotifications()，是不同实例；
      // 幂等记账必须跟着共享的 useState 走，否则两个实例会各减一次。
      const preview = useNotifications()
      const detail = useNotifications()
      await preview.refreshUnreadCount()
      expect(preview.unreadCount.value).toBe(3)

      await preview.markRead(12)
      await detail.markRead(12)

      expect(preview.unreadCount.value).toBe(2)
      expect(detail.unreadCount.value).toBe(2)
    })
  })

  test('never drops the badge below zero', async () => {
    await withApiGlobals(async () => {
      const api = useNotifications()
      await api.refreshUnreadCount()
      await api.markRead(12)
      await api.markAllRead()
      await api.markRead(99)
      expect(api.unreadCount.value).toBe(0)
    })
  })
})
