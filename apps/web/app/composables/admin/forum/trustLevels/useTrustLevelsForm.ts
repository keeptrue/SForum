import { computed, ref } from 'vue'
import { apiErrorReason, useApiClient } from '~/composables/useApiClient'
import type { AdminWebOption } from '~/composables/useWebOptions'
import {
  FALLBACK_TIERS,
  TRUST_LEVEL_METRIC_KEYS,
  TRUST_LEVEL_METRIC_SUFFIX_BY_KEY,
  TRUST_LEVEL_SLUGS,
  defaultTierIconName,
  tierIconClass,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'
import { normalizeTierColor } from '~/utils/profile/trustLevelColors'
import {
  activeTiersOf,
  cloneTiers,
  EMPTY_TIER_LIMITS,
  toTierForm,
  TRUST_LEVEL_TIER_MAX,
  TRUST_LEVEL_TIER_MIN,
  zeroRequirements,
  type TierForm,
  type TrustLevelLimits,
  type TrustLevelRequirements
} from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'

/** 内置五档 slug 只在 v1 升级与 i18n 回退里出现（不是档位数量的定义）。 */
export { TRUST_LEVEL_SLUGS }
export type { TrustLevelLimits, TrustLevelRequirements, TierForm }

/**
 * 信任等级档位与能力解锁的读写层（`/control-panel/forum/trust-levels` 页的纯逻辑部分）。
 *
 * 契约权威是 Go：`apps/api/app/Models/TrustLevels/config.go`。`DecodeConfig` 开了
 * `DisallowUnknownFields`，所以字段名、容器形态、档位范围**一个都不能自创**——写错就是保存被拒。
 *
 * 形状（schemaVersion = 2，唯一写出口）：
 * ```
 * { "schemaVersion": 2,
 *   "tiers": [ { "id": "member",
 *                "name": { "zh": "正式用户", "en": "Member" },
 *                "icon": "leaf",
 *                "requirements": { "days_visited": 3, … 6 个 metric … },
 *                "limits": null,
 *                "earnable": [],
 *                "retired": false } ] }
 * ```
 * 五条硬约束：
 * - `tiers` 是**数组**，数组顺序 = 判定与展示顺序；`rank` 由 active 位次派生，TL0 **不在**数组里；
 * - `earnable` 每档必须显式出现且是数组（省略 = `len(RawMessage)==0` = 422）；
 * - `limits` 的 `null` ≠ `0`：`null` 继承站点默认，`0` 免冷却/不限（D27）；
 * - v1（`levels[]` + `levelSlug`）读入时在内存升级为 v2，**写出只写 v2**；
 * - 推荐默认值的唯一来源是后台读面的 `recommendedConfig`；本地 `FALLBACK_TIERS` 只兜底（D-N9）。
 */

export const TRUST_LEVELS_OPTION = 'trust.levels'
export const TRUST_LEVELS_SCHEMA_VERSION = 2
/** 旧档（v1）：`levels[]` + `levelSlug`，读入时升级，不再写出。 */
export const TRUST_LEVELS_LEGACY_SCHEMA_VERSION = 1

export type TrustLevelThresholdMetric = {
  /** `requirements` 的字段名 / options 键后缀；与 Go `Metrics` 逐字一致。 */
  suffix: string
  /** i18n 后缀：`admin.forum.trustLevels.metric.{i18nKey}`。 */
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

/** 表单模型：唯一的档位序列（TL0 不在其中）+ 删除墓碑。 */
export type TrustLevelsForm = {
  tiers: TierForm[]
  /**
   * 已删除档位的 id 墓碑（D-C3）：服务端据此抬高 `nextTierSeq`，id 永不复用。
   * `undefined` / 空数组**不写进载荷** —— 这是回滚口径（旧读面不认这个字段）。
   */
  deletedTierIds?: string[]
}

/**
 * 本地兜底档位（**只**在读面不可用时使用，D-N9）。
 *
 * 颜色刻意置空（= 跟随位次），不继承 `FALLBACK_TIERS.color`：那份颜色只服务**公开面兜底展示**
 * （目录读不到时卡片也要有色）。若把默认色钉进表单，运营第一次保存就会把颜色写死，
 * 之后上下移动档位，颜色不再跟着位次走 —— 那不是「默认按位次配色」。
 */
export function fallbackTiers(): TierForm[] {
  return FALLBACK_TIERS.map(tier => ({ ...toTierForm(tier), color: null }))
}

/**
 * 推荐默认（= 后端 `recommendedConfig` 解码结果；读面不可用时回落 `FALLBACK_TIERS`）。
 * 传 `tiers` 时以传入的推荐值为准 —— 读面可用时它就是唯一来源。
 */
export function recommendedTrustLevelsForm(tiers?: readonly TrustLevelTier[] | readonly TierForm[] | null): TrustLevelsForm {
  const source = tiers && tiers.length > 0
    ? tiers.map(tier => toTierForm(tier as TrustLevelTier))
    : fallbackTiers()
  return { tiers: source }
}

export function cloneTrustLevelsForm(form: TrustLevelsForm): TrustLevelsForm {
  const next: TrustLevelsForm = { tiers: cloneTiers(form.tiers) }
  // 空墓碑不落键：`{ deletedTierIds: [] }` 与「没有墓碑」在载荷上是同一件事，多一份空数组只是噪声。
  if (form.deletedTierIds && form.deletedTierIds.length > 0) next.deletedTierIds = [...form.deletedTierIds]
  return next
}

/** 墓碑 id 收敛：去空白、去空串、去重、保序（顺序即删除先后，便于对账）。 */
function normalizeTombstones(raw: unknown): string[] {
  if (!Array.isArray(raw)) return []
  const ids: string[] = []
  for (const entry of raw) {
    if (typeof entry !== 'string') continue
    const id = entry.trim().slice(0, 40)
    if (!id || ids.includes(id)) continue
    ids.push(id)
  }
  return ids
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

function normalizeLimits(raw: unknown): TrustLevelLimits | null {
  // `null` 是合法值（继承站点默认），不是「缺字段」：两者都收敛成 null，语义一致。
  if (raw === null || raw === undefined) return null
  if (typeof raw !== 'object') return null
  const record = raw as Record<string, unknown>
  return {
    topicCooldownSeconds: boundedLimit(record.topicCooldownSeconds),
    commentCooldownSeconds: boundedLimit(record.commentCooldownSeconds),
    dailyTopicLimit: boundedLimit(record.dailyTopicLimit),
    dailyCommentLimit: boundedLimit(record.dailyCommentLimit)
  }
}

function normalizeEarnable(raw: unknown): string[] {
  if (!Array.isArray(raw)) return []
  const keys: string[] = []
  for (const key of raw) {
    if (typeof key !== 'string') continue
    const trimmed = key.trim()
    if (!trimmed || keys.includes(trimmed)) continue
    keys.push(trimmed)
  }
  return keys
}

function normalizeName(raw: unknown, fallback?: { zh?: string, en?: string }): { zh: string, en: string } {
  const record = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
  return {
    zh: typeof record.zh === 'string' ? record.zh.trim() : (fallback?.zh ?? ''),
    en: typeof record.en === 'string' ? record.en.trim() : (fallback?.en ?? '')
  }
}

function normalizeIcon(raw: unknown): string | null {
  if (typeof raw !== 'string') return null
  const trimmed = raw.trim()
  if (!trimmed) return null
  // 存储口径是**两种合法形态**：16 个默认短名（`leaf`），或 `i-(lucide|tabler)-<name>` 全名。
  // 这里绝不能把 `i-lucide-` 前缀剥掉 —— 剥完得到的短名不在后端 16 项白名单里（`arrow-up` 等），
  // 界面能选、保存必 422。归一化只做 trim，形态由后端 `IsTierIcon` 判定。
  return trimmed
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

/** 图标白名单的一项：`value` 是存储取值的原样（短名或 `i-<collection>-<name>` 全名），`label` 是运营看到的人话。 */
export type TrustLevelIconChoice = { value: string, label: string }

/** 命名池的一项：编辑辅助，**不是**存储字段（D-N3：存储只有 `name{zh,en}`）。 */
export type TrustLevelNameSuggestion = { zh: string, en: string }

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
   * 后端不下发该字段时是 `[]` —— 含义是「无可披露的基线键」，**不是**「基线键为空集」的断言。
   */
  baselineKeys: string[]
  /**
   * 推荐默认配置（D-N9 的**唯一权威**）。`null` = 读面没给或形状非法 → 用 `FALLBACK_TIERS` 兜底，
   * 且「恢复默认」按钮的提示必须说明正在用本地兜底（不能假装那就是后端推荐值）。
   */
  recommended: TrustLevelsForm | null
  /** 命名池（D-N3）：只做「一键填入」，可以是空数组。 */
  namePool: TrustLevelNameSuggestion[]
  /** 图标白名单（D-N4）：可以是空数组 = 后端未下发 → 选择器只展示当前值，不提供候选。 */
  iconChoices: TrustLevelIconChoice[]
  /** 颜色白名单（D-C1）：空 = 后端未下发 → 颜色选择器不编造候选。 */
  colorChoices: TrustLevelColorChoice[]
  /**
   * 下一个新档序号（后端 `nextTierSeq`）：新档 id 必须是 `tier_<n>`，序号由服务端派生。
   * `0` = 后端没下发 → 调用方退化为「现有 id 里取最小空位」。
   */
  nextTierSeq: number
  /** `failed` 的原因：后端 `reason`，或 `invalid_response` / `request_failed`。 */
  error: string | null
}

export function idleTrustLevelWhitelist(): TrustLevelWhitelistState {
  return {
    status: 'idle',
    keys: [],
    baselineKeys: [],
    recommended: null,
    namePool: [],
    iconChoices: [],
    colorChoices: [],
    nextTierSeq: 0,
    error: null
  }
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
 * 与 `parseEligiblePermissions` 的关键差别：这里**宽容**。白名单是矩阵的唯一渲染来源，
 * 形状非法必须整块失败（T22：空矩阵 = 谎）；而基线键只是**附加标注**，缺失或脏值
 * 不该让运营看不到矩阵，也不该让 UI 说出没有依据的话。
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

/** 图标白名单：宽容解析（`string[]` 或 `{value|name|id, label?}[]`），非法条目丢弃。 */
export function parseIconChoices(raw: unknown): TrustLevelIconChoice[] {
  if (!Array.isArray(raw)) return []
  const choices: TrustLevelIconChoice[] = []
  const seen = new Set<string>()
  for (const entry of raw) {
    const record = (entry && typeof entry === 'object' ? entry : {}) as Record<string, unknown>
    const value = typeof entry === 'string'
      ? entry
      : (typeof record.value === 'string'
          ? record.value
          : (typeof record.name === 'string' ? record.name : (typeof record.id === 'string' ? record.id : '')))
    const normalized = normalizeIcon(value)
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    const label = typeof record.label === 'string' && record.label.trim()
      ? record.label.trim()
      : normalized
    choices.push({ value: normalized, label })
  }
  return choices
}

/**
 * 颜色白名单（D-C1）：`colorChoices` 的 token 数与合法 rank 数相等（= `MaxLevel + 1`，见
 * `trustLevelColors.ts` 与后端 `tier_colors.go`），**顺序即默认色序**（index = rank）。
 *
 * 与 `parseIconChoices` 同形：宽容解析、非法条目丢弃、白名单外的 token 一律不认
 * （后端 `color_not_whitelisted` 会拒）。`label` 由 UI 走 i18n（`colors.<token>`）解析，
 * 这里给的是**兜底文本**（token 本身），这样组件在白名单缺 label 时也有可读文案。
 */
export type TrustLevelColorChoice = { value: string, label: string }

export function parseColorChoices(raw: unknown): TrustLevelColorChoice[] {
  if (!Array.isArray(raw)) return []
  const choices: TrustLevelColorChoice[] = []
  const seen = new Set<string>()
  for (const entry of raw) {
    const value = typeof entry === 'string'
      ? entry
      : (entry && typeof entry === 'object' ? (entry as Record<string, unknown>).value : '')
    const normalized = normalizeTierColor(typeof value === 'string' ? value.trim() : null)
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    choices.push({ value: normalized, label: normalized })
  }
  return choices
}

/**
 * 命名池：宽容解析。
 *
 * wire 上的形状是**平行数组** `{ zh: string[], en: string[] }`（后端 `trustLevelNamePool`），
 * 这里按同一下标配对；同时也接受 `{zh,en}[]` / `{name:{zh,en}}[]` / 单串数组（历史与测试便利）。
 * 非法条目丢弃、去重保序 —— 它只是编辑辅助，坏了不该让页面报错。
 */
export function parseNamePool(raw: unknown): TrustLevelNameSuggestion[] {
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
    const record = raw as Record<string, unknown>
    const zh = Array.isArray(record.zh) ? record.zh : []
    const en = Array.isArray(record.en) ? record.en : []
    const pool: TrustLevelNameSuggestion[] = []
    const length = Math.max(zh.length, en.length)
    for (let index = 0; index < length; index += 1) {
      const suggestion = {
        zh: typeof zh[index] === 'string' ? (zh[index] as string).trim() : '',
        en: typeof en[index] === 'string' ? (en[index] as string).trim() : ''
      }
      if (!suggestion.zh && !suggestion.en) continue
      if (pool.some(item => item.zh === suggestion.zh && item.en === suggestion.en)) continue
      pool.push(suggestion)
    }
    return pool
  }
  if (!Array.isArray(raw)) return []
  const pool: TrustLevelNameSuggestion[] = []
  for (const entry of raw) {
    if (typeof entry === 'string') {
      const text = entry.trim()
      if (text) pool.push({ zh: text, en: text })
      continue
    }
    if (!entry || typeof entry !== 'object') continue
    const record = entry as Record<string, unknown>
    const source = record.name && typeof record.name === 'object' ? record.name : record
    const name = normalizeName(source)
    if (!name.zh && !name.en) continue
    if (pool.some(item => item.zh === name.zh && item.en === name.en)) continue
    pool.push(name)
  }
  return pool
}

export type TrustLevelAdminReadOutcome =
  | { ok: true, body: unknown }
  | { ok: false, error: unknown }

/**
 * 把一次读面收敛成状态。四态互不冒充：
 * 请求失败（`failed` + reason）、响应形状非法（`failed` + `invalid_response`）、
 * 白名单真的为空（`ready` + `[]`）、正常（`ready` + 键）。
 *
 * `recommendedConfig` / `namePool` / `iconChoices` 与白名单**分开判定**：
 * 前三个是编辑辅助（缺失 → 本地兜底），白名单是矩阵的唯一渲染来源（缺失 → 整块失败）。
 */
export function resolveTrustLevelWhitelist(outcome: TrustLevelAdminReadOutcome): TrustLevelWhitelistState {
  const empty = idleTrustLevelWhitelist()
  if (!outcome.ok) {
    return { ...empty, status: 'failed', error: apiErrorReason(outcome.error) || 'request_failed' }
  }
  const body = (outcome.body && typeof outcome.body === 'object' ? outcome.body : {}) as Record<string, unknown>
  const keys = parseEligiblePermissions(body.eligiblePermissions)
  const recommended = parseRecommendedConfig(body.recommendedConfig)
  const namePool = parseNamePool(body.namePool)
  const iconChoices = parseIconChoices(body.iconChoices)
  const colorChoices = parseColorChoices(body.colorChoices)
  const nextTierSeq = Number(body.nextTierSeq)
  const seq = Number.isFinite(nextTierSeq) && nextTierSeq > 0 ? Math.trunc(nextTierSeq) : 0
  if (!keys) {
    return {
      ...empty,
      recommended,
      namePool,
      iconChoices,
      colorChoices,
      nextTierSeq: seq,
      status: 'failed',
      error: 'invalid_response'
    }
  }
  return {
    status: 'ready',
    keys,
    baselineKeys: parseBaselinePermissions(body.baselinePermissions, keys),
    recommended,
    namePool,
    iconChoices,
    colorChoices,
    nextTierSeq: seq,
    error: null
  }
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
export function earnableKeys(form: TrustLevelsForm, tierId: string): string[] {
  return form.tiers.find(tier => tier.id === tierId)?.earnable ?? []
}

/**
 * 切换「某档**新增**某键」——纯函数，返回新表单，不改入参。
 *
 * 累积语义由后端 `Earnable.Grants()` 负责（勾 TL2 = TL2 及以上全部解锁），
 * 这里**只**记该档新增，绝不把低档已勾的键复制到高档——复制会造出第二套语义。
 * 停用档与白名单外的键一律拒绝：返回等值副本，不报错（fail-closed）。
 */
export function toggleEarnableKey(
  form: TrustLevelsForm,
  tierId: string,
  key: string,
  whitelist: readonly string[]
): TrustLevelsForm {
  const next = cloneTrustLevelsForm(form)
  const tier = next.tiers.find(entry => entry.id === tierId)
  if (!tier || tier.retired) return next
  if (!whitelist.includes(key)) return next
  const toggled = tier.earnable.includes(key)
    ? tier.earnable.filter(entry => entry !== key)
    : [...tier.earnable, key]
  tier.earnable = sanitizeEarnableKeys(toggled, whitelist)
  return next
}

/** 整表净化：每档的键都保留（`[]` 也保留），只清掉白名单外的键。 */
export function sanitizeEarnableForm(form: TrustLevelsForm, whitelist: readonly string[]): TrustLevelsForm {
  const next = cloneTrustLevelsForm(form)
  for (const tier of next.tiers) tier.earnable = sanitizeEarnableKeys(tier.earnable, whitelist)
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
 * 把档位条目收敛成合法档位：id 缺失/重复的条目丢弃，阈值越界回落 0，
 * `limits` / `earnable` 只做类型净化，`retired` 只认布尔真值。
 */
function normalizeTierList(raw: unknown, fallbackRequirements?: (index: number) => TrustLevelRequirements): TierForm[] {
  if (!Array.isArray(raw)) return []
  const tiers: TierForm[] = []
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue
    const record = entry as Record<string, unknown>
    const id = typeof record.id === 'string' ? record.id.trim().slice(0, 40) : ''
    if (!id || tiers.some(tier => tier.id === id)) continue
    const row = (record.requirements && typeof record.requirements === 'object'
      ? record.requirements
      : {}) as Record<string, unknown>
    const defaults = fallbackRequirements?.(tiers.length) ?? zeroRequirements()
    const requirements = zeroRequirements()
    for (const metric of TRUST_LEVEL_THRESHOLD_METRICS) {
      requirements[metric.suffix] = boundedThreshold(row[metric.suffix], defaults[metric.suffix] ?? 0, metric.max)
    }
    tiers.push({
      id,
      name: normalizeName(record.name),
      icon: normalizeIcon(record.icon),
      // 白名单外的色名落回 null（按位次自动）：否则会把一个后端必拒的值回填进表单。
      color: normalizeTierColor(record.color),
      requirements,
      limits: normalizeLimits(record.limits),
      earnable: normalizeEarnable(record.earnable),
      retired: record.retired === true
    })
  }
  return tiers
}

/**
 * v1 → v2 内存升级：`levels[]` 按 `levelSlug` 对回内置五档顺序。
 *
 * 名字留空、图标留空是**故意**的：升级后的档位走 D-N3 的四层回退链，
 * 于是内置五档继续显示运营熟悉的 i18n 名称，而不是被写进一份前端编造的英文名。
 */
function upgradeLegacyLevels(raw: unknown): TierForm[] {
  if (!Array.isArray(raw)) return []
  const bySlug = new Map<string, Record<string, unknown>>()
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue
    const record = entry as Record<string, unknown>
    if (typeof record.levelSlug === 'string') bySlug.set(record.levelSlug, record)
  }
  const tiers: TierForm[] = []
  TRUST_LEVEL_SLUGS.slice(1).forEach((id, index) => {
    const record = bySlug.get(id)
    if (!record) return
    const row = (record.requirements && typeof record.requirements === 'object'
      ? record.requirements
      : {}) as Record<string, unknown>
    const requirements = zeroRequirements()
    for (const metric of TRUST_LEVEL_THRESHOLD_METRICS) {
      requirements[metric.suffix] = boundedThreshold(row[metric.suffix], 0, metric.max)
    }
    tiers.push({
      id,
      name: { zh: '', en: '' },
      icon: null,
      color: null,
      requirements,
      limits: normalizeLimits(record.limits),
      earnable: normalizeEarnable(record.earnable),
      retired: false
    })
  })
  return tiers
}

/** 每档必须有 6 个 metric 键，缺的补 0（不参与判定）。 */
function normalizeTierForm(tiers: TierForm[]): TierForm[] {
  return tiers.map(tier => ({
    ...tier,
    requirements: Object.fromEntries(
      TRUST_LEVEL_THRESHOLD_METRICS.map(metric => [metric.suffix, tier.requirements[metric.suffix] ?? 0])
    )
  }))
}

/**
 * 把任意来源（option JSON / 表单 / 推荐配置）收敛成合法表单：
 * - 1..8 个 active 档位；不足 1 档或形状非法 → 本地兜底；
 * - 超过 8 档：截断到前 8 个 active 档（保留停用档，避免静默丢配置形变）。
 */
export function normalizeTrustLevelsForm(source: unknown): TrustLevelsForm {
  const record = (source && typeof source === 'object' ? source : {}) as Record<string, unknown>
  const raw = Array.isArray(record.tiers) ? record.tiers : null
  const deletedTierIds = normalizeTombstones(record.deletedTierIds)
  let tiers = normalizeTierList(raw)
  if (tiers.length === 0) return recommendedTrustLevelsForm()
  // active 档数护栏：截断只发生在「超出 8 档」这种前端从不生成的形态上。
  let actives = 0
  tiers = tiers.filter(tier => {
    if (tier.retired) return true
    actives += 1
    return actives <= TRUST_LEVEL_TIER_MAX
  })
  if (activeTiersOf(tiers).length < TRUST_LEVEL_TIER_MIN) return recommendedTrustLevelsForm()
  const next: TrustLevelsForm = { tiers: normalizeTierForm(tiers) }
  if (deletedTierIds.length > 0) next.deletedTierIds = deletedTierIds
  return next
}

/** 解析读面下发的 `recommendedConfig`；非法/缺失 → null（调用方回落本地兜底）。 */
export function parseRecommendedConfig(raw: unknown): TrustLevelsForm | null {
  let parsed: unknown = raw
  if (typeof raw === 'string') {
    try {
      parsed = JSON.parse(raw)
    } catch {
      return null
    }
  }
  const record = (parsed && typeof parsed === 'object' ? parsed : {}) as Record<string, unknown>
  const tiers = normalizeTierList(record.tiers)
  if (tiers.length < TRUST_LEVEL_TIER_MIN) return null
  const deletedTierIds = normalizeTombstones(record.deletedTierIds)
  const next: TrustLevelsForm = { tiers: normalizeTierForm(tiers) }
  if (deletedTierIds.length > 0) next.deletedTierIds = deletedTierIds
  return next
}

/**
 * 从 admin options 读出当前配置。
 * 键缺失、解码失败、`schemaVersion` 未知 → 回落本地兜底（D-N9）——
 * 与后端「空值 = DefaultPolicy」一致，但读面可用时调用方会先给出 `recommendedConfig`。
 */
export function readTrustLevelsConfig(map: Record<string, AdminWebOption>, fallback?: TrustLevelsForm): TrustLevelsForm {
  const base = fallback ? cloneTrustLevelsForm(fallback) : recommendedTrustLevelsForm()
  const option = map[TRUST_LEVELS_OPTION]
  if (!option) return base
  let parsed: unknown
  try {
    parsed = JSON.parse(option.value)
  } catch {
    return base
  }
  if (!parsed || typeof parsed !== 'object') return base
  const config = parsed as { schemaVersion?: unknown, tiers?: unknown, levels?: unknown, deletedTierIds?: unknown }
  const version = Number(config.schemaVersion)
  if (version === TRUST_LEVELS_SCHEMA_VERSION) {
    const tiers = normalizeTierList(config.tiers)
    if (tiers.length === 0) return base
    if (activeTiersOf(tiers).length < TRUST_LEVEL_TIER_MIN) return base
    const deletedTierIds = normalizeTombstones(config.deletedTierIds)
    const next: TrustLevelsForm = { tiers: normalizeTierForm(tiers) }
    if (deletedTierIds.length > 0) next.deletedTierIds = deletedTierIds
    return next
  }
  if (version === TRUST_LEVELS_LEGACY_SCHEMA_VERSION) {
    const tiers = upgradeLegacyLevels(config.levels)
    if (tiers.length === 0) return base
    return { tiers: normalizeTierForm(tiers) }
  }
  return base
}

/**
 * 序列化为 `saveMany` 的载荷：单键 JSON、`schemaVersion: 2`、`tiers` 数组。
 * 形状以 `apps/api/app/Models/TrustLevels/config.go` 为准；那边改字段名时只改本函数。
 *
 * 传了 `whitelist` 就先按白名单净化（白名单外的键不发出去——后端会拒，前端不该发）。
 * 不传 = 原样带回：白名单没读到时我们无法判断哪些键合法，此时**不许**凭空丢弃已存配置。
 *
 * T23：`earnable` **必须每档都出现**且是数组（`[]` = 该档无新增，合法；省略 = 422）。
 * `icon` 始终写出：未选时按 active 位次取默认序列，避免 `null` 触发后端白名单校验。
 * `limits` 原样带回（D-N11：`null` 继承站点默认，`0` 免冷却/不限）。
 */
export function trustLevelsPayload(
  form: TrustLevelsForm,
  whitelist?: readonly string[]
): Array<{ name: string, value: string }> {
  let activeRank = 0
  const tiers = form.tiers.map(tier => {
    if (!tier.retired) activeRank += 1
    const requirements: Record<string, number> = {}
    for (const metric of TRUST_LEVEL_THRESHOLD_METRICS) {
      requirements[metric.suffix] = tier.requirements[metric.suffix] ?? 0
    }
    const stored = tier.earnable ?? []
    const color = normalizeTierColor(tier.color)
    return {
      id: tier.id,
      name: { zh: tier.name.zh, en: tier.name.en },
      icon: tier.icon ?? defaultTierIconName(tier.retired ? activeRank : Math.max(0, activeRank - 1)),
      // `color` 是 omitempty：null = 跟随位次自动，**不能写成空串**（后端按白名单拒）。
      ...(color ? { color } : {}),
      requirements,
      limits: tier.limits ? { ...EMPTY_TIER_LIMITS, ...tier.limits } : null,
      earnable: whitelist ? sanitizeEarnableKeys(stored, whitelist) : stored,
      retired: tier.retired === true
    }
  })
  const payload: Record<string, unknown> = { schemaVersion: TRUST_LEVELS_SCHEMA_VERSION, tiers }
  // 墓碑同理：空数组不落键（omitempty），否则旧读面会看到一份「有字段但没内容」的配置。
  const deletedTierIds = normalizeTombstones(form.deletedTierIds)
  if (deletedTierIds.length > 0) payload.deletedTierIds = deletedTierIds
  return [{
    name: TRUST_LEVELS_OPTION,
    value: JSON.stringify(payload)
  }]
}

/** active 档位身份的序列（顺序也算）：`a\u0000b\u0000c`。 */
function activeIdSequence(form: TrustLevelsForm): string {
  return form.tiers.filter(tier => !tier.retired).map(tier => tier.id).join('\u0000')
}

/**
 * 本次改动是否改变了**档位结构**（D-N1 / 计划 §9）：只要 active 档位的**集合或顺序**变了
 * （新增、停用、恢复、插序、上移下移）就算。
 *
 * 为什么按结构而不是「所有改动」判：档位结构变化会让所有人的等级判定结果重排，必须告知；
 * 只改阈值/名字/图标/能力解锁不会改变「谁落在哪一档」，每次都弹确认只会训练运营闭眼点确定。
 * 纯函数，住在契约层，`bun test` 直接断言，不依赖浏览器。
 */
export function tierStructureChanged(saved: TrustLevelsForm, next: TrustLevelsForm): boolean {
  return activeIdSequence(saved) !== activeIdSequence(next)
}

/**
 * 专用写端点的 body 字段：`{"config": "<v2 JSON 字符串>"}`（`PUT /admin/trust-levels`）。
 *
 * 与 `trustLevelsPayload` 的唯一差别是包装层：那条是通用 `saveMany` 的单键 option，
 * 这条是信任等级自己的写端点。两者共用同一份序列化结果，**不产生第二套形状**。
 */
export function trustLevelsConfig(form: TrustLevelsForm, whitelist?: readonly string[]): string {
  return trustLevelsPayload(form, whitelist)[0]!.value
}

/** 只允许数字键：与 newcomers tab 同口径，避免 `type=number` 收下 `1e5`/`-`/`+`。 */
export function blockNonIntegerKey(event: KeyboardEvent) {
  const allowed = ['Backspace', 'Delete', 'Tab', 'Escape', 'Enter', 'ArrowLeft', 'ArrowRight', 'Home', 'End']
  if (!allowed.includes(event.key) && !event.metaKey && !event.ctrlKey && !/^\d$/.test(event.key)) event.preventDefault()
}

/** 图标短名 → 渲染类名（组件只用这一个入口，别在模板里拼 `i-lucide-`）。 */
export function iconClassOf(icon: string | null | undefined): string {
  return tierIconClass(icon)
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
    state.value = { ...idleTrustLevelWhitelist(), status: 'loading' }
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
