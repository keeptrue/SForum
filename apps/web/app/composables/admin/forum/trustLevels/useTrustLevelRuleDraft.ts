import type { BadgeConditionCatalogEntry, BadgeFactCandidate, BadgeRule } from '~/utils/admin/rule'
import type { BadgeRefCandidateMap } from '~/utils/admin/badgeRefCandidates'
import type { TrustLevelRuleDocument } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import type { TrustLevelSaveErrorBuckets } from '~/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'

/**
 * 阈值 tab 折叠面板的**纯逻辑**（无 Vue、无 i18n、无 DOM），给 `bun test` 直接断言。
 *
 * 四件事：
 * 1. `decodeRuleDocument`：把读面下发的原样 JSON 认成编辑器能吃的 `{v:1, root}`；
 * 2. `catalogUsabilityFor`：**D21 保存闸门的唯一实现**（`¬catalogUsable` 四条判据）；
 * 3. `anyTierRuleDirty`：闸门的另一半（本次会话是否真的动过规则字节）；
 * 4. `firstRuleErrorTierId`：422 后要自动展开哪一档。
 *
 * 判据放在这里而不是组件里，是因为它们要给「保存按钮」和「面板失败态」两个消费者用，
 * 两处各写一遍必然会漂移（徽章那刀「两份 deny CTE」的教训）。
 */

/** `¬catalogUsable` 的四种成因；`null` = 可用。 */
export type RuleCatalogBlockReason =
  | 'request-failed'
  | 'empty-catalog'
  | 'fact-candidates-missing'
  | 'ref-candidates-missing'

export type RuleCatalogState = {
  status: 'idle' | 'loading' | 'ready' | 'failed'
  items: readonly BadgeConditionCatalogEntry[]
  factCandidates: readonly BadgeFactCandidate[]
  refCandidates: BadgeRefCandidateMap
}

export type RuleCatalogUsability = {
  usable: boolean
  reason: RuleCatalogBlockReason | null
  /** 缺失的 objectRef 种类（诊断用；文案只展示整句原因，不逐个列）。 */
  missingRefKinds: string[]
}

/** 编辑器只认这个形状；别的（`null` / 脏值 / 版本不符）一律当作「不是可编辑文档」。 */
export function decodeRuleDocument(raw: TrustLevelRuleDocument | undefined): BadgeRule | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const record = raw as Record<string, unknown>
  if (record.v !== 1) return null
  const root = record.root
  if (!root || typeof root !== 'object') return null
  const node = root as Record<string, unknown>
  const isLeaf = typeof node.type === 'string'
  const isGroup = typeof node.op === 'string' && Array.isArray(node.children)
  if (!isLeaf && !isGroup) return null
  return raw as unknown as BadgeRule
}

/** 宽容遍历：不假设版本、不假设根形状，只把「看起来像叶子」的对象挑出来。 */
export function ruleLeaves(raw: unknown): Array<{ type: string, params?: Record<string, unknown> }> {
  const leaves: Array<{ type: string, params?: Record<string, unknown> }> = []
  const walk = (node: unknown) => {
    if (!node || typeof node !== 'object') return
    if (Array.isArray(node)) {
      node.forEach(walk)
      return
    }
    const record = node as Record<string, unknown>
    if (typeof record.type === 'string') {
      const params = record.params && typeof record.params === 'object' && !Array.isArray(record.params)
        ? record.params as Record<string, unknown>
        : undefined
      leaves.push({ type: record.type, params })
      return
    }
    if (Array.isArray(record.children)) {
      record.children.forEach(walk)
      return
    }
    if ('root' in record) walk(record.root)
  }
  walk(raw)
  return leaves
}

/**
 * 保存闸门的一半（ui-spec §6.2 / 计划 §4.4 D21）：
 *
 * - **只在 `ready` 时才可能不可用**。`idle` / `loading` 一律当作可用 —— 页面刚打开就
 *   把保存按钮灰掉是「自我封锁」，而且此时表单里的规则字节就是库里的字节，没有改坏的风险。
 * - `failed` → 不可用（第 1 条）；`items` 空 → 不可用（第 2 条）。
 * - 第 3、4 条是「**无法校验**」而不是「本来就没有」：候选缺失时前端判不出该叶子是失效
 *   还是有效，原样回传等于把不可校验当成有效。
 */
export function catalogUsabilityFor(
  catalog: RuleCatalogState,
  rules: readonly (TrustLevelRuleDocument | undefined)[]
): RuleCatalogUsability {
  const usable: RuleCatalogUsability = { usable: true, reason: null, missingRefKinds: [] }
  if (catalog.status === 'failed') return { ...usable, usable: false, reason: 'request-failed' }
  if (catalog.status !== 'ready') return usable
  if (catalog.items.length === 0) return { ...usable, usable: false, reason: 'empty-catalog' }

  const byKey = new Map(catalog.items.map(item => [item.key, item]))
  const missing = new Set<string>()
  let needsFacts = false

  for (const rule of rules) {
    for (const leaf of ruleLeaves(rule)) {
      if (leaf.type === 'fact.count') needsFacts = true
      const entry = byKey.get(leaf.type)
      for (const spec of entry?.params ?? []) {
        // `fact` 不是 refCandidates 的键（它是事实候选那条独立通道），跳出去别误判。
        if (!spec.refKind || spec.refKind === 'fact') continue
        const group = catalog.refCandidates[spec.refKind]
        if (!group || group.candidates.length === 0) missing.add(spec.refKind)
      }
    }
  }

  if (needsFacts && catalog.factCandidates.length === 0) {
    return { ...usable, usable: false, reason: 'fact-candidates-missing', missingRefKinds: [...missing] }
  }
  if (missing.size > 0) {
    return { ...usable, usable: false, reason: 'ref-candidates-missing', missingRefKinds: [...missing] }
  }
  return usable
}

/**
 * 闸门的另一半：本次会话是否**编辑过**至少一档的规则。
 *
 * 用「与已保存值的字节比较」而不是另存一个 dirty 布尔：
 * - 打开面板看两眼再关掉不算改动（dirty 布尔会误判）；
 * - `applyAdminPayload` 之后自然归零，不需要额外的重置钩子。
 *
 * 这条与 `willWriteRule`（载荷里有没有 rule）**不等价**，也是 D21 顶回徽章那版的原因：
 * 一个已经配了规则的站点，在目录挂掉时还应该能改阈值 —— 那些规则字节不会被这次提交改动。
 */
export function anyTierRuleDirty(
  tiers: readonly { id: string, rule: TrustLevelRuleDocument }[],
  initial: readonly { id: string, rule: TrustLevelRuleDocument }[]
): boolean {
  const saved = new Map(initial.map(tier => [tier.id, tier.rule ?? null]))
  return tiers.some(tier => JSON.stringify(saved.get(tier.id) ?? null) !== JSON.stringify(tier.rule ?? null))
}

/**
 * 422 后要自动展开哪一档：**第一个**带规则域字段错误的档位。
 *
 * 判据是 `field`（后端把 `ruleengine.Error.Path` 喂进 `params.path`，`field` 恒为 `rule`），
 * 不是 `reasonCode` —— 码会随映射表变化，字段名是稳定契约。
 */
export function firstRuleErrorTierId(buckets: TrustLevelSaveErrorBuckets): string | null {
  for (const [tierId, errors] of Object.entries(buckets.byTierId)) {
    if (errors.some(error => error.field === 'rule' || error.field.startsWith('rule.'))) return tierId
  }
  return null
}
