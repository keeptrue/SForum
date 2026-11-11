import { forumBadgeTier, type ProfileBadgeGroup } from '~/utils/forum/forumBadge'

/**
 * `GET /api/v1/badges` 的 wire 形状（backend `Badges/dto.go` 的 publicBadgeCatalog*）。
 *
 * 三条硬口径，映射时不许「补」字段：
 * - wire 上**不存在** `threshold` / `metric` / `current`（D37）：目录页不显示阈值、进度条或「还差 N 个」。
 * - 也**不存在** `grantedAt`：所以 catalog 变体的已获得卡只写「已获得」，不写「获得于 {date}」。
 * - 顺序即 `position`：后端已按 `badge_groups.position, badges.position, id` 排好，前端只保序，不重排。
 */
type PublicBadgeCatalogItem = {
  slug: string
  name: string
  description: string
  icon: string
  tier: string
  group: string
  earned: boolean
}

type PublicBadgeCatalogGroup = {
  key: string
  name: string
  badges: PublicBadgeCatalogItem[]
}

type PublicBadgeCatalogResponse = {
  groups: PublicBadgeCatalogGroup[]
  truncated: boolean
}

/**
 * `/badges` 公共目录的数据层。
 *
 * 匿名可见、`earned` 是查看者相关字段：后端用 `private, no-store` 承载（D37），
 * 前端不参与任何缓存头逻辑，也不把响应塞进共享缓存。
 */
export async function useBadgesCatalog() {
  const { request } = useApiClient()
  const { t } = useI18n()

  const { data, pending, error, refresh } = await useAsyncData(
    'forum-badges-catalog',
    () => request<PublicBadgeCatalogResponse>('/badges'),
    { default: () => ({ groups: [] as PublicBadgeCatalogGroup[], truncated: false }) }
  )

  const groups = computed<ProfileBadgeGroup[]>(() => (data.value?.groups ?? []).map((group, groupIndex) => ({
    key: group.key,
    // 空名字表示未分组；文案由 i18n 决定，领域层不硬编码「未分组」。
    name: group.name || t('badges.catalog.ungrouped'),
    position: groupIndex,
    badges: group.badges.map((badge, badgeIndex) => ({
      slug: badge.slug,
      name: badge.name,
      // wire 上的 tier 是字符串，卡片只认 gold/silver/bronze 三档，交给既有归一化函数。
      tier: forumBadgeTier(badge.tier),
      icon: badge.icon,
      position: badgeIndex,
      earned: badge.earned
    }))
  })))

  const truncated = computed(() => Boolean(data.value?.truncated))

  return { groups, truncated, pending, error, refresh }
}
