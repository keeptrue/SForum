import type { AdminForumContentFilters, AdminForumContentKind } from '~/utils/admin/adminForumContent'

/**
 * 内容管理页的 **URL** 状态（UX2-14）。
 *
 * 与 `adminForumContent.ts` 里的 `buildAdminForumContentQuery` 不是一回事：那个构造的是
 * 发给 API 的 keyset 查询串，这里构造的是浏览器地址栏里的页面状态。
 *
 * 有意取舍：**分页游标不入 query**。游标是服务端 keyset token，塞进地址栏既长又不可
 * 阅读；因此刷新/分享会回到第一页，筛选与 tab 保持。这条取舍钉在测试名里。
 */

const CONTENT_STATUSES = ['active', 'locked', 'pending', 'rejected', 'hidden', 'deleted'] as const
const CONTENT_PER_PAGE = [20, 50, 100] as const
export const DEFAULT_CONTENT_PER_PAGE = 20

type RouteQuery = Record<string, unknown>

export type AdminForumContentRouteState = {
  tab: AdminForumContentKind | null
  filters: AdminForumContentFilters
}

function firstValue(value: unknown): string {
  if (Array.isArray(value)) return typeof value[0] === 'string' ? value[0] : ''
  return typeof value === 'string' ? value : ''
}

function positiveInt(value: unknown): number | undefined {
  const raw = firstValue(value).trim()
  if (!raw) return undefined
  const parsed = Number(raw)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

function text(value: unknown): string {
  return firstValue(value).trim()
}

/**
 * 解析地址栏状态。
 *
 * `allowedTabs` 是权限过滤后的可用分区：`?tab=topics` 在缺少 `topic.edit_any` /
 * `topic.revision.view_any` 时必须回落，不能把用户送进一个他无权访问的分区。
 * 非法值一律回落默认（tab 交给调用方取首个可用分区，筛选回落空）。
 */
export function parseAdminForumContentRouteQuery(
  query: RouteQuery,
  allowedTabs: readonly AdminForumContentKind[]
): AdminForumContentRouteState {
  const tabValue = firstValue(query.tab)
  const tab = (allowedTabs as readonly string[]).includes(tabValue)
    ? tabValue as AdminForumContentKind
    : null

  const statusValue = firstValue(query.status)
  const status = (CONTENT_STATUSES as readonly string[]).includes(statusValue) ? statusValue : ''
  const perPageValue = Number(firstValue(query.perPage))
  const perPage = (CONTENT_PER_PAGE as readonly number[]).includes(perPageValue)
    ? perPageValue
    : DEFAULT_CONTENT_PER_PAGE

  return {
    tab,
    filters: {
      status,
      authorUserID: positiveInt(query.authorUserID),
      authorUsername: text(query.authorUsername),
      updatedFrom: text(query.updatedFrom),
      updatedTo: text(query.updatedTo),
      topicID: positiveInt(query.topicID),
      titlePrefix: text(query.titlePrefix),
      categorySlug: text(query.categorySlug),
      perPage
    }
  }
}

/** 把页面状态写成地址栏 query：只保留非默认值，URL 保持可读。 */
export function buildAdminForumContentRouteQuery(
  tab: AdminForumContentKind,
  filters: AdminForumContentFilters
): Record<string, string> {
  const query: Record<string, string> = { tab }
  const values: Record<string, string | number | undefined> = {
    status: filters.status,
    authorUserID: filters.authorUserID,
    authorUsername: filters.authorUsername,
    updatedFrom: filters.updatedFrom,
    updatedTo: filters.updatedTo,
    topicID: filters.topicID,
    titlePrefix: filters.titlePrefix,
    categorySlug: filters.categorySlug,
    perPage: filters.perPage === DEFAULT_CONTENT_PER_PAGE ? undefined : filters.perPage
  }
  for (const [key, value] of Object.entries(values)) {
    if (value === undefined || value === null) continue
    const raw = `${value}`.trim()
    if (raw === '') continue
    query[key] = raw
  }
  return query
}
