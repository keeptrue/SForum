import type { AdminWebOption } from '~/composables/useWebOptions'
import {
  TRUST_LEVEL_METRIC_KEYS,
  TRUST_LEVEL_METRIC_SUFFIX_BY_KEY,
  TRUST_LEVEL_SLUGS
} from '~/composables/profile/useTrustLevel'

export { TRUST_LEVEL_SLUGS }

/**
 * 信任等级阈值（后台 tab 的读写层）。
 *
 * 契约权威是 Go：`apps/api/app/Models/TrustLevels/config.go` 的 `Config` / `LevelConfig`
 * 与 `types.go` 的 `Metrics` / `LevelLimits`。`DecodeConfig` 开了 `DisallowUnknownFields`，
 * 所以这里的字段名、容器形态、档位范围**一个都不能自创**——写错就是后端直接拒绝保存。
 *
 * 形状（schemaVersion = 1）：
 * ```
 * { "schemaVersion": 1,
 *   "levels": [ { "levelSlug": "member",
 *                 "requirements": { "days_visited": 0, … 6 个 metric … },
 *                 "limits": { "topicCooldownSeconds": null, … },
 *                 "earnable": [] } ] }
 * ```
 * 三条硬约束：
 * - `levels` 是**数组**，不是按档位下标索引的对象；
 * - **TL0 不能出现**在 `levels` 里（config.go 明确拒绝：TL0 由 `trust.new_user_*` 决定）；
 * - `limits` 的 4 个字段可空：`null` = 继承站点默认，`0` = 免冷却/不限，两者语义不同（D27）。
 */

export const TRUST_LEVELS_OPTION = 'trust.levels'
export const TRUST_LEVELS_SCHEMA_VERSION = 1

/** 表单渲染的五档（TL0 只读展示，不参与提交）。 */
export const TRUST_LEVEL_ROWS = [0, 1, 2, 3, 4] as const

/** 可编辑并进入 `levels` 的档位：TL1–TL4。 */
export const TRUST_LEVEL_EDITABLE_LEVELS = [1, 2, 3, 4] as const

export type TrustLevelThresholdMetric = {
  /** `requirements` 的字段名 / options 键后缀；与 Go `Metrics` 逐字一致。 */
  suffix: string
  /** i18n 后缀：`admin.settings.trustLevels.metric.{i18nKey}`。 */
  i18nKey: string
  /** 输入护栏上限，不是业务上限。 */
  max: number
}

/**
 * 指标名与顺序来自 `useTrustLevel` 的单一来源（= Go `Metrics`），这里只补输入护栏上限。
 * 两处各写一份 suffix 列表迟早会漂移，而漂移的代价是保存被后端拒绝。
 */
const METRIC_MAX: Record<string, number> = {
  daysVisited: 3650,
  topicCount: 10000,
  commentCount: 100000,
  likeReceived: 100000,
  likeGiven: 100000,
  bookmarkCount: 100000
}

export const TRUST_LEVEL_THRESHOLD_METRICS: TrustLevelThresholdMetric[] = TRUST_LEVEL_METRIC_KEYS.map(key => ({
  suffix: TRUST_LEVEL_METRIC_SUFFIX_BY_KEY[key],
  i18nKey: key,
  max: METRIC_MAX[key] ?? 100000
}))

/** 与 Go `LevelLimits` 同形：`null` 继承站点默认，数字即显式覆盖。 */
export type TrustLevelLimits = {
  topicCooldownSeconds: number | null
  commentCooldownSeconds: number | null
  dailyTopicLimit: number | null
  dailyCommentLimit: number | null
}

export type TrustLevelRequirements = Record<string, number>

/** 表单模型：阈值可编辑；limits / earnable 本刀不提供编辑器，但要原样带回。 */
export type TrustLevelsForm = {
  requirements: Record<string, TrustLevelRequirements>
  limits: Record<string, TrustLevelLimits>
  earnable: Record<string, string[]>
}

const EMPTY_LIMITS: TrustLevelLimits = {
  topicCooldownSeconds: null,
  commentCooldownSeconds: null,
  dailyTopicLimit: null,
  dailyCommentLimit: null
}

