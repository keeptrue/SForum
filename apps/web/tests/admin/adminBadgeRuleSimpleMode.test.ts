import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, ref } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'
import type {
  BadgeConditionCatalogEntry,
  BadgeRule,
  BadgeRuleLeaf,
  BadgeRuleNode
} from '../../app/utils/admin/badgeRule'
import {
  BADGE_RULE_LIMITS,
  createDefaultLeaf,
  createGroup,
  createLeaf,
  insertChild,
  isGroupNode,
  removeNode,
  replaceNode,
  ruleStats,
  validateRuleDraft,
  wrapWithGroup
} from '../../app/utils/admin/badgeRule'
import { resolveParamOptionLabel } from '../../app/utils/admin/badgeRuleParams'
import {
  applyFactKeySelection,
  factCandidateUnit,
  factKeyMissing,
  factKeyOptions,
  factVariantOptions
} from '../../app/utils/admin/badgeFactCandidates'
import {
  refKindGroup,
  refKindItems,
  isBadgeRefKind,
  refKindMode,
  refOptionLabel
} from '../../app/utils/admin/badgeRefCandidates'
import {
  buildFlatAllRule,
  flattenAllLeaves,
  isSimpleRule,
  leafSentence,
  ruleSentence,
  type RuleSentenceI18n
} from '../../app/utils/admin/badgeRuleSentence'

/**
 * 简单模式的契约：**表达能力**判断（`isSimpleRule`）、无损往返、降级不改 AST、
 * 以及「切到简单模式看到的是人话」。
 *
 * 最要紧的一条：`any` / `not` / 嵌套组永远进不了简单模式，切过去只降级不迁移——
 * 压平会改变判定结果，等于替运营发错徽章。
 */
const localesRoot = new URL('../../i18n/locales/', import.meta.url)
const readJson = (name: string) => JSON.parse(readFileSync(new URL(name, localesRoot), 'utf8')) as Record<string, unknown>

function flattenKeys(node: unknown, prefix = '', out: Record<string, string> = {}): Record<string, string> {
  if (!node || typeof node !== 'object') return out
  for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object') flattenKeys(value, path, out)
    else out[path] = String(value)
  }
  return out
}

const zhKeys = flattenKeys(readJson('zh-CN.json'))
const enKeys = flattenKeys(readJson('en-US.json'))

/** 真实 locale + 真实插值：句子断言看的是运营真正会读到的话，不是我自己编的中文。 */
function sentenceI18nOf(locale: 'zh-CN' | 'en-US'): RuleSentenceI18n {
  const dict = locale === 'zh-CN' ? zhKeys : enKeys
  return {
    t: (key: string, params?: Record<string, unknown>) =>
      (dict[key] ?? key).replace(/\{(\w+)\}/g, (_match, name: string) => String(params?.[name] ?? '')),
    te: (key: string) => key in dict
  }
}

const i18nStub = () => ({
  t: (key: string, params?: Record<string, unknown>) => sentenceI18nOf('zh-CN').t(key, params),
  te: (key: string) => key in zhKeys,
  locale: { value: 'zh-CN' }
})

const toastCalls: unknown[] = []
const useToastStub = () => ({ add: (payload: unknown) => { toastCalls.push(payload) } })
const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })

const ruleDir = new URL('../../app/components/admin/badges/rule/', import.meta.url)

const ParamField = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleParamField.vue', ruleDir)), 'simple-param-field', {
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
})

const Leaf = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleLeaf.vue', ruleDir)), 'simple-leaf', {
  createDefaultLeaf,
  replaceNode,
  computed,
  ref,
  useI18n: i18nStub,
  resolveParamOptionLabel,
  applyFactKeySelection,
  factCandidateUnit,
  factKeyMissing,
  factKeyOptions,
  factVariantOptions,
  SFAdminBadgeRuleParamField: ParamField
})

const Group = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleGroup.vue', ruleDir)), 'simple-group', {
  BADGE_RULE_LIMITS,
  createDefaultLeaf,
  createGroup,
  insertChild,
  isGroupNode,
  removeNode,
  replaceNode,
  ruleStats,
  wrapWithGroup,
  computed,
  ref,
  useI18n: i18nStub,
  SFAdminBadgeRuleLeaf: Leaf
})

