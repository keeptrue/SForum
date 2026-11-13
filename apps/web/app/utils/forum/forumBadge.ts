import { forumUserProfilePath } from '~/utils/forum/forumTaxonomy'
import { tierAccentVars } from '~/utils/theme/tierColor'

/**
 * 徽章的读模型口径。
 *
 * tier 在服务端是**运营可增删的等级 key**（TEXT），不再是固定三档：前端不再把未知值折叠成
 * bronze —— 那会让新建等级的所有徽章都显示成「铜牌」。等级的中英名、色值、位次随载荷下发
 * （`tierName` / `tierColor` / `tierPosition`）。
 *
 * **缺外观 = 中性，绝不猜等级色。** 载荷没给 `tierColor` 时一律落中性 `slate`，不再按 tier key
 * 反查内置色表 —— 猜出来的色会和运营在后台配的色打架，而且看起来像真的。
 * 等级名同理：只在 locale 里有对应键时才用键，否则落到通用文案（见 `forumBadgeTierLabel`）。
 *
 * `data-tier` 与 URL `?tier=` 仍然是 key 字符串，读侧按已加载的等级白名单归一。
 */
export type BadgeTier = string

/** 载荷里的等级名（中英双份，按当前 locale 取一个）。 */
export interface ForumBadgeTierName {
  zh: string
  en: string
}

export interface ForumBadgeRef {
  slug: string
  name: string
  tier: BadgeTier
  icon: string
  /** 等级显示名（服务端 `tierName`）；缺失时回退内置三档文案。 */
  tierName?: ForumBadgeTierName | null
  /** 等级色：token 名或 `#rrggbb`（服务端 `tierColor`）；缺失时落中性 `slate`。 */
  tierColor?: string | null
  /** 等级位次（服务端 `tierPosition`，升序 = 展示顺序）；缺失时排在已知等级之后。 */
  tierPosition?: number | null
}

/** 未知等级的兜底文案键（绝不把 `badges.tier.<key>` 裸键渲染出去）。 */
export const BADGE_TIER_FALLBACK_KEY = 'badges.tier.bronze'

/**
 * 归一化：只去空白，不做三档折叠。
 * 空值仍落到 bronze —— 那是「服务端没给 tier」的旧数据，不是「新等级」。
 */
export function forumBadgeTier(tier?: string | null): BadgeTier {
  const value = typeof tier === 'string' ? tier.trim() : ''
  return value || 'bronze'
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
  /** 徽章自身的说明（服务端 `description`）；缺键表示运营没写，详情弹窗整段不渲染。 */
  description?: string
  /** 「获得条件」双语句子（服务端 `requirementText`）；空句表示该徽章没有可展示的条件文案。 */
  requirementText?: ForumBadgeRequirementText | null
  current?: number
  threshold?: number
  grantedAt?: string
}

/** 载荷里的「获得条件」句子（中英双份，按当前 locale 取一个）。 */
export interface ForumBadgeRequirementText {
  zh: string
  en: string
}

export interface ProfileBadgeGroup {
  key: string
  name: string
  position: number
  badges: ProfileBadgeEntry[]
}

/** 等级图标：只做内置三档兜底，可配置等级的图标由服务端下发。 */
export function forumBadgeTierIcon(tier?: string | null): string {
  const value = forumBadgeTier(tier)
  if (value === 'gold') {
    return 'i-lucide-crown'
  }
  return value === 'silver' ? 'i-lucide-medal' : 'i-lucide-award'
}

/** 载荷 `tierName` 里挑当前语言的一份；两边都空视为「没给」。 */
export function pickTierName(name?: ForumBadgeTierName | null, locale = 'zh-CN'): string {
  if (!name) return ''
  const preferred = locale.toLowerCase().startsWith('zh') ? name.zh : name.en
  const fallback = locale.toLowerCase().startsWith('zh') ? name.en : name.zh
  return (preferred || fallback || '').trim()
}

