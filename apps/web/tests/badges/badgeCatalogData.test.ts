import { beforeEach, describe, expect, test } from 'bun:test'
import { computed, ref } from 'vue'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

/**
 * `/badges` 数据层契约（M5b）：wire → ProfileBadgeGroup 的映射、truncated、
 * 以及「点重试 = 重新发请求」（B-25 要求的第二次请求可观测）。
 *
 * 源文件用 Nuxt 自动导入（`useAsyncData` / `useApiClient` / `useI18n` / `computed` / `ref`
 * 都不 import），所以这里注入到 globalThis，再动态 import 真实模块——测的是生产代码，不是替身。
 */
const messages = JSON.parse(
  readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

function t(key: string, params: Record<string, unknown> = {}) {
  const template = lookup(key)
  if (typeof template !== 'string') throw new Error(`missing locale key: ${key}`)
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

let requestPaths: string[] = []
let payload: unknown = { groups: [], truncated: false }
let failNext = false

const globals = globalThis as unknown as Record<string, unknown>
globals.computed = computed
globals.ref = ref
globals.useI18n = () => ({ t })
globals.useApiClient = () => ({
  request: async (path: string) => {
    requestPaths.push(path)
    if (failNext) throw new Error('catalog request failed')
    return payload
  }
})
globals.useAsyncData = async (
  _key: string,
  fetcher: () => Promise<unknown>,
  options: { default: () => unknown }
) => {
  const data = ref(options.default())
  const pending = ref(false)
  const error = ref<Error | null>(null)
  const run = async () => {
    pending.value = true
    try {
      data.value = await fetcher()
    } catch (caught) {
      error.value = caught as Error
    } finally {
      pending.value = false
    }
  }
  await run()
  return { data, pending, error, refresh: run }
}

const { useBadgesCatalog } = await import('../../app/composables/forum/badges/useBadgesCatalog')

const catalogItem = (overrides: Record<string, unknown> = {}) => ({
  slug: 'first-post',
  name: '初次发帖',
  description: '发第一帖',
  icon: 'i-lucide-zap',
  tier: 'bronze',
  group: 'content',
  earned: false,
  ...overrides
})

beforeEach(() => {
  requestPaths = []
  failNext = false
  payload = { groups: [], truncated: false }
})

describe('useBadgesCatalog wire mapping', () => {
  test('keeps the server order as position and never invents progress fields', async () => {
    payload = {
      groups: [
        { key: 'activity', name: '活跃', badges: [catalogItem({ slug: 'a', earned: true }), catalogItem({ slug: 'b' })] },
        { key: '', name: '', badges: [catalogItem({ slug: 'c' })] }
      ],
      truncated: true
    }

    const { groups, truncated } = await useBadgesCatalog()

    expect(requestPaths).toEqual(['/badges'])
    expect(groups.value.map(group => group.key)).toEqual(['activity', ''])
    expect(groups.value.map(group => group.position)).toEqual([0, 1])
    expect(groups.value[1]!.name).toBe('未分组')
    expect(groups.value[0]!.badges.map(badge => badge.position)).toEqual([0, 1])
    expect(groups.value[0]!.badges[0]!.earned).toBe(true)
    // D37 白名单：wire 上没有阈值与授予时间，映射后也不许凭空长出来。
    expect('threshold' in groups.value[0]!.badges[1]!).toBe(false)
    expect('current' in groups.value[0]!.badges[1]!).toBe(false)
    expect('grantedAt' in groups.value[0]!.badges[0]!).toBe(false)
    expect(truncated.value).toBe(true)
  })

  test('an empty catalog stays an empty array, not null', async () => {
    const { groups, truncated } = await useBadgesCatalog()
    expect(groups.value).toEqual([])
    expect(truncated.value).toBe(false)
  })

  test('refresh re-issues the request, so the retry button can be observed as a second call', async () => {
    const catalog = await useBadgesCatalog()
    expect(requestPaths).toHaveLength(1)

    await catalog.refresh()
    expect(requestPaths).toHaveLength(2)

    await catalog.refresh()
    expect(requestPaths).toHaveLength(3)
  })

  test('a failed request surfaces as error instead of a fake empty catalog', async () => {
    failNext = true
    const { groups, error } = await useBadgesCatalog()
    expect(error.value).toBeInstanceOf(Error)
    expect(groups.value).toEqual([])
  })
})

describe('/badges island wiring', () => {
  const island = () => readFileSync(
    join(new URL('../../app/', import.meta.url).pathname, 'components/forum/badges/SFBadgeCatalogPage.vue'),
    'utf8'
  )

  test('routes retry to the composable refresh and announces truncation', () => {
    const source = island()
    expect(source).toContain('@retry="refresh"')
    expect(source).toContain("t('badges.catalog.truncated')")
    expect(source).toContain('role="status"')
    // 缓存头归 backend（D37）；前端一行都不写。
    expect(source).not.toContain('Cache-Control')
  })
})
