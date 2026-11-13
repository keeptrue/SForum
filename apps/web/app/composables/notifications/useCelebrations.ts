import { computed, watch } from 'vue'

import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useNotifications } from '~/composables/notifications/useNotifications'
import type { CelebrationItem, CelebrationPage } from '~/utils/notifications/celebrationPresentation'

/**
 * 站内庆祝（celebration）的数据层。
 *
 * 语义（与后端 `GET/POST /notifications/celebrations*` 一并冻结）：
 * - `initialized === false` 表示这个账号第一次进庆祝通道：只把静默游标推到当前
 *   `cursorId`（POST /seen），**不回放历史**，也不在同一轮里再拉一次。
 * - 关闭/点击后 ack 的是**已消费的最大条目 id**，不是响应里的 `cursorId`：服务端那个
 *   `cursorId` 是「读取前游标」，条目按 `id > cursorId` 取出，ack 它等于恒等 no-op，
 *   下一次 revision 刷新会把同一批又弹回来（`GREATEST` upsert 只进不退）。
 * - 庆祝不是关键路径：任何失败静默保留上一份状态，不 toast、不抛给调用方。
 * - 状态放 `useState`：宿主、页面与导航往返共享同一份队列，条目按 id 去重。
 */
export type CelebrationState = {
  initialized: boolean
  cursorId: number
  total: number
  items: CelebrationItem[]
}

export function emptyCelebrationState(): CelebrationState {
  // 默认 `initialized: true`：还没拉到服务端响应时不能把自己当成「首访」去 POST。
  return { initialized: true, cursorId: 0, total: 0, items: [] }
}

/** 同一 id 只保留一条：重复拉取（revision 抖动、页面往返）不会叠出两张卡。 */
export function dedupeCelebrationItems(items: CelebrationItem[] | undefined): CelebrationItem[] {
  if (!Array.isArray(items)) return []
  const seen = new Set<number>()
  const result: CelebrationItem[] = []
  for (const item of items) {
    if (!item || typeof item !== 'object') continue
    const id = Math.trunc(Number(item.id) || 0)
    // 没有 id 的条目无法 ack、也无法去重：整条丢掉，避免画出一张关不掉的卡。
    if (id <= 0 || seen.has(id)) continue
    seen.add(id)
    result.push({ ...item, id })
  }
  return result
}

/**
 * ack 用的游标：`max(cursorId, 已加载条目 id)`。
 *
 * 为什么不直接发 `cursorId`：服务端 feed 里的 `cursorId` 是**读取前**的游标，条目按
 * `id > cursorId` 选出，所以条目 id 恒大于它。只 ack `cursorId` 在 `GREATEST` upsert 下
 * 不推进任何值 —— 用户关掉弹窗，下一条实时推送又把同一批弹回来。
 * 没有条目时保持 `cursorId`（幂等 no-op），首访与空队列共用同一条路径。
 */
export function consumedCursorId(state: Pick<CelebrationState, 'cursorId' | 'items'>): number {
  const base = Math.max(0, Math.trunc(Number(state?.cursorId) || 0))
  const queued = Array.isArray(state?.items) ? state.items : []
  return queued.reduce((max, item) => {
    const id = Math.trunc(Number(item?.id) || 0)
    return id > max ? id : max
  }, base)
}

export function useCelebrations() {
  const { request } = useApiClient()
  const { revision } = useNotifications()
  const { user } = useAuthSession()
  const state = useState<CelebrationState>('notification-celebrations', emptyCelebrationState)
  const pending = useState<boolean>('notification-celebrations-pending', () => false)
  const items = computed(() => state.value.items)

  function reset() {
    state.value = emptyCelebrationState()
  }

  async function markSeen(cursorId: number) {
    if (cursorId <= 0) return
    await request('/notifications/celebrations/seen', {
      method: 'POST',
      body: { cursorId }
    })
  }

  async function refresh(): Promise<CelebrationState> {
    if (!user.value) {
      reset()
      return state.value
    }
    // 并发去重：revision 连发时只保留一次在途请求。
    if (pending.value) return state.value
    pending.value = true
    try {
      const page = await request<CelebrationPage>('/notifications/celebrations')
      const cursorId = Math.max(0, Math.trunc(Number(page?.cursorId) || 0))
      if (page?.initialized === false) {
        await markSeen(cursorId)
        state.value = { initialized: true, cursorId, total: 0, items: [] }
        return state.value
      }
      state.value = {
        initialized: true,
        cursorId,
        total: Math.max(0, Math.trunc(Number(page?.total) || 0)),
        items: dedupeCelebrationItems(page?.items)
      }
      return state.value
    } catch {
      return state.value
    } finally {
      pending.value = false
    }
  }

  /**
   * 关闭或点击跳转后调用：先收起本地队列（不闪回），再 ack **已消费的最大条目 id**。
   *
   * `state.cursorId` 是服务端「读取前游标」，条目恒为 `id > cursorId`；只 ack 它不会推进
   * 任何东西，关闭后下一次 revision 刷新会把同一批再拉回来。没有条目时退回 `cursorId`，
   * 那是一个幂等 no-op（`markSeen` 对 <=0 直接返回）。
   */
  async function dismiss() {
    const ackId = consumedCursorId(state.value)
    state.value = { ...state.value, items: [], total: 0 }
    try {
      await markSeen(ackId)
    } catch {
      // 静默：下次 revision 会重新拉到未消费的条目，不会永久吞掉通知。
    }
  }

  // 通知 revision 推进（实时推送或任一页面刷新未读数）与登录态切换都重新对齐。
  watch(revision, () => {
    void refresh()
  })
  watch(() => user.value?.id ?? 0, (id) => {
    if (id > 0) {
      void refresh()
      return
    }
    reset()
  })

  return { state, items, cursorId: computed(() => state.value.cursorId), initialized: computed(() => state.value.initialized), pending, refresh, dismiss, markSeen, reset }
}
