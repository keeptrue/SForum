import { describe, expect, test } from 'bun:test'
import {
  anyTierRuleDirty,
  catalogUsabilityFor,
  decodeRuleDocument,
  firstRuleErrorTierId,
  ruleLeaves,
  type RuleCatalogState
} from '../../../app/composables/admin/forum/trustLevels/useTrustLevelRuleDraft'
import type { TrustLevelSaveError } from '../../../app/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'

/**
 * D21 保存闸门与面板判据的纯逻辑契约。
 *
 * 这些函数给两个消费者用（保存按钮 + 面板失败态），所以每条判据都在这里钉死一次：
 * 判据漂移时测试先红，而不是等运营在目录挂掉时发现保存被莫名禁用（或更糟：静默写坏了规则）。
 */

const catalogEntry = (key: string, refKind?: string) => ({
  key,
  valueKind: 'count' as const,
  compare: ['gte', 'lte'] as const,
  labelZh: key,
  labelEn: key,
  params: refKind
    ? [{ name: 'slug', kind: 'objectRef' as const, required: true, refKind }]
    : []
})

const readyCatalog = (overrides: Partial<RuleCatalogState> = {}): RuleCatalogState => ({
  status: 'ready',
  items: [catalogEntry('activity.count'), catalogEntry('fact.count'), catalogEntry('badge.owned', 'badge')],
  factCandidates: [{ key: 'acme.tasks', labelZh: '任务', labelEn: 'Tasks', unitZh: '', unitEn: '', compare: ['gte'], variants: [] }],
  refCandidates: { badge: { kind: 'badge', candidates: [{ value: 'a', label: 'A', flags: [] }], unavailable: false, truncated: false } },
  ...overrides
})

const rule = (type: string) => ({ v: 1, root: { type, params: {}, cmp: { op: 'gte', value: 3 } } })

describe('decodeRuleDocument：只认 {v:1, root}', () => {
  test('合法文档原样返回（不重建、不丢键）', () => {
    const doc = { v: 1, root: { op: 'all', children: [{ type: 'activity.count', extra: '保留' }] }, unknownTop: true }
    expect(decodeRuleDocument(doc)).toBe(doc as never)
  })

  test('版本不符 / 缺 root / 根形状不认识 → null（调用方据此走空态，不静默覆盖）', () => {
    expect(decodeRuleDocument(null)).toBeNull()
    expect(decodeRuleDocument(undefined)).toBeNull()
    expect(decodeRuleDocument({ v: 2, root: { type: 'a' } })).toBeNull()
    expect(decodeRuleDocument({ v: 1 })).toBeNull()
    expect(decodeRuleDocument({ v: 1, root: {} })).toBeNull()
    expect(decodeRuleDocument({ v: 1, root: { type: 'activity.count' } })).not.toBeNull()
    expect(decodeRuleDocument({ v: 1, root: { op: 'all', children: [] } })).not.toBeNull()
  })
})

describe('ruleLeaves：宽容遍历（脏规则也要数得出来）', () => {
  test('组/叶子/裸根都能展开', () => {
    expect(ruleLeaves(rule('activity.count'))).toHaveLength(1)
    expect(ruleLeaves({ v: 1, root: { op: 'all', children: [rule('a').root, { op: 'not', children: [rule('b').root] }] } })).toHaveLength(2)
    // 没有 v / 没有 root 的形态也不抛错
    expect(ruleLeaves({ op: 'all', children: [{ type: 'x' }] })).toHaveLength(1)
    expect(ruleLeaves(null)).toEqual([])
  })
})

