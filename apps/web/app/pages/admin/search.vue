<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { apiErrorMessage, apiErrorStatusCode } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { createAdminForumApi, type ReindexRun, type ReindexStatus } from '~/utils/admin/adminForum'
import { reindexHistoryLoadKind, reindexStatusBadgeVariant, reindexStatusLoadKind } from '~/utils/admin/adminSearch'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminSearch'
})

const { format: formatSiteDateTime } = useSiteDateTime()
const { t } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const adminPage = useAdminPage('/search')
const adminRoutes = useAdminRoutes()

const api = createAdminForumApi(request)

// 当前重建进度。null 表示尚无记录。
const currentStatus = ref<ReindexStatus | null>(null)
// 历史 run 列表。
const runs = ref<ReindexRun[]>([])
// 触发重建中的 loading。
const rebuilding = ref(false)
// 刷新中的 loading。
const refreshing = ref(false)
// 确认弹窗。
const confirmOpen = ref(false)

// UX2-05：错误必须可分。403 与 500 都不能再退化成「从未重建过」。
const statusError = ref('')
const statusForbidden = ref(false)
const historyError = ref('')
const historyForbidden = ref(false)

const isRunning = computed(() => currentStatus.value?.status === 'running')

// 拉取当前进度。404 是「尚无 run」的空态，其它状态码是错误。
async function refreshStatus() {
  try {
    currentStatus.value = await api.getReindexStatus()
    statusError.value = ''
    statusForbidden.value = false
  } catch (error) {
    currentStatus.value = null
    const kind = reindexStatusLoadKind(apiErrorStatusCode(error))
    statusForbidden.value = kind === 'forbidden'
    statusError.value = kind === 'empty' || kind === 'ok'
      ? ''
      : kind === 'forbidden'
        ? t('admin.search.forbidden')
        : apiErrorMessage(error) || t('admin.search.loadFailed')
  }
}

// 拉取历史。失败不再静默：错误态 + 重试入口。
async function refreshHistory() {
  try {
    runs.value = await api.listReindexRuns()
    historyError.value = ''
    historyForbidden.value = false
  } catch (error) {
    runs.value = []
    const kind = reindexHistoryLoadKind(apiErrorStatusCode(error))
    historyForbidden.value = kind === 'forbidden'
    historyError.value = kind === 'forbidden'
      ? t('admin.search.forbidden')
      : apiErrorMessage(error) || t('admin.search.historyLoadFailed')
  }
}

async function refresh() {
  refreshing.value = true
  try {
    await Promise.all([refreshStatus(), refreshHistory()])
  } finally {
    refreshing.value = false
  }
}

// 触发重建。
async function triggerReindex() {
  rebuilding.value = true
  try {
    await api.reindexSearch()
    toast.add({ color: 'success', title: t('admin.search.toast.started'), icon: 'i-lucide-check' })
    await refresh()
    startPolling()
  } catch (error) {
    const msg = apiErrorMessage(error)
    if (msg?.includes('reindex_running')) {
      toast.add({ color: 'warning', title: t('admin.search.toast.alreadyRunning'), icon: 'i-lucide-alert-triangle' })
    } else {
      toast.add({ color: 'error', duration: 0, title: msg || t('admin.search.toast.failed'), icon: 'i-lucide-x' })
    }
  } finally {
    rebuilding.value = false
  }
}

// 轮询：running 时每 2s 刷新进度；完成/失败时停止。
let pollTimer: ReturnType<typeof setInterval> | null = null
function startPolling() {
  if (pollTimer || !import.meta.client) return
  pollTimer = setInterval(async () => {
    // 每次 tick 都取「状态 + 历史」：只刷状态会让刚完成的重建停在旧历史表上
    // （用户看到「已完成」toast，表格里却还是上一轮的记录）。
    await refresh()
    if (currentStatus.value && currentStatus.value.status !== 'running') {
      stopPolling()
      // 完成时给一次 toast。
      if (currentStatus.value.status === 'completed') {
        toast.add({ color: 'success', title: t('admin.search.toast.completed'), icon: 'i-lucide-check' })
      }
    }
  }, 2000)
}
function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

// 状态徽标语义（UX2-06：`SFBadge` 只认 `variant`，`error` 不是合法取值）。
function statusVariant(status: string) {
  return reindexStatusBadgeVariant(status)
}

// 按站点时区与日期时间格式展示。
function formatTime(value?: string | null): string {
  if (!value) return '-'
  return formatSiteDateTime(value) || '-'
}

