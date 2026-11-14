import {
  DEFAULT_TIER_ICON_NAMES,
  TRUST_LEVEL_METRIC_KEYS,
  TRUST_LEVEL_METRIC_SUFFIX_BY_KEY,
  type TrustLevelTier,
  type TrustLevelTierName
} from '~/composables/profile/useTrustLevel'
import { HOST_ICON_NAME_PATTERN } from '~/utils/iconPicker'
import { isTierColorValue, normalizeTierColor } from '~/utils/profile/trustLevelColors'

/**
 * 动态档位的**纯函数**编辑层（无 Vue、无 i18n、无 DOM）。
 *
 * 住在 composable 而不是 `.vue` 里，是为了能被 bun test 直接断言：
 * 组件只剩模板与绑定，插入/停用/排序/边界这些会写坏配置的地方都在这里。
 *
 * 三条不变量（D-N7 与「插入锚定」）：
 * 1. **id 锚定**：任何操作都不改其它档的 `id`，配置跟着 id 走；
 *    所以「在 TL2 前插一档」只改数组顺序，不会让原 TL2 的阈值变成新档的阈值。
 * 2. **顺序即判定顺序**：数组顺序 = 判定与展示顺序，`rank` 由 active 位次派生（TL0 不在数组内）。
 * 3. **软停用**：`retired` 的档位留在数组里、保留全部配置，只是判定与 UI 跳过（D-N8）。
 *
 * 所有写操作都返回**新数组**（不修改入参），越界时返回等值副本（fail-closed，不抛错）。
 */

/** active 档位下限：至少 1 档（D-N7）。 */
export const TRUST_LEVEL_TIER_MIN = 1
/** active 档位上限：8 档（D-N7）。 */
export const TRUST_LEVEL_TIER_MAX = 8
/** 名称长度护栏：不是业务上限，只是别让 4000 字的名字撑爆矩阵列头。 */
export const TRUST_LEVEL_TIER_NAME_MAX = 40
/** 档位 id 长度护栏。 */
export const TRUST_LEVEL_TIER_ID_MAX = 40

/** 与 Go `LevelLimits` 同形：`null` 继承站点默认，数字即显式覆盖（`null ≠ 0`）。 */
export type TrustLevelLimits = {
  topicCooldownSeconds: number | null
  commentCooldownSeconds: number | null
  dailyTopicLimit: number | null
  dailyCommentLimit: number | null
}

export const EMPTY_TIER_LIMITS: TrustLevelLimits = {
  topicCooldownSeconds: null,
  commentCooldownSeconds: null,
  dailyTopicLimit: null,
  dailyCommentLimit: null
}

export type TrustLevelRequirements = Record<string, number>

/**
 * 一档的可选规则树（M3）。
 *
 * **刻意保持「原样 JSON」而不是解码后的 `BadgeRule`**：没被编辑过的规则必须逐字节回传读面
 * 拿到的值（后端 `TierConfig.Rule` 是 `json.RawMessage`，重新序列化会重排键、丢未知键，
 * 脏规则还可能变成 `null`——徽章那刀已证）。编辑过的档位会整份换成编辑器产出的新对象。
 */
export type TrustLevelRuleDocument = Record<string, unknown> | null

/** 表单里的一档（= v2 存储的一档，`earnable` 永远是数组，省略会被后端 422）。 */
export type TierForm = {
  id: string
  name: TrustLevelTierName
  icon: string | null
  /** 颜色白名单 token（数量 = 合法 rank 数，见 `trustLevelColors.ts`）；`null` = 跟随位次自动（D-C1）。 */
  color: string | null
  requirements: TrustLevelRequirements
  limits: TrustLevelLimits | null
  earnable: string[]
  retired: boolean
  /** 可选规则树：`null` = 该档无规则（载荷里**不写**这个键，保持与引入前的字节一致）。 */
  rule: TrustLevelRuleDocument
}

/** 六个判定指标全 0：新插入的档位默认不设门槛（运营随后按需填）。 */
export function zeroRequirements(): TrustLevelRequirements {
  const requirements: TrustLevelRequirements = {}
  for (const key of TRUST_LEVEL_METRIC_KEYS) requirements[TRUST_LEVEL_METRIC_SUFFIX_BY_KEY[key]] = 0
  return requirements
}

