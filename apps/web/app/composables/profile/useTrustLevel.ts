/**
 * 信任等级公开面（M5 + 动态档位 v2）。
 *
 * 数据来源：
 * - **他人视角**：`PublicProfile.trustLevel = { level, levelSlug }`（`Profile/types.go` 的
 *   `PublicTrustLevel`，`omitempty`）。**没有 progress / bottleneck** —— 刻意裁决（D1）。
 * - **本人视角**：`GET /api/v1/trust-levels` 的 `viewer` 字段（等级 + `nextLevel` + `bottleneck`
 *   + 逐条 `conditions`）。
 * - **档位目录**：同一读面的 `tiers`（v2 存储形状）。档位数、名字、图标由运营定义，
 *   所以本文件里的五档常量**只剩兜底用途**（读面不可用时的 TL{rank} + 默认图标）。
 *
 * 字段名纪律（D21）：等级相关 JSON 字段统一 `levelSlug`，禁止裸 `slug`。
 */

import { defaultTierColorName, normalizeTierColor } from '~/utils/profile/trustLevelColors'


/** 内置五档 slug：**只**作为 i18n 回退键与 v1 存储升级的锚点，不再决定档位数。 */
export const TRUST_LEVEL_SLUGS = ['newcomer', 'member', 'resident', 'veteran', 'leader'] as const
export type TrustLevelSlug = typeof TRUST_LEVEL_SLUGS[number]

/** 兜底最高档下标（内置五档 = 4）。真实最高档由目录长度决定，见 `trustLevelLadder`。 */
export const TRUST_LEVEL_MAX = TRUST_LEVEL_SLUGS.length - 1

/**
 * 默认图标序列（按 rank 取，超出循环）。存储里的 `icon` 是**短名**（`"leaf"`，见 v2 契约），
 * 渲染时补 `i-lucide-` 前缀；未选图标的档位走这条序列（D-N4）。
 *
 * **必须与后端 `apps/api/app/Models/TrustLevels/tier_icons.go` 的 `tierIcons` 逐字一致**
 * （顺序也是语义：前 5 项 = TL0..TL4 的默认图标）。改那边就要改这里 ——
 * `tests/admin/forum/trustLevelNameFallback.test.ts` 会直接读那个 Go 文件比对，漂移即红。
 * 这条兜底只在公开目录不可用时生效，所以偏差平时看不见，正是第二份真相最贵的地方。
 */
export const DEFAULT_TIER_ICON_NAMES = [
  'sprout',
  'leaf',
  'home',
  'mountain',
  'crown',
  'star',
  'sun',
  'moon',
  'flame',
  'shield',
  'gem',
  'compass',
  'zap',
  'anchor',
  'medal',
  'trophy'
] as const

/** TL0 的虚拟档：不在 `tiers` 数组内，但展示上永远占据 rank 0。 */
export const TRUST_LEVEL_TL0_ID = 'newcomer'

/** v2 存储的档位形状（与后端 `DisallowUnknownFields` 逐字对齐）。 */
export type TrustLevelTierName = { zh: string, en: string }

export type TrustLevelTier = {
  id: string
  name: TrustLevelTierName
  /** 短名（`"leaf"`）或 null；未选时按 rank 取默认序列。 */
  icon: string | null
  /** 白名单 token（`tier_colors.go`，数量 = 合法 rank 数）；`null`/缺省 = 跟随位次自动（见 `trustLevelColors.ts`）。 */
  color?: string | null
  requirements?: Record<string, number>
  limits?: Record<string, number | null> | null
  earnable?: string[]
  retired?: boolean
  /** 后台读面下发的可选规则树（公开面**没有**这个字段，前端只透传不解码）。 */
  rule?: unknown
}

/**
 * 与 i18n `profile.trustLevel.metric.*` 一一对应；顺序 = Go `TrustLevels.Metrics`。
 * 只有 6 个：`account_age_days` 不是判定指标（逐档账龄 C7 已作废，只保留 `trust.new_user_days` 上限闸）。
 */
export const TRUST_LEVEL_METRIC_KEYS = [
  'daysVisited',
  'topicCount',
  'commentCount',
  'likeReceived',
  'likeGiven',
  'bookmarkCount'
] as const
export type TrustLevelMetricKey = typeof TRUST_LEVEL_METRIC_KEYS[number]

