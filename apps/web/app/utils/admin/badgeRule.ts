/**
 * 徽章规则 AST 的前端镜像（**仅后台**使用）。
 *
 * 事实来源：`knowledge/plans/2026-11-12-badge-rule-engine.md` §5.1。权威实现是后端
 * `Support/RuleEngine`，这里只做三件事：
 *
 * 1. 不可变的树编辑（插入 / 删除 / 替换 / 包一层组），给编辑器当状态原语；
 * 2. `validateRuleDraft`：保存前的同口径镜像校验（后端仍会再校验一次，422 才是准的）；
 * 3. `describeRule`：后台影响确认弹窗与健康提示用的自然语言句子。
 *
 * 公开面（`/badges`、资料页）**不得**调用 `describeRule`：公开文案由服务端
 * `requirement_summary_zh/en` 下发（计划 §0 决策 14），前端再生成一份必然分叉。
 */

import { isIntegerIn, validateParams } from './badgeRuleParams'
import type { BadgeConditionParam } from './badgeRuleParams'

export type BadgeRuleOp = 'all' | 'any' | 'not'
export type BadgeRuleCompareOp = 'gte' | 'lte'
/** 取值形态：计数类必须带 `cmp`，布尔类必须不带。由条件目录下发。 */
export type BadgeRuleValueKind = 'count' | 'boolean'

export interface BadgeRuleCompare {
  op: BadgeRuleCompareOp
  value: number
}

export interface BadgeRuleLeaf {
  type: string
  params?: Record<string, unknown>
  cmp?: BadgeRuleCompare
}

export interface BadgeRuleGroup {
  op: BadgeRuleOp
  children: BadgeRuleNode[]
}

export type BadgeRuleNode = BadgeRuleGroup | BadgeRuleLeaf

/** 落库形态：`badges.rule` 整列就是这个对象（`v` 是 AST 版本）。 */
export interface BadgeRule {
  v: 1
  root: BadgeRuleNode
}

/**
 * 参数层类型从 `badgeRuleParams.ts` re-export：调用方 import 面保持只认 `badgeRule`。
 * 参数 schema 校验的实现在那边（`validateParams` / `validateParamSpec`）。
 */
/**
 * 服务端能自动生成公开说明的指标白名单。
 *
 * 镜像 `Models/Badges/requirement_text.go` 的 `metricLabels` 键集，**不是**抄
 * `BADGE_METRIC_OPTIONS`：后者少一个历史指标 `like_given_count`（服务端仍认它），
 * 按下拉白名单判会把「服务端能生成」误判成「生成不了」。
 */
const AUTO_SUMMARY_METRICS: readonly string[] = [
  'topic_count',
  'comment_count',
  'like_received_count',
  'like_given_count',
  'bookmark_count',
  'days_visited'
]

/**
 * 规则是否落在服务端「自动生成公开说明」的形状里。
 *
 * 镜像 `fastPathRuleForText`：单 `activity.count` 叶子 + `window.kind === 'all'` +
 * 不带 `days` + `cmp.op === 'gte'` + `value > 0` + 指标在白名单里。别的形状服务端一律
 * 不生成（宁可不出文案，也不把内部结构拼成人话）。
 */
function badgeRuleAutoSummarized(rule: BadgeRule | null | undefined): boolean {
  if (!rule || typeof rule !== 'object') return false
  const root = rule.root
  if (!root || typeof root !== 'object' || isGroupNode(root)) return false
  if (root.type !== 'activity.count') return false
  const cmp = root.cmp
  if (!cmp || cmp.op !== 'gte' || !(Number(cmp.value) > 0)) return false
  const params = root.params ?? {}
  if (typeof params.metric !== 'string' || !AUTO_SUMMARY_METRICS.includes(params.metric)) return false
  const window = params.window as Record<string, unknown> | undefined
  if (!window || typeof window !== 'object' || window.kind !== 'all') return false
  // 服务端用「键存在」判 days，不是判值：`days: undefined` 也会被算作带了窗口天数。
  if ('days' in window) return false
  return true
}

