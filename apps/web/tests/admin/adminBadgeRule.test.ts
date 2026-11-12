import { describe, expect, test } from 'bun:test'

import type {
  BadgeConditionCatalogEntry,
  BadgeConditionParam,
  BadgeRule,
  BadgeRuleNode,
  BadgeRuleRefKind
} from '../../app/utils/admin/badgeRule'
import {
  BADGE_RULE_LIMITS,
  createGroup,
  createLeaf,
  describeRule,
  insertChild,
  isGroupNode,
  nodeAt,
  removeNode,
  replaceNode,
  validateRuleDraft,
  wrapWithGroup
} from '../../app/utils/admin/badgeRule'

/**
 * 参数层类型从 `badgeRule.ts` re-export（拆文件后 import 面不变）：这里显式用一次，
 * 让 typecheck 守住这条契约。
 */
const roleRefKind: BadgeRuleRefKind = 'role'
const roleParam: BadgeConditionParam = { name: 'roleKey', kind: 'objectRef', required: true, refKind: roleRefKind }
/** provider 是「已安装/已声明」的候选集合，同样是 objectRef，不是自由文本。 */
const providerParam: BadgeConditionParam = { name: 'providerId', kind: 'objectRef', required: true, refKind: 'provider' }

/** 目录形状与 `GET /admin/badges/conditions` 单条响应一致（宿主已扁平化 i18n）。 */
const catalog: BadgeConditionCatalogEntry[] = [
  { key: 'activity.count', valueKind: 'count', compare: ['gte', 'lte'], labelZh: '计数', labelEn: 'Count' },
  { key: 'activity.streak', valueKind: 'count', compare: ['gte'], labelZh: '连续访问', labelEn: 'Streak' },
  { key: 'identity.email_verified', valueKind: 'boolean', compare: [], labelZh: '邮箱已验证', labelEn: 'Email verified' },
  {
    key: 'object.authoring',
    valueKind: 'count',
    compare: ['gte'],
    labelZh: '在分类发文',
    labelEn: 'Topics in category',
    describeZh: '在「{param.category}」发文 {value} 篇',
    describeEn: '{value} topics in {param.category}',
    params: [
      {
        name: 'category',
        kind: 'enum',
        required: true,
        options: [
          { value: '公告', labelZh: '公告', labelEn: 'Announcements' },
          { value: 'news', labelZh: '新闻', labelEn: 'News' }
        ]
      },
      { name: 'days', kind: 'int', required: false, min: 1, max: 3650 }
    ]
  },
  {
    key: 'plugin.fact.count',
    valueKind: 'count',
    compare: ['gte', 'lte'],
    labelZh: '插件事实',
    labelEn: 'Plugin fact',
    params: [
      { name: 'window', kind: 'window', required: true },
      { name: 'limit', kind: 'int', required: false, min: 5, max: 50 },
      { name: 'target', kind: 'objectRef', required: false }
    ]
  },
  {
    key: 'identity.oauth_link',
    valueKind: 'boolean',
    compare: [],
    labelZh: '已绑定 OAuth',
    labelEn: 'OAuth linked',
    params: [providerParam]
  },
  {
    key: 'identity.role',
    valueKind: 'boolean',
    compare: [],
    labelZh: '拥有角色',
    labelEn: 'Has role',
    params: [roleParam]
  },
  {
    // string / bool 内置条件未使用，只为 M3 插件条件预留——用 plugin.* 夹具演示。
    key: 'plugin.text_fact',
    valueKind: 'boolean',
    compare: [],
    labelZh: '插件文本事实',
    labelEn: 'Plugin text fact',
    params: [{ name: 'note', kind: 'string', required: true }]
  },
  {
    key: 'plugin.bool_fact',
    valueKind: 'boolean',
    compare: [],
    labelZh: '插件布尔事实',
    labelEn: 'Plugin bool fact',
    params: [{ name: 'flag', kind: 'bool', required: true }]
  }
]

function leaf(type: string, value?: number, params: Record<string, unknown> = {}): BadgeRuleNode {
  const node = createLeaf(type, params)
  return value === undefined ? node : { ...node, cmp: { op: 'gte', value } }
}

function leafWith(type: string, op: 'gte' | 'lte', value: number, params: Record<string, unknown> = {}): BadgeRuleNode {
  return { ...createLeaf(type, params), cmp: { op, value } }
}

function group(op: 'all' | 'any' | 'not', children: BadgeRuleNode[] = []): BadgeRuleNode {
  return createGroup(op, children)
}

function ruleOf(root: BadgeRuleNode): BadgeRule {
  return { v: 1, root }
}

