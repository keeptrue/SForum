import { beforeEach, describe, expect, test } from 'bun:test'
import { ref } from 'vue'

/**
 * `recompute()` 的返回口径（4-3 的根因就在这层）。
 *
 * 后端返回 `{ granted, refreshed, queued }`，三个数语义不同：granted = 新插入授予行数，
 * refreshed = top badge 变化的用户数，queued = 转入异步评估的徽章数。这里钉两件事：
 * 1) `queued` 必须活着到调用方（曾被整段丢掉）；
 * 2) 三个数不许相加（相加会把「后台还在跑 9 枚」抬成「影响了 9 个用户」这种假数字）。
 *
 * 源文件用 Nuxt 自动导入（`ref` / `useApiClient` 都不 import），所以注入到 globalThis
 * 再动态 import 真实模块——测的是生产代码。
 */
let requests: Array<{ path: string, method?: string }> = []
let recomputePayload: unknown = { granted: 0, refreshed: 0, queued: 0 }

const globals = globalThis as unknown as Record<string, unknown>
globals.ref = ref
globals.useApiClient = () => ({
  request: async (path: string, options?: { method?: string }) => {
    requests.push({ path, method: options?.method })
    if (path === '/admin/badges/recompute') return recomputePayload
    // recompute 之后会重新 load 列表，这里给空集合即可。
    if (path === '/admin/badges') return { items: [] }
    if (path === '/admin/badges/groups') return { items: [] }
    if (path === '/admin/badges/tiers') return { items: [], colorChoices: [] }
    return {}
  }
})

const { useAdminBadges } = await import('../../app/composables/admin/badges/useAdminBadges')

beforeEach(() => {
  requests = []
  recomputePayload = { granted: 0, refreshed: 0, queued: 0 }
})

describe('useAdminBadges · recompute result', () => {
  test('returns the three wire numbers as an object instead of a summed total', async () => {
    recomputePayload = { granted: 3, refreshed: 4, queued: 9 }
    const store = useAdminBadges()

    const result = await store.recompute()

    expect(result).toEqual({ granted: 3, refreshed: 4, queued: 9 })
    // 相加的反面证据：7 是 granted+refreshed，13 是三项之和，都不能出现在返回值里。
    expect(Object.values(result)).not.toContain(7)
    expect(Object.values(result)).not.toContain(13)
    expect(requests.filter(entry => entry.path === '/admin/badges/recompute')).toEqual([
      { path: '/admin/badges/recompute', method: 'POST' }
    ])
    // 重算后必须刷新列表（否则授予人数还是旧值）。
    expect(requests.some(entry => entry.path === '/admin/badges' && entry.method === undefined)).toBe(true)
  })

  test('a queued-only response keeps queued and leaves the affected counters at zero', async () => {
    recomputePayload = { granted: 0, refreshed: 0, queued: 5 }
    const store = useAdminBadges()

    expect(await store.recompute()).toEqual({ granted: 0, refreshed: 0, queued: 5 })
  })

  test('a missing queued field normalizes to 0 instead of NaN', async () => {
    recomputePayload = { granted: 2, refreshed: 0 }
    const store = useAdminBadges()

    expect(await store.recompute()).toEqual({ granted: 2, refreshed: 0, queued: 0 })
  })
})
