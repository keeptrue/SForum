import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { computed } from 'vue'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import type { BadgeConditionCatalogEntry, BadgeFactCandidate, BadgeRuleLeaf } from '../../app/utils/admin/badgeRule'
import { createDefaultLeaf, replaceNode } from '../../app/utils/admin/badgeRule'
import { resolveParamOptionLabel } from '../../app/utils/admin/badgeRuleParams'
import {
  applyFactKeySelection,
  factCandidateLabel,
  factCandidateUnit,
  factKeyMissing,
  factKeyOptions,
  factVariantOptions,
  normalizeFactCandidates
} from '../../app/utils/admin/badgeFactCandidates'
import {
  refKindGroup,
  refKindItems,
  isBadgeRefKind,
  refKindMode,
  refOptionLabel
} from '../../app/utils/admin/badgeRefCandidates'

/**
 * M3.1：编辑器消费 `factCandidates` 的契约（Errata 2 冻结形状）。
 *
 * 断言按**失败类**写，不锁实现/测试名：
 * - 候选文案是**值级**的（插件 label 真的出现在 option 里，不是「键存在」）；
 * - `variants` 为空 → 整项隐藏（渲染出空 SelectContent 就是回归）+ `variant` 置空串；
 * - 旧 key 已失效 → 原值保留可见 + 失效标记，**不许静默清空**；
 * - 候选整体读不到 → `[]` + 可见提示，编辑器仍可用。
 */
const i18nStub = () => ({
  t: (key: string) => key,
  te: () => true,
  locale: { value: 'zh-CN' }
})

const ParamField = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/admin/badges/rule/SFAdminBadgeRuleParamField.vue', import.meta.url)),
  'rule-param-field-facts',
  {
    computed,
    useI18n: i18nStub,
    resolveParamOptionLabel,
    applyFactKeySelection,
    factKeyMissing,
    factKeyOptions,
    factVariantOptions,
    refKindGroup,
    refKindItems,
    isBadgeRefKind,
    refKindMode,
    refOptionLabel
  }
)

const Leaf = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/admin/badges/rule/SFAdminBadgeRuleLeaf.vue', import.meta.url)),
  'rule-leaf-facts',
  {
    createDefaultLeaf,
    replaceNode,
    computed,
    useI18n: i18nStub,
    resolveParamOptionLabel,
    applyFactKeySelection,
    factCandidateUnit,
    factKeyMissing,
    factKeyOptions,
    factVariantOptions,
    SFAdminBadgeRuleParamField: ParamField
  }
)

const catalog: BadgeConditionCatalogEntry[] = [
  {
    key: 'fact.count',
    valueKind: 'count',
    compare: ['gte', 'lte'],
    labelZh: '插件事实计数',
    labelEn: 'Plugin fact count',
    params: [
      // 目录只下发取值；事实候选（factCandidates）负责文案。
      { name: 'factKey', kind: 'objectRef', required: true, refKind: 'fact', options: [
        'example.tasks',
        'example.streak',
        'example.plain'
      ] },
      { name: 'variant', kind: 'enum', required: false, options: [] },
      { name: 'window', kind: 'window', required: false }
    ]
  }
]

function candidate(overrides: Partial<BadgeFactCandidate> = {}): BadgeFactCandidate {
  return {
    key: 'example.tasks',
    labelZh: '完成新手任务数',
    labelEn: 'Completed onboarding tasks',
    unitZh: '个',
    unitEn: 'tasks',
    compare: ['gte'],
    variants: [],
    ...overrides
  }
}

const candidates: BadgeFactCandidate[] = [
  candidate({
    variants: [
      { value: 'beginner', labelZh: '入门', labelEn: 'Beginner' },
      { value: 'advanced', labelZh: '进阶', labelEn: 'Advanced' }
    ]
  }),
  candidate({ key: 'example.streak', labelZh: '连续打卡天数', labelEn: '', variants: [] })
]