/** 链式嵌套到指定深度：depth 1 = 叶子自己，depth 5 = 4 层组 + 叶子。 */
function ruleOfDepth(depth: number): BadgeRule {
  let node: BadgeRuleNode = leaf('activity.count', 10)
  for (let remaining = depth - 1; remaining > 0; remaining -= 1) {
    node = group('all', [node])
  }
  return ruleOf(node)
}

function childrenOf(node: BadgeRuleNode): BadgeRuleNode[] {
  if (!isGroupNode(node)) throw new Error('expected a group node')
  return node.children
}

function codesOf(rule: BadgeRule) {
  return validateRuleDraft(rule, catalog).errors.map(error => error.code)
}

describe('badgeRule AST 原语', () => {
  test('createLeaf / createGroup 复制入参，不与调用方共享引用', () => {
    const params = { metric: 'topic_count' }
    const created = createLeaf('activity.count', params)
    params.metric = 'comment_count'
    expect(created).toEqual({ type: 'activity.count', params: { metric: 'topic_count' } })

    const children = [leaf('activity.count', 1)]
    const createdGroup = createGroup('all', children)
    children.push(leaf('activity.count', 2))
    expect(childrenOf(createdGroup)).toHaveLength(1)
    expect(createdGroup.op).toBe('all')
  })

  test('nodeAt 按数字索引路径取节点，越界与叶子路径返回 null', () => {
    const rule = ruleOf(group('all', [group('any', [leaf('activity.count', 3)])]))
    expect(nodeAt(rule, [])).toBe(rule.root)
    expect(nodeAt(rule, [0, 0])).toEqual({ type: 'activity.count', params: {}, cmp: { op: 'gte', value: 3 } })
    expect(nodeAt(rule, [0, 0, 0])).toBeNull()
    expect(nodeAt(rule, [5])).toBeNull()
  })
})

describe('badgeRule 不可变编辑', () => {
  const original = ruleOf(group('all', [
    group('any', [leaf('activity.count', 10)]),
    leaf('identity.email_verified')
  ]))
  const snapshot = JSON.stringify(original)

  test('insertChild 追加到目标组，只克隆路径上的节点', () => {
    const next = insertChild(original, [0], leaf('activity.count', 20))
    expect(childrenOf(childrenOf(next.root)[0] as BadgeRuleNode).map(node => (node as { cmp?: { value: number } }).cmp?.value))
      .toEqual([10, 20])
    expect(next).not.toBe(original)
    // 未触及的兄弟子树保持引用共享
    expect(childrenOf(next.root)[1]).toBe(childrenOf(original.root)[1])
  })

  test('insertChild 指向叶子或越界路径时原样返回', () => {
    expect(insertChild(original, [1], leaf('activity.count', 1))).toBe(original)
    expect(insertChild(original, [9], leaf('activity.count', 1))).toBe(original)
  })

  test('removeNode 删除目标节点，删根与越界是空操作', () => {
    const removed = removeNode(original, [0, 0])
    expect(childrenOf(childrenOf(removed.root)[0] as BadgeRuleNode)).toHaveLength(0)
    expect(removeNode(original, [])).toBe(original)
    expect(removeNode(original, [7])).toBe(original)
  })

  test('replaceNode 替换节点，也允许替换整棵根', () => {
    const replaced = replaceNode(original, [1], leaf('activity.streak', 5))
    expect(childrenOf(replaced.root)[1]).toEqual({ type: 'activity.streak', params: {}, cmp: { op: 'gte', value: 5 } })

    const replacedRoot = replaceNode(original, [], group('not', [leaf('identity.email_verified')]))
    expect(replacedRoot.root).toEqual({ op: 'not', children: [{ type: 'identity.email_verified', params: {} }] })
  })

  test('wrapWithGroup 把目标节点包进指定 op 的组', () => {
    const wrapped = wrapWithGroup(original, [1], 'not')
    expect(childrenOf(wrapped.root)[1]).toEqual({
      op: 'not',
      children: [{ type: 'identity.email_verified', params: {} }]
    })
    // 包根同样成立
    expect(wrapWithGroup(original, [], 'any').root).toEqual({ op: 'any', children: [original.root] })
  })

  test('四个操作都不改动原对象（深比较 + 引用）', () => {
    insertChild(original, [0], leaf('activity.count', 1))
    removeNode(original, [0])
    replaceNode(original, [1], leaf('activity.streak', 2))
    wrapWithGroup(original, [1], 'not')
    expect(JSON.stringify(original)).toBe(snapshot)
    expect(childrenOf(original.root)[0]).toEqual({ op: 'any', children: [{ type: 'activity.count', params: {}, cmp: { op: 'gte', value: 10 } }] })
  })
})

