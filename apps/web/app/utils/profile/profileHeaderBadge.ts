import type { ForumBadgeRef } from '~/utils/forum/forumBadge'

/**
 * 资料页头部昵称行「该显示哪枚徽章」的唯一裁决点。
 *
 * 优先级（每条都有原因）：
 * 1. **他人页只认接口字段。** 本地佩戴态是「当前登录者」的，和眼前这个人的佩戴无关；
 *    拿它来填他人页会显示成页主的徽章，是彻底的错。
 * 2. **本地态没加载过 → 回落接口。** 没进过徽章 tab 时本地没有任何可信选择，
 *    接口投影的 `topBadge` 才是服务端已确认的佩戴结果。
 * 3. **归属不匹配 → 回落接口。** 登录/登出是客户端跳转、不整页刷新，全局 `useState`
 *    会带着上一个账号的 `loaded` 与 `wornSlug` 活下来。不校验归属，上一个账号选了
 *    「不佩戴」会让新账号头部永远空着，戴过的通用徽章则会被当成本人的。
 * 4. **hidden → null。** 本人刚点了「不佩戴」：头部必须立刻消失，不能等下一次资料请求
 *    回包（那时的 `topBadge` 还是旧的）。
 * 5. **slug 为空 → 回落接口。** auto 模式且服务端也没给自动徽章时，本地态没有可渲染对象。
 * 6. **命中徽章墙 → 那一条。** chip 的名字、等级名与等级色都来自徽章列表条目，佩戴层只有
 *    slug；命中才能渲染出完整 chip。
 * 7. **命中不到 → 回落接口。** 徽章被停用/删除或徽章墙还没加载时，与其拼一个空壳 chip，
 *    不如用服务端已投影好的那枚。
 */
export type HeaderBadgeInput = {
  /** 当前页是不是本人的资料页。 */
  isSelf: boolean
  /** 当前登录账号 id（游客为 0）。 */
  viewerId: number
  /** 本地佩戴态归属的账号 id（`useBadgeWorn().ownerId`）。 */
  wornOwnerId: number
  /** 本地佩戴态是否已经从服务端读到过。 */
  wornLoaded: boolean
  /** 本人是否选择了「不佩戴任何徽章」。 */
  wornHidden: boolean
  /** 本地佩戴态里的 slug（可能为空）。 */
  wornSlug: string
  /** 徽章墙的扁平条目：把 slug 解析成可渲染的 chip 载荷。 */
  wallBadges: ForumBadgeRef[]
  /** 服务端投影的 `topBadge`；没有可展示徽章时为 null。 */
  fallback: ForumBadgeRef | null
}

export function resolveHeaderBadge(input: HeaderBadgeInput): ForumBadgeRef | null {
  if (!input.isSelf) return input.fallback
  if (!input.wornLoaded) return input.fallback
  if (Number(input.wornOwnerId) !== Number(input.viewerId)) return input.fallback
  // hidden 必须排在 slug 判空之前：hidden 态的 slug 恒为空，先判空会把它回落成旧徽章。
  if (input.wornHidden) return null
  const slug = input.wornSlug.trim()
  if (!slug) return input.fallback
  return input.wallBadges.find(badge => badge.slug === slug) ?? input.fallback
}
