import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, ref, toRaw } from 'vue'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import type {
  BadgeConditionCatalogEntry,
  BadgeRule,
  BadgeRuleGroup,
  BadgeRuleNode
} from '../../app/utils/admin/badgeRule'
import {
  applyFactKeySelection,
  factCandidateUnit,
  factKeyMissing,
  factKeyOptions,
  factVariantOptions
} from '../../app/utils/admin/badgeFactCandidates'
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
  wrapWithGroup
} from '../../app/utils/admin/badgeRule'

/**
 * 规则编辑器的行为契约：树递归、not 包裹/解包、上限在 UI 层拦截、发出对象不可变。
 * 文案一律用 i18n 键断言（stub 的 `t` 返回键名），避免在这里抄第二份中文。
 */
const i18nStub = () => ({
  t: (key: string) => key,
  te: () => true,
  locale: { value: 'zh-CN' }
})

const toastCalls: unknown[] = []
const useToastStub = () => ({ add: (payload: unknown) => { toastCalls.push(payload) } })

const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })

const ruleDir = new URL('../../app/components/admin/badges/rule/', import.meta.url)

const Leaf = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleLeaf.vue', ruleDir)), 'rule-leaf', {
  createDefaultLeaf,
  replaceNode,
  computed,
  ref,
  useI18n: i18nStub,
  // fact.count 的两级下拉读这组纯函数（import 被剥离，必须按名注入）。
  applyFactKeySelection,
  factCandidateUnit,
  factKeyMissing,
  factKeyOptions,
  factVariantOptions
})

const JsonPreview = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleJsonPreview.vue', ruleDir)), 'rule-json', {
  computed,
  ref,
  useI18n: i18nStub,
  useToast: useToastStub
})

const Group = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleGroup.vue', ruleDir)), 'rule-group', {
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

const Editor = await compileVueSfc(fileURLToPath(new URL('SFAdminBadgeRuleEditor.vue', ruleDir)), 'rule-editor', {
  isGroupNode,
  computed,
  ref,
  useI18n: i18nStub,
  SFAdminBadgeRuleGroup: Group,
  SFAdminBadgeRuleLeaf: Leaf,
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
      {
        name: 'metric',
        kind: 'enum',
        required: true,
        options: [
          { value: 'topic_count', labelZh: '主题数', labelEn: 'Topics' },
          { value: 'comment_count', labelZh: '回复数', labelEn: 'Comments' }
        ]
      },
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
      {
        name: 'targetKind',
        kind: 'enum',
        required: true,
        options: [
          { value: 'category', labelZh: '分类', labelEn: 'Category' },
          { value: 'tag', labelZh: '标签', labelEn: 'Tag' }
        ]
      },
      { name: 'targetSlug', kind: 'objectRef', required: true, refKind: 'category' },
      { name: 'window', kind: 'window', required: true }
    ]
  }
]

function ruleOf(root: BadgeRuleNode): BadgeRule {
  return { v: 1, root }
}

function leaf(type: string, value?: number, params: Record<string, unknown> = {}): BadgeRuleNode {
  const node = createLeaf(type, params)
  return value === undefined ? node : { ...node, cmp: { op: 'gte', value } }
}

function group(op: 'all' | 'any' | 'not', children: BadgeRuleNode[] = []): BadgeRuleNode {
  return createGroup(op, children)
}

function mountEditor(rule: BadgeRule, items: BadgeConditionCatalogEntry[] = catalog) {
  return mount(Editor, {
    props: { modelValue: rule, catalog: items },
    global: {
      // 组组件递归渲染自身：编译后的对象按名字注册，运行时 resolveComponent 才能找到。
      components: { SFAdminBadgeRuleGroup: Group },
      stubs: { UIcon: UIconStub }
    }
  })
}

function buttonByLabel(wrapper: ReturnType<typeof mountEditor>, label: string, index = 0) {
  const matches = wrapper.findAll('button').filter(button => button.attributes('aria-label') === label)
  return matches[index]!
}

function lastEmitted(wrapper: ReturnType<typeof mountEditor>): BadgeRule {
  const events = wrapper.emitted('update:modelValue')
  expect(events).toBeDefined()
  return events![events!.length - 1]![0] as BadgeRule
}

function childrenOf(rule: BadgeRule): BadgeRuleNode[] {
  if (!isGroupNode(rule.root)) throw new Error('expected a group root')
  return rule.root.children
}

