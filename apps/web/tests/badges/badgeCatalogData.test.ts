import { beforeEach, describe, expect, test } from 'bun:test'
import { computed, ref } from 'vue'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { forumBadgeHasProgress, forumBadgeRequirementText } from '~/utils/forum/forumBadge'

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
  test('keeps the server order as position and stays silent about progress the wire did not send', async () => {
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
    // 缺键 = 不可测：映射层不许补 0，否则卡片会画出 0/N 的空进度。
    expect('threshold' in groups.value[0]!.badges[1]!).toBe(false)
    expect('current' in groups.value[0]!.badges[1]!).toBe(false)
    expect('grantedAt' in groups.value[0]!.badges[0]!).toBe(false)
    expect(truncated.value).toBe(true)
  })

  test('carries rule-engine progress for unearned measurable badges (value-level, not key-level)', async () => {
    payload = {
      groups: [
        {
          key: 'content',
          name: '内容',
          badges: [
            catalogItem({ slug: 'first-post', current: 3, threshold: 10 }),
            catalogItem({ slug: 'zero-progress', current: 0, threshold: 5 })
          ]
        }
      ],
      truncated: false
    }

    const { groups } = await useBadgesCatalog()
    const measurable = groups.value[0]!.badges[0]!
    const zero = groups.value[0]!.badges[1]!

    // current=0 是有效值（后端用指针 + omitempty 保住它），不能被 `||` 吃掉变成缺键。
    expect(measurable.current).toBe(3)
    expect(measurable.threshold).toBe(10)
    expect(zero.current).toBe(0)
    expect(zero.threshold).toBe(5)
    expect(forumBadgeHasProgress(measurable)).toBe(true)
    expect(forumBadgeHasProgress(zero)).toBe(true)
  })

  test('drops a threshold of 0 and refuses progress on earned rows', async () => {
    payload = {
      groups: [
        {
          key: 'content',
          name: '内容',
          badges: [
            catalogItem({ slug: 'zero-threshold', current: 4, threshold: 0 }),
            // 防御性：即使服务端给已获得行塞了进度，映射后也不能让卡片画进度条。
            catalogItem({ slug: 'already-earned', earned: true, current: 9, threshold: 9 })
          ]
        }
      ],
      truncated: false
    }

    const { groups } = await useBadgesCatalog()
    const [zeroThreshold, earned] = groups.value[0]!.badges

    expect('threshold' in zeroThreshold!).toBe(false)
    expect('current' in zeroThreshold!).toBe(false)
    expect(forumBadgeHasProgress(zeroThreshold!)).toBe(false)
    expect(forumBadgeHasProgress(earned!)).toBe(false)
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

describe('useBadgesCatalog · requirementText', () => {
  test('carries the server sentence through with both languages non-empty', async () => {
    const requirementText = { zh: '在任意版块发布 10 个主题', en: 'Publish 10 topics in any category' }
    payload = {
      groups: [
        { key: 'content', name: '内容', badges: [catalogItem({ requirementText })] }
      ],
      truncated: false
    }

    const { groups } = await useBadgesCatalog()
    const mapped = groups.value[0]!.badges[0]!

    // 值级断言：DTO 无 omitempty，漏赋值会得到 { zh: '', en: '' } —— 只断言键存在抓不到。
    expect(mapped.requirementText).toEqual(requirementText)
    expect(mapped.requirementText?.zh).not.toBe('')
    expect(mapped.requirementText?.en).not.toBe('')
    // 两种语言各自可达（映射没把某一语言吃掉）。
    expect(forumBadgeRequirementText(mapped, 'zh-CN')).toBe(requirementText.zh)
    expect(forumBadgeRequirementText(mapped, 'en-US')).toBe(requirementText.en)
  })

  test('a multi-condition badge keeps the empty pair empty instead of inventing copy', async () => {
    payload = {
      groups: [
        { key: 'content', name: '内容', badges: [catalogItem({ requirementText: { zh: '', en: '' } })] }
      ],
      truncated: false
    }

    const { groups } = await useBadgesCatalog()
    const mapped = groups.value[0]!.badges[0]!

    expect(mapped.requirementText).toEqual({ zh: '', en: '' })
    expect(forumBadgeRequirementText(mapped, 'zh-CN')).toBe('')
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
