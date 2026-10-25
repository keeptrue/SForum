<script setup lang="ts">
import { useAdminTabs } from '~/composables/admin/useAdminTabs'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import SFExtensionSettingsRenderer from '~/components/extensions/settings/SFExtensionSettingsRenderer.vue'
import SFTrustedSettingsComponent from '~/components/extensions/settings/SFTrustedSettingsComponent.vue'
import SFTrustedAdminPageComponent from '~/components/extensions/admin/SFTrustedAdminPageComponent.vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import {
  extensionAdminPageRoute,
  extensionAffectsPublicSurface,
  extensionLocalizedDisplay,
  findExtensionAdminPage,
  normalizeExtensionPagePath,
  recommendedExtensionSettingValues,
  type AdminExtension,
  type AdminExtensionPageBootstrap,
  type AdminExtensionSettings,
  type AdminExtensionSettingsAction,
  type AdminExtensionSettingsActionResult
} from '~/utils/admin/adminExtensions'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensionDynamicPage'
})

const { t, locale } = useI18n()
const route = useRoute()
const adminRoutes = useAdminRoutes()
const adminTabs = useAdminTabs()
const toast = useToast()
const { request } = useApiClient()

const extensionId = computed(() => {
  const value = route.params.extensionId
  return Array.isArray(value) ? value[0] || '' : `${value || ''}`
})
const currentPagePath = computed(() => normalizeExtensionPagePath(route.params.pagePath as string[] | string | undefined))

type AdminExtensionPageState = {
  requestKey: string
  bootstrap: AdminExtensionPageBootstrap
}

// 列表缓存只负责让 SPA 导航立即显示标题；Host bootstrap 仍是页面类型和
// Settings Document 的最终权威，避免按 URL 猜测任意 manifest 页面。
const { data: cachedExtensions } = useNuxtData<AdminExtension[]>('admin-extensions')
const cachedExtension = computed(() => cachedExtensions.value?.find(item => item.id === extensionId.value))
const pageDataKey = computed(() => `admin-extension-page-bootstrap:${extensionId.value}:${currentPagePath.value}:${locale.value}`)
const {
  data: pageState,
  pending: loadingPage,
  error,
  refresh
} = await useAsyncData<AdminExtensionPageState>(
  pageDataKey,
  async () => {
    const requestKey = pageDataKey.value
    const requestedExtensionId = extensionId.value
    const requestedPagePath = currentPagePath.value
    const bootstrap = await request<AdminExtensionPageBootstrap>(
      `/admin/extensions/${encodeURIComponent(requestedExtensionId)}/page-bootstrap?path=${encodeURIComponent(requestedPagePath)}`
    )
    return { requestKey, bootstrap }
  },
  {
    deep: false,
    lazy: true
  }
)

// Nuxt 会把 reactive key 的旧数据暂存到新 key。用请求启动时的精确 key
// 拒绝跨扩展、跨页面或跨 locale 的旧 bootstrap，避免错误设置表单闪现。
const pageBootstrap = computed<AdminExtensionPageBootstrap | undefined>({
  get: () => pageState.value?.requestKey === pageDataKey.value ? pageState.value.bootstrap : undefined,
  set: (next) => {
    const current = pageState.value
    if (!next || !current || current.requestKey !== pageDataKey.value) return
    pageState.value = { ...current, bootstrap: next }
  }
})

const extension = computed(() => pageBootstrap.value?.extension || cachedExtension.value)
const extensionDisplay = computed(() => extension.value ? extensionLocalizedDisplay(extension.value, locale.value) : null)
const adminPage = computed(() => {
  if (pageBootstrap.value) {
    if (!pageBootstrap.value.page) return undefined
    return findExtensionAdminPage(pageBootstrap.value.extension, currentPagePath.value, locale.value)
      || pageBootstrap.value.page
  }
  return cachedExtension.value
    ? findExtensionAdminPage(cachedExtension.value, currentPagePath.value, locale.value)
    : undefined
})
const pending = computed(() => loadingPage.value && !extension.value)
const formValues = reactive<Record<string, string>>({})
const savingSettings = ref(false)
const actionLoading = reactive<Record<string, boolean>>({})
const actionResults = reactive<Record<string, AdminExtensionSettingsActionResult | undefined>>({})