/** Go metric 名（契约里的 snake_case）→ i18n 键后缀。两套命名只在这里对齐。 */
export const TRUST_LEVEL_METRIC_SUFFIX_BY_KEY: Record<TrustLevelMetricKey, string> = {
  daysVisited: 'days_visited',
  topicCount: 'topic_count',
  commentCount: 'comment_count',
  likeReceived: 'like_received_count',
  likeGiven: 'like_given_count',
  bookmarkCount: 'bookmark_count'
}

const METRIC_KEY_BY_SUFFIX = Object.fromEntries(
  Object.entries(TRUST_LEVEL_METRIC_SUFFIX_BY_KEY).map(([key, suffix]) => [suffix, key as TrustLevelMetricKey])
) as Record<string, TrustLevelMetricKey>

export type TrustLevelMetric = {
  key: TrustLevelMetricKey
  current: number
  /** 下一档阈值；`0` = 该指标不参与下一档判定（与后端 LevelPolicy 同语义）。 */
  threshold: number
}

/**
 * 一条「离下一档还差哪几项」的规则条件（`viewer.conditions[]` 的投影）。
 *
 * 句子是**服务端下发**的双语文本：公开面刻意不含条件标识、参数与比较符（见 OpenAPI
 * `TrustLevelConditionProgress`），所以这里没有 key 可拼、也不许前端自己造。
 */
export type TrustLevelCondition = {
  satisfied: boolean
  text: TrustLevelTierName
}

export type TrustLevelView = {
  level: number
  /**
   * 后端下发的档位身份；字段名按 D21 固定为 `levelSlug`。
   *
   * **原样透传，不做枚举校验、不做回退映射**：运营自定义的档位 id（`tier_9`）必须原封不动地
   * 出现在公开面上。本地内置五档数组只在内置回退分支里影响名字与图标，绝不覆盖服务端身份。
   */
  levelSlug: string
  /**
   * 下一档的瓶颈指标。
   * - 他人视角：恒为 `[]`（后端不下发瓶颈，见文件头）；
   * - 本人视角：0 或 1 项，取决于 `viewer.bottleneck` 是否存在。
   * 卡片据此决定是否渲染进度块：空数组 = 不渲染（规范 §1.4-2b）。
   */
  metrics: TrustLevelMetric[]
  /**
   * 下一档 rank；`null` = 已达最高档（阶梯只有 TL0 时同样是 `null`）。
   *
   * 本人优先用 `viewer.nextLevel`（后端只在未满级时下发）；他人视角没有这个字段，
   * 按阶梯邻位推算 —— 弹窗对他人的门槛区块就靠它定位「下一档」。
   */
  nextLevel: number | null
  /**
   * 逐条规则条件。
   * - 他人视角：恒为 `[]`（后端只对本人下发）；
   * - 本人视角：`viewer.conditions` 的原样投影（仅下一档配了扁平 AND 规则时非空）。
   */
  conditions: TrustLevelCondition[]
}

export type TrustLevelProgress = {
  metric: TrustLevelMetric
  /** `aria-valuemax`：阈值，不是 100。 */
  max: number
  /** `aria-valuenow`：clamp 到 `[0, max]`。 */
  now: number
  /** 填充宽度百分比，与 `now` 独立计算（规范 §1.4-7）。 */
  percent: number
  /** `threshold - current`，负数按 0 处理。 */
  remaining: number
}

/** 后端 `PublicProfile.trustLevel` / `viewer` 的最小公共形状。 */
export type PublicTrustLevel = {
  level: number
  levelSlug: string
}

type ViewerTrustLevel = PublicTrustLevel & {
  nextLevel?: number
  bottleneck?: { metric?: string, current?: number, threshold?: number }
  /** 原始 wire 值：`{ satisfied, text: { zh, en } }[]`，解析在 `parseViewerConditions`。 */
  conditions?: unknown
}

type TrustLevelsResponse = {
  levels?: unknown
  tiers?: unknown
  ladder?: unknown
  newUserDays?: number
  viewer?: ViewerTrustLevel | null
}

