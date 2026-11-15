import { announceAssertive, announcePolite } from './liveRegion'

/**
 * 列表类异步面的播报决策（UX2-17）。
 *
 * 抽成纯决策 + 两条播报通道，是因为这里最容易写错的两件事都属于「状态判定」：
 * - `useAsyncData(..., { default: () => [] })` 在请求失败时保留默认值，
 *   只看条数会得出「已加载 0 个任务」这种与错误 toast 互相矛盾的播报；
 * - 首屏数据可能在 watch 注册前就已经落位（例如 `await` 过的 composable），
 *   没有 `immediate` 的话首屏永远不播报。
 *
 * 所以调用方统一传 pending / failed，由这里决定：未完成 → 不播；失败 → assertive；
 * 成功 → polite。返回值只用于测试与调试，不影响播报。
 */

export type AsyncAnnounceState = 'pending' | 'failed' | 'succeeded'

export type AsyncAnnounceOptions = {
  /** 请求仍在进行。 */
  pending: boolean
  /** 请求失败（保留错误对象或布尔值都行）。 */
  failed: boolean
  /** 成功文案（已翻译）。 */
  success: string
  /** 失败文案（已翻译）。 */
  failure: string
}

export function announceAsyncResult(options: AsyncAnnounceOptions): AsyncAnnounceState {
  if (options.pending) return 'pending'

  if (options.failed) {
    const message = options.failure.trim()
    if (message) announceAssertive(message)
    return 'failed'
  }

  const message = options.success.trim()
  if (message) announcePolite(message)
  return 'succeeded'
}