const pageTitle = computed(() => adminPage.value?.label || extensionDisplay.value?.name || t('admin.extensions.dynamic.notFoundTitle'))
const pageDescription = computed(() => adminPage.value?.description || extensionDisplay.value?.description || '')
// bootstrap 已解析的信任结论：传给组件视图/设置视图，避免首帧把"未知"渲染成"未授权"。
const pageFrontendStatus = computed(() => pageBootstrap.value?.frontend || null)
const pageHostIdentity = computed(() => pageBootstrap.value?.host || undefined)
const isSettingsView = computed(() => adminPage.value?.view === 'settings')
const isComponentView = computed(() => adminPage.value?.view === 'component')
// 插件/主题未启用时：允许查看 about，但功能性设置页与贡献组件不可用。
const isExtensionActive = computed(() => extension.value?.status === 'enabled')
const dynamicTabHydrated = ref(false)

const loadingSettings = computed(() => loadingPage.value)
const settingsError = error
const settings = computed<AdminExtensionSettings | undefined>({
  get: () => pageBootstrap.value?.settings || undefined,
  set: (next) => {
    if (pageBootstrap.value) {
      pageBootstrap.value = { ...pageBootstrap.value, settings: next || null }
    }
  }
})

// SSR payload 或客户端 lazy 请求负责初次 bootstrap；挂载后不再无条件 refresh，
// 避免刚完成水合的设置表单被卸载再挂载。需要最新状态时使用显式刷新入口。
onMounted(() => {
  dynamicTabHydrated.value = true
  syncDynamicExtensionTab()
})

const recommendedApplied = computed(() => {
  const items = settings.value?.items || []
  if (!items.length) {
    return true
  }
  const recommended = recommendedExtensionSettingValues(items)
  return Object.entries(recommended).every(([key, value]) => (formValues[key] ?? '') === value)
})

const hasSecretFields = computed(() => (settings.value?.items || []).some(item => item.type === 'secret'))
const hasPrebuiltSettingsComponent = computed(() => settings.value?.renderer.component?.kind === 'prebuilt')

const settingsComponentContext = computed(() => ({
  items: settings.value?.items || [],
  values: formValues,
  updateValue: (key: string, value: string) => {
    formValues[key] = value
  },
  save: saveSettings,
  reset: resetSettings
}))

useSeoMeta({
  title: pageTitle
})

function syncDynamicExtensionTab() {
  const item = extension.value
  const page = adminPage.value
  if (!item || !page) {
    return
  }
  const id = extensionAdminPageRoute(item.id, page.path)
  adminTabs.openCustomTab({
    id,
    label: page.label,
    to: adminRoutes.path(id),
    icon: page.icon || (item.type === 'theme' ? 'i-lucide-palette' : 'i-lucide-plug'),
    closable: true,
    componentName: 'AdminExtensionDynamicPage'
  })
}

watch([extension, adminPage], () => {
  // SSR 与首次客户端水合都保留路由占位 tab；正式扩展标题在 mounted 后同步，避免标题和图标不一致。
  if (dynamicTabHydrated.value) {
    syncDynamicExtensionTab()
  }
})

watch(settings, (next) => {
  Object.keys(formValues).forEach((key) => {
    delete formValues[key]
  })
  for (const item of next?.items || []) {
    formValues[item.key] = item.value
  }
}, { immediate: true })

watch(settingsError, (current) => {
  if (current && import.meta.client) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(current) || t('admin.extensions.dynamic.settingsLoadFailed'),
      duration: 0
    })
  }
})

async function loadSettings() {
  await refresh()
}

function settingsSuccessToast(kind: 'saved' | 'reset') {
  const affectsPublic = extensionAffectsPublicSurface(extension.value)
  const title = kind === 'saved'
    ? t('admin.extensions.dynamic.settingsSaved')
    : t('admin.extensions.dynamic.settingsReset')
  toast.add({
    color: 'success',
    icon: kind === 'saved' ? 'i-lucide-save' : 'i-lucide-rotate-ccw',
    title,
    // 影响公开贡献时说明：刷新帖子页即可，无需重新激活主题；revision bump 已更新匿名 SWR 键。
    description: affectsPublic ? t('admin.extensions.dynamic.settingsPublicSurfaceHint') : undefined,
    duration: 5000
  })
}

async function saveSettings() {
  savingSettings.value = true
  try {
    const updated = await request<AdminExtensionSettings>(`/admin/extensions/${extensionId.value}/settings`, {
      method: 'PUT',
      body: { values: { ...formValues } }
    })
    settings.value = updated
    for (const item of updated.items) {
      formValues[item.key] = item.value
    }
    settingsSuccessToast('saved')
  } catch (error) {
    // 错误 Toast 不自动关闭，便于运营读完再处理。
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('admin.extensions.dynamic.settingsSaveFailed'),
      duration: 0
    })
  } finally {
    savingSettings.value = false
  }
}

