import type { AdminEffectiveContribution } from './adminExtensions'

/**
 * 扩展点治理（启用 / 排序 / 恢复默认）的纯逻辑。
 *
 * 宿主只覆盖「生效状态与顺序」两件事：贡献内容仍是插件 manifest 的声明。
 * 因此这里的删除语义是**恢复默认**（DELETE 覆盖），不是删掉插件贡献。
 */
// 一条排序写入。字段名就是 wire 名 `sortOrder`（PUT body），读模型里对应的是
// `effectiveSortOrder`（排序后实际生效的值）：写的是覆盖值，读的是裁决结果。
export type ContributionSortPlanEntry = {
  id: string
  sortOrder: number
}

export type ContributionSortPlan =
  | { ok: true, updates: ContributionSortPlanEntry[] }
  | { ok: false, reason: 'boundary' }

export function contributionRowKey(point: string, contributionId: string): string {
  return `${point}:${contributionId}`
}

export function effectiveOrderOf(item: AdminEffectiveContribution): number {
  return typeof item.effectiveSortOrder === 'number' ? item.effectiveSortOrder : item.order
}

export function contributionEnabled(item: AdminEffectiveContribution): boolean {
  return item.enabled !== false
}

export function contributionOverridden(item: AdminEffectiveContribution): boolean {
  return Boolean(item.overridden)
}

/** 同一扩展点内按生效顺序排列；顺序相同再按 id，保证渲染稳定。 */
export function sortContributions(items: AdminEffectiveContribution[]): AdminEffectiveContribution[] {
  return [...items].sort((left, right) => (effectiveOrderOf(left) - effectiveOrderOf(right))
    || left.id.localeCompare(right.id))
}

export function contributionsForPoint(
  items: AdminEffectiveContribution[],
  point: string
): AdminEffectiveContribution[] {
  return sortContributions(items.filter(item => item.point === point))
}

/**
 * 上移/下移一格：只改动交换双方的值（两个 PUT）。
 * 两边生效顺序相同时（例如都还是 manifest 默认值），整组按 1..n 重排，
 * 否则交换同值等于没动。
 */
export function contributionMovePlan(
  items: AdminEffectiveContribution[],
  point: string,
  contributionId: string,
  direction: -1 | 1
): ContributionSortPlan {
  const group = contributionsForPoint(items, point)
  const index = group.findIndex(item => item.id === contributionId)
  if (index < 0) return { ok: false, reason: 'boundary' }
  const target = index + direction
  if (target < 0 || target >= group.length) return { ok: false, reason: 'boundary' }

  const current = group[index]
  const neighbor = group[target]
  if (!current || !neighbor) return { ok: false, reason: 'boundary' }

  const currentOrder = effectiveOrderOf(current)
  const neighborOrder = effectiveOrderOf(neighbor)
  if (currentOrder === neighborOrder) {
    return {
      ok: true,
      updates: group.map((item, position) => ({ id: item.id, sortOrder: position + 1 }))
    }
  }
  return {
    ok: true,
    updates: [
      { id: current.id, sortOrder: neighborOrder },
      { id: neighbor.id, sortOrder: currentOrder }
    ]
  }
}

/** 贡献点里是否承载「tab」类表面：禁用确认文案要能说清影响面。 */
export function isTabSurfacePoint(point: string): boolean {
  return /(^|[._-])(tab|tabs)([._-]|$)/i.test(point) || /(^|[._-])(section|sections)([._-]|$)/i.test(point)
}

/** 治理页列表区该显示什么：四种状态互斥，空态与失败态不能长得一样。 */
export type ContributionCatalogViewState = 'idle' | 'loading' | 'error' | 'empty' | 'list'

export function contributionCatalogViewState(
  status: string,
  total: number
): ContributionCatalogViewState {
  switch (status) {
    case 'loading':
      return 'loading'
    case 'error':
      return 'error'
    case 'ready':
      return total > 0 ? 'list' : 'empty'
    default:
      return 'idle'
  }
}
