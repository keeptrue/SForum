<script setup lang="ts">
import type { Component } from 'vue'
import type { AdminWebOption } from '~/composables/useWebOptions'
import SFAdminFixedTabNav from '~/components/admin/settings/shared/SFAdminFixedTabNav.vue'
import SFAdminSiteAccountSecurityTab from '~/components/admin/settings/site/tabs/SFAdminSiteAccountSecurityTab.vue'
import SFAdminSiteBasicTab from '~/components/admin/settings/site/tabs/SFAdminSiteBasicTab.vue'
import SFAdminSiteClientTab from '~/components/admin/settings/site/tabs/SFAdminSiteClientTab.vue'
import SFAdminSiteMaintenanceTab from '~/components/admin/settings/site/tabs/SFAdminSiteMaintenanceTab.vue'
import SFAdminSiteNewcomersTab from '~/components/admin/settings/site/tabs/SFAdminSiteNewcomersTab.vue'
import SFAdminSiteRegistrationTab from '~/components/admin/settings/site/tabs/SFAdminSiteRegistrationTab.vue'
import SFAdminSiteUpdatesTab from '~/components/admin/settings/site/tabs/SFAdminSiteUpdatesTab.vue'
import SFAdminSiteVerificationTab from '~/components/admin/settings/site/tabs/SFAdminSiteVerificationTab.vue'
import SFAdminSettingsExtensionTab from '~/components/admin/settings/contributions/SFAdminSettingsExtensionTab.vue'
import { apiErrorMessage, apiErrorStatusCode } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import type { AdminSettingsSurfaceTab } from '~/utils/admin/adminExtensions'
import {
  adminSettingsSurfaceTabsForState,
  adminSurfaceTabsFailureKind,
  adminSurfaceTabsViewState,
  type AdminSurfaceTabsLoadState
} from '~/utils/admin/adminSettingsSurfaceTabs'
import { resolveLocalizedText } from '~/utils/settings/profileSettings'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminSettings'
})

type SettingsTab = 'basic' | 'accountSecurity' | 'registration' | 'newcomers' | 'maintenance' | 'client' | 'verification' | 'updates'

const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()
const { request } = useApiClient()
const { options, fetchAdminEnvelope } = useWebOptions()
const adminPage = useAdminPage('/settings')
const adminRoutes = useAdminRoutes()
const fixedTabIds: SettingsTab[] = ['basic', 'accountSecurity', 'registration', 'newcomers', 'maintenance', 'client', 'verification', 'updates']
// 信任等级已提升为独立页：旧链接 `?tab=trustLevels` 在初始化前重定向，不留幽灵 tab。
if (route.query.tab === 'trustLevels') {
  await navigateTo({
    path: adminRoutes.path('/forum/trust-levels'),
    replace: true
  })
}

const fixedTabs = computed(() => [
  { id: 'basic', label: t('admin.settings.tabs.basic'), icon: 'i-lucide-sliders-horizontal' },
  { id: 'accountSecurity', label: t('admin.settings.tabs.accountSecurity'), icon: 'i-lucide-shield' },
  { id: 'registration', label: t('admin.settings.tabs.registration'), icon: 'i-lucide-user-plus' },
  { id: 'newcomers', label: t('admin.settings.tabs.newcomers'), icon: 'i-lucide-sprout' },
  { id: 'maintenance', label: t('admin.settings.tabs.maintenance'), icon: 'i-lucide-construction' },
  { id: 'client', label: t('admin.settings.tabs.client'), icon: 'i-lucide-smartphone' },
  { id: 'verification', label: t('admin.settings.tabs.verification'), icon: 'i-lucide-shield-check' },
  { id: 'updates', label: t('admin.settings.tabs.updates'), icon: 'i-lucide-refresh-cw' }
])
const tabComponents: Record<SettingsTab, Component> = {
  basic: SFAdminSiteBasicTab,
  accountSecurity: SFAdminSiteAccountSecurityTab,
  registration: SFAdminSiteRegistrationTab,
  newcomers: SFAdminSiteNewcomersTab,
  maintenance: SFAdminSiteMaintenanceTab,
  client: SFAdminSiteClientTab,
  verification: SFAdminSiteVerificationTab,
  updates: SFAdminSiteUpdatesTab
}

