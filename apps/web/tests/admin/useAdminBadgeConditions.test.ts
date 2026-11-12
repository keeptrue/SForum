import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  factCandidatesInvariantBroken,
  normalizeConditionsPayload,
  useAdminBadgeConditions
} from '../../app/composables/admin/badges/useAdminBadgeConditions'
import type { BadgeConditionCatalogEntry } from '../../app/utils/admin/badgeRule'

/**
 * 条件目录的降级契约：端点可能还没上线（M1.4 前 404），失败必须是
 * 「空目录 + failed 标记」，不能抛错、不能阻塞页面；成功形状兼容 `{items}` 与裸数组。
 */
const entry: BadgeConditionCatalogEntry = {
  key: 'activity.count',
  valueKind: 'count',
  compare: ['gte'],
  labelZh: '计数',
  labelEn: 'Count'
}

describe('useAdminBadgeConditions', () => {
  test('成功：{ items } 与裸数组都接受', async () => {
    const wrapped = useAdminBadgeConditions(async () => ({ items: [entry] }))
    await wrapped.load()
    expect(wrapped.catalog.value).toEqual([entry])
    expect(wrapped.failed.value).toBe(false)
    expect(wrapped.loaded.value).toBe(true)

    const bare = useAdminBadgeConditions(async () => [entry])
    await bare.load()
    expect(bare.catalog.value).toEqual([entry])
    expect(bare.failed.value).toBe(false)
  })

  test('请求失败退化成空目录，不抛错', async () => {
    const failed = useAdminBadgeConditions(async () => {
      throw new Error('404')
    })
    await expect(failed.load()).resolves.toBeUndefined()
    expect(failed.catalog.value).toEqual([])
    expect(failed.failed.value).toBe(true)
  })

  test('形状不符也退化成空目录并标记失败', async () => {
    const broken = useAdminBadgeConditions(async () => ({ nope: true }))
    await broken.load()
    expect(broken.catalog.value).toEqual([])
    expect(broken.failed.value).toBe(true)
  })

  test('失败后重试成功可恢复', async () => {
    let offline = true
    const conditions = useAdminBadgeConditions(async () => {
      if (offline) throw new Error('boom')
      return { items: [entry] }
    })
    await conditions.load()
    expect(conditions.failed.value).toBe(true)

    offline = false
    await conditions.load()
    expect(conditions.failed.value).toBe(false)
    expect(conditions.catalog.value).toEqual([entry])
  })

  test('normalizeConditionsPayload 过滤没有 key 的脏项，无法识别时返回 null', () => {
    expect(normalizeConditionsPayload({ items: [entry, { nope: 1 }, null] })).toEqual([entry])
    expect(normalizeConditionsPayload('nope')).toBeNull()
  })

  test('params[].options 归一为字符串数组：非数组 → []，非字符串元素丢弃', () => {
    const dirty = {
      items: [{
        ...factCondition,
        params: [
          { name: 'factKey', kind: 'objectRef', required: true, refKind: 'fact', options: { value: 'a.b' } },
          { name: 'metric', kind: 'enum', required: true, options: ['topic_count', 7, null, 'comment_count'] },
          { name: 'window', kind: 'window', required: true }
        ]
      }]
    }
    const [normalized] = normalizeConditionsPayload(dirty)!
    const params = normalized!.params!
    // 对象型（或任何非数组）options 收敛成空数组，而不是把 undefined 塞进 <option>。
    expect(params[0]!.options).toEqual([])
    expect(params[1]!.options).toEqual(['topic_count', 'comment_count'])
    // 未声明 options 的字段保持原样：既不补空数组也不丢键。
    expect(params[2]).toEqual({ name: 'window', kind: 'window', required: true })
    // 其余字段一个不丢。
    expect(normalized!.key).toBe('fact.count')
    expect(normalized!.compare).toEqual(['gte'])
  })

  test('params 不是数组时归一为 []，整条条件仍然保留', () => {
    const [normalized] = normalizeConditionsPayload({ items: [{ ...entry, params: { metric: 'topic_count' } }] })!
    expect(normalized!.params).toEqual([])
    expect(normalized!.key).toBe('activity.count')
  })
})

describe('badges page shell wiring', () => {
  const shell = readFileSync(new URL('../../app/pages/admin/badges.vue', import.meta.url), 'utf8')
  const tab = readFileSync(
    new URL('../../app/components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue', import.meta.url),
    'utf8'
  )

  test('壳层只做接线，不在页面里写第二套双口径判断', () => {
    expect(shell).toContain('useAdminBadgeConditions')
    expect(shell).toContain('void badgeConditions.load()')
    expect(shell).toContain('catalog: badgeConditions.catalog.value')
    expect(shell).toContain('catalogFailed: badgeConditions.failed.value')
    expect(shell).not.toContain('ruleReady')
  })

  test('tab 把 catalog 原样传给弹窗', () => {
    expect(tab).toContain(':catalog="props.catalog"')
    expect(tab).toContain(':catalog-failed="props.catalogFailed"')
  })
})

const factCondition: BadgeConditionCatalogEntry = {
  key: 'fact.count',
  valueKind: 'count',
  compare: ['gte'],
  labelZh: '插件事实计数',
  labelEn: 'Plugin fact count',
  params: [
    // wire 形状：目录只下发取值（`conditionParamDTO.Options []string`）。
    { name: 'factKey', kind: 'objectRef', required: true, refKind: 'fact', options: ['a.b'] },
    { name: 'variant', kind: 'enum', required: false, options: [] }
  ]
}

describe('factCandidates 不变量：声称有候选就必须给候选', () => {
  test('目录里有 fact.count 但缺 factCandidates → 判候选失败（不静默成空候选）', async () => {
    const conditions = useAdminBadgeConditions(async () => ({ items: [factCondition] }))
    await conditions.load()
    expect(conditions.factCandidates.value).toEqual([])
    expect(conditions.factCandidatesFailed.value).toBe(true)
    // 目录本身没坏：编辑器照常可用。
    expect(conditions.failed.value).toBe(false)
    expect(conditions.catalog.value).toHaveLength(1)
  })

  test('给了空数组是合法的「有候选字段、暂时没候选」', async () => {
    const conditions = useAdminBadgeConditions(async () => ({ items: [factCondition], factCandidates: [] }))
    await conditions.load()
    expect(conditions.factCandidates.value).toEqual([])
    expect(conditions.factCandidatesFailed.value).toBe(false)
  })

  test('没有 fact.count 且两者同时缺失是安静的正常形态（老后端）', async () => {
    const conditions = useAdminBadgeConditions(async () => ({ items: [entry] }))
    await conditions.load()
    expect(conditions.factCandidatesFailed.value).toBe(false)
    expect(factCandidatesInvariantBroken([entry], undefined)).toBe(false)
  })

  test('字段存在但不是数组 → 失败', async () => {
    const conditions = useAdminBadgeConditions(async () => ({ items: [factCondition], factCandidates: { nope: 1 } }))
    await conditions.load()
    expect(conditions.factCandidates.value).toEqual([])
    expect(conditions.factCandidatesFailed.value).toBe(true)
    expect(factCandidatesInvariantBroken([factCondition], undefined)).toBe(true)
  })
})
