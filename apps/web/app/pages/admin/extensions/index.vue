<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { useAdminExtensionsManager } from '~/composables/admin/useAdminExtensionsManager'
import { useAdminExtensionUpgradeFlow } from '~/composables/admin/useAdminExtensionUpgradeFlow'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import SFAdminExtensionEnableDialog from '~/components/admin/SFAdminExtensionEnableDialog.vue'
import SFAdminExtensionLifecycleDialog from '~/components/admin/SFAdminExtensionLifecycleDialog.vue'
import SFAdminExtensionUninstallDialog from '~/components/admin/SFAdminExtensionUninstallDialog.vue'
import SFAdminMissingArtifactsCleanupDialog from '~/components/admin/SFAdminMissingArtifactsCleanupDialog.vue'
import SFAdminVersionGcDialog from '~/components/admin/SFAdminVersionGcDialog.vue'
import SFAdminThemeActivateDialog from '~/components/admin/SFAdminThemeActivateDialog.vue'
import SFAdminExtensionIdentity from '~/components/admin/extensions/SFAdminExtensionIdentity.vue'
import { useAdminListSurfaces } from '~/composables/admin/useAdminListSurfaces'
import { useAdminExtensionVersionGc } from '~/composables/admin/useAdminExtensionVersionGc'
import {
  canRequestExtensionUninstall,
  canRestartPlugin,
  canUpgradePlugin,
  extensionEventPage,
  extensionLocalizedDisplay,
  extensionManageRoute,
  formatPluginMemoryBytes,
  isExtensionArtifactAvailable,
  missingArtifactCleanupCandidates,
  selectMissingArtifactCleanupTargets,
  themeActionState,
  themeStatusLabelKey,
  type AdminExtension,
  type AdminMissingArtifactCleanupResult,
  type AdminMissingArtifactDataMode
} from '~/utils/admin/adminExtensions'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensions'
})

const { format: formatSiteDateTime } = useSiteDateTime()
const { t, locale } = useI18n()
const { request } = useApiClient()
const toast = useToast()
const adminPage = useAdminPage('/extensions')
const adminRoutes = useAdminRoutes()
const selectedEventPage = ref(1)
// 与当前 UI 语言绑定，切换语言时列表与详情文案会立刻重算。
const extensionRows = computed(() => extensions.value.map((item) => ({
  item,
  display: extensionLocalizedDisplay(item, locale.value)
})))
const selectedDisplay = computed(() => selected.value ? extensionLocalizedDisplay(selected.value, locale.value) : null)

const {
  extensions,
  pending,
  error,
  refresh,
  fileInput,
  selectedId,
  selected,
  selectedEvents,
  uploading,
  busyId,
  loadingEvents,
  stats,
  openUpload,
  uploadArchive,
  enableExtension,
  confirmEnableExtension,
  issueEnableTrustChallenge,
  cancelEnableExtension,
  enableConfirmOpen,
  enableConfirmItem,
  enableTrustMode,
  enableTrustStatus,
  enableTrustChallenge,
  enableTrustError,
  enableTrustBusy,
  isSuperAdmin,
  openUninstallExtension,
  confirmUninstallExtension,
  cancelUninstallExtension,
  finalizeUninstallCleanup,
  uninstallConfirmOpen,
  uninstallConfirmItem,
  uninstallRemovalMode,
  uninstallError,
  uninstallCleanupDetails,
  finalizeCleanupBusyId,
  lifecycleDialogOpen,
  lifecycleDialogItem,
  lifecycleOperations,
  lifecycleOperation,
  lifecycleLoading,
  lifecycleRecoveryBusy,
  lifecycleError,
  openLifecycleExtension,
  selectLifecycleOperation,
  recoverLifecycleOperation,
  disableExtension,
  restartExtension,
  activateTheme,
  themeActivateTrustMode,
  themeActivateConfirmOpen,
  themePreviewConfirmOpen,
  themeActivateConfirmItem,
  themeActivatePreview,
  themePreviewAddCount,
  themePreviewReplaceCount,
  themePreviewReactivating,
  themeActivateTrustStatus,
  themeActivateTrustChallenge,
  themeActivateTrustError,
  themeActivateTrustBusy,
  issueThemeActivateTrustChallenge,
  confirmThemeActivate,
  cancelThemeActivate,
  confirmThemePreviewActivate,
  cancelThemePreviewActivate,
  loadEvents,
  statusColor,
  typeLabel,
  statusLabel
} = await useAdminExtensionsManager()
const {
  upgradeCandidates,
  upgradeConfirmOpen,
  upgradeConfirmItem,
  upgradeTrustMode,
  upgradeTrustStatus,
  upgradeTrustChallenge,
  upgradeTrustError,
  upgradeTrustBusy,
  upgradeBusyId,
  upgradeAllBusy,
  upgradeAllCompleted,
  upgradeAllTotal,
  upgradeExtension,
  upgradeAllExtensions,
  issueUpgradeTrustChallenge,
  confirmUpgradeExtension,
  cancelUpgradeExtension
} = useAdminExtensionUpgradeFlow({ extensions, refresh, isSuperAdmin })

