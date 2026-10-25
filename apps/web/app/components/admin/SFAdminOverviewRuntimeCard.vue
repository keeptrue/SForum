<script setup lang="ts">
import {
  formatOverviewBytes,
  formatOverviewCommit,
  formatOverviewCount,
  formatOverviewDate,
  formatOverviewUptime,
  type AdminOverviewRuntime
} from '~/utils/admin/adminOverview'

const props = defineProps<{
  runtime: AdminOverviewRuntime
}>()

const { t } = useI18n()
type RuntimeRow = {
  label: string
  value: string
  icon: string
  title?: string
  tone?: string
}

const rows = computed<RuntimeRow[]>(() => {
  const runtime = props.runtime
  const worker = runtime.worker
  const workerValue = !worker
    ? t('admin.home.runtime.workerUnavailable')
    : worker.status === 'ok'
      ? t('admin.home.runtime.workerOk', { age: formatOverviewCount(worker.ageSeconds ?? 0) })
      : worker.status === 'stale'
        ? t('admin.home.runtime.workerStale', { age: formatOverviewCount(worker.ageSeconds ?? 0) })
        : t('admin.home.runtime.workerUnknown')
  const lag = runtime.queueLag
  const lagValue = !lag
    ? t('admin.home.runtime.queueLagUnavailable')
    : t('admin.home.runtime.queueLagValue', {
        waiting: formatOverviewCount(lag.waiting),
        running: formatOverviewCount(lag.running),
        failed: formatOverviewCount(lag.failed)
      })

  return [
    {
      label: t('admin.home.runtime.commit'),
      value: runtime.build.commit
        ? `${formatOverviewCommit(runtime.build.commit)}${runtime.build.dirty ? ` · ${t('admin.home.runtime.dirty')}` : ''}`
        : t('admin.home.runtime.unavailable'),
      title: runtime.build.commit || undefined,
      icon: 'i-lucide-git-commit-horizontal'
    },
    {
      label: t('admin.home.runtime.builtAt'),
      value: runtime.build.builtAt
        ? (formatOverviewDate(runtime.build.builtAt) || runtime.build.builtAt)
        : t('admin.home.runtime.unavailable'),
      title: runtime.build.builtAt || undefined,
      icon: 'i-lucide-calendar-clock'
    },
    {
      label: t('admin.home.runtime.goVersion'),
      value: runtime.build.goVersion,
      title: runtime.build.goVersion,
      icon: 'i-lucide-code-xml'
    },
    {
      label: t('admin.home.runtime.arch'),
      value: runtime.build.platform || t('admin.home.runtime.unavailable'),
      title: runtime.build.platform || undefined,
      icon: 'i-lucide-monitor-cog'
    },
    {
      label: t('admin.home.runtime.uptime'),
      value: formatOverviewUptime(runtime.uptimeSeconds),
      icon: 'i-lucide-clock-3'
    },
    {
      label: t('admin.home.runtime.worker'),
      value: workerValue,
      icon: worker?.stale === false ? 'i-lucide-heart-pulse' : 'i-lucide-heart-off',
      tone: worker?.status === 'ok'
        ? 'text-emerald-600 dark:text-emerald-400'
        : worker?.status === 'stale'
          ? 'text-amber-600 dark:text-amber-400'
          : 'text-slate-500'
    },
    {
      label: t('admin.home.runtime.queueLag'),
      value: lagValue,
      icon: 'i-lucide-layers'
    },
    {
      label: t('admin.home.runtime.goroutines'),
      value: formatOverviewCount(runtime.goroutineCount),
      icon: 'i-lucide-git-branch'
    },
    {
      label: t('admin.home.runtime.gc'),
      value: formatOverviewCount(runtime.gcCount),
      icon: 'i-lucide-repeat-2'
    },
    {
      label: t('admin.home.runtime.goSys'),
      value: formatOverviewBytes(runtime.sysBytes ?? 0),
      icon: 'i-lucide-cpu'
    },
    {
      label: t('admin.home.runtime.database'),
      value: `${formatOverviewCount(runtime.database.acquiredConnections)} / ${formatOverviewCount(runtime.database.maxConnections)}`,
      icon: 'i-lucide-database'
    }
  ]
})
</script>

<template>
  <UCard class="elegant-card border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100">
    <template #header>
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('admin.home.runtime.title') }}
          </h3>
          <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.home.runtime.description') }}
          </p>
        </div>
        <div class="flex shrink-0 items-center gap-1.5">
          <UButton
            :to="runtime.build.sourceUrl"
            target="_blank"
            rel="noopener noreferrer"
            icon="i-lucide-github"
            color="neutral"
            variant="ghost"
            size="sm"
            :aria-label="t('admin.home.runtime.source')"
            :title="t('admin.home.runtime.source')"
          />
        </div>
      </div>
    </template>

    <div class="space-y-2.5">
      <div v-for="row in rows" :key="row.label" class="flex items-center gap-3 rounded-md border border-slate-100 px-3 py-2.5 dark:border-zinc-800">
        <span class="grid size-8 shrink-0 place-items-center rounded-md bg-slate-100 text-slate-600 dark:bg-zinc-800 dark:text-zinc-300">
          <UIcon :name="row.icon" class="size-4" />
        </span>
        <span class="min-w-0 flex-1">
          <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">{{ row.label }}</span>
          <span
            class="block truncate text-sm font-bold"
            :class="row.tone || 'text-slate-900 dark:text-white'"
            :title="row.title"
          >{{ row.value }}</span>
        </span>
      </div>
    </div>
  </UCard>
</template>
