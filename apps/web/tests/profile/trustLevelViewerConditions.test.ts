import { describe, expect, test } from 'bun:test'
import {
  TRUST_LEVEL_METRIC_KEYS,
  parseViewerConditions,
  trustLevelNextLevel,
  trustLevelRequirementRows,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'

/**
 * 详情弹窗的数据层：三个纯函数，全部可离线断言。
 *
 * - `parseViewerConditions`：`viewer.conditions` 的宽容解析（坏条目丢弃，不让整块消失）；
 * - `trustLevelRequirementRows`：下一档门槛清单（只留 `> 0`，顺序跟 `TRUST_LEVEL_METRIC_KEYS`）；
 * - `trustLevelNextLevel`：下一档 rank（后端值优先，越界回落阶梯邻位，满级为 null）。
 *
 * 这三条是「他人页有门槛、无进度」「满级不画进度」「0 门槛不渲染区块」三处渲染行为的
 * 唯一输入，所以它们自己必须先钉死。
 */

describe('parseViewerConditions', () => {
  test('keeps entries with a boolean satisfied and at least one non-empty text side', () => {
    const conditions = parseViewerConditions([
      { satisfied: true, text: { zh: '已发布 3 个主题', en: 'Published 3 topics' } },
      { satisfied: false, text: { zh: '获得 10 个赞', en: 'Received 10 likes' } }
    ])
    expect(conditions).toEqual([
      { satisfied: true, text: { zh: '已发布 3 个主题', en: 'Published 3 topics' } },
      { satisfied: false, text: { zh: '获得 10 个赞', en: 'Received 10 likes' } }
    ])
  })

  test('keeps a one-sided sentence instead of dropping the row', () => {
    // 运营只填了中文名（或只有英文）时，另一侧为空是合法数据，不是坏条目。
    expect(parseViewerConditions([{ satisfied: true, text: { zh: '只填了中文' } }]))
      .toEqual([{ satisfied: true, text: { zh: '只填了中文', en: '' } }])
    expect(parseViewerConditions([{ satisfied: false, text: { en: 'English only' } }]))
      .toEqual([{ satisfied: false, text: { zh: '', en: 'English only' } }])
  })

  test('drops malformed entries instead of failing the whole block', () => {
    const conditions = parseViewerConditions([
      { satisfied: 'true', text: { zh: '不是布尔' } },
      { text: { zh: '缺 satisfied' } },
      { satisfied: true, text: { zh: '   ', en: '  ' } },
      { satisfied: true },
      { satisfied: false, text: { zh: '保留这条', en: 'kept' } },
      null,
      'nope',
      42
    ])
    expect(conditions).toEqual([{ satisfied: false, text: { zh: '保留这条', en: 'kept' } }])
  })

  test('non-array payloads yield an empty list', () => {
    // 他人视角根本不带这个字段（undefined），读面失败时也可能是 null。
    for (const raw of [undefined, null, {}, 'x', 0]) {
      expect(parseViewerConditions(raw)).toEqual([])
    }
  })
})

describe('trustLevelRequirementRows', () => {
  test('orders rows by TRUST_LEVEL_METRIC_KEYS and drops zero thresholds', () => {
    // 对象字面量的键序是「怎么写的」，清单顺序必须是「Go Metrics 的顺序」。
    const tier: TrustLevelTier = {
      id: 'veteran',
      name: { zh: '资深用户', en: 'Veteran' },
      icon: 'mountain',
      requirements: {
        bookmark_count: 5,
        like_received_count: 30,
        topic_count: 0,
        days_visited: 45,
        like_given_count: 20,
        comment_count: 80
      },
      retired: false
    }
    const rows = trustLevelRequirementRows(tier)
    // 5 行（topic_count = 0 不参与判定），顺序 = TRUST_LEVEL_METRIC_KEYS 的子序列。
    expect(rows).toEqual([
      { key: 'daysVisited', threshold: 45 },
      { key: 'commentCount', threshold: 80 },
      { key: 'likeReceived', threshold: 30 },
      { key: 'likeGiven', threshold: 20 },
      { key: 'bookmarkCount', threshold: 5 }
    ])
    const order = rows.map(row => TRUST_LEVEL_METRIC_KEYS.indexOf(row.key))
    expect(order).toEqual([...order].sort((a, b) => a - b))
  })

  test('drops unknown snake_case keys instead of inventing an i18n key', () => {
    // `account_age_days` 不是判定指标：留在清单里就会渲染成一个没有文案的指标名。
    const rows = trustLevelRequirementRows({
      id: 'member',
      name: { zh: '正式用户', en: 'Member' },
      icon: null,
      requirements: { account_age_days: 7, days_visited: 3, comment_count: 5 },
      retired: false
    })
    expect(rows).toEqual([
      { key: 'daysVisited', threshold: 3 },
      { key: 'commentCount', threshold: 5 }
    ])
  })

  test('a tier whose thresholds are all zero has no rows at all', () => {
    // 规范 §8：门槛区块整块不渲染，不写「没有门槛」之类的解释文案。
    expect(trustLevelRequirementRows({
      id: 'member',
      name: { zh: '正式用户', en: 'Member' },
      icon: null,
      requirements: { days_visited: 0, topic_count: 0, comment_count: 0 },
      retired: false
    })).toEqual([])
    expect(trustLevelRequirementRows({ id: 'x', name: { zh: '', en: '' }, icon: null, retired: false })).toEqual([])
    expect(trustLevelRequirementRows(null)).toEqual([])
    expect(trustLevelRequirementRows(undefined)).toEqual([])
  })

  test('negative and non-numeric thresholds are dropped, not rendered as "-3"', () => {
    const rows = trustLevelRequirementRows({
      id: 'member',
      name: { zh: '正式用户', en: 'Member' },
      icon: null,
      requirements: { days_visited: -3, topic_count: Number.NaN, comment_count: 5 },
      retired: false
    })
    expect(rows).toEqual([{ key: 'commentCount', threshold: 5 }])
  })
})

describe('trustLevelNextLevel', () => {
  test('prefers the viewer next level when it is strictly ahead and inside the ladder', () => {
    // 本人视角：后端 `viewer.nextLevel` 是权威值（它跟着配置的 LastRank 走，不是写死的 +1）。
    expect(trustLevelNextLevel(2, 4, 3)).toBe(3)
    expect(trustLevelNextLevel(5, 8, 6)).toBe(6)
  })

  test('falls back to the ladder neighbour for other profiles and a missing viewer', () => {
    // 他人视角没有 nextLevel：门槛照常显示，靠阶梯邻位推算（规范 §7）。
    expect(trustLevelNextLevel(2, 4)).toBe(3)
    expect(trustLevelNextLevel(0, 4, undefined)).toBe(1)
    expect(trustLevelNextLevel(3, 4, null)).toBe(4)
  })

  test('returns null at the top of the ladder', () => {
    expect(trustLevelNextLevel(4, 4)).toBeNull()
    expect(trustLevelNextLevel(4, 4, 5)).toBeNull()
    expect(trustLevelNextLevel(0, 0)).toBeNull()
  })

  test('an out-of-range viewer value never becomes the next level', () => {
    // 读面来自缓存/上一个账号时可能越界或倒退：宁可回落阶梯，也不显示「下一档是 TL0」。
    expect(trustLevelNextLevel(2, 4, 9)).toBe(3)
    expect(trustLevelNextLevel(2, 4, 2)).toBe(3)
    expect(trustLevelNextLevel(2, 4, 0)).toBe(3)
    expect(trustLevelNextLevel(2, 4, Number.NaN)).toBe(3)
    expect(trustLevelNextLevel(2, 4, '4')).toBe(4)
  })
})