/**
 * 公开面「获得条件」句子的唯一解析入口。
 *
 * **句子由服务端生成，前端只选语言。** 服务端已按规则 AST 渲染成人话（含阈值、指标名）
 * 并逐语言回落，所以这里只做「zh 前缀取 zh、否则取 en，选中那种语言为空则回落到另一种，
 * 两者都空返回空串」。
 *
 * 公开面**禁止**调用 `describeRule`（`utils/admin/badgeRule.ts`）：那是后台影响确认用的
 * 自渲染器，公开页复用它就会多出第二套口径，还会把 `cmp` / `params` / `op` 这些规则内部
 * 结构漏到页面上。
 */
export function forumBadgeRequirementText(
  entry: { requirementText?: ForumBadgeRequirementText | null },
  locale = 'zh-CN'
): string {
  const text = entry?.requirementText
  if (!text) return ''
  const zhPreferred = locale.toLowerCase().startsWith('zh')
  const preferred = zhPreferred ? text.zh : text.en
  const fallback = zhPreferred ? text.en : text.zh
  return (preferred || fallback || '').trim()
}

/**
 * 等级的显示名，三级回退：载荷 `tierName` → locale 里的 `badges.tier.<key>` → 内置三档文案。
 *
 * 第三级不是为了「好看」：新建等级的 key 在旧 locale 里没有文案，直接渲染会露出
 * `badges.tier.legendary` 这种裸键。宁可退成通用文案，也不能把键名当产品文案。
 */
export function forumBadgeTierLabel(
  badge: Pick<ForumBadgeRef, 'tier' | 'tierName'>,
  locale: string,
  translate: (key: string) => string,
  has: (key: string) => boolean
): string {
  const fromPayload = pickTierName(badge.tierName, locale)
  if (fromPayload) return fromPayload
  const key = `badges.tier.${forumBadgeTier(badge.tier)}`
  if (has(key)) return translate(key)
  return translate(BADGE_TIER_FALLBACK_KEY)
}

/** 等级的色值：只认载荷 `tierColor`；缺外观一律中性 `slate`，不按 tier key 猜色。 */
export function forumBadgeTierColor(badge: Pick<ForumBadgeRef, 'tier' | 'tierColor'>): string {
  const payload = typeof badge.tierColor === 'string' ? badge.tierColor.trim() : ''
  if (payload) return payload
  return 'slate'
}

/**
 * 公开面统一色变量：hue = 等级色（明暗两套），填充/环线由组件用 `color-mix` 就地混。
 * 卡片与 chip 共用这一条，避免两处各写一套比例。
 */
export function forumBadgeTierStyle(badge: Pick<ForumBadgeRef, 'tier' | 'tierColor'>): Record<string, string> {
  return tierAccentVars(forumBadgeTierColor(badge))
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

/** 未知位次排在所有已知等级之后（升序 = 展示顺序，最高档在前）。 */
const UNKNOWN_TIER_POSITION = Number.MAX_SAFE_INTEGER

function tierPositionOf(entry: Pick<ProfileBadgeEntry, 'tierPosition'>): number {
  const value = Number(entry.tierPosition)
  return Number.isFinite(value) ? value : UNKNOWN_TIER_POSITION
}

/** 组内排序：等级位次升序（服务端顺序 = 高等级在前），同级按 position 升序。 */
export function sortProfileBadgeEntries(entries: ProfileBadgeEntry[]): ProfileBadgeEntry[] {
  return [...entries].sort((a, b) => {
    const byTier = tierPositionOf(a) - tierPositionOf(b)
    return byTier !== 0 ? byTier : a.position - b.position
  })
}

/** 分组排序：按 position 升序；后端已排序时这一步是幂等的。 */
export function sortProfileBadgeGroups(groups: ProfileBadgeGroup[]): ProfileBadgeGroup[] {
  return [...groups].sort((a, b) => a.position - b.position)
}