describe('badgeRule validateRuleDraft', () => {
  test('三层嵌套（all → any → not → 叶子）通过', () => {
    const rule = ruleOf(group('all', [
      group('any', [
        group('not', [leaf('identity.email_verified')]),
        leaf('activity.count', 10)
      ]),
      leaf('activity.streak', 3)
    ]))
    expect(validateRuleDraft(rule, catalog)).toEqual({ ok: true, errors: [] })
  })

  test('not 只允许恰好 1 个子节点', () => {
    expect(codesOf(ruleOf(group('not', [])))).toContain('rule.not_children')
    expect(codesOf(ruleOf(group('not', [leaf('activity.count', 1), leaf('activity.count', 2)]))))
      .toContain('rule.not_children')
    expect(validateRuleDraft(ruleOf(group('not', [leaf('identity.email_verified')])), catalog).ok).toBe(true)
  })

  test('深度 5 通过、6 拒绝', () => {
    expect(validateRuleDraft(ruleOfDepth(BADGE_RULE_LIMITS.maxDepth), catalog).ok).toBe(true)
    const tooDeep = validateRuleDraft(ruleOfDepth(BADGE_RULE_LIMITS.maxDepth + 1), catalog)
    expect(tooDeep.ok).toBe(false)
    expect(tooDeep.errors.map(error => error.code)).toContain('rule.depth_exceeded')
    expect(tooDeep.errors.find(error => error.code === 'rule.depth_exceeded')?.params).toEqual({ depth: 6, max: 5 })
  })

  test('叶子 32 通过、33 拒绝', () => {
    const atLimit = ruleOf(group('all', Array.from({ length: BADGE_RULE_LIMITS.maxLeaves }, () => leaf('activity.count', 1))))
    expect(validateRuleDraft(atLimit, catalog).ok).toBe(true)

    const overLimit = ruleOf(group('all', Array.from({ length: BADGE_RULE_LIMITS.maxLeaves + 1 }, () => leaf('activity.count', 1))))
    const result = validateRuleDraft(overLimit, catalog)
    expect(result.ok).toBe(false)
    expect(result.errors.map(error => error.code)).toContain('rule.too_many_leaves')
  })

  test('节点 64 上限：叶子不超限时仍可被节点总数拒绝', () => {
    // 32 个叶子各挂在 3 层组下：叶子 32 未超限、深度 5 未超限，节点数 1 + 32*4 = 129。
    const chain = () => group('all', [group('all', [leaf('activity.count', 1)])])
    const rule = ruleOf(group('all', Array.from({ length: BADGE_RULE_LIMITS.maxLeaves }, chain)))
    const codes = validateRuleDraft(rule, catalog).errors.map(error => error.code)
    expect(codes).toContain('rule.too_many_nodes')
    expect(codes).not.toContain('rule.too_many_leaves')
    expect(codes).not.toContain('rule.depth_exceeded')
  })

  test('boolean 条件不许带 cmp，count 条件必须带 cmp', () => {
    expect(codesOf(ruleOf(leaf('identity.email_verified', 1)))).toContain('rule.boolean_cmp')
    expect(validateRuleDraft(ruleOf(leaf('identity.email_verified')), catalog).ok).toBe(true)
    expect(codesOf(ruleOf(leaf('activity.count')))).toContain('rule.count_cmp_missing')
  })

  test('catalog 里没有的 type 一律拒绝', () => {
    const result = validateRuleDraft(ruleOf(leaf('plugin.unknown_fact', 1)), catalog)
    expect(result.ok).toBe(false)
    expect(result.errors[0]?.code).toBe('rule.unknown_type')
    expect(result.errors[0]?.params).toEqual({ type: 'plugin.unknown_fact' })
  })

  test('days / minDays / window.days 必须在 1..3650', () => {
    expect(validateRuleDraft(ruleOf(leaf('activity.count', 1, { window: { kind: 'recent_days', days: 30 } })), catalog).ok).toBe(true)
    expect(validateRuleDraft(ruleOf(leaf('activity.count', 1, { window: { kind: 'recent_days', days: 3650 } })), catalog).ok).toBe(true)

    expect(codesOf(ruleOf(leaf('activity.count', 1, { window: { kind: 'recent_days', days: 0 } })))).toContain('rule.days_range')
    expect(codesOf(ruleOf(leaf('activity.count', 1, { window: { kind: 'recent_days', days: 3651 } })))).toContain('rule.days_range')
    expect(codesOf(ruleOf(leaf('activity.streak', 1, { minDays: 0 })))).toContain('rule.days_range')
    expect(codesOf(ruleOf(leaf('activity.count', 1, { days: 1.5 })))).toContain('rule.days_range')
  })

  test('cmp.value 必须在 1..1e9 且为整数', () => {
    expect(validateRuleDraft(ruleOf(leaf('activity.count', 1)), catalog).ok).toBe(true)
    expect(validateRuleDraft(ruleOf(leaf('activity.count', 1_000_000_000)), catalog).ok).toBe(true)
    expect(codesOf(ruleOf(leaf('activity.count', 0)))).toContain('rule.cmp_value_range')
    expect(codesOf(ruleOf(leaf('activity.count', 1_000_000_001)))).toContain('rule.cmp_value_range')
    expect(codesOf(ruleOf(leaf('activity.count', 2.5)))).toContain('rule.cmp_value_range')
  })

  test('AST 版本号不是 1 时拒绝', () => {
    expect(codesOf({ v: 2 as unknown as 1, root: leaf('activity.count', 1) })).toContain('rule.version')
  })

  test('空 all / any 组必须拒绝（空 all 是 vacuously true，会全站发徽章）', () => {
    const emptyAll = validateRuleDraft(ruleOf(group('all', [])), catalog)
    expect(emptyAll.ok).toBe(false)
    expect(emptyAll.errors).toEqual([{ code: 'rule.empty_group', path: [], params: { op: 'all' } }])

    const emptyAny = validateRuleDraft(ruleOf(group('any', [])), catalog)
    expect(emptyAny.ok).toBe(false)
    expect(emptyAny.errors.map(error => error.code)).toEqual(['rule.empty_group'])
    expect(emptyAny.errors[0]?.params).toEqual({ op: 'any' })
  })

  test('嵌套空组按节点路径报出', () => {
    const rule = ruleOf(group('all', [leaf('activity.count', 1), group('any', [])]))
    const errors = validateRuleDraft(rule, catalog).errors
    expect(errors).toHaveLength(1)
    expect(errors[0]?.code).toBe('rule.empty_group')
    expect(errors[0]?.path).toEqual([1])
  })

  test('params 声明存在时按 schema 校验：必填 / 未知键 / enum / int 区间', () => {
    expect(codesOf(ruleOf(leaf('object.authoring', 5, {})))).toContain('rule.param_required')
    expect(codesOf(ruleOf(leaf('object.authoring', 5, { category: '公告', tz: 'UTC' })))).toContain('rule.param_unknown')
    expect(codesOf(ruleOf(leaf('object.authoring', 5, { category: '公告栏' })))).toContain('rule.param_enum')
    expect(codesOf(ruleOf(leaf('object.authoring', 5, { category: '公告', days: 0 })))).toContain('rule.param_range')

    const valid = validateRuleDraft(ruleOf(leaf('object.authoring', 5, { category: '公告', days: 30 })), catalog)
    expect(valid).toEqual({ ok: true, errors: [] })
  })

  test('必填参数缺失时带 params.<name> 字段定位', () => {
    const errors = validateRuleDraft(ruleOf(leaf('object.authoring', 5, {})), catalog).errors
    const missing = errors.find(error => error.code === 'rule.param_required')
    expect(missing?.field).toBe('params.category')
    expect(missing?.params).toEqual({ param: 'category' })
  })

  test('window 参数校验 kind，days 仍走引擎级区间', () => {
    expect(validateRuleDraft(ruleOf(leaf('plugin.fact.count', 1, { window: { kind: 'all' } })), catalog).ok).toBe(true)
    expect(codesOf(ruleOf(leaf('plugin.fact.count', 1, { window: 'all' })))).toContain('rule.param_invalid')

    const badKind = validateRuleDraft(ruleOf(leaf('plugin.fact.count', 1, { window: { kind: 'last_year' } })), catalog)
    expect(badKind.errors.map(error => error.code)).toContain('rule.param_window')
    expect(badKind.errors.find(error => error.code === 'rule.param_window')?.field).toBe('params.window.kind')

    const badDays = validateRuleDraft(
      ruleOf(leaf('plugin.fact.count', 1, { window: { kind: 'recent_days', days: 5000 } })),
      catalog
    )
    expect(badDays.errors.map(error => error.code)).toContain('rule.days_range')
    expect(badDays.errors.map(error => error.code)).not.toContain('rule.param_window')
  })

  test('params 声明缺失时跳过参数层（向后兼容旧目录）', () => {
    expect(validateRuleDraft(ruleOf(leaf('activity.count', 1, { anything: 'goes' })), catalog).ok).toBe(true)
  })

  test('objectRef 恒为字符串 slug：数字与空白串拒绝，refKind 不做存在性校验', () => {
    expect(validateRuleDraft(ruleOf(leaf('identity.role', undefined, { roleKey: 'moderator' })), catalog).ok).toBe(true)
    // 引用不存在的角色名也不拦：存在性属于后端 rule_health，前端不查库。
    expect(validateRuleDraft(ruleOf(leaf('identity.role', undefined, { roleKey: 'no-such-role' })), catalog).ok).toBe(true)

    const numeric = validateRuleDraft(ruleOf(leaf('identity.role', undefined, { roleKey: 42 })), catalog)
    expect(numeric.errors.map(error => error.code)).toContain('rule.param_invalid')
    expect(numeric.errors.find(error => error.code === 'rule.param_invalid')?.field).toBe('params.roleKey')

    expect(codesOf(ruleOf(leaf('identity.role', undefined, { roleKey: '   ' })))).toContain('rule.param_invalid')
    expect(codesOf(ruleOf(leaf('identity.role', undefined, {})))).toContain('rule.param_required')

    // providerId 同属 objectRef（有候选列表）：传数字必须被拒，防止有人当成 int。
    expect(validateRuleDraft(ruleOf(leaf('identity.oauth_link', undefined, { providerId: 'sforum.auth-github' })), catalog).ok).toBe(true)
    const providerNumeric = validateRuleDraft(ruleOf(leaf('identity.oauth_link', undefined, { providerId: 7 })), catalog)
    expect(providerNumeric.errors.map(error => error.code)).toContain('rule.param_invalid')
    expect(providerNumeric.errors.find(error => error.code === 'rule.param_invalid')?.field).toBe('params.providerId')
  })

  test('string / bool 是 M3 插件预留：内置条件不用，标量类型仍要校验', () => {
    expect(validateRuleDraft(ruleOf(leaf('plugin.text_fact', undefined, { note: 'free text' })), catalog).ok).toBe(true)
    expect(codesOf(ruleOf(leaf('plugin.text_fact', undefined, { note: 42 })))).toContain('rule.param_invalid')

    expect(validateRuleDraft(ruleOf(leaf('plugin.bool_fact', undefined, { flag: true })), catalog).ok).toBe(true)
    expect(codesOf(ruleOf(leaf('plugin.bool_fact', undefined, { flag: 'yes' })))).toContain('rule.param_invalid')
  })

  test('compare 不在条件允许集合内时拒绝', () => {
    expect(codesOf(ruleOf(leafWith('activity.streak', 'lte', 3)))).toContain('rule.cmp_not_allowed')
    expect(validateRuleDraft(ruleOf(leafWith('activity.count', 'lte', 3)), catalog).ok).toBe(true)
  })
})