/**
 * 公开说明是否需要运营手填（后台提示，仅提醒、不阻止保存）。
 *
 * 判据是**镜像服务端谓词**，不是「条件个数 > 1」：用户点名的是多条件，但同一后果
 * （公开面没有任何解释）在「单个计数条件用最近 30 天窗口」和「单个身份条件」上照样
 * 发生。只要判据跟服务端一致，就不会对服务端会生成的形状误报。
 *
 * - 服务端会生成的形状 → `false`（不提示）；
 * - 其余形状 → 仅当 `summary.zh` 与 `summary.en` **都为空**时 `true`；
 *   任一侧有值就不提示（另一侧由服务端/运营自己决定，不在这里造第二套口径）。
 */
export function badgeRuleNeedsSummary(
  rule: BadgeRule | null | undefined,
  summary?: { zh: string, en: string } | null
): boolean {
  if (badgeRuleAutoSummarized(rule)) return false
  const zh = typeof summary?.zh === 'string' ? summary.zh.trim() : ''
  const en = typeof summary?.en === 'string' ? summary.en.trim() : ''
  return zh === '' && en === ''
}

export type {
  BadgeConditionParam,
  BadgeConditionParamOption,
  BadgeRuleParamKind,
  BadgeRuleRefKind
} from './badgeRuleParams'

export interface BadgeConditionVariant {
  value: string
  labelZh: string
  labelEn: string
}

/**
 * 条件目录的一条，等于 `GET /admin/badges/conditions` 的单条响应（宿主已把 manifest 的
 * `{"zh-CN":…,"en-US":…}` 扁平化成 `labelZh` / `labelEn`）。
 *
 * `params` 缺失表示「这个条件的参数表还没声明」，校验会跳过参数层——向后兼容旧目录；
 * 一旦给了 `params`，未声明的参数键也会被拒绝（后端 `CodeParamUnknown` 同口径）。
 */
export interface BadgeConditionCatalogEntry {
  key: string
  valueKind: BadgeRuleValueKind
  /** 允许的比较符；计数条件里 `cmp.op` 必须落在其中。 */
  compare: BadgeRuleCompareOp[]
  labelZh: string
  labelEn: string
  /** 可选句子模板，支持 `{value}` 与 `{param.xxx}`；缺省时用「label ≥ value」。 */
  describeZh?: string
  describeEn?: string
  unitZh?: string
  unitEn?: string
  params?: BadgeConditionParam[]
  variants?: BadgeConditionVariant[]
}

/**
 * 事实候选（`GET /admin/badges/conditions` 的顶层 `factCandidates`，Errata 2 冻结形状）。
 *
 * 宿主已把 manifest 的 `{zh-CN,en-US}` 扁平化成 `labelZh/labelEn`；`variants` 是该
 * `factKey` 专属的可选变体，**空数组 ≠ 下拉占位**——编辑器必须整项隐藏（M2 的
 * 「空候选不得渲染成空 SelectContent」）。
 */
export interface BadgeFactCandidate {
  key: string
  labelZh: string
  labelEn: string
  unitZh: string
  unitEn: string
  compare: string[]
  variants: BadgeConditionVariant[]
}

/** 后端硬上限（计划 §5.1）：超限 422 `forum.badge_rule_too_complex`。 */
export const BADGE_RULE_LIMITS = {
  maxDepth: 5,
  maxLeaves: 32,
  maxNodes: 64,
  minDays: 1,
  maxDays: 3650,
  minValue: 1,
  maxValue: 1_000_000_000
} as const

export function isGroupNode(node: BadgeRuleNode): node is BadgeRuleGroup {
  return Array.isArray((node as BadgeRuleGroup | undefined)?.children)
}

export function createLeaf(type: string, params: Record<string, unknown> = {}): BadgeRuleLeaf {
  return { type, params: { ...params } }
}

/** `not` 的「恰好 1 个子节点」由 `validateRuleDraft` 兜，创建时给空 children 便于插入。 */
export function createGroup(op: BadgeRuleOp, children: BadgeRuleNode[] = []): BadgeRuleGroup {
  return { op, children: [...children] }
}

/** 按数字索引路径取节点：`[]` 是根，`[0, 1]` 是 `root.children[0].children[1]`。 */
export function nodeAt(rule: BadgeRule, path: readonly number[]): BadgeRuleNode | null {
  let node: BadgeRuleNode | undefined = rule.root
  for (const index of path) {
    if (!node || !isGroupNode(node)) return null
    node = node.children[index]
  }
  return node ?? null
}

