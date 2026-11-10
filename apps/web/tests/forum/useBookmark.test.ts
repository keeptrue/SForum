import { afterAll, beforeEach, describe, expect, test } from 'bun:test'
import { computed, ref, toValue, watch } from 'vue'
import { useBookmark } from '~/composables/forum/useBookmark'

/**
 * 收藏状态机行为：乐观切换、失败回滚、可见性/提醒复用同一 PUT、匿名跳登录、开关关闭不发请求。
 *
 * useBookmark 里的 Nuxt 自动导入（useI18n/useToast/useRouter/useApiClient/useState）在
 * bun 环境里不存在，这里按运行时同名注入，测的是真实 composable 而不是它的副本。
 */

type ApiCall = { path: string; method: string; body?: unknown }
type ToastCall = { title: string; color?: string; duration?: number }

const calls: ApiCall[] = []
const toasts: ToastCall[] = []
const pushes: unknown[] = []
const authUser = ref<{ id: number } | null>({ id: 1 })
let responder: (call: ApiCall) => Promise<unknown> = async () => ({})

const globalKeys = [
  'computed', 'ref', 'watch', 'toValue', 'useI18n', 'useToast', 'useRouter',
  'useRoute', 'useLocalePath', 'useState', 'useApiClient'
] as const
const previousGlobals = new Map<string, unknown>(
  globalKeys.map(key => [key, (globalThis as Record<string, unknown>)[key]])
)

Object.assign(globalThis, {
  computed,
  ref,
  watch,
  toValue,
  useI18n: () => ({ t: (key: string) => key }),
  useToast: () => ({
    add: (toast: ToastCall) => {
      toasts.push(toast)
    }
  }),
  useRouter: () => ({
    push: async (target: unknown) => {
      pushes.push(target)
    }
  }),
  useRoute: () => ({ fullPath: '/t/7/hello' }),
  useLocalePath: () => (target: unknown) => target,
  // 真实 useAuthSession 走 useState('auth:user')：让测试能控制登录态。
  useState: (key: string, init: () => unknown) => (key === 'auth:user' ? authUser : ref(init())),
  useApiClient: () => ({
    request: async (path: string, options: { method?: string; body?: unknown } = {}) => {
      const call: ApiCall = { path, method: options.method || 'GET', body: options.body }
      calls.push(call)
      return await responder(call)
    }
  })
})

function state(overrides: Record<string, unknown> = {}) {
  return {
    targetType: 'topic',
    targetId: 7,
    visibility: 'private',
    bookmarked: true,
    publicBookmarkCount: 1,
    remindAt: null,
    ...overrides
  }
}

/** 服务端回声：把请求体里的元数据原样回填，验证「响应为准」的回写链路。 */
function echoState(call: ApiCall) {
  const body = (call.body ?? {}) as { visibility?: string; remindAt?: string | null }
  return state({
    bookmarked: call.method === 'PUT',
    visibility: body.visibility ?? 'private',
    remindAt: body.remindAt ?? null,
    publicBookmarkCount: call.method === 'PUT' ? 1 : 0
  })
}

function setup(overrides: { bookmarked?: boolean; enabled?: boolean; targetType?: 'topic' | 'comment' } = {}) {
  return useBookmark({
    targetType: () => overrides.targetType ?? 'topic',
    targetId: () => 7,
    bookmarked: () => overrides.bookmarked ?? false,
    enabled: () => overrides.enabled ?? true
  })
}

afterAll(() => {
  // 这些注入只服务本文件：还原全局，避免影响同进程内的其它用例。
  for (const [key, value] of previousGlobals) {
    if (value === undefined) {
      delete (globalThis as Record<string, unknown>)[key]
    } else {
      (globalThis as Record<string, unknown>)[key] = value
    }
  }
})

beforeEach(() => {
  calls.length = 0
  toasts.length = 0
  pushes.length = 0
  authUser.value = { id: 1 }
  responder = async () => state()
})

