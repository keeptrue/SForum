import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, h, ref } from 'vue'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import type {
  BadgeConditionCatalogEntry,
  BadgeRule,
  BadgeRuleLeaf
} from '../../app/utils/admin/badgeRule'
import {
  BADGE_RULE_LIMITS,
  createDefaultLeaf,
  isGroupNode,
  replaceNode,
  validateRuleDraft
} from '../../app/utils/admin/badgeRule'
import { buildFlatAllRule, flattenAllLeaves, leafSentence } from '../../app/utils/admin/badgeRuleSentence'
import { paramOptionLabelKey, resolveParamOptionLabel } from '../../app/utils/admin/badgeRuleParams'
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
  refOptionLabel,
  type BadgeRefCandidateMap
} from '../../app/utils/admin/badgeRefCandidates'

/**
 * 叶子参数控件的行为契约（从 `adminBadgeRuleEditor.test.ts` 拆出：那里只留树/组/上限，
 * 这里专管「一个参数 = 一个控件」）。
 *
 * 回归锁就在本文件：enum / window 参数的主控件**下方不允许**再出现别的输入框——
 * 曾经的 bug 是所有 kind 都会命中末尾兜底文本框，指标格多出 `days_visited`、
 * 窗口格多出 `[object Object]`。
 */
const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })

/**
 * `USelectMenu` 在测试环境里没有全局注册，未解析组件会把 props 压成字符串属性，
 * 断言就会变成「猜 attribute 名」。用同形 stub 把「传了什么、能选什么」变成结构化证据。
 */
const SelectMenuStub = defineComponent({
  name: 'USelectMenu',
  inheritAttrs: false,
  props: {
    modelValue: { type: String, default: '' },
    items: { type: Array, default: () => [] },
    valueKey: { type: String, default: '' },
    labelKey: { type: String, default: '' },
    searchInput: { type: [Boolean, Object], default: false },
    filterFields: { type: Array, default: () => [] },
    clear: { type: [Boolean, Object], default: false },
    placeholder: { type: String, default: '' }
  },
  emits: ['update:modelValue'],
  setup(props, { attrs, emit }) {
    return () => h('div', {
      ...attrs,
      'data-select-stub': '',
      'data-model': props.modelValue as string,
      'data-search-input': String(props.searchInput),
      'data-filter-fields': (props.filterFields as string[]).join(','),
      'data-value-key': props.valueKey as string,
      'data-label-key': props.labelKey as string,
      'data-clear': String(props.clear),
      'data-placeholder': props.placeholder as string
    }, (props.items as Array<{ value: string, label: string }>).map(item => h(
      'button',
      { type: 'button', 'data-option': item.value, onClick: () => emit('update:modelValue', item.value) },
      item.label
    )))
  }
})

const i18nStub = () => ({
  t: (key: string) => key,
  te: () => true,
  locale: { value: 'zh-CN' }
})

const ParamField = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/admin/badges/rule/SFAdminBadgeRuleParamField.vue', import.meta.url)),
  'rule-param-field',
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
  'rule-leaf-params',
  {
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
  }
)

/**
 * 简单模式表单只用在这一条跨模式漂移锁里：两条渲染路径必须共用 `ParamField`，
 * 任何一边退化（多出兜底文本框 / 少了候选）都要在这里红。
 */
const SimpleForm = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/admin/badges/rule/SFAdminBadgeRuleSimpleForm.vue', import.meta.url)),
  'rule-simple-form-drift',
  {
    BADGE_RULE_LIMITS,
    createDefaultLeaf,
    isGroupNode,
    validateRuleDraft,
    buildFlatAllRule,
    flattenAllLeaves,
    leafSentence,
    computed,
    useI18n: i18nStub,
    SFAdminBadgeRuleParamField: ParamField
  }
)