function updateNodeAt(
  node: BadgeRuleNode,
  path: readonly number[],
  update: (node: BadgeRuleNode) => BadgeRuleNode
): BadgeRuleNode {
  if (path.length === 0) return update(node)
  if (!isGroupNode(node)) return node
  const index = path[0] as number
  const child = node.children[index]
  if (!child) return node
  const next = updateNodeAt(child, path.slice(1), update)
  if (next === child) return node
  const children = node.children.slice()
  children[index] = next
  return { op: node.op, children }
}

/** 只克隆路径上的节点，未触及的子树保持引用共享；路径无效时原样返回同一个 rule。 */
function applyUpdate(
  rule: BadgeRule,
  path: readonly number[],
  update: (node: BadgeRuleNode) => BadgeRuleNode
): BadgeRule {
  const root = updateNodeAt(rule.root, path, update)
  return root === rule.root ? rule : { v: rule.v, root }
}

/** 在 `path` 指向的组节点末尾追加子节点；指向叶子或路径无效时原样返回。 */
export function insertChild(rule: BadgeRule, path: readonly number[], node: BadgeRuleNode): BadgeRule {
  return applyUpdate(rule, path, (target) => {
    if (!isGroupNode(target)) return target
    return { op: target.op, children: [...target.children, node] }
  })
}

/** 删除 `path` 指向的节点；删根或路径无效时原样返回。 */
export function removeNode(rule: BadgeRule, path: readonly number[]): BadgeRule {
  if (path.length === 0) return rule
  const parentPath = path.slice(0, -1)
  const index = path[path.length - 1] as number
  return applyUpdate(rule, parentPath, (parent) => {
    if (!isGroupNode(parent)) return parent
    if (index < 0 || index >= parent.children.length) return parent
    return { op: parent.op, children: parent.children.filter((_child, position) => position !== index) }
  })
}

/** 用 `node` 替换 `path` 指向的节点（`[]` 允许替换整棵根）。 */
export function replaceNode(rule: BadgeRule, path: readonly number[], node: BadgeRuleNode): BadgeRule {
  return applyUpdate(rule, path, () => node)
}

/** 把 `path` 指向的节点包进一个 `op` 组（唯一子节点就是它自己）。 */
export function wrapWithGroup(rule: BadgeRule, path: readonly number[], op: BadgeRuleOp): BadgeRule {
  return applyUpdate(rule, path, target => ({ op, children: [target] }))
}

/** 树规模统计：编辑器用它在按钮上做上限拦截，别等保存时 422 才发现。 */
export function ruleStats(rule: BadgeRule): { nodes: number, leaves: number, depth: number } {
  let nodes = 0
  let leaves = 0
  let depth = 0
  const walk = (node: BadgeRuleNode, level: number) => {
    nodes += 1
    if (level > depth) depth = level
    if (isGroupNode(node)) {
      node.children.forEach(child => walk(child, level + 1))
      return
    }
    leaves += 1
  }
  if (rule?.root) walk(rule.root, 1)
  return { nodes, leaves, depth }
}

/**
 * 按目录声明生成一个「零参数歧义」的默认叶子：枚举取第一项、int 取最小值、
 * 窗口默认累计、计数条件带目录允许的第一个比较符与阈值 1。
 * 默认条件固定选 `activity.count`（见编辑器的 `defaultEntry`）——它没有必填的对象引用。
 */
export function createDefaultLeaf(entry: BadgeConditionCatalogEntry): BadgeRuleLeaf {
  const params: Record<string, unknown> = {}
  for (const spec of entry.params ?? []) {
    switch (spec.kind) {
      case 'int':
        params[spec.name] = spec.min ?? 1
        break
      case 'enum':
        params[spec.name] = spec.options?.[0]?.value ?? ''
        break
      case 'window':
        params[spec.name] = { kind: 'all' }
        break
      case 'bool':
        params[spec.name] = false
        break
      default:
        params[spec.name] = ''
    }
  }
  if (entry.valueKind !== 'count') {
    return { type: entry.key, params }
  }
  return { type: entry.key, params, cmp: { op: entry.compare[0] ?? 'gte', value: 1 } }
}

/** 新建规则的初值：一个 `all` 组套一枚默认条件（合法且立刻可保存）。 */
export function createDefaultRule(entry: BadgeConditionCatalogEntry): BadgeRule {
  return { v: 1, root: createGroup('all', [createDefaultLeaf(entry)]) }
}

