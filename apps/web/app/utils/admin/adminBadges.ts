import type { BadgeTier } from '~/utils/forum/forumBadge'

/** 授予方式：自动按统计阈值，手动由管理员授予。 */
export type BadgeGrantMode = 'automatic' | 'manual'

/** 后台列表行：徽章定义 + 已授予人数（后端聚合）。 */
export interface AdminBadgeRow {
  id: number
  slug: string
  name: string
  description: string
  icon: string
  tier: BadgeTier
  groupId: number | null
  groupName: string | null
  grantMode: BadgeGrantMode
  metric: string | null
  threshold: number | null
  grantedCount: number
  enabled: boolean
  position: number
}

/**
 * 分组创建/更新请求体：字段名与后端 `badgeGroupBody`（Badges/controller.go）逐字一致。
 * PATCH 是 merge 语义，四个字段都可选提交。
 */
export type AdminBadgeGroupInput = {
  key: string
  name: string
  description: string
  position: number
}

/**
 * 后台授予记录行：字段名与后端 `BadgeGrantRecord`（Models/Badges/types.go）json tag 逐字一致。
 * 该类型由 controller 直接透传（不经过 dto.go 的 admin 映射），改字段名前先读 types.go。
 */
export interface AdminBadgeGrantRow {
  userId: number
  username: string
  displayName: string
  badgeId: number
  badgeSlug: string
  grantedAt: string
  grantedBy: number | null
  grantReason: string
}

/** 授予来源三态，判别式与 knowledge/modules/badges.md「三来源判别式」一致。 */
export type AdminBadgeGrantSource = 'manual' | 'recompute' | 'automatic'

/**
 * 来源判别式：`grantedBy` 非空 = 管理员手动；`grantReason === 'recompute'` = 批量重算补授；
 * 其余（grantedBy 为空 + 无理由）= 规则自动授予。
 */
export function grantSourceOf(row: Pick<AdminBadgeGrantRow, 'grantedBy' | 'grantReason'>): AdminBadgeGrantSource {
  if (row.grantedBy !== null && row.grantedBy !== undefined) return 'manual'
  if (row.grantReason === 'recompute') return 'recompute'
  return 'automatic'
}

/** 只有手动授予行才允许收回：自动/重算来源收回会 422 forum.badge_manual_only。 */
export function grantRevocable(row: AdminBadgeGrantRow): boolean {
  return grantSourceOf(row) === 'manual'
}

/**
 * 规则列口径：手动徽章、或规则字段不全时返回 null，由调用方渲染占位符。
 * 不在这里编造 `metric ≥ 0` 这类看起来有、实际不存在的规则。
 */
export function adminBadgeRule(
  badge: Pick<AdminBadgeRow, 'grantMode' | 'metric' | 'threshold'>
): { metric: string, threshold: number } | null {
  if (badge.grantMode !== 'automatic' || !badge.metric || !badge.threshold) {
    return null
  }
  return { metric: badge.metric, threshold: badge.threshold }
}

/**
 * 规则列的指标文案。
 *
 * 直接把 `rule.metric` 塞进插值会渲染出 `topic_count 达到 5 时授予` —— 后端枚举值不是人话。
 * 优先用 `admin.badges.metrics.<slug>`；缺键才回落原始 slug（保留信息，不静默变成空字符串）。
 * 做成独立函数是为了能被断言：组件里内联三元表达式只能靠读代码相信它。
 */
export function adminBadgeMetricLabel(
  metric: string | undefined,
  translate: (key: string) => string,
  has: (key: string) => boolean
): string {
  if (!metric) return ''
  const key = `admin.badges.metrics.${metric}`
  return has(key) ? translate(key) : metric
}

/** 后台可选图标白名单（规范 §4.4）：只允许 Tabler / Lucide，禁止 emoji 与手写 SVG。 */
export const BADGE_ICON_CHOICES: readonly string[] = [
  'i-lucide-medal',
  'i-lucide-award',
  'i-lucide-crown',
  'i-lucide-trophy',
  'i-lucide-star',
  'i-lucide-sparkles',
  'i-lucide-flame',
  'i-lucide-heart',
  'i-lucide-zap',
  'i-lucide-book-open',
  'i-lucide-message-square',
  'i-lucide-thumbs-up',
  'i-lucide-calendar-check',
  'i-lucide-gem',
  'i-lucide-target'
]

/** 自动徽章可用的统计指标；顺序与后端 ActivityMetric 一致。 */
export const BADGE_METRIC_OPTIONS: readonly string[] = [
  'topic_count',
  'comment_count',
  'like_received_count',
  'bookmark_count',
  'days_visited'
]