// 初始化。
//
// SSR 期间 `refresh()` 写入的是**组件 ref**，而 ref 不参与 payload 序列化：水合后
// 组件是新的实例，`useAsyncData` 命中 payload 也不会重跑回调 —— 结果是 F5 后页面
// 恒显示「暂无重建记录」，必须手动点刷新才出现数据。因此把首屏结果一并放进 payload，
// 水合时再写回 ref（服务端执行时是同一个值，等价于 no-op）。
const { data: bootstrap } = await useAsyncData('admin-search-init', async () => {
  await refresh()
  return {
    status: currentStatus.value,
    runs: runs.value,
    statusError: statusError.value,
    statusForbidden: statusForbidden.value,
    historyError: historyError.value,
    historyForbidden: historyForbidden.value
  }
})

if (bootstrap.value) {
  currentStatus.value = bootstrap.value.status
  runs.value = bootstrap.value.runs
  statusError.value = bootstrap.value.statusError
  statusForbidden.value = bootstrap.value.statusForbidden
  historyError.value = bootstrap.value.historyError
  historyForbidden.value = bootstrap.value.historyForbidden
}

onMounted(() => {
  if (isRunning.value) startPolling()
})
onBeforeUnmount(stopPolling)

watch(isRunning, (running) => {
  if (running) startPolling()
  else stopPolling()
})
</script>

