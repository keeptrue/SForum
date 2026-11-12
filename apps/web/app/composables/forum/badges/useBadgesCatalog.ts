import { forumBadgeTier, type ForumBadgeRequirementText, type ForumBadgeTierName, type ProfileBadgeGroup } from '~/utils/forum/forumBadge'

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
  /** 等级显示名（中英双份）与色值、位次：等级可增删，前端不许再靠 tier key 猜文案。 */
  tierName?: ForumBadgeTierName | null
  tierColor?: string | null
  tierPosition?: number | null
  group: string
  earned: boolean
  /** 「获得条件」双语句子（服务端已逐语言回落）；前端只选语言，不重写文案。 */
  requirementText?: ForumBadgeRequirementText | null
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
      // 原样保留等级 key（不再折叠成三档），文案与色由载荷字段决定。
      tier: forumBadgeTier(badge.tier),
      tierName: badge.tierName ?? null,
      tierColor: badge.tierColor ?? null,
      tierPosition: badge.tierPosition ?? null,
      icon: badge.icon,
      position: badgeIndex,
      earned: badge.earned,
      // 原样透传：句子与逐语言回落都在服务端完成，映射层不补字、不改写。
      requirementText: badge.requirementText ?? null
    }))
  })))

  const truncated = computed(() => Boolean(data.value?.truncated))

  return { groups, truncated, pending, error, refresh }
}