/** 与后端目录一致：`activity.count` 的 `metric` 六个候选（wire 上是字符串数组）。 */
const METRIC_VALUES = [
  'topic_count',
  'comment_count',
  'like_received_count',
  'like_given_count',
  'bookmark_count',
  'days_visited'
]

const catalog: BadgeConditionCatalogEntry[] = [
  {
    key: 'activity.count',
    valueKind: 'count',
    compare: ['gte', 'lte'],
    labelZh: '计数',
    labelEn: 'Count',
    params: [
      // wire 形状：后端只下发取值，文案走 i18n（`admin.badges.metrics.<value>` 等）。
      { name: 'metric', kind: 'enum', required: true, options: METRIC_VALUES },
      { name: 'window', kind: 'window', required: true }
    ]
  },
  {
    key: 'identity.email_verified',
    valueKind: 'boolean',
    compare: [],
    labelZh: '邮箱已验证',
    labelEn: 'Email verified'
  },
  {
    key: 'object.authoring',
    valueKind: 'count',
    compare: ['gte'],
    labelZh: '分类发文',
    labelEn: 'Topics in category',
    params: [
      { name: 'targetKind', kind: 'enum', required: true, options: ['category', 'tag'] },
      { name: 'targetSlug', kind: 'objectRef', required: true, refKind: 'category' },
      { name: 'window', kind: 'window', required: true }
    ]
  }
]

function activityLeaf(params: Record<string, unknown> = { metric: 'topic_count', window: { kind: 'all' } }, value = 5): BadgeRuleLeaf {
  return { type: 'activity.count', params, cmp: { op: 'gte', value } }
}

function mountLeaf(
  node: BadgeRuleLeaf,
  items: BadgeConditionCatalogEntry[] = catalog,
  extra: Record<string, unknown> = {}
) {
  const rule: BadgeRule = { v: 1, root: node }
  return mount(Leaf, {
    props: { rule, node, path: [], depth: 1, catalog: items, ...extra },
    global: { stubs: { USelectMenu: SelectMenuStub } }
  })
}

/** objectRef 候选的三类形态：五项齐全 / 截断 / 不可用 / 空列表。 */
function refMap(overrides: Partial<BadgeRefCandidateMap> = {}): BadgeRefCandidateMap {
  return {
    category: {
      kind: 'category',
      candidates: [
        { value: 'news', label: '新闻', flags: [] },
        { value: 'archived-cat', label: '归档分类', flags: ['hidden'] }
      ],
      unavailable: false,
      truncated: false
    },
    ...overrides
  }
}

function lastEmittedLeaf(wrapper: ReturnType<typeof mountLeaf>): BadgeRuleLeaf {
  const events = wrapper.emitted('update:rule')
  expect(events).toBeDefined()
  const rule = events![events!.length - 1]![0] as BadgeRule
  return rule.root as BadgeRuleLeaf
}