<template>
  <div data-testid="admin-search" class="min-w-0 w-full">
    <!-- UX2-23：后台统一几何 —— 紧凑标题 + registry icon，简介只出现在工具栏。 -->
    <header class="mb-4">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.search.title') }}
      </h2>
    </header>

    <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <div class="flex min-w-0 items-center gap-2 text-sm">
          <UIcon name="i-lucide-database" class="size-4" />
          <span class="truncate">{{ t('admin.search.description') }}</span>
        </div>
      </template>
      <template #right>
        <UButton
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-refresh-cw"
          :loading="refreshing"
          @click="refresh()"
        >
          {{ t('admin.common.refresh') }}
        </UButton>
      </template>
    </UDashboardToolbar>

    <div class="flex w-full min-w-0 flex-col gap-4">
      <UAlert
        v-if="statusError"
        data-testid="admin-search-status-error"
        color="error"
        variant="soft"
        icon="i-lucide-triangle-alert"
        :title="statusError"
        :description="statusForbidden ? t('admin.search.forbiddenHint') : undefined"
      >
        <template v-if="!statusForbidden" #actions>
          <UButton size="sm" color="neutral" variant="outline" icon="i-lucide-rotate-cw" @click="refreshStatus">
            {{ t('admin.common.refresh') }}
          </UButton>
        </template>
      </UAlert>

      <UAlert
        v-if="historyError"
        data-testid="admin-search-history-error"
        color="error"
        variant="soft"
        icon="i-lucide-triangle-alert"
        :title="historyError"
        :description="historyForbidden ? t('admin.search.forbiddenHint') : undefined"
      >
        <template v-if="!historyForbidden" #actions>
          <UButton size="sm" color="neutral" variant="outline" icon="i-lucide-rotate-cw" @click="refreshHistory">
            {{ t('admin.common.refresh') }}
          </UButton>
        </template>
      </UAlert>

      <SFCard>
        <template #header>
          <div class="flex items-center gap-2">
            <UIcon name="i-lucide-activity" class="size-5 text-primary-500" />
            <h2 class="text-base font-semibold text-slate-800 dark:text-slate-100">
              {{ t('admin.search.current.title') }}
            </h2>
          </div>
        </template>
        <p class="text-xs text-slate-500 dark:text-slate-400">
          {{ t('admin.search.providerHint') }}
          <NuxtLink
            :to="adminRoutes.path('/forum/settings') + '?tab=search'"
            class="ml-1 font-medium text-[var(--sf-accent)] hover:underline"
          >
            {{ t('admin.search.providerLink') }}
          </NuxtLink>
        </p>

        <!-- 当前重建进度卡片 -->
        <div v-if="currentStatus" class="mt-5 rounded-lg border border-slate-200 p-4 dark:border-slate-700">
          <div class="mb-3 flex items-center justify-end">
            <SFBadge :variant="statusVariant(currentStatus.status)">
              {{ t(`admin.search.current.status.${currentStatus.status}`) }}
            </SFBadge>
          </div>
          <SFProgress :value="currentStatus.percent" :label="t('admin.search.current.progressLabel')" />
          <div class="mt-3 grid grid-cols-3 gap-4 text-center">
            <div>
              <div class="text-xs text-slate-400">{{ t('admin.search.current.processed') }}</div>
              <div class="text-lg font-semibold text-slate-700 dark:text-slate-200">{{ currentStatus.processed }}</div>
            </div>
            <div>
              <div class="text-xs text-slate-400">{{ t('admin.search.current.remaining') }}</div>
              <div class="text-lg font-semibold text-slate-700 dark:text-slate-200">{{ currentStatus.remaining }}</div>
            </div>
            <div>
              <div class="text-xs text-slate-400">{{ t('admin.search.current.total') }}</div>
              <div class="text-lg font-semibold text-slate-700 dark:text-slate-200">{{ currentStatus.total }}</div>
            </div>
          </div>
          <div class="mt-3 flex justify-between text-xs text-slate-400">
            <span>{{ t('admin.search.current.startedAt') }}: {{ formatTime(currentStatus.startedAt) }}</span>
            <span v-if="currentStatus.finishedAt">{{ t('admin.search.current.finishedAt') }}: {{ formatTime(currentStatus.finishedAt) }}</span>
          </div>
          <p v-if="currentStatus.error" class="mt-2 text-xs text-red-500">{{ currentStatus.error }}</p>
        </div>
        <!-- 404 空态：从未重建过，与「加载失败」明确区分。 -->
        <p v-else-if="!statusError" data-testid="admin-search-status-empty" class="mt-5 text-sm text-slate-500 dark:text-slate-400">
          {{ t('admin.search.current.empty') }}
        </p>

        <!-- 重建按钮 -->
        <div class="mt-5 flex justify-end">
          <SFButton
            :loading="rebuilding || isRunning"
            :disabled="rebuilding || isRunning"
            @click="confirmOpen = true"
          >
            <template #leading>
              <UIcon name="i-lucide-refresh-cw" class="size-4" />
            </template>
            {{ isRunning ? t('admin.search.action.reindexing') : t('admin.search.action.reindex') }}
          </SFButton>
        </div>
      </SFCard>

      <!-- 历史记录 -->
      <SFCard>
        <template #header>
          <div class="flex items-center gap-2">
            <UIcon name="i-lucide-history" class="size-5 text-slate-500" />
            <h2 class="text-base font-semibold text-slate-800 dark:text-slate-100">
              {{ t('admin.search.history.title') }}
            </h2>
          </div>
        </template>
        <div
          v-if="runs.length === 0 && !historyError"
          data-testid="admin-search-history-empty"
          class="py-6 text-center text-sm text-slate-400"
        >
          {{ t('admin.search.history.empty') }}
        </div>
        <!-- UX2-18：5 列在 390px 会撑破页面，横向滚动容器 + 最小宽度是唯一解。 -->
        <div
          v-else
          data-testid="admin-search-history-scroll"
          class="w-full overflow-x-auto"
          tabindex="0"
          role="region"
          :aria-label="t('admin.search.history.scrollLabel')"
        >
          <table class="w-full min-w-[640px] text-sm">
            <thead>
              <tr class="border-b border-slate-200 text-left text-xs text-slate-400 dark:border-slate-700">
                <th class="py-2 pr-3">{{ t('admin.search.history.columns.id') }}</th>
                <th class="py-2 pr-3">{{ t('admin.search.history.columns.status') }}</th>
                <th class="py-2 pr-3">{{ t('admin.search.history.columns.total') }}</th>
                <th class="py-2 pr-3">{{ t('admin.search.history.columns.startedAt') }}</th>
                <th class="py-2">{{ t('admin.search.history.columns.finishedAt') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="run in runs" :key="run.id" class="border-b border-slate-100 dark:border-slate-800">
                <td class="py-2 pr-3 text-slate-500">#{{ run.id }}</td>
                <td class="py-2 pr-3"><SFBadge :variant="statusVariant(run.status)">{{ t(`admin.search.current.status.${run.status}`) }}</SFBadge></td>
                <td class="py-2 pr-3 text-slate-600 dark:text-slate-300">{{ run.total }}</td>
                <td class="py-2 pr-3 text-slate-500">{{ formatTime(run.startedAt) }}</td>
                <td class="py-2 text-slate-500">{{ formatTime(run.finishedAt) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </SFCard>
    </div>

    <!-- 确认弹窗 -->
    <UModal v-model:open="confirmOpen">
      <template #content>
        <div class="space-y-4 p-6">
          <div class="flex items-start gap-3">
            <UIcon name="i-lucide-alert-triangle" class="mt-0.5 size-5 text-amber-500" />
            <div>
              <h3 class="text-base font-semibold text-slate-800 dark:text-slate-100">{{ t('admin.search.action.confirmTitle') }}</h3>
              <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">{{ t('admin.search.action.confirmDescription') }}</p>
            </div>
          </div>
          <div class="flex justify-end gap-2">
            <SFButton variant="ghost" @click="confirmOpen = false">{{ t('admin.search.action.cancel') }}</SFButton>
            <SFButton @click="confirmOpen = false; triggerReindex()">{{ t('admin.search.action.confirm') }}</SFButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>