async function resetSettings() {
  savingSettings.value = true
  try {
    const updated = await request<AdminExtensionSettings>(`/admin/extensions/${extensionId.value}/settings`, {
      method: 'PUT',
      body: { values: recommendedExtensionSettingValues(settings.value?.items || []) }
    })
    settings.value = updated
    for (const item of updated.items) {
      formValues[item.key] = item.value
    }
    settingsSuccessToast('reset')
  } catch (error) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('admin.extensions.dynamic.settingsSaveFailed'),
      duration: 0
    })
  } finally {
    savingSettings.value = false
  }
}

function updateSettingValue(key: string, value: string) {
  formValues[key] = value
}

async function executeSettingsAction(action: AdminExtensionSettingsAction) {
  if (!settings.value || !action.available) return
  actionLoading[action.id] = true
  actionResults[action.id] = undefined
  const values: Record<string, string> = {}
  const secrets: Record<string, { mode: 'preserve' | 'replace', value?: string }> = {}
  const allowed = new Set(action.fields?.length ? action.fields : settings.value.items.map(item => item.key))
  for (const item of settings.value.items) {
    if (!allowed.has(item.key)) continue
    if (item.type === 'secret') {
      const draft = formValues[item.key] || ''
      secrets[item.key] = draft ? { mode: 'replace', value: draft } : { mode: 'preserve' }
    } else {
      values[item.key] = formValues[item.key] ?? ''
    }
  }
  try {
    const result = await request<AdminExtensionSettingsActionResult>(`/admin/extensions/${extensionId.value}/settings/actions/${action.id}`, {
      method: 'POST',
      body: { values, secrets }
    })
    actionResults[action.id] = result
    if (result.success) {
      toast.add({ color: 'success', icon: 'i-lucide-activity', title: result.message || action.label, duration: 5000 })
      setTimeout(() => {
        if (actionResults[action.id] === result) actionResults[action.id] = undefined
      }, 10000)
    }
  } catch (error) {
    const message = apiErrorMessage(error) || t('admin.extensions.dynamic.actionFailed')
    actionResults[action.id] = { success: false, reason: 'request_failed', message, durationMs: 0 }
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: message })
  } finally {
    actionLoading[action.id] = false
  }
}

