<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { useAdminExtensionsManager } from '~/composables/admin/useAdminExtensionsManager'
import { useAdminExtensionUpgradeFlow } from '~/composables/admin/useAdminExtensionUpgradeFlow'
import { useAdminBuiltinAutoUpdate } from '~/composables/admin/useAdminBuiltinAutoUpdate'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import SFAdminExtensionEnableDialog from '~/components/admin/SFAdminExtensionEnableDialog.vue'
import SFAdminExtensionLifecycleDialog from '~/components/admin/SFAdminExtensionLifecycleDialog.vue'
import SFAdminExtensionUninstallDialog from '~/components/admin/SFAdminExtensionUninstallDialog.vue'
import SFAdminFrontendTrustPanel from '~/components/admin/SFAdminFrontendTrustPanel.vue'
import SFAdminExtensionHostActionsNotice from '~/components/admin/SFAdminExtensionHostActionsNotice.vue'
import SFAdminExtensionIdentity from '~/components/admin/extensions/SFAdminExtensionIdentity.vue'
import { useAdminListSurfaces } from '~/composables/admin/useAdminListSurfaces'
import {
  canRestartPlugin,
  canUpgradePlugin,
  extensionLocalizedDisplay,
  extensionManageRoute,
  extensionSettingsPresentation,
  filterExtensionsByType,
  formatPluginMemoryBytes,
  isExtensionArtifactAvailable,
  isLifecycleV2Plugin,
  runtimeCapabilitySummary,
  runtimeStatusLabelKey,
  type AdminExtension,
  type AdminRuntimeState
} from '~/utils/admin/adminExtensions'
import {
  builtinAutoUpdateStateLabelKey,
  isBuiltinMaintainedExtension,
  shouldShowBuiltinAutoUpdateDisabled,
  shouldShowBuiltinAutoUpdateIncomplete
} from '~/utils/admin/extensions/builtinAutoUpdate'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensionPlugins'
})