const SimpleForm = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleSimpleForm.vue', ruleDir)), 'simple-form', {
  BADGE_RULE_LIMITS,
  createDefaultLeaf,
  isGroupNode,
  validateRuleDraft,
  resolveParamOptionLabel,
  applyFactKeySelection,
  factKeyOptions,
  factVariantOptions,
  buildFlatAllRule,
  flattenAllLeaves,
  leafSentence,
  computed,
  ref,
  useI18n: i18nStub,
  SFAdminBadgeRuleParamField: ParamField
})

const JsonPreview = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleJsonPreview.vue', ruleDir)), 'simple-json', {
  computed,
  ref,
  useI18n: i18nStub,
  useToast: useToastStub
})

const Editor = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleEditor.vue', ruleDir)), 'simple-editor', {
  isGroupNode,
  isSimpleRule,
  ruleSentence,
  computed,
  ref,
  useI18n: i18nStub,
  SFAdminBadgeRuleGroup: Group,
  SFAdminBadgeRuleLeaf: Leaf,
  SFAdminBadgeRuleSimpleForm: SimpleForm,
  SFAdminBadgeRuleJsonPreview: JsonPreview
})

const catalog: BadgeConditionCatalogEntry[] = [
  {
    key: 'activity.count',
    valueKind: 'count',
    compare: ['gte', 'lte'],
    labelZh: '计数',
    labelEn: 'Count',
    params: [
      { name: 'metric', kind: 'enum', required: true, options: ['topic_count', 'comment_count', 'days_visited'] },
      { name: 'window', kind: 'window', required: true }
    ]
  },
  {
    key: 'identity.email_verified',
    valueKind: 'boolean',
    compare: [],
    labelZh: '邮箱已验证',
    labelEn: 'Email verified'
  }
]

function activityLeaf(params: Record<string, unknown> = { metric: 'days_visited', window: { kind: 'all' } }, value = 7): BadgeRuleLeaf {
  return { type: 'activity.count', params, cmp: { op: 'gte', value } }
}

function ruleOf(root: BadgeRuleNode): BadgeRule {
  return { v: 1, root }
}

function mountEditor(rule: BadgeRule, extra: Record<string, unknown> = {}) {
  return mount(Editor, {
    props: { modelValue: rule, catalog, ...extra },
    global: { stubs: { UIcon: UIconStub } }
  })
}

function lastRule(wrapper: ReturnType<typeof mountEditor>): BadgeRule {
  const events = wrapper.emitted('update:modelValue')
  expect(events).toBeDefined()
  return events![events!.length - 1]![0] as BadgeRule
}

describe('isSimpleRule 只认扁平 all', () => {
  test('扁平 all 与单叶根算简单，空 all 也算（保存另有 empty_group 拦截）', () => {
    expect(isSimpleRule(ruleOf({ op: 'all', children: [activityLeaf()] }))).toBe(true)
    expect(isSimpleRule(ruleOf({ op: 'all', children: [activityLeaf(), createLeaf('identity.email_verified')] }))).toBe(true)
    expect(isSimpleRule(ruleOf(activityLeaf()))).toBe(true)
    expect(isSimpleRule(ruleOf({ op: 'all', children: [] }))).toBe(true)
  })

  test('any / not / 嵌套组都不是简单规则', () => {
    expect(isSimpleRule(ruleOf({ op: 'any', children: [activityLeaf()] }))).toBe(false)
    expect(isSimpleRule(ruleOf({ op: 'not', children: [activityLeaf()] }))).toBe(false)
    expect(isSimpleRule(ruleOf({ op: 'all', children: [{ op: 'all', children: [activityLeaf()] }] }))).toBe(false)
    expect(isSimpleRule(null)).toBe(false)
  })
})

describe('flattenAllLeaves ↔ buildFlatAllRule 无损往返', () => {
  test('扁平 all 往返等价，且不改入参', () => {
    const rule = ruleOf({ op: 'all', children: [activityLeaf(), createLeaf('identity.email_verified')] })
    const snapshot = JSON.stringify(rule)
    const leaves = flattenAllLeaves(rule)
    expect(leaves).toHaveLength(2)
    expect(buildFlatAllRule(leaves)).toEqual(rule)
    expect(JSON.stringify(rule)).toBe(snapshot)
  })

  test('单叶根进入简单模式后归一成 all[leaf]（语义等价，切换本身不 emit）', () => {
    const rule = ruleOf(activityLeaf())
    const built = buildFlatAllRule(flattenAllLeaves(rule))
    expect(built.root).toEqual({ op: 'all', children: [activityLeaf()] })
  })

  test('不可表达的规则 flatten 返回空数组，调用方必须先判 isSimpleRule', () => {
    expect(flattenAllLeaves(ruleOf({ op: 'any', children: [activityLeaf()] }))).toEqual([])
    expect(flattenAllLeaves(ruleOf({ op: 'not', children: [activityLeaf()] }))).toEqual([])
    // 空列表仍产出 all 组（不是 null）：保存由既有 empty_group 拦住。
    expect(buildFlatAllRule([]).root).toEqual({ op: 'all', children: [] })
  })
})

