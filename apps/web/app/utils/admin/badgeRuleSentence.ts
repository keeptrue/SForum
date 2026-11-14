/**
 * 规则树 → 后台人话句子，以及「简单模式」能表达什么样的规则（纯函数）。
 *
 * 三条边界：
 * 1. **只给后台用**。公开面文案由服务端 `requirement_summary_zh/en` 下发，前端再生成
 *    一份必然分叉（`badgeRule.ts` 头注释同口径）。
 * 2. 不 import 任何 Nuxt 自动导入：文案通过注入的 `t` / `te` 取，指标名沿用
 *    `badgeRuleParams.ts` 的键序（`params.<name>.options.<v>` → `metrics.<v>` → `enums.<v>`），
 *    所以同一个取值在句子与下拉里不会出现两种说法。
 * 3. `isSimpleRule` / `flattenAllLeaves` / `buildFlatAllRule` 只服务「扁平 all」形态：
 *    任何 `any` / `not` / 嵌套组都**不**是简单规则，绝不在这里压平——把 `any` 悄悄变成
 *    `all` 会改变判定结果，等于让运营发错徽章。
 */
import { isGroupNode, type BadgeConditionCatalogEntry, type BadgeRule, type BadgeRuleLeaf, type BadgeRuleNode } from './badgeRule'
import { resolveParamOptionLabel } from './badgeRuleParams'

/**
 * 句子要用的最小 i18n 面。注入而不是全局 import：纯函数才可单测，也才不会被
 * 「组件里 t / 纯函数里再写一份中文」分成两套文案。
 */
export interface RuleSentenceI18n {
  t: (key: string, params?: Record<string, unknown>) => string
  te: (key: string) => boolean
  /**
   * key 前缀，默认 `admin.badges.rule`。
   *
   * 共享编辑器把句子前缀一起参数化（信任等级传 `admin.forum.trustLevels.rule`）：
   * 句子是**唯一**会把 `t` / `te` 一起用上的地方（`te` 决定模板缺失时是否回落），
   * 只参数化组件里的 `t` 会让缺键时静默显示原始 key。
   */
  i18nKeyPrefix?: string
}

/** 句子键族的默认前缀（徽章）。 */
export const DEFAULT_RULE_I18N_PREFIX = 'admin.badges.rule'

function sentenceKey(i18n: RuleSentenceI18n, name: string): string {
  return `${i18n.i18nKeyPrefix ?? DEFAULT_RULE_I18N_PREFIX}.sentence.${name}`
}

/**
 * 简单模式 = 无嵌套、无 `any`、无 `not` 的条件列表。
 *
 * 单叶根（还没加过条件的徽章）也算简单：它同样是「一串并列条件」的退化形态。
 * 注意这是**表达能力**判断，不是合法性判断：空 `all` 组也是简单规则，保存仍由
 * 既有 `rule.empty_group` 拦住，这里不另立口径。
 */
export function isSimpleRule(rule: BadgeRule | null | undefined): boolean {
  const root = rule?.root
  if (!root) return false
  if (!isGroupNode(root)) return true
  if (root.op !== 'all') return false
  return root.children.every(child => !isGroupNode(child))
}

/**
 * 取出扁平 `all` 里的叶子。不可表达（`any` / `not` / 嵌套组）时返回 `[]`：
 * 调用方必须先 `isSimpleRule`，否则拿到的空数组会被误当成「还没有条件」。
 */
export function flattenAllLeaves(rule: BadgeRule | null | undefined): BadgeRuleLeaf[] {
  if (!rule || !isSimpleRule(rule)) return []
  const root = rule.root
  if (!isGroupNode(root)) return [root]
  return root.children.filter((child): child is BadgeRuleLeaf => !isGroupNode(child))
}

/**
 * 逆操作：永远产出扁平 `all` 组。单叶根会被归一成 `all[leaf]`（语义等价，
 * 模式切换本身不 emit，所以不会在运营没动手时改掉落库形状）。
 */