describe('useBookmark toggle', () => {
  test('adds optimistically through PUT and settles on the server state', async () => {
    const bookmark = setup()
    expect(bookmark.bookmarked.value).toBe(false)
    // 请求发出时本地已经是「已收藏」：乐观切换在 await 网络之前完成。
    let optimisticAtRequest: boolean | null = null
    responder = async (call) => {
      optimisticAtRequest = bookmark.bookmarked.value
      return echoState(call)
    }

    const pending = bookmark.toggle()
    expect(bookmark.pending.value).toBe(false)
    await pending

    expect(optimisticAtRequest).toBe(true)
    expect(bookmark.pending.value).toBe(false)
    expect(calls).toEqual([{
      path: '/topics/7/bookmark',
      method: 'PUT',
      body: { visibility: 'private', remindAt: null }
    }])
    expect(bookmark.bookmarked.value).toBe(true)
    expect(toasts).toEqual([{ title: 'bookmark.saved', color: 'success', duration: 5000, icon: 'i-lucide-check' }])
  })

  test('removes through DELETE and reports removal', async () => {
    responder = async () => state({ bookmarked: false, publicBookmarkCount: 0 })
    const bookmark = setup({ bookmarked: true })

    await bookmark.toggle()

    expect(calls).toEqual([{ path: '/topics/7/bookmark', method: 'DELETE', body: undefined }])
    expect(bookmark.bookmarked.value).toBe(false)
    expect(toasts[0]?.title).toBe('bookmark.removed')
  })

  test('comments use the comment endpoint', async () => {
    const bookmark = setup({ targetType: 'comment' })
    await bookmark.toggle()
    expect(calls[0]?.path).toBe('/comments/7/bookmark')
  })

  test('rolls the optimistic flip back on failure and keeps the error toast open', async () => {
    responder = async () => {
      throw Object.assign(new Error('forbidden'), { statusCode: 403 })
    }
    const bookmark = setup()

    await bookmark.toggle()

    expect(bookmark.bookmarked.value).toBe(false)
    expect(bookmark.pending.value).toBe(false)
    expect(toasts).toEqual([{
      title: 'bookmark.error.forbidden',
      color: 'error',
      duration: 0,
      icon: 'i-lucide-alert-circle'
    }])
  })

  test('maps the disabled feature reason to its own copy', async () => {
    responder = async () => {
      throw { statusCode: 403, data: { code: 403, message: 'disabled', data: { reason: 'forum.bookmarks_disabled' } } }
    }
    const bookmark = setup()
    await bookmark.toggle()
    expect(toasts[0]?.title).toBe('bookmark.error.unavailable')
  })
})