/* ────────────────────────────────────────────────────────────────────────────
 * 兜底档位表（D-N9）
 *
 * 推荐默认值的**唯一来源**是后台读面的 `recommendedConfig`；本表只在读面不可用时兜底。
 * 两边的等值关系由 `tests/admin/forum/trustLevelRecommended.test.ts` 锚定：
 * 字段漂移会在那条测试里立刻红，而不是等到运营按下「恢复默认」才发现阈值对不上。
 * ──────────────────────────────────────────────────────────────────────────── */

/** Go `DefaultPolicy()` 的 TL1–TL4 判定阈值（与 v1 的 `RECOMMENDED` 同值）。 */
const FALLBACK_REQUIREMENTS: Array<Record<string, number>> = [
  { days_visited: 3, topic_count: 0, comment_count: 5, like_received_count: 0, like_given_count: 0, bookmark_count: 0 },
  { days_visited: 15, topic_count: 2, comment_count: 20, like_received_count: 5, like_given_count: 3, bookmark_count: 0 },
  { days_visited: 45, topic_count: 8, comment_count: 80, like_received_count: 30, like_given_count: 20, bookmark_count: 5 },
  { days_visited: 100, topic_count: 25, comment_count: 250, like_received_count: 100, like_given_count: 80, bookmark_count: 20 }
]

/** 兜底名称：与 i18n `profile.trustLevel.name.*` 同文，供读面不可用时离线展示。 */
const FALLBACK_NAMES: Array<TrustLevelTierName> = [
  { zh: '正式用户', en: 'Member' },
  { zh: '常驻用户', en: 'Resident' },
  { zh: '资深用户', en: 'Veteran' },
  { zh: '社区领袖', en: 'Leader' }
]

/** 兜底档位的图标短名：内置生长序列（芽 → 叶 → 屋 → 山 → 冠）的后四档，与后端前五项一致。 */
const FALLBACK_ICON_NAMES = ['leaf', 'home', 'mountain', 'crown'] as const

/** 读面不可用时的档位兜底（TL1–TL4；TL0 不在数组内，由 `trustLevelLadder` 补）。 */
export const FALLBACK_TIERS: TrustLevelTier[] = TRUST_LEVEL_SLUGS.slice(1).map((id, index) => ({
  id,
  name: { ...FALLBACK_NAMES[index]! },
  icon: FALLBACK_ICON_NAMES[index]!,
  // 兜底也要有颜色：否则目录不可用时阶梯会掉成单色，看起来像「这项功能没做」。
  color: defaultTierColorName(index + 1),
  requirements: { ...FALLBACK_REQUIREMENTS[index]! },
  limits: null,
  earnable: [],
  retired: false
}))

/** TL0 的虚拟档位（rank 0）：名字/图标走内置回退链。 */
export const TL0_TIER: TrustLevelTier = {
  id: TRUST_LEVEL_TL0_ID,
  name: { zh: '', en: '' },
  icon: DEFAULT_TIER_ICON_NAMES[0],
  // TL0 固定 slate：它是「未入门」的位置，不该跟某一档抢注意力。
  color: defaultTierColorName(0),
  retired: false
}

/** 默认图标短名（按 rank 循环取）：rank 0 = sprout。 */
export function defaultTierIconName(rank: number): string {
  const index = Math.max(0, Math.trunc(Number(rank) || 0))
  return DEFAULT_TIER_ICON_NAMES[index % DEFAULT_TIER_ICON_NAMES.length]!
}

/** 短名 / 完整类名 / null → `i-lucide-*` 类名（空串 = 不渲染图标）。 */
export function tierIconClass(icon: string | null | undefined): string {
  const raw = typeof icon === 'string' ? icon.trim() : ''
  if (!raw) return ''
  // 后端允许直接下发完整类名（历史数据/白名单口径不一）时不要拼成 `i-lucide-i-lucide-x`。
  return raw.startsWith('i-') ? raw : `i-lucide-${raw}`
}

/** 档位图标：存储值优先，未选按 rank 取默认序列（D-N4）。 */
export function trustLevelIconOf(tier: TrustLevelTier | null | undefined, rank: number): string {
  return tierIconClass(tier?.icon || defaultTierIconName(rank))
}

/** 内置五档 i18n 回退键：只有 id 命中内置 slug 才成立（D-N3 第三层）。 */
export function builtinTierI18nKey(id: string): string | null {
  return (TRUST_LEVEL_SLUGS as readonly string[]).includes(id) ? `profile.trustLevel.name.${id}` : null
}