export function cloneTier(tier: TierForm): TierForm {
  return {
    id: tier.id,
    name: { ...tier.name },
    icon: tier.icon,
    color: tier.color ?? null,
    requirements: { ...tier.requirements },
    limits: tier.limits ? { ...tier.limits } : null,
    earnable: [...tier.earnable],
    retired: tier.retired,
    // 引用复制：规则树只整份替换，从不原地改，所以共享同一份即可（深拷贝会白花一次全树遍历）。
    rule: tier.rule ?? null
  }
}

export function cloneTiers(tiers: readonly TierForm[]): TierForm[] {
  return tiers.map(cloneTier)
}

/** active 档位（顺序 = 判定与展示顺序）。 */
export function activeTiersOf(tiers: readonly TierForm[]): TierForm[] {
  return tiers.filter(tier => !tier.retired)
}

export function activeTierCount(tiers: readonly TierForm[]): number {
  return activeTiersOf(tiers).length
}

/** 能否新增/恢复：active 档数 < 8。 */
export function canAddTier(tiers: readonly TierForm[]): boolean {
  return activeTierCount(tiers) < TRUST_LEVEL_TIER_MAX
}

/** 能否停用：active 档数 > 1（至少保留一档）。 */
export function canRetireTier(tiers: readonly TierForm[]): boolean {
  return activeTierCount(tiers) > TRUST_LEVEL_TIER_MIN
}

/**
 * active 位次（1 起）= rank。TL0 不占位次，所以最小 rank 是 1。
 * 停用或不存在 → `null`（调用方据此不给 rank 文案）。
 */
export function activeRankOf(tiers: readonly TierForm[], id: string): number | null {
  const rank = activeTiersOf(tiers).findIndex(tier => tier.id === id)
  return rank < 0 ? null : rank + 1
}

/**
 * 新档 id：`tier_<n>`（**下划线**，与后端 `idDerivation` 的取值域一致）。
 *
 * 后端只接受内置五档 id 或 `tier_<数字>`；前端曾用 `tier-1`（连字符），保存必然 422 `id_invalid`。
 * 序号优先用后台读面下发的 `nextTierSeq`（服务端知道下一个可用号），没下发时才从现有 id 里
 * 取最小空位；`used` 冲突时继续自增，绝不返回重复 id。
 */
export function nextTierId(tiers: readonly TierForm[], seq?: number): string {
  const used = new Set(tiers.map(tier => tier.id))
  const start = Number.isFinite(seq) && Number(seq) > 0 ? Math.trunc(Number(seq)) : 1
  for (let index = start; index <= start + TRUST_LEVEL_TIER_MAX * 4; index += 1) {
    const candidate = `tier_${index}`
    if (!used.has(candidate)) return candidate
  }
  return `tier_${start + TRUST_LEVEL_TIER_MAX * 4 + 1}`
}

/** 现有 `tier_<n>` 里的最大序号（服务端没下发 `nextTierSeq` 时的本地兜底）。 */
export function maxTierSeq(tiers: readonly TierForm[]): number {
  let max = 0
  for (const tier of tiers) {
    const match = /^tier_(\d+)$/.exec(tier.id)
    if (match) max = Math.max(max, Number(match[1]))
  }
  return max
}

/** 新档的初始形状：0 门槛、无限额覆盖、无新增解锁、未停用。 */
export function makeTier(id: string, icon: string | null = null): TierForm {
  return {
    id: id.slice(0, TRUST_LEVEL_TIER_ID_MAX),
    name: { zh: '', en: '' },
    icon,
    // 新档不写颜色：保存时按位次取默认，运营想固定下来才显式选。
    color: null,
    requirements: zeroRequirements(),
    limits: { ...EMPTY_TIER_LIMITS },
    earnable: [],
    retired: false,
    rule: null
  }
}

/**
 * 在末尾追加一档（active 档数上限 8）。
 * 达到上限时返回等值副本 —— 组件侧按钮同步禁用，这里只是最后一道闸。
 */
export function addTier(tiers: readonly TierForm[], icon: string | null = null, seq?: number): TierForm[] {
  const next = cloneTiers(tiers)
  if (!canAddTier(next)) return next
  next.push(makeTier(nextTierId(next, seq), icon))
  return next
}

/**
 * 在 active rank `at` 之前插入一档（`at` 越界 → 追加到末尾）。
 *
 * 锚定语义：插入只改变数组顺序，**其它档位的配置逐字保留**（id 不变、requirements/earnable/limits 不变）。
 * 这是「在 TL2 前插一档」不偷走 TL2 阈值的原因，也是 `trustLevelTiers.test.ts` 断言的核心。
 */