describe('useBookmark metadata writes reuse the same PUT', () => {
  test('visibility change keeps remindAt and reports the update', async () => {
    const bookmark = setup({ bookmarked: true })
    responder = async (call) => echoState(call)
    await bookmark.setVisibility('public')

    expect(calls).toEqual([{
      path: '/topics/7/bookmark',
      method: 'PUT',
      body: { visibility: 'public', remindAt: null }
    }])
    expect(bookmark.visibility.value).toBe('public')
    expect(toasts[0]?.title).toBe('bookmark.visibility.updated')
  })

  test('reminder set and clear both go through the same PUT', async () => {
    const bookmark = setup({ bookmarked: true })
    responder = async (call) => echoState(call)
    const future = new Date(Date.now() + 86400000).toISOString()
    await bookmark.setRemindAt(future)

    expect(calls[0]).toEqual({
      path: '/topics/7/bookmark',
      method: 'PUT',
      body: { visibility: 'private', remindAt: future }
    })
    expect(bookmark.remindAt.value).toBe(future)
    expect(toasts[0]?.title).toBe('bookmark.remind.saved')

    // 清除走显式开关：remindAt: null 在后端旧实现里不清行。
    responder = async () => state({ remindAt: null })
    await bookmark.setRemindAt(null)
    expect(calls[1]?.body).toEqual({ visibility: 'private', clearReminder: true })
    expect(bookmark.remindAt.value).toBeNull()
    expect(toasts[1]?.title).toBe('bookmark.remind.cleared')
  })

  test('clearing blanks the local reminder even when the server echoes the old time', async () => {
    const bookmark = setup({ bookmarked: true })
    const future = new Date(Date.now() + 86400000).toISOString()
    responder = async (call) => echoState(call)
    await bookmark.setRemindAt(future)
    expect(bookmark.remindAt.value).toBe(future)

    // 旧后端把旧 remindAt 原样回显：界面不能因此弹回原值却提示「已清除」。
    responder = async () => state({ remindAt: future, remindedAt: null })
    const saved = await bookmark.setRemindAt(null)

    expect(saved).toBe(true)
    expect(bookmark.remindAt.value).toBeNull()
    expect(bookmark.remindedAt.value).toBeNull()
    expect(toasts.at(-1)?.title).toBe('bookmark.remind.cleared')
  })

  test('setting the same reminder again after clearing still succeeds', async () => {
    const bookmark = setup({ bookmarked: true })
    const future = new Date(Date.now() + 86400000).toISOString()
    responder = async (call) => echoState(call)
    await bookmark.setRemindAt(future)
    responder = async () => state({ remindAt: null })
    await bookmark.setRemindAt(null)

    responder = async (call) => echoState(call)
    const saved = await bookmark.setRemindAt(future)

    expect(saved).toBe(true)
    expect(calls.at(-1)?.body).toEqual({ visibility: 'private', remindAt: future })
    expect(bookmark.remindAt.value).toBe(future)
    expect(toasts.at(-1)?.title).toBe('bookmark.remind.saved')
  })

  test('exposes remindedAt so the control can tell 已提醒 from 将提醒', async () => {
    const bookmark = setup({ bookmarked: true })
    const future = new Date(Date.now() + 86400000).toISOString()
    responder = async () => state({ remindAt: future, remindedAt: '2026-01-03T00:00:00.000Z' })
    await bookmark.setRemindAt(future)

    expect(bookmark.remindedAt.value).toBe('2026-01-03T00:00:00.000Z')
  })

  test('a past reminder is rejected locally without hitting the API', async () => {
    const bookmark = setup({ bookmarked: true })
    const saved = await bookmark.setRemindAt('2020-01-01T00:00:00.000Z')

    expect(saved).toBe(false)
    expect(calls).toEqual([])
    expect(toasts).toEqual([])
  })

  test('a reminder beyond the two-year ceiling is rejected locally', async () => {
    const bookmark = setup({ bookmarked: true })
    const tooFar = new Date(Date.now() + 3 * 365 * 86400000).toISOString()
    const saved = await bookmark.setRemindAt(tooFar)

    expect(saved).toBe(false)
    expect(calls).toEqual([])
  })

  test('server-side 422 reminder reasons map to their own copy', async () => {
    const bookmark = setup({ bookmarked: true })
    responder = async () => {
      throw { statusCode: 422, data: { code: 422, message: 'bad reminder', data: { reason: 'forum.bookmark_remind_in_past' } } }
    }
    const future = new Date(Date.now() + 86400000).toISOString()
    await bookmark.setRemindAt(future)

    expect(toasts.at(-1)?.title).toBe('bookmark.error.inPast')
    expect(toasts.at(-1)?.duration).toBe(0)
  })

  test('a failed metadata write rolls the local value back', async () => {
    const bookmark = setup({ bookmarked: true })
    responder = async () => {
      throw Object.assign(new Error('boom'), { statusCode: 500 })
    }
    await bookmark.setVisibility('public')

    expect(bookmark.visibility.value).toBe('private')
    expect(toasts[0]?.title).toBe('bookmark.error.unknown')
  })
})

describe('useBookmark guards', () => {
  test('guests are sent to login with the current page as redirect and no request is made', async () => {
    authUser.value = null
    const bookmark = setup()
    await bookmark.toggle()

    expect(calls).toEqual([])
    expect(pushes).toEqual([{ path: '/login', query: { redirect: '/t/7/hello' } }])
  })

  test('the disabled feature flag blocks every write', async () => {
    const bookmark = setup({ enabled: false })
    await bookmark.toggle()
    await bookmark.setVisibility('public')
    await bookmark.setRemindAt('2026-01-02T00:00:00.000Z')

    expect(calls).toEqual([])
    expect(bookmark.bookmarked.value).toBe(false)
  })
})