/** 存储名（不含 i18n 回退）：命中当前语言优先，另一语言兜次。 */
export function localizedTierName(name: TrustLevelTierName | null | undefined, locale: string): string {
  const zh = (name?.zh ?? '').trim()
  const en = (name?.en ?? '').trim()
  const preferZh = locale.toLowerCase().startsWith('zh')
  return preferZh ? (zh || en) : (en || zh)
}

/** 档位名的存储层回退（不含 i18n）：规则条件的双语句子与档位名共用这一条。 */
export function trustLevelTierName(tier: TrustLevelTier | null | undefined, locale: string): string {
  return tier ? localizedTierName(tier.name, locale) : ''
}

type Translate = (key: string) => string

/**
 * D-N3 四层回退链：`name[locale]` → `name[另一语言]` → 内置五档 i18n 键（id 命中）→ `TL{rank}`。
 *
 * `translate` 缺失时只走第 1、2、4 层：纯函数测试与 SSR 前渲染都可能在 i18n 之外调用，
 * 但第四层是**确定**的兜底，所以永远有值可渲染。
 */
export function trustLevelName(
  tier: TrustLevelTier | null | undefined,
  locale: string,
  rank: number,
  translate?: Translate
): string {
  const stored = trustLevelTierName(tier, locale)
  if (stored) return stored
  const key = builtinTierI18nKey(tier?.id ?? '')
  if (key && translate) {
    const translated = translate(key)
    // vue-i18n 未命中时原样返回键；那比 TL{rank} 更像内容，所以要求两者不同。
    if (translated && translated !== key) return translated
  }
  return `TL${Math.max(0, Math.trunc(Number(rank) || 0))}`
}

/** 目录里的 active 档位（停用 = 软停用：保留配置，判定与 UI 跳过）。 */
export function activeTiers(tiers: readonly TrustLevelTier[] | null | undefined): TrustLevelTier[] {
  return (tiers ?? []).filter(tier => !tier.retired)
}

/**
 * 展示阶梯：rank 0 = 虚拟 TL0，其后是 active 档位（数组顺序 = 展示顺序）。
 * 索引即 rank，所以卡片/矩阵都能用 `ladder[level]` 直接取档，不需要另一套映射。
 */
export function trustLevelLadder(tiers: readonly TrustLevelTier[] | null | undefined): TrustLevelTier[] {
  return [TL0_TIER, ...activeTiers(tiers)]
}

/** 阶梯最高 rank（TL0 单独时返回 0）。 */
export function trustLevelLadderMax(ladder: readonly TrustLevelTier[]): number {
  return Math.max(0, ladder.length - 1)
}

/**
 * 下一档 rank：后端下发的 `nextLevel` 优先（合法 = 严格高于当前档且不越过阶梯顶端），
 * 越界/缺失时按阶梯邻位推算，已到顶端返回 `null`。
 *
 * 为什么还要推算：`viewer` 只对本人且只在读面成功时存在；他人视角完全没有这个字段，
 * 但「下一档门槛」是公开信息（阈值随目录下发），弹窗不能因为少一个字段就整块不渲染。
 */
export function trustLevelNextLevel(level: number, maxLevel: number, viewerNext?: unknown): number | null {
  const current = Math.max(0, Math.trunc(Number(level) || 0))
  const top = Math.max(0, Math.trunc(Number(maxLevel) || 0))
  const candidate = Math.trunc(Number(viewerNext))
  if (Number.isFinite(candidate) && candidate > current && candidate <= top) return candidate
  return current + 1 <= top ? current + 1 : null
}

/**
 * 内置五档的完整阶梯（TL0 + 4 档）：**只**在公开目录不可用时供展示兜底。
 *
 * 卡片/矩阵收到的是最终阶梯（含 TL0），不再自己拼 —— 阶梯怎么组成（哪几档 active、
 * TL0 是合成的）是数据层知识，展示层只负责「收到什么就渲染什么」。
 */
export const FALLBACK_LADDER: TrustLevelTier[] = trustLevelLadder(FALLBACK_TIERS)

/** 越界钳制：目录比存档短时钳到最高档，不返回 undefined。 */
export function ladderTierAt(ladder: readonly TrustLevelTier[], level: number): TrustLevelTier {
  const index = Math.min(trustLevelLadderMax(ladder), Math.max(0, Math.trunc(Number(level) || 0)))
  return ladder[index] ?? TL0_TIER
}

