<script setup lang="ts">
import { useModerationApi } from '~/composables/moderation/useModerationApi'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { apiErrorMessage, apiErrorStatusCode } from '~/composables/useApiClient'
import ModerationDecisionTable from '~/components/moderation/ModerationDecisionTable.vue'
import ModerationSettingsForm from '~/components/moderation/ModerationSettingsForm.vue'
import type { ModerationDecision, ModerationReport, ModerationSettings } from '~/composables/moderation/useModerationApi'

definePageMeta({ middleware: 'admin', layout: 'admin' })
defineOptions({ name: 'AdminModeration' })

const { t } = useI18n()
const adminPage = useAdminPage('/moderation')
const moderationApi = useModerationApi()

const settingsModalOpen = ref(false)

const { data: settings, pending: settingsPending, refresh: refreshSettings, error: settingsError } = await useAsyncData(
  'admin-moderation-settings',
  () => moderationApi.getSettings()
)
// UX2-20：审核记录不能再固定截断在最近 30 条 —— 页码进状态，total 参与分页。
const historyPage = ref(1)
const historyPerPage = 30
const { data: history, pending: historyPending, refresh: refreshHistory, error: historyError } = await useAsyncData(
  'admin-moderation-history',
  () => moderationApi.listHistory({ page: historyPage.value, perPage: historyPerPage }, true),
  {
    default: () => ({ items: [] as ModerationDecision[], total: 0, page: 1, perPage: historyPerPage }),
    watch: [historyPage]
  }
)
// NF-02：待处理举报的乐观锁。每行带回自己的 revision，更新时原样回传；
// 409 说明别人先改了（或已结案），必须给可见反馈并刷新，而不是静默覆盖。
const reportsPage = ref(1)
const reportsPerPage = 10
const { data: reports, pending: reportsPending, refresh: refreshReports } = await useAsyncData(
  'admin-moderation-reports',
  () => moderationApi.listReports({ status: 'open', page: reportsPage.value, perPage: reportsPerPage }),
  {
    default: () => ({ items: [] as ModerationReport[], total: 0, page: 1, perPage: reportsPerPage }),
    watch: [reportsPage]
  }
)
const reportsTotalPages = computed(() => Math.max(1, Math.ceil((reports.value?.total ?? 0) / reportsPerPage)))
const reportNote = ref('')
const reportBusyId = ref<number | null>(null)
const reportFeedback = ref('')

async function decideReport(row: ModerationReport, status: 'resolved' | 'rejected') {
  reportBusyId.value = row.id
  reportFeedback.value = ''
  try {
    await moderationApi.updateReport(row.id, {
      status,
      reviewNote: reportNote.value.trim() || undefined,
      expectedRevision: row.revision
    })
    reportNote.value = ''
    await Promise.all([refreshReports(), refreshHistory()])
  } catch (error) {
    reportFeedback.value = apiErrorMessage(error) || t('admin.moderation.reports.conflict')
    if (apiErrorStatusCode(error) === 409) await refreshReports()
  } finally {
    reportBusyId.value = null
  }
}

const historyTotal = computed(() => history.value?.total ?? 0)
const historyTotalPages = computed(() => Math.max(1, Math.ceil(historyTotal.value / historyPerPage)))

// UX2-04：弹窗三态。加载中给骨架；403 说明缺权限；其余给错误 + 重试。
const settingsForbidden = computed(() => apiErrorStatusCode(settingsError.value) === 403)
const settingsModalMessage = computed(() => {
  if (!settingsError.value) return ''
  if (settingsForbidden.value) return t('admin.moderation.settingsForbidden')
  return apiErrorMessage(settingsError.value) || t('admin.moderation.settingsLoadFailed')
})

const loadError = computed(() => {
  const err = settingsError.value || historyError.value
  if (!err) return ''
  return typeof err === 'object' && err !== null && 'message' in err
    ? String((err as { message?: unknown }).message || '')
    : String(err)
})

const currentModeLabel = computed(() => {
  const mode = settings.value?.mode
  if (!mode) return ''
  return t(`admin.moderation.mode.${mode}`)
})

function settingsUpdated(value: ModerationSettings) {
  settings.value = value
  void refreshHistory()
}

function openSettingsModal() {
  settingsModalOpen.value = true
}

function closeSettingsModal() {
  settingsModalOpen.value = false
}

async function refreshAll() {
  await Promise.all([refreshSettings(), refreshHistory()])
}