export function buildFlatAllRule(leaves: readonly BadgeRuleLeaf[]): BadgeRule {
  return { v: 1, root: { op: 'all', children: leaves.map(leaf => ({ ...leaf })) } }
}

function windowPhrase(window: unknown, i18n: RuleSentenceI18n): string {
  const record = window && typeof window === 'object' ? (window as Record<string, unknown>) : {}
  if (record.kind !== 'recent_days') return i18n.t(sentenceKey(i18n, 'allTime'))
  const days = typeof record.days === 'number' ? record.days : 30
  return i18n.t(sentenceKey(i18n, 'withinDays'), { days })
}

/** 目录级计量单位；`days_visited` 本身就是天数，「访问天数 ≥ 7 次」读不通。 */
function unitPhrase(entry: BadgeConditionCatalogEntry, params: Record<string, unknown>, zh: boolean): string {
  if (params.metric === 'days_visited') return ''
  const primary = zh ? entry.unitZh : entry.unitEn
  const secondary = zh ? entry.unitEn : entry.unitZh
  const unit = typeof primary === 'string' && primary.trim() !== '' ? primary : secondary
  return typeof unit === 'string' ? unit : ''
}

/** 一片叶子的人话。未知条件类型回退 `node.type`（比空串更可诊断）。 */
export function leafSentence(
  leaf: BadgeRuleLeaf,
  catalog: readonly BadgeConditionCatalogEntry[],
  locale: string,
  i18n: RuleSentenceI18n
): string {
  const entry = catalog.find(item => item.key === leaf.type)
  if (!entry) return leaf.type
  const zh = locale.toLowerCase().startsWith('zh')
  const label = (zh ? entry.labelZh : entry.labelEn) || leaf.type
  if (!leaf.cmp) return label

  const params = (leaf.params ?? {}) as Record<string, unknown>
  const metric = typeof params.metric === 'string' && params.metric !== ''
    ? resolveParamOptionLabel('metric', params.metric, i18n.t, i18n.te)
    : ''
  const symbol = leaf.cmp.op === 'lte' ? '≤' : '≥'
  const unit = unitPhrase(entry, params, zh)
  const value = unit === '' ? String(leaf.cmp.value) : `${leaf.cmp.value} ${unit}`
  // 只有真的声明了 `window` 参数的条件才谈得上「累计 / 最近 N 天」：账户年龄、连续访问
  // 这类条件没有时间窗口概念，给它们加「累计」会读成一句不存在的话。
  const when = 'window' in params ? windowPhrase(params.window, i18n) : ''
  const sentence = i18n.t(sentenceKey(i18n, 'tpl'), { when, label: metric || label, symbol, value })
  // 空 `when` 在英文模板（`{when} {label} …`）里会留一个行首空格；只裁首尾，
  // 不折叠中间的空格——「最近 30 天内」依赖那个词间空格。
  return sentence.trim()
}

function nodeSentence(
  node: BadgeRuleNode,
  catalog: readonly BadgeConditionCatalogEntry[],
  locale: string,
  i18n: RuleSentenceI18n
): string {
  if (!isGroupNode(node)) return leafSentence(node, catalog, locale, i18n)
  const parts = node.children.map(child => nodeSentence(child, catalog, locale, i18n))
  if (node.op === 'not') {
    return i18n.t(sentenceKey(i18n, 'negate'), { inner: parts[0] ?? i18n.t(sentenceKey(i18n, 'empty')) })
  }
  if (parts.length === 0) return i18n.t(sentenceKey(i18n, 'empty'))
  return parts.join(i18n.t(node.op === 'any' ? sentenceKey(i18n, 'joinAny') : sentenceKey(i18n, 'joinAll')))
}

/** 整条规则的人话：模式切换下方的常显预览。空组显式说话，不留空串。 */
export function ruleSentence(
  rule: BadgeRule | null | undefined,
  catalog: readonly BadgeConditionCatalogEntry[],
  locale: string,
  i18n: RuleSentenceI18n
): string {
  if (!rule?.root) return i18n.t(sentenceKey(i18n, 'empty'))
  return nodeSentence(rule.root, catalog, locale, i18n)
}