describe('SFAdminBadgeRuleLeaf params', () => {
  test('按目录 params 渲染枚举、窗口与比较/阈值控件', () => {
    const wrapper = mountLeaf(activityLeaf())
    try {
      const metric = wrapper.find('[data-param="metric"]')
      expect(metric.exists()).toBe(true)
      expect(metric.findAll('option')).toHaveLength(METRIC_VALUES.length)
      expect((metric.find('select').element as HTMLSelectElement).value).toBe('topic_count')

      const windowField = wrapper.find('[data-param="window"]')
      expect((windowField.find('select').element as HTMLSelectElement).value).toBe('all')
      expect((wrapper.find('[data-rule-compare]').element as HTMLSelectElement).value).toBe('gte')
      expect((wrapper.find('[data-rule-value]').element as HTMLInputElement).value).toBe('5')
    } finally {
      wrapper.unmount()
    }
  })

  test('每个参数恰好一个控件：enum / window 下方不再多渲染兜底文本框（回归锁）', () => {
    const wrapper = mountLeaf(activityLeaf())
    try {
      expect(wrapper.findAll('[data-param="metric"] select')).toHaveLength(1)
      expect(wrapper.findAll('[data-param="window"] select')).toHaveLength(1)
      // 曾经的 bug：objectRef 分支另起一条 v-if 链后，尾部兜底 v-else 文本框对 enum / window
      // 也命中，指标格下多出显示 `days_visited`、窗口格下多出显示 `[object Object]` 的输入框。
      expect(wrapper.findAll('[data-param="metric"] input[type="text"]')).toHaveLength(0)
      expect(wrapper.findAll('[data-param="window"] input[type="text"]')).toHaveLength(0)
      expect(wrapper.findAll('[data-param="metric"] input')).toHaveLength(0)
      expect(wrapper.findAll('[data-param="window"] input')).toHaveLength(0)
      expect(wrapper.findAll('[data-param="metric"] .sf-rule-control')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('metric 下拉 6 项都有值有文案（文案是 i18n 键，不是 undefined 空白项）', () => {
    const wrapper = mountLeaf(activityLeaf())
    try {
      const options = wrapper.findAll('[data-param="metric"] option')
      expect(options).toHaveLength(METRIC_VALUES.length)
      expect(options.map(option => (option.element as HTMLOptionElement).value)).toEqual(METRIC_VALUES)
      // stub 的 `t` 返回键名：文案必须等于候选键，既不是空串也不是 undefined。
      expect(options.map(option => option.text())).toEqual(
        METRIC_VALUES.map(value => paramOptionLabelKey('metric', value)[0]!)
      )
    } finally {
      wrapper.unmount()
    }
  })

  test('选项文案按参数专属 → 共享指标 → 共享枚举依次回退，全缺失时回落原始值', () => {
    expect(paramOptionLabelKey('metric', 'topic_count')).toEqual([
      'admin.badges.conditions.params.metric.options.topic_count',
      'admin.badges.metrics.topic_count',
      'admin.badges.conditions.enums.topic_count'
    ])
    expect(resolveParamOptionLabel(
      'metric',
      'topic_count',
      key => `zh:${key}`,
      key => key === 'admin.badges.metrics.topic_count'
    )).toBe('zh:admin.badges.metrics.topic_count')
    // 插件条件的未知取值：没有任何键命中，回落原文，绝不留空白项。
    expect(resolveParamOptionLabel('metric', 'plugin_metric', key => key, () => false)).toBe('plugin_metric')
  })

  test('改 metric 只改 params.metric，比较符与阈值不动', async () => {
    const wrapper = mountLeaf(activityLeaf({ metric: 'topic_count', window: { kind: 'all' } }, 5))
    try {
      await wrapper.find('[data-param="metric"] select').setValue('like_given_count')
      const next = lastEmittedLeaf(wrapper)
      expect(next.params?.metric).toBe('like_given_count')
      expect(next.cmp).toEqual({ op: 'gte', value: 5 })
      expect(next.params?.window).toEqual({ kind: 'all' })
    } finally {
      wrapper.unmount()
    }
  })

  test('目录给了单位就显示后缀，days_visited 例外（「访问天数 ≥ 7 次」读不通）', () => {
    const withUnit = catalog.map(item => (item.key === 'activity.count'
      ? { ...item, unitZh: '次', unitEn: 'times' }
      : item))
    const counted = mountLeaf(activityLeaf(), withUnit)
    try {
      const valueWrap = counted.find('.sf-rule-field__value')
      expect(valueWrap.exists()).toBe(true)
      // 与简单模式同一套语言：input 与单位同父 = 同行，单位不再独占第三行。
      expect(counted.find('[data-rule-value]').element.parentElement).toBe(valueWrap.element)
      expect(valueWrap.find('.sf-rule-field__unit').text()).toBe('次')
      const fieldEl = counted.find('[data-rule-value]').element.closest('.sf-rule-field') as HTMLElement
      const unitAsOwnRow = Array.from(fieldEl.children)
        .some(child => child.classList.contains('sf-rule-field__unit'))
      expect(unitAsOwnRow).toBe(false)
    } finally {
      counted.unmount()
    }
    const days = mountLeaf(activityLeaf({ metric: 'days_visited', window: { kind: 'all' } }, 7), withUnit)
    try {
      expect(days.find('.sf-rule-field__unit').exists()).toBe(false)
    } finally {
      days.unmount()
    }
  })

  test('切换条件类型时按目录生成默认参数（不保留上一个条件的参数）', async () => {
    const wrapper = mountLeaf(activityLeaf())
    try {
      await wrapper.find('[data-rule-condition]').setValue('object.authoring')
      const next = lastEmittedLeaf(wrapper)
      expect(next.type).toBe('object.authoring')
      expect(next.params).toEqual({ targetKind: 'category', targetSlug: '', window: { kind: 'all' } })
    } finally {
      wrapper.unmount()
    }
  })

  /**
   * `objectRef` 的四种情形（线上 bug：原来是「只含占位 option 的空下拉」，值永远为空，
   * 保存必报「缺少必填参数」。现在候选可用 → 可搜索选择；其余一律文本输入）。
   */
  const refNode = (targetSlug = ''): BadgeRuleLeaf => ({
    type: 'object.authoring',
    params: { targetKind: 'category', targetSlug, window: { kind: 'all' } },
    cmp: { op: 'gte', value: 3 }
  })

  test('有候选：objectRef 渲染可搜索选择控件（不是文本框、不是空下拉）', () => {
    const wrapper = mountLeaf(refNode(), catalog, { refCandidates: refMap() })
    try {
      const target = wrapper.find('[data-param="targetSlug"]')
      const select = target.find('[data-ref-select]')
      expect(select.exists()).toBe(true)
      // 首用 USelectMenu：搜索、按 label/value 过滤、取值用 value、显示用 label。
      expect(select.attributes('data-search-input')).toBe('true')
      expect(select.attributes('data-filter-fields')).toBe('label,value')
      expect(select.attributes('data-value-key')).toBe('value')
      expect(select.attributes('data-label-key')).toBe('label')
      expect(select.attributes('data-clear')).toBe('false')
      expect(select.attributes('data-ref-kind')).toBe('category')
      expect(select.attributes('data-placeholder')).toBe('admin.badges.conditions.refCandidatesPlaceholder')
      // 候选真的渲染成可选项，隐藏标记只加后缀、仍然可选。
      expect(select.findAll('[data-option]').map(option => option.text())).toEqual([
        '新闻',
        `归档分类${'admin.badges.conditions.refFlagHidden'}`
      ])
      // 这条链上不许再出现任何文本框（曾经的兜底文本框就是这个 bug 的观感）。
      expect(target.find('input[type="text"]').exists()).toBe(false)
      expect(target.find('select').exists()).toBe(false)
      // data-param / data-param-kind 保留，新增 data-ref-kind。
      expect(target.attributes('data-param-kind')).toBe('objectRef')
    } finally {
      wrapper.unmount()
    }
  })

  test('有候选：选中一项把 slug 落进 params，其余参数不动', async () => {
    const wrapper = mountLeaf(refNode(), catalog, { refCandidates: refMap() })
    try {
      await wrapper.find('[data-param="targetSlug"] [data-option="news"]').trigger('click')
      const next = lastEmittedLeaf(wrapper)
      expect(next.params).toEqual({ targetKind: 'category', targetSlug: 'news', window: { kind: 'all' } })
    } finally {
      wrapper.unmount()
    }
  })

  test('截断：追加「仅显示前 500 条」提示，控件仍是选择器', () => {
    const map = refMap({
      category: {
        kind: 'category',
        candidates: [{ value: 'news', label: '新闻', flags: [] }],
        unavailable: false,
        truncated: true
      }
    })
    const wrapper = mountLeaf(refNode(), catalog, { refCandidates: map })
    try {
      const target = wrapper.find('[data-param="targetSlug"]')
      expect(target.find('[data-ref-select]').exists()).toBe(true)
      expect(target.find('[data-ref-truncated]').text()).toBe('admin.badges.conditions.refTruncated')
    } finally {
      wrapper.unmount()
    }
  })

  test('不可用 / 空候选：退成文本框 + 提示，绝不留空下拉', async () => {
    const unavailable: BadgeRefCandidateMap = {
      category: { kind: 'category', candidates: [], unavailable: true, truncated: false }
    }
    const empty: BadgeRefCandidateMap = {
      category: { kind: 'category', candidates: [], unavailable: false, truncated: false }
    }
    // 三态在 DOM 上必须可分辨：判据走 `data-ref-manual-state`，不靠文案字符串。
    const cases: Array<[BadgeRefCandidateMap, 'unavailable' | 'empty']> = [
      [unavailable, 'unavailable'],
      [empty, 'empty'],
      // 整个字段缺失（老后端）：`{}` → 每一项都是「取不到」，不是「本来没有」。
      [{} as BadgeRefCandidateMap, 'unavailable']
    ]
    for (const [map, state] of cases) {
      const wrapper = mountLeaf(refNode(), catalog, { refCandidates: map })
      try {
        const target = wrapper.find('[data-param="targetSlug"]')
        const manual = target.find('input[data-ref-manual]')
        expect(manual.exists()).toBe(true)
        expect(manual.attributes('data-ref-kind')).toBe('category')
        expect(manual.attributes('data-ref-manual-state')).toBe(state)
        // 「取不到」与「本来就没有」必须说两句不同的话（上一版共用一句 = 把正常状态说成故障）。
        expect(target.find('[data-ref-manual-hint]').text()).toBe(state === 'empty'
          ? 'admin.badges.conditions.refEmptyHint'
          : 'admin.badges.conditions.refUnavailableHint')
        expect(target.find('[data-ref-unknown-kind]').exists()).toBe(false)
        // 不再有任何下拉（含只带占位 option 的那种）。
        expect(target.find('[data-ref-select]').exists()).toBe(false)
        expect(target.find('select').exists()).toBe(false)
        await manual.setValue('news')
        expect(lastEmittedLeaf(wrapper).params?.targetSlug).toBe('news')
      } finally {
        wrapper.unmount()
      }
    }
  })

  test('白名单外的 refKind：给可见诊断，不是静默文本框', () => {
    // 插件把 refKind 拼成 `categories`（或用了前端还不认识的类型）：候选永远取不到。
    const odd: BadgeConditionCatalogEntry[] = [{
      key: 'object.authoring',
      valueKind: 'count',
      compare: ['gte'],
      labelZh: '分类发文',
      labelEn: 'Topics in category',
      params: [
        { name: 'targetKind', kind: 'enum', required: true, options: ['category', 'tag'] },
        { name: 'targetSlug', kind: 'objectRef', required: true, refKind: 'categories' }
      ]
    }]
    const wrapper = mountLeaf(refNode(), odd, { refCandidates: refMap() })
    try {
      const target = wrapper.find('[data-param="targetSlug"]')
      const unknown = target.find('[data-ref-unknown-kind]')
      expect(unknown.exists()).toBe(true)
      expect(unknown.text()).toContain('admin.badges.conditions.refUnknownKind')
      expect(unknown.attributes('data-ref-kind')).toBe('categories')
      // 值仍可见可改（老规则可能已经存了这个参数），但提示先讲清根因。
      expect(target.find('input[data-ref-manual]').exists()).toBe(true)
      // 一句笼统的「候选读不到」会盖掉根因，这里不许同时出现。
      expect(target.find('[data-ref-manual-hint]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('失效值：保留原值并标「（失效）」，不静默清空', () => {
    const wrapper = mountLeaf(refNode('legacy-category'), catalog, { refCandidates: refMap() })
    try {
      const options = wrapper.find('[data-param="targetSlug"]').findAll('[data-option]')
      expect(options.map(option => option.text())).toEqual([
        '新闻',
        `归档分类${'admin.badges.conditions.refFlagHidden'}`,
        `legacy-category${'admin.badges.conditions.refStale'}`
      ])
      const stale = options[options.length - 1]!
      expect(stale.attributes('data-option')).toBe('legacy-category')
      // 打开弹窗 / 渲染候选不是一次写操作：失效值只展示，不 emit。
      expect(wrapper.emitted('update:rule')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })

  test('目录未命中的条件渲染为只读失效块（原始 key + 提示），不渲染空控件', () => {
    const wrapper = mountLeaf({ type: 'plugin.removed_fact', params: {}, cmp: { op: 'gte', value: 3 } })
    try {
      const missing = wrapper.find('[data-rule-leaf-missing]')
      expect(missing.exists()).toBe(true)
      expect(missing.text()).toContain('plugin.removed_fact')
      expect(missing.text()).toContain('admin.badges.conditions.unknownHint')
      // 失效节点没有可编辑控件：改不了就明确只读，别给一个永远对不上的下拉。
      expect(wrapper.find('[data-rule-condition]').exists()).toBe(false)
      expect(wrapper.find('[data-rule-compare]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('bool 参数渲染开关、string 参数渲染文本框，未知 kind 只给只读提示', () => {
    const pluginCatalog: BadgeConditionCatalogEntry[] = [{
      key: 'plugin.flags',
      valueKind: 'count',
      compare: ['gte'],
      labelZh: '插件开关',
      labelEn: 'Plugin flags',
      params: [
        { name: 'enabled', kind: 'bool', required: false },
        { name: 'note', kind: 'string', required: false }
      ]
    }]
    const wrapper = mountLeaf(
      { type: 'plugin.flags', params: { enabled: true, note: 'hello' }, cmp: { op: 'gte', value: 1 } },
      pluginCatalog
    )
    try {
      expect(wrapper.find('[data-param="enabled"] input[type="checkbox"]').exists()).toBe(true)
      expect(wrapper.find('[data-param="note"] input[type="text"]').exists()).toBe(true)
      expect(wrapper.findAll('[data-param-unsupported]')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('简单模式 / 高级模式共用同一套参数控件', () => {
  /** 两条路径的同一叶子：返回 [标签, wrapper] 便于逐条比同一批断言。 */
  function renderBoth(node: BadgeRuleLeaf, extra: { catalog?: BadgeConditionCatalogEntry[], factCandidates?: unknown } = {}) {
    const items = extra.catalog ?? catalog
    const advanced = mount(Leaf, {
      props: {
        rule: { v: 1, root: node },
        node,
        path: [],
        depth: 1,
        catalog: items,
        factCandidates: extra.factCandidates ?? []
      }
    })
    const simple = mount(SimpleForm, {
      props: {
        rule: { v: 1, root: { op: 'all', children: [node] } },
        catalog: items,
        factCandidates: extra.factCandidates ?? []
      },
      global: { stubs: { UIcon: UIconStub } }
    })
    return [['advanced', advanced], ['simple', simple]] as const
  }

  test('enum + window(kind=all)：两侧控件与顺序一致', () => {
    const pairs = renderBoth(activityLeaf())
    try {
      for (const [name, wrapper] of pairs) {
        expect({ name, metricSelects: wrapper.findAll('[data-param="metric"] select').length })
          .toEqual({ name, metricSelects: 1 })
        expect({ name, windowSelects: wrapper.findAll('[data-param="window"] select').length })
          .toEqual({ name, windowSelects: 1 })
        expect({ name, textInputs: wrapper.findAll('[data-param] input[type="text"]').length })
          .toEqual({ name, textInputs: 0 })
        expect({ name, options: wrapper.findAll('[data-param="metric"] option').length })
          .toEqual({ name, options: METRIC_VALUES.length })
        expect({ name, params: wrapper.findAll('[data-param]').map(el => el.attributes('data-param')) })
          .toEqual({ name, params: ['metric', 'window'] })
      }
    } finally {
      for (const [, wrapper] of pairs) wrapper.unmount()
    }
  })

  test('window(recent_days/days=12)：两侧都渲染 days 输入且值相同', () => {
    // kind=all 时两侧的 days 输入都是 0 个，对「一边漏了 days 输入」不敏感；
    // 这里用 recent_days 把那条输入真正逼出来。
    const node = activityLeaf({ metric: 'topic_count', window: { kind: 'recent_days', days: 12 } })
    const pairs = renderBoth(node)
    try {
      for (const [name, wrapper] of pairs) {
        const daysInputs = wrapper.findAll('[data-param="window"] input[type="number"]')
        expect({ name, daysInputs: daysInputs.length }).toEqual({ name, daysInputs: 1 })
        expect({ name, days: (daysInputs[0]!.element as HTMLInputElement).value }).toEqual({ name, days: '12' })
        expect({ name, selects: wrapper.findAll('[data-param="window"] select').length })
          .toEqual({ name, selects: 1 })
      }
    } finally {
      for (const [, wrapper] of pairs) wrapper.unmount()
    }
  })

  test('fact.count：候选下拉、变体、窗口与失效提示在两侧呈现一致', () => {
    const factCatalog: BadgeConditionCatalogEntry[] = [{
      key: 'fact.count',
      valueKind: 'count',
      compare: ['gte', 'lte'],
      labelZh: '插件事实计数',
      labelEn: 'Plugin fact count',
      params: [
        { name: 'factKey', kind: 'objectRef', required: true, refKind: 'fact', options: ['example.tasks', 'gone.key'] },
        { name: 'variant', kind: 'enum', required: false, options: [] },
        { name: 'window', kind: 'window', required: false }
      ]
    }]
    const candidates = [{
      key: 'example.tasks',
      labelZh: '完成新手任务数',
      labelEn: 'Completed tasks',
      unitZh: '个',
      unitEn: 'tasks',
      compare: ['gte'],
      variants: [
        { value: 'beginner', labelZh: '入门', labelEn: 'Beginner' },
        { value: 'advanced', labelZh: '进阶', labelEn: 'Advanced' }
      ]
    }]
    const fresh: BadgeRuleLeaf = {
      type: 'fact.count',
      params: { factKey: 'example.tasks', variant: 'beginner', window: { kind: 'recent_days', days: 9 } },
      cmp: { op: 'gte', value: 3 }
    }
    const stale: BadgeRuleLeaf = {
      type: 'fact.count',
      params: { factKey: 'gone.key', variant: '', window: { kind: 'recent_days', days: 9 } },
      cmp: { op: 'gte', value: 3 }
    }

    for (const node of [fresh, stale]) {
      // 失效 key 的变体无从判断：`variant` 整项隐藏（两侧必须同时隐藏，否则就是漂移）。
      const expectedParams = node === stale ? ['factKey', 'window'] : ['factKey', 'variant', 'window']
      const pairs = renderBoth(node, { catalog: factCatalog, factCandidates: candidates })
      try {
        for (const [name, wrapper] of pairs) {
          expect({ name, params: wrapper.findAll('[data-param]').map(el => el.attributes('data-param')) })
            .toEqual({ name, params: expectedParams })
          expect({ name, selects: wrapper.findAll('[data-param] select').length })
            .toEqual({ name, selects: expectedParams.length })
          expect({ name, numbers: wrapper.findAll('[data-param] input[type="number"]').length })
            .toEqual({ name, numbers: 1 })
          expect({ name, stale: wrapper.findAll('[data-fact-stale]').length })
            .toEqual({ name, stale: node === stale ? 1 : 0 })
        }
      } finally {
        for (const [, wrapper] of pairs) wrapper.unmount()
      }
    }
  })
})