function leaf(params: Record<string, unknown> = { factKey: 'example.tasks', variant: '' }): BadgeRuleLeaf {
  return { type: 'fact.count', params, cmp: { op: 'gte', value: 3 } }
}

function mountLeaf(node: BadgeRuleLeaf, extraProps: Record<string, unknown> = {}) {
  return mount(Leaf, {
    props: {
      rule: { v: 1, root: node },
      node,
      path: [],
      depth: 1,
      catalog,
      factCandidates: candidates,
      ...extraProps
    }
  })
}

describe('factCandidates normalization', () => {
  test('accepts flattened and manifest-nested bilingual labels', () => {
    const rows = normalizeFactCandidates([
      { key: 'a.flat', labelZh: '扁平', labelEn: 'Flat', unitZh: '次', unitEn: 'times', compare: ['gte'], variants: [] },
      { key: 'a.nested', label: { zh: '嵌套', en: 'Nested' }, unit: { zh: '个', en: 'items' }, variants: [{ value: 'v1', label: { zh: '一', en: 'One' } }] }
    ])
    expect(rows?.[0]?.labelZh).toBe('扁平')
    expect(rows?.[1]?.labelZh).toBe('嵌套')
    expect(rows?.[1]?.unitEn).toBe('items')
    expect(rows?.[1]?.variants[0]).toEqual({ value: 'v1', labelZh: '一', labelEn: 'One' })
  })

  test('an absent field is a normal empty candidate list, not a failure', () => {
    // 后端还没升级（M3 并行期）不该天天报警：只有「给了但不是数组」才是失败。
    expect(normalizeFactCandidates(undefined)).toEqual([])
    expect(normalizeFactCandidates([])).toEqual([])
    expect(normalizeFactCandidates('nope')).toBeNull()
    expect(normalizeFactCandidates({ items: [] })).toBeNull()
  })

  test('drops unusable rows but keeps the rest', () => {
    const rows = normalizeFactCandidates([{ key: '' }, { nope: 1 }, { key: 'keep.me', labelZh: '留下' }])
    expect(rows?.map(row => row.key)).toEqual(['keep.me'])
  })
})

describe('fact option mapping is value-level, not key-level', () => {
  test('labels come from the candidate in the active language, then the other one, then the key', () => {
    const zhLabels = factKeyOptions(['example.tasks', 'example.streak'], candidates, true)
    expect(zhLabels.map(option => option.label)).toEqual(['完成新手任务数', '连续打卡天数'])
    // 英文缺失时回落到中文，而不是回落到裸 key。
    const enLabels = factKeyOptions(['example.tasks', 'example.streak'], candidates, false)
    expect(enLabels.map(option => option.label)).toEqual(['Completed onboarding tasks', '连续打卡天数'])
    // 两侧都空 → key，绝不显示空串。
    expect(factCandidateLabel(candidate({ labelZh: '', labelEn: '' }), true, 'example.tasks')).toBe('example.tasks')
  })

  test('variants belong to a fact key: another key never inherits them', () => {
    expect(factVariantOptions(candidates, 'example.tasks', true).map(option => option.label)).toEqual(['入门', '进阶'])
    expect(factVariantOptions(candidates, 'example.streak', true)).toEqual([])
    expect(factVariantOptions(candidates, 'gone.key', true)).toEqual([])
  })

  test('unit is read from the candidate and disappears with it', () => {
    expect(factCandidateUnit(candidates, 'example.tasks', true)).toBe('个')
    expect(factCandidateUnit(candidates, 'gone.key', true)).toBe('')
  })

  test('picking a key without variants clears the stale variant value', () => {
    const next = applyFactKeySelection({ factKey: 'example.tasks', variant: 'beginner' }, candidates, 'example.streak', true)
    // 变体属于旧 key：留着它等于保存一条必然被域层拒绝的规则。
    expect(next.variant).toBe('')
    expect(next.factKey).toBe('example.streak')
  })

  test('picking a key keeps a variant that still belongs to it', () => {
    const next = applyFactKeySelection({ factKey: 'example.tasks', variant: 'advanced' }, candidates, 'example.tasks', true)
    expect(next.variant).toBe('advanced')
  })

  test('a missing key is reported as a stale reference instead of being cleared', () => {
    expect(factKeyMissing(candidates, 'gone.key')).toBe(true)
    expect(factKeyMissing(candidates, 'example.tasks')).toBe(false)
    expect(factKeyMissing(candidates, '')).toBe(false)
  })
})

