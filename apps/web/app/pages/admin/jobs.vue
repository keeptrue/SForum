<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAdminJobs } from '~/composables/admin/useAdminJobs'
import { ALL_ADMIN_JOBS_FILTER, jobCanCancel, jobCanRetry, jobStateColor } from '~/utils/admin/adminJobs'
import { announceAsyncResult } from '~/utils/a11y/asyncAnnounce'

definePageMeta({ middleware: 'admin', layout: 'admin' })
defineOptions({ name: 'AdminJobs' })
const { t } = useI18n()
const adminPage = useAdminPage('/jobs')
const adminRoutes = useAdminRoutes()
const { can } = useAuthSession()
const manager = useAdminJobs()
const canManage = computed(() => can('jobs.manage'))
const counts = computed(() => manager.overview.data.value.counts)
const stateOptions = computed(() => [ALL_ADMIN_JOBS_FILTER, 'available', 'running', 'retryable', 'scheduled', 'completed', 'discarded', 'cancelled'].map(value => ({ label: value === ALL_ADMIN_JOBS_FILTER ? t('admin.jobs.allStates') : value, value })))
const queueOptions = computed(() => [{ label: t('admin.jobs.allQueues'), value: ALL_ADMIN_JOBS_FILTER }, ...manager.overview.data.value.queues.map(item => ({ label: item.name, value: item.name }))])
function closeDetail() { manager.selected.value = null }
// UX2-17：任务列表是异步面。三件事必须同时成立才播报成功：
// pending 已结束、没有 error（失败时 useAsyncData 会保留 default 的空数组）、在客户端。
// `immediate` 负责首屏（SSR 后 pending 可能已经落位，没有它首屏永远不播）。
watch([
  () => manager.jobs.pending.value,
  () => manager.jobs.error.value,
  () => manager.jobs.data.value.length
], () => {
  if (import.meta.server) return
  announceAsyncResult({
    pending: manager.jobs.pending.value,
    failed: Boolean(manager.jobs.error.value),
    success: t('admin.jobs.listLoaded', { count: manager.jobs.data.value.length }),
    failure: t('admin.jobs.listLoadFailed')
  })
}, { immediate: true })
</script>

