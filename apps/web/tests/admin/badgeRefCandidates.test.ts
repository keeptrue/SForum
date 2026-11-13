import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import type { BadgeConditionCatalogEntry } from '../../app/utils/admin/badgeRule'
import {
  BADGE_REF_KINDS,
  excludeBadgeSlug,
  normalizeRefCandidates,
  refCandidatesInvariantBroken,
  refKindGroup,
  refKindItems,
  refKindManual,
  refOptionLabel,
  type BadgeRefCandidateMap
} from '../../app/utils/admin/badgeRefCandidates'
import { useAdminBadgeConditions } from '../../app/composables/admin/badges/useAdminBadgeConditions'

/**
 * `objectRef` 候选（`refCandidates`）的读侧契约。
 *
 * 断言按**失败类**写：线上 bug 是「只有占位 option 的空下拉，值永远为空，保存必报缺参」，
 * 所以这里的每条都在锁「拿不到候选时一定有可输入/可选的路径」，而不是锁实现细节。
 */
const catalogWithRef: BadgeConditionCatalogEntry = {
  key: 'object.authoring',
  valueKind: 'count',
  compare: ['gte'],
  labelZh: '分类发文',
  labelEn: 'Topics in category',
  params: [
    { name: 'targetKind', kind: 'enum', required: true, options: ['category', 'tag'] },
    { name: 'targetSlug', kind: 'objectRef', required: true, refKind: 'category' }
  ]
}

const catalogWithoutRef: BadgeConditionCatalogEntry = {
  key: 'identity.email_verified',
  valueKind: 'boolean',
  compare: [],
  labelZh: '邮箱已验证',
  labelEn: 'Email verified'
}

describe('normalizeRefCandidates', () => {
  test('解析五类候选；缺失的 kind 补成「不可用」而不是「没有候选」', () => {
    const map = normalizeRefCandidates([
      {
        kind: 'category',
        candidates: [
          { value: 'news', label: '新闻', flags: [] },
          { value: 'old', label: '旧分类', flags: ['hidden', 'bogus'] }
        ],
        unavailable: false,
        truncated: true
      },
      { kind: 'badge', candidates: [{ value: 'first-post', label: '首帖', flags: ['disabled'] }] }
    ])
    expect(Object.keys(map).sort()).toEqual([...BADGE_REF_KINDS].sort())
    expect(map.category).toEqual({
      kind: 'category',
      candidates: [
        { value: 'news', label: '新闻', flags: [] },
        { value: 'old', label: '旧分类', flags: ['hidden'] }
      ],
      unavailable: false,
      truncated: true
    })
    expect(map.badge?.candidates[0]?.flags).toEqual(['disabled'])
    // 契约固定五项：缺席的三项只能是「取不到」，不能解释成「这一类没有候选」。
    for (const kind of ['tag', 'role', 'provider'] as const) {
      expect(map[kind]).toEqual({ kind, candidates: [], unavailable: true, truncated: false })
    }
  })

  test('整体字段缺失 / 非数组 → 空映射（读侧按每项不可用处理），不抛错', () => {
    expect(normalizeRefCandidates(undefined)).toEqual({})
    expect(normalizeRefCandidates(null)).toEqual({})
    expect(normalizeRefCandidates({ nope: 1 })).toEqual({})
    expect(normalizeRefCandidates('[]')).toEqual({})
  })

  test('坏行被丢掉：未知 kind、空 value、非对象候选', () => {
    const map = normalizeRefCandidates([
      { kind: 'fact', candidates: [{ value: 'a.b', label: 'x', flags: [] }] },
      { kind: 'tag', candidates: [{ value: '', label: '空', flags: [] }, null, { value: 'go', label: 'Go', flags: [] }] }
    ])
    // `fact` 不走这份候选，出现也只能当未知 kind 丢弃。
    expect(Object.keys(map)).not.toContain('fact')
    expect(map.tag?.candidates).toEqual([{ value: 'go', label: 'Go', flags: [] }])
  })

  test('label 为空时由显示层回落 value，规范化阶段不编造文案', () => {
    const map = normalizeRefCandidates([{ kind: 'role', candidates: [{ value: 'moderator', label: '' }] }])
    expect(map.role?.candidates[0]?.label).toBe('')
    expect(refKindItems(map, 'role', '')[0]?.label).toBe('moderator')
  })
})