/** 编辑表单 → 请求体：手动徽章不带 metric/threshold，避免后端按自动规则校验。 */
export function toAdminBadgeInput(form: {
  slug: string
  name: string
  description: string
  groupId: number | null
  tier: string
  icon: string
  grantMode: string
  metric: string | null
  threshold: number | null
  enabled: boolean
  position: number
}) {
  const automatic = form.grantMode === 'automatic'
  return {
    slug: form.slug.trim(),
    name: form.name.trim(),
    description: form.description.trim(),
    groupId: form.groupId,
    tier: form.tier,
    icon: form.icon,
    grantMode: form.grantMode,
    metric: automatic ? form.metric : null,
    threshold: automatic ? form.threshold : null,
    enabled: form.enabled,
    position: form.position
  }
}

/** 徽章 tab 的筛选状态；空串表示「不过滤」。字段名与 URL query 键一致。 */
export interface AdminBadgeFilters {
  q: string
  /** 分组 id 的字符串形式；'' = 全部分组，'none' = 未分组。 */
  group: string
  tier: string
  grantMode: string
  /** 'true' | 'false' | ''（全部）。 */
  enabled: string
}

export const EMPTY_BADGE_FILTERS: AdminBadgeFilters = {
  q: '',
  group: '',
  tier: '',
  grantMode: '',
  enabled: ''
}

/** 筛选是否真的在起作用：区分「列表本来就空」与「筛选把结果筛空了」。 */
export function badgeFiltersActive(filters: AdminBadgeFilters): boolean {
  return filters.q.trim() !== '' || filters.group !== '' || filters.tier !== ''
    || filters.grantMode !== '' || filters.enabled !== ''
}

/**
 * 等级位次表 → 排序用的 `Record<key, position>`。
 *
 * 等级是运营可增删的数据，前端不许再写一份三档权重：位次只来自 `GET /admin/badges/tiers`
 * 的 `position`（升序 = 后台与公开面一致的展示顺序）。
 */
export function tierOrderMap(tiers: ReadonlyArray<{ key: string, position: number }>): Record<string, number> {
  const order: Record<string, number> = {}
  for (const tier of tiers) order[tier.key] = Number(tier.position)
  return order
}

/** 按筛选条件过滤徽章列表；q 同时匹配名称、slug 与说明（大小写不敏感）。 */
export function filterBadges(badges: AdminBadgeRow[], filters: AdminBadgeFilters): AdminBadgeRow[] {
  const q = filters.q.trim().toLowerCase()
  return badges.filter((badge) => {
    if (q && !`${badge.name} ${badge.slug} ${badge.description}`.toLowerCase().includes(q)) return false
    if (filters.group === 'none' && badge.groupId !== null) return false
    if (filters.group !== '' && filters.group !== 'none' && String(badge.groupId) !== filters.group) return false
    if (filters.tier !== '' && badge.tier !== filters.tier) return false
    if (filters.grantMode !== '' && badge.grantMode !== filters.grantMode) return false
    if (filters.enabled === 'true' && !badge.enabled) return false
    if (filters.enabled === 'false' && badge.enabled) return false
    return true
  })
}

export type AdminBadgeSortKey = 'name' | 'tier' | 'grantedCount'
export type AdminBadgeSortDir = 'asc' | 'desc'

/** 可排序列集合：表格列头的排序按钮与 URL query 白名单共用这份枚举。 */
export const BADGE_SORT_KEYS: readonly AdminBadgeSortKey[] = ['name', 'tier', 'grantedCount']

/**
 * 排序为稳定排序（同键保持后端原有次序）：后端列表已按分组 position → 徽章 position
 * 排好，前端排序只在其上重排，不越权重排分组。
 */
export function sortBadges(
  badges: AdminBadgeRow[],
  key: AdminBadgeSortKey | '',
  dir: AdminBadgeSortDir,
  tierOrder: Record<string, number> = {}
): AdminBadgeRow[] {
  if (!key) return badges
  const factor = dir === 'desc' ? -1 : 1
  // 未知等级（等级被删但行还没刷新）排在所有已知等级之后，不丢行、不猜位次。
  const tierWeight = (tier: string) => tierOrder[tier] ?? Number.MAX_SAFE_INTEGER
  return [...badges].sort((left, right) => {
    if (key === 'tier') return (tierWeight(left.tier) - tierWeight(right.tier)) * factor
    if (key === 'grantedCount') return (left.grantedCount - right.grantedCount) * factor
    return left.name.localeCompare(right.name) * factor
  })
}

/** 分组表单校验：错误码由 tab 组件翻译成文案。 */
export function validateBadgeGroupForm(form: Pick<AdminBadgeGroupInput, 'key' | 'name'>): {
  key?: 'required', name?: 'required'
} {
  const errors: { key?: 'required', name?: 'required' } = {}
  if (!form.key.trim()) errors.key = 'required'
  if (!form.name.trim()) errors.name = 'required'
  return errors
}