describe('人话预览 ruleSentence', () => {
  test('累计窗口 + 指标中文名 + 比较符', () => {
    const sentence = ruleSentence(ruleOf({ op: 'all', children: [activityLeaf()] }), catalog, 'zh-CN', sentenceI18nOf('zh-CN'))
    expect(sentence).toContain('累计')
    expect(sentence).toContain('访问天数')
    expect(sentence).toContain('≥ 7')
    expect(sentence).not.toContain('days_visited')
  })

  test('最近 N 天窗口把天数说出来，且英文不落到 slug', () => {
    const rule = ruleOf({
      op: 'all',
      children: [activityLeaf({ metric: 'comment_count', window: { kind: 'recent_days', days: 30 } }, 5)]
    })
    const zh = ruleSentence(rule, catalog, 'zh-CN', sentenceI18nOf('zh-CN'))
    expect(zh).toContain('最近 30 天内')
    expect(zh).toContain('回复数')
    const en = ruleSentence(rule, catalog, 'en-US', sentenceI18nOf('en-US'))
    expect(en).toContain('In the last 30 days')
    expect(en).toContain('Replies')
    expect(en).not.toContain('comment_count')
  })

  test('没有窗口参数的条件不加「累计」前缀（账户年龄 / 连续访问没有时间窗口概念）', () => {
    const timelessCatalog: BadgeConditionCatalogEntry[] = [
      {
        key: 'identity.account_age',
        valueKind: 'count',
        compare: ['gte', 'lte'],
        labelZh: '账户年龄',
        labelEn: 'Account age',
        params: [{ name: 'days', kind: 'int', required: true, min: 1, max: 3650 }]
      },
      {
        key: 'activity.streak',
        valueKind: 'count',
        compare: ['gte'],
        labelZh: '连续访问',
        labelEn: 'Visit streak',
        params: [{ name: 'minDays', kind: 'int', required: true, min: 1, max: 3650 }]
      }
    ]
    const rule = ruleOf({
      op: 'all',
      children: [
        { type: 'identity.account_age', params: { days: 30 }, cmp: { op: 'gte', value: 30 } },
        { type: 'activity.streak', params: { minDays: 7 }, cmp: { op: 'gte', value: 7 } }
      ]
    })
    const zh = ruleSentence(rule, timelessCatalog, 'zh-CN', sentenceI18nOf('zh-CN'))
    expect(zh).not.toContain('累计')
    expect(zh.startsWith('账户年龄')).toBe(true)
    expect(zh).toContain('连续访问')

    const en = ruleSentence(rule, timelessCatalog, 'en-US', sentenceI18nOf('en-US'))
    expect(en).not.toContain('All-time')
    // 空 when 在英文模板里会留行首空格：裁掉首尾，但不许出现「双空格」或前导空格。
    expect(en.startsWith('Account age')).toBe(true)
    expect(en).not.toContain('  ')
  })

  test('any / not 用对应连接词，未知条件类型回退原始 key', () => {
    const any = ruleSentence(
      ruleOf({ op: 'any', children: [activityLeaf(), createLeaf('identity.email_verified')] }),
      catalog,
      'zh-CN',
      sentenceI18nOf('zh-CN')
    )
    expect(any).toContain(' 或 ')
    const not = ruleSentence(ruleOf({ op: 'not', children: [activityLeaf()] }), catalog, 'zh-CN', sentenceI18nOf('zh-CN'))
    expect(not).toContain('非（')
    const unknown = leafSentence({ type: 'plugin.gone', cmp: { op: 'gte', value: 1 } }, catalog, 'zh-CN', sentenceI18nOf('zh-CN'))
    expect(unknown).toBe('plugin.gone')
    expect(ruleSentence(ruleOf({ op: 'all', children: [] }), catalog, 'zh-CN', sentenceI18nOf('zh-CN')))
      .toBe(zhKeys['admin.badges.rule.sentence.empty'])
  })
})

