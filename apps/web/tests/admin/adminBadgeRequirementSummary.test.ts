import { describe, expect, test } from 'bun:test'
import { badgeRuleNeedsSummary, type BadgeRule } from '../../app/utils/admin/badgeRule'

/**
 * `badgeRuleNeedsSummary` 的真值表（本轮 5）。
 *
 * 判据必须**镜像服务端** `fastPathRuleForText`（`Models/Badges/requirement_text.go`）：
 * 只有「单 activity.count 叶子 + window.all + 无 days + gte + value>0 + 指标在服务端
 * 文案白名单里」这一种形状会被自动生成说明，其余形状公开面就没有解释。
 *
 * 所以两边都要验：服务端会生成的形状必须 `false`（别误报），服务端不会生成的必须
 * 在两侧都空时 `true`（别漏报）。只验「该提示的提示了」等于没验判据。
 */
function rule(node: BadgeRule['root']): BadgeRule {
  return { v: 1, root: node }
}

const autoLeaf = rule({
  type: 'activity.count',
  params: { metric: 'topic_count', window: { kind: 'all' } },
  cmp: { op: 'gte', value: 3 }
})

describe('badgeRuleNeedsSummary · 服务端会生成的形状 → false（不提示）', () => {
  test('single activity.count leaf with window.all and gte is described by the server', () => {
    expect(badgeRuleNeedsSummary(autoLeaf, { zh: '', en: '' })).toBe(false)
    expect(badgeRuleNeedsSummary(autoLeaf, null)).toBe(false)
    expect(badgeRuleNeedsSummary(autoLeaf)).toBe(false)
  })

  test('like_given_count is absent from the admin dropdown but the server still describes it', () => {
    const hiddenMetric = rule({
      type: 'activity.count',
      params: { metric: 'like_given_count', window: { kind: 'all' } },
      cmp: { op: 'gte', value: 10 }
    })
    expect(badgeRuleNeedsSummary(hiddenMetric, { zh: '', en: '' })).toBe(false)
  })
})

describe('badgeRuleNeedsSummary · 服务端不会生成的形状 → true（两侧都空时提示）', () => {
  const cases: Array<[string, BadgeRule]> = [
    ['two leaves in an all group', rule({
      op: 'all',
      children: [
        { type: 'activity.count', params: { metric: 'topic_count', window: { kind: 'all' } }, cmp: { op: 'gte', value: 3 } },
        { type: 'activity.count', params: { metric: 'comment_count', window: { kind: 'all' } }, cmp: { op: 'gte', value: 5 } }
      ]
    })],
    ['a not group', rule({
      op: 'not',
      children: [
        { type: 'activity.count', params: { metric: 'topic_count', window: { kind: 'all' } }, cmp: { op: 'gte', value: 3 } }
      ]
    })],
    ['single leaf with a recent_days window', rule({
      type: 'activity.count',
      params: { metric: 'topic_count', window: { kind: 'recent_days', days: 30 } },
      cmp: { op: 'gte', value: 3 }
    })],
    ['single leaf comparing with lte', rule({
      type: 'activity.count',
      params: { metric: 'topic_count', window: { kind: 'all' } },
      cmp: { op: 'lte', value: 3 }
    })],
    ['single leaf with a zero threshold', rule({
      type: 'activity.count',
      params: { metric: 'topic_count', window: { kind: 'all' } },
      cmp: { op: 'gte', value: 0 }
    })],
    ['single identity condition (no cmp, no window)', rule({
      type: 'identity.role',
      params: { roleKey: 'moderator' }
    })],
    ['window.all that still carries a days key', rule({
      type: 'activity.count',
      params: { metric: 'topic_count', window: { kind: 'all', days: 7 } },
      cmp: { op: 'gte', value: 3 }
    })],
    ['a metric the server has no label for', rule({
      type: 'activity.count',
      params: { metric: 'plugin.magic', window: { kind: 'all' } },
      cmp: { op: 'gte', value: 3 }
    })]
  ]

  for (const [label, value] of cases) {
    test(label, () => {
      expect(badgeRuleNeedsSummary(value, { zh: '', en: '' })).toBe(true)
    })
  }

  test('no rule at all (server has nothing to describe)', () => {
    expect(badgeRuleNeedsSummary(null, { zh: '', en: '' })).toBe(true)
    expect(badgeRuleNeedsSummary(undefined, { zh: '', en: '' })).toBe(true)
  })
})

describe('badgeRuleNeedsSummary · 已填文案就不提示（两侧都空才算缺）', () => {
  const needs = rule({
    op: 'all',
    children: [
      { type: 'activity.count', params: { metric: 'topic_count', window: { kind: 'all' } }, cmp: { op: 'gte', value: 3 } }
    ]
  })

  test('a Chinese summary is enough', () => {
    expect(badgeRuleNeedsSummary(needs, { zh: '连续发帖三篇', en: '' })).toBe(false)
  })

  test('an English-only summary is enough as well', () => {
    expect(badgeRuleNeedsSummary(needs, { zh: '', en: 'three topics' })).toBe(false)
  })

  test('whitespace-only text counts as empty', () => {
    expect(badgeRuleNeedsSummary(needs, { zh: '   ', en: '\n' })).toBe(true)
  })
})