const { t, locale } = useI18n()
const adminPage = useAdminPage('/extensions/plugins')
const adminRoutes = useAdminRoutes()
const {
  extensions,
  pending,
  error,
  refresh,
  busyId,
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
  statusColor,
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

const { can } = useAuthSession()
const toast = useToast()
const canManagePlugins = computed(() => can('extension.plugin.manage'))
const {
  supported: builtinAutoUpdateSupported,
  enabled: builtinAutoUpdateEnabled,
  saving: builtinAutoUpdateSaving,
  loadError: builtinAutoUpdateLoadError,
  saveError: builtinAutoUpdateSaveError,
  load: loadBuiltinAutoUpdate,
  save: saveBuiltinAutoUpdate,
  restoreRecommended: restoreBuiltinAutoUpdate
} = useAdminBuiltinAutoUpdate()

// 只读管理员没有 extension.plugin.manage，读取 admin web-options 会被拒绝；
// 这种情况下不请求、也不渲染开关。
const builtinAutoUpdateLoadFailed = ref(false)
if (canManagePlugins.value) {
  builtinAutoUpdateLoadFailed.value = !(await loadBuiltinAutoUpdate())
}

const showBuiltinAutoUpdate = computed(() => canManagePlugins.value && builtinAutoUpdateSupported.value)
const builtinAutoUpdateState = computed(() => ({
  supported: builtinAutoUpdateSupported.value,
  enabled: builtinAutoUpdateEnabled.value
}))
const builtinAutoUpdateStateLabel = computed(() => t(builtinAutoUpdateStateLabelKey(builtinAutoUpdateEnabled.value)))
const builtinAutoUpdateAriaLabel = computed(() => `${t('admin.extensions.plugins.autoUpdate.label')} · ${builtinAutoUpdateStateLabel.value}`)
const builtinAutoUpdateHelp = computed(() => t('admin.extensions.plugins.autoUpdate.help'))

function reportBuiltinAutoUpdate(ok: boolean, successTitle: string) {
  if (ok) {
    toast.add({ color: 'success', icon: 'i-lucide-check', title: successTitle, duration: 5000 })
    return
  }
  toast.add({
    color: 'error',
    icon: 'i-lucide-triangle-alert',
    title: builtinAutoUpdateSaveError.value || t('admin.extensions.plugins.autoUpdate.saveFailed'),
    duration: 0
  })
}

async function setBuiltinAutoUpdate(next: boolean) {
  if (builtinAutoUpdateSaving.value) {
    return
  }
  reportBuiltinAutoUpdate(
    await saveBuiltinAutoUpdate(next),
    t('admin.extensions.plugins.autoUpdate.saveSuccess')
  )
}

async function restoreBuiltinAutoUpdateRecommended() {
  if (builtinAutoUpdateSaving.value) {
    return
  }
  reportBuiltinAutoUpdate(
    await restoreBuiltinAutoUpdate(),
    t('admin.extensions.plugins.autoUpdate.restoreSuccess')
  )
}

function stagedVersionHint(item: AdminExtension) {
  if (!item.stagedVersion) {
    return ''
  }
  if (shouldShowBuiltinAutoUpdateIncomplete(item, builtinAutoUpdateState.value)) {
    return t('admin.extensions.plugins.autoUpdate.incompleteHint')
  }
  return t('admin.extensions.stagedVersionHint', {
    version: item.stagedVersion.version,
    current: item.version
  })
}

const plugins = computed(() => filterExtensionsByType(extensions.value, 'plugin'))
const inspectorMenuItems = computed(() => [[
  { label: t('admin.nav.extensionRouteProviders'), icon: 'i-lucide-route', to: adminRoutes.path('/extensions/route-providers') },
  { label: t('admin.nav.extensionRouteInspector'), icon: 'i-lucide-scan-search', to: adminRoutes.path('/extensions/route-inspector') },
  { label: t('admin.nav.extensionCacheInspector'), icon: 'i-lucide-database-zap', to: adminRoutes.path('/extensions/cache-inspector') },
  { label: t('admin.nav.extensionAssetInspector'), icon: 'i-lucide-package', to: adminRoutes.path('/extensions/asset-inspector') },
  { label: t('admin.nav.extensionTemplateInspector'), icon: 'i-lucide-layout-template', to: adminRoutes.path('/extensions/template-inspector') },
  { label: t('admin.nav.extensionComponentInspector'), icon: 'i-lucide-boxes', to: adminRoutes.path('/extensions/component-inspector') },
  { label: t('admin.nav.extensionNavigationInspector'), icon: 'i-lucide-map', to: adminRoutes.path('/extensions/navigation-inspector') },
  { label: t('admin.nav.extensionRegistryCatalogs'), icon: 'i-lucide-library', to: adminRoutes.path('/extensions/registry-catalogs') },
  { label: t('admin.nav.extensionProviderSlots'), icon: 'i-lucide-waypoints', to: adminRoutes.path('/extensions/provider-slots') }
]])
// 与当前 UI 语言绑定，切换语言时列表文案会立刻重算。
const pluginRows = computed(() => plugins.value.map((item) => ({
  item,
  display: extensionLocalizedDisplay(item, locale.value)
})))

// 扩展列表把每一行当作一个 resource 交给 Admin Surface：list_column 的输出
// 渲染成行内徽章，links 提供深链。没有 surface 时这一层完全不出现。
const updateSurfaces = useAdminListSurfaces({
  pageId: '/extensions/plugins',
  resources: computed(() => plugins.value.map(item => ({
    id: item.id,
    attributes: {
      version: item.version,
      stagedVersion: item.stagedVersion?.version || '',
      type: item.type,
      status: item.status,
      source: item.source || ''
    }
  }))),
  context: { resourceType: 'extension', page: 'plugins' },
  refreshHost: refresh
})

const surfaceFailures = computed(() => updateSurfaces.failures.value.filter(item => item.id !== 'catalog'))

async function refreshAll() {
  await refresh()
  await updateSurfaces.refreshSurfaces()
}

function runtimeColor(state?: AdminRuntimeState) {
  if (state === 'running') {
    return 'success'
  }
  if (state === 'failed') {
    return 'error'
  }
  if (state === 'degraded' || state === 'starting') {
    return 'warning'
  }
  return 'neutral'
}

function storageInstancesRoute(item: AdminExtension) {
  const provider = item.manifest.providers?.find(candidate => candidate.slot === 'attachment.storage.provider' && candidate.multiInstance)
  if (!provider) return ''
  return `${adminRoutes.path('/attachments/settings')}?provider=${encodeURIComponent(item.id)}`
}

useSeoMeta({
  title: t('admin.extensions.plugins.metaTitle')
})
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.extensions.plugins.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.extensions.plugins.intro') }}
    </p>
  </div>

  <UDashboardToolbar class="border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 rounded-lg px-4 py-2.5 mb-6 text-slate-500 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm">
        <UIcon name="i-lucide-plug" class="size-4" />
        <span class="hidden truncate sm:inline">{{ t('admin.extensions.plugins.count', { count: plugins.length }) }}</span>
      </div>
    </template>
    <template #right>
      <div v-if="showBuiltinAutoUpdate" class="flex shrink-0 items-center gap-2">
        <UIcon name="i-lucide-package-check" class="size-4 shrink-0" />
        <span class="hidden whitespace-nowrap text-xs xl:inline">
          {{ t('admin.extensions.plugins.autoUpdate.label') }}
        </span>
        <USwitch
          :model-value="builtinAutoUpdateEnabled"
          :loading="builtinAutoUpdateSaving"
          size="sm"
          :aria-label="builtinAutoUpdateAriaLabel"
          :title="builtinAutoUpdateHelp"
          @update:model-value="setBuiltinAutoUpdate(Boolean($event))"
        />
      </div>
      <UButton
        v-if="showBuiltinAutoUpdate && !builtinAutoUpdateEnabled"
        icon="i-lucide-rotate-ccw"
        color="neutral"
        variant="ghost"
        size="sm"
        :loading="builtinAutoUpdateSaving"
        :aria-label="t('admin.extensions.plugins.autoUpdate.restoreRecommended')"
        :title="t('admin.extensions.plugins.autoUpdate.restoreRecommended')"
        @click="restoreBuiltinAutoUpdateRecommended"
      >
        <span class="hidden 2xl:inline">{{ t('admin.extensions.plugins.autoUpdate.restoreRecommended') }}</span>
      </UButton>
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
        <span class="hidden xl:inline">
          {{ upgradeAllBusy
            ? t('admin.extensions.upgradeAllProgress', { completed: upgradeAllCompleted, total: upgradeAllTotal })
            : t('admin.extensions.upgradeAll', { count: upgradeCandidates.length }) }}
        </span>
      </UButton>
      <UDropdownMenu :items="inspectorMenuItems" :content="{ align: 'end' }">
        <UButton
          class="xl:hidden"
          icon="i-lucide-ellipsis"
          color="neutral"
          variant="subtle"
          :aria-label="t('common.more')"
          :title="t('common.more')"
        />
      </UDropdownMenu>
      <UButton
        class="hidden xl:inline-flex"
        icon="i-lucide-route"
        color="neutral"
        variant="subtle"
        :to="adminRoutes.path('/extensions/route-providers')"
      >
        {{ t('admin.nav.extensionRouteProviders') }}
      </UButton>
      <UButton
        class="hidden xl:inline-flex"
        icon="i-lucide-scan-search"
        color="neutral"
        variant="subtle"
        :to="adminRoutes.path('/extensions/route-inspector')"
      >
        {{ t('admin.nav.extensionRouteInspector') }}
      </UButton>
      <UButton
        class="hidden xl:inline-flex"
        icon="i-lucide-database-zap"
        color="neutral"
        variant="subtle"
        :to="adminRoutes.path('/extensions/cache-inspector')"
      >
        {{ t('admin.nav.extensionCacheInspector') }}
      </UButton>
      <UButton
        class="hidden xl:inline-flex"
        icon="i-lucide-package"
        color="neutral"
        variant="subtle"
        :to="adminRoutes.path('/extensions/asset-inspector')"
      >
        {{ t('admin.nav.extensionAssetInspector') }}
      </UButton>
      <UButton
        class="hidden xl:inline-flex"
        icon="i-lucide-layout-template"
        color="neutral"
        variant="subtle"
        :to="adminRoutes.path('/extensions/template-inspector')"
      >
        {{ t('admin.nav.extensionTemplateInspector') }}
      </UButton>
      <UButton
        class="hidden xl:inline-flex"
        icon="i-lucide-boxes"
        color="neutral"
        variant="subtle"
        :to="adminRoutes.path('/extensions/component-inspector')"
      >
        {{ t('admin.nav.extensionComponentInspector') }}
      </UButton>
      <UButton
        class="hidden xl:inline-flex"
        icon="i-lucide-map"
        color="neutral"
        variant="subtle"
        :to="adminRoutes.path('/extensions/navigation-inspector')"
      >
        {{ t('admin.nav.extensionNavigationInspector') }}
      </UButton>
      <UButton
        class="hidden xl:inline-flex"
        icon="i-lucide-library"
        color="neutral"
        variant="subtle"
        :to="adminRoutes.path('/extensions/registry-catalogs')"
      >
        {{ t('admin.nav.extensionRegistryCatalogs') }}
      </UButton>
      <UButton
        class="hidden xl:inline-flex"
        icon="i-lucide-waypoints"
        color="neutral"
        variant="subtle"
        :to="adminRoutes.path('/extensions/provider-slots')"
      >
        {{ t('admin.nav.extensionProviderSlots') }}
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
        <span class="hidden xl:inline">{{ t('admin.extensions.updateCheck.refresh') }}</span>
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
        <span class="hidden xl:inline">{{ t('admin.extensions.refresh') }}</span>
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

  <UAlert
    v-if="canManagePlugins && builtinAutoUpdateLoadFailed"
    color="warning"
    icon="i-lucide-triangle-alert"
    variant="subtle"
    :title="builtinAutoUpdateLoadError || t('admin.extensions.plugins.autoUpdate.loadFailed')"
    class="mb-6"
  />

  <div class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
    <div v-if="plugins.length === 0 && !pending" class="p-10">
      <SFEmptyState icon-label="PLG" :title="t('admin.extensions.plugins.emptyTitle')" :description="t('admin.extensions.plugins.emptyDescription')" />
    </div>
    <div v-else class="divide-y divide-slate-200 dark:divide-zinc-800">
      <div
        v-for="{ item, display } in pluginRows"
        :key="item.id"
        class="grid gap-4 px-4 py-4 md:grid-cols-[1fr_auto]"
      >
        <div class="min-w-0">
          <SFAdminExtensionIdentity
            :extension="item"
            :display="display"
            :status-label="statusLabel(item.status)"
            :status-color="statusColor(item.status)"
            icon="i-lucide-plug"
            :badges="updateSurfaces.badgesFor(item.id)"
          >
            <template #badges>
              <UBadge :color="runtimeColor(item.runtime?.state)" variant="subtle">
                {{ t(runtimeStatusLabelKey(item)) }}
              </UBadge>
              <UBadge
                v-if="isBuiltinMaintainedExtension(item)"
                color="neutral"
                variant="subtle"
                icon="i-lucide-package-check"
                :title="t('admin.extensions.plugins.autoUpdate.builtinMaintainedHint')"
              >
                {{ t('admin.extensions.plugins.autoUpdate.builtinMaintained') }}
              </UBadge>
              <UBadge
                v-if="shouldShowBuiltinAutoUpdateDisabled(item, builtinAutoUpdateState)"
                color="neutral"
                variant="outline"
                icon="i-lucide-pause"
                :title="t('admin.extensions.plugins.autoUpdate.disabledHint')"
              >
                {{ t('admin.extensions.plugins.autoUpdate.disabledBadge') }}
              </UBadge>
              <UBadge
                v-if="item.stagedVersion"
                color="warning"
                variant="outline"
                icon="i-lucide-package-plus"
                :title="stagedVersionHint(item)"
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
              <UBadge
                v-if="item.runtime?.protocolDeprecated"
                color="warning"
                variant="subtle"
                icon="i-lucide-history"
              >
                {{ t('admin.extensions.runtime.protocolDeprecated', { version: item.runtime.protocolVersion }) }}
              </UBadge>
              <UBadge
                :color="extensionSettingsPresentation(item).color"
                variant="subtle"
                :icon="extensionSettingsPresentation(item).icon"
              >
                {{ t(extensionSettingsPresentation(item).labelKey) }}
              </UBadge>
              <UBadge
                v-if="item.runtime?.circuitOpen"
                color="warning"
                variant="subtle"
                icon="i-lucide-zap-off"
              >
                {{ t('admin.extensions.runtime.circuitOpen') }}
              </UBadge>
              <UBadge
                v-if="item.runtime?.memoryBytes"
                color="neutral"
                variant="subtle"
                icon="i-lucide-memory-stick"
                :title="t('admin.extensions.runtime.memoryHint')"
              >
                {{ t('admin.extensions.runtime.memory', { size: formatPluginMemoryBytes(item.runtime.memoryBytes) }) }}
              </UBadge>
            </template>
            <template #details>
              <p
                v-if="item.runtime?.protocolVersion"
                class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-slate-500 dark:text-zinc-400"
              >
                <span>{{ t('admin.extensions.runtime.protocol', { transport: item.runtime.protocolTransport, version: item.runtime.protocolVersion }) }}</span>
                <span>{{ t('admin.extensions.runtime.protocolStarts', { count: item.runtime.protocolStartCount || 0 }) }}</span>
                <span>{{ t('admin.extensions.runtime.protocolCalls', { count: item.runtime.protocolCallCount || 0 }) }}</span>
              </p>
              <p
                v-if="item.runtime?.state === 'degraded' || item.runtime?.consecutiveFailures"
                class="mt-1 text-xs text-amber-700 dark:text-amber-300"
              >
                <span v-if="item.runtime?.consecutiveFailures">
                  {{ t('admin.extensions.runtime.failures', { count: item.runtime.consecutiveFailures }) }}
                </span>
                <span v-if="item.runtime?.lastFailureReason" class="ml-1">
                  · {{ t('admin.extensions.runtime.lastFailure', { reason: item.runtime.lastFailureReason }) }}
                </span>
              </p>
            </template>
            <template #footer>
              <p class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-slate-500 dark:text-zinc-400">
                <span>{{ t('admin.extensions.capability.routes', { count: runtimeCapabilitySummary(item).routes }) }}</span>
                <span>{{ t('admin.extensions.capability.hooks', { count: runtimeCapabilitySummary(item).hooks }) }}</span>
                <span>{{ t('admin.extensions.capability.events', { count: runtimeCapabilitySummary(item).events }) }}</span>
                <span>{{ t('admin.extensions.capability.providers', { count: runtimeCapabilitySummary(item).providers }) }}</span>
              </p>
              <p v-if="item.runtime?.lastError" class="mt-1 truncate text-xs text-red-600 dark:text-red-400">
                {{ item.runtime.lastError }}
              </p>
              <SFAdminExtensionHostActionsNotice :extension="item" />
              <SFAdminFrontendTrustPanel :extension="item" />
            </template>
          </SFAdminExtensionIdentity>
        </div>
        <div class="flex flex-wrap items-center gap-2 [&>a]:shrink-0 [&>button]:shrink-0">
          <UButton
            v-if="item.stagedVersion"
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
            v-if="storageInstancesRoute(item)"
            size="sm"
            color="neutral"
            variant="subtle"
            icon="i-lucide-database"
            :to="item.status === 'enabled' ? storageInstancesRoute(item) : undefined"
            :disabled="item.status !== 'enabled'"
            :title="item.status === 'enabled' ? t('admin.extensions.configureStorage') : t('admin.extensions.configureStorageEnableFirst')"
          >
            {{ t('admin.extensions.configureStorage') }}
          </UButton>
          <UButton
            size="sm"
            color="neutral"
            variant="ghost"
            icon="i-lucide-history"
            :title="t('admin.extensions.lifecycle.title')"
            :aria-label="t('admin.extensions.lifecycle.title')"
            @click="openLifecycleExtension(item)"
          />
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
            v-if="item.status !== 'enabled'"
            size="sm"
            icon="i-lucide-play"
            :disabled="!isExtensionArtifactAvailable(item)"
            :loading="busyId === item.id"
            @click="enableExtension(item)"
          >
            {{ t('admin.extensions.enable') }}
          </UButton>
          <UButton
            v-else
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
            v-if="item.isDeletable && item.source !== 'builtin' && !item.isSystem"
            size="sm"
            color="error"
            variant="ghost"
            icon="i-lucide-trash-2"
            :disabled="item.status === 'enabled' && !isLifecycleV2Plugin(item)"
            :loading="busyId === item.id"
            :title="item.status === 'enabled' && !isLifecycleV2Plugin(item) ? t('admin.extensions.confirmUninstallBody', { name: item.name }) : t('admin.extensions.uninstall')"
            @click="openUninstallExtension(item)"
          >
            {{ t('admin.extensions.uninstall') }}
          </UButton>
        </div>
      </div>
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
  </div>
</template>
