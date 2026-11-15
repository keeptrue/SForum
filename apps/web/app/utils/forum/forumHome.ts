import { forumCategoryPath, normalizeForumTopicSort, type ForumTopicSort } from './forumTaxonomy'

export type ForumHomeFilters = {
  query: string
  categorySlug: string
  tagSlug: string
  /** '' = 站点默认排序（URL 不携带 sort，由服务端 forum.list.default_sort 决定） */
  sort: ForumTopicSort | ''
}

export type ForumHomeRequestToken = {
  generation: number
  feedKey: string
}

export type ForumHomePageProgress = {
  requestedPage: number
  responsePage: number
  responseItemCount: number
  newItemCount: number
  loadedCount: number
  total: number
  perPage: number
  /** M5：API hasMore；优先于 total 推算 */
  hasMore?: boolean
  /** 本页是否用了 cursor 续页 */
  usedCursor?: boolean
}

type RouteQuery = Record<string, unknown>

const scalar = (value: unknown) => typeof value === 'string' ? value.trim() : ''

/**
 * 重复查询参数（`?category=a&category=b`、`?category[]=a`）在 Nuxt 里是数组，
 * 直接当字符串用会拼出 `/c/a%2Cb` 这种坏路径。只取首个字符串值，其余一律当空。
 */
export const firstQueryValue = (value: unknown): string =>
  Array.isArray(value) ? scalar(value[0]) : scalar(value)

/**
 * 首页旧链接 `?category=` 的兼容跳转目标。
 * 返回 '' 表示不跳转：空值、非字符串（数组里也没有字符串）、以及已经在 `/c/` 下。
 */
export function forumHomeCategoryRedirect(query: RouteQuery, path: string) {
  if (path.startsWith('/c/')) {
    return ''
  }
  const slug = firstQueryValue(query.category)

  return slug ? forumCategoryPath(slug) : ''
}

export const parseForumHomeQuery = (query: RouteQuery): ForumHomeFilters => ({
  query: scalar(query.q),
  categorySlug: scalar(query.category),
  tagSlug: scalar(query.tag),
  sort: normalizeForumTopicSort(query.sort)
})

export const buildForumHomeQuery = (filters: ForumHomeFilters) => Object.fromEntries(
  [
    ['q', filters.query.trim()],
    ['category', filters.categorySlug.trim()],
    ['tag', filters.tagSlug.trim()],
    ['sort', filters.sort]
  ].filter((entry): entry is [string, string] => Boolean(entry[1]))
)

export const forumHomeFeedKey = (filters: ForumHomeFilters) => JSON.stringify([
  filters.query.trim(),
  filters.categorySlug.trim(),
  filters.tagSlug.trim(),
  filters.sort
])

export function isForumHomeRequestCurrent(
  request: ForumHomeRequestToken,
  currentGeneration: number,
  activeFeedKey: string
) {
  return request.generation === currentGeneration && request.feedKey === activeFeedKey
}

export function hasReachedForumHomeEnd(progress: ForumHomePageProgress) {
  // M5：API 明确 hasMore=false 时立即结束（不依赖近似 total）
  if (progress.hasMore === false) {
    return true
  }
  if (progress.hasMore === true) {
    // 有 hasMore 但本页无新增（去重后）仍视为结束，避免死循环
    return progress.newItemCount === 0 && progress.responseItemCount === 0
  }
  return progress.responsePage < progress.requestedPage
    || progress.responseItemCount === 0
    || progress.responseItemCount < progress.perPage
    || progress.newItemCount === 0
    || progress.loadedCount >= progress.total
}
