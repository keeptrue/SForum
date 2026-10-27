<script setup lang="ts">
import { useAdminExtensionsManager } from '~/composables/admin/useAdminExtensionsManager'
import { useExtensionRowMenu } from '~/composables/admin/useExtensionRowMenu'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import SFAdminExtensionEnableDialog from '~/components/admin/SFAdminExtensionEnableDialog.vue'
import SFAdminExtensionUninstallDialog from '~/components/admin/SFAdminExtensionUninstallDialog.vue'
import SFAdminExtensionHostActionsNotice from '~/components/admin/SFAdminExtensionHostActionsNotice.vue'
import SFAdminExtensionIdentity from '~/components/admin/extensions/SFAdminExtensionIdentity.vue'
import SFAdminExtensionPrimaryAction from '~/components/admin/extensions/SFAdminExtensionPrimaryAction.vue'
import { useAdminListSurfaces } from '~/composables/admin/useAdminListSurfaces'
import SFAdminThemeActivateDialog from '~/components/admin/SFAdminThemeActivateDialog.vue'
import { extensionLocalizedDisplay, extensionSettingsPresentation, filterExtensionsByType, isExtensionArtifactAvailable, themeStatusLabelKey } from '~/utils/admin/adminExtensions'
import { resolveExtensionUpdateState } from '~/utils/admin/extensions/updateState'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensionThemes'
})

const { t, locale } = useI18n()
const adminPage = useAdminPage('/extensions/themes')
const {
  extensions,
  pending,
  error,
  refresh,
  busyId,
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
  statusColor
} = await useAdminExtensionsManager()

const themes = computed(() => filterExtensionsByType(extensions.value, 'theme'))
// 与当前 UI 语言绑定，切换语言时主题名称/描述会立刻重算。
const themeRows = computed(() => themes.value.map((item) => ({
  item,
  display: extensionLocalizedDisplay(item, locale.value)
})))

function themeLabel(item: (typeof themes.value)[number]) {
  return t(themeStatusLabelKey(item))
}

// 主题列表同样按 resource 暴露给 Admin Surface：list_column 的单元格渲染成徽章，
// links 提供到宿主页面的深链。
const updateSurfaces = useAdminListSurfaces({
  pageId: '/extensions/themes',
  resources: computed(() => themes.value.map(item => ({
    id: item.id,
    attributes: {
      version: item.version,
      stagedVersion: item.stagedVersion?.version || '',
      type: item.type,
      status: item.status,
      source: item.source || ''
    }
  }))),
  context: { resourceType: 'extension', page: 'themes' },
  refreshHost: refresh
})

const surfaceFailures = computed(() => updateSurfaces.failures.value.filter(item => item.id !== 'catalog'))

async function refreshAll() {
  await refresh()
  await updateSurfaces.refreshSurfaces()
}

const onlyWithUpdates = ref(false)
const rowBadges = computed(() => new Map(themeRows.value.map(row => [row.item.id, updateSurfaces.badgesFor(row.item.id)])))

// 命中条件 = 核心更新状态非空 或 商城徽章；制品缺失两者都不算。
function hasUpdates(item: (typeof themes.value)[number]) {
  // 制品缺失时任何应用动作都会失败，不计入「只看有更新」。
  if (!isExtensionArtifactAvailable(item)) return false
  return resolveExtensionUpdateState(item).kind !== 'none' || (rowBadges.value.get(item.id)?.length ?? 0) > 0
}

const updateCount = computed(() => themeRows.value.filter(row => hasUpdates(row.item)).length)
const visibleRows = computed(() => onlyWithUpdates.value
  ? themeRows.value.filter(row => hasUpdates(row.item))
  : themeRows.value)
const updateFilterAria = computed(() => updateCount.value > 0
  ? t('admin.extensions.update.onlyWithUpdatesAria', { count: updateCount.value })
  : t('admin.extensions.update.onlyWithUpdatesAriaEmpty'))

const rowMenu = useExtensionRowMenu({
  canUninstall: item => Boolean(item.isDeletable) && item.source !== 'builtin' && !item.isSystem && item.status !== 'enabled',
  uninstall: openUninstallExtension
})
const rowMenus = computed(() => Object.fromEntries(
  visibleRows.value.map(row => [row.item.id, rowMenu(row.item)])
))