export type BadgeRuleErrorCode =
  | 'rule.version'
  | 'rule.invalid_node'
  | 'rule.group_op'
  | 'rule.empty_group'
  | 'rule.not_children'
  | 'rule.unknown_type'
  | 'rule.boolean_cmp'
  | 'rule.count_cmp_missing'
  | 'rule.cmp_op'
  | 'rule.cmp_not_allowed'
  | 'rule.cmp_value_range'
  | 'rule.days_range'
  | 'rule.param_required'
  | 'rule.param_unknown'
  | 'rule.param_invalid'
  | 'rule.param_range'
  | 'rule.param_enum'
  | 'rule.param_window'
  | 'rule.depth_exceeded'
  | 'rule.too_many_leaves'
  | 'rule.too_many_nodes'

export interface BadgeRuleError {
  code: BadgeRuleErrorCode
  /** 出错节点路径，口径同 `nodeAt`；上限类错误给最深节点的路径。 */
  path: number[]
  /** 节点内的字段位置，例如 `cmp.op` / `params.window.kind`；节点级错误没有。 */
  field?: string
  params?: Record<string, unknown>
}

export interface BadgeRuleValidation {
  ok: boolean
  errors: BadgeRuleError[]
}

interface DayField {
  field: string
  value: unknown
}

/** 日数字段只认这三个位置，避免把任意 `days` 同名字段误当窗口。 */
function dayFields(params: Record<string, unknown> | undefined): DayField[] {
  if (!params) return []
  const fields: DayField[] = []
  if ('days' in params) fields.push({ field: 'days', value: params.days })
  if ('minDays' in params) fields.push({ field: 'minDays', value: params.minDays })
  const window = params.window
  if (window && typeof window === 'object' && 'days' in (window as Record<string, unknown>)) {
    fields.push({ field: 'window.days', value: (window as Record<string, unknown>).days })
  }
  return fields
}

/**
 * 保存前校验：镜像后端上限与形态约束，**不**替代后端 422。
 * 目录里查不到的 `type` 一律拒绝（插件条件卸载后规则会在这里先报出来）。
 */
