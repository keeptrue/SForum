import { ref, type Ref } from 'vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import type {
  AdminVersionGCCandidate,
  AdminVersionGCConfirmation,
  AdminVersionGCPolicy,
  AdminVersionGCSweepResult
} from '~/utils/admin/adminVersionGc'

type AdminExtensionVersionGcOptions = {
  isSuperAdmin: Ref<boolean>
  refresh: () => Promise<void> | void
}

/** Version GC 管理端入口：plan → confirmation → result 三段阶段机。 */
export function useAdminExtensionVersionGc(options: AdminExtensionVersionGcOptions) {
  const { isSuperAdmin, refresh } = options
  const { request } = useApiClient()
  const { t } = useI18n()
  const toast = useToast()

  const versionGcOpen = ref(false)
  const versionGcBusy = ref(false)
  const versionGcError = ref('')
  const versionGcCandidates = ref<AdminVersionGCCandidate[]>([])
  const versionGcPolicy = ref<AdminVersionGCPolicy>({ retain: 1, retainDays: 0 })
  const versionGcConfirmation = ref<AdminVersionGCConfirmation | null>(null)
  const versionGcResult = ref<AdminVersionGCSweepResult | null>(null)

  function versionGcQueryString() {
    const query = new URLSearchParams({
      retain: String(Math.max(0, Number(versionGcPolicy.value.retain) || 0)),
      retainDays: String(Math.max(0, Number(versionGcPolicy.value.retainDays) || 0))
    })
    return query.toString()
  }

  function resetVersionGcState() {
    versionGcError.value = ''
    versionGcCandidates.value = []
    versionGcPolicy.value = { retain: 1, retainDays: 0 }
    versionGcConfirmation.value = null
    versionGcResult.value = null
  }

  function openVersionGc() {
    if (!isSuperAdmin.value) return
    resetVersionGcState()
    versionGcOpen.value = true
    void refreshVersionGcPlan()
  }

  function cancelVersionGc() {
    if (versionGcBusy.value) return
    versionGcOpen.value = false
    resetVersionGcState()
  }

  async function refreshVersionGcPlan() {
    if (versionGcBusy.value) return
    versionGcBusy.value = true
    versionGcError.value = ''
    versionGcConfirmation.value = null
    versionGcResult.value = null
    try {
      const data = await request<{ candidates: AdminVersionGCCandidate[] }>(
        `/admin/extensions/version-gc/candidates?${versionGcQueryString()}`,
        { method: 'GET' }
      )
      versionGcCandidates.value = data.candidates
    } catch (error) {
      versionGcError.value = apiErrorMessage(error) || t('admin.extensions.actionFailed')
    } finally {
      versionGcBusy.value = false
    }
  }

  async function confirmVersionGcPlan() {
    if (versionGcBusy.value) return
    versionGcBusy.value = true
    versionGcError.value = ''
    try {
      versionGcConfirmation.value = await request<AdminVersionGCConfirmation>(
        '/admin/extensions/version-gc/confirmations',
        {
          method: 'POST',
          body: {
            retain: Math.max(0, Number(versionGcPolicy.value.retain) || 0),
            retainDays: Math.max(0, Number(versionGcPolicy.value.retainDays) || 0)
          }
        }
      )
    } catch (error) {
      versionGcError.value = apiErrorMessage(error) || t('admin.extensions.actionFailed')
    } finally {
      versionGcBusy.value = false
    }
  }

  async function executeVersionGc() {
    if (versionGcBusy.value || !versionGcConfirmation.value) return
    versionGcBusy.value = true
    versionGcError.value = ''
    try {
      const result = await request<AdminVersionGCSweepResult>('/admin/extensions/version-gc/execute', {
        method: 'POST',
        body: {
          retain: Math.max(0, Number(versionGcPolicy.value.retain) || 0),
          retainDays: Math.max(0, Number(versionGcPolicy.value.retainDays) || 0),
          token: versionGcConfirmation.value.token
        }
      })
      // 防御旧后端/缓存的 null 数组：JSON null 不满足数组契约时按空数组归一。
      versionGcResult.value = {
        ...result,
        removed: result.removed ?? [],
        skipped: result.skipped ?? [],
        failed: result.failed ?? [],
        warnings: result.warnings ?? []
      }
      await refresh()
      if ((result.failed ?? []).length > 0) {
        toast.add({
          color: 'error',
          icon: 'i-lucide-triangle-alert',
          title: t('admin.extensions.versionGc.failedSummary', { count: (result.failed ?? []).length }),
          duration: 0
        })
      } else {
        toast.add({
          color: 'success',
          icon: 'i-lucide-archive-x',
          title: t('admin.extensions.versionGc.success', { removed: (result.removed ?? []).length }),
          duration: 5000
        })
      }
    } catch (error) {
      versionGcError.value = apiErrorMessage(error) || t('admin.extensions.actionFailed')
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: versionGcError.value,
        duration: 0
      })
    } finally {
      versionGcBusy.value = false
    }
  }

  return {
    versionGcOpen,
    versionGcBusy,
    versionGcError,
    versionGcCandidates,
    versionGcPolicy,
    versionGcConfirmation,
    versionGcResult,
    openVersionGc,
    cancelVersionGc,
    refreshVersionGcPlan,
    confirmVersionGcPlan,
    executeVersionGc
  }
}
