<script setup lang="ts">
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAdminContributionCatalog } from '~/composables/admin/useAdminContributionCatalog'
import { useAdminContributionGovernance } from '~/composables/admin/useAdminContributionGovernance'
import { usePermissions } from '~/composables/identity/usePermissions'
import {
  extensionContributionLabel,
  extensionContributionPage,
  extensionContributionPayloadSummary,
  type AdminContributionPointDefinition,
  type AdminEffectiveContribution
} from '~/utils/admin/adminExtensions'
import {
  contributionEnabled,
  contributionOverridden,
  contributionsForPoint
} from '~/utils/admin/adminContributions'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensionContributions'
})

const { t, locale } = useI18n()
const adminPage = useAdminPage('/extensions/contributions')
const contributionPage = ref(1)
// 加载失败必须可见：这里拿到的是显式状态，而不是「catch 成空列表」的假空态。
const {
  points: contributionPoints,
  contributions,
  loading: pending,
  failed,
  errorMessage,
  viewState,
  load: loadCatalog
} = useAdminContributionCatalog()
await loadCatalog()

const { can } = usePermissions()
// 治理操作（启用 / 顺序 / 恢复默认）需要插件管理权限；没有权限时整列不渲染，
// 只留下只读的顺序与来源说明。
const canGovern = computed(() => can('extension.plugin.manage'))
const {
  rowErrors,
  rowBusy,
  rowKey,
  effectiveOrderOf,
  setEnabled,
  move,
  resetDefault
} = useAdminContributionGovernance({ contributions })

const contributionPageInfo = computed(() => extensionContributionPage(contributions.value, contributionPage.value))

useSeoMeta({
  title: t('admin.extensions.contributions.metaTitle')
})

watch(() => contributionPageInfo.value.page, (page) => {
  contributionPage.value = page
})

async function refreshContributions() {
  await loadCatalog()
}

function pointDefinition(point: string): AdminContributionPointDefinition | undefined {
  return contributionPoints.value.find(item => item.id === point)
}

// 上移/下移的边界由同一份排序结果决定：首行不能再上移，末行不能再下移。
function canMove(item: AdminEffectiveContribution, direction: -1 | 1): boolean {
  const group = contributionsForPoint(contributions.value, item.point)
  const index = group.findIndex(entry => entry.id === item.id)
  if (index < 0) return false
  const target = index + direction
  return target >= 0 && target < group.length
}

function enabledOf(item: AdminEffectiveContribution): boolean {
  return contributionEnabled(item)
}

// UButton/USwitch 的事件签名是 void | Promise<void>，这里不把治理结果透给模板。
function onMove(item: AdminEffectiveContribution, direction: -1 | 1) {
  void move(item, direction)
}

function onToggle(item: AdminEffectiveContribution, value: unknown) {
  void setEnabled(item, value === true)
}

function onReset(item: AdminEffectiveContribution) {
  void resetDefault(item)
}
</script>

