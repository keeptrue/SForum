import { computed, ref } from 'vue'
import { apiErrorReason, useApiClient } from '~/composables/useApiClient'
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

/** 表单模型：阈值与 earnable 可编辑；limits 本刀不提供编辑器，但要原样带回。 */
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

/* ────────────────────────────────────────────────────────────────────────────
 * earnable 白名单
 *
 * 唯一真相是 Go 的 `earnableWhitelist`（`app/Models/TrustLevels/registry.go`），
 * 前端唯一的数据源是后台读面 `GET /admin/trust-levels` 的 `eligiblePermissions`。
 * 这里**不许**出现那七个权限键的字面量：硬编码一份就是第二份副本，两份迟早漂移，
 * 而漂移的表现是「UI 能勾、保存 422」——最贵的那种 bug。
 * ──────────────────────────────────────────────────────────────────────────── */

/** 后台读面路径（`useApiClient` 自己补 `/api/v1` 前缀，这里不写绝对地址）。 */
export const TRUST_LEVEL_ADMIN_READ_PATH = '/admin/trust-levels'

export type TrustLevelWhitelistStatus = 'idle' | 'loading' | 'ready' | 'failed'

export type TrustLevelWhitelistState = {
  status: TrustLevelWhitelistStatus
  /**
   * 仅在 `status === 'ready'` 时有意义。
   * `failed` 时同样是 `[]` —— 所以**禁止**用「keys 为空」判断「没有可勾的键」，
   * 那是把失败当空（T22）。
   */
  keys: string[]
  /**
   * 系统基线键：默认安装下由等级直接门控（TL0 不生效、TL1 起生效）的白名单子集。
   *
   * 唯一来源是后台读面的 `baselinePermissions`（见 `apps/web/TODO-trust-level-baseline-field.md`）。
   * 后端不下发该字段时是 `[]` —— 含义是「无可披露的基线键」，**不是**「基线键为空集」的断言：
   * 组件只在非空时才展示标注与相关文案，绝不把两个键写死在组件里（那是第二份副本）。
   */
  baselineKeys: string[]
  /** `failed` 的原因：后端 `reason`，或 `invalid_response` / `request_failed`。 */
  error: string | null
}

export function idleTrustLevelWhitelist(): TrustLevelWhitelistState {
  return { status: 'idle', keys: [], baselineKeys: [], error: null }
}

/**
 * 解析 `eligiblePermissions`。
 *
 * 返回值刻意三态：**非法形状 → `null`**、空数组 → `[]`、正常 → 键数组（去重、保序）。
 * 顺序即 UI 展示顺序（J-02），所以不做排序，只去重。
 */
export function parseEligiblePermissions(raw: unknown): string[] | null {
  if (!Array.isArray(raw)) return null
  const keys: string[] = []
  for (const entry of raw) {
    if (typeof entry !== 'string' || entry.trim().length === 0) return null
    if (!keys.includes(entry)) keys.push(entry)
  }
  return keys
}

/**
 * 解析 `baselinePermissions`（系统基线键）。
 *
 * 与 `parseEligiblePermissions` 的关键差别：这里**宽容**。
 * 白名单是矩阵的唯一渲染来源，形状非法必须整块失败（T22：空矩阵 = 谎）；
 * 而基线键只是**附加标注**，它缺失或带脏值不该让运营看不到矩阵，也不该让 UI
 * 说出没有依据的话。所以：非数组 / 非字符串 / 不在白名单内的条目一律丢弃，
 * 结果是「可披露的基线键」，可能是空。
 */
export function parseBaselinePermissions(raw: unknown, eligible: readonly string[]): string[] {
  if (!Array.isArray(raw)) return []
  const allowed = new Set(eligible)
  const keys: string[] = []
  for (const entry of raw) {
    if (typeof entry !== 'string' || entry.trim().length === 0) continue
    if (!allowed.has(entry) || keys.includes(entry)) continue
    keys.push(entry)
  }
  return keys
}

export type TrustLevelAdminReadOutcome =
  | { ok: true, body: unknown }
  | { ok: false, error: unknown }

/**
 * 把一次读面收敛成状态。四态互不冒充：
 * 请求失败（`failed` + reason）、响应形状非法（`failed` + `invalid_response`）、
 * 白名单真的为空（`ready` + `[]`）、正常（`ready` + 键）。
 */
export function resolveTrustLevelWhitelist(outcome: TrustLevelAdminReadOutcome): TrustLevelWhitelistState {
  if (!outcome.ok) {
    return { status: 'failed', keys: [], baselineKeys: [], error: apiErrorReason(outcome.error) || 'request_failed' }
  }
  const body = (outcome.body && typeof outcome.body === 'object' ? outcome.body : {}) as Record<string, unknown>
  const keys = parseEligiblePermissions(body.eligiblePermissions)
  if (!keys) return { status: 'failed', keys: [], baselineKeys: [], error: 'invalid_response' }
  return { status: 'ready', keys, baselineKeys: parseBaselinePermissions(body.baselinePermissions, keys), error: null }
}

/** 白名单净化：丢掉白名单外的键、去重，并按白名单顺序排列。 */
export function sanitizeEarnableKeys(keys: readonly string[], whitelist: readonly string[]): string[] {
  const selected = new Set(keys)
  return whitelist.filter(key => selected.has(key))
}

/** 白名单外（或已失效）的键：UI 用它披露「保存会移除这些键」，而不是静默丢数据。 */
export function unknownEarnableKeys(keys: readonly string[], whitelist: readonly string[]): string[] {
  const allowed = new Set(whitelist)
  return [...new Set(keys.filter(key => !allowed.has(key)))]
}

