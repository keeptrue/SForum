import { ref } from 'vue'

/**
 * 规则影响面估算（`GET /admin/badges/:badgeID/rule-impact`，形状见
 * `contracts/openapi/schemas/badges.yaml` 的 `BadgeRuleImpact`）。
 *
 * 语义要点，读侧不要自行加码：
 * - 估算的对象是**库里已保存的那条规则**（`EstimateRuleImpact(ctx, actor, badgeID)`），
 *   不是弹窗里还没提交的草稿；
 * - `estimated` 是**上界**（AND 取最严叶子、OR 取和，都对「同时满足」偏高）；
 * - `estimated === null` 表示**无法估算**，与 `0`（确实是零人）是两件事。
 */
export type AdminBadgeRuleImpactMethod =
  | 'single_leaf'
  | 'strictest_leaf'
  | 'strictest_leaf_partial'
  | 'sum_of_leaves'
  | 'complement_of_leaf'
  | 'unsupported_condition'

export interface AdminBadgeRuleImpact {
  estimated: number | null
  method: AdminBadgeRuleImpactMethod
  partial: boolean
  /** 参与不了估算的条件 key（例如 `object.authoring`），要逐个展示，不能只报个数。 */
  unsupportedConditions: string[]
}

export type AdminBadgeRuleImpactStatus = 'idle' | 'loading' | 'error' | 'ready'

/**
 * 影响面读取器。
 *
 * 端点可能在旧版本后端上不存在，也可能因为徽章被删/规则为空而失败：**一律不抛**，
 * 退化成 `status = 'error'`，由弹窗给可见提示并允许继续保存——估算失败不该挡住运营
 * 改名字这类无关操作。
 */
export function useAdminBadgeRuleImpact() {
  const { request } = useApiClient()

  const status = ref<AdminBadgeRuleImpactStatus>('idle')
  const impact = ref<AdminBadgeRuleImpact | null>(null)

  function reset() {
    status.value = 'idle'
    impact.value = null
  }

  async function load(badgeId: number): Promise<AdminBadgeRuleImpact | null> {
    if (!Number.isFinite(badgeId) || badgeId <= 0) {
      reset()
      return null
    }
    status.value = 'loading'
    impact.value = null
    try {
      const result = await request<Partial<AdminBadgeRuleImpact>>(`/admin/badges/${badgeId}/rule-impact`)
      impact.value = {
        // 只有「没给」和「明确 null」才算无法估算；数字 0 是有效读数。
        estimated: result?.estimated === null || result?.estimated === undefined
          ? null
          : Number(result.estimated),
        method: result?.method ?? 'unsupported_condition',
        partial: result?.partial === true,
        unsupportedConditions: Array.isArray(result?.unsupportedConditions)
          ? result.unsupportedConditions.filter((key): key is string => typeof key === 'string')
          : []
      }
      status.value = 'ready'
      return impact.value
    } catch {
      impact.value = null
      status.value = 'error'
      return null
    }
  }

  return { status, impact, load, reset }
}