<template>
  <div data-testid="admin-extension-contributions-page" class="min-w-0 shrink-0">
    <div class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.extensions.contributions.title') }}
      </h2>
      <p class="text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.extensions.contributions.intro') }}
      </p>
    </div>

    <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <div class="flex min-w-0 items-center gap-2 text-sm">
          <UIcon name="i-lucide-blocks" class="size-4" />
          <span class="truncate">{{ t('admin.extensions.contributions.pointsCount', { count: contributionPoints.length }) }}</span>
          <span class="text-slate-300 dark:text-zinc-700">/</span>
          <span class="truncate">{{ t('admin.extensions.contributions.activeCount', { count: contributions.length }) }}</span>
        </div>
      </template>
      <template #right>
        <UButton icon="i-lucide-rotate-cw" color="neutral" variant="subtle" :loading="pending" @click="refreshContributions">
          {{ t('admin.extensions.refresh') }}
        </UButton>
      </template>
    </UDashboardToolbar>

    <div
      v-if="failed"
      data-testid="contributions-load-error"
      class="mb-6 flex flex-col gap-3 rounded-lg border border-red-200 bg-red-50 px-4 py-3 dark:border-red-900/60 dark:bg-red-950/30"
    >
      <UAlert
        color="error"
        icon="i-lucide-triangle-alert"
        variant="subtle"
        :title="errorMessage || t('admin.extensions.contributions.loadErrorTitle')"
        :description="t('admin.extensions.contributions.loadErrorHint')"
      />
      <div>
        <UButton
          icon="i-lucide-rotate-cw"
          color="error"
          variant="soft"
          :loading="pending"
          @click="refreshContributions()"
        >
          {{ t('admin.extensions.contributions.retry') }}
        </UButton>
      </div>
    </div>

    <section class="mb-6 overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
      <div class="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
        <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
          {{ t('admin.extensions.contributions.pointsTitle') }}
        </h3>
        <UBadge color="neutral" variant="outline">
          {{ t('admin.extensions.contributions.pointsCount', { count: contributionPoints.length }) }}
        </UBadge>
      </div>
      <div v-if="viewState === 'empty'" class="p-10">
        <SFEmptyState icon-label="EXT" :title="t('admin.extensions.contributions.emptyPointsTitle')" :description="t('admin.extensions.contributions.emptyPointsDescription')" />
      </div>
      <div v-else class="divide-y divide-slate-200 dark:divide-zinc-800">
        <div
          v-for="point in contributionPoints"
          :key="point.id"
          class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_180px]"
        >
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <UIcon name="i-lucide-radio" class="size-4 text-[var(--sf-accent)]" />
              <h3 class="min-w-0 break-all text-sm font-semibold text-slate-900 dark:text-zinc-100">
                {{ point.id }}
              </h3>
              <UBadge color="neutral" variant="subtle">
                {{ point.kind }}
              </UBadge>
            </div>
            <p class="mt-1 text-sm text-slate-500 dark:text-zinc-400">
              {{ point.description }}
            </p>
          </div>
          <div class="flex flex-col gap-1 text-xs text-slate-500 md:items-end dark:text-zinc-400">
            <span>{{ point.owner }}</span>
            <span>{{ point.payloadType }}</span>
          </div>
        </div>
      </div>
    </section>

    <section class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
      <div class="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
        <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
          {{ t('admin.extensions.contributions.activeTitle') }}
        </h3>
        <UBadge color="neutral" variant="outline">
          {{ t('admin.extensions.contributions.activeCount', { count: contributions.length }) }}
        </UBadge>
      </div>
      <div v-if="viewState === 'empty'" class="p-10">
        <SFEmptyState icon-label="ACT" :title="t('admin.extensions.contributions.emptyActiveTitle')" :description="t('admin.extensions.contributions.emptyActiveDescription')" />
      </div>
      <div v-else class="divide-y divide-slate-200 dark:divide-zinc-800">
        <div
          v-for="contribution in contributionPageInfo.items"
          :key="`${contribution.extensionId}:${contribution.point}:${contribution.id}`"
          class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_260px]"
          :data-contribution-key="rowKey(contribution)"
          :data-governance-enabled="enabledOf(contribution) ? 'true' : 'false'"
        >
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <UIcon :name="contribution.icon || 'i-lucide-plug'" class="size-4 text-[var(--sf-accent)]" />
              <h3 class="min-w-0 break-all text-sm font-semibold text-slate-900 dark:text-zinc-100">
                {{ extensionContributionLabel(contribution, locale) }}
              </h3>
              <UBadge color="neutral" variant="subtle">
                {{ contribution.point }}
              </UBadge>
              <UBadge v-if="!enabledOf(contribution)" color="warning" variant="subtle">
                {{ t('admin.extensions.contributions.governance.disabledBadge') }}
              </UBadge>
              <UBadge v-if="contributionOverridden(contribution)" color="primary" variant="subtle">
                {{ t('admin.extensions.contributions.governance.overriddenBadge') }}
              </UBadge>
            </div>
            <p class="mt-1 break-all text-xs text-slate-500 dark:text-zinc-400">
              {{ contribution.extensionName }} · {{ contribution.id }}
            </p>
            <p class="mt-1 break-all text-xs text-slate-500 dark:text-zinc-400">
              {{ pointDefinition(contribution.point)?.description || contribution.point }}
            </p>
            <p class="mt-1 break-all text-xs text-slate-500 dark:text-zinc-400">
              {{ extensionContributionPayloadSummary(contribution) }}
            </p>
            <p
              v-if="rowErrors[rowKey(contribution)]"
              class="mt-2 text-xs font-semibold text-red-600 dark:text-red-400"
              role="alert"
            >
              {{ rowErrors[rowKey(contribution)] }}
            </p>
          </div>
          <div class="flex flex-col gap-2 text-xs text-slate-500 md:items-end dark:text-zinc-400">
            <span>{{ t('admin.extensions.contributions.order', { order: effectiveOrderOf(contribution) }) }}</span>
            <div v-if="canGovern" class="flex flex-wrap items-center gap-1 md:justify-end">
              <UButton
                icon="i-lucide-arrow-up"
                color="neutral"
                variant="ghost"
                size="xs"
                :aria-label="t('admin.extensions.contributions.governance.moveUp')"
                :title="t('admin.extensions.contributions.governance.moveUp')"
                :disabled="!canMove(contribution, -1) || Boolean(rowBusy[rowKey(contribution)])"
                @click="onMove(contribution, -1)"
              />
              <UButton
                icon="i-lucide-arrow-down"
                color="neutral"
                variant="ghost"
                size="xs"
                :aria-label="t('admin.extensions.contributions.governance.moveDown')"
                :title="t('admin.extensions.contributions.governance.moveDown')"
                :disabled="!canMove(contribution, 1) || Boolean(rowBusy[rowKey(contribution)])"
                @click="onMove(contribution, 1)"
              />
              <USwitch
                :model-value="enabledOf(contribution)"
                :aria-label="t('admin.extensions.contributions.governance.toggleAria')"
                :disabled="Boolean(rowBusy[rowKey(contribution)])"
                @update:model-value="onToggle(contribution, $event)"
              />
              <UButton
                v-if="contributionOverridden(contribution)"
                color="neutral"
                variant="ghost"
                size="xs"
                icon="i-lucide-rotate-ccw"
                :disabled="Boolean(rowBusy[rowKey(contribution)])"
                @click="onReset(contribution)"
              >
                {{ t('admin.extensions.contributions.governance.reset') }}
              </UButton>
            </div>
          </div>
        </div>
        <div
          v-if="contributionPageInfo.totalPages > 1"
          class="flex flex-col gap-3 px-4 py-4 text-xs text-slate-500 sm:flex-row sm:items-center sm:justify-between dark:text-zinc-400"
        >
          <span>
            {{ t('admin.extensions.eventPageSummary', { start: contributionPageInfo.start, end: contributionPageInfo.end, count: contributionPageInfo.total }) }}
          </span>
          <SFPagination v-model:page="contributionPage" :total-pages="contributionPageInfo.totalPages" />
        </div>
      </div>
    </section>
  </div>
</template>