describe('refKindManual / refKindItems', () => {
  const map: BadgeRefCandidateMap = {
    category: {
      kind: 'category',
      candidates: [
        { value: 'news', label: '新闻', flags: [] },
        { value: 'archive', label: '归档', flags: ['hidden'] }
      ],
      unavailable: false,
      truncated: false
    },
    tag: { kind: 'tag', candidates: [], unavailable: false, truncated: false },
    role: { kind: 'role', candidates: [], unavailable: true, truncated: false }
  }

  test('不可用与空列表都只能手填（两者给不出可选项，后果一样）', () => {
    expect(refKindManual(map, 'role')).toBe(true)
    expect(refKindManual(map, 'tag')).toBe(true)
    expect(refKindManual(map, 'category')).toBe(false)
    // 缺失 kind（`{}` 或老后端）同样只能手填。
    expect(refKindManual({}, 'category')).toBe(true)
    expect(refKindManual(undefined, 'category')).toBe(true)
    expect(refKindManual(map, undefined)).toBe(true)
  })

  test('refKindItems：有候选就列候选；不可用时返回空（此时走文本输入）', () => {
    expect(refKindItems(map, 'category', '').map(item => item.value)).toEqual(['news', 'archive'])
    expect(refKindItems(map, 'role', 'anything')).toEqual([])
    expect(refKindGroup(map, 'role').unavailable).toBe(true)
  })

  test('失效值追加为最后一项且不重复；原值保留可见', () => {
    const stale = refKindItems(map, 'category', 'legacy')
    expect(stale.map(item => item.value)).toEqual(['news', 'archive', 'legacy'])
    expect(stale[2]).toEqual({ value: 'legacy', label: 'legacy', flags: [], stale: true })
    // 当前值仍在候选里：不追加重复项。
    expect(refKindItems(map, 'category', 'news')).toHaveLength(2)
    // 空值与不可用都不产生失效项。
    expect(refKindItems(map, 'category', '')).toHaveLength(2)
  })

  test('显示文案：失效 / 已隐藏 / 已停用都只加后缀，选项仍然可选', () => {
    const markers = { stale: '（失效）', hidden: '（已隐藏）', disabled: '（已停用）' }
    expect(refOptionLabel({ value: 'news', label: '新闻', flags: [], stale: false }, markers)).toBe('新闻')
    expect(refOptionLabel({ value: 'archive', label: '归档', flags: ['hidden'], stale: false }, markers))
      .toBe('归档（已隐藏）')
    expect(refOptionLabel({ value: 'x', label: 'X', flags: ['hidden', 'disabled'], stale: true }, markers))
      .toBe('X（失效）（已隐藏）（已停用）')
  })
})

describe('excludeBadgeSlug：规则不能引用自身', () => {
  const map: BadgeRefCandidateMap = {
    badge: {
      kind: 'badge',
      candidates: [
        { value: 'self-badge', label: '自身', flags: [] },
        { value: 'other', label: '别的徽章', flags: [] }
      ],
      unavailable: false,
      truncated: false
    }
  }

  test('剔掉自身 slug，其余候选保持原样', () => {
    const filtered = excludeBadgeSlug(map, 'self-badge')
    expect(filtered.badge?.candidates.map(candidate => candidate.value)).toEqual(['other'])
    expect(map.badge?.candidates).toHaveLength(2)
  })

  test('只有自身一个候选时退成手填，不补占位项', () => {
    const onlySelf: BadgeRefCandidateMap = {
      badge: { kind: 'badge', candidates: [{ value: 'self-badge', label: '自身', flags: [] }], unavailable: false, truncated: false }
    }
    const filtered = excludeBadgeSlug(onlySelf, 'self-badge')
    expect(filtered.badge?.candidates).toEqual([])
    expect(refKindManual(filtered, 'badge')).toBe(true)
  })

  test('slug 为空 / 没有 badge 组时原样返回', () => {
    expect(excludeBadgeSlug(map, '')).toBe(map)
    expect(excludeBadgeSlug({}, 'self-badge')).toEqual({})
    expect(excludeBadgeSlug(undefined, 'self-badge')).toEqual({})
  })
})

