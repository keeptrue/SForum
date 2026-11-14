import { ref } from 'vue'
import type { BadgeConditionCatalogEntry, BadgeFactCandidate } from '~/utils/admin/rule'
import { normalizeFactCandidates } from '~/utils/admin/badgeFactCandidates'
import { normalizeRefCandidates, type BadgeRefCandidateMap } from '~/utils/admin/badgeRefCandidates'

/**
 * 信任等级的条件目录（`GET /admin/trust-levels/conditions`，M1）。
 *
 * 三态而不是两态：`idle`（还没发请求）/ `loading` / `ready` / `failed`。分开的理由是
 * **保存闸门要能区分「还没拉」与「拉失败」** —— 前者不该拦保存（用户可能永远不开面板），
 * 后者按 D21 拦。把两者压成一个布尔就会在页面刚打开时误拦一次。
 *
 * 载荷形状与徽章目录**同名字段**（后端 `dto.go` 明说「逐字一致」），所以参数/事实/引用
 * 三类候选直接复用徽章那两个归一化函数，不写第二套解析。
 *
 * `loader` 是测试注入点：默认走 `useApiClient()`，传了就不碰网络层。
 */
export type TrustLevelConditionsLoader = () => Promise<unknown>

export type TrustLevelConditionCatalog = {
  /** 服务端裁剪后的 12 条可用条件（14 条减去内容质量两条）。 */
  items: BadgeConditionCatalogEntry[]
  factCandidates: BadgeFactCandidate[]
  refCandidates: BadgeRefCandidateMap
}

export type TrustLevelConditionsStatus = 'idle' | 'loading' | 'ready' | 'failed'

/**
 * 归一化响应：`{conditions, factCandidates, refCandidates}`。
 *
 * 只认新形状的 `conditions`，**不接受裸数组或 `{items}`**：那两个是徽章端点的历史兼容，
 * 信任等级这条端点从第一天就是这个形状，兼容老形状只会把一次字段改名变成「目录空但成功」。
 */
export function normalizeTrustLevelConditions(payload: unknown): TrustLevelConditionCatalog | null {
  if (!payload || typeof payload !== 'object') return null
  const record = payload as Record<string, unknown>
  if (!Array.isArray(record.conditions)) return null
  const items = record.conditions.flatMap((item) => {
    if (!item || typeof item !== 'object' || typeof (item as { key?: unknown }).key !== 'string') return []
    return [item as BadgeConditionCatalogEntry]
  })
  return {
    items,
    factCandidates: normalizeFactCandidates(record.factCandidates) ?? [],
    refCandidates: normalizeRefCandidates(record.refCandidates)
  }
}

export function useAdminTrustLevelConditions(loader?: TrustLevelConditionsLoader) {
  const status = ref<TrustLevelConditionsStatus>('idle')
  const items = ref<BadgeConditionCatalogEntry[]>([])
  const factCandidates = ref<BadgeFactCandidate[]>([])
  const refCandidates = ref<BadgeRefCandidateMap>({})
  /** 失败原因原文（网络错误信息 / 'unexpected conditions payload'），只作为技术细节展示。 */
  const error = ref('')

  async function load() {
    if (status.value === 'loading') return
    status.value = 'loading'
    error.value = ''
    try {
      const payload = loader ? await loader() : await useApiClient().request<unknown>('/admin/trust-levels/conditions')
      const catalog = normalizeTrustLevelConditions(payload)
      if (!catalog) throw new Error('unexpected conditions payload')
      items.value = catalog.items
      factCandidates.value = catalog.factCandidates
      refCandidates.value = catalog.refCandidates
      status.value = 'ready'
    } catch (cause) {
      items.value = []
      factCandidates.value = []
      refCandidates.value = {}
      error.value = cause instanceof Error ? cause.message : String(cause ?? '')
      status.value = 'failed'
    }
  }

  return { status, items, factCandidates, refCandidates, error, load }
}
