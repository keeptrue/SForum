import { afterEach, describe, expect, test } from 'bun:test'

import { apiErrorMessage } from '../../app/composables/useApiClient'
import { useAdminUserList } from '../../app/composables/admin/identity/useAdminUserList'
import { flushPromises, testVue } from '../helpers/vueSfc'

/**
 * RV-32：用户列表的取数代次守卫。
 *
 * 真实场景是「翻到第 2 页 → 立刻改筛选」：筛选会回到第 1 页并发起新请求，
 * 而第 2 页的旧响应可能后到，把页码和行一起回退。守卫的作用是只让最新一代落地。
 */

const globals = globalThis as typeof globalThis & Record<string, unknown>

type Snapshot = Record<string, unknown>
let snapshot: Snapshot = {}

const GLOBAL_KEYS = [
  'ref',
  'reactive',
  'computed',
  'watch',
  'nextTick',
  'useTemplateRef',
  'useI18n',
  'useRoute',
  'useToast',
  'useApiClient',
  'apiErrorMessage'
] as const

const emptySurfaceCatalog = { revision: 'r1', surfaces: [], resolved: [], failures: [] }

function userRow(id: number, username: string) {
  return {
    id,
    username,
    email: `${username}@example.test`,
    displayName: username,
    locale: 'zh-CN',
    status: 'active',
    kind: 'human',
    isInitialSuperAdmin: false,
    roleKeys: ['member'],
    createdAt: '2026-10-27T02:00:00Z',
    updatedAt: '2026-10-27T02:00:00Z'
  }
}

type Deferred = {
  path: string
  resolve: (value: unknown) => void
  reject: (error: unknown) => void
}

/** 列表请求挂起，由测试决定谁先回来——乱序返回只能这样构造。 */
function installGlobals() {
  snapshot = {}
  for (const key of GLOBAL_KEYS) {
    snapshot[key] = globals[key]
  }

  const pending: Deferred[] = []
  const paths: string[] = []

  const request = (path: string) => {
    paths.push(path)
    if (path.startsWith('/admin/admin-surfaces')) {
      return Promise.resolve(emptySurfaceCatalog)
    }

    return new Promise((resolve, reject) => {
      pending.push({ path, resolve, reject })
    })
  }

  Object.assign(globals, {
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    nextTick: testVue.nextTick,
    useTemplateRef: testVue.useTemplateRef,
    useI18n: () => ({
      locale: testVue.ref('zh-CN'),
      t: (key: string) => key,
      te: () => false
    }),
    useRoute: () => ({ path: '/control-panel/users', params: {}, query: {} }),
    useToast: () => ({ add: () => {} }),
    useApiClient: () => ({ request }),
    apiErrorMessage
  })

  return { pending, paths }
}

function restoreGlobals() {
  for (const key of GLOBAL_KEYS) {
    if (snapshot[key] === undefined) {
      delete globals[key]
    } else {
      globals[key] = snapshot[key]
    }
  }
  snapshot = {}
}

afterEach(() => {
  restoreGlobals()
})

describe('useAdminUserList request generation', () => {
  test('ignores the stale page-2 response that arrives after the new filter result', async () => {
    const { pending } = installGlobals()
    const list = useAdminUserList({ onError: () => {} })

    // 第 2 页的请求先发。
    list.search.value = 'alpha'
    list.page.value = 2
    const stale = list.refreshUsers()

    // 立刻改筛选：applyFilters 把页码拉回 1 并发出新一代请求。
    list.search.value = 'beta'
    const fresh = list.applyFilters()
    await flushPromises()

    expect(pending).toHaveLength(2)
    expect(pending[0]!.path).toContain('page=2')
    expect(pending[1]!.path).toContain('page=1')
    expect(pending[1]!.path).toContain('query=beta')

    // 新的先回：这一代落地。
    pending[1]!.resolve({ items: [userRow(2, 'beta_user')], total: 40, page: 1, perPage: 20 })
    await flushPromises()

    expect(list.users.value.map(user => user.username)).toEqual(['beta_user'])
    expect(list.total.value).toBe(40)
    expect(list.page.value).toBe(1)
    expect(list.pending.value).toBe(false)

    // 旧的后回：必须被丢弃，不能把行和页码回退到上一代。
    pending[0]!.resolve({ items: [userRow(1, 'alpha_user')], total: 3, page: 2, perPage: 20 })
    await Promise.all([stale, fresh])
    await flushPromises()

    expect(list.users.value.map(user => user.username)).toEqual(['beta_user'])
    expect(list.total.value).toBe(40)
    expect(list.page.value).toBe(1)
    expect(list.pending.value).toBe(false)
  })

  test('does not paint a stale failure over the latest success', async () => {
    const { pending } = installGlobals()
    const errors: string[] = []
    const list = useAdminUserList({ onError: message => errors.push(message) })

    list.search.value = 'alpha'
    const stale = list.refreshUsers()
    list.search.value = 'beta'
    const fresh = list.applyFilters()
    await flushPromises()

    pending[1]!.resolve({ items: [userRow(2, 'beta_user')], total: 1, page: 1, perPage: 20 })
    await flushPromises()
    expect(list.forbidden.value).toBe(false)

    pending[0]!.reject({ code: 403, message: 'permission.denied' })
    await Promise.allSettled([stale, fresh])
    await flushPromises()

    // 旧请求的 403 属于上一代：它既不能点亮权限面板，也不能写页面级红条。
    expect(list.forbidden.value).toBe(false)
    expect(errors).toEqual([''])
    expect(list.users.value.map(user => user.username)).toEqual(['beta_user'])
  })

  test('keeps the page when the only request fails', async () => {
    const { pending } = installGlobals()
    const list = useAdminUserList({ onError: () => {} })

    list.page.value = 3
    const load = list.refreshUsers()
    await flushPromises()
    pending[0]!.reject({ code: 500, message: '服务暂时不可用。' })
    await load
    await flushPromises()

    // 失败分支不得改页码：否则分页器会把用户带到一个从未加载过的页。
    expect(list.page.value).toBe(3)
    expect(list.total.value).toBe(0)
    expect(list.users.value).toEqual([])
    expect(list.pending.value).toBe(false)
  })
})