<template>
  <div class="mb-5 flex flex-wrap items-start justify-between gap-3">
    <!-- UX2-22：页头只保留紧凑标题；简介的唯一落点是下面的工具栏（对齐 settings/index.vue 几何基准）。 -->
    <div><h2 class="flex items-center gap-2 text-xl font-bold"><UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)]" />{{ t('admin.jobs.title') }}</h2></div>
  </div>
  <UDashboardToolbar class="mb-5 rounded-lg border border-slate-200 bg-white px-4 py-2.5 dark:border-zinc-800 dark:bg-zinc-900"><template #left><span class="text-sm text-slate-500">{{ t('admin.jobs.intro') }}</span></template><template #right><UButton icon="i-lucide-rotate-cw" color="neutral" variant="subtle" :loading="manager.jobs.pending.value" @click="manager.refresh">{{ t('admin.extensions.refresh') }}</UButton></template></UDashboardToolbar>

  <div class="mb-5 grid grid-cols-2 gap-3 lg:grid-cols-4">
    <div v-for="state in ['available', 'running', 'retryable', 'discarded']" :key="state" class="border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900"><p class="text-xs uppercase text-slate-500">{{ state }}</p><p class="mt-2 text-2xl font-semibold tabular-nums">{{ counts[state] || 0 }}</p></div>
  </div>

  <section class="mb-5 border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
    <div class="border-b border-slate-200 px-4 py-3 text-sm font-semibold dark:border-zinc-800">{{ t('admin.jobs.queues') }}</div>
    <div class="divide-y divide-slate-200 dark:divide-zinc-800"><div v-for="queue in manager.overview.data.value.queues" :key="queue.name" class="flex flex-wrap items-center justify-between gap-3 px-4 py-3"><div><p class="font-mono text-sm font-medium">{{ queue.name }}</p><p class="mt-1 text-xs text-slate-500">{{ queue.available }} waiting · {{ queue.running }} running · {{ queue.failed }} failed</p></div><UButton v-if="canManage" size="xs" :icon="queue.pausedAt ? 'i-lucide-play' : 'i-lucide-pause'" color="neutral" variant="subtle" :loading="manager.busy.value === `queue:${queue.name}`" @click="manager.queueAction(queue.name, !queue.pausedAt)">{{ queue.pausedAt ? t('admin.jobs.resume') : t('admin.jobs.pause') }}</UButton></div></div>
  </section>

  <section class="mb-5 flex flex-wrap items-center justify-between gap-3 border border-slate-200 bg-white px-4 py-3 dark:border-zinc-800 dark:bg-zinc-900">
    <div>
      <p class="text-sm font-semibold">{{ t('admin.jobs.schedules') }}</p>
      <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.jobs.schedulesMoved') }}</p>
    </div>
    <UButton :to="adminRoutes.path('/schedules')" icon="i-lucide-calendar-clock" color="neutral" variant="subtle" size="sm">
      {{ t('admin.jobs.openSchedules') }}
    </UButton>
  </section>

  <div class="mb-3 grid gap-2 sm:grid-cols-3"><UInput v-model="manager.filters.kind" icon="i-lucide-search" :placeholder="t('admin.jobs.filterKind')" :aria-label="t('admin.jobs.filterKind')" /><USelect v-model="manager.filters.state" :items="stateOptions" :aria-label="t('admin.jobs.filterStateAria')" /><USelect v-model="manager.filters.queue" :items="queueOptions" :aria-label="t('admin.jobs.filterQueueAria')" /></div>
  <div class="overflow-x-auto border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900"><table class="min-w-full text-left text-sm"><thead class="bg-slate-50 text-xs text-slate-500 dark:bg-zinc-950"><tr><th class="px-3 py-3">ID</th><th class="px-3 py-3">Kind / Queue</th><th class="px-3 py-3">State</th><th class="px-3 py-3">Attempts</th><th class="px-3 py-3 text-right">{{ t('admin.jobs.actions') }}</th></tr></thead><tbody class="divide-y divide-slate-200 dark:divide-zinc-800"><tr v-for="job in manager.jobs.data.value" :key="job.id"><td class="px-3 py-3 font-mono">#{{ job.id }}</td><td class="px-3 py-3"><p class="font-medium">{{ job.kind }}</p><p class="text-xs text-slate-500">{{ job.queue }}</p></td><td class="px-3 py-3"><UBadge :color="jobStateColor(job.state)" variant="subtle">{{ job.state }}</UBadge></td><td class="px-3 py-3 tabular-nums">{{ job.attempt }}/{{ job.maxAttempts }}</td><td class="px-3 py-3"><div class="flex justify-end gap-1"><UButton size="xs" icon="i-lucide-eye" color="neutral" variant="ghost" :aria-label="t('admin.jobs.detail')" :title="t('admin.jobs.detail')" @click="manager.detail(job.id)" /><UButton v-if="canManage && jobCanRetry(job.state)" size="xs" icon="i-lucide-refresh-cw" :aria-label="t('admin.jobs.retry')" :title="t('admin.jobs.retry')" :loading="manager.busy.value === `${job.id}:retry`" @click="manager.jobAction(job.id, 'retry')" /><UButton v-if="canManage && jobCanCancel(job.state)" size="xs" icon="i-lucide-ban" color="error" variant="ghost" :aria-label="t('admin.jobs.cancel')" :title="t('admin.jobs.cancel')" :loading="manager.busy.value === `${job.id}:cancel`" @click="manager.jobAction(job.id, 'cancel')" /></div></td></tr></tbody></table><div v-if="!manager.jobs.data.value.length && !manager.jobs.pending.value" class="p-10 text-center text-sm text-slate-500">{{ t('admin.jobs.empty') }}</div></div>

  <UModal :open="Boolean(manager.selected.value)" @update:open="value => { if (!value) closeDetail() }"><template #content><div v-if="manager.selected.value" class="max-h-[85vh] overflow-y-auto p-5"><div class="flex items-center justify-between"><h3 class="font-semibold">Job #{{ manager.selected.value.id }}</h3><UButton icon="i-lucide-x" color="neutral" variant="ghost" :aria-label="t('admin.jobs.closeDetail')" :title="t('admin.jobs.closeDetail')" @click="closeDetail" /></div><dl class="mt-4 grid gap-3 text-sm sm:grid-cols-2"><div><dt class="text-slate-500">Kind</dt><dd>{{ manager.selected.value.kind }}</dd></div><div><dt class="text-slate-500">Queue</dt><dd>{{ manager.selected.value.queue }}</dd></div></dl><pre class="mt-4 overflow-auto bg-zinc-950 p-3 text-xs text-zinc-200">{{ JSON.stringify(manager.selected.value.args, null, 2) }}</pre></div></template></UModal>
</template>