// 总览页同样把每行扩展当作 resource：list_column 徽章与 links 深链都来自
// Admin Surface，页面不再自己推断「有没有更新」。
const updateSurfaces = useAdminListSurfaces({
  pageId: '/extensions',
  resources: computed(() => extensions.value.map(item => ({
    id: item.id,
    attributes: {
      version: item.version,
      stagedVersion: item.stagedVersion?.version || '',
      type: item.type,
      status: item.status,
      source: item.source || ''
    }
  }))),
  context: { resourceType: 'extension', page: 'overview' },
  refreshHost: refresh
})

const surfaceFailures = computed(() => updateSurfaces.failures.value.filter(item => item.id !== 'catalog'))

async function refreshAll() {
  await refresh()
  await updateSurfaces.refreshSurfaces()
}
const selectedEventPageInfo = computed(() => extensionEventPage(selectedEvents.value, selectedEventPage.value))
const missingArtifacts = computed(() => missingArtifactCleanupCandidates(extensions.value))
const missingCleanupOpen = ref(false)
const missingCleanupBusy = ref(false)
const missingCleanupError = ref('')
const missingCleanupDataMode = ref<AdminMissingArtifactDataMode>('preserve')
const missingCleanupTargets = ref<AdminExtension[]>([])

function openMissingCleanup(extensionId = '') {
  const targets = selectMissingArtifactCleanupTargets(extensions.value, extensionId)
  if (!isSuperAdmin.value || targets.length === 0) return
  missingCleanupTargets.value = targets
  missingCleanupDataMode.value = 'preserve'
  missingCleanupError.value = ''
  missingCleanupOpen.value = true
}

function openExtensionUninstall(item: AdminExtension) {
  if (!isExtensionArtifactAvailable(item)) {
    openMissingCleanup(item.id)
    return
  }
  openUninstallExtension(item)
}

function cancelMissingCleanup() {
  if (missingCleanupBusy.value) return
  missingCleanupOpen.value = false
  missingCleanupTargets.value = []
  missingCleanupError.value = ''
  missingCleanupDataMode.value = 'preserve'
}

async function confirmMissingCleanup() {
  const extensionIds = missingCleanupTargets.value.map(item => item.id)
  if (extensionIds.length === 0) {
    cancelMissingCleanup()
    return
  }
  missingCleanupBusy.value = true
  missingCleanupError.value = ''
  try {
    const result = await request<AdminMissingArtifactCleanupResult>('/admin/extensions/missing-artifacts/uninstall', {
      method: 'POST',
      body: {
        extensionIds,
        dataMode: missingCleanupDataMode.value
      }
    })
    await refresh()
    missingCleanupOpen.value = false
    missingCleanupTargets.value = []
    toast.add({
      color: 'success',
      icon: 'i-lucide-package-check',
      title: t('admin.extensions.missingCleanup.success', { count: result.removed.length }),
      duration: 5000
    })
  } catch (error) {
    missingCleanupError.value = apiErrorMessage(error) || t('admin.extensions.actionFailed')
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: missingCleanupError.value,
      duration: 0
    })
  } finally {
    missingCleanupBusy.value = false
  }
}

useSeoMeta({
  title: t('admin.extensions.metaTitle')
})

// ---- Version GC（阶段 5 管理端入口）：plan → confirmation → result 阶段机 ----
const {
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
} = useAdminExtensionVersionGc({ isSuperAdmin, refresh })