useSeoMeta({
  title: t('admin.moderation.managementTitle')
})
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.moderation.managementTitle') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.moderation.managementDescription') }}
    </p>
  </div>

  <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 flex-wrap items-center gap-2 text-sm">
        <UIcon name="i-lucide-shield-check" class="size-4" />
        <span class="truncate">{{ t('admin.moderation.toolbar') }}</span>
        <UBadge v-if="settings" color="primary" variant="soft">
          {{ t('admin.moderation.currentMode', { mode: currentModeLabel }) }}
        </UBadge>
      </div>
    </template>
    <template #right>
      <div class="flex flex-wrap items-center gap-2">
        <UButton
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-refresh-cw"
          :loading="settingsPending || historyPending"
          class="border-slate-200 dark:border-zinc-700"
          @click="refreshAll()"
        >
          {{ t('admin.common.refresh') }}
        </UButton>
        <UButton
          leading-icon="i-lucide-settings-2"
          :loading="settingsPending && !settings"
          @click="openSettingsModal"
        >
          {{ t('admin.moderation.openSettings') }}
        </UButton>
      </div>
    </template>
  </UDashboardToolbar>

  <div class="flex w-full min-w-0 flex-col gap-4">
    <UAlert
      v-if="loadError"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      class="w-full shrink-0"
      :title="loadError || t('admin.moderation.loadFailed')"
    />

    <UAlert
      color="primary"
      variant="soft"
      icon="i-lucide-sparkles"
      class="w-full shrink-0"
      :title="t('admin.moderation.recommendedTitle')"
      :description="t('admin.moderation.recommendedDescription')"
    />

    <UCard
      class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100"
      data-testid="admin-moderation-reports"
    >
      <template #header>
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="text-base font-bold text-slate-900 dark:text-white">
              {{ t('admin.moderation.reports.title') }}
            </h2>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.moderation.reports.description') }}
            </p>
          </div>
          <UButton
            color="neutral"
            variant="outline"
            size="xs"
            leading-icon="i-lucide-refresh-cw"
            :loading="reportsPending"
            class="border-slate-200 dark:border-zinc-700"
            @click="refreshReports()"
          >
            {{ t('admin.common.refresh') }}
          </UButton>
        </div>
      </template>

      <UAlert
        v-if="reportFeedback"
        color="error"
        variant="soft"
        icon="i-lucide-triangle-alert"
        class="mb-3"
        :title="reportFeedback"
        data-testid="admin-moderation-report-conflict"
      />

      <p v-if="!reportsPending && !reports.items.length" class="text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.moderation.reports.empty') }}
      </p>

      <ul v-else class="grid gap-2">
        <li
          v-for="row in reports.items"
          :key="row.id"
          class="min-w-0 rounded-md border border-slate-200 px-3 py-2.5 dark:border-zinc-700"
        >
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div class="min-w-0">
              <p class="truncate text-sm font-medium text-slate-900 dark:text-zinc-100">
                {{ row.targetType }} #{{ row.targetId }} · {{ row.reasonCode }}
              </p>
              <p class="mt-1 text-[11px] text-slate-500 dark:text-zinc-400">
                {{ t('admin.moderation.reports.meta', { reporter: row.reporterName || row.reporterUserId, revision: row.revision }) }}
              </p>
              <p v-if="row.body" class="mt-1 break-words text-xs text-slate-600 dark:text-zinc-300">{{ row.body }}</p>
            </div>
            <UBadge color="neutral" variant="subtle" size="xs">{{ row.status }}</UBadge>
          </div>
          <div class="mt-2 flex flex-wrap items-center gap-2">
            <UButton
              size="xs"
              :loading="reportBusyId === row.id"
              @click="decideReport(row, 'resolved')"
            >
              {{ t('admin.moderation.reports.resolve') }}
            </UButton>
            <UButton
              size="xs"
              color="neutral"
              variant="outline"
              :loading="reportBusyId === row.id"
              @click="decideReport(row, 'rejected')"
            >
              {{ t('admin.moderation.reports.reject') }}
            </UButton>
          </div>
        </li>
      </ul>

      <div class="mt-3 flex flex-wrap items-center justify-between gap-3">
        <UInput
          v-model="reportNote"
          :placeholder="t('admin.moderation.reports.notePlaceholder')"
          class="min-w-0 flex-1"
          size="sm"
        />
        <SFPagination
          v-if="reportsTotalPages > 1"
          :page="reportsPage"
          :total-pages="reportsTotalPages"
          @update:page="reportsPage = $event"
        />
      </div>
    </UCard>

    <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100">
      <template #header>
        <div>
          <h2 id="moderation-audit-title" class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('admin.moderation.auditTitle') }}
          </h2>
          <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.moderation.auditDescription') }}
          </p>
        </div>
      </template>
      <ModerationDecisionTable :items="history.items" :loading="historyPending" />
      <div class="mt-4 flex flex-wrap items-center justify-between gap-3">
        <span class="text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.moderation.history.page', { page: historyPage, total: historyTotal }) }}
        </span>
        <SFPagination
          v-if="historyTotalPages > 1"
          :page="historyPage"
          :total-pages="historyTotalPages"
          @update:page="historyPage = $event"
        />
      </div>
    </UCard>
  </div>

  <!-- 发布审核策略设置 -->
  <UModal
    v-model:open="settingsModalOpen"
    :ui="{ content: 'sm:max-w-3xl' }"
  >
    <template #content>
      <div class="max-h-[90vh] overflow-y-auto p-1 sm:p-2">
        <div class="flex items-center justify-end px-3 pt-2">
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            icon="i-lucide-x"
            :aria-label="t('admin.common.cancel')"
            @click="closeSettingsModal"
          />
        </div>
        <div v-if="settings" class="px-2 pb-2">
          <ModerationSettingsForm
            :model-value="settings"
            @updated="settingsUpdated"
          />
        </div>
        <div v-else-if="settingsPending" class="space-y-3 px-5 py-8" aria-busy="true">
          <SFSkeleton width="36%" />
          <SFSkeleton width="100%" height="180px" />
        </div>
        <!-- 失败/无权限不是「永久骨架」：给出原因与重试入口。 -->
        <div v-else class="space-y-3 px-5 py-8" data-testid="admin-moderation-settings-error">
          <UAlert
            color="error"
            variant="soft"
            icon="i-lucide-triangle-alert"
            :title="settingsModalMessage || t('admin.moderation.settingsLoadFailed')"
          />
          <div class="flex justify-end">
            <UButton
              icon="i-lucide-rotate-cw"
              color="neutral"
              variant="outline"
              :loading="settingsPending"
              @click="refreshSettings()"
            >
              {{ t('admin.common.refresh') }}
            </UButton>
          </div>
        </div>
      </div>
    </template>
  </UModal>
</template>