export function validateRuleDraft(
  rule: BadgeRule,
  catalog: readonly BadgeConditionCatalogEntry[]
): BadgeRuleValidation {
  const errors: BadgeRuleError[] = []
  const entries = new Map(catalog.map(entry => [entry.key, entry]))
  let nodes = 0
  let leaves = 0
  let deepestPath: number[] = []
  let maxDepth = 0

  if (!rule || rule.v !== 1) {
    errors.push({ code: 'rule.version', path: [], params: { version: String(rule?.v) } })
  }
  if (!rule || !rule.root) {
    errors.push({ code: 'rule.invalid_node', path: [] })
    return { ok: false, errors }
  }

  const walk = (node: BadgeRuleNode | undefined, path: number[], depth: number) => {
    nodes += 1
    if (depth > maxDepth) {
      maxDepth = depth
      deepestPath = path
    }
    if (!node || typeof node !== 'object') {
      errors.push({ code: 'rule.invalid_node', path })
      return
    }
    if (isGroupNode(node)) {
      if (node.op !== 'all' && node.op !== 'any' && node.op !== 'not') {
        errors.push({ code: 'rule.group_op', path, params: { op: String(node.op) } })
      }
      // 空组必须拒绝：空 `all` 是 vacuously true，一次保存就会把徽章发给全站；
      // 空 `any` 永不命中。后端 `walkGroup` 同口径（CodeShapeInvalid）。
      if ((node.op === 'all' || node.op === 'any') && node.children.length === 0) {
        errors.push({ code: 'rule.empty_group', path, params: { op: node.op } })
      }
      if (node.op === 'not' && node.children.length !== 1) {
        errors.push({ code: 'rule.not_children', path, params: { count: node.children.length } })
      }
      node.children.forEach((child, index) => walk(child, [...path, index], depth + 1))
      return
    }

    leaves += 1
    const entry = entries.get(node.type)
    if (!entry) {
      errors.push({ code: 'rule.unknown_type', path, params: { type: node.type } })
    }
    if (node.cmp) {
      if (node.cmp.op !== 'gte' && node.cmp.op !== 'lte') {
        errors.push({ code: 'rule.cmp_op', path, field: 'cmp.op', params: { op: String(node.cmp.op) } })
      } else if (entry && entry.compare.length > 0 && !entry.compare.includes(node.cmp.op)) {
        errors.push({ code: 'rule.cmp_not_allowed', path, field: 'cmp.op', params: { type: node.type, op: node.cmp.op } })
      }
      if (!isIntegerIn(node.cmp.value, BADGE_RULE_LIMITS.minValue, BADGE_RULE_LIMITS.maxValue)) {
        errors.push({ code: 'rule.cmp_value_range', path, field: 'cmp.value', params: { value: String(node.cmp.value) } })
      }
    }
    if (entry?.valueKind === 'boolean' && node.cmp) {
      errors.push({ code: 'rule.boolean_cmp', path, field: 'cmp', params: { type: node.type } })
    }
    if (entry?.valueKind === 'count' && !node.cmp) {
      errors.push({ code: 'rule.count_cmp_missing', path, field: 'cmp', params: { type: node.type } })
    }
    if (entry?.params) {
      validateParams(entry.params, node.params, path, errors)
    }
    for (const { field, value } of dayFields(node.params)) {
      if (!isIntegerIn(value, BADGE_RULE_LIMITS.minDays, BADGE_RULE_LIMITS.maxDays)) {
        errors.push({ code: 'rule.days_range', path, field: `params.${field}`, params: { field, value: String(value) } })
      }
    }
  }

  walk(rule.root, [], 1)

  if (maxDepth > BADGE_RULE_LIMITS.maxDepth) {
    errors.push({ code: 'rule.depth_exceeded', path: deepestPath, params: { depth: maxDepth, max: BADGE_RULE_LIMITS.maxDepth } })
  }
  if (leaves > BADGE_RULE_LIMITS.maxLeaves) {
    errors.push({ code: 'rule.too_many_leaves', path: [], params: { leaves, max: BADGE_RULE_LIMITS.maxLeaves } })
  }
  if (nodes > BADGE_RULE_LIMITS.maxNodes) {
    errors.push({ code: 'rule.too_many_nodes', path: [], params: { nodes, max: BADGE_RULE_LIMITS.maxNodes } })
  }

  return { ok: errors.length === 0, errors }
}

function fillTemplate(template: string, node: BadgeRuleLeaf): string {
  return template.replace(/\{(\w+(?:\.\w+)*)\}/g, (match, key: string) => {
    if (key === 'value') return String(node.cmp?.value ?? '')
    const [head, ...rest] = key.split('.')
    if (head !== 'param' || rest.length === 0) return match
    const value = (node.params ?? {})[rest[0] as string]
    return value === undefined || value === null ? '' : String(value)
  })
}

/**
 * 后台可读句子（影响确认弹窗 / 健康提示）。空组渲染成「空条件」而不是空字符串，
 * 让半成品规则在 UI 上可见，而不是看起来像一句完整的话。
 */
export function describeRule(
  rule: BadgeRule,
  catalog: readonly BadgeConditionCatalogEntry[],
  locale = 'zh-CN'
): string {
  const zh = locale.toLowerCase().startsWith('zh')
  const entries = new Map(catalog.map(entry => [entry.key, entry]))

  const describe = (node: BadgeRuleNode): string => {
    if (isGroupNode(node)) {
      const parts = node.children.map(describe)
      if (node.op === 'not') {
        const inner = parts[0] ?? (zh ? '空条件' : 'empty')
        return zh ? `非（${inner}）` : `not (${inner})`
      }
      if (parts.length === 0) return zh ? '（空条件）' : '(empty)'
      const joiner = node.op === 'all' ? (zh ? ' 且 ' : ' and ') : (zh ? ' 或 ' : ' or ')
      const body = parts.join(joiner)
      return zh ? `（${body}）` : `(${body})`
    }

    const entry = entries.get(node.type)
    const label = entry ? (zh ? entry.labelZh : entry.labelEn) : node.type
    const template = entry ? (zh ? entry.describeZh : entry.describeEn) : undefined
    if (template) return fillTemplate(template, node)
    if (!node.cmp) return label
    const symbol = node.cmp.op === 'lte' ? '≤' : '≥'
    return `${label} ${symbol} ${node.cmp.value}`
  }

  return describe(rule.root)
}
