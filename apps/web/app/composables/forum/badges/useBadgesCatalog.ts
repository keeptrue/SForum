import {
  forumBadgeTier,
  type ForumBadgeRequirementText,
  type ForumBadgeTierName,
  type ProfileBadgeEntry,
  type ProfileBadgeGroup
} from '~/utils/forum/forumBadge'

/**
 * `GET /api/v1/badges` 的 wire 形状（backend `Badges/dto.go` 的 publicBadgeCatalog*）。
 *
 * 映射口径（M4 修订，原 D37「公开面不给阈值」的结论已被本轮决策改写）：
 * - 进度**由规则引擎派生**，所以 wire 在「未获得 + 可测」的自动徽章上会下发
 *   `current` / `threshold`（`omitempty`：缺键 = 不可测 / 非自动 / 已获得，映射层不得补 0）。
 *   阈值本来就已经随 `requirementText` 公开（「发布 10 个主题」），再藏这两个数字只是自欺。
 * - `grantedAt` / `metric` / `enabled` 等授予与运营字段仍然**不存在**：已获得卡只写「已获得」。
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
  /** 进度（规则引擎派生）：只在「未获得 + 可测」时出现，缺键即无进度。 */
  current?: number | null
  threshold?: number | null
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
function wireCount(value: unknown): number | undefined {
  if (value === null || value === undefined || value === '') return undefined
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : undefined
}

/**
 * 进度字段只在**服务端真的给了可用阈值**时出现：否则连键都不留。
 * `threshold: 0` 与 `threshold` 缺键等价（卡片只按 `threshold > 0` 决定画不画进度条），
 * 但映射层先收口一次，避免把 0 当成「有进度」传给卡片。
 */
function badgeProgressFields(badge: PublicBadgeCatalogItem): { current?: number, threshold?: number } {
  const threshold = wireCount(badge.threshold)
  if (threshold === undefined || threshold <= 0) return {}
  const current = wireCount(badge.current)
  return { current: current === undefined ? 0 : current, threshold }
}

/** wire → ProfileBadgeGroup 的纯映射（取数在 useBadgesCatalog，这里不发请求）。 */
export function mapPublicBadgeCatalog(
  response: PublicBadgeCatalogResponse | null | undefined,
  ungroupedLabel: string
): ProfileBadgeGroup[] {
  return (response?.groups ?? []).map((group, groupIndex) => ({
    key: group.key,
    // 空名字表示未分组；文案由 i18n 决定，领域层不硬编码「未分组」。
    name: group.name || ungroupedLabel,
    position: groupIndex,
    badges: group.badges.map((badge, badgeIndex) => {
      // 说明是可选文案：空串/缺键都不留 `description: ''`，否则详情弹窗会画出空段落。
      const description = String(badge.description ?? '').trim()
      return {
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
        ...(description ? { description } : {}),
        // 缺键即无进度：键在、值为 undefined 也会让卡片误判「有 threshold」，所以用展开补。
        ...badgeProgressFields(badge),
        // 原样透传：句子与逐语言回落都在服务端完成，映射层不补字、不改写。
        requirementText: badge.requirementText ?? null
      } satisfies ProfileBadgeEntry
    })
  }))
}

export async function useBadgesCatalog() {
  const { request } = useApiClient()
  const { t } = useI18n()

  const { data, pending, error, refresh } = await useAsyncData(
    'forum-badges-catalog',
    () => request<PublicBadgeCatalogResponse>('/badges'),
    { default: () => ({ groups: [] as PublicBadgeCatalogGroup[], truncated: false }) }
  )

  const groups = computed<ProfileBadgeGroup[]>(() =>
    mapPublicBadgeCatalog(data.value, t('badges.catalog.ungrouped'))
  )

  const truncated = computed(() => Boolean(data.value?.truncated))

  return { groups, truncated, pending, error, refresh }
}