/** 阶梯位置 → 展示名（rank 参与第四层兜底）。 */
export function ladderTierName(
  ladder: readonly TrustLevelTier[],
  level: number,
  locale: string,
  translate?: Translate
): string {
  const index = Math.min(trustLevelLadderMax(ladder), Math.max(0, Math.trunc(Number(level) || 0)))
  return trustLevelName(ladder[index], locale, index, translate)
}

export function trustLevelSlug(level: number): TrustLevelSlug {
  const index = Math.min(TRUST_LEVEL_MAX, Math.max(0, Math.trunc(Number(level) || 0)))
  return TRUST_LEVEL_SLUGS[index] as TrustLevelSlug
}

/**
 * 内置五档图标（兜底）。动态档位走 `trustLevelIconOf`；
 * 保留本函数是因为若干既有调用点（阈值/总览行）在目录加载完成前也要有图标可画。
 */
export function trustLevelIcon(level: number) {
  const index = Math.min(TRUST_LEVEL_MAX, Math.max(0, Math.trunc(Number(level) || 0)))
  return tierIconClass(DEFAULT_TIER_ICON_NAMES[index])
}

/**
 * 瓶颈 metric：排除 `threshold <= 0`（不参与判定）后，取 `current / threshold` 最小者。
 * 全部不参与 → 返回 null（调用方按「没有可推进的档位」处理，不渲染进度块）。
 */
export function trustLevelBottleneck(metrics: TrustLevelMetric[]): TrustLevelMetric | null {
  let bottleneck: TrustLevelMetric | null = null
  let lowestRatio = Number.POSITIVE_INFINITY
  for (const metric of metrics) {
    if (!(metric.threshold > 0)) continue
    const ratio = metric.current / metric.threshold
    if (ratio < lowestRatio) {
      bottleneck = metric
      lowestRatio = ratio
    }
  }
  return bottleneck
}

export function trustLevelProgress(metrics: TrustLevelMetric[]): TrustLevelProgress | null {
  const metric = trustLevelBottleneck(metrics)
  if (!metric) return null
  const threshold = Math.trunc(metric.threshold)
  const current = Math.max(0, Math.trunc(metric.current))
  return {
    metric,
    max: threshold,
    now: Math.min(current, threshold),
    percent: Math.min(100, Math.round((current / threshold) * 100)),
    remaining: Math.max(0, threshold - current)
  }
}

/** `viewer.bottleneck` → 卡片的 `metrics`；未知 metric 名或非法数值一律丢弃。 */
export function trustLevelViewerMetrics(viewer: ViewerTrustLevel | null | undefined): TrustLevelMetric[] {
  const bottleneck = viewer?.bottleneck
  if (!bottleneck || typeof bottleneck.metric !== 'string') return []
  const key = METRIC_KEY_BY_SUFFIX[bottleneck.metric]
  if (!key) return []
  const current = Number(bottleneck.current)
  const threshold = Number(bottleneck.threshold)
  if (!Number.isFinite(current) || !Number.isFinite(threshold) || threshold <= 0) return []
  return [{ key, current, threshold }]
}

/**
 * `viewer.conditions` → 弹窗的规则条件行。
 *
 * 刻意宽容：`satisfied` 必须是布尔、句子至少一侧非空，否则整条丢弃 —— 一条坏条件
 * 不该让「还差哪几项」整块消失，也不该渲染出一个没有句子的空行。
 */
export function parseViewerConditions(raw: unknown): TrustLevelCondition[] {
  if (!Array.isArray(raw)) return []
  const conditions: TrustLevelCondition[] = []
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue
    const item = entry as Record<string, unknown>
    if (typeof item.satisfied !== 'boolean') continue
    const text = readTierName(item.text)
    // 两侧都只有空白 = 没有句子可渲染（与档位名的「空串」同义，别留一个空行）。
    if (!text.zh.trim() && !text.en.trim()) continue
    conditions.push({ satisfied: item.satisfied, text })
  }
  return conditions
}

