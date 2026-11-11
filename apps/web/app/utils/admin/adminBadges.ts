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
