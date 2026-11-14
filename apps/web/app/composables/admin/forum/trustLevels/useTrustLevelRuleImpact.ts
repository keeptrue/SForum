import { ref } from 'vue'
import type { TierForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'

/**
 * 保存前的影响面估算（`POST /admin/trust-levels/rule-impact`，M1）。
 *
 * 三个设计点：
 * 1. **草稿请求体**：新档也估，所以 `tierId` 可以是空串/新 id —— 不依赖已保存的档位。
 *    这正是「新建档位不弹估算」那条作废的原因（那是徽章 GET-by-id 的形态）。
 * 2. **失败不拦保存**：`loadFailed` 是信息不是闸门；估算接口挂掉时运营仍然要能保存配置。
 * 3. **`estimated: null` 与 `0` 严格分开**：`null` 是「算不出来」，`0` 是读数，
 *    归一化时**不许**用 `||` 把 0 变成 null。
 */
export const TRUST_LEVEL_RULE_IMPACT_PATH = '/admin/trust-levels/rule-impact'

export type TrustLevelImpactStatus = 'idle' | 'loading' | 'ready' | 'error'

export type TrustLevelImpactTierResult = {
  tierId: string
  /** `null` = 无法估算（OR/NOT 含不可估条件，或规则解不开）；`0` 是合法读数。 */
  estimated: number | null
  method: string
  partial: boolean
  unsupportedConditions: string[]
}

export type TrustLevelImpactLoader =
  (tiers: readonly TierForm[]) => Promise<unknown>

/** 载荷里**至少有一档带规则**才需要估算（纯数值编辑不打扰运营）。 */
export function hasRulePayload(tiers: readonly TierForm[]): boolean {
  return tiers.some(tier => tier.rule !== null && tier.rule !== undefined)
}

/** 请求体：草稿档位，每档 `{tierId, requirements, rule}`（`rule` 原样透传）。 */
export function ruleImpactRequestBody(tiers: readonly TierForm[]) {
  return {
    tiers: tiers.map(tier => ({
      tierId: tier.id,
      requirements: tier.requirements,
      rule: tier.rule ?? null
    }))
  }
}

/** 归一化响应：`{tiers: [...]}`；形状不符 → `null`（调用方按失败处理，不静默当空）。 */
export function normalizeRuleImpactResponse(payload: unknown): TrustLevelImpactTierResult[] | null {
  if (!payload || typeof payload !== 'object') return null
  const record = payload as Record<string, unknown>
  if (!Array.isArray(record.tiers)) return null
  return record.tiers.flatMap((entry) => {
    if (!entry || typeof entry !== 'object') return []
    const item = entry as Record<string, unknown>
    // 0 必须先于「假值」判断：`Number.isFinite(0)` 为真，`0 || null` 会把它吃掉。
    const raw = item.estimated
    const estimated = raw === null || raw === undefined
      ? null
      : (Number.isFinite(Number(raw)) ? Math.trunc(Number(raw)) : null)
    return [{
      tierId: typeof item.tierId === 'string' ? item.tierId : '',
      estimated,
      method: typeof item.method === 'string' ? item.method : '',
      partial: item.partial === true,
      unsupportedConditions: Array.isArray(item.unsupportedConditions)
        ? item.unsupportedConditions.filter((key): key is string => typeof key === 'string')
        : []
    }]
  })
}

export function useTrustLevelRuleImpact(loader?: TrustLevelImpactLoader) {
  const status = ref<TrustLevelImpactStatus>('idle')
  const results = ref<TrustLevelImpactTierResult[]>([])

  async function estimate(tiers: readonly TierForm[]) {
    status.value = 'loading'
    results.value = []
    try {
      const payload = loader
        ? await loader(tiers)
        : await useApiClient().request<unknown>(TRUST_LEVEL_RULE_IMPACT_PATH, {
          method: 'POST',
          body: ruleImpactRequestBody(tiers)
        })
      const normalized = normalizeRuleImpactResponse(payload)
      if (!normalized) throw new Error('unexpected rule impact payload')
      results.value = normalized
      status.value = 'ready'
    } catch {
      // 估算失败 = 一条信息，不是闸门：保持 `error` 让弹窗给「仍可继续保存」。
      results.value = []
      status.value = 'error'
    }
  }

  function reset() {
    status.value = 'idle'
    results.value = []
  }

  return { status, results, estimate, reset }
}