describe('SFAdminBadgeRuleEditor tree', () => {
  test('三层嵌套按 all → any → not 递归渲染，深度逐层 +1', () => {
    const rule = ruleOf(group('all', [group('any', [group('not', [leaf('identity.email_verified')])])]))
    const wrapper = mountEditor(rule)
    try {
      const groups = wrapper.findAll('[data-rule-group]')
      expect(groups.map(node => node.attributes('data-rule-group'))).toEqual(['all', 'any', 'not'])
      expect(groups.map(node => node.attributes('data-depth'))).toEqual(['1', '2', '3'])
      expect(wrapper.findAll('[data-rule-leaf]')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('空组渲染「至少需要 1 个条件」的保存提示', () => {
    const wrapper = mountEditor(ruleOf(group('all', [])))
    try {
      expect(wrapper.text()).toContain('admin.badges.rule.emptyGroup')
    } finally {
      wrapper.unmount()
    }
  })

  test('not 组没有「添加子节点」入口', () => {
    const rule = ruleOf(group('all', [group('not', [leaf('identity.email_verified')])]))
    const wrapper = mountEditor(rule)
    try {
      const notGroup = wrapper.find('[data-rule-group="not"]')
      expect(notGroup.find('[data-rule-add-condition]').exists()).toBe(false)
      expect(notGroup.find('[data-rule-add-group]').exists()).toBe(false)
      expect(notGroup.find('button[aria-label="admin.badges.rule.unwrap"]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('把条件包进 not，再解包回原条件', async () => {
    const rule = ruleOf(group('all', [leaf('activity.count', 1, { metric: 'topic_count', window: { kind: 'all' } })]))
    const wrapper = mountEditor(rule)
    try {
      await buttonByLabel(wrapper, 'admin.badges.rule.wrapNot').trigger('click')
      const wrapped = lastEmitted(wrapper)
      const notNode = childrenOf(wrapped)[0] as BadgeRuleGroup
      expect(notNode.op).toBe('not')
      expect((notNode.children[0] as { type: string }).type).toBe('activity.count')
      // 入参没被就地改
      expect(childrenOf(rule)[0]).not.toHaveProperty('op')

      wrapper.unmount()
      const remounted = mountEditor(wrapped)
      try {
        await buttonByLabel(remounted, 'admin.badges.rule.unwrap').trigger('click')
        const unwrapped = lastEmitted(remounted)
        expect(childrenOf(unwrapped)[0]).toEqual(notNode.children[0]!)
        expect(childrenOf(unwrapped)[0]).not.toHaveProperty('op')
      } finally {
        remounted.unmount()
      }
    } finally {
      wrapper.unmount()
    }
  })

  test('删除 not 的唯一子节点时连 not 一起移除，不留下修不好的空 not', async () => {
    const rule = ruleOf(group('all', [group('not', [leaf('identity.email_verified')])]))
    const wrapper = mountEditor(rule)
    try {
      // 第一个 remove 属于 not 组本身（父组的子节点），第二个才是 not 的子节点。
      await buttonByLabel(wrapper, 'admin.badges.rule.remove', 1).trigger('click')
      expect(childrenOf(lastEmitted(wrapper))).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('删除普通子节点只移除该节点', async () => {
    const rule = ruleOf(group('all', [leaf('activity.count', 1, { metric: 'topic_count', window: { kind: 'all' } }), leaf('identity.email_verified')]))
    const wrapper = mountEditor(rule)
    try {
      await buttonByLabel(wrapper, 'admin.badges.rule.remove', 0).trigger('click')
      const children = childrenOf(lastEmitted(wrapper))
      expect(children).toHaveLength(1)
      expect((children[0] as { type: string }).type).toBe('identity.email_verified')
    } finally {
      wrapper.unmount()
    }
  })

  test('添加条件后发出的文档与入参不可变，未触及的子树保持引用共享', async () => {
    const first = leaf('activity.count', 1, { metric: 'topic_count', window: { kind: 'all' } })
    const rule = ruleOf(group('all', [first]))
    const snapshot = JSON.stringify(rule)
    const wrapper = mountEditor(rule)
    try {
      await buttonByLabel(wrapper, 'admin.badges.rule.addCondition').trigger('click')
      const next = lastEmitted(wrapper)
      expect(next).not.toBe(rule)
      expect(next.root).not.toBe(rule.root)
      expect(childrenOf(next)).toHaveLength(2)
      expect(toRaw(childrenOf(next)[0]!)).toBe(first)
      expect(JSON.stringify(rule)).toBe(snapshot)
      expect(childrenOf(rule)).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFAdminBadgeRuleEditor limits', () => {
  function chain(depth: number): BadgeRuleNode {
    let node: BadgeRuleNode = leaf('activity.count', 1, { metric: 'topic_count', window: { kind: 'all' } })
    for (let remaining = depth - 1; remaining > 0; remaining -= 1) node = group('all', [node])
    return node
  }

  test('组已经在第 5 层时添加按钮禁用，title 说明原因', () => {
    // 第 5 层组再挂子节点就是第 6 层：草稿里允许存在，但按钮必须拦住。
    const wrapper = mountEditor(ruleOf(chain(BADGE_RULE_LIMITS.maxDepth + 1)))
    try {
      const deepest = wrapper.find(`[data-depth="${BADGE_RULE_LIMITS.maxDepth}"]`)
      const add = deepest.find('[data-rule-add-condition]')
      expect(add.attributes('disabled')).toBeDefined()
      expect(add.attributes('title')).toBe('admin.badges.rule.limitDepth')
    } finally {
      wrapper.unmount()
    }
  })

  test('叶子到 32 时添加按钮禁用', () => {
    const leaves = Array.from({ length: BADGE_RULE_LIMITS.maxLeaves }, () => leaf('identity.email_verified'))
    const wrapper = mountEditor(ruleOf(group('all', leaves)))
    try {
      const add = wrapper.find('[data-rule-add-condition]')
      expect(add.attributes('disabled')).toBeDefined()
      expect(add.attributes('title')).toBe('admin.badges.rule.limitLeaves')
    } finally {
      wrapper.unmount()
    }
  })

  test('节点数到 64 时添加按钮禁用（叶子还没到 32）', () => {
    // 22 个「两层组 + 叶子」：节点 1 + 22*3 = 67，叶子 22。
    const children = Array.from({ length: 22 }, () => group('all', [group('all', [leaf('identity.email_verified')])]))
    const wrapper = mountEditor(ruleOf(group('all', children)))
    try {
      const add = wrapper.find('[data-rule-add-condition]')
      expect(add.attributes('disabled')).toBeDefined()
      expect(add.attributes('title')).toBe('admin.badges.rule.limitNodes')
    } finally {
      wrapper.unmount()
    }
  })

  test('未到上限时按钮可用，title 是动作名', () => {
    const wrapper = mountEditor(ruleOf(group('all', [leaf('identity.email_verified')])))
    try {
      const add = wrapper.find('[data-rule-add-condition]')
      expect(add.attributes('disabled')).toBeUndefined()
      expect(add.attributes('title')).toBe('admin.badges.rule.addCondition')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFAdminBadgeRuleLeaf params', () => {
  test('按目录 params 渲染枚举、窗口与比较/阈值控件', () => {
    const rule = ruleOf(leaf('activity.count', 5, { metric: 'topic_count', window: { kind: 'all' } }))
    const wrapper = mountEditor(rule)
    try {
      const metric = wrapper.find('[data-param="metric"]')
      expect(metric.exists()).toBe(true)
      expect(metric.findAll('option')).toHaveLength(2)
      expect((metric.find('select').element as HTMLSelectElement).value).toBe('topic_count')

      const windowField = wrapper.find('[data-param="window"]')
      expect((windowField.find('select').element as HTMLSelectElement).value).toBe('all')
      expect((wrapper.find('[data-rule-compare]').element as HTMLSelectElement).value).toBe('gte')
      expect((wrapper.find('[data-rule-value]').element as HTMLInputElement).value).toBe('5')
    } finally {
      wrapper.unmount()
    }
  })

  test('切换条件类型时按目录生成默认参数（不保留上一个条件的参数）', async () => {
    const rule = ruleOf(group('all', [leaf('activity.count', 1, { metric: 'topic_count', window: { kind: 'all' } })]))
    const wrapper = mountEditor(rule)
    try {
      const select = wrapper.find('[data-rule-condition]')
      await select.setValue('object.authoring')
      const next = childrenOf(lastEmitted(wrapper))[0] as { type: string, params: Record<string, unknown> }
      expect(next.type).toBe('object.authoring')
      expect(next.params).toEqual({ targetKind: 'category', targetSlug: '', window: { kind: 'all' } })
    } finally {
      wrapper.unmount()
    }
  })

  test('objectRef 只渲染「候选待下发」占位，不提供自由输入', () => {
    const rule = ruleOf(leaf('object.authoring', 3, { targetKind: 'category', targetSlug: '', window: { kind: 'all' } }))
    const wrapper = mountEditor(rule)
    try {
      const target = wrapper.find('[data-param="targetSlug"]')
      expect(target.find('input[type="text"]').exists()).toBe(false)
      const options = target.findAll('option')
      expect(options).toHaveLength(1)
      expect(options[0]!.text()).toBe('admin.badges.conditions.candidatesPending')
    } finally {
      wrapper.unmount()
    }
  })

  test('目录未命中的条件渲染为只读失效块（原始 key + 提示），不渲染空控件', () => {
    const rule = ruleOf(group('all', [leaf('plugin.removed_fact', 3)]))
    const wrapper = mountEditor(rule)
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

  test('目录为空时显示提示而不是空白树', () => {
    const rule = ruleOf(group('all', [leaf('activity.count', 1)]))
    const wrapper = mountEditor(rule, [])
    try {
      expect(wrapper.text()).toContain('admin.badges.conditions.empty')
      expect(wrapper.find('[data-rule-group]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFAdminBadgeRuleJsonPreview', () => {
  test('只读渲染当前规则并复制到剪贴板', async () => {
    const written: string[] = []
    Object.defineProperty(globalThis.navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async (text: string) => { written.push(text) } }
    })
    const rule = ruleOf(group('all', [leaf('identity.email_verified')]))
    const wrapper = mount(JsonPreview, { props: { rule }, global: { stubs: { UIcon: UIconStub } } })
    try {
      expect(wrapper.find('pre').text()).toContain('"v": 1')
      expect(wrapper.find('pre').text()).toContain('identity.email_verified')
      toastCalls.length = 0
      await wrapper.find('[data-rule-copy]').trigger('click')
      expect(written[0]).toContain('"identity.email_verified"')
      expect(toastCalls).toHaveLength(1)
      expect((toastCalls[0] as { title: string }).title).toBe('admin.badges.rule.copied')
    } finally {
      wrapper.unmount()
    }
  })
})