type SurfaceTabsLoad =
  | { status: 'ready', tabs: AdminSettingsSurfaceTab[] }
  /** 失败要能解释：403 是「没有扩展查看权限」，其余是「暂时不可用」。 */
  | { status: 'error', forbidden: boolean, message: string }

/**
 * 插件贡献的 `type=settings` 分区：与站内固定 tab 混排在同一根 tab 栏里，
 * id 已由服务端放在 `ext.` 命名空间内，因此未知名回落逻辑不需要猜品牌。
 *
 * 权限归属（已定裁决，不改 RBAC）：插件设置分区属于**扩展管理员面** ——
 * 可见要 `extension.view`，保存要 `extension.plugin.manage`。`RoleOperator`
 * 看不到是正确行为，但他必须被告知原因；因此失败不再吞成空列表，而是带状态
 * 返回，由页面出非阻塞提示。核心 8 个 tab 在任何情况下都照常可用。
 */
const { data: surfaceTabsLoad, pending: surfaceTabsPending, refresh: refreshSurfaceTabs } = await useAsyncData<SurfaceTabsLoad>(
  'admin-settings-surface-tabs',
  async () => {
    try {
      const items = await request<AdminSettingsSurfaceTab[]>('/admin/settings/surface-tabs')
      return { status: 'ready', tabs: Array.isArray(items) ? items : [] }
    } catch (error) {
      return {
        status: 'error',
        forbidden: adminSurfaceTabsFailureKind(apiErrorStatusCode(error)) === 'forbidden',
        message: apiErrorMessage(error)
      }
    }
  },
  { default: () => ({ status: 'ready', tabs: [] }) as SurfaceTabsLoad }
)

const surfaceTabsLoadState = computed<AdminSurfaceTabsLoadState>(() => {
  if (surfaceTabsPending.value) return 'loading'
  return surfaceTabsLoad.value?.status === 'error' ? 'error' : 'ready'
})
const loadedSurfaceTabs = computed<AdminSettingsSurfaceTab[]>(() => (
  surfaceTabsLoad.value?.status === 'ready' ? surfaceTabsLoad.value.tabs : []
))
const surfaceTabsState = computed(() => adminSurfaceTabsViewState(surfaceTabsLoadState.value, loadedSurfaceTabs.value.length))
// 只有 ready 才产出插件 tab：error/loading 时 tab 栏必须与「真的没有分区」一视同仁地空着。
const surfaceTabs = computed<AdminSettingsSurfaceTab[]>(() => (
  adminSettingsSurfaceTabsForState(surfaceTabsState.value, loadedSurfaceTabs.value)
))
const surfaceTabsHint = computed(() => {
  const load = surfaceTabsLoad.value
  const forbidden = load?.status === 'error' && load.forbidden
  return forbidden
    ? t('admin.settings.contributions.visibilityForbidden')
    : t('admin.settings.contributions.visibilityUnavailable')
})
const surfaceTabsDetail = computed(() => (
  surfaceTabsLoad.value?.status === 'error' ? surfaceTabsLoad.value.message : ''
))

const tabs = computed(() => [
  ...fixedTabs.value,
  ...surfaceTabs.value.map(tab => ({
    id: tab.id,
    // 插件 tab 的标签可能是 locale 映射，按当前语言解析后再交给共享 tab 栏。
    label: resolveLocalizedText(tab.label, locale.value, tab.id),
    icon: tab.icon || 'i-lucide-plug'
  }))
])

const activeTab = ref<string>(normalizeTab(route.query.tab))
const activeComponent = computed(() => tabComponents[activeTab.value as SettingsTab])
const activeSurfaceTab = computed(() => surfaceTabs.value.find(tab => tab.id === activeTab.value) || null)

const { data: adminWebOptions, pending, error, refresh } = await useAsyncData<AdminWebOption[]>(
  'admin-web-options',
  async () => (await fetchAdminEnvelope()).data
)