watch(() => selected.value?.id, async (id) => {
  if (!id) {
    return
  }
  selectedId.value = id
  selectedEventPage.value = 1
  await loadEvents(id)
}, { immediate: true })

watch(() => selectedEventPageInfo.value.page, (page) => {
  selectedEventPage.value = page
})

async function refreshSelectedEvents() {
  if (selected.value) {
    await loadEvents(selected.value.id)
  }
}

function extensionStatusLabel(item: (typeof extensions.value)[number]) {
  return item.type === 'theme' ? t(themeStatusLabelKey(item)) : statusLabel(item.status)
}

</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.extensions.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.extensions.intro') }}
    </p>
  </div>

  <UDashboardToolbar class="border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 rounded-lg px-4 py-2.5 mb-6 text-slate-500 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm text-slate-500 dark:text-zinc-400">
        <UIcon name="i-lucide-package" class="size-4" />
        <span class="hidden truncate sm:inline">{{ t('admin.extensions.installedCount', { count: extensions.length }) }}</span>
      </div>
      <input ref="fileInput" class="hidden" type="file" accept=".zip,application/zip" @change="uploadArchive">
    </template>
    <template #right>
      <UButton
        v-if="upgradeCandidates.length"
        icon="i-lucide-package-plus"
        color="warning"
        variant="subtle"
        :loading="upgradeAllBusy"
        :aria-label="upgradeAllBusy
          ? t('admin.extensions.upgradeAllProgress', { completed: upgradeAllCompleted, total: upgradeAllTotal })
          : t('admin.extensions.upgradeAll', { count: upgradeCandidates.length })"
        :title="upgradeAllBusy
          ? t('admin.extensions.upgradeAllProgress', { completed: upgradeAllCompleted, total: upgradeAllTotal })
          : t('admin.extensions.upgradeAll', { count: upgradeCandidates.length })"
        @click="upgradeAllExtensions"
      >
        <span class="hidden sm:inline">
          {{ upgradeAllBusy
            ? t('admin.extensions.upgradeAllProgress', { completed: upgradeAllCompleted, total: upgradeAllTotal })
            : t('admin.extensions.upgradeAll', { count: upgradeCandidates.length }) }}
        </span>
      </UButton>
      <UButton
        v-if="isSuperAdmin && missingArtifacts.length"
        icon="i-lucide-package-x"
        color="error"
        variant="subtle"
        :aria-label="t('admin.extensions.missingCleanup.action', { count: missingArtifacts.length })"
        :title="t('admin.extensions.missingCleanup.action', { count: missingArtifacts.length })"
        @click="openMissingCleanup()"
      >
        <span class="hidden sm:inline">{{ t('admin.extensions.missingCleanup.action', { count: missingArtifacts.length }) }}</span>
      </UButton>
      <UButton
        v-if="isSuperAdmin"
        icon="i-lucide-archive-x"
        color="neutral"
        variant="subtle"
        :aria-label="t('admin.extensions.versionGc.action')"
        :title="t('admin.extensions.versionGc.action')"
        @click="openVersionGc()"
      >
        <span class="hidden sm:inline">{{ t('admin.extensions.versionGc.action') }}</span>
      </UButton>
      <UButton
        v-if="updateSurfaces.hasListColumns.value"
        icon="i-lucide-package-search"
        color="neutral"
        variant="subtle"
        :loading="updateSurfaces.pending.value"
        :aria-label="t('admin.extensions.updateCheck.refresh')"
        :title="t('admin.extensions.updateCheck.refresh')"
        @click="updateSurfaces.refreshSurfaces()"
      >
        <span class="hidden sm:inline">{{ t('admin.extensions.updateCheck.refresh') }}</span>
      </UButton>
      <UButton
        icon="i-lucide-rotate-cw"
        color="neutral"
        variant="subtle"
        :loading="pending"
        :aria-label="t('admin.extensions.refresh')"
        :title="t('admin.extensions.refresh')"
        @click="refreshAll()"
      >
        <span class="hidden sm:inline">{{ t('admin.extensions.refresh') }}</span>
      </UButton>
      <UButton
        icon="i-lucide-upload"
        color="primary"
        :loading="uploading"
        :aria-label="t('admin.extensions.upload')"
        :title="t('admin.extensions.upload')"
        @click="openUpload"
      >
        <span class="hidden sm:inline">{{ t('admin.extensions.upload') }}</span>
      </UButton>
    </template>
  </UDashboardToolbar>

  <UAlert
    v-if="error"
    color="error"
    icon="i-lucide-triangle-alert"
    variant="subtle"
    :title="apiErrorMessage(error) || t('admin.extensions.loadFailed')"
    class="mb-6"
  />

  <UAlert
    v-if="surfaceFailures.length"
    color="warning"
    icon="i-lucide-triangle-alert"
    variant="subtle"
    :title="t('admin.extensions.updateCheck.failed')"
    class="mb-6"
  >
    <template #description>
      <ul class="list-inside list-disc">
        <li v-for="failure in surfaceFailures" :key="failure.id">{{ failure.label }}: {{ failure.message }}</li>
      </ul>
    </template>
    <template #actions>
      <UButton color="warning" variant="subtle" icon="i-lucide-rotate-cw" @click="updateSurfaces.refreshSurfaces()">
        {{ t('admin.extensions.updateCheck.retry') }}
      </UButton>
    </template>
  </UAlert>

  <div class="grid gap-5 md:grid-cols-3 mb-6">
    <UCard class="elegant-card border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
      <div class="flex items-center justify-between gap-4">
        <div class="min-w-0">
          <p class="text-xs font-semibold text-slate-500 dark:text-zinc-400 uppercase tracking-wider">
            {{ t('admin.extensions.stats.plugins') }}
          </p>
          <p class="mt-2.5 truncate text-3xl font-black text-slate-900 dark:text-white tracking-tight">
            {{ stats.pluginCount }}
          </p>
        </div>
        <span class="icon-glass-box shrink-0 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]">
          <UIcon name="i-lucide-blocks" class="size-5 z-10" />
        </span>
      </div>
    </UCard>
    <UCard class="elegant-card border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
      <div class="flex items-center justify-between gap-4">
        <div class="min-w-0">
          <p class="text-xs font-semibold text-slate-500 dark:text-zinc-400 uppercase tracking-wider">
            {{ t('admin.extensions.stats.themes') }}
          </p>
          <p class="mt-2.5 truncate text-3xl font-black text-slate-900 dark:text-white tracking-tight">
            {{ stats.themeCount }}
          </p>
        </div>
        <span class="icon-glass-box shrink-0 text-purple-600 dark:text-purple-400">
          <UIcon name="i-lucide-palette" class="size-5 z-10" />
        </span>
      </div>
    </UCard>
    <UCard class="elegant-card border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
      <div class="flex items-center justify-between gap-4">
        <div class="min-w-0">
          <p class="text-xs font-semibold text-slate-500 dark:text-zinc-400 uppercase tracking-wider">
            {{ t('admin.extensions.stats.enabledPlugins') }}
          </p>
          <p class="mt-2.5 truncate text-3xl font-black text-slate-900 dark:text-white tracking-tight">
            {{ stats.enabledPluginCount }}
          </p>
        </div>
        <span class="icon-glass-box shrink-0 text-green-600 dark:text-green-400">
          <UIcon name="i-lucide-play" class="size-5 z-10" />
        </span>
      </div>
    </UCard>
  </div>

  <div class="flex flex-col gap-6">
    <div class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_420px]">
      <div class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
        <div class="border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
          <h2 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.extensions.installed') }}
          </h2>
        </div>
        <div v-if="extensions.length === 0 && !pending" class="p-10">
          <SFEmptyState icon-label="ZIP" :title="t('admin.extensions.emptyTitle')" :description="t('admin.extensions.emptyDescription')" />
        </div>
        <div v-else class="divide-y divide-slate-200 dark:divide-zinc-800">
          <div
            v-for="{ item, display } in extensionRows"
            :key="item.id"
            class="grid gap-4 px-4 py-4 transition hover:bg-slate-50 md:grid-cols-[minmax(0,1fr)_auto] dark:hover:bg-zinc-800/50"
            :class="selected?.id === item.id ? 'bg-slate-50 dark:bg-zinc-800/50' : ''"
          >
            <div class="min-w-0">
              <SFAdminExtensionIdentity
                :extension="item"
                :display="display"
                :status-label="extensionStatusLabel(item)"
                :status-color="statusColor(item.status)"
                :icon="item.type === 'theme' ? 'i-lucide-palette' : 'i-lucide-blocks'"
                :badges="updateSurfaces.badgesFor(item.id)"
                selectable
                :selected="selected?.id === item.id"
                @select="selectedId = item.id"
              >
                <template #badges>
                  <UBadge color="neutral" variant="outline">
                    {{ typeLabel(item.type) }}
                  </UBadge>
                  <UBadge
                    v-if="item.stagedVersion"
                    color="warning"
                    variant="outline"
                    icon="i-lucide-package-plus"
                    :title="t('admin.extensions.stagedVersionHint', { version: item.stagedVersion.version, current: item.version })"
                  >
                    {{ t('admin.extensions.stagedVersionBadge', { version: item.stagedVersion.version }) }}
                  </UBadge>
                  <UBadge
                    v-if="!isExtensionArtifactAvailable(item)"
                    color="error"
                    variant="subtle"
                    icon="i-lucide-package-x"
                  >
                    {{ t('admin.extensions.artifact.missing') }}
                  </UBadge>
                  <UBadge
                    v-if="item.pendingUninstallCleanup"
                    color="warning"
                    variant="subtle"
                    icon="i-lucide-brush-cleaning"
                    :title="t('admin.extensions.uninstallCleanup.pendingHint')"
                  >
                    {{ t('admin.extensions.uninstallCleanup.pendingBadge') }}
                  </UBadge>
                </template>
              </SFAdminExtensionIdentity>
            </div>
            <div class="flex flex-wrap items-center gap-2 [&>a]:shrink-0 [&>button]:shrink-0 md:justify-end">
              <UButton
                v-if="item.type === 'plugin' && item.stagedVersion"
                size="sm"
                color="warning"
                variant="subtle"
                icon="i-lucide-package-plus"
                :disabled="upgradeAllBusy || !canUpgradePlugin(item)"
                :loading="upgradeBusyId === item.id"
                :title="canUpgradePlugin(item)
                  ? t('admin.extensions.stagedVersionHint', { version: item.stagedVersion.version, current: item.version })
                  : t('admin.extensions.upgradeUnavailable')"
                @click="upgradeExtension(item)"
              >
                {{ t('admin.extensions.upgrade') }}
              </UButton>
              <UButton
                size="sm"
                color="neutral"
                variant="ghost"
                icon="i-lucide-settings"
                :to="isExtensionArtifactAvailable(item) ? adminRoutes.path(extensionManageRoute(item)) : undefined"
                :disabled="!isExtensionArtifactAvailable(item)"
                :title="!isExtensionArtifactAvailable(item) ? t('admin.extensions.artifact.actionUnavailable') : undefined"
              >
                {{ t('admin.extensions.manage') }}
              </UButton>
              <UButton
                v-if="item.type === 'plugin' && item.status !== 'enabled'"
                size="sm"
                icon="i-lucide-play"
                :disabled="!isExtensionArtifactAvailable(item)"
                :loading="busyId === item.id"
                @click="enableExtension(item)"
              >
                {{ t('admin.extensions.enable') }}
              </UButton>
              <UButton
                v-else-if="item.type === 'plugin'"
                size="sm"
                color="neutral"
                variant="subtle"
                icon="i-lucide-pause"
                :loading="busyId === item.id"
                @click="disableExtension(item)"
              >
                {{ t('admin.extensions.disable') }}
              </UButton>
              <UButton
                v-if="item.type === 'plugin'"
                size="sm"
                color="neutral"
                variant="ghost"
                icon="i-lucide-history"
                :title="t('admin.extensions.lifecycle.title')"
                :aria-label="t('admin.extensions.lifecycle.title')"
                @click="openLifecycleExtension(item)"
              />
              <UButton
                v-if="item.type === 'plugin'"
                size="sm"
                color="neutral"
                variant="ghost"
                icon="i-lucide-refresh-cw"
                :disabled="!canRestartPlugin(item)"
                :loading="busyId === item.id && canRestartPlugin(item)"
                :title="canRestartPlugin(item) ? t('admin.extensions.restart') : t('admin.extensions.restartUnavailable')"
                @click="restartExtension(item)"
              >
                {{ t('admin.extensions.restart') }}
              </UButton>
              <UButton
                v-else-if="themeActionState(item) === 'activateDefault'"
                size="sm"
                icon="i-lucide-rotate-ccw"
                :disabled="!isExtensionArtifactAvailable(item)"
                :loading="busyId === item.id"
                @click="activateTheme(item)"
              >
                {{ t('admin.extensions.restoreDefaultTheme') }}
              </UButton>
              <UButton
                v-else-if="themeActionState(item) === 'activate'"
                size="sm"
                icon="i-lucide-play"
                :disabled="!isExtensionArtifactAvailable(item)"
                :loading="busyId === item.id"
                @click="activateTheme(item)"
              >
                {{ t('admin.extensions.activateTheme') }}
              </UButton>
              <UButton
                v-else
                size="sm"
                color="neutral"
                variant="subtle"
                icon="i-lucide-refresh-cw"
                :disabled="!isExtensionArtifactAvailable(item)"
                :loading="busyId === item.id"
                :title="t('admin.extensions.reactivateThemeHint')"
                @click="activateTheme(item)"
              >
                {{ t('admin.extensions.reactivateTheme') }}
              </UButton>
              <UButton
                v-if="canRequestExtensionUninstall(item, isSuperAdmin)"
                size="sm"
                color="error"
                variant="ghost"
                icon="i-lucide-trash-2"
                :loading="busyId === item.id"
                @click="openExtensionUninstall(item)"
              >
                {{ t('admin.extensions.uninstall') }}
              </UButton>
            </div>
          </div>
        </div>
      </div>

      <aside class="space-y-4">
        <div class="rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
          <h2 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ selectedDisplay?.name || t('admin.extensions.detailTitle') }}
          </h2>
          <p
            v-if="selectedDisplay?.description"
            class="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-300"
          >
            {{ selectedDisplay.description }}
          </p>
          <dl v-if="selected" class="mt-4 space-y-3 text-sm">
            <div class="flex justify-between gap-3">
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.manifest.id') }}</dt>
              <dd class="truncate text-slate-900 dark:text-zinc-100">{{ selected.id }}</dd>
            </div>
            <div class="flex justify-between gap-3">
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.manifest.version') }}</dt>
              <dd class="text-slate-900 dark:text-zinc-100">{{ selected.version }}</dd>
            </div>
            <div v-if="selected.stagedVersion" class="flex justify-between gap-3">
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.stagedVersion') }}</dt>
              <dd>
                <UBadge
                  color="warning"
                  variant="outline"
                  icon="i-lucide-package-plus"
                  :title="t('admin.extensions.stagedVersionHint', { version: selected.stagedVersion.version, current: selected.version })"
                >
                  v{{ selected.stagedVersion.version }}
                </UBadge>
              </dd>
            </div>
            <div class="flex justify-between gap-3">
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.manifest.sforum') }}</dt>
              <dd class="text-slate-900 dark:text-zinc-100">{{ selected.manifest.sforumVersion }}</dd>
            </div>
            <div
              v-if="selected.type === 'plugin'"
              class="flex justify-between gap-3"
              :title="t('admin.extensions.runtime.memoryHint')"
            >
              <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.runtime.memoryLabel') }}</dt>
              <dd class="text-slate-900 dark:text-zinc-100">
                <template v-if="selected.runtime?.memoryBytes !== undefined && selected.runtime.memoryBytes > 0">
                  {{ formatPluginMemoryBytes(selected.runtime.memoryBytes) }}
                </template>
                <template
                  v-else-if="selected.manifest.backend?.entry && ['starting', 'running', 'degraded'].includes(selected.runtime?.state || '')"
                >
                  {{ t('admin.extensions.runtime.memoryPending') }}
                </template>
                <template v-else>
                  {{ t('admin.extensions.runtime.memoryUnavailable') }}
                </template>
              </dd>
            </div>
          </dl>
        </div>

        <div v-if="selected" class="rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
          <h2 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.extensions.capabilities') }}
          </h2>
          <div class="mt-4 flex flex-wrap gap-2">
            <UBadge v-if="selected.manifest.backend?.entry" color="warning" variant="subtle" icon="i-lucide-terminal">
              {{ t('admin.extensions.capability.backend') }}
            </UBadge>
            <UBadge v-if="selected.manifest.routes?.length" color="neutral" variant="subtle" icon="i-lucide-route">
              {{ t('admin.extensions.capability.routes', { count: selected.manifest.routes.length }) }}
            </UBadge>
            <UBadge v-if="selected.manifest.hooks?.length" color="neutral" variant="subtle" icon="i-lucide-webhook">
              {{ t('admin.extensions.capability.hooks', { count: selected.manifest.hooks.length }) }}
            </UBadge>
            <UBadge v-if="selected.manifest.jobs?.length" color="neutral" variant="subtle" icon="i-lucide-list-checks">
              {{ t('admin.extensions.capability.jobs', { count: selected.manifest.jobs.length }) }}
            </UBadge>
          </div>
          <!-- F2.1 Host 能力授权列表 -->
          <div v-if="selected.capabilityGrants?.length" class="mt-4 space-y-2 border-t border-slate-100 pt-4 dark:border-zinc-800">
            <p class="text-xs font-medium uppercase tracking-wide text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensions.hostCapabilities') }}
            </p>
            <div
              v-for="grant in selected.capabilityGrants"
              :key="grant.key"
              class="rounded-md bg-slate-50 px-3 py-2 text-sm dark:bg-zinc-950"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="font-medium text-slate-900 dark:text-zinc-100">
                  {{ locale.startsWith('zh') ? grant.labelZh : grant.labelEn }}
                </span>
                <UBadge
                  size="xs"
                  variant="subtle"
                  :color="grant.risk === 'high' ? 'error' : grant.risk === 'medium' ? 'warning' : 'success'"
                >
                  {{ t(`admin.extensions.capabilityRisk.${grant.risk}`) }}
                </UBadge>
              </div>
              <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                <code class="text-[11px]">{{ grant.key }}</code>
                <span v-if="grant.implied" class="ml-2">{{ t('admin.extensions.capabilityImplied') }}</span>
              </p>
              <p class="mt-1 text-xs leading-5 text-slate-600 dark:text-zinc-300">
                {{ grant.description }}
              </p>
            </div>
          </div>
        </div>

        <div v-if="selected" class="rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
          <div class="flex items-center justify-between gap-3">
            <h2 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
              {{ t('admin.extensions.events') }}
            </h2>
            <UButton size="xs" color="neutral" variant="ghost" icon="i-lucide-rotate-cw" :loading="loadingEvents" @click="refreshSelectedEvents" />
          </div>
          <div class="mt-4 space-y-3">
            <p v-if="selectedEvents.length === 0" class="text-sm text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensions.noEvents') }}
            </p>
            <div v-for="event in selectedEventPageInfo.items" :key="event.id" class="rounded-md bg-slate-50 p-3 text-sm dark:bg-zinc-900">
              <div class="flex items-center justify-between gap-3">
                <span class="font-medium text-slate-900 dark:text-zinc-100">{{ event.action }}</span>
                <span class="text-xs text-slate-500 dark:text-zinc-400">{{ formatSiteDateTime(event.createdAt) }}</span>
              </div>
              <p v-if="event.message" class="mt-1 text-slate-500 dark:text-zinc-400">
                {{ event.message }}
              </p>
            </div>
            <div
              v-if="selectedEventPageInfo.totalPages > 1"
              class="flex flex-col gap-3 border-t border-slate-200 pt-4 text-xs text-slate-500 sm:flex-row sm:items-center sm:justify-between dark:border-zinc-800 dark:text-zinc-400"
            >
              <span>
                {{ t('admin.extensions.eventPageSummary', { start: selectedEventPageInfo.start, end: selectedEventPageInfo.end, count: selectedEventPageInfo.total }) }}
              </span>
              <SFPagination v-model:page="selectedEventPage" :total-pages="selectedEventPageInfo.totalPages" />
            </div>
          </div>
        </div>
      </aside>
    </div>

    <SFAdminExtensionEnableDialog
      v-model:open="enableConfirmOpen"
      :extension="enableConfirmItem"
      :mode="enableTrustMode"
      :trust-status="enableTrustStatus"
      :challenge="enableTrustChallenge"
      :error="enableTrustError"
      :busy="enableTrustBusy || Boolean(enableConfirmItem && busyId === enableConfirmItem.id)"
      :is-super-admin="isSuperAdmin"
      @cancel="cancelEnableExtension"
      @issue-challenge="issueEnableTrustChallenge"
      @confirm="confirmEnableExtension"
    />

    <SFAdminExtensionEnableDialog
      v-model:open="upgradeConfirmOpen"
      :extension="upgradeConfirmItem"
      :mode="upgradeTrustMode"
      :trust-status="upgradeTrustStatus"
      :challenge="upgradeTrustChallenge"
      :error="upgradeTrustError"
      :busy="upgradeTrustBusy || Boolean(upgradeConfirmItem && upgradeBusyId === upgradeConfirmItem.id)"
      :is-super-admin="isSuperAdmin"
      purpose="upgrade"
      @cancel="cancelUpgradeExtension"
      @issue-challenge="issueUpgradeTrustChallenge"
      @confirm="confirmUpgradeExtension"
    />

    <!-- L0/L1 与 trust_not_required：页面预览确认 Modal（替代原生 confirm）。 -->
    <SFAdminThemeActivateDialog
      v-model:open="themePreviewConfirmOpen"
      :extension="themeActivateConfirmItem"
      :impacts="themeActivatePreview?.impacts || []"
      :add-count="themePreviewAddCount"
      :replace-count="themePreviewReplaceCount"
      :reactivating="themePreviewReactivating"
      :busy="Boolean(themeActivateConfirmItem && busyId === themeActivateConfirmItem.id)"
      @cancel="cancelThemePreviewActivate"
      @confirm="confirmThemePreviewActivate"
    />

    <!-- Executable (L2) theme activation reuses the exact trust challenge dialog pattern (shared with plugins enable). -->
    <SFAdminExtensionEnableDialog
      v-model:open="themeActivateConfirmOpen"
      :extension="themeActivateConfirmItem"
      :mode="themeActivateTrustMode"
      :trust-status="themeActivateTrustStatus"
      :challenge="themeActivateTrustChallenge"
      :error="themeActivateTrustError"
      :busy="themeActivateTrustBusy || Boolean(themeActivateConfirmItem && busyId === themeActivateConfirmItem.id)"
      :is-super-admin="isSuperAdmin"
      purpose="activate"
      @cancel="cancelThemeActivate"
      @issue-challenge="issueThemeActivateTrustChallenge"
      @confirm="confirmThemeActivate"
    />

    <SFAdminExtensionUninstallDialog
      v-model:open="uninstallConfirmOpen"
      v-model:removal-mode="uninstallRemovalMode"
      :extension="uninstallConfirmItem"
      :busy="Boolean(uninstallConfirmItem && busyId === uninstallConfirmItem.id)"
      :error="uninstallError"
      :cleanup-details="uninstallCleanupDetails"
      :finalize-busy="Boolean(uninstallConfirmItem && finalizeCleanupBusyId === uninstallConfirmItem.id)"
      @cancel="cancelUninstallExtension"
      @confirm="confirmUninstallExtension"
      @finalize="uninstallConfirmItem && finalizeUninstallCleanup(uninstallConfirmItem)"
    />

    <SFAdminExtensionLifecycleDialog
      v-model:open="lifecycleDialogOpen"
      :extension="lifecycleDialogItem"
      :operations="lifecycleOperations"
      :operation="lifecycleOperation"
      :loading="lifecycleLoading"
      :recovery-busy="lifecycleRecoveryBusy"
      :error="lifecycleError"
      :is-super-admin="isSuperAdmin"
      @select="selectLifecycleOperation"
      @recover="recoverLifecycleOperation"
    />

    <SFAdminMissingArtifactsCleanupDialog
      v-model:open="missingCleanupOpen"
      v-model:data-mode="missingCleanupDataMode"
      :extensions="missingCleanupTargets"
      :busy="missingCleanupBusy"
      :error="missingCleanupError"
      @cancel="cancelMissingCleanup"
      @confirm="confirmMissingCleanup"
    />

    <SFAdminVersionGcDialog
      v-model:open="versionGcOpen"
      v-model:policy="versionGcPolicy"
      :candidates="versionGcCandidates"
      :confirmation="versionGcConfirmation"
      :result="versionGcResult"
      :busy="versionGcBusy"
      :error="versionGcError"
      @cancel="cancelVersionGc"
      @refresh="refreshVersionGcPlan"
      @confirm-plan="confirmVersionGcPlan"
      @execute="executeVersionGc"
    />
  </div>
</template>