describe('fact leaf rendering', () => {
  test('both levels render plugin labels instead of raw keys', () => {
    const wrapper = mountLeaf(leaf())
    const factOptions = wrapper.findAll('select[data-fact-key] option').map(option => option.text())
    expect(factOptions).toContain('完成新手任务数')
    expect(factOptions).not.toContain('example.tasks')

    const variantOptions = wrapper.findAll('[data-param="variant"] option').map(option => option.text())
    expect(variantOptions).toEqual(['入门', '进阶'])
    // 单位跟在比较值后面，让「3」有量纲。
    expect(wrapper.text()).toContain('个')
    wrapper.unmount()
  })

  test('a fact key without variants hides the whole variant field and empties the value', async () => {
    const wrapper = mountLeaf(leaf({ factKey: 'example.tasks', variant: 'beginner' }))
    expect(wrapper.find('[data-param="variant"]').exists()).toBe(true)

    await wrapper.find('select[data-fact-key]').setValue('example.streak')
    const emitted = wrapper.emitted('update:rule')?.at(-1)?.[0] as { root: BadgeRuleLeaf }
    expect(emitted.root.params?.variant).toBe('')
    expect(emitted.root.params?.factKey).toBe('example.streak')

    // 用父层会拿到的那个新节点重挂：变体项必须**整项消失**，而不是渲染一个空 SelectContent。
    const next = mountLeaf(emitted.root)
    expect(next.find('[data-param="variant"]').exists()).toBe(false)
    next.unmount()
    wrapper.unmount()
  })

  test('an unmounted extension key stays visible and flagged instead of being wiped', () => {
    const wrapper = mountLeaf(leaf({ factKey: 'gone.key', variant: '' }))
    const select = wrapper.find('select[data-fact-key]')
    expect(select.exists()).toBe(true)
    // 原值仍在下拉里（可见、可保存），不是被换成空值。
    expect((select.element as HTMLSelectElement).value).toBe('gone.key')
    expect(wrapper.findAll('select[data-fact-key] option').map(option => option.attributes('value'))).toContain('gone.key')

    const stale = wrapper.find('[data-fact-stale]')
    expect(stale.exists()).toBe(true)
    // 与后端 rule-health 的 missing_fact_key 同一句文案，运营不会看到两套说法。
    expect(stale.text()).toContain('admin.badges.rule.health.missing_fact_key')
    // 挂载本身不许产生写入（静默清空正是这条要挡住的）。
    expect(wrapper.emitted('update:rule')).toBeUndefined()
    // 失效 key 的变体无从判断：不渲染空变体下拉。
    expect(wrapper.find('[data-param="variant"]').exists()).toBe(false)
    wrapper.unmount()
  })

  test('candidate failure shows a visible hint and leaves the editor usable', () => {
    const wrapper = mountLeaf(leaf({ factKey: 'example.tasks', variant: '' }), {
      factCandidates: [],
      factCandidatesFailed: true
    })
    const hint = wrapper.find('[data-fact-candidates-failed]')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toContain('admin.badges.conditions.factCandidatesFailed')
    // 条件选择、比较符、阈值都还在：候选缺失不阻塞编辑。
    expect(wrapper.find('[data-rule-condition]').exists()).toBe(true)
    expect(wrapper.find('[data-rule-compare]').exists()).toBe(true)
    expect(wrapper.find('[data-rule-value]').exists()).toBe(true)
    wrapper.unmount()
  })
})