</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage?.icon || 'i-lucide-blocks'" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ pageTitle }}
    </h2>
    <p v-if="pageDescription" class="text-sm text-slate-500 dark:text-zinc-400">
      {{ pageDescription }}
    </p>
  </div>

  <UAlert
    v-if="error"
    color="error"
    icon="i-lucide-triangle-alert"
    variant="subtle"
    :title="apiErrorMessage(error) || t('admin.extensions.loadFailed')"
    class="mb-6"
  />

  <div v-if="pending" class="rounded-lg border border-slate-200 bg-white p-8 text-sm text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
    {{ t('admin.extensions.dynamic.loading') }}
  </div>

  <div v-else-if="!extension || !adminPage" class="rounded-lg border border-slate-200 bg-white p-10 dark:border-zinc-800 dark:bg-zinc-900">
    <SFEmptyState icon-label="EXT" :title="t('admin.extensions.dynamic.notFoundTitle')" :description="t('admin.extensions.dynamic.notFoundDescription')" />
    <div class="mt-5 flex justify-center">
      <UButton icon="i-lucide-rotate-cw" color="neutral" variant="subtle" :loading="pending" @click="refresh()">
        {{ t('admin.extensions.refresh') }}
      </UButton>
    </div>
  </div>

  <div v-else-if="isSettingsView" class="space-y-4">
    <UAlert
      v-if="!isExtensionActive"
      color="warning"
      variant="subtle"
      icon="i-lucide-shield-check"
      :title="t('admin.extensions.dynamic.configureBeforeEnableTitle')"
      :description="t('admin.extensions.dynamic.configureBeforeEnableDescription')"
    />
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-2">
          <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.extensions.dynamic.settingsTitle') }}
          </h3>
          <UBadge v-if="hasPrebuiltSettingsComponent" color="neutral" variant="subtle" size="sm">
            {{ t('admin.extensions.dynamic.customPageBadge') }}
          </UBadge>
          <UBadge v-else-if="settings?.renderer.source === 'legacy_array'" color="neutral" variant="subtle" size="sm">
            {{ t('admin.extensions.dynamic.compatBadge') }}
          </UBadge>
          <UBadge v-else color="primary" variant="subtle" size="sm">
            {{ t('admin.extensions.dynamic.schemaBadge') }}
          </UBadge>
        </div>
        <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.extensions.dynamic.settingsIntro') }}
        </p>
      </div>
      <UButton icon="i-lucide-rotate-cw" color="neutral" variant="ghost" :loading="loadingSettings" @click="loadSettings">
        {{ t('admin.extensions.refresh') }}
      </UButton>
    </div>

    <!-- 宿主通用表单：字段标签/说明来自 API 已按 locale 解析的 settings -->
    <SFTrustedSettingsComponent
      v-if="extension && settings && hasPrebuiltSettingsComponent"
      :extension="extension"
      :settings="settings"
      :initial-status="pageFrontendStatus"
      :host="pageHostIdentity"
      :context="settingsComponentContext"
    >
      <template #fallback>
        <SFExtensionSettingsRenderer
          :settings="settings"
          :values="formValues"
          :loading="loadingSettings"
          :saving="savingSettings"
          :recommended-applied="recommendedApplied"
          :action-loading="actionLoading"
          :action-results="actionResults"
          @update="updateSettingValue"
          @save="saveSettings"
          @reset="resetSettings"
          @action="executeSettingsAction"
        />
      </template>
    </SFTrustedSettingsComponent>
    <SFExtensionSettingsRenderer
      v-else
      :settings="settings || null"
      :values="formValues"
      :loading="loadingSettings"
      :saving="savingSettings"
      :recommended-applied="recommendedApplied"
      :action-loading="actionLoading"
      :action-results="actionResults"
      @update="updateSettingValue"
      @save="saveSettings"
      @reset="resetSettings"
      @action="executeSettingsAction"
    />
  </div>

  <div v-else-if="isComponentView && extension && adminPage" class="min-w-0">
    <UAlert
      v-if="!isExtensionActive"
      color="warning"
      variant="subtle"
      icon="i-lucide-power"
      :title="t('admin.extensions.dynamic.configureBeforeEnableTitle')"
      :description="t('admin.extensions.dynamic.configureBeforeEnableDescription')"
    />
    <SFTrustedAdminPageComponent
      v-else
      :extension="extension"
      :page="adminPage"
      :initial-status="pageFrontendStatus"
      :host="pageHostIdentity"
    />
  </div>

  <div v-else class="grid gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
    <div class="rounded-lg border border-slate-200 bg-white p-5 dark:border-zinc-800 dark:bg-zinc-900">
      <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
        {{ extensionDisplay?.name }}
      </h3>
      <p class="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-300">
        {{ extensionDisplay?.description }}
      </p>
      <div class="mt-5 flex flex-wrap gap-2">
        <UButton :to="extension.manifest.url" target="_blank" color="neutral" variant="subtle" icon="i-lucide-external-link">
          {{ t('admin.extensions.dynamic.website') }}
        </UButton>
        <UButton
          v-if="extension.manifest.author?.url"
          :to="extension.manifest.author.url"
          target="_blank"
          color="neutral"
          variant="ghost"
          icon="i-lucide-user-round"
        >
          {{ extension.manifest.author.name }}
        </UButton>
      </div>
    </div>

    <div class="rounded-lg border border-slate-200 bg-white p-5 dark:border-zinc-800 dark:bg-zinc-900">
      <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
        {{ t('admin.extensions.dynamic.packageInfo') }}
      </h3>
      <dl class="mt-4 space-y-3 text-sm">
        <div class="flex items-center justify-between gap-4">
          <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.dynamic.type') }}</dt>
          <dd class="font-medium text-slate-900 dark:text-zinc-100">{{ extension.type }}</dd>
        </div>
        <div class="flex items-center justify-between gap-4">
          <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.dynamic.version') }}</dt>
          <dd class="font-medium text-slate-900 dark:text-zinc-100">{{ extension.version }}</dd>
        </div>
        <div class="flex items-center justify-between gap-4">
          <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.dynamic.status') }}</dt>
          <dd class="font-medium text-slate-900 dark:text-zinc-100">{{ extension.status }}</dd>
        </div>
        <div class="flex items-center justify-between gap-4">
          <dt class="text-slate-500 dark:text-zinc-400">{{ t('admin.extensions.dynamic.author') }}</dt>
          <dd class="truncate font-medium text-slate-900 dark:text-zinc-100">{{ extension.manifest.author?.name }}</dd>
        </div>
      </dl>
    </div>
  </div>
</template>
