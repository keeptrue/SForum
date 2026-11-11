import {
  cloneTiers,
  EMPTY_TIER_LIMITS,
  type TierForm,
  type TrustLevelLimits
} from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'

/**
 * 分档限额的**纯函数**编辑层（无 Vue、无 i18n、无 DOM）。
 *
 * 三态语义（后端 `config.go` 的实现口径，不可简化）：
 * 1. **字段省略**（`len(raw) == 0`）→ 后端 422。前端永远不写这种形态：
 *    `trustLevelsPayload` 对 `limits` 只输出 `null` 或对象两态。
 * 2. **`null`** → 该档完全继承站点默认（四个字段都不覆盖）。
 * 3. **对象**（可只含部分字段）→ 逐字段覆盖；对象里某个字段是 `null` = 该字段仍继承。
 *
 * 关键区别：**`0` 不是「没配」**。`topicCooldownSeconds: 0` = 免冷却，
 * `dailyTopicLimit: 0` = 不限量，和「继承站点默认」（`null`）是两件事。
 * 所有写操作都返回新数组，非法值返回**等值副本**（fail-closed，不抛错）。
 */

/** 四个分档限额字段（顺序 = 展示顺序，与 Go `LevelLimits` 字段同名）。 */
export const TRUST_LEVEL_LIMIT_KEYS = [
  'topicCooldownSeconds',
  'commentCooldownSeconds',
  'dailyTopicLimit',
  'dailyCommentLimit'
] as const

export type TrustLevelLimitKey = typeof TRUST_LEVEL_LIMIT_KEYS[number]

/** 继承 = `limits === null`；其余都算逐字段覆盖（哪怕对象里的值全是 null）。 */
export type TrustLevelLimitMode = 'inherit' | 'custom'

/** 输入框的字符串形态：空串 = 该字段继承（不是 0）。 */
export type TrustLevelLimitInput = Record<TrustLevelLimitKey, string>

/**
 * 输入护栏上限：**这只是输入护栏，不是业务上限**。
 *
 * 写侧（本文件）用它把「手滑多按几个 0」挡在表单之外；读侧 `useTrustLevelsForm.normalizeLimits`
 * 的 `boundedLimit` **故意不设上限** —— 服务端可能已经存了更大的值，读的时候截断会把运营配置
 * 静默改小。两面不对称是刻意的：读宽容、写严格。
 */
export const TRUST_LEVEL_LIMIT_MAX: Record<TrustLevelLimitKey, number> = {
  topicCooldownSeconds: 30 * 24 * 3600,
  commentCooldownSeconds: 30 * 24 * 3600,
  dailyTopicLimit: 100000,
  dailyCommentLimit: 100000
}

export function limitsModeOf(limits: TrustLevelLimits | null | undefined): TrustLevelLimitMode {
  return limits === null || limits === undefined ? 'inherit' : 'custom'
}

/** 某字段的存储值：`null` 表示该字段继承站点默认（`0` 是明确的不限额）。 */
export function limitValueOf(
  limits: TrustLevelLimits | null | undefined,
  key: TrustLevelLimitKey
): number | null {
  if (!limits) return null
  const value = limits[key]
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.trunc(value) : null
}

/** 存储形状 → 输入框字符串（空串 = 继承，`"0"` = 明确不限额）。 */
export function toLimitsForm(limits: TrustLevelLimits | null | undefined): TrustLevelLimitInput {
  const input = {} as TrustLevelLimitInput
  for (const key of TRUST_LEVEL_LIMIT_KEYS) {
    const value = limitValueOf(limits, key)
    input[key] = value === null ? '' : String(value)
  }
  return input
}

/**
 * 解析一个输入值：
 * - `null` / `''` → `null`（该字段继承站点默认）
 * - 合法的非负整数（不超输入护栏）→ 数字（`0` 保留为 0，语义是「不限额」）
 * - 其它（负数 / 小数 / 非数字 / 超上限）→ `undefined` = 非法，调用方应保持原值
 */
export function parseLimitInput(
  raw: string | number | null | undefined,
  key: TrustLevelLimitKey
): number | null | undefined {
  if (raw === null || raw === undefined) return null
  const text = String(raw).trim()
  if (text === '') return null
  if (!/^\d+$/.test(text)) return undefined
  const parsed = Number(text)
  if (!Number.isFinite(parsed)) return undefined
  const max = TRUST_LEVEL_LIMIT_MAX[key]
  return parsed <= max ? parsed : undefined
}

/** 输入框字符串 → 存储形状（非法值按「继承」收敛，与 `toLimitsForm` 对称）。 */
export function fromLimitsForm(input: Partial<TrustLevelLimitInput> | null | undefined): TrustLevelLimits {
  const limits = { ...EMPTY_TIER_LIMITS }
  for (const key of TRUST_LEVEL_LIMIT_KEYS) {
    const parsed = parseLimitInput(input?.[key] ?? '', key)
    limits[key] = typeof parsed === 'number' ? parsed : null
  }
  return limits
}

/**
 * 切换三态：
 * - `inherit` → `limits = null`（丢掉逐字段覆盖；这是「完全继承」的语义，不做值备份）。
 * - `custom` → 若原来是 `null`，落一个四字段全 `null` 的对象（= 全部仍继承，但已进入逐字段模式）；
 *   已是对象则保留现有值。
 */
export function setLimitsMode(
  tiers: readonly TierForm[],
  id: string,
  mode: TrustLevelLimitMode
): TierForm[] {
  const next = cloneTiers(tiers)
  const tier = next.find(entry => entry.id === id)
  if (!tier) return next
  if (mode === 'inherit') {
    tier.limits = null
    return next
  }
  if (tier.limits === null) tier.limits = { ...EMPTY_TIER_LIMITS }
  return next
}

/**
 * 写一个限额字段。
 *
 * `null` = 该字段仍继承；数字 = 覆盖（`0` 就是不限额）。
 * 非法值（负数/小数/超护栏）→ 等值副本，不把脏值写进表单。
 * 若该档当时是 `null`（完全继承）又写入了合法值，会自动升级成对象 —— 否则那个值无处安放。
 */
export function setLimitValue(
  tiers: readonly TierForm[],
  id: string,
  key: TrustLevelLimitKey,
  value: string | number | null
): TierForm[] {
  const next = cloneTiers(tiers)
  const tier = next.find(entry => entry.id === id)
  if (!tier) return next
  if (!TRUST_LEVEL_LIMIT_KEYS.includes(key)) return next
  const parsed = parseLimitInput(value, key)
  if (parsed === undefined) return next
  const limits = { ...EMPTY_TIER_LIMITS, ...(tier.limits ?? {}) }
  limits[key] = parsed
  tier.limits = limits
  return next
}