/**
 * 推荐默认（= Go `DefaultPolicy()` 的 TL1–TL4，逐项一致）。
 * 必须与后端同值，否则「恢复默认」会写出一份与站点推荐值不一致的阈值。
 */
const RECOMMENDED: Record<string, TrustLevelRequirements> = {
  1: { days_visited: 3, topic_count: 0, comment_count: 5, like_received_count: 0, like_given_count: 0, bookmark_count: 0 },
  2: { days_visited: 15, topic_count: 2, comment_count: 20, like_received_count: 5, like_given_count: 3, bookmark_count: 0 },
  3: { days_visited: 45, topic_count: 8, comment_count: 80, like_received_count: 30, like_given_count: 20, bookmark_count: 5 },
  4: { days_visited: 100, topic_count: 25, comment_count: 250, like_received_count: 100, like_given_count: 80, bookmark_count: 20 }
}

export function recommendedTrustLevelsForm(): TrustLevelsForm {
  const form: TrustLevelsForm = { requirements: {}, limits: {}, earnable: {} }
  for (const level of TRUST_LEVEL_EDITABLE_LEVELS) {
    form.requirements[String(level)] = { ...RECOMMENDED[String(level)]! }
    form.limits[String(level)] = { ...EMPTY_LIMITS }
    form.earnable[String(level)] = []
  }
  return form
}

export function cloneTrustLevelsForm(form: TrustLevelsForm): TrustLevelsForm {
  return {
    requirements: Object.fromEntries(Object.entries(form.requirements).map(([level, row]) => [level, { ...row }])),
    limits: Object.fromEntries(Object.entries(form.limits).map(([level, limits]) => [level, { ...limits }])),
    earnable: Object.fromEntries(Object.entries(form.earnable).map(([level, keys]) => [level, [...keys]]))
  }
}

function boundedThreshold(value: unknown, fallback: number, max: number) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return fallback
  const normalized = Math.trunc(parsed)
  return normalized >= 0 && normalized <= max ? normalized : fallback
}

/** `null` 与 0 语义不同：只把「非法值」收敛成 null，不把 0 当空。 */
function boundedLimit(value: unknown): number | null {
  if (value === null || value === undefined) return null
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return null
  const normalized = Math.trunc(parsed)
  return normalized >= 0 ? normalized : null
}

function normalizeLimits(raw: unknown): TrustLevelLimits {
  const record = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
  return {
    topicCooldownSeconds: boundedLimit(record.topicCooldownSeconds),
    commentCooldownSeconds: boundedLimit(record.commentCooldownSeconds),
    dailyTopicLimit: boundedLimit(record.dailyTopicLimit),
    dailyCommentLimit: boundedLimit(record.dailyCommentLimit)
  }
}

function normalizeEarnable(raw: unknown): string[] {
  if (!Array.isArray(raw)) return []
  return raw.filter((key): key is string => typeof key === 'string' && key.trim().length > 0)
}

/**
 * 把任意来源（option JSON / 表单）收敛成合法表单：阈值越界回落推荐默认，
 * limits / earnable 只做类型净化。TL0 与未知 slug 一律丢弃（后端也会拒绝）。
 */
export function normalizeTrustLevelsForm(source: unknown): TrustLevelsForm {
  const form = recommendedTrustLevelsForm()
  const record = (source && typeof source === 'object' ? source : {}) as Record<string, unknown>
  const requirements = (record.requirements && typeof record.requirements === 'object'
    ? record.requirements
    : {}) as Record<string, unknown>
  const limits = (record.limits && typeof record.limits === 'object' ? record.limits : {}) as Record<string, unknown>
  const earnable = (record.earnable && typeof record.earnable === 'object'
    ? record.earnable
    : {}) as Record<string, unknown>

  for (const level of TRUST_LEVEL_EDITABLE_LEVELS) {
    const key = String(level)
    const row = (requirements[key] && typeof requirements[key] === 'object'
      ? requirements[key]
      : {}) as Record<string, unknown>
    for (const metric of TRUST_LEVEL_THRESHOLD_METRICS) {
      form.requirements[key]![metric.suffix] = boundedThreshold(
        row[metric.suffix],
        RECOMMENDED[key]![metric.suffix]!,
        metric.max
      )
    }
    form.limits[key] = normalizeLimits(limits[key])
    form.earnable[key] = normalizeEarnable(earnable[key])
  }
  return form
}

