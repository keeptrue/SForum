import { forumUserProfilePath } from '~/utils/forum/forumTaxonomy'

/**
 * 徽章的读模型口径：`BadgeRef` 只带展示需要的四个字段。
 *
 * tier 在后端是 `TEXT + CHECK`，前端仍然收窄一次，未知值按 bronze 渲染，
 * 避免徽章文案缺键或样式变体落空。
 */
export type BadgeTier = 'bronze' | 'silver' | 'gold'

export interface ForumBadgeRef {
  slug: string
  name: string
  tier: BadgeTier
  icon: string
}

/** tier 展示顺序：与后端视图一致，gold 在前。 */
export const BADGE_TIERS: readonly BadgeTier[] = ['gold', 'silver', 'bronze']

export function forumBadgeTier(tier?: string | null): BadgeTier {
  return tier === 'gold' || tier === 'silver' ? tier : 'bronze'
}

/** 排序权重（越大越靠前），供资料页徽章 tab 与后台列表共用。 */
export function forumBadgeTierWeight(tier?: string | null): number {
  return BADGE_TIERS.length - BADGE_TIERS.indexOf(forumBadgeTier(tier))
}

/** 资料页徽章 tab 的地址；chip 跳转与 tab 写回 query 共用同一口径。 */
export function forumUserBadgesPath(username?: string | null): string | null {
  const handle = username?.trim()
  return handle ? `${forumUserProfilePath(handle)}?tab=badges` : null
}

/**
 * 资料页徽章 tab 的单条（M3 契约）。
 *
 * 后端按「没有这个键」表达缺省，不是给 0 / null：
 * - `grantedAt` 只在 earned=true 时出现
 * - `current` / `threshold` 只在「未获得的自动徽章」上出现；手动徽章两者都缺
 * 所以三个字段都可选，组件按 `threshold > 0` 决定要不要画进度条。
 */
export interface ProfileBadgeEntry extends ForumBadgeRef {
  position: number
  earned: boolean
  current?: number
  threshold?: number
  grantedAt?: string
}

export interface ProfileBadgeGroup {
  key: string
  name: string
  position: number
  badges: ProfileBadgeEntry[]
}

/** tier 的兜底图标：后台 tier 列与 tier 选择卡用，避免只靠颜色表达级别。 */
export function forumBadgeTierIcon(tier?: string | null): string {
  const value = forumBadgeTier(tier)
  if (value === 'gold') {
    return 'i-lucide-crown'
  }
  return value === 'silver' ? 'i-lucide-medal' : 'i-lucide-award'
}

/** 只有「未获得的自动徽章」才带进度：threshold 缺失或为 0 时整条进度都不画。 */
export function forumBadgeHasProgress(badge: Pick<ProfileBadgeEntry, 'earned' | 'threshold'>): boolean {
  return !badge.earned && Number(badge.threshold) > 0
}

/** 进度百分比：缺字段或 threshold<=0 时返回 0，不产生 NaN 宽度。 */
export function forumBadgeProgressPercent(current?: number, threshold?: number): number {
  if (!Number.isFinite(threshold) || (threshold as number) <= 0) {
    return 0
  }
  const safeCurrent = Number.isFinite(current) ? (current as number) : 0
  return Math.round(Math.min(1, Math.max(0, safeCurrent / (threshold as number))) * 100)
}

/** 组内排序：tier 降序（gold → bronze），同级按 position 升序。 */
export function sortProfileBadgeEntries(entries: ProfileBadgeEntry[]): ProfileBadgeEntry[] {
  return [...entries].sort((a, b) => {
    const weight = forumBadgeTierWeight(b.tier) - forumBadgeTierWeight(a.tier)
    return weight !== 0 ? weight : a.position - b.position
  })
}

/** 分组排序：按 position 升序；后端已排序时这一步是幂等的。 */
export function sortProfileBadgeGroups(groups: ProfileBadgeGroup[]): ProfileBadgeGroup[] {
  return [...groups].sort((a, b) => a.position - b.position)
}