describe('编辑器双模式', () => {
  test('扁平规则默认进简单模式：一行一句，不渲染布尔树', () => {
    const wrapper = mountEditor(ruleOf({ op: 'all', children: [activityLeaf()] }))
    try {
      expect(wrapper.find('[data-sf-badge-rule-editor]').attributes('data-rule-mode')).toBe('simple')
      expect(wrapper.find('[data-simple-rule]').exists()).toBe(true)
      expect(wrapper.find('[data-rule-group]').exists()).toBe(false)
      expect(wrapper.find('[data-simple-sentence]').text()).toContain('访问天数')
      // 常显预览仍然在模式切换下方。
      expect(wrapper.find('[data-rule-sentence]').text()).toContain('访问天数')
    } finally {
      wrapper.unmount()
    }
  })

  test('any / not 默认进高级模式；切到简单模式只降级、绝不改 AST', async () => {
    for (const root of [
      { op: 'any' as const, children: [activityLeaf(), createLeaf('identity.email_verified')] },
      { op: 'not' as const, children: [activityLeaf()] },
      { op: 'all' as const, children: [{ op: 'all' as const, children: [activityLeaf()] }] }
    ]) {
      const rule = ruleOf(root)
      const wrapper = mountEditor(rule)
      try {
        expect(wrapper.find('[data-sf-badge-rule-editor]').attributes('data-rule-mode')).toBe('advanced')
        expect(wrapper.find('[data-rule-group]').exists()).toBe(true)

        await wrapper.find('[data-rule-mode-simple]').trigger('click')
        expect(wrapper.find('[data-sf-badge-rule-editor]').attributes('data-rule-mode')).toBe('simple')
        expect(wrapper.find('[data-rule-mode-advanced-only]').exists()).toBe(true)
        expect(wrapper.find('[data-simple-rule]').exists()).toBe(false)
        // 关键断言：降级路径一次都不 emit，规则原样不动。
        expect(wrapper.emitted('update:modelValue')).toBeUndefined()
      } finally {
        wrapper.unmount()
      }
    }
  })

  test('initialMode=simple 强开在 any 规则上：只降级、不 emit、不动 AST', () => {
    // 宿主（或测试）把它强开成简单模式时，唯一允许的结果是降级视图。
    const anyRule = ruleOf({ op: 'any', children: [activityLeaf(), createLeaf('identity.email_verified')] })
    const snapshot = JSON.stringify(anyRule)
    const wrapper = mountEditor(anyRule, { initialMode: 'simple' })
    try {
      expect(wrapper.find('[data-sf-badge-rule-editor]').attributes('data-rule-mode')).toBe('simple')
      expect(wrapper.find('[data-rule-mode-advanced-only]').exists()).toBe(true)
      expect(wrapper.find('[data-simple-rule]').exists()).toBe(false)
      expect(wrapper.emitted('update:modelValue')).toBeUndefined()
      // 入参规则本身也没被就地改（降级路径不碰数据）。
      expect(JSON.stringify(anyRule)).toBe(snapshot)
    } finally {
      wrapper.unmount()
    }
  })

  test('高级模式仍可用布尔树；切回简单模式恢复表单', async () => {
    const wrapper = mountEditor(ruleOf({ op: 'all', children: [activityLeaf()] }))
    try {
      await wrapper.find('[data-rule-mode-advanced]').trigger('click')
      expect(wrapper.find('[data-sf-badge-rule-editor]').attributes('data-rule-mode')).toBe('advanced')
      expect(wrapper.find('[data-rule-group]').exists()).toBe(true)
      expect(wrapper.find('[data-simple-rule]').exists()).toBe(false)

      await wrapper.find('[data-rule-mode-simple]').trigger('click')
      expect(wrapper.find('[data-simple-rule]').exists()).toBe(true)
      expect(wrapper.find('[data-rule-mode-advanced-only]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('简单表单产出的 AST', () => {
  test('添加条件：空 all 加一条 activity.count 默认参数', async () => {
    const wrapper = mountEditor(ruleOf({ op: 'all', children: [] }))
    try {
      expect(wrapper.find('[data-simple-empty]').exists()).toBe(true)
      await wrapper.find('[data-simple-add]').trigger('click')
      const next = lastRule(wrapper)
      expect(next.root).toEqual({
        op: 'all',
        children: [{
          type: 'activity.count',
          params: { metric: 'topic_count', window: { kind: 'all' } },
          cmp: { op: 'gte', value: 1 }
        }]
      })
    } finally {
      wrapper.unmount()
    }
  })

  test('删除条件只移除那一行，其余叶子保持不变', async () => {
    const keep = activityLeaf()
    const wrapper = mountEditor(ruleOf({ op: 'all', children: [keep, createLeaf('identity.email_verified')] }))
    try {
      const removers = wrapper.findAll('[data-simple-remove]')
      expect(removers).toHaveLength(2)
      await removers[1]!.trigger('click')
      const next = lastRule(wrapper)
      expect(next.root).toEqual({ op: 'all', children: [keep] })
    } finally {
      wrapper.unmount()
    }
  })

  test('删除按钮在卡片头（与句子同行），不在控件行里，点击仍删掉该行', async () => {
    const keep = activityLeaf()
    const wrapper = mountEditor(ruleOf({ op: 'all', children: [keep, createLeaf('identity.email_verified')] }))
    try {
      // 控件行只放控件：窄屏时才不会把按钮挤成孤立图标。
      expect(wrapper.find('.sf-simple-rule__controls [data-simple-remove]').exists()).toBe(false)
      expect(wrapper.find('.sf-simple-rule__head [data-simple-remove]').exists()).toBe(true)
      expect(wrapper.find('.sf-simple-rule__head [data-simple-sentence]').exists()).toBe(true)
      // 可访问名仍在（不是只剩一个图标）。
      const removeButton = wrapper.findAll('.sf-simple-rule__head [data-simple-remove]')[1]!
      expect(removeButton.attributes('aria-label')).toBe(zhKeys['admin.badges.rule.simple.delete'])
      expect(removeButton.attributes('title')).toBe(zhKeys['admin.badges.rule.simple.delete'])

      await removeButton.trigger('click')
      expect(lastRule(wrapper).root).toEqual({ op: 'all', children: [keep] })
    } finally {
      wrapper.unmount()
    }
  })

  test('window 是组合控件：选「最近 N 天」保留默认天数，改 N 只改 days', async () => {
    const wrapper = mountEditor(ruleOf({ op: 'all', children: [activityLeaf({ metric: 'topic_count', window: { kind: 'all' } })] }))
    try {
      const windowSelect = wrapper.find('[data-param="window"] select')
      expect(windowSelect.exists()).toBe(true)
      await windowSelect.setValue('recent_days')
      let leaf = (lastRule(wrapper).root as { children: BadgeRuleLeaf[] }).children[0]!
      expect(leaf.params?.window).toEqual({ kind: 'recent_days', days: 30 })
      // 受控组件：宿主把新规则写回之后，N 输入才会出现（这里手动扮演宿主）。
      await wrapper.setProps({ modelValue: lastRule(wrapper) })

      const days = wrapper.find('[data-param="window"] input[type="number"]')
      expect(days.exists()).toBe(true)
      await days.setValue('45')
      leaf = (lastRule(wrapper).root as { children: BadgeRuleLeaf[] }).children[0]!
      expect(leaf.params?.window).toEqual({ kind: 'recent_days', days: 45 })
    } finally {
      wrapper.unmount()
    }
  })

  test('叶子根规则的行内错误落在第 0 行（空 path 不等于没有行）', () => {
    // `regular-visitor` 这类徽章的 root 就是叶子：validateRuleDraft 给它 path `[]`，
    // 旧实现 `path[0] === index` 恒假，运营在简单模式下看不到任何行内错误。
    const rule = ruleOf({ type: 'activity.count', params: {}, cmp: { op: 'gte', value: 1 } })
    const wrapper = mountEditor(rule)
    try {
      const rows = wrapper.findAll('[data-simple-row]')
      expect(rows).toHaveLength(1)
      const error = rows[0]!.find('[data-simple-row-error]')
      expect(error.exists()).toBe(true)
      expect(error.text()).toContain(zhKeys['admin.badges.rule.errors.param_required'])
    } finally {
      wrapper.unmount()
    }
  })

  test('组根的规则级错误（空 path）不会误挂到第 0 行', () => {
    // too_many_leaves 的 path 也是 `[]`：它不是「第 0 行少了参数」，不该伪装成行内错误。
    const leaves = Array.from({ length: BADGE_RULE_LIMITS.maxLeaves + 1 }, () => activityLeaf())
    const wrapper = mountEditor(ruleOf({ op: 'all', children: leaves }))
    try {
      expect(wrapper.findAll('[data-simple-row]')).toHaveLength(leaves.length)
      expect(wrapper.findAll('[data-simple-row-error]')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('带单位的阈值：单位是 input 的行内后缀，不是第三行', () => {
    const withUnit = catalog.map(item => (item.key === 'activity.count'
      ? { ...item, unitZh: '次', unitEn: 'times' }
      : item))
    const rule = ruleOf({ op: 'all', children: [activityLeaf({ metric: 'topic_count', window: { kind: 'all' } })] })
    const wrapper = mountEditor(rule, { catalog: withUnit })
    try {
      const inputEl = wrapper.find('[data-simple-value]').element
      const valueWrap = wrapper.find('.sf-simple-field__value')
      expect(valueWrap.exists()).toBe(true)
      // 单位与 input 同一个父节点 = 同行渲染，字段因此只有两行（label / 控件）。
      expect(inputEl.parentElement).toBe(valueWrap.element)
      expect(valueWrap.element.querySelector('.sf-simple-field__unit')?.textContent?.trim()).toBe('次')
      // 旧结构里单位是 `.sf-simple-field` 的第三个直接子元素（独占一行）——必须不复存在。
      const fieldEl = inputEl.closest('.sf-simple-field') as HTMLElement
      const unitAsOwnRow = Array.from(fieldEl.children)
        .some(child => child.classList.contains('sf-simple-field__unit'))
      expect(unitAsOwnRow).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('days_visited 仍然不显示单位（「访问天数 ≥ 7 次」读不通）', () => {
    const withUnit = catalog.map(item => (item.key === 'activity.count'
      ? { ...item, unitZh: '次', unitEn: 'times' }
      : item))
    const wrapper = mountEditor(ruleOf({ op: 'all', children: [activityLeaf()] }), { catalog: withUnit })
    try {
      expect(wrapper.find('.sf-simple-field__unit').exists()).toBe(false)
      expect(wrapper.find('.sf-simple-field__value').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('行内错误按 error.path 定位：缺必填参数时只在那一行报', async () => {
    // 直接给一个缺 metric / window 的叶子：validateRuleDraft 报的 path 是 [0]。
    const rule = ruleOf({ op: 'all', children: [{ type: 'activity.count', params: {}, cmp: { op: 'gte', value: 1 } }, activityLeaf()] })
    const wrapper = mountEditor(rule)
    try {
      const rows = wrapper.findAll('[data-simple-row]')
      expect(rows).toHaveLength(2)
      const first = rows[0]!.find('[data-simple-row-error]')
      expect(first.exists()).toBe(true)
      expect(first.text()).toContain(zhKeys['admin.badges.rule.errors.param_required'])
      // 第二行正常，不该蹭到别人的错误。
      expect(rows[1]!.find('[data-simple-row-error]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('规则 JSON 折叠与复制', () => {
  test('折叠态复制按钮就在 summary 行：一次点击可用，且不连带展开内容区', async () => {
    const written: string[] = []
    Object.defineProperty(globalThis.navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async (text: string) => { written.push(text) } }
    })
    const wrapper = mount(JsonPreview, {
      props: { rule: ruleOf({ op: 'all', children: [activityLeaf()] }) },
      global: { stubs: { UIcon: UIconStub } }
    })
    try {
      // 默认折叠。复制按钮在 summary 里 → 折叠态也可见（真机实测：内容区的按钮要 2 次点击）。
      expect(wrapper.find('details').attributes('open')).toBeUndefined()
      expect(wrapper.find('summary [data-rule-copy]').exists()).toBe(true)
      // `pre` 仍在 details 内容区（折叠时浏览器不绘制），而不是被挪到 summary。
      expect(wrapper.find('summary pre').exists()).toBe(false)
      expect(wrapper.find('pre').exists()).toBe(true)

      toastCalls.length = 0
      await wrapper.find('[data-rule-copy]').trigger('click')
      await flushPromises()
      expect(written[0]).toContain('"activity.count"')
      expect(toastCalls).toHaveLength(1)
      // 点复制不该顺带把 JSON 展开（summary 内的按钮已 stop+prevent）。
      expect(wrapper.find('details').attributes('open')).toBeUndefined()

      await wrapper.find('summary').trigger('click')
      await flushPromises()
      expect(wrapper.find('details').attributes('open')).toBeDefined()
    } finally {
      wrapper.unmount()
    }
  })
})
