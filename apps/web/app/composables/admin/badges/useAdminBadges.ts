import type { AdminBadgeGrantRow, AdminBadgeGroupInput } from '~/utils/admin/adminBadges'
import type { AdminBadgeRow } from '~/utils/admin/adminBadges'
import { apiErrorReason } from '~/composables/useApiClient'

/** 后台徽章分组（GET /admin/badges/groups）。 */
export type AdminBadgeGroupRow = {
  id: number
  key: string
  name: string
  description: string
  position: number
  badgeCount: number
}

/** 新建 / 编辑徽章的请求体，字段名与后端 badgeBody 逐字一致。 */
export type AdminBadgeInput = {
  slug: string
  name: string
  description: string
  groupId: number | null
  tier: string
  icon: string
  grantMode: string
  metric: string | null
  threshold: number | null
  enabled: boolean
  position: number
}

/** 两种 403 语义必须分开：功能关闭 ≠ 没权限。 */
export type AdminBadgeBlockReason = '' | 'disabled' | 'forbidden'

/**
 * 后台徽章的数据层：列表、分组、CRUD、授予、重算。
 *
 * 错误分类只认后端稳定错误码：`forum.badges_disabled` 是运营关了产品开关，
 * `permission.denied` 是账号没权限。页面据此分别显示「功能已关闭」与「无权限」，
 * 不按 HTTP 403 统一处理。
 */
export function useAdminBadges() {
  const { request } = useApiClient()

  const badges = ref<AdminBadgeRow[]>([])
  const groups = ref<AdminBadgeGroupRow[]>([])
  const pending = ref(false)
  const failed = ref(false)
  const blocked = ref<AdminBadgeBlockReason>('')

  function classify(error: unknown) {
    const reason = apiErrorReason(error)
    blocked.value = reason === 'forum.badges_disabled'
      ? 'disabled'
      : reason === 'permission.denied' ? 'forbidden' : ''
    return blocked.value
  }

  // 并发 load 的序号：壳层刷新 / 分区 onSaved / grants onGranted 可能同时触发，
  // 只允许最后一次的响应写状态，否则旧列表覆盖新列表、pending 提前熄灭。
  let loadSeq = 0

  async function load() {
    const seq = ++loadSeq
    pending.value = true
    failed.value = false
    blocked.value = ''
    try {
      const [list, groupList] = await Promise.all([
        request<{ items: AdminBadgeRow[] }>('/admin/badges'),
        request<{ items: AdminBadgeGroupRow[] }>('/admin/badges/groups')
      ])
      if (seq !== loadSeq) return
      badges.value = Array.isArray(list?.items) ? list.items : []
      groups.value = Array.isArray(groupList?.items) ? groupList.items : []
    } catch (error) {
      if (seq !== loadSeq) return
      classify(error)
      failed.value = true
    } finally {
      if (seq === loadSeq) pending.value = false
    }
  }

  async function createBadge(input: AdminBadgeInput) {
    await request<AdminBadgeRow>('/admin/badges', { method: 'POST', body: input })
    await load()
  }

  async function updateBadge(id: number, input: AdminBadgeInput) {
    await request<AdminBadgeRow>(`/admin/badges/${id}`, { method: 'PATCH', body: input })
    await load()
  }

  async function deleteBadge(id: number) {
    await request<{ deleted: boolean }>(`/admin/badges/${id}`, { method: 'DELETE' })
    await load()
  }

  async function createBadgeGroup(input: AdminBadgeGroupInput) {
    await request<AdminBadgeGroupRow>('/admin/badges/groups', { method: 'POST', body: input })
    await load()
  }

  async function updateBadgeGroup(id: number, input: AdminBadgeGroupInput) {
    await request<AdminBadgeGroupRow>(`/admin/badges/groups/${id}`, { method: 'PATCH', body: input })
    await load()
  }

  /** 删除分组不拒绝有徽章的组：组内徽章由外键 SET NULL 落成未分组（领域层语义）。 */
  async function deleteBadgeGroup(id: number) {
    const result = await request<{ deleted: boolean }>(`/admin/badges/groups/${id}`, { method: 'DELETE' })
    await load()
    return result?.deleted !== false
  }

  /**
   * 授予记录（GET /admin/badges/:id/grants）：后端有分页，perPage 上限 200、缺省 50，
   * 返回 { items, total }。total 用于总数展示与翻页判断。
   */
  async function listGrants(
    badgeId: number,
    pagination: { perPage?: number, offset?: number } = {}
  ): Promise<{ items: AdminBadgeGrantRow[], total: number }> {
    const query = new URLSearchParams({
      perPage: String(pagination.perPage ?? 20),
      offset: String(pagination.offset ?? 0)
    })
    const result = await request<{ items: AdminBadgeGrantRow[], total: number }>(
      `/admin/badges/${badgeId}/grants?${query.toString()}`,
      { method: 'GET' }
    )
    return {
      items: Array.isArray(result?.items) ? result.items : [],
      total: Number(result?.total ?? 0)
    }
  }

  /**
   * 手动收回（DELETE /admin/badges/:id/grants/:userId）。
   * 返回 revoked 原值：false 表示「本来未持有」（仍是 200 幂等），不是请求失败。
   */
  async function revokeGrant(badgeId: number, userId: number): Promise<boolean> {
    const result = await request<{ revoked: boolean }>(`/admin/badges/${badgeId}/grants/${userId}`, { method: 'DELETE' })
    return result?.revoked === true
  }

  async function grantBadge(badgeId: number, payload: { username: string, reason: string }) {
    await request<{ granted: boolean }>(`/admin/badges/${badgeId}/grants`, {
      method: 'POST',
      body: { username: payload.username.trim(), reason: payload.reason.trim() }
    })
    await load()
  }

  /**
   * 全量重算。后端返回 { granted, refreshed }（Models/Badges/admin_service.go 的
   * RecomputeResult）：granted = 新插入的授予行数，refreshed = top_badge_id 实际变化的
   * 用户数。两者相互独立（{0,0} 是正常幂等结果），返回合计作为「本次影响量」。
   */
  async function recompute(): Promise<number> {
    const result = await request<{ granted: number, refreshed: number }>('/admin/badges/recompute', { method: 'POST' })
    await load()
    return Number(result?.granted ?? 0) + Number(result?.refreshed ?? 0)
  }

  return {
    badges,
    groups,
    pending,
    failed,
    blocked,
    load,
    createBadge,
    updateBadge,
    deleteBadge,
    createBadgeGroup,
    updateBadgeGroup,
    deleteBadgeGroup,
    grantBadge,
    listGrants,
    revokeGrant,
    recompute
  }
}
