<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { useAdminExtensionsManager } from '~/composables/admin/useAdminExtensionsManager'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import SFAdminExtensionEnableDialog from '~/components/admin/SFAdminExtensionEnableDialog.vue'
import SFAdminExtensionUninstallDialog from '~/components/admin/SFAdminExtensionUninstallDialog.vue'
import SFAdminExtensionHostActionsNotice from '~/components/admin/SFAdminExtensionHostActionsNotice.vue'
import SFAdminExtensionIdentity from '~/components/admin/extensions/SFAdminExtensionIdentity.vue'
import { useAdminListSurfaces } from '~/composables/admin/useAdminListSurfaces'
import SFAdminThemeActivateDialog from '~/components/admin/SFAdminThemeActivateDialog.vue'
import { extensionLocalizedDisplay, extensionManageRoute, extensionSettingsPresentation, filterExtensionsByType, isExtensionArtifactAvailable, themeActionState, themeStatusLabelKey } from '~/utils/admin/adminExtensions'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensionThemes'
})

const { t, locale } = useI18n()
const adminPage = useAdminPage('/extensions/themes')
const adminRoutes = useAdminRoutes()
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
      <div class="flex min-w-0 items-center gap-2 text-sm">
        <UIcon name="i-lucide-palette" class="size-4" />
        <span class="truncate">{{ t('admin.extensions.themes.count', { count: themes.length }) }}</span>
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
    <div v-else class="divide-y divide-slate-200 dark:divide-zinc-800">
      <div
        v-for="{ item, display } in themeRows"
        :key="item.id"
        class="grid gap-4 px-4 py-4 md:grid-cols-[1fr_auto]"
      >
        <div class="min-w-0">
          <SFAdminExtensionIdentity
            :extension="item"
            :display="display"
            :status-label="themeLabel(item)"
            :status-color="statusColor(item.status)"
            icon="i-lucide-palette"
            :badges="updateSurfaces.badgesFor(item.id)"
          >
            <template #badges>
              <UBadge v-if="item.source === 'builtin'" color="primary" variant="subtle" icon="i-lucide-shield-check">
                {{ t('admin.extensions.source.builtin') }}
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
        <div class="flex items-center gap-2">
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
            v-if="themeActionState(item) === 'activateDefault'"
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
            v-if="item.isDeletable && item.source !== 'builtin' && !item.isSystem && item.status !== 'enabled'"
            size="sm"
            color="error"
            variant="ghost"
            icon="i-lucide-trash-2"
            :loading="busyId === item.id"
            @click="openUninstallExtension(item)"
          >
            {{ t('admin.extensions.uninstall') }}
          </UButton>
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
