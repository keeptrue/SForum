import type { ForumUserSummary } from '~/utils/forum/forumTaxonomy'

/**
 * 点赞的纯决策函数：乐观切换、按钮可见性、登录回跳。
 *
 * 组件只负责渲染与网络，状态计算留在这里，便于在没有 SFC 编译的环境里测试。
 */

export type ForumReactionSnapshot = {
  liked: boolean
  count: number
}

/** 乐观切换：点赞 +1、取消 -1，计数永不为负。 */
export function forumReactionToggle(snapshot: ForumReactionSnapshot): ForumReactionSnapshot {
  const liked = !snapshot.liked
  const count = Math.max(0, snapshot.count + (liked ? 1 : -1))
  return { liked, count }
}

/** 失败回滚：恢复点击前的快照，不把乐观结果留在界面上。 */
export function forumReactionRollback(previous: ForumReactionSnapshot): ForumReactionSnapshot {
  return { liked: previous.liked, count: Math.max(0, previous.count) }
}

/**
 * 父层回写：把控件 settle 后的权威值写回页面持有的主题对象。
 * 不写回时，页面任何一次按 props 重渲染都会把计数与选中态弹回服务端初值。
 */
export function forumReactionApplyToTopic<T extends {
  likeCount: number
  likedByMe?: boolean
  likedBy?: ForumUserSummary[]
}>(
  topic: T,
  liked: boolean,
  count: number,
  likedBy?: ForumUserSummary[]
): T {
  topic.likeCount = Math.max(0, Number.isFinite(count) ? count : 0)
  topic.likedByMe = liked
  // 头像组也要回写：只改计数会让父层重渲染时把叠放弹回服务端初值。
  if (likedBy) {
    topic.likedBy = likedBy
  }
  return topic
}

/**
 * 点击门槛：开关关闭或请求仍在途时不再发第二次请求。
 * 双击的第二下必须在这里被挡掉，否则会多打一次接口并打乱计数。
 */
export function forumReactionCanToggle(enabled: boolean, pending: boolean): boolean {
  return enabled && !pending
}

/** 服务端真值回填：以响应为准，计数钳制到非负。 */
export function forumReactionFromResponse(liked: boolean, likeCount: number): ForumReactionSnapshot {
  return { liked, count: Math.max(0, Number.isFinite(likeCount) ? likeCount : 0) }
}

/** 匿名访客点击点赞时应先登录；已登录直接发请求。 */
export function forumReactionNeedsLogin(authenticated: boolean): boolean {
  return !authenticated
}

/** 名单入口只在有赞且开关开启时出现。 */
export function forumReactionRosterVisible(enabled: boolean, count: number): boolean {
  return enabled && count > 0
}

/** 登录回跳路径：保留当前页面（含查询串）作为 redirect。 */
export function forumReactionLoginTarget(currentPath: string): { path: string, redirect: string } {
  return { path: '/login', redirect: currentPath }
}

/** 点赞切换后的权威状态（POST/DELETE /like 的 data）。 */
export type ForumReactionState = {
  targetType: 'topic' | 'comment'
  targetId: number
  reaction: 'like'
  liked: boolean
  likeCount: number
}

/** 「谁赞了」名单；items 复用公开用户摘要形状。 */
export type ForumReactionRoster = {
  items: ForumUserSummary[]
  total: number
  hasMore: boolean
}

/** 预览头像组：按 id 去重后截断到 max（服务端已保证自己优先、其余最早）。 */
export function forumReactionPreviewItems(
  likedBy: ForumUserSummary[] | null | undefined,
  max: number
): ForumUserSummary[] {
  const limit = Number.isFinite(max) ? Math.max(0, Math.trunc(max)) : 0
  if (limit === 0) return []
  const seen = new Set<number>()
  const items: ForumUserSummary[] = []
  for (const item of likedBy || []) {
    if (!item || seen.has(item.id)) continue
    seen.add(item.id)
    items.push(item)
    if (items.length === limit) break
  }
  return items
}

/** 头像组溢出数：总数减已展示，永不为负（+N 的 N）。 */
export function forumReactionStackOverflow(total: number, shown: number): number {
  const totalValue = Number.isFinite(total) ? Math.max(0, Math.trunc(total)) : 0
  const shownValue = Number.isFinite(shown) ? Math.max(0, Math.trunc(shown)) : 0
  return Math.max(0, totalValue - shownValue)
}

/**
 * 点赞/取消后的本地头像组：点赞把自己置顶，取消把自己摘掉，结果截断到 max。
 * base 必须是服务端给的预览（不含本次切换），否则会重复插入自己。
 */
export function forumReactionPreviewAfterToggle(
  base: ForumUserSummary[] | null | undefined,
  self: ForumUserSummary | null | undefined,
  liked: boolean,
  max: number
): ForumUserSummary[] {
  const rest = (base || []).filter(item => !self || item.id !== self.id)
  const next = liked && self ? [self, ...rest] : rest
  return forumReactionPreviewItems(next, max)
}

/** 名单分页合并：按 id 去重，保留首次出现的顺序。 */
export function forumReactionRosterMerge(
  existing: ForumUserSummary[] | null | undefined,
  page: ForumUserSummary[] | null | undefined
): ForumUserSummary[] {
  const merged = [...(existing || [])]
  const seen = new Set(merged.map(item => item.id))
  for (const item of page || []) {
    if (!item || seen.has(item.id)) continue
    seen.add(item.id)
    merged.push(item)
  }
  return merged
}

/** 还能不能继续翻页：有下一页、不在加载中、且 offset 未到服务端上限。 */
export function forumReactionRosterCanLoadMore(
  hasMore: boolean,
  loading: boolean,
  offset: number,
  cap: number
): boolean {
  const offsetValue = Number.isFinite(offset) ? Math.max(0, Math.trunc(offset)) : 0
  const capValue = Number.isFinite(cap) ? Math.max(0, Math.trunc(cap)) : 0
  return Boolean(hasMore) && !loading && offsetValue < capValue
}

/**
 * 名单总人数：首页取服务端真值；追加页只允许变大。
 * 服务端在 offset 越界时返回 total=0，直接采信会让「共 N 人」凭空消失。
 */
export function forumReactionRosterTotal(current: number, incoming: number, reset: boolean): number {
  const safe = (value: number) => (Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0)
  return reset ? safe(incoming) : Math.max(safe(current), safe(incoming))
}
