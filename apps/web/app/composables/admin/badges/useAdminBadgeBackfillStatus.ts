import { ref } from 'vue'
import { normalizeBackfillStatusPayload, type AdminBadgeBackfillRun } from '~/utils/admin/adminBadges'

/**
 * 回扫进度（`GET /admin/badges/backfill-status`，形状冻结于计划 §13#15 的后续刀）。
 *
 * 只读、只用于渲染「那一行」：`running` 的已扫描游标 / `failed` 的失败游标 / `completed`。
 * 端点可能还没上线（本刀前后端并行），也可能因为权限或网络失败：**一律不抛**，
 * 退化成空清单 + `failed = true`，由行组件给一条不打断的提示——静默比报错更糟，
 * 运营会以为后台什么都没在跑。
 */
export type AdminBadgeBackfillStatusLoader = () => Promise<unknown>

export function useAdminBadgeBackfillStatus(loader?: AdminBadgeBackfillStatusLoader) {
  const runs = ref<AdminBadgeBackfillRun[]>([])
  const failed = ref(false)
  const pending = ref(false)
  const loaded = ref(false)

  async function load() {
    if (pending.value) return
    pending.value = true
    try {
      const payload = loader ? await loader() : await useApiClient().request<unknown>('/admin/badges/backfill-status')
      const normalized = normalizeBackfillStatusPayload(payload)
      if (!normalized) throw new Error('unexpected badge backfill-status payload')
      runs.value = normalized
      failed.value = false
    } catch {
      // 端点未上线 / 网络失败 / 形状不符：空清单 + failed，UI 只加一条提示，页面照常可用。
      runs.value = []
      failed.value = true
    } finally {
      pending.value = false
      loaded.value = true
    }
  }

  return { runs, failed, pending, loaded, load }
}
