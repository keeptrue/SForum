import { ref } from 'vue'

/**
 * 规则健康清单（`GET /admin/badges/rule-health`，形状冻结于计划 §9）。
 *
 * 只回答「规则里的引用/条件还成立吗」，**不回答「有没有人达标」**——零授予是正常状态，
 * 把它渲染成告警会让运营去改一条本来正确的规则。
 *
 * 端点可能还没上线：失败一律退化成空清单 + `failed = true`，不抛错、不阻塞页面。
 * 空 `items` 是「全站规则健康」，UI 不显示任何东西。
 */
export type BadgeRuleHealthKind =
  | 'missing_topic'
  | 'missing_category'
  | 'missing_tag'
  | 'missing_role'
  | 'missing_provider'
  | 'missing_badge_slug'
  | 'missing_fact_key'
  /** 能解码但结构/取值不合法（例如空条件组）：`ref` 是引擎错误码，`label` 为空串。 */
  | 'invalid_rule_shape'
  | 'undecodable_rule'

export interface BadgeRuleHealthIssue {
  kind: BadgeRuleHealthKind
  /** 与引擎 `Error.Path` 同一套坐标，可直接定位到规则节点。 */
  path: string
  ref: string
  /** 服务端给的人类可读引用（例如「主题 #123」）。 */
  label: string
}

export interface BadgeRuleHealthRow {
  badgeId: number
  slug: string
  name: string
  issues: BadgeRuleHealthIssue[]
}

export type BadgeRuleHealthLoader = () => Promise<unknown>

/** 兼容 `{ items: [...] }` 与裸数组；无法识别时返回 null。
 *
 * 契约落地后**收成一种形状并删掉另一条分支**：同时兼容两种会让后端改响应形状时
 * 前端安静地继续工作，契约漂移永远不红。收紧时同时补一条「只接受一种」的测试。
 */
export function normalizeRuleHealthPayload(payload: unknown): BadgeRuleHealthRow[] | null {
  const candidate = Array.isArray(payload) ? payload : (payload as { items?: unknown } | null)?.items
  if (!Array.isArray(candidate)) return null
  return candidate.filter(
    (row): row is BadgeRuleHealthRow =>
      Boolean(row) && typeof row === 'object' && typeof (row as { badgeId?: unknown }).badgeId === 'number'
  )
}

export function useAdminBadgeRuleHealth(loader?: BadgeRuleHealthLoader) {
  const items = ref<BadgeRuleHealthRow[]>([])
  const failed = ref(false)
  const pending = ref(false)
  const loaded = ref(false)

  async function load() {
    if (pending.value) return
    pending.value = true
    try {
      const payload = loader ? await loader() : await useApiClient().request<unknown>('/admin/badges/rule-health')
      const rows = normalizeRuleHealthPayload(payload)
      if (!rows) throw new Error('unexpected rule-health payload')
      items.value = rows
      failed.value = false
    } catch {
      // 端点未上线 / 网络失败 / 形状不符：空清单 + failed，UI 只给一条不打断的提示。
      items.value = []
      failed.value = true
    } finally {
      pending.value = false
      loaded.value = true
    }
  }

  return { items, failed, pending, loaded, load }
}
