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

  async function load() {
    pending.value = true
    failed.value = false
    blocked.value = ''
    try {
      const [list, groupList] = await Promise.all([
        request<{ items: AdminBadgeRow[] }>('/admin/badges'),
        request<{ items: AdminBadgeGroupRow[] }>('/admin/badges/groups')
      ])
      badges.value = Array.isArray(list?.items) ? list.items : []
      groups.value = Array.isArray(groupList?.items) ? groupList.items : []
    } catch (error) {
      classify(error)
      failed.value = true
    } finally {
      pending.value = false
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

  async function grantBadge(badgeId: number, payload: { username: string, reason: string }) {
    await request<{ granted: boolean }>(`/admin/badges/${badgeId}/grants`, {
      method: 'POST',
      body: { username: payload.username.trim(), reason: payload.reason.trim() }
    })
    await load()
  }

  async function recompute() {
    const result = await request<{ changed: number }>('/admin/badges/recompute', { method: 'POST' })
    await load()
    return Number(result?.changed ?? 0)
  }

  return { badges, groups, pending, failed, blocked, load, createBadge, updateBadge, deleteBadge, grantBadge, recompute }
}