useSeoMeta({
  title: t('admin.extensions.themes.metaTitle')
})
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.extensions.themes.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.extensions.themes.intro') }}
    </p>
  </div>

  <UDashboardToolbar class="border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 rounded-lg px-4 py-2.5 mb-6 text-slate-500 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 text-sm">
        <div class="flex min-w-0 items-center gap-2">
          <UIcon name="i-lucide-palette" class="size-4" />
          <span class="hidden truncate sm:inline">{{ t('admin.extensions.themes.count', { count: themes.length }) }}</span>
        </div>
        <div class="flex items-center gap-1.5">
          <UIcon name="i-lucide-sparkles" class="size-4 shrink-0" aria-hidden="true" />
          <span class="hidden whitespace-nowrap text-xs xl:inline">{{ t('admin.extensions.update.onlyWithUpdates') }}</span>
          <USwitch
            :model-value="onlyWithUpdates"
            size="sm"
            :aria-label="updateFilterAria"
            :title="t('admin.extensions.update.onlyWithUpdatesHint')"
            @update:model-value="onlyWithUpdates = Boolean($event)"
          />
        </div>
      </div>
    </template>
    <template #right>
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
      <UButton icon="i-lucide-rotate-cw" color="neutral" variant="subtle" :loading="pending" @click="refreshAll()">
        {{ t('admin.extensions.refresh') }}
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

  <div class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
    <div v-if="themes.length === 0 && !pending" class="p-10">
      <SFEmptyState icon-label="THM" :title="t('admin.extensions.themes.emptyTitle')" :description="t('admin.extensions.themes.emptyDescription')" />
    </div>
    <div v-else-if="visibleRows.length === 0" class="p-10">
      <SFEmptyState
        icon-label="OK"
        :title="surfaceFailures.length
          ? t('admin.extensions.update.noneInView')
          : t('admin.extensions.update.allUpToDate')"
        :description="surfaceFailures.length
          ? t('admin.extensions.update.noneInViewDescription')
          : t('admin.extensions.update.allUpToDateDescription', { count: themes.length })"
        :action-label="t('admin.extensions.update.showAll')"
        @action="onlyWithUpdates = false"
      />
    </div>
    <div v-else class="divide-y divide-slate-200 dark:divide-zinc-800">
      <div
        v-for="{ item, display } in visibleRows"
        :key="item.id"
        class="grid gap-4 px-4 py-4 md:grid-cols-[minmax(0,1fr)_auto]"
      >
        <div class="min-w-0">
          <SFAdminExtensionIdentity
            :extension="item"
            :display="display"
            :status-label="themeLabel(item)"
            :status-color="statusColor(item.status)"
            icon="i-lucide-palette"
            :badges="rowBadges.get(item.id) || []"
          >
            <template #status-badges>
              <UBadge
                v-if="!isExtensionArtifactAvailable(item)"
                color="error"
                variant="subtle"
                icon="i-lucide-package-x"
              >
                {{ t('admin.extensions.artifact.missing') }}
              </UBadge>
            </template>
            <template #badges>
              <UBadge v-if="item.source === 'builtin'" color="primary" variant="subtle" icon="i-lucide-shield-check">
                {{ t('admin.extensions.source.builtin') }}
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
                :color="extensionSettingsPresentation(item).color"
                variant="subtle"
                :icon="extensionSettingsPresentation(item).icon"
              >
                {{ t(extensionSettingsPresentation(item).labelKey) }}
              </UBadge>
            </template>
            <template #footer>
              <SFAdminExtensionHostActionsNotice :extension="item" />
            </template>
          </SFAdminExtensionIdentity>
        </div>
        <div class="flex flex-wrap items-center gap-1">
          <SFAdminExtensionPrimaryAction
            :extension="item"
            :busy="busyId === item.id"
            @activate="activateTheme(item)"
          />
          <UDropdownMenu
            :items="rowMenus[item.id] || []"
            :content="{ align: 'end' }"
          >
            <template #default="{ open }">
              <UButton
                icon="i-lucide-ellipsis"
                color="neutral"
                variant="ghost"
                size="sm"
                :aria-label="t('admin.extensions.update.moreActions', { name: display.name })"
                :title="t('admin.extensions.update.moreActions', { name: display.name })"
                :aria-expanded="open"
              />
            </template>
          </UDropdownMenu>
        </div>
      </div>
    </div>

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

    <!-- Executable (L2) theme activation reuses the exact trust challenge dialog pattern. -->
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
  </div>
</template>