describe('catalogUsabilityFor：D21 的唯一实现（四条判据）', () => {
  test('未加载（idle / loading）不拦：页面刚打开就灰按钮是自我封锁', () => {
    expect(catalogUsabilityFor({ ...readyCatalog(), status: 'idle' }, [rule('activity.count')]).usable).toBe(true)
    expect(catalogUsabilityFor({ ...readyCatalog(), status: 'loading' }, [rule('activity.count')]).usable).toBe(true)
  })

  test('① 请求失败 → request-failed', () => {
    const verdict = catalogUsabilityFor({ ...readyCatalog(), status: 'failed', items: [] }, [rule('activity.count')])
    expect(verdict).toMatchObject({ usable: false, reason: 'request-failed' })
  })

  test('② 目录空 → empty-catalog（成功但 0 条等于没得选）', () => {
    expect(catalogUsabilityFor(readyCatalog({ items: [] }), [rule('activity.count')]))
      .toMatchObject({ usable: false, reason: 'empty-catalog' })
  })

  test('③ 载荷含 fact.count 而事实候选为空 → 无法校验', () => {
    expect(catalogUsabilityFor(readyCatalog({ factCandidates: [] }), [rule('fact.count')]))
      .toMatchObject({ usable: false, reason: 'fact-candidates-missing' })
    // 没有 fact.count 叶子时，候选为空**不**拦（那是「本来就没用到」）。
    expect(catalogUsabilityFor(readyCatalog({ factCandidates: [] }), [rule('activity.count')]).usable).toBe(true)
  })

  test('④ 载荷含 refKind=k 而该 kind 候选缺失 → 无法校验（并报出缺哪种）', () => {
    const verdict = catalogUsabilityFor(readyCatalog({ refCandidates: {} }), [rule('badge.owned')])
    expect(verdict).toMatchObject({ usable: false, reason: 'ref-candidates-missing' })
    expect(verdict.missingRefKinds).toEqual(['badge'])
  })

  test('`fact` 不是 refCandidates 的键：不会被误判成缺候选', () => {
    const catalog = readyCatalog({
      items: [{ ...catalogEntry('fact.count'), params: [{ name: 'factKey', kind: 'enum', required: true, refKind: 'fact' }] }]
    })
    expect(catalogUsabilityFor(catalog, [rule('fact.count')]).usable).toBe(true)
  })

  test('判据只描述目录本身；「没有规则」由 `anyTierRuleDirty` 那一侧放行', () => {
    // 目录失败就是失败（与载荷无关）：两个判据各管一件事，合成才是闸门。
    const failed = { ...readyCatalog(), status: 'failed' as const }
    expect(catalogUsabilityFor(failed, []).usable).toBe(false)
    expect(catalogUsabilityFor(failed, [rule('a')]).usable).toBe(false)
    // 闸门 = dirty ∧ ¬usable：没动过规则时永不拦，只改数值的提交照常走。
    expect(anyTierRuleDirty([{ id: 'a', rule: null }], [{ id: 'a', rule: null }])).toBe(false)
  })
})

describe('anyTierRuleDirty：闸门的另一半', () => {
  const initial = [{ id: 'a', rule: null }, { id: 'b', rule: { v: 1, root: { type: 'x' } } }]

  test('没动过 → false（打开面板看两眼不算改动）', () => {
    expect(anyTierRuleDirty([{ id: 'a', rule: null }, { id: 'b', rule: { v: 1, root: { type: 'x' } } }], initial)).toBe(false)
  })

  test('改一档 → true；改成同值（重新序列化等价）→ false', () => {
    expect(anyTierRuleDirty([{ id: 'a', rule: rule('y') }, initial[1]!], initial)).toBe(true)
    expect(anyTierRuleDirty([{ id: 'a', rule: null }, { id: 'b', rule: { v: 1, root: { type: 'x' } } }], initial)).toBe(false)
  })

  test('新档（initial 里没有 id）带规则 → true', () => {
    expect(anyTierRuleDirty([...initial, { id: 'tier_9', rule: rule('z') }], initial)).toBe(true)
  })

  test('删掉的档不再参与比较（只比较当前表单里的档）', () => {
    expect(anyTierRuleDirty([{ id: 'a', rule: null }], initial)).toBe(false)
  })
})

describe('firstRuleErrorTierId：422 后要展开哪一档', () => {
  const error = (field: string, rank = 0, tierId = ''): TrustLevelSaveError => ({ rank, tierId, field, reasonCode: 'rule_shape_invalid', reason: '', params: {} })

  test('规则域字段（rule / rule.*）才触发展开', () => {
    expect(firstRuleErrorTierId({ byTierId: { member: [error('rule')] }, pageLevel: [] })).toBe('member')
    expect(firstRuleErrorTierId({ byTierId: { member: [error('rule.cmp')] }, pageLevel: [] })).toBe('member')
  })

  test('阈值/名字类错误不展开面板（那一行本来就在眼前）', () => {
    expect(firstRuleErrorTierId({ byTierId: { member: [error('requirements.topic_count')] }, pageLevel: [] })).toBeNull()
  })

  test('没有档位级错误 → null', () => {
    expect(firstRuleErrorTierId({ byTierId: {}, pageLevel: [error('rule')] })).toBeNull()
  })
})