type RawLevelConfig = {
  levelSlug?: unknown
  requirements?: unknown
  limits?: unknown
  earnable?: unknown
}

/** 把 option JSON 的 `levels` 数组摊成按等级索引的表；未知 slug 丢弃。 */
function levelsBySlug(raw: unknown): Record<string, RawLevelConfig> {
  const result: Record<string, RawLevelConfig> = {}
  if (!Array.isArray(raw)) return result
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue
    const config = entry as RawLevelConfig
    if (typeof config.levelSlug !== 'string') continue
    result[config.levelSlug] = config
  }
  return result
}

/**
 * 从 admin options 读出当前配置。
 * 键缺失、解码失败、`schemaVersion` 不匹配都回落推荐默认 —— 与后端「空值 = DefaultPolicy」一致。
 */
export function readTrustLevelsConfig(map: Record<string, AdminWebOption>): TrustLevelsForm {
  const option = map[TRUST_LEVELS_OPTION]
  if (!option) return recommendedTrustLevelsForm()
  let parsed: unknown
  try {
    parsed = JSON.parse(option.value)
  } catch {
    return recommendedTrustLevelsForm()
  }
  if (!parsed || typeof parsed !== 'object') return recommendedTrustLevelsForm()
  const config = parsed as { schemaVersion?: unknown, levels?: unknown }
  if (Number(config.schemaVersion) !== TRUST_LEVELS_SCHEMA_VERSION) return recommendedTrustLevelsForm()

  const bySlug = levelsBySlug(config.levels)
  const source = { requirements: {}, limits: {}, earnable: {} } as {
    requirements: Record<string, unknown>
    limits: Record<string, unknown>
    earnable: Record<string, unknown>
  }
  for (const level of TRUST_LEVEL_EDITABLE_LEVELS) {
    const entry = bySlug[TRUST_LEVEL_SLUGS[level]!]
    if (!entry) continue
    const key = String(level)
    source.requirements[key] = entry.requirements
    source.limits[key] = entry.limits
    source.earnable[key] = entry.earnable
  }
  return normalizeTrustLevelsForm(source)
}

/**
 * 序列化为 `saveMany` 的载荷：单键 JSON、`schemaVersion`、`levels` 数组、TL1–TL4。
 * 形状以 `apps/api/app/Models/TrustLevels/config.go` 为准；那边改字段名时只改本函数。
 */
export function trustLevelsPayload(form: TrustLevelsForm): Array<{ name: string, value: string }> {
  const levels = TRUST_LEVEL_EDITABLE_LEVELS.map(level => {
    const key = String(level)
    const requirements: Record<string, number> = {}
    for (const metric of TRUST_LEVEL_THRESHOLD_METRICS) {
      requirements[metric.suffix] = form.requirements[key]?.[metric.suffix] ?? 0
    }
    return {
      levelSlug: TRUST_LEVEL_SLUGS[level],
      requirements,
      limits: form.limits[key] ?? { ...EMPTY_LIMITS },
      earnable: form.earnable[key] ?? []
    }
  })
  return [{
    name: TRUST_LEVELS_OPTION,
    value: JSON.stringify({ schemaVersion: TRUST_LEVELS_SCHEMA_VERSION, levels })
  }]
}

/** 只允许数字键：与 newcomers tab 同口径，避免 `type=number` 收下 `1e5`/`-`/`+`。 */
export function blockNonIntegerKey(event: KeyboardEvent) {
  const allowed = ['Backspace', 'Delete', 'Tab', 'Escape', 'Enter', 'ArrowLeft', 'ArrowRight', 'Home', 'End']
  if (!allowed.includes(event.key) && !event.metaKey && !event.ctrlKey && !/^\d$/.test(event.key)) event.preventDefault()
}
