<script setup lang="ts">
import SFExtensionSettingsRenderer from '~/components/extensions/settings/SFExtensionSettingsRenderer.vue'
import { useAdminExtensionSettingsDraft } from '~/composables/admin/useAdminExtensionSettingsDraft'
import type { AdminExtensionSettings, AdminSettingsSurfaceTab } from '~/utils/admin/adminExtensions'
import { resolveLocalizedText } from '~/utils/settings/profileSettings'
/**
 * 站点设置页里的插件设置 tab（`type=settings`）。
 *
 * 只做宿主容器：标题、来源徽标、以及「保存只作用于该插件」的说明；
 * 表单本身仍然是既有 SFExtensionSettingsRenderer（与扩展设置页同一份渲染器）。
 * 保存/重置/动作由 useAdminExtensionSettingsDraft 自管，不参与站点设置页的
 * `webOptions` 存取流程 —— 插件设置不是宿主的 option 行。
 */
const props = defineProps<{
  tab: AdminSettingsSurfaceTab
}>()

const { t, locale } = useI18n()
const tabLabel = computed(() => resolveLocalizedText(props.tab.label, locale.value, props.tab.id))

const settings = computed<AdminExtensionSettings | null>(() => props.tab.settings || null)
const {
  values,
  saving,
  loading,
  recommendedApplied,
  actionLoading,
  actionResults,
  updateValue,
  save,
  reset,
  executeAction
} = useAdminExtensionSettingsDraft({ settings })
</script>

<template>
  <div class="min-w-0 space-y-4" data-testid="admin-settings-extension-tab">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <p class="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-zinc-100">
          <UIcon :name="tab.icon || 'i-lucide-plug'" class="size-4 text-[var(--sf-accent)]" />
          <span class="min-w-0 break-all">{{ tabLabel }}</span>
          <UBadge color="neutral" variant="subtle">
            {{ t('admin.settings.contributions.badge') }}
          </UBadge>
        </p>
        <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.settings.contributions.extensionHint', { extensionId: tab.extensionId }) }}
        </p>
      </div>
    </div>

    <SFExtensionSettingsRenderer
      :settings="settings"
      :values="values"
      :loading="loading"
      :saving="saving"
      :recommended-applied="recommendedApplied"
      :action-loading="actionLoading"
      :action-results="actionResults"
      @update="updateValue"
      @save="save"
      @reset="reset"
      @action="executeAction"
    />
  </div>
</template>
