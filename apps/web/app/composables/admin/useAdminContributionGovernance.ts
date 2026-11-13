import { apiErrorMessage } from '~/composables/useApiClient'
import type { AdminEffectiveContribution } from '~/utils/admin/adminExtensions'
import {
  contributionMovePlan,
  contributionRowKey,
  effectiveOrderOf,
  isTabSurfacePoint
} from '~/utils/admin/adminContributions'

/**
 * 扩展点治理：启用开关 / 顺序 / 恢复默认。
 *
 * 交互契约：点下去立刻在本地生效（乐观更新），请求失败整表回滚并把错误写在
 * 那一行上 —— 治理是行内操作，失败不该用一个全局 alert 抢走整页注意力。
 * 保存成功用 Toast（5s 自动消失）确认。
 */
export type UseAdminContributionGovernanceOptions = {
  contributions: Ref<AdminEffectiveContribution[]>
}

export function useAdminContributionGovernance(options: UseAdminContributionGovernanceOptions) {
  const { t } = useI18n()
  const toast = useToast()
  const { request } = useApiClient()

  const rowErrors = ref<Record<string, string>>({})
  const rowBusy = ref<Record<string, boolean>>({})

  function rowKey(item: AdminEffectiveContribution): string {
    return contributionRowKey(item.point, item.id)
  }

  function setRowError(key: string, message: string) {
    rowErrors.value = { ...rowErrors.value, [key]: message }
  }

  function clearRowError(key: string) {
    if (!rowErrors.value[key]) return
    const { [key]: _removed, ...rest } = rowErrors.value
    rowErrors.value = rest
  }

  function setRowBusy(key: string, busy: boolean) {
    rowBusy.value = { ...rowBusy.value, [key]: busy }
  }

  function pathFor(item: AdminEffectiveContribution): string {
    return `/admin/extensions/contributions/${encodeURIComponent(item.point)}/${encodeURIComponent(item.id)}`
  }

  function successToast(title: string) {
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title,
      duration: 5000
    })
  }

  function snapshot(): AdminEffectiveContribution[] {
    return options.contributions.value.map(item => ({ ...item }))
  }

  function replace(item: AdminEffectiveContribution, patch: Partial<AdminEffectiveContribution>) {
    options.contributions.value = options.contributions.value.map(current => (
      current.point === item.point && current.id === item.id ? { ...current, ...patch } : current
    ))
  }

  /** 启用/禁用。禁用承载 tab 的扩展点时先确认：它会让插件 tab 对所有人消失。 */
  async function setEnabled(item: AdminEffectiveContribution, enabled: boolean): Promise<boolean> {
    const key = rowKey(item)
    if (!enabled) {
      const message = isTabSurfacePoint(item.point)
        ? t('admin.extensions.contributions.governance.disableTabConfirm')
        : t('admin.extensions.contributions.governance.disableConfirm')
      if (!window.confirm(message)) return false
    }
    clearRowError(key)
    const previous = snapshot()
    replace(item, { enabled, overridden: true })
    setRowBusy(key, true)
    try {
      await request(pathFor(item), { method: 'PUT', body: { enabled } })
      successToast(enabled
        ? t('admin.extensions.contributions.governance.enabledToast')
        : t('admin.extensions.contributions.governance.disabledToast'))
      return true
    } catch (error) {
      options.contributions.value = previous
      setRowError(key, apiErrorMessage(error) || t('admin.extensions.contributions.governance.saveFailed'))
      return false
    } finally {
      setRowBusy(key, false)
    }
  }

  /** 上移 / 下移一格。计划只改动必要的项，逐项 PUT；任何一项失败整表回滚。 */
  async function move(item: AdminEffectiveContribution, direction: -1 | 1): Promise<boolean> {
    const key = rowKey(item)
    const plan = contributionMovePlan(options.contributions.value, item.point, item.id, direction)
    if (!plan.ok) return false
    clearRowError(key)
    const previous = snapshot()
    // plan 给的是 wire 值（sortOrder）；本地读模型仍写 effectiveSortOrder。
    const nextOrder = new Map(plan.updates.map(entry => [entry.id, entry.sortOrder]))
    options.contributions.value = options.contributions.value.map(current => (
      current.point === item.point && nextOrder.has(current.id)
        ? { ...current, effectiveSortOrder: nextOrder.get(current.id), overridden: true }
        : current
    ))
    setRowBusy(key, true)
    try {
      await Promise.all(plan.updates.map(update => request(
        `/admin/extensions/contributions/${encodeURIComponent(item.point)}/${encodeURIComponent(update.id)}`,
        { method: 'PUT', body: { sortOrder: update.sortOrder } }
      )))
      successToast(t('admin.extensions.contributions.governance.reorderedToast'))
      return true
    } catch (error) {
      options.contributions.value = previous
      setRowError(key, apiErrorMessage(error) || t('admin.extensions.contributions.governance.saveFailed'))
      return false
    } finally {
      setRowBusy(key, false)
    }
  }

  /** 恢复默认：删掉宿主侧覆盖，回到插件 manifest 的启用状态与顺序。 */
  async function resetDefault(item: AdminEffectiveContribution): Promise<boolean> {
    const key = rowKey(item)
    clearRowError(key)
    const previous = snapshot()
    replace(item, { enabled: true, overridden: false, effectiveSortOrder: item.order })
    setRowBusy(key, true)
    try {
      await request(pathFor(item), { method: 'DELETE' })
      successToast(t('admin.extensions.contributions.governance.resetToast'))
      return true
    } catch (error) {
      options.contributions.value = previous
      setRowError(key, apiErrorMessage(error) || t('admin.extensions.contributions.governance.saveFailed'))
      return false
    } finally {
      setRowBusy(key, false)
    }
  }

  return {
    rowErrors,
    rowBusy,
    rowKey,
    effectiveOrderOf,
    setEnabled,
    move,
    resetDefault
  }
}