export function insertTierAt(
  tiers: readonly TierForm[],
  at: number,
  icon: string | null = null,
  seq?: number
): TierForm[] {
  const next = cloneTiers(tiers)
  if (!canAddTier(next)) return next
  const actives = activeTiersOf(next)
  const rank = Math.trunc(Number(at) || 0)
  const target = rank >= 1 && rank <= actives.length ? actives[rank - 1]! : null
  const tier = makeTier(nextTierId(next, seq), icon)
  if (!target) {
    next.push(tier)
    return next
  }
  const insertIndex = next.findIndex(entry => entry.id === target.id)
  next.splice(insertIndex, 0, tier)
  return next
}

/** 软停用：保留配置与位置，只把 `retired` 置真（D-N8）。最后一档不允许停用。 */
export function retireTier(tiers: readonly TierForm[], id: string): TierForm[] {
  const next = cloneTiers(tiers)
  if (!canRetireTier(next)) return next
  const tier = next.find(entry => entry.id === id)
  if (!tier || tier.retired) return next
  tier.retired = true
  return next
}

/** 恢复停用档：回到它原来的位置（数组位置未变），并重新参与判定。上限 8 档。 */
export function restoreTier(tiers: readonly TierForm[], id: string): TierForm[] {
  const next = cloneTiers(tiers)
  if (!canAddTier(next)) return next
  const tier = next.find(entry => entry.id === id)
  if (!tier || !tier.retired) return next
  tier.retired = false
  return next
}

/**
 * 上移/下移一档：与相邻的 **active** 档交换数组位置。
 *
 * `delta` 只用符号（-1 / +1）；越界时返回等值副本。停用档位夹在中间时，
 * 交换两端条目即可 —— 停用档的相对位置不变，active 顺序按预期改变。
 */
export function moveTier(tiers: readonly TierForm[], id: string, delta: number): TierForm[] {
  const next = cloneTiers(tiers)
  const actives = activeTiersOf(next)
  const rank = actives.findIndex(tier => tier.id === id)
  const direction = Math.sign(Math.trunc(Number(delta) || 0))
  if (rank < 0 || direction === 0) return next
  const targetRank = rank + direction
  if (targetRank < 0 || targetRank >= actives.length) return next
  const fromId = actives[rank]!.id
  const toId = actives[targetRank]!.id
  const fromIndex = next.findIndex(tier => tier.id === fromId)
  const toIndex = next.findIndex(tier => tier.id === toId)
  const swapped = next[fromIndex]!
  next[fromIndex] = next[toIndex]!
  next[toIndex] = swapped
  return next
}

/** 改名：只动当前语言的字段，另一语言不动（运营分语言维护，不互相覆盖）。 */
export function renameTier(
  tiers: readonly TierForm[],
  id: string,
  locale: 'zh' | 'en',
  value: string
): TierForm[] {
  const next = cloneTiers(tiers)
  const tier = next.find(entry => entry.id === id)
  if (!tier) return next
  tier.name[locale] = String(value ?? '').replace(/\s+/g, ' ').trim().slice(0, TRUST_LEVEL_TIER_NAME_MAX)
  return next
}

/**
 * 选图标（D-N4）：闸门是**形状**，不是一份候选清单。
 *
 * 后端接受两种合法取值：16 个默认短名（`tierIcons`），或 `i-(lucide|tabler)-<name>` 全名。
 * 图标选择器是 lucide + tabler 全量目录，运营能选到的名字**不可能**等于那份 16 项白名单；
 * 拿白名单当闸门只会把合法选择静默丢掉，而保存 422 的风险早已由形状校验覆盖。
 * `null` = 清空（回落到 rank 默认序列）。
 */
export function setTierIcon(
  tiers: readonly TierForm[],
  id: string,
  icon: string | null
): TierForm[] {
  const next = cloneTiers(tiers)
  const tier = next.find(entry => entry.id === id)
  if (!tier) return next
  if (icon === null) {
    tier.icon = null
    return next
  }
  const name = icon.trim()
  if (!name) return next
  if (!HOST_ICON_NAME_PATTERN.test(name) && !(DEFAULT_TIER_ICON_NAMES as readonly string[]).includes(name)) {
    return next
  }
  tier.icon = name
  return next
}

/**
 * 选颜色（D-C1）：合法值 = 推荐 token ∪ 自定义 `#rrggbb`（后端已放宽到同一取值域）。
 *
 * `null` = 回到「按位次自动」（合法，且是默认值）；形状不合法的值一律拒绝（等值副本），
 * 因为写一个后端不认的色值等于保存 422 —— 与 `setTierIcon` 同一口径。
 * 推荐色板只是**渲染候选**，不是闸门：候选之外的 hex 也必须能落进表单。
 */