/**
 * 一档的门槛清单：只保留 `threshold > 0` 的指标（与后端「0 = 该指标不参与判定」同语义），
 * 顺序固定为 `TRUST_LEVEL_METRIC_KEYS`（= Go `Metrics` 顺序），与等级卡的六件套同序。
 *
 * 反查 `METRIC_KEY_BY_SUFFIX` 而不是正向遍历：目录里出现未知 snake_case 键时直接丢弃，
 * 而不是渲染成一个没有 i18n 文案的指标名。
 */
export function trustLevelRequirementRows(
  tier: TrustLevelTier | null | undefined
): Array<{ key: TrustLevelMetricKey, threshold: number }> {
  const requirements = tier?.requirements
  if (!requirements || typeof requirements !== 'object') return []
  const rows: Array<{ key: TrustLevelMetricKey, threshold: number }> = []
  for (const [suffix, rawThreshold] of Object.entries(requirements)) {
    const key = METRIC_KEY_BY_SUFFIX[suffix]
    if (!key) continue
    const threshold = Math.trunc(Number(rawThreshold))
    if (!Number.isFinite(threshold) || threshold <= 0) continue
    rows.push({ key, threshold })
  }
  const order = (key: TrustLevelMetricKey) => TRUST_LEVEL_METRIC_KEYS.indexOf(key)
  return rows.sort((a, b) => order(a.key) - order(b.key))
}

/* ────────────────────────────────────────────────────────────────────────────
 * 档位目录（公开读面）
 *
 * 解析刻意宽容：目录只影响**名字与图标**这两层展示，非法条目丢弃比整块失败更划算
 * （卡片不能因为一个坏档位就不渲染等级）。四个字段都缺失 → `[]` = 走内置兜底。
 * ──────────────────────────────────────────────────────────────────────────── */

function readTierName(raw: unknown): TrustLevelTierName {
  const record = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
  return {
    zh: typeof record.zh === 'string' ? record.zh : '',
    en: typeof record.en === 'string' ? record.en : ''
  }
}

/** 公开读面可能把目录放在 `tiers` / `ladder` / `catalog.tiers`：三处都认，别因位置漂移静默回落。 */
export function parseTrustLevelCatalog(body: unknown): TrustLevelTier[] | null {
  if (!body || typeof body !== 'object') return null
  const record = body as Record<string, unknown>
  const catalog = (record.catalog && typeof record.catalog === 'object' ? record.catalog : {}) as Record<string, unknown>
  // wire 上档位数组仍叫 `levels[]`（改名只发生在后端 `payload.go`），所以三处都认。
  const raw = record.tiers ?? record.levels ?? record.ladder ?? catalog.tiers ?? catalog.levels
  if (!Array.isArray(raw)) return null
  const tiers: Array<{ tier: TrustLevelTier, level: number | null }> = []
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue
    const item = entry as Record<string, unknown>
    // 身份字段：存储形状叫 `id`，公开 wire 叫 `levelSlug`（与 `level` 并存），两个都认。
    const rawId = typeof item.id === 'string' ? item.id : (typeof item.levelSlug === 'string' ? item.levelSlug : '')
    const id = rawId.trim()
    if (!id || tiers.some(entry => entry.tier.id === id)) continue
    const rawLevel = Number(item.level)
    const level = Number.isFinite(rawLevel) && rawLevel >= 0 ? Math.trunc(rawLevel) : null
    // 公开目录的第一条是服务端合成的 TL0（`level: 0` / `editable: false`）：
    // 本地 `trustLevelLadder` 会自己补 TL0，所以这里**必须**跳过，否则阶梯开头会出现两条。
    if (level === 0 || item.editable === false) continue
    tiers.push({
      level,
      tier: {
        id,
        // wire 上叫 `displayName`（后台读面与公开读面同形），存储形状叫 `name`：两个都认。
        name: readTierName(item.displayName ?? item.name),
        icon: typeof item.icon === 'string' && item.icon.trim() ? item.icon.trim() : null,
        // 白名单外的值落回 null（= 按位次自动），合法就原样透传：服务端说了算。
        color: normalizeTierColor(item.color),
        requirements: (item.requirements && typeof item.requirements === 'object'
          ? item.requirements
          : undefined) as Record<string, number> | undefined,
        limits: (item.limits && typeof item.limits === 'object'
          ? item.limits
          : null) as Record<string, number | null> | null,
        earnable: Array.isArray(item.earnable)
          ? item.earnable.filter((key): key is string => typeof key === 'string' && key.trim().length > 0)
          : [],
        retired: item.retired === true
      }
    })
  }
  // wire 同时下发 `level`（rank）时按它排序：数组顺序即判定顺序，但不能依赖对方已经排好。
  const ordered = tiers.every(entry => entry.level !== null)
    ? [...tiers].sort((a, b) => (a.level! - b.level!))
    : tiers
  return ordered.map(entry => entry.tier)
}