// 插件 tab 可能晚于首屏到达（刷新/权限变化）：到达后按 **URL** 重新归一化。
// 基准必须是 route.query.tab：那时 activeTab 早已回落到 'basic'，拿它当基准会
// 把深链 `?tab=ext.*` 永久丢掉，还会被下面的 watch 改写成 `?tab=basic`。
watch(surfaceTabs, () => {
  const normalized = normalizeTab(route.query.tab ?? activeTab.value)
  if (normalized !== activeTab.value) activeTab.value = normalized
})

watch(() => route.query.tab, (tab) => {
  const normalized = normalizeTab(tab)
  if (normalized !== activeTab.value) activeTab.value = normalized
})

watch(activeTab, async (tab) => {
  if (route.query.tab === tab) return
  await router.replace({ query: { ...route.query, tab } })
})

useSeoMeta({
  title: t('admin.settings.metaTitle')
})

function normalizeTab(value: unknown): string {
  const candidate = Array.isArray(value) ? value[0] : value
  const ids = tabs.value.map(tab => tab.id)
  return typeof candidate === 'string' && ids.includes(candidate) ? candidate : 'basic'
}

function setActiveTab(value: string) {
  activeTab.value = normalizeTab(value)
}

function applySavedOptions(items: AdminWebOption[]) {
  adminWebOptions.value = items
  const publicOptions = items.filter(item => item.public && !item.secret)
  options.value = {
    ...options.value,
    ...Object.fromEntries(publicOptions.map(item => [item.name, item.value]))
  }
}

async function refreshAll() {
  await Promise.all([refresh(), refreshSurfaceTabs()])
}
</script>

<template>
  <div class="mb-4">
    <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.settings.title') }}
    </h2>
  </div>

  <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm">
        <UIcon name="i-lucide-database" class="size-4" />
        <span class="truncate">{{ t('admin.settings.intro') }}</span>
      </div>
    </template>
    <template #right>
      <UButton color="neutral" variant="outline" leading-icon="i-lucide-refresh-cw" :loading="pending" @click="refreshAll()">
        {{ t('admin.settings.refresh') }}
      </UButton>
    </template>
  </UDashboardToolbar>

  <div class="flex flex-col gap-4">
    <UAlert v-if="error" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="t('admin.settings.loadFailed')" />
    <div
      v-if="surfaceTabsState === 'error'"
      data-testid="admin-settings-surface-tabs-hint"
      role="status"
      class="flex flex-col gap-2 rounded-lg border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-600 dark:border-zinc-800 dark:bg-zinc-900/60 dark:text-zinc-300"
    >
      <div class="flex flex-wrap items-center gap-2">
        <UIcon name="i-lucide-info" class="size-4 shrink-0" aria-hidden="true" />
        <span class="font-medium">{{ t('admin.settings.contributions.visibilityTitle') }}</span>
      </div>
      <p class="text-xs leading-relaxed">{{ surfaceTabsHint }}</p>
      <details class="text-xs">
        <summary class="cursor-pointer select-none">{{ t('admin.settings.contributions.visibilityDetails') }}</summary>
        <p class="mt-1 break-all font-mono text-[11px] text-slate-500 dark:text-zinc-400">
          {{ surfaceTabsDetail || t('admin.settings.contributions.visibilityDetailEmpty') }}
        </p>
      </details>
      <div>
        <UButton icon="i-lucide-rotate-cw" color="neutral" variant="subtle" size="xs" :loading="surfaceTabsPending" @click="refreshSurfaceTabs()">
          {{ t('admin.settings.contributions.retry') }}
        </UButton>
      </div>
    </div>

    <SFAdminFixedTabNav
      :items="tabs"
      :model-value="activeTab"
      :ariaLabel="t('admin.settings.tabs.label')"
      @update:model-value="setActiveTab"
    />
    <KeepAlive>
      <SFAdminSettingsExtensionTab
        v-if="activeSurfaceTab"
        :key="activeTab"
        :tab="activeSurfaceTab"
      />
      <component
        :is="activeComponent"
        v-else-if="activeComponent"
        :key="activeTab"
        :items="adminWebOptions || []"
        @saved="applySavedOptions"
      />
    </KeepAlive>
  </div>
</template>
