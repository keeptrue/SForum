export type PaginationWindowItem = number | 'ellipsis-start' | 'ellipsis-end'

/** 非有限值（NaN/Infinity）回落到默认值，其余收敛为不小于 min 的整数。 */
function normalizeInteger(value: number, fallback: number, min: number): number {
  const integer = Math.trunc(value)
  return Number.isFinite(integer) ? Math.max(min, integer) : Math.max(min, fallback)
}

/**
 * 生成分页页码窗口：首页、末页、当前页 ± siblingCount，只有真的隔开多页时才放省略号。
 *
 * 页数少时整段展示。此前「只隔一个页码」也会折叠成省略号：4 页第 1 页渲染成
 * `1 2 … 4`，第 3 页不可见，而第 2 页又显示 `1 2 3 4`——同一个列表翻一页就换了套规则。
 */
export function buildPaginationWindow(
  page: number,
  totalPages: number,
  siblingCount = 1
): PaginationWindowItem[] {
  const total = normalizeInteger(totalPages, 1, 1)
  const current = Math.min(total, normalizeInteger(page, 1, 1))
  const siblings = normalizeInteger(siblingCount, 1, 0)
  // 贴边时向内补足的窗口宽度：首尾 + 当前页 ± siblings。
  const edgeWindow = siblings * 2 + 3

  // 页数不超过「首尾 + 两侧省略号 + 窗口」时整段展示，避免只藏一页却放省略号。
  if (total <= edgeWindow + 2) {
    return Array.from({ length: total }, (_, index) => index + 1)
  }

  let start = Math.max(1, current - siblings)
  let end = Math.min(total, current + siblings)
  if (start === 1) {
    end = Math.min(total, edgeWindow)
  }
  if (end === total) {
    start = Math.max(1, total - edgeWindow + 1)
  }
  // 只隔一页时直接展示那一页：省略号只用于「至少藏两页」的缺口。
  if (start === 3) {
    start = 2
  }
  if (end === total - 2) {
    end = total - 1
  }

  const values: PaginationWindowItem[] = [1]
  if (start > 2) {
    values.push('ellipsis-start')
  }
  for (let value = Math.max(2, start); value <= Math.min(total - 1, end); value += 1) {
    values.push(value)
  }
  if (end < total - 1) {
    values.push('ellipsis-end')
  }
  if (total > 1) {
    values.push(total)
  }
  return values
}