describe('refCandidatesInvariantBroken：只作诊断，不升级硬失败', () => {
  test('目录声明 objectRef 且没有候选数组 → true（老后端必然命中）', () => {
    expect(refCandidatesInvariantBroken([catalogWithRef], undefined)).toBe(true)
    expect(refCandidatesInvariantBroken([catalogWithRef], { nope: 1 })).toBe(true)
  })

  test('给了数组、或目录根本没有 objectRef → false', () => {
    expect(refCandidatesInvariantBroken([catalogWithRef], [])).toBe(false)
    expect(refCandidatesInvariantBroken([catalogWithoutRef], undefined)).toBe(false)
    // `fact` 类 objectRef 不属于这份候选：不能因为它缺 refCandidates 就报警。
    expect(refCandidatesInvariantBroken([{
      ...catalogWithRef,
      params: [{ name: 'factKey', kind: 'objectRef', required: true, refKind: 'fact' }]
    }], undefined)).toBe(false)
  })
})

describe('useAdminBadgeConditions 暴露 refCandidates', () => {
  test('成功：解析 refCandidates，且不影响 factCandidates / failed 语义', async () => {
    const conditions = useAdminBadgeConditions(async () => ({
      items: [catalogWithRef],
      refCandidates: [
        { kind: 'category', candidates: [{ value: 'news', label: '新闻', flags: [] }], unavailable: false, truncated: false }
      ]
    }))
    await conditions.load()
    expect(conditions.failed.value).toBe(false)
    expect(conditions.factCandidatesFailed.value).toBe(false)
    expect(conditions.refCandidates.value.category?.candidates.map(candidate => candidate.value)).toEqual(['news'])
    expect(conditions.refCandidatesIncomplete.value).toBe(false)
  })

  test('老后端：目录有 objectRef 却没有 refCandidates → 空映射 + 诊断标记，不报失败', async () => {
    const conditions = useAdminBadgeConditions(async () => ({ items: [catalogWithRef] }))
    await conditions.load()
    expect(conditions.refCandidates.value).toEqual({})
    expect(conditions.refCandidatesIncomplete.value).toBe(true)
    // 诊断不等于失败：目录可用、保存流程不受影响。
    expect(conditions.failed.value).toBe(false)
    expect(conditions.catalog.value).toHaveLength(1)
  })

  test('请求失败：空映射，failed 语义不变', async () => {
    const conditions = useAdminBadgeConditions(async () => {
      throw new Error('404')
    })
    await conditions.load()
    expect(conditions.refCandidates.value).toEqual({})
    expect(conditions.refCandidatesIncomplete.value).toBe(false)
    expect(conditions.failed.value).toBe(true)
  })

  test('壳层把 refCandidates 一路带到规则编辑器（单 prop，不经第二套状态）', () => {
    const shell = readFileSync(new URL('../../app/pages/admin/badges.vue', import.meta.url), 'utf8')
    const tab = readFileSync(
      new URL('../../app/components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue', import.meta.url),
      'utf8'
    )
    expect(shell).toContain('refCandidates: badgeConditions.refCandidates.value')
    expect(tab).toContain(':ref-candidates="props.refCandidates"')
    // 组件与组合式函数之间没有第二份状态：`refCandidates` 只由 composable 持有。
    expect(typeof useAdminBadgeConditions).toBe('function')
  })
})
