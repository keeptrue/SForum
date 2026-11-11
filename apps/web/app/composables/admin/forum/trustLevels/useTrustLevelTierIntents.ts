import { computed, ref, toValue, type MaybeRefOrGetter, type Ref } from 'vue'
import {
  activeTiersOf,
  canRetireTier,
  deleteTier,
  retireTier,
  retiredTierImpactKeys,
  type TierForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import type { TrustLevelsForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'

/**
 * 档位的两个**破坏性意图**（停用 / 删除）共享同一个形状：
 * 「先立起请求 → 由 shell 弹 modal 披露后果 → 运营确认后才写表单」。
 *
 * 为什么单独一个 composable：
 * 1. `useTrustLevelsAdminPage` 已经 500 行出头，再加两条意图只会把它变成上帝对象；
 * 2. 两条意图的**闸门**比 UI 更重要，独立出来后每条闸门都能被单独读懂：
 *    - 停用：active 档数 > 1；
 *    - 删除：该档**已经保存为停用**（`savedRetiredIds` 命中）+ 当前也是停用态。
 * 3. 意图只做「表单怎么变」，不做保存：保存入口永远只有 footer 一个（D-N10）。
 *
 * 删除的两步闸门（D-C2）刻意严格：删除是不可恢复的，若允许「停用未保存 → 直接删」，
 * 就会绕过「停用先落盘、看清影响面」这一层，也让「删除永不改 rank」的论证失效
 * （未落盘的停用档当时仍占着 rank，删掉它就是一次静默重排）。
 */

/** 意图请求：modal 需要的全部上下文（名称用于标题，impactKeys 用于影响面披露）。 */
export type TrustLevelTierIntentRequest = {
  id: string
  name: { zh: string, en: string }
  /** active 位次（1 起）；已停用档没有位次 → `0`。 */
  rank: number
  /** 该档给、其它 active 档都不给的解锁键（可能为空）。 */
  impactKeys: string[]
}

/** 停用请求：与删除请求同形，保留独立别名是为了既有调用点的读法不变。 */
export type TrustLevelRetireRequest = TrustLevelTierIntentRequest
export type TrustLevelDeleteRequest = TrustLevelTierIntentRequest

export function useTrustLevelTierIntents(options: {
  /** 表单本体：删除要把墓碑 id 写回 `form.deletedTierIds`，所以这里必须拿到 form 而不是只拿 tiers。 */
  form: Ref<TrustLevelsForm>
  /** **已保存**的停用档 id（来自 `initial`）：删除的前置条件。 */
  savedRetiredIds: MaybeRefOrGetter<string[]>
  canManage: MaybeRefOrGetter<boolean>
  /** 唯一写档位数组的通道（页面的 `applyTiers`，自带权限兜底与深拷贝）。 */
  applyTiers: (mutate: (tiers: TierForm[]) => TierForm[]) => void
}) {
  const tiers = computed(() => options.form.value.tiers)
  const savedRetired = computed(() => [...toValue(options.savedRetiredIds)])

  const retireRequest = ref<TrustLevelTierIntentRequest | null>(null)
  const deleteRequest = ref<TrustLevelTierIntentRequest | null>(null)

  function request(id: string): TrustLevelTierIntentRequest | null {
    const tier = tiers.value.find(entry => entry.id === id)
    if (!tier) return null
    const rank = activeTiersOf(tiers.value).findIndex(entry => entry.id === id) + 1
    return {
      id,
      name: { ...tier.name },
      rank: tier.retired ? 0 : rank,
      impactKeys: retiredTierImpactKeys(tiers.value, id)
    }
  }

  /* ────────────────────────────── 停用（软停用，两步） ────────────────────────────── */

  function requestRetire(id: string) {
    if (!toValue(options.canManage)) return
    if (!canRetireTier(tiers.value)) return
    const tier = tiers.value.find(entry => entry.id === id)
    if (!tier || tier.retired) return
    retireRequest.value = request(id)
  }

  function confirmRetire() {
    const pending = retireRequest.value
    if (!pending) return
    options.applyTiers(tiers => retireTier(tiers, pending.id))
    retireRequest.value = null
  }

  function cancelRetire() {
    retireRequest.value = null
  }

  /* ────────────────────────────── 删除（不可恢复，留墓碑） ────────────────────────────── */

  /** 该档能否删除：已保存为停用 + 当前仍是停用态（两道都要，缺一不可）。 */
  function canDelete(id: string) {
    const tier = tiers.value.find(entry => entry.id === id)
    return Boolean(tier?.retired) && savedRetired.value.includes(id)
  }

  function requestDelete(id: string) {
    if (!toValue(options.canManage)) return
    if (!canDelete(id)) return
    deleteRequest.value = request(id)
  }

  function confirmDelete() {
    const pending = deleteRequest.value
    if (!pending) return
    options.applyTiers(tiers => deleteTier(tiers, pending.id))
    appendTombstone(pending.id)
    deleteRequest.value = null
  }

  function cancelDelete() {
    deleteRequest.value = null
  }

  /**
   * 墓碑 id 追加（去重、保序）。必须在 `applyTiers` **之后**调用：
   * `applyTiers` 会整块替换 `form.value`，先写墓碑会被覆盖掉。
   */
  function appendTombstone(id: string) {
    const current = options.form.value.deletedTierIds ?? []
    if (current.includes(id)) return
    options.form.value = { ...options.form.value, deletedTierIds: [...current, id] }
  }

  return {
    retireRequest,
    requestRetire,
    confirmRetire,
    cancelRetire,
    deleteRequest,
    canDelete,
    requestDelete,
    confirmDelete,
    cancelDelete,
    savedRetiredIds: savedRetired
  }
}