/** 目录稳定 key：SSR 直出、客户端复用同一份 payload（`useAsyncData` 的缓存键）。 */
export const TRUST_LEVEL_CATALOG_KEY = 'trust-levels-catalog'
export const TRUST_LEVEL_CATALOG_PATH = '/trust-levels'

/**
 * 资料页等级卡的数据层。
 *
 * - `profile.trustLevel` 决定等级与刻度（他人视角只有这个）；
 * - `isSelf()` 为真时**额外**读 `/trust-levels` 的 `viewer` 拿进度；
 * - 等级目录（名字 / 图标 / 档位数）走 `useAsyncData('trust-levels-catalog')`：
 *   SSR 直出、失败静默回退（TL{rank} + 默认图标），永不阻塞卡片渲染。
 */
export function useTrustLevel(
  profileTrustLevel: Ref<PublicTrustLevel | null | undefined>,
  isSelf: () => boolean
) {
  const { trustLevelsEnabled } = useWebOptions()
  const { request } = useApiClient()

  const viewer = ref<ViewerTrustLevel | null>(null)
  let viewerRequested = false

  async function loadViewer() {
    if (viewerRequested || !import.meta.client) return
    viewerRequested = true
    try {
      const response = await request<TrustLevelsResponse>('/trust-levels')
      viewer.value = response?.viewer ?? null
    } catch {
      // 失败静默：等级卡是增强信息，不在这里留错误条（规范 §1.6）。
      viewer.value = null
    }
  }

  /**
   * 目录与 viewer 共用同一个读面，但**不共用失败语义**：目录失败只回落展示层，
   * 所以 `useAsyncData` 的 handler 自己吞掉异常并返回 null（不让 `error` 冒到页面）。
   */
  const { data: catalogTiers } = useAsyncData<TrustLevelTier[] | null>(
    TRUST_LEVEL_CATALOG_KEY,
    async () => {
      try {
        return parseTrustLevelCatalog(await request<unknown>(TRUST_LEVEL_CATALOG_PATH))
      } catch {
        return null
      }
    },
    { default: () => null, server: true, lazy: false }
  )

  const ladder = computed<TrustLevelTier[]>(() => trustLevelLadder(catalogTiers.value ?? FALLBACK_TIERS))

  watch(
    () => [Boolean(profileTrustLevel.value), isSelf()] as const,
    ([hasLevel, self]) => {
      if (hasLevel && self) void loadViewer()
    },
    { immediate: true }
  )

  const trustLevel = computed<TrustLevelView | null>(() => {
    if (!trustLevelsEnabled.value) return null
    const base = profileTrustLevel.value
    if (!base || !base.levelSlug) return null
    const maxLevel = trustLevelLadderMax(ladder.value)
    const level = Math.min(maxLevel, Math.max(0, Math.trunc(Number(base.level) || 0)))
    // viewer 只对本人有意义，且必须与资料页等级一致（避免切换账号后读到上一个人的进度）。
    const ownViewer = isSelf() && viewer.value && Math.trunc(Number(viewer.value.level) || 0) === level
      ? viewer.value
      : null
    return {
      level,
      // 服务端说什么就是什么：这里曾经用本地五元素数组按 rank 重算 slug，那会把运营自定义的
      // 档位 id 静默改写回内置 slug —— 公开面第一个消费者就断了，还不报错。
      levelSlug: base.levelSlug,
      metrics: trustLevelViewerMetrics(ownViewer),
      // 他人视角没有 nextLevel/conditions（后端只对本人下发），两者都退到「阶梯邻位 + 空条件」。
      nextLevel: trustLevelNextLevel(level, maxLevel, ownViewer?.nextLevel),
      conditions: ownViewer ? parseViewerConditions(ownViewer.conditions) : []
    }
  })

  return { trustLevel, ladder }
}
