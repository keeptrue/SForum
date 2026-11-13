import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  contributionEnabled,
  contributionMovePlan,
  contributionOverridden,
  contributionRowKey,
  contributionsForPoint,
  effectiveOrderOf,
  isTabSurfacePoint,
  sortContributions
} from '../../app/utils/admin/adminContributions'
import type { AdminEffectiveContribution } from '../../app/utils/admin/adminExtensions'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
const governance = () => source('../../app/composables/admin/useAdminContributionGovernance.ts')
const page = () => source('../../app/pages/admin/extensions/contributions.vue')

function contribution(overrides: Partial<AdminEffectiveContribution> = {}): AdminEffectiveContribution {
  return {
    extensionId: 'demo',
    extensionName: 'Demo',
    extensionType: 'plugin',
    point: 'admin.settings.tab',
    id: 'alpha',
    order: 10,
    ...overrides
  }
}

describe('contribution ordering plan', () => {
  const items = [
    contribution({ id: 'a', order: 10 }),
    contribution({ id: 'b', order: 20 }),
    contribution({ id: 'c', order: 30 })
  ]

  test('sorts by effective order, falling back to the manifest order', () => {
    expect(sortContributions([
      contribution({ id: 'y', order: 5 }),
      contribution({ id: 'x', order: 1, effectiveSortOrder: 99 })
    ]).map(item => item.id)).toEqual(['y', 'x'])
    expect(effectiveOrderOf(contribution({ order: 7 }))).toBe(7)
    expect(effectiveOrderOf(contribution({ order: 7, effectiveSortOrder: 2 }))).toBe(2)
  })

  test('scopes the plan to one extension point', () => {
    const mixed = [...items, contribution({ id: 'z', point: 'public.navigation', order: 1 })]
    expect(contributionsForPoint(mixed, 'admin.settings.tab').map(item => item.id)).toEqual(['a', 'b', 'c'])
    expect(contributionMovePlan(mixed, 'admin.settings.tab', 'b', -1).ok).toBe(true)
    expect(contributionMovePlan(mixed, 'admin.settings.tab', 'nope', -1)).toEqual({ ok: false, reason: 'boundary' })
  })

  test('swapping two distinct orders produces exactly two updates', () => {
    const plan = contributionMovePlan(items, 'admin.settings.tab', 'c', -1)
    expect(plan).toEqual({
      ok: true,
      updates: [
        { id: 'c', sortOrder: 20 },
        { id: 'b', sortOrder: 30 }
      ]
    })
  })

  test('ties renumber the whole point instead of swapping equal values', () => {
    const tied = [contribution({ id: 'a' }), contribution({ id: 'b' }), contribution({ id: 'c' })]
    const plan = contributionMovePlan(tied, 'admin.settings.tab', 'a', 1)
    expect(plan.ok).toBe(true)
    if (!plan.ok) return
    expect(plan.updates.map(entry => [entry.id, entry.sortOrder])).toEqual([
      ['a', 1],
      ['b', 2],
      ['c', 3]
    ])
  })

  test('refuses to move past either edge', () => {
    expect(contributionMovePlan(items, 'admin.settings.tab', 'a', -1)).toEqual({ ok: false, reason: 'boundary' })
    expect(contributionMovePlan(items, 'admin.settings.tab', 'c', 1)).toEqual({ ok: false, reason: 'boundary' })
  })

  test('governance state reads defaults as enabled and unoverridden', () => {
    expect(contributionEnabled(contribution())).toBe(true)
    expect(contributionEnabled(contribution({ enabled: false }))).toBe(false)
    expect(contributionOverridden(contribution())).toBe(false)
    expect(contributionOverridden(contribution({ overridden: true }))).toBe(true)
    expect(contributionRowKey('admin.settings.tab', 'a')).toBe('admin.settings.tab:a')
  })

  test('read model names: section and per-field order are both effectiveSortOrder', () => {
    const src = source('../../app/utils/admin/adminExtensions.ts')
    const block = src.match(/export type AdminEffectiveContribution = \{[\s\S]*?\n\}/)?.[0] ?? ''
    expect(block).toBeTruthy()
    // 读模型只有 effectiveSortOrder；裸 sortOrder 只允许出现在 wire 请求体里。
    expect(block).toContain('effectiveSortOrder?: number')
    expect(block).not.toMatch(/\bsortOrder\?:/)
    // item 覆盖的字段键与 settingsSections 同一把键，且只有一个名字。
    expect(block).toMatch(/fields\?: Array<\{ fieldKey: string, enabled\?: boolean, effectiveSortOrder\?: number, overridden\?: boolean \}>/)
    expect(block).not.toMatch(/fields\?: Array<\{ key:/)
  })

  test('tab-bearing points are recognised so the disable confirm can warn about tabs', () => {
    expect(isTabSurfacePoint('admin.settings.tab')).toBe(true)
    expect(isTabSurfacePoint('profile.settings.section')).toBe(true)
    expect(isTabSurfacePoint('public.navigation')).toBe(false)
    expect(isTabSurfacePoint('admin.dashboard.widget')).toBe(false)
  })
})

describe('governance request bodies and rollback', () => {
  test('enable/disable PUTs only the enabled flag on the namespaced contribution path', () => {
    const src = governance()
    expect(src).toContain('`/admin/extensions/contributions/${encodeURIComponent(item.point)}/${encodeURIComponent(item.id)}`')
    expect(src).toContain("{ method: 'PUT', body: { enabled } }")
  })

  test('reordering PUTs one sortOrder per changed item (wire name, not the read model)', () => {
    const src = governance()
    expect(src).toContain("{ method: 'PUT', body: { sortOrder: update.sortOrder } }")
    expect(src).toContain('await Promise.all(plan.updates.map(update => request(')
    // wire 只叫 sortOrder；effectiveSortOrder 是读模型字段，不进任何请求体。
    expect(src).not.toMatch(/body:\s*\{[^}]*effectiveSortOrder/)
  })

  test('the sort plan entry is exactly { id, sortOrder } — nothing read-model-shaped leaks out', () => {
    const plan = contributionMovePlan(
      [contribution({ id: 'a', order: 10 }), contribution({ id: 'b', order: 20 })],
      'admin.settings.tab',
      'b',
      -1
    )
    expect(plan.ok).toBe(true)
    if (!plan.ok) return
    expect(plan.updates.map(entry => Object.keys(entry).sort())).toEqual([['id', 'sortOrder'], ['id', 'sortOrder']])
    expect(plan.updates).toEqual([{ id: 'b', sortOrder: 10 }, { id: 'a', sortOrder: 20 }])
  })

  test('optimistic update writes the wire value back into the read model field', () => {
    // 请求发 sortOrder，界面读 effectiveSortOrder：这两条映射必须显式存在。
    const src = governance()
    expect(src).toContain('const nextOrder = new Map(plan.updates.map(entry => [entry.id, entry.sortOrder]))')
    expect(src).toContain('{ ...current, effectiveSortOrder: nextOrder.get(current.id), overridden: true }')
  })

  test('restore-default DELETEs the override instead of deleting the contribution', () => {
    const src = governance()
    expect(src).toContain("{ method: 'DELETE' }")
    expect(src).toContain('replace(item, { enabled: true, overridden: false, effectiveSortOrder: item.order })')
    expect(src).not.toContain("method: 'DELETE', body")
  })

  test('optimistic updates roll back the whole list and mark the row on failure', () => {
    const src = governance()
    expect(src.match(/options\.contributions\.value = previous/g)).toHaveLength(3)
    expect(src).toContain('setRowError(key, apiErrorMessage(error) ||')
    expect(src).toContain('rowErrors.value = { ...rowErrors.value, [key]: message }')
    // 成功才弹 Toast，且 5s 自动消失
    expect(src).toContain('duration: 5000')
  })
})

describe('contributions page governance column', () => {
  test('is gated behind extension.plugin.manage', () => {
    const src = page()
    expect(src).toContain("can('extension.plugin.manage')")
    expect(src).toContain('v-if="canGovern"')
  })

  test('warns that a tab disappears for everyone before disabling', () => {
    const src = governance()
    expect(src).toContain("t('admin.extensions.contributions.governance.disableTabConfirm')")
    expect(src).toContain("t('admin.extensions.contributions.governance.disableConfirm')")
    const zh = JSON.parse(source('../../i18n/locales/zh-CN.json'))
    expect(zh.admin.extensions.contributions.governance.disableTabConfirm).toContain('插件 tab 将对所有人隐藏')
  })

  test('failures stay inline on the row, edges disable the move buttons', () => {
    const src = page()
    expect(src).toContain('v-if="rowErrors[rowKey(contribution)]"')
    expect(src).toContain('role="alert"')
    expect(src).toContain(':disabled="!canMove(contribution, -1)')
    expect(src).toContain(':disabled="!canMove(contribution, 1)')
    expect(src).toContain('@click="onMove(contribution, -1)"')
    expect(src).toContain('@update:model-value="onToggle(contribution, $event)"')
  })
})