describe('badgeRule describeRule（仅后台）', () => {
  const rule = ruleOf(group('all', [
    leaf('activity.count', 10),
    group('any', [
      leaf('identity.email_verified'),
      leaf('activity.streak', 3)
    ])
  ]))

  test('中文句子按 all/any 连接并加括号', () => {
    expect(describeRule(rule, catalog, 'zh-CN')).toBe('（计数 ≥ 10 且 （邮箱已验证 或 连续访问 ≥ 3））')
  })

  test('英文句子用 and/or 与半角括号', () => {
    expect(describeRule(rule, catalog, 'en-US')).toBe('(Count ≥ 10 and (Email verified or Streak ≥ 3))')
  })

  test('not 组与目录模板占位符', () => {
    expect(describeRule(ruleOf(group('not', [leaf('identity.email_verified')])), catalog, 'zh-CN'))
      .toBe('非（邮箱已验证）')
    expect(describeRule(ruleOf(leaf('object.authoring', 5, { category: '公告' })), catalog, 'zh-CN'))
      .toBe('在「公告」发文 5 篇')
    expect(describeRule(ruleOf(leaf('object.authoring', 5, { category: 'news' })), catalog, 'en-US'))
      .toBe('5 topics in news')
  })

  test('目录缺失时回退原始 type，不抛错', () => {
    expect(describeRule(ruleOf(leaf('plugin.unknown_fact')), catalog, 'zh-CN')).toBe('plugin.unknown_fact')
    expect(describeRule(ruleOf(group('all', [])), catalog, 'zh-CN')).toBe('（空条件）')
  })
})