/** 某档的 earnable 键。`noUncheckedIndexedAccess` 下模板里双下标会 TS2532，统一走这里。 */
export function earnableKeys(form: TrustLevelsForm, level: number): string[] {
  return form.earnable[String(level)] ?? []
}

/**
 * 切换「某档**新增**某键」——纯函数，返回新表单，不改入参。
 *
 * 累积语义由后端 `Earnable.Grants()` 负责（勾 TL2 = TL2 及以上全部解锁），
 * 这里**只**记该档新增，绝不把低档已勾的键复制到高档——复制会造出第二套语义。
 * 非可编辑档（TL0）与白名单外的键一律拒绝：返回等值副本，不报错（fail-closed）。
 */
export function toggleEarnableKey(
  form: TrustLevelsForm,
  level: number,
  key: string,
  whitelist: readonly string[]
): TrustLevelsForm {
  const next = cloneTrustLevelsForm(form)
  if (!(TRUST_LEVEL_EDITABLE_LEVELS as readonly number[]).includes(level)) return next
  if (!whitelist.includes(key)) return next
  const levelKey = String(level)
  const current = next.earnable[levelKey] ?? []
  const toggled = current.includes(key) ? current.filter(entry => entry !== key) : [...current, key]
  next.earnable[levelKey] = sanitizeEarnableKeys(toggled, whitelist)
  return next
}

/** 整表净化：每档的键都保留（`[]` 也保留），只清掉白名单外的键。 */
export function sanitizeEarnableForm(form: TrustLevelsForm, whitelist: readonly string[]): TrustLevelsForm {
  const next = cloneTrustLevelsForm(form)
  for (const level of TRUST_LEVEL_EDITABLE_LEVELS) {
    const levelKey = String(level)
    next.earnable[levelKey] = sanitizeEarnableKeys(next.earnable[levelKey] ?? [], whitelist)
  }
  return next
}

/**
 * 保存时该不该带白名单：**只有 `ready` 才带**。
 *
 * `idle` / `loading` / `failed` 一律返回 `undefined` = 走 `trustLevelsPayload` 的「原样带回」，
 * 因为此时我们并不知道哪些键合法。把「读不到白名单」当成「白名单为空」会在保存时
 * 静默清空运营已配好的能力解锁——那是一次没有报错的数据丢失。
 */
export function earnableWhitelistForSave(state: TrustLevelWhitelistState): readonly string[] | undefined {
  return state.status === 'ready' ? state.keys : undefined
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
 *
 * 传了 `whitelist` 就先按白名单净化（白名单外的键不发出去——后端会拒，前端不该发）。
 * 不传 = 原样带回：白名单没读到时我们无法判断哪些键合法，此时**不许**凭空丢弃已存配置。
 *
 * T23：`earnable` **必须每档都出现**且是数组。
 * `[]` = 该档无新增（合法）；省略 = `DecodeConfig` 直接 422（`len(RawMessage)==0`）。
 * `null` 后端也当空集，但前端不写 `null`——形态越少，往返越不会漂。
 */
export function trustLevelsPayload(
  form: TrustLevelsForm,
  whitelist?: readonly string[]
): Array<{ name: string, value: string }> {
  const levels = TRUST_LEVEL_EDITABLE_LEVELS.map(level => {
    const key = String(level)
    const requirements: Record<string, number> = {}
    for (const metric of TRUST_LEVEL_THRESHOLD_METRICS) {
      requirements[metric.suffix] = form.requirements[key]?.[metric.suffix] ?? 0
    }
    const stored = form.earnable[key] ?? []
    return {
      levelSlug: TRUST_LEVEL_SLUGS[level],
      requirements,
      limits: form.limits[key] ?? { ...EMPTY_LIMITS },
      earnable: whitelist ? sanitizeEarnableKeys(stored, whitelist) : stored
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

/**
 * 后台白名单读面（M10）。
 *
 * 失败**不**回落成空矩阵：`state.status === 'failed'` 时矩阵必须整块不可编辑 + 常驻错误条，
 * 因为空矩阵在运营眼里等于「后端说没有可勾的键」，那是谎（T22）。
 * 组件侧用 `isEditable` 决定能否勾选、`state.error` 决定错误条文案、`state.keys` 决定列。
 */
export function useTrustLevelEarnableWhitelist(
  /**
   * 传输层注入点（只给测试用）：不传就走 `useApiClient()`。
   * 测试要断言的是「开关关闭时一次请求都不发」，那只能数真实请求次数，
   * 不能靠替换整个状态机（那是让测试断言自己的实现）。
   */
  requestOverride?: (path: string) => Promise<unknown>
) {
  const request = requestOverride ?? useApiClient().request
  const state = ref<TrustLevelWhitelistState>(idleTrustLevelWhitelist())
  const isEditable = computed(() => state.value.status === 'ready')
  const pending = computed(() => state.value.status === 'loading')

  async function load(): Promise<TrustLevelWhitelistState> {
    state.value = { status: 'loading', keys: [], baselineKeys: [], error: null }
    let outcome: TrustLevelAdminReadOutcome
    try {
      outcome = { ok: true, body: await request<unknown>(TRUST_LEVEL_ADMIN_READ_PATH) }
    } catch (error) {
      outcome = { ok: false, error }
    }
    state.value = resolveTrustLevelWhitelist(outcome)
    return state.value
  }

  return { state, isEditable, pending, load }
}