export function setTierColor(
  tiers: readonly TierForm[],
  id: string,
  color: string | null
): TierForm[] {
  const next = cloneTiers(tiers)
  const tier = next.find(entry => entry.id === id)
  if (!tier) return next
  if (color === null) {
    tier.color = null
    return next
  }
  if (!isTierColorValue(color.trim())) return next
  tier.color = normalizeTierColor(color)
  return next
}

/**
 * 能否删除：**只有停用的档位**可以删（D-C2）。
 *
 * active 档位直接删会绕过「停用两步闸门」，还会让 rank 静默前移 —— 那等于悄悄重排所有人的等级。
 */
export function canDeleteTier(tiers: readonly TierForm[], id: string): boolean {
  return tiers.some(tier => tier.id === id && tier.retired)
}

/**
 * 删除一档（只从表单里移除；墓碑 id 由调用方记进 `form.deletedTierIds`）。
 *
 * 非停用或不存在 → 等值副本（fail-closed）。这一个函数就是「删除永不改 rank」的实现点：
 * 停用档不在 active 序列里，移除它不会移动任何在位档位的 rank。
 */
export function deleteTier(tiers: readonly TierForm[], id: string): TierForm[] {
  const next = cloneTiers(tiers)
  if (!canDeleteTier(next, id)) return next
  return next.filter(tier => tier.id !== id)
}

/**
 * 某一档若被停用/删除会撤回的解锁：**该档给、其它 active 档都不给**的键。
 *
 * 与 `retiredImpactKeys` 同一判据，但按档取：已停用行要常驻显示「已撤回 N 项解锁」，
 * 而不能把所有停用档的影响混成一句。
 */
export function retiredTierImpactKeys(tiers: readonly TierForm[], id: string): string[] {
  const tier = tiers.find(entry => entry.id === id)
  if (!tier) return []
  const otherActiveKeys = new Set(
    activeTiersOf(tiers).filter(entry => entry.id !== id).flatMap(entry => entry.earnable)
  )
  return [...new Set(tier.earnable)].filter(key => !otherActiveKeys.has(key))
}

/** 写一个门槛值：非负整数、超上限回落 0（输入护栏，不是业务上限）。 */
export function setTierRequirement(
  tiers: readonly TierForm[],
  id: string,
  suffix: string,
  value: string | number,
  max = 100000
): TierForm[] {
  const next = cloneTiers(tiers)
  const tier = next.find(entry => entry.id === id)
  if (!tier) return next
  const parsed = Number(value)
  const normalized = Number.isFinite(parsed) ? Math.trunc(parsed) : 0
  tier.requirements[suffix] = normalized >= 0 && normalized <= max ? normalized : 0
  return next
}

/**
 * 停用会产生降权风险的键（D-N8 披露用）：**只有**「退役档位给、现存 active 档位都不给」的键。
 *
 * 措辞必须留有余地（「可能」），因为累积语义的最终裁决在后端 `Earnable.Grants()`；
 * 前端只负责把**可预见**的影响面摆到运营眼前，不替后端下结论。
 */
export function retiredImpactKeys(tiers: readonly TierForm[]): string[] {
  const activeKeys = new Set(activeTiersOf(tiers).flatMap(tier => tier.earnable))
  const impacted: string[] = []
  for (const tier of tiers) {
    if (!tier.retired) continue
    for (const key of tier.earnable) {
      if (activeKeys.has(key) || impacted.includes(key)) continue
      impacted.push(key)
    }
  }
  return impacted
}

/** 从存储形状（可能缺字段）收敛成表单档位；未知条目由调用方先过滤。 */
export function toTierForm(tier: TrustLevelTier): TierForm {
  return {
    id: tier.id,
    name: { zh: tier.name?.zh ?? '', en: tier.name?.en ?? '' },
    icon: tier.icon ?? null,
    color: normalizeTierColor(tier.color),
    requirements: { ...zeroRequirements(), ...(tier.requirements ?? {}) },
    limits: tier.limits ? { ...EMPTY_TIER_LIMITS, ...tier.limits } : null,
    earnable: [...(tier.earnable ?? [])],
    retired: tier.retired === true,
    // 读面来的规则**不解码、不重建**：`undefined`/`null` 都归一成 null（该档无规则）。
    rule: (tier as { rule?: unknown }).rule && typeof (tier as { rule?: unknown }).rule === 'object'
      ? (tier as { rule?: Record<string, unknown> }).rule ?? null
      : null
  }
}
