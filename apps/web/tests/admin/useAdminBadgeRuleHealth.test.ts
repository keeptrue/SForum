import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  normalizeRuleHealthPayload,
  useAdminBadgeRuleHealth,
  type BadgeRuleHealthRow
} from '../../app/composables/admin/badges/useAdminBadgeRuleHealth'

/**
 * 规则健康清单的契约：空 `items` 是「全站健康」（不是错误），失败一律降级为
 * 空清单 + `failed`，不抛错、不阻塞页面。
 */
const row: BadgeRuleHealthRow = {
  badgeId: 12,
  slug: 'veteran',
  name: '元老',
  issues: [
    { kind: 'missing_topic', path: 'root.children[0].params.topicId', ref: '123', label: '主题 #123' }
  ]
}

describe('useAdminBadgeRuleHealth', () => {
  test('成功：{ items } 与裸数组都接受', async () => {
    const wrapped = useAdminBadgeRuleHealth(async () => ({ items: [row] }))
    await wrapped.load()
    expect(wrapped.items.value).toEqual([row])
    expect(wrapped.failed.value).toBe(false)
    expect(wrapped.loaded.value).toBe(true)

    const bare = useAdminBadgeRuleHealth(async () => [row])
    await bare.load()
    expect(bare.items.value).toEqual([row])
  })

  test('空 items 是健康结果，不是失败', async () => {
    const healthy = useAdminBadgeRuleHealth(async () => ({ items: [] }))
    await healthy.load()
    expect(healthy.items.value).toEqual([])
    expect(healthy.failed.value).toBe(false)
  })

  test('请求失败退化成空清单，不抛错', async () => {
    const failed = useAdminBadgeRuleHealth(async () => {
      throw new Error('404')
    })
    await expect(failed.load()).resolves.toBeUndefined()
    expect(failed.items.value).toEqual([])
    expect(failed.failed.value).toBe(true)
  })

  test('形状不符也退化成空清单并标记失败', async () => {
    const broken = useAdminBadgeRuleHealth(async () => ({ nope: true }))
    await broken.load()
    expect(broken.items.value).toEqual([])
    expect(broken.failed.value).toBe(true)
  })

  test('失败后重试成功可恢复', async () => {
    let offline = true
    const health = useAdminBadgeRuleHealth(async () => {
      if (offline) throw new Error('boom')
      return { items: [row] }
    })
    await health.load()
    expect(health.failed.value).toBe(true)

    offline = false
    await health.load()
    expect(health.failed.value).toBe(false)
    expect(health.items.value).toEqual([row])
  })

  test('normalizeRuleHealthPayload 过滤没有 badgeId 的脏项', () => {
    expect(normalizeRuleHealthPayload({ items: [row, { slug: 'x' }, null] })).toEqual([row])
    expect(normalizeRuleHealthPayload('nope')).toBeNull()
  })
})

describe('badges shell health wiring', () => {
  const shell = readFileSync(new URL('../../app/pages/admin/badges.vue', import.meta.url), 'utf8')
  const tab = readFileSync(
    new URL('../../app/components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue', import.meta.url),
    'utf8'
  )

  test('壳层加载健康清单并只做接线', () => {
    expect(shell).toContain('useAdminBadgeRuleHealth')
    expect(shell).toContain('void badgeRuleHealth.load()')
    expect(shell).toContain('ruleHealth: badgeRuleHealth.items.value')
    expect(shell).toContain('ruleHealthFailed: badgeRuleHealth.failed.value')
  })

  test('tab 把健康清单原样传给弹窗', () => {
    expect(tab).toContain(':rule-health="props.ruleHealth"')
    expect(tab).toContain(':rule-health-failed="props.ruleHealthFailed"')
  })
})
