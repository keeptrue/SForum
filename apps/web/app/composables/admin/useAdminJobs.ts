import { ALL_ADMIN_JOBS_FILTER, adminJobFilterValue, type AdminJob, type AdminJobSchedule, type AdminJobScheduleTriggerResult, type AdminJobsOverview } from '~/utils/admin/adminJobs'
import { apiErrorMessage } from '~/composables/useApiClient'

export function useAdminJobs() {
  const { request } = useApiClient()
  const toast = useToast()
  const { t } = useI18n()
  const filters = reactive({ queue: ALL_ADMIN_JOBS_FILTER, state: ALL_ADMIN_JOBS_FILTER, kind: '' })
  const selected = ref<AdminJob | null>(null)
  const busy = ref('')
  const overview = useAsyncData<AdminJobsOverview>('admin-jobs-overview', () => request('/admin/jobs/overview'), { default: () => ({ counts: {}, queues: [] }) })
  const schedules = useAsyncData<AdminJobSchedule[]>('admin-jobs-schedules', () => request('/admin/jobs/schedules'), { default: () => [] })
  const jobs = useAsyncData<AdminJob[]>('admin-jobs-list', () => request(`/admin/jobs?limit=100&queue=${encodeURIComponent(adminJobFilterValue(filters.queue))}&state=${encodeURIComponent(adminJobFilterValue(filters.state))}&kind=${encodeURIComponent(filters.kind)}`), { default: () => [] })

  async function refresh() {
    await Promise.all([overview.refresh(), schedules.refresh(), jobs.refresh()])
  }

  async function refreshSchedules() {
    await schedules.refresh()
  }

  async function detail(id: number) {
    selected.value = await request(`/admin/jobs/${id}`)
  }

  async function jobAction(id: number, action: 'retry' | 'cancel') {
    busy.value = `${id}:${action}`
    try {
      await request(`/admin/jobs/${id}/${action}`, { method: 'POST', body: {} })
      toast.add({ color: 'success', icon: action === 'retry' ? 'i-lucide-refresh-cw' : 'i-lucide-ban', title: t(`admin.jobs.${action}Success`), duration: 5000 })
      await refresh()
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.jobs.actionFailed'), duration: 0 })
    } finally {
      busy.value = ''
    }
  }

  async function queueAction(name: string, paused: boolean) {
    busy.value = `queue:${name}`
    try {
      await request(`/admin/jobs/queues/${encodeURIComponent(name)}/${paused ? 'pause' : 'resume'}`, { method: 'POST', body: {} })
      toast.add({ color: 'success', icon: paused ? 'i-lucide-pause' : 'i-lucide-play', title: t(paused ? 'admin.jobs.pauseSuccess' : 'admin.jobs.resumeSuccess'), duration: 5000 })
      await refresh()
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.jobs.actionFailed'), duration: 0 })
    } finally {
      busy.value = ''
    }
  }

  async function setScheduleEnabled(id: string, enabled: boolean) {
    busy.value = `schedule:${id}:${enabled ? 'enable' : 'disable'}`
    try {
      await request(`/admin/jobs/schedules/${encodeURIComponent(id)}/${enabled ? 'enable' : 'disable'}`, { method: 'POST', body: {} })
      toast.add({
        color: 'success',
        icon: enabled ? 'i-lucide-play' : 'i-lucide-pause',
        title: t(enabled ? 'admin.schedules.enableSuccess' : 'admin.schedules.disableSuccess'),
        duration: 5000
      })
      await refreshSchedules()
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.jobs.actionFailed'), duration: 0 })
    } finally {
      busy.value = ''
    }
  }

  async function triggerSchedule(id: string) {
    busy.value = `schedule:${id}:trigger`
    try {
      const result = await request<AdminJobScheduleTriggerResult>(`/admin/jobs/schedules/${encodeURIComponent(id)}/trigger`, { method: 'POST', body: {} })
      toast.add({
        color: 'success',
        icon: 'i-lucide-zap',
        title: t('admin.schedules.triggerSuccess', { id: result.jobId }),
        duration: 5000
      })
      await Promise.all([refreshSchedules(), jobs.refresh()])
      return result
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.jobs.actionFailed'), duration: 0 })
      return null
    } finally {
      busy.value = ''
    }
  }

  watch(filters, () => { void jobs.refresh() })
  return {
    overview,
    schedules,
    jobs,
    filters,
    selected,
    busy,
    refresh,
    refreshSchedules,
    detail,
    jobAction,
    queueAction,
    setScheduleEnabled,
    triggerSchedule
  }
}
