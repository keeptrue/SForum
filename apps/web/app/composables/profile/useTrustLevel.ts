/**
 * 信任等级公开面（M5）。
 *
 * 数据来源（backend 已落地，契约见 `apps/api/app/Http/Controllers/TrustLevels/dto.go`）：
 * - **他人视角**：`PublicProfile.trustLevel = { level, levelSlug }`（`Profile/types.go` 的
 *   `PublicTrustLevel`，`omitempty`）。**没有 progress / bottleneck** —— 刻意裁决：瓶颈可能落在
 *   `like_given_count` / `bookmark_count`，而这两个计数不公开（D1）。资料页公开的只有获赞与访问天数。
 * - **本人视角**：`GET /api/v1/trust-levels` 的 `viewer` 字段（等级 + `nextLevel` + `bottleneck`）。
 *   匿名时该字段不存在；`bottleneck.metric` 是 Go metric 名（snake_case）。
 *
 * 字段名纪律（D21）：等级相关 JSON 字段统一 `levelSlug`，禁止裸 `slug`。
 */

export const TRUST_LEVEL_SLUGS = ['newcomer', 'member', 'resident', 'veteran', 'leader'] as const
export type TrustLevelSlug = typeof TRUST_LEVEL_SLUGS[number]

/** 最高档下标；同时是「没有下一档」的判据。 */
export const TRUST_LEVEL_MAX = TRUST_LEVEL_SLUGS.length - 1

/** 档位图标：芽 → 叶 → 屋 → 山 → 冠，一条生长/攀升序列（规范 §2.1 白名单）。 */
const TRUST_LEVEL_ICONS = [
  'i-lucide-sprout',
  'i-lucide-leaf',
  'i-lucide-house',
  'i-lucide-mountain-snow',
  'i-lucide-crown'
] as const

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

export type TrustLevelView = {
  level: number
  /** 后端下发的档位 slug；字段名按 D21 固定为 `levelSlug`。 */
  levelSlug: TrustLevelSlug
  /**
   * 下一档的瓶颈指标。
   * - 他人视角：恒为 `[]`（后端不下发瓶颈，见文件头）；
   * - 本人视角：0 或 1 项，取决于 `viewer.bottleneck` 是否存在。
   * 卡片据此决定是否渲染进度块：空数组 = 不渲染（规范 §1.4-2b）。
   */
  metrics: TrustLevelMetric[]
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
}

type TrustLevelsResponse = {
  levels?: unknown
  newUserDays?: number
  viewer?: ViewerTrustLevel | null
}

export function trustLevelSlug(level: number): TrustLevelSlug {
  const index = Math.min(TRUST_LEVEL_MAX, Math.max(0, Math.trunc(Number(level) || 0)))
  return TRUST_LEVEL_SLUGS[index] as TrustLevelSlug
}

export function trustLevelIcon(level: number) {
  const index = Math.min(TRUST_LEVEL_MAX, Math.max(0, Math.trunc(Number(level) || 0)))
  return TRUST_LEVEL_ICONS[index] as string
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
 * 资料页等级卡的数据层。
 *
 * - `profile.trustLevel` 决定等级与刻度（他人视角只有这个）；
 * - `isSelf()` 为真时**额外**读 `GET /trust-levels` 的 `viewer` 拿进度。客户端取：
 *   进度是增强信息，服务端渲染先给等级与刻度，避免为它阻塞资料页首屏。
 * - 开关关闭、未解析、请求失败一律回落「无进度」而不是不渲染卡片（卡片本身由 profile 决定）。
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
    const level = Math.min(TRUST_LEVEL_MAX, Math.max(0, Math.trunc(Number(base.level) || 0)))
    // viewer 只对本人有意义，且必须与资料页等级一致（避免切换账号后读到上一个人的进度）。
    const ownViewer = isSelf() && viewer.value && Math.trunc(Number(viewer.value.level) || 0) === level
      ? viewer.value
      : null
    return {
      level,
      levelSlug: trustLevelSlug(level),
      metrics: trustLevelViewerMetrics(ownViewer)
    }
  })

  return { trustLevel }
}
