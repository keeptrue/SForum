/** 搜索索引管理页的状态语义：把「HTTP 状态」与「展示语义」分开，页面只负责取文案。 */
export type AdminSearchLoadKind = 'ok' | 'empty' | 'forbidden' | 'error'

/** 徽标语义与 `SFBadge` 的 variant 取值一一对应（`danger` 而不是 `error`）。 */
export type AdminSearchBadgeVariant = 'neutral' | 'primary' | 'info' | 'success' | 'warning' | 'danger'

/**
 * 当前重建状态的加载结果。
 *
 * - `404`：该搜索引擎从未重建过 —— 这是**空态**，不是错误。
 * - `403`：没有 `search.manage` —— 必须与「暂无记录」区分，否则运营会以为功能坏了。
 * - 其余（含网络错误 / 500）：错误态，页面要给重试入口。
 */
export function reindexStatusLoadKind(statusCode: number): AdminSearchLoadKind {
  if (statusCode === 404) return 'empty'
  if (statusCode === 403) return 'forbidden'
  // 注意：这里只在 catch 分支被调用，`0` 表示「拿不到状态码」（网络/SSR 失败），
  // 不是「没有错误」。把它当成功会让整页静默退化成「从未重建过」——正是 UX2-05 的形态。
  return 'error'
}

/** 历史列表没有「尚无 run」的 404 语义：任何失败都是错误，不能静默。 */
export function reindexHistoryLoadKind(statusCode: number): AdminSearchLoadKind {
  if (statusCode === 403) return 'forbidden'
  return 'error'
}

/** 状态 → 徽标 variant。`failed` 必须落到 `danger`，旧实现返回的 `error` 不是合法 variant。 */
export function reindexStatusBadgeVariant(status: string): AdminSearchBadgeVariant {
  switch (status) {
    case 'running': return 'info'
    case 'completed': return 'success'
    case 'failed': return 'danger'
    default: return 'neutral'
  }
}
